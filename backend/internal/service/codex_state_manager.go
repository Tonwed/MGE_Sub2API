package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	CodexStateAutoMintExtraKey      = "codex_state_auto_mint"
	CodexStateDegradedExtraKey      = "codex_state_degraded"
	CodexStateModelsExtraKey        = "codex_state_models"
	CodexStateProxyIDsExtraKey      = "codex_state_proxy_ids"
	CodexStateConcurrencyExtraKey   = "codex_state_concurrency"
	CodexStateRefreshBeforeExtraKey = "codex_state_refresh_before_minutes"
	CodexStateLast292ExtraKey       = "codex_state_last_292_at"
	CodexStateLast312ExtraKey       = "codex_state_last_312_at"
	CodexStateGoodLength            = 292
	CodexStateDegradedLength        = 312
	CodexStateLifetime              = 4 * time.Minute
	CodexStateRefreshBefore         = 2 * time.Minute
	CodexStateMinRefreshDelay       = 30 * time.Second
	codexStateSnapshotCacheKey      = "sub2api:codex-state:v1:"
	codexStateCookieCacheKey        = "sub2api:codex-state:cookies:v1:"
	codexStateCookieCacheTTL        = 10 * time.Minute
	codexStateDefaultRetryCount     = 8
	codexStateRetryDelay            = 3 * time.Second
	CodexStateConcurrencyMin        = 1
	CodexStateConcurrencyMax        = 20
	CodexStateRefreshBeforeMin      = 1
	CodexStateRefreshBeforeMax      = 60
)

var ErrCodexStateNotFound = errors.New("codex turn state not found")

// codexStateAccountExtraMu serializes the reads and writes of Account.Extra
// performed by this manager. Account objects are shared between request
// handlers and background refresh timers, so mutating the map in place races
// with those reads and can abort the whole process.
var codexStateAccountExtraMu sync.Mutex

// codexStateAccountGate reports whether auto-minting is enabled and how early
// to refresh, taking the Extra snapshot under the shared lock.
func codexStateAccountGate(account *Account) (bool, time.Duration) {
	codexStateAccountExtraMu.Lock()
	defer codexStateAccountExtraMu.Unlock()
	enabled, _ := account.Extra[CodexStateAutoMintExtraKey].(bool)
	return enabled, codexStateRefreshBefore(account)
}

// Managed states are selected per turn, not pinned to a pooled WS handshake.
func codexStateTakeoverEnabled(account *Account) bool {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return false
	}
	enabled, _ := account.Extra[CodexStateAutoMintExtraKey].(bool)
	return enabled
}

// CodexStateModelStatus is persisted in accounts.extra under codex_state_models.
type CodexStateModelStatus struct {
	Degraded   bool       `json:"degraded"`
	Last292At  *time.Time `json:"last_292_at,omitempty"`
	Last312At  *time.Time `json:"last_312_at,omitempty"`
	LastMintAt *time.Time `json:"last_mint_at,omitempty"`
}

