//go:build codex_state_live

package service_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type liveAccountStore struct {
	mu       sync.Mutex
	accounts map[int64]*service.Account
}

func (s *liveAccountStore) GetByID(_ context.Context, id int64) (*service.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accounts[id], nil
}

func (s *liveAccountStore) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[id]
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	return nil
}

type liveProxyProvider struct {
	proxy service.Proxy
}

func (p liveProxyProvider) ListByIDs(_ context.Context, _ []int64) ([]service.Proxy, error) {
	if p.proxy.Host == "" {
		return nil, nil
	}
	return []service.Proxy{p.proxy}, nil
}

type liveAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func loadLiveAccount(t *testing.T, id int64, path string) *service.Account {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file %s: %v", path, err)
	}
	var auth liveAuthFile
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatalf("parse auth file %s: %v", path, err)
	}
	if auth.Tokens.AccessToken == "" || auth.Tokens.AccountID == "" {
		t.Fatalf("auth file %s is missing OAuth token data", path)
	}
	return &service.Account{
		ID:       id,
		Name:     "live-" + strconv.FormatInt(id, 10),
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"access_token":       auth.Tokens.AccessToken,
			"chatgpt_account_id": auth.Tokens.AccountID,
		},
		Extra: map[string]any{
			service.CodexStateAutoMintExtraKey: true,
		},
		Concurrency: 5,
	}
}

func parseLiveProxy(t *testing.T, raw string) service.Proxy {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return service.Proxy{}
	}
	if !strings.Contains(raw, "://") {
		parts := strings.SplitN(raw, ":", 4)
		if len(parts) != 4 {
			t.Fatalf("proxy must be URL or host:port:username:password")
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("invalid proxy port: %v", err)
		}
		return service.Proxy{
			ID:       1,
			Name:     "live-codex-state-rotating",
			Protocol: "http",
			Host:     parts[0],
			Port:     port,
			Username: parts[2],
			Password: parts[3],
			Status:   service.StatusActive,
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	port, _ := strconv.Atoi(parsed.Port())
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	return service.Proxy{
		ID:       1,
		Name:     "live-codex-state-rotating",
		Protocol: parsed.Scheme,
		Host:     parsed.Hostname(),
		Port:     port,
		Username: username,
		Password: password,
		Status:   service.StatusActive,
	}
}

func TestLiveCodexStateManagerTwoAccounts(t *testing.T) {
	authA := strings.TrimSpace(os.Getenv("CODEX_STATE_AUTH_A"))
	authB := strings.TrimSpace(os.Getenv("CODEX_STATE_AUTH_B"))
	if authA == "" || authB == "" {
		t.Skip("set CODEX_STATE_AUTH_A and CODEX_STATE_AUTH_B")
	}
	accountA := loadLiveAccount(t, 101, authA)
	accountB := loadLiveAccount(t, 102, authB)
	store := &liveAccountStore{accounts: map[int64]*service.Account{
		accountA.ID: accountA,
		accountB.ID: accountB,
	}}
	proxy := parseLiveProxy(t, os.Getenv("CODEX_STATE_PROXY"))
	provider := liveProxyProvider{proxy: proxy}
	if proxy.Host != "" {
		for _, account := range []*service.Account{accountA, accountB} {
			account.Extra[service.CodexStateProxyIDsExtraKey] = []any{proxy.ID}
		}
	}
	upstream := repository.NewHTTPUpstream(&config.Config{})
	manager := service.NewCodexStateManager(store, provider, upstream, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("minting account B")
	stateB, err := manager.Mint(ctx, accountB, "gpt-6-astra")
	if err != nil {
		t.Fatalf("mint account B: %v", err)
	}
	t.Logf("minted account B state length=%d", len(stateB.Value))
	currentB, err := manager.Current(ctx, accountB, "gpt-6-astra")
	if err != nil || currentB == nil || currentB.Value != stateB.Value {
		t.Fatalf("account B state was not cached: %v", err)
	}

	t.Log("minting account A")
	stateA, err := manager.Mint(ctx, accountA, "gpt-6-astra")
	if err != nil {
		t.Logf("account A proxy mint unavailable; continuing with degraded-state checks: %v", err)
	} else {
		t.Logf("minted account A state length=%d", len(stateA.Value))
		if stateA.Value == stateB.Value {
			t.Fatal("different accounts unexpectedly returned the same state")
		}
		currentA, err := manager.Current(ctx, accountA, "gpt-6-astra")
		if err != nil || currentA == nil || currentA.Value != stateA.Value {
			t.Fatalf("account A state was not cached: %v", err)
		}
	}

	manager.Observe(ctx, accountA, "gpt-6-astra", strings.Repeat("x", service.CodexStateDegradedLength), "")
	statusA := manager.ModelStatus(accountA, "gpt-6-astra")
	if !statusA.Degraded {
		t.Fatal("account A was not marked degraded after 312")
	}
	recoveryState := ""
	if stateA != nil {
		recoveryState = stateA.Value
	}
	if recoveryState == "" {
		recoveryState = stateB.Value
	}
	manager.Observe(ctx, accountA, "gpt-6-astra", recoveryState, "")
	statusA = manager.ModelStatus(accountA, "gpt-6-astra")
	if statusA.Degraded {
		t.Fatal("account A stayed degraded after a 292 observation")
	}
}
