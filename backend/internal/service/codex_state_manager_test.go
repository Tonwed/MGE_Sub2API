package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type codexStateTestAccountStore struct {
	account *Account
}

func (s *codexStateTestAccountStore) GetByID(_ context.Context, _ int64) (*Account, error) {
	return s.account, nil
}

func (s *codexStateTestAccountStore) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if s.account.Extra == nil {
		s.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		s.account.Extra[key] = value
	}
	return nil
}

type codexStateTestProxyProvider struct {
	proxies []Proxy
}

func (p codexStateTestProxyProvider) ListByIDs(_ context.Context, _ []int64) ([]Proxy, error) {
	return p.proxies, nil
}

type codexStateTestHTTPUpstream struct {
	mu          sync.Mutex
	requests    int
	lastHeaders http.Header
	lastBody    []byte
}

type codexStateFlakyHTTPUpstream struct {
	mu       sync.Mutex
	requests int
	failures int
}

type codexStateConcurrencyHTTPUpstream struct {
	mu     sync.Mutex
	active int
	max    int
}

type codexStateBlockingHTTPUpstream struct {
	started chan struct{}
}

func (u *codexStateBlockingHTTPUpstream) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	select {
	case u.started <- struct{}{}:
	default:
	}
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func (u *codexStateBlockingHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func (u *codexStateConcurrencyHTTPUpstream) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	u.mu.Lock()
	u.active++
	if u.active > u.max {
		u.max = u.active
	}
	u.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	u.mu.Lock()
	u.active--
	u.mu.Unlock()
	header := make(http.Header)
	header.Set(openAICodexTurnStateHeader, strings.Repeat("x", CodexStateDegradedLength))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n")),
		Request:    request,
	}, nil
}

func (u *codexStateConcurrencyHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func (u *codexStateFlakyHTTPUpstream) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	u.mu.Lock()
	u.requests++
	current := u.requests
	u.mu.Unlock()
	header := make(http.Header)
	if current <= u.failures {
		header.Set(openAICodexTurnStateHeader, strings.Repeat("x", CodexStateDegradedLength))
	} else {
		header.Set(openAICodexTurnStateHeader, strings.Repeat("y", CodexStateGoodLength))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n")),
		Request:    request,
	}, nil
}