// CodexTurnStateEntry is the server-side routing token and its lifetime.
type CodexTurnStateEntry struct {
	Value        string    `json:"value"`
	Model        string    `json:"model"`
	AccountID    int64     `json:"account_id"`
	ProxyID      int64     `json:"proxy_id,omitempty"`
	AcquiredAt   time.Time `json:"acquired_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	VerifiedAt   time.Time `json:"verified_at"`
	CookieHeader string    `json:"cookie_header,omitempty"`
}

type codexStateCookieSnapshot struct {
	Cookies   []string  `json:"cookies"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CodexTurnStateSnapshot struct {
	Current  *CodexTurnStateEntry `json:"current,omitempty"`
	Previous *CodexTurnStateEntry `json:"previous,omitempty"`
}

type CodexStateMintRunStatus struct {
	Running       bool       `json:"running"`
	Attempts      int        `json:"attempts"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	NextRetryAt   *time.Time `json:"next_retry_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// CodexStateCache is intentionally independent from GatewayCache so existing
// test doubles do not need to implement the full gateway interface.
type CodexStateCache interface {
	SetCodexState(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	GetCodexState(ctx context.Context, key string) ([]byte, error)
	DeleteCodexState(ctx context.Context, key string) error
}

type CodexStateAccountStore interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type CodexStateProxyProvider interface {
	ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error)
}

type codexStateMemoryCache struct {
	mu      sync.RWMutex
	entries map[string]codexStateMemoryEntry
}

type codexStateMemoryEntry struct {
	payload   []byte
	expiresAt time.Time
}

func newCodexStateMemoryCache() *codexStateMemoryCache {
	return &codexStateMemoryCache{entries: make(map[string]codexStateMemoryEntry)}
}

func (c *codexStateMemoryCache) SetCodexState(_ context.Context, key string, payload []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = codexStateMemoryEntry{
		payload:   append([]byte(nil), payload...),
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

func (c *codexStateMemoryCache) GetCodexState(_ context.Context, key string) ([]byte, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return nil, ErrCodexStateNotFound
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		_ = c.DeleteCodexState(context.Background(), key)
		return nil, ErrCodexStateNotFound
	}
	return append([]byte(nil), entry.payload...), nil
}

func (c *codexStateMemoryCache) DeleteCodexState(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}

type CodexStateManager struct {
	accountStore  CodexStateAccountStore
	proxyProvider CodexStateProxyProvider
	httpDoer      HTTPUpstream
	cache         CodexStateCache
	memory        *codexStateMemoryCache
	now           func() time.Time
	retryDelay    time.Duration

	mu       sync.Mutex
	bgLocks  map[string]*sync.Mutex
	refresh  map[string]*time.Timer
	inflight map[string]bool
	loops    map[string]bool
	cancels  map[string]context.CancelFunc
	runs     map[string]*CodexStateMintRunStatus
}

func NewCodexStateManager(
	accountStore CodexStateAccountStore,
	proxyProvider CodexStateProxyProvider,
	httpDoer HTTPUpstream,
	cache CodexStateCache,
) *CodexStateManager {
	return &CodexStateManager{
		accountStore:  accountStore,
		proxyProvider: proxyProvider,
		httpDoer:      httpDoer,
		cache:         cache,
		memory:        newCodexStateMemoryCache(),
		now:           time.Now,
		retryDelay:    codexStateRetryDelay,
		bgLocks:       make(map[string]*sync.Mutex),
		refresh:       make(map[string]*time.Timer),
		inflight:      make(map[string]bool),
		loops:         make(map[string]bool),
		cancels:       make(map[string]context.CancelFunc),
		runs:          make(map[string]*CodexStateMintRunStatus),
	}
}

func (m *CodexStateManager) SetNowForTest(now func() time.Time) {
	if m != nil && now != nil {
		m.now = now
	}
}

func (m *CodexStateManager) SetRetryDelayForTest(delay time.Duration) {
	if m == nil || delay <= 0 {
		return
	}
	m.retryDelay = delay
}

func (m *CodexStateManager) Enabled(account *Account) bool {
	if m == nil || account == nil || account.Extra == nil {
		return false
	}
	enabled, _ := account.Extra[CodexStateAutoMintExtraKey].(bool)
	return enabled
}

func (m *CodexStateManager) ModelStatus(account *Account, model string) CodexStateModelStatus {
	status := CodexStateModelStatus{}
	if account == nil || account.Extra == nil {
		return status
	}
	raw, ok := account.Extra[CodexStateModelsExtraKey].(map[string]any)
	if !ok {
		return status
	}
	value, ok := raw[normalizeCodexStateModel(model)]
	if !ok {
		return status
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return status
	}
	_ = json.Unmarshal(encoded, &status)
	return status
}

func (m *CodexStateManager) InjectHeader(ctx context.Context, account *Account, model string, headers http.Header) {
	if m == nil || account == nil || headers == nil {
		return
	}
	if !m.Enabled(account) {
		slog.Debug("codex_state.inject_skipped", "account_id", account.ID, "model", model, "reason", "disabled")
		return
	}
	if !codexStateModelManaged(account, model) {
		return
	}
	entry, err := m.Current(ctx, account, model)
	if err != nil || entry == nil || len(entry.Value) != CodexStateGoodLength {
		m.TriggerMint(account, model)
		slog.Debug("codex_state.inject_skipped", "account_id", account.ID, "model", model, "reason", "no_current")
		return
	}
	headers.Set(openAICodexTurnStateHeader, entry.Value)
	if cookieHeader := strings.TrimSpace(entry.CookieHeader); cookieHeader != "" {
		headers.Set("cookie", cookieHeader)
	} else {
		m.injectCookies(ctx, account.ID, headers)
	}
	if entry.ExpiresAt.Sub(m.now()) <= codexStateRefreshBefore(account) {
		m.TriggerMint(account, model)
	}
}

func codexStateCookieNameAllowed(name string) bool {
	switch strings.TrimSpace(name) {
	case "__cf_bm", "__cflb", "__oailb":
		return true
	default:
		return false
	}
}

func codexStateCookiesFromHeaders(headers http.Header) []string {
	if headers == nil {
		return nil
	}
	response := &http.Response{Header: headers}
	out := make([]string, 0, 3)
	for _, cookie := range response.Cookies() {
		if cookie == nil || !codexStateCookieNameAllowed(cookie.Name) || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		out = append(out, cookie.Name+"="+cookie.Value)
	}
	return normalizeCodexStateCookies(out)
}

func normalizeCodexStateCookies(cookies []string) []string {
	values := make(map[string]string, len(cookies))
	for _, raw := range cookies {
		name, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || !codexStateCookieNameAllowed(name) || value == "" {
			continue
		}
		values[name] = value
	}
	out := make([]string, 0, len(values))
	for _, name := range []string{"__cf_bm", "__cflb", "__oailb"} {
		if value := values[name]; value != "" {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func mergeCodexStateCookies(existing, incoming []string) []string {
	return normalizeCodexStateCookies(append(append([]string(nil), existing...), incoming...))
}

func (m *CodexStateManager) loadCookieSnapshot(ctx context.Context, accountID int64) (*codexStateCookieSnapshot, error) {
	if m == nil || accountID <= 0 {
		return nil, ErrCodexStateNotFound
	}
	payload, err := m.cacheGet(ctx, fmt.Sprintf("%s%d", codexStateCookieCacheKey, accountID))
	if err != nil {
		return nil, err
	}
	var snapshot codexStateCookieSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	snapshot.Cookies = normalizeCodexStateCookies(snapshot.Cookies)
	if len(snapshot.Cookies) == 0 || (!snapshot.ExpiresAt.IsZero() && !m.now().Before(snapshot.ExpiresAt)) {
		return nil, ErrCodexStateNotFound
	}
	return &snapshot, nil
}

func (m *CodexStateManager) storeCookies(ctx context.Context, accountID int64, incoming []string) {
	if m == nil || accountID <= 0 {
		return
	}
	incoming = normalizeCodexStateCookies(incoming)
	if len(incoming) == 0 {
		return
	}
	existing, _ := m.loadCookieSnapshot(ctx, accountID)
	var current []string
	if existing != nil {
		current = existing.Cookies
	}
	now := m.now()
	snapshot := &codexStateCookieSnapshot{
		Cookies:   mergeCodexStateCookies(current, incoming),
		UpdatedAt: now,
		ExpiresAt: now.Add(codexStateCookieCacheTTL),
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	_ = m.cacheSet(ctx, fmt.Sprintf("%s%d", codexStateCookieCacheKey, accountID), payload, codexStateCookieCacheTTL)
}

func (m *CodexStateManager) cookieHeader(ctx context.Context, accountID int64) string {
	snapshot, err := m.loadCookieSnapshot(ctx, accountID)
	if err != nil || snapshot == nil || len(snapshot.Cookies) == 0 {
		return ""
	}
	return strings.Join(snapshot.Cookies, "; ")
}

func (m *CodexStateManager) injectCookies(ctx context.Context, accountID int64, headers http.Header) {
	if headers == nil {
		return
	}
	if cookieHeader := m.cookieHeader(ctx, accountID); cookieHeader != "" {
		headers.Set("cookie", cookieHeader)
	}
}

func (m *CodexStateManager) ObserveCookies(ctx context.Context, account *Account, headers http.Header) {
	if m == nil || account == nil {
		return
	}
	m.storeCookies(ctx, account.ID, codexStateCookiesFromHeaders(headers))
}

func (m *CodexStateManager) Observe(ctx context.Context, account *Account, model, state, errorCode string) {
	if m == nil || account == nil {
		return
	}
	if !codexStateModelManaged(account, model) {
		return
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		return
	}
	now := m.now()
	switch len(strings.TrimSpace(state)) {
	case CodexStateGoodLength:
		status := m.ModelStatus(account, model)
		entry := &CodexTurnStateEntry{
			Value:        strings.TrimSpace(state),
			Model:        model,
			AccountID:    account.ID,
			AcquiredAt:   now,
			ExpiresAt:    now.Add(CodexStateLifetime),
			VerifiedAt:   now,
			CookieHeader: m.cookieHeader(ctx, account.ID),
		}
		if err := m.storeSnapshot(ctx, account, &CodexTurnStateSnapshot{Current: entry}); err == nil {
			if status.Degraded || status.Last292At == nil ||
				now.Sub(*status.Last292At) >= time.Minute {
				_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
					status.Degraded = false
					status.Last292At = &now
				})
			}
			m.scheduleRefresh(account, model, entry)
		}
	case CodexStateDegradedLength:
		status := m.ModelStatus(account, model)
		if !status.Degraded || status.Last312At == nil ||
			now.Sub(*status.Last312At) >= time.Minute {
			_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
				status.Degraded = true
				status.Last312At = &now
			})
		}
		m.maybeTriggerMint(ctx, account, model)
	default:
		if strings.TrimSpace(errorCode) == "server_is_overloaded" {
			_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
				status.Degraded = true
				status.Last312At = &now
			})
			m.maybeTriggerMint(ctx, account, model)
		}
	}
}

// MarkDegraded records that upstream rerouted a managed model away from its
// canonical name (for example Astra to Luna). The current turn state is no
// longer trustworthy, so this also requests a fresh one.
func (m *CodexStateManager) MarkDegraded(ctx context.Context, account *Account, model string) {
	if m == nil || account == nil || !codexStateModelManaged(account, model) {
		return
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		return
	}
	now := m.now()
	status := m.ModelStatus(account, model)
	if !status.Degraded || status.Last312At == nil || now.Sub(*status.Last312At) >= time.Minute {
		_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
			status.Degraded = true
			status.Last312At = &now
		})
	}
	m.maybeTriggerMint(ctx, account, model)
}

func (m *CodexStateManager) Current(ctx context.Context, account *Account, model string) (*CodexTurnStateEntry, error) {
	if m == nil || account == nil {
		return nil, ErrCodexStateNotFound
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		return nil, ErrCodexStateNotFound
	}
	snapshot, err := m.loadSnapshot(ctx, account.ID, model)
	if err != nil {
		return nil, err
	}
	now := m.now()
	if snapshot.Current != nil && snapshot.Current.ExpiresAt.After(now) {
		return snapshot.Current, nil
	}
	if snapshot.Previous != nil && snapshot.Previous.ExpiresAt.After(now) {
		snapshot.Current = snapshot.Previous
		snapshot.Previous = nil
		_ = m.persistSnapshot(ctx, account.ID, model, snapshot)
		return snapshot.Current, nil
	}
	return nil, ErrCodexStateNotFound
}

func (m *CodexStateManager) TriggerMint(account *Account, model string) {
	if m == nil || account == nil || !m.Enabled(account) {
		return
	}
	m.startMintLoop(account.ID, model, true)
}

func (m *CodexStateManager) TriggerMintForce(account *Account, model string) {
	if m == nil || account == nil {
		return
	}
	m.startMintLoop(account.ID, model, false)
}

func (m *CodexStateManager) startMintLoop(accountID int64, model string, requireEnabled bool) {
	model = normalizeCodexStateModel(model)
	if model == "" {
		return
	}
	key := codexStateKey(accountID, model)
	now := m.now()
	m.mu.Lock()
	if m.loops[key] {
		m.mu.Unlock()
		return
	}
	m.loops[key] = true
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	m.cancels[key] = cancelLoop
	m.runs[key] = &CodexStateMintRunStatus{
		Running:   true,
		StartedAt: &now,
	}
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.loops, key)
			delete(m.cancels, key)
			m.mu.Unlock()
		}()
		m.runMintLoop(loopCtx, accountID, model, requireEnabled)
	}()
}