func (u *codexStateFlakyHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func (u *codexStateTestHTTPUpstream) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	u.mu.Lock()
	u.requests++
	requestNumber := u.requests
	u.lastHeaders = request.Header.Clone()
	u.lastBody, _ = io.ReadAll(request.Body)
	u.mu.Unlock()
	header := make(http.Header)
	body := "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n"
	switch requestNumber % 3 {
	case 1:
		header.Set(openAICodexTurnStateHeader, strings.Repeat("a", CodexStateDegradedLength))
	case 2:
		stateByte := byte('b' + (requestNumber/3)%20)
		header.Set(openAICodexTurnStateHeader, strings.Repeat(string(stateByte), CodexStateGoodLength))
	}
	if request.Header.Get(openAICodexTurnStateHeader) != "" {
		header.Del(openAICodexTurnStateHeader)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func (u *codexStateTestHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func TestCodexStateManagerMintRetryObserveAndInject(t *testing.T) {
	account := &Account{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Extra: map[string]any{
			CodexStateAutoMintExtraKey: true,
			CodexStateProxyIDsExtraKey: []any{1},
		},
		Credentials: map[string]any{
			"access_token":       "test-token",
			"chatgpt_account_id": "test-account",
		},
		Concurrency: 2,
	}
	store := &codexStateTestAccountStore{account: account}
	provider := codexStateTestProxyProvider{proxies: []Proxy{{
		ID:       1,
		Name:     "rotating",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user-region-Rand",
		Password: "test",
		Status:   StatusActive,
	}}}
	upstream := &codexStateTestHTTPUpstream{}
	manager := NewCodexStateManager(store, provider, upstream, nil)

	entry, err := manager.Mint(context.Background(), account, "gpt-6-astra")
	require.NoError(t, err)
	require.Len(t, entry.Value, CodexStateGoodLength)

	headers := make(http.Header)
	manager.InjectHeader(context.Background(), account, "gpt-6-astra", headers)
	require.Equal(t, entry.Value, headers.Get(openAICodexTurnStateHeader))

	manager.Observe(context.Background(), account, "gpt-6-astra", strings.Repeat("x", CodexStateDegradedLength), "")
	require.True(t, manager.ModelStatus(account, "gpt-6-astra").Degraded)
	manager.Observe(context.Background(), account, "gpt-6-astra", entry.Value, "")
	require.False(t, manager.ModelStatus(account, "gpt-6-astra").Degraded)
}

func TestCodexStateManagerMarkDegradedFlagsModel(t *testing.T) {
	account := &Account{
		ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "a", "chatgpt_account_id": "a"},
		Extra:       map[string]any{CodexStateAutoMintExtraKey: true},
	}
	upstream := &codexStateTestHTTPUpstream{}
	manager := NewCodexStateManager(&codexStateTestAccountStore{account: account}, nil, upstream, nil)

	manager.MarkDegraded(context.Background(), account, "gpt-6-astra")

	status := manager.ModelStatus(account, "gpt-6-astra")
	require.True(t, status.Degraded)
	require.NotNil(t, status.Last312At)

	// Unmanaged models are ignored.
	manager.MarkDegraded(context.Background(), account, "gpt-5-codex")
	require.False(t, manager.ModelStatus(account, "gpt-5-codex").Degraded)
}

func TestCodexStateManagerKeepsAccountsIsolated(t *testing.T) {
	upstream := &codexStateTestHTTPUpstream{}
	accountA := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "a", "chatgpt_account_id": "a"},
		Extra:       map[string]any{CodexStateAutoMintExtraKey: true},
	}
	accountB := &Account{
		ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "b", "chatgpt_account_id": "b"},
		Extra:       map[string]any{CodexStateAutoMintExtraKey: true},
	}
	store := &codexStateTestAccountStore{account: accountA}
	manager := NewCodexStateManager(store, codexStateTestProxyProvider{}, upstream, nil)

	entryA, err := manager.Mint(context.Background(), accountA, "gpt-6-astra")
	require.NoError(t, err)
	store.account = accountB
	entryB, err := manager.Mint(context.Background(), accountB, "gpt-6-astra")
	require.NoError(t, err)
	require.NotEqual(t, entryA.Value, entryB.Value)

	currentA, err := manager.Current(context.Background(), accountA, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, entryA.Value, currentA.Value)
	currentB, err := manager.Current(context.Background(), accountB, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, entryB.Value, currentB.Value)

	_, err = manager.Current(context.Background(), accountA, "gpt-6-terra")
	require.ErrorIs(t, err, ErrCodexStateNotFound, "state must stay isolated by model")

	proxyID := int64(99)
	accountA.ProxyID = &proxyID
	currentAAfterProxyChange, err := manager.Current(context.Background(), accountA, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, entryA.Value, currentAAfterProxyChange.Value, "same account/model state is reusable across proxy changes")
	require.Equal(t, CodexStateLifetime, entryA.ExpiresAt.Sub(entryA.AcquiredAt))
}

func TestCodexStateManagerRetriesUntilSuccess(t *testing.T) {
	account := &Account{
		ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "token", "chatgpt_account_id": "account"},
		Extra:       map[string]any{CodexStateAutoMintExtraKey: true},
	}
	store := &codexStateTestAccountStore{account: account}
	upstream := &codexStateFlakyHTTPUpstream{failures: 9}
	manager := NewCodexStateManager(store, codexStateTestProxyProvider{}, upstream, nil)
	manager.SetRetryDelayForTest(10 * time.Millisecond)

	manager.TriggerMintForce(account, "gpt-6-astra")

	require.Eventually(t, func() bool {
		entry, err := manager.Current(context.Background(), account, "gpt-6-astra")
		return err == nil && entry != nil && entry.Value == strings.Repeat("y", CodexStateGoodLength)
	}, 2*time.Second, 20*time.Millisecond)
	status := manager.MintRunStatus(account.ID, "gpt-6-astra")
	require.False(t, status.Running)
	require.GreaterOrEqual(t, status.Attempts, 1)
}

func TestCodexStateMintConcurrencyClampsConfiguredValue(t *testing.T) {
	require.Equal(t, CodexStateConcurrencyMin, codexStateMintConcurrency(&Account{}))
	require.Equal(t, CodexStateConcurrencyMin, codexStateMintConcurrency(&Account{Extra: map[string]any{
		CodexStateConcurrencyExtraKey: -1,
	}}))
	require.Equal(t, 7, codexStateMintConcurrency(&Account{Extra: map[string]any{
		CodexStateConcurrencyExtraKey: 7,
	}}))
	require.Equal(t, CodexStateConcurrencyMax, codexStateMintConcurrency(&Account{Extra: map[string]any{
		CodexStateConcurrencyExtraKey: 99,
	}}))
}