func (m *CodexStateManager) runMintLoop(ctx context.Context, accountID int64, model string, requireEnabled bool) {
	key := codexStateKey(accountID, model)
	delay := m.retryDelay
	if delay <= 0 {
		delay = codexStateRetryDelay
	}
	for {
		if ctx.Err() != nil {
			m.finishMintLoop(key, "", false)
			return
		}
		account := (*Account)(nil)
		if m.accountStore != nil {
			if loaded, err := m.accountStore.GetByID(ctx, accountID); err == nil {
				account = loaded
			}
		}
		if account == nil || (requireEnabled && !m.Enabled(account)) {
			m.finishMintLoop(key, "", false)
			return
		}
		if requireEnabled && !codexStateModelManaged(account, model) {
			m.finishMintLoop(key, "", false)
			return
		}
		attemptAt := m.now()
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, 3*time.Minute)
		_, err := m.Mint(attemptCtx, account, model)
		cancelAttempt()
		if ctx.Err() != nil {
			m.finishMintLoop(key, "", false)
			return
		}
		if err == nil {
			m.finishMintLoop(key, "", true)
			return
		}
		nextRetryAt := m.now().Add(delay)
		m.mu.Lock()
		if status := m.runs[key]; status != nil {
			status.Attempts++
			status.LastAttemptAt = &attemptAt
			status.NextRetryAt = &nextRetryAt
			status.LastError = err.Error()
		}
		m.mu.Unlock()
		timer := time.NewTimer(delay)
		<-timer.C
	}
}

func (m *CodexStateManager) StopMint(accountID int64) {
	if m == nil || accountID <= 0 {
		return
	}
	prefix := fmt.Sprintf("%s%d:", codexStateSnapshotCacheKey, accountID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, cancel := range m.cancels {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		cancel()
		if status := m.runs[key]; status != nil {
			status.Running = false
			status.NextRetryAt = nil
			status.LastError = ""
		}
	}
}

func (m *CodexStateManager) finishMintLoop(key, lastError string, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.runs[key]
	if status == nil {
		return
	}
	status.Running = false
	status.NextRetryAt = nil
	status.LastError = lastError
	if success {
		status.LastError = ""
	}
}

func (m *CodexStateManager) MintRunStatus(accountID int64, model string) CodexStateMintRunStatus {
	if m == nil {
		return CodexStateMintRunStatus{}
	}
	key := codexStateKey(accountID, model)
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.runs[key]
	if status == nil {
		return CodexStateMintRunStatus{}
	}
	return *status
}

func (m *CodexStateManager) Snapshot(ctx context.Context, accountID int64, model string) (*CodexTurnStateSnapshot, error) {
	if m == nil {
		return nil, ErrCodexStateNotFound
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		return nil, ErrCodexStateNotFound
	}
	return m.loadSnapshot(ctx, accountID, model)
}

func (m *CodexStateManager) Mint(ctx context.Context, account *Account, model string) (*CodexTurnStateEntry, error) {
	if m == nil || account == nil || m.httpDoer == nil {
		return nil, errors.New("codex state manager is not configured")
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		return nil, errors.New("codex state model is empty")
	}
	key := codexStateKey(account.ID, model)
	m.mu.Lock()
	if m.inflight[key] {
		m.mu.Unlock()
		return nil, errors.New("codex state mint already running")
	}
	m.inflight[key] = true
	m.mu.Unlock()
	now := m.now()
	_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
		status.LastMintAt = &now
	})
	defer func() {
		m.mu.Lock()
		delete(m.inflight, key)
		m.mu.Unlock()
	}()

	proxies, err := m.resolveProxies(ctx, account)
	if err != nil {
		return nil, err
	}
	attempts := len(proxies)
	if len(proxies) == 0 {
		attempts = codexStateDefaultRetryCount
	}
	if len(proxies) == 1 && isRotatingProxy(proxies[0]) {
		attempts = codexStateDefaultRetryCount
	}
	concurrency := codexStateMintConcurrency(account)
	if attempts < concurrency {
		attempts = concurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	roundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	successCh := make(chan *CodexTurnStateEntry, concurrency)
	errCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := range jobs {
				var proxy *Proxy
				if len(proxies) > 0 {
					proxy = &proxies[attempt%len(proxies)]
				}
				entry, err := m.mintOnce(roundCtx, account, model, proxy)
				if err == nil && entry != nil {
					select {
					case successCh <- entry:
						cancel()
					case <-roundCtx.Done():
					}
					return
				}
				if err != nil {
					errCh <- err
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for attempt := 0; attempt < attempts; attempt++ {
			select {
			case jobs <- attempt:
			case <-roundCtx.Done():
				return
			}
		}
	}()
	wg.Wait()
	close(successCh)
	close(errCh)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if entry := <-successCh; entry != nil {
		previous, _ := m.Current(ctx, account, model)
		snapshot := &CodexTurnStateSnapshot{Current: entry, Previous: previous}
		if err := m.storeSnapshot(ctx, account, snapshot); err != nil {
			return nil, err
		}
		now = m.now()
		_ = m.updateStatus(ctx, account, model, func(status *CodexStateModelStatus) {
			status.Degraded = false
			status.Last292At = &now
			status.LastMintAt = &now
		})
		m.scheduleRefresh(account, model, entry)
		return entry, nil
	}
	var lastErr error
	for err := range errCh {
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no usable 292 state acquired")
	}
	return nil, lastErr
}

func (m *CodexStateManager) maybeTriggerMint(ctx context.Context, account *Account, model string) {
	if m == nil || !m.Enabled(account) {
		return
	}
	now := m.now()
	status := m.ModelStatus(account, model)
	if status.LastMintAt != nil && now.Sub(*status.LastMintAt) < time.Minute {
		return
	}
	current, err := m.Current(ctx, account, model)
	if err == nil && current != nil && current.ExpiresAt.Sub(now) > codexStateRefreshBefore(account) {
		return
	}
	m.TriggerMint(account, model)
}

func (m *CodexStateManager) updateStatus(ctx context.Context, account *Account, model string, mutate func(*CodexStateModelStatus)) error {
	if account == nil || m.accountStore == nil {
		return errors.New("account store is not configured")
	}
	model = normalizeCodexStateModel(model)
	statuses := cloneAnyMap(account.Extra[CodexStateModelsExtraKey])
	status := CodexStateModelStatus{}
	if raw, ok := statuses[model]; ok {
		encoded, _ := json.Marshal(raw)
		_ = json.Unmarshal(encoded, &status)
	}
	mutate(&status)
	statuses[model] = status
	aggregate := false
	for _, raw := range statuses {
		encoded, _ := json.Marshal(raw)
		var item CodexStateModelStatus
		if json.Unmarshal(encoded, &item) == nil && item.Degraded {
			aggregate = true
			break
		}
	}
	updates := map[string]any{
		CodexStateModelsExtraKey:   statuses,
		CodexStateDegradedExtraKey: aggregate,
	}
	if status.Last292At != nil {
		updates[CodexStateLast292ExtraKey] = status.Last292At.UTC().Format(time.RFC3339)
	}
	if status.Last312At != nil {
		updates[CodexStateLast312ExtraKey] = status.Last312At.UTC().Format(time.RFC3339)
	}
	if err := m.accountStore.UpdateExtra(ctx, account.ID, updates); err != nil {
		return err
	}
	codexStateAccountExtraMu.Lock()
	next := make(map[string]any, len(account.Extra)+len(updates))
	for key, value := range account.Extra {
		next[key] = value
	}
	for key, value := range updates {
		next[key] = value
	}
	account.Extra = next
	codexStateAccountExtraMu.Unlock()
	return nil
}

func (m *CodexStateManager) resolveProxies(ctx context.Context, account *Account) ([]Proxy, error) {
	if m.proxyProvider == nil || account == nil {
		return nil, nil
	}
	ids := codexStateProxyIDs(account)
	if len(ids) == 0 {
		return nil, nil
	}
	proxies, err := m.proxyProvider.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := m.now()
	out := make([]Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if !proxy.IsActive() || proxy.IsExpired(now) {
			continue
		}
		out = append(out, proxy)
	}
	if len(ids) > 0 && len(out) == 0 {
		return nil, errors.New("no active codex state proxy is available")
	}
	return out, nil
}

func (m *CodexStateManager) loadSnapshot(ctx context.Context, accountID int64, model string) (*CodexTurnStateSnapshot, error) {
	key := codexStateKey(accountID, model)
	payload, err := m.cacheGet(ctx, key)
	if err != nil {
		return nil, err
	}
	var snapshot CodexTurnStateSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (m *CodexStateManager) storeSnapshot(ctx context.Context, account *Account, snapshot *CodexTurnStateSnapshot) error {
	if account == nil || snapshot == nil || snapshot.Current == nil {
		return errors.New("invalid codex state snapshot")
	}
	return m.persistSnapshot(ctx, account.ID, snapshot.Current.Model, snapshot)
}

func (m *CodexStateManager) persistSnapshot(ctx context.Context, accountID int64, model string, snapshot *CodexTurnStateSnapshot) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	ttl := CodexStateLifetime + CodexStateRefreshBefore
	if snapshot.Previous != nil {
		if until := snapshot.Previous.ExpiresAt.Sub(m.now()); until > ttl {
			ttl = until
		}
	}
	return m.cacheSet(ctx, codexStateKey(accountID, model), payload, ttl)
}

func (m *CodexStateManager) cacheGet(ctx context.Context, key string) ([]byte, error) {
	if m.cache != nil {
		return m.cache.GetCodexState(ctx, key)
	}
	return m.memory.GetCodexState(ctx, key)
}

func (m *CodexStateManager) cacheSet(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if m.cache != nil {
		return m.cache.SetCodexState(ctx, key, payload, ttl)
	}
	return m.memory.SetCodexState(ctx, key, payload, ttl)
}

func (m *CodexStateManager) scheduleRefresh(account *Account, model string, entry *CodexTurnStateEntry) {
	if m == nil || account == nil || entry == nil {
		return
	}
	key := codexStateKey(account.ID, model)
	delay := entry.ExpiresAt.Sub(m.now()) - codexStateRefreshBefore(account)
	if delay < CodexStateMinRefreshDelay {
		delay = CodexStateMinRefreshDelay
	}
	m.mu.Lock()
	if old := m.refresh[key]; old != nil {
		old.Stop()
	}
	m.refresh[key] = time.AfterFunc(delay, func() {
		ctx := context.Background()
		latest := account
		if m.accountStore != nil {
			if loaded, err := m.accountStore.GetByID(ctx, account.ID); err == nil && loaded != nil {
				latest = loaded
			}
		}
		if enabled, refreshBefore := codexStateAccountGate(latest); enabled {
			current, err := m.Current(ctx, latest, model)
			if err == nil && current != nil && current.ExpiresAt.Sub(m.now()) <= refreshBefore {
				m.TriggerMint(latest, model)
			}
		}
		m.mu.Lock()
		delete(m.refresh, key)
		m.mu.Unlock()
	})
	m.mu.Unlock()
}

func codexStateProxyIDs(account *Account) []int64 {
	if account == nil || account.Extra == nil {
		return nil
	}
	if account.ProxyID != nil {
		if raw, ok := account.Extra[CodexStateProxyIDsExtraKey]; !ok || len(anySlice(raw)) == 0 {
			return []int64{*account.ProxyID}
		}
	}
	raw := anySlice(account.Extra[CodexStateProxyIDsExtraKey])
	out := make([]int64, 0, len(raw))
	for _, value := range raw {
		switch typed := value.(type) {
		case int:
			out = append(out, int64(typed))
		case int64:
			out = append(out, typed)
		case float64:
			out = append(out, int64(typed))
		case json.Number:
			if id, err := typed.Int64(); err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

func codexStateMintConcurrency(account *Account) int {
	if account == nil || account.Extra == nil {
		return CodexStateConcurrencyMin
	}
	value := CodexStateConcurrencyMin
	switch typed := account.Extra[CodexStateConcurrencyExtraKey].(type) {
	case int:
		value = typed
	case int64:
		value = int(typed)
	case float64:
		value = int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			value = int(parsed)
		}
	}
	if value < CodexStateConcurrencyMin {
		return CodexStateConcurrencyMin
	}
	if value > CodexStateConcurrencyMax {
		return CodexStateConcurrencyMax
	}
	return value
}

func codexStateRefreshBefore(account *Account) time.Duration {
	minutes := int(CodexStateRefreshBefore / time.Minute)
	if account != nil && account.Extra != nil {
		switch typed := account.Extra[CodexStateRefreshBeforeExtraKey].(type) {
		case int:
			minutes = typed
		case int64:
			minutes = int(typed)
		case float64:
			minutes = int(typed)
		case json.Number:
			if parsed, err := typed.Int64(); err == nil {
				minutes = int(parsed)
			}
		}
	}
	if minutes < CodexStateRefreshBeforeMin {
		minutes = CodexStateRefreshBeforeMin
	}
	if minutes > CodexStateRefreshBeforeMax {
		minutes = CodexStateRefreshBeforeMax
	}
	if maxMinutes := int(CodexStateRefreshBefore / time.Minute); maxMinutes > 0 && minutes > maxMinutes {
		minutes = maxMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []int64:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func cloneAnyMap(value any) map[string]any {
	out := make(map[string]any)
	raw, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for key, item := range raw {
		out[key] = item
	}
	return out
}

func normalizeCodexStateModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func codexStateModelManaged(account *Account, model string) bool {
	model = normalizeCodexStateModel(model)
	if model == "" {
		return false
	}
	models := codexStateConfiguredModels(account)
	if len(models) == 0 {
		return model == normalizeCodexStateModel(CodexStateDefaultModel)
	}
	for _, configured := range models {
		if normalizeCodexStateModel(configured) == model {
			return true
		}
	}
	return false
}

func codexStateKey(accountID int64, model string) string {
	return fmt.Sprintf("%s%d:%s", codexStateSnapshotCacheKey, accountID, normalizeCodexStateModel(model))
}

func isRotatingProxy(proxy Proxy) bool {
	value := strings.ToLower(proxy.Username + " " + proxy.Name + " " + proxy.Host)
	return strings.Contains(value, "region-rand") || strings.Contains(value, "rotating") || strings.Contains(value, "rotate")
}