func TestCodexStateRefreshBeforeClampsConfiguredValue(t *testing.T) {
	require.Equal(t, CodexStateRefreshBefore, codexStateRefreshBefore(&Account{}))
	require.Equal(t, time.Minute, codexStateRefreshBefore(&Account{Extra: map[string]any{
		CodexStateRefreshBeforeExtraKey: 0,
	}}))
	require.Equal(t, 2*time.Minute, codexStateRefreshBefore(&Account{Extra: map[string]any{
		CodexStateRefreshBeforeExtraKey: 15,
	}}))
	require.Equal(t, 2*time.Minute, codexStateRefreshBefore(&Account{Extra: map[string]any{
		CodexStateRefreshBeforeExtraKey: 99,
	}}))
}

func TestCodexStateManagerUsesConfiguredConcurrency(t *testing.T) {
	account := &Account{
		ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "token", "chatgpt_account_id": "account"},
		Extra: map[string]any{
			CodexStateAutoMintExtraKey:    true,
			CodexStateConcurrencyExtraKey: 3,
			CodexStateProxyIDsExtraKey:    []any{1},
		},
	}
	store := &codexStateTestAccountStore{account: account}
	provider := codexStateTestProxyProvider{proxies: []Proxy{{
		ID:       1,
		Name:     "single",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     1,
		Status:   StatusActive,
	}}}
	upstream := &codexStateConcurrencyHTTPUpstream{}
	manager := NewCodexStateManager(store, provider, upstream, nil)

	_, err := manager.Mint(context.Background(), account, "gpt-6-astra")
	require.Error(t, err)
	upstream.mu.Lock()
	maxConcurrent := upstream.max
	upstream.mu.Unlock()
	require.GreaterOrEqual(t, maxConcurrent, 2)
}

func TestCodexStateManagerStopMintCancelsRunningAttempts(t *testing.T) {
	account := &Account{
		ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "token", "chatgpt_account_id": "account"},
		Extra: map[string]any{
			CodexStateAutoMintExtraKey: true,
			CodexStateProxyIDsExtraKey: []any{1},
		},
	}
	store := &codexStateTestAccountStore{account: account}
	provider := codexStateTestProxyProvider{proxies: []Proxy{{
		ID:       1,
		Name:     "single",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     1,
		Status:   StatusActive,
	}}}
	upstream := &codexStateBlockingHTTPUpstream{started: make(chan struct{}, 1)}
	manager := NewCodexStateManager(store, provider, upstream, nil)
	manager.SetRetryDelayForTest(time.Hour)

	manager.TriggerMint(account, "gpt-6-astra")
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("mint attempt did not start")
	}

	manager.StopMint(account.ID)
	require.Eventually(t, func() bool {
		return !manager.MintRunStatus(account.ID, "gpt-6-astra").Running
	}, time.Second, 10*time.Millisecond)
}

func TestCodexStateTakeoverOverridesAndRefreshesClientState(t *testing.T) {
	account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{
			CodexStateAutoMintExtraKey: true,
			CodexStateModelsExtraKey:   map[string]any{"test-model": map[string]any{}},
		}}
	manager := NewCodexStateManager(nil, nil, nil, nil)
	ctx := context.Background()
	entry := &CodexTurnStateEntry{AccountID: account.ID, Model: "test-model",
		Value: strings.Repeat("a", 292), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, manager.storeSnapshot(ctx, account, &CodexTurnStateSnapshot{Current: entry}))
	headers := make(http.Header)
	headers.Set(openAICodexTurnStateHeader, strings.Repeat("x", 312))
	manager.InjectHeader(ctx, account, "test-model", headers)
	require.Equal(t, entry.Value, headers.Get(openAICodexTurnStateHeader))

	entry.Value = strings.Repeat("b", 292)
	require.NoError(t, manager.storeSnapshot(ctx, account, &CodexTurnStateSnapshot{Current: entry}))
	manager.InjectHeader(ctx, account, "test-model", headers)
	require.Equal(t, entry.Value, headers.Get(openAICodexTurnStateHeader))

	account.Extra[CodexStateAutoMintExtraKey] = false
	headers.Set(openAICodexTurnStateHeader, "client-state")
	manager.InjectHeader(ctx, account, "test-model", headers)
	require.Equal(t, "client-state", headers.Get(openAICodexTurnStateHeader))
}

func TestCodexStateTakeoverRoutesWebSocketsThroughHTTP(t *testing.T) {
	account := &Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{CodexStateAutoMintExtraKey: true}}
	svc := &OpenAIGatewayService{}
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(account, 1, "resp_previous"))
	require.True(t, svc.shouldBridgeOpenAIWSPassthroughFirstMessage(account, []byte(`{"type":"response.create"}`)))
	decision := NewOpenAIWSProtocolResolver(nil).Resolve(account)
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, decision.Transport)
	require.Equal(t, "codex_state_takeover", decision.Reason)
	account.Extra[CodexStateAutoMintExtraKey] = false
	require.False(t, codexStateTakeoverEnabled(account))
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(account, 1, ""))
}

func TestCodexStateModelManagedLimitsAutomaticTakeover(t *testing.T) {
	account := &Account{Extra: map[string]any{
		CodexStateModelsExtraKey: map[string]any{"gpt-6-astra": map[string]any{}},
	}}
	require.True(t, codexStateModelManaged(account, "GPT-6-ASTRA"))
	require.False(t, codexStateModelManaged(account, "gpt-5.6-luna"))
	require.False(t, codexStateModelManaged(&Account{}, "gpt-5.6-luna"))
	require.True(t, codexStateModelManaged(&Account{}, CodexStateDefaultModel))
}

func TestCodexStateCookiePoolIsAccountScopedAndMerged(t *testing.T) {
	manager := NewCodexStateManager(nil, nil, nil, nil)
	account := &Account{ID: 77}
	headers := make(http.Header)
	headers.Add("Set-Cookie", "__cf_bm=cf-old; Path=/; Secure")
	headers.Add("Set-Cookie", "__cflb=flb-old; Path=/; Secure")
	headers.Add("Set-Cookie", "ignored=value; Path=/")
	manager.ObserveCookies(context.Background(), account, headers)

	headers = make(http.Header)
	headers.Add("Set-Cookie", "__cf_bm=cf-new; Path=/; Secure")
	headers.Add("Set-Cookie", "__oailb=oai-new; Path=/; Secure")
	manager.ObserveCookies(context.Background(), account, headers)

	requestHeaders := make(http.Header)
	manager.injectCookies(context.Background(), account.ID, requestHeaders)
	cookieHeader := requestHeaders.Get("Cookie")
	require.Contains(t, cookieHeader, "__cf_bm=cf-new")
	require.Contains(t, cookieHeader, "__cflb=flb-old")
	require.Contains(t, cookieHeader, "__oailb=oai-new")
	require.NotContains(t, cookieHeader, "ignored=")

	otherAccountHeaders := make(http.Header)
	manager.injectCookies(context.Background(), 78, otherAccountHeaders)
	require.Empty(t, otherAccountHeaders.Get("Cookie"))
}

func TestExtractCodexStateCompletedModel(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n")
	require.Equal(t, "gpt-6-astra", extractCodexStateCompletedModel(body))
	require.Empty(t, extractCodexStateCompletedModel([]byte("data: {\"type\":\"response.created\"}\n\n")))
}

func TestCodexStateProbeUsesCompleteCodexWireIdentity(t *testing.T) {
	account := &Account{
		ID:       91,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "token",
			"chatgpt_account_id": "account",
		},
	}
	upstream := &codexStateTestHTTPUpstream{}
	manager := NewCodexStateManager(nil, nil, upstream, nil)

	_, err := manager.probeOnce(context.Background(), account, "gpt-6-astra", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastHeaders)

	for _, name := range []string{"session-id", "thread-id", "x-client-request-id", "x-codex-window-id"} {
		value := upstream.lastHeaders.Get(name)
		parsed, parseErr := uuid.Parse(value)
		require.NoError(t, parseErr, name)
		require.Equal(t, uuid.Version(7), parsed.Version(), name)
	}
	installation, err := uuid.Parse(upstream.lastHeaders.Get("x-codex-installation-id"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), installation.Version())
	require.Equal(t, upstream.lastHeaders.Get("session-id"), upstream.lastHeaders.Get("session_id"))
	require.NotEqual(t, upstream.lastHeaders.Get("thread-id"), upstream.lastHeaders.Get("x-client-request-id"))

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(upstream.lastHeaders.Get("x-codex-turn-metadata")), &metadata))
	require.Equal(t, upstream.lastHeaders.Get("session-id"), metadata["session_id"])
	require.Equal(t, upstream.lastHeaders.Get("thread-id"), metadata["thread_id"])
	require.Equal(t, upstream.lastHeaders.Get("x-codex-window-id"), metadata["window_id"])
	require.Contains(t, string(upstream.lastBody), `"prompt_cache_key"`)
	require.Contains(t, string(upstream.lastBody), `"client_metadata"`)
}
