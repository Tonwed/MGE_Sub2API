package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const codexStateCompactionInstructions = "You are a helpful coding assistant."
const codexStateProbeMinVersion = "0.155.0-alpha.2.6"
const codexStateProbeRequestTimeout = 90 * time.Second

type codexStateProbeResult struct {
	State      string
	StatusCode int
	ErrorCode  string
	ErrorBody  string
}

type codexStateProbeWireIdentity struct {
	InstallationID    string
	SessionID         string
	ThreadID          string
	TurnID            string
	WindowID          string
	RequestID         string
	TurnStartedAtUnix int64
}

func newCodexStateProbeWireIdentity(account *Account, model string) codexStateProbeWireIdentity {
	seed := strconv.FormatInt(account.ID, 10) + ":" + normalizeCodexStateModel(model)
	sessionID := deriveStableUUIDv7("sub2api:codex-state:session:v2:" + seed)
	threadID := deriveStableUUIDv7("sub2api:codex-state:thread:v2:" + seed)
	return codexStateProbeWireIdentity{
		InstallationID:    deriveStableUUIDv4("sub2api:codex-state:installation:v2:" + seed),
		SessionID:         sessionID,
		ThreadID:          threadID,
		TurnID:            uuid.Must(uuid.NewV7()).String(),
		WindowID:          deriveStableUUIDv7("sub2api:codex-state:window:v2:" + threadID),
		RequestID:         uuid.Must(uuid.NewV7()).String(),
		TurnStartedAtUnix: time.Now().UnixMilli(),
	}
}

func (id codexStateProbeWireIdentity) turnMetadata() string {
	raw, _ := json.Marshal(map[string]any{
		"installation_id":         id.InstallationID,
		"session_id":              id.SessionID,
		"thread_id":               id.ThreadID,
		"turn_id":                 id.TurnID,
		"window_id":               id.WindowID,
		"turn_started_at_unix_ms": id.TurnStartedAtUnix,
	})
	return string(raw)
}

func (m *CodexStateManager) mintOnce(ctx context.Context, account *Account, model string, proxy *Proxy) (*CodexTurnStateEntry, error) {
	proxyURL := ""
	proxyID := int64(0)
	if proxy != nil {
		proxyURL = proxy.URL()
		proxyID = proxy.ID
	}
	first, err := m.probeOnce(ctx, account, model, proxyURL, "")
	if err != nil {
		return nil, err
	}
	if first.StatusCode != http.StatusOK || first.ErrorCode != "" || len(first.State) != CodexStateGoodLength {
		return nil, fmt.Errorf(
			"state probe failed: status=%d state_len=%d error=%s body=%s",
			first.StatusCode,
			len(first.State),
			first.ErrorCode,
			first.ErrorBody,
		)
	}
	verified, err := m.probeOnce(ctx, account, model, proxyURL, first.State)
	if err != nil {
		return nil, err
	}
	if verified.StatusCode != http.StatusOK || verified.ErrorCode != "" {
		return nil, fmt.Errorf(
			"state replay failed: status=%d error=%s body=%s",
			verified.StatusCode,
			verified.ErrorCode,
			verified.ErrorBody,
		)
	}
	now := m.now()
	return &CodexTurnStateEntry{
		Value:      first.State,
		Model:      normalizeCodexStateModel(model),
		AccountID:  account.ID,
		ProxyID:    proxyID,
		AcquiredAt: now,
		ExpiresAt:  now.Add(CodexStateLifetime),
		VerifiedAt: now,
	}, nil
}

func (m *CodexStateManager) probeOnce(
	ctx context.Context,
	account *Account,
	model string,
	proxyURL string,
	state string,
) (codexStateProbeResult, error) {
	model = normalizeCodexStateModel(model)
	if account == nil {
		return codexStateProbeResult{}, errors.New("account is nil")
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexStateProbeRequestTimeout)
	defer cancel()
	accessToken := strings.TrimSpace(account.GetOpenAIAccessToken())
	if accessToken == "" {
		return codexStateProbeResult{}, errors.New("openai access token is empty")
	}
	wire := newCodexStateProbeWireIdentity(account, model)
	turnMetadata := wire.turnMetadata()
	payload := map[string]any{
		"model":        model,
		"instructions": codexStateCompactionInstructions,
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
			map[string]any{"type": "compaction_trigger"},
		},
		"store":            false,
		"stream":           true,
		"prompt_cache_key": wire.SessionID,
		"reasoning":        map[string]any{"effort": "low"},
		"include":          []any{},
		"tools":            []any{},
		"client_metadata": map[string]any{
			"x-codex-installation-id": wire.InstallationID,
			"session_id":              wire.SessionID,
			"thread_id":               wire.ThreadID,
			"turn_id":                 wire.TurnID,
			"x-codex-window-id":       wire.WindowID,
			"x-codex-turn-metadata":   turnMetadata,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return codexStateProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(
		probeCtx,
		http.MethodPost,
		chatgptCodexURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return codexStateProbeResult{}, err
	}
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("accept-encoding", openAICodexAcceptEncoding)
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("authorization", "Bearer "+accessToken)
	req.Header.Set("chatgpt-account-id", account.GetChatGPTAccountID())
	req.Header.Set("conversation_id", wire.SessionID)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("openai-beta", "responses=experimental")
	identity := resolveCodexOutboundIdentity("")
	req.Header.Set("originator", identity.originator)
	req.Header.Set("session-id", wire.SessionID)
	req.Header.Set("session_id", wire.SessionID)
	req.Header.Set("thread-id", wire.ThreadID)
	req.Header.Set("x-client-request-id", wire.RequestID)
	req.Header.Set("x-codex-installation-id", wire.InstallationID)
	req.Header.Set("x-codex-turn-metadata", turnMetadata)
	req.Header.Set("x-codex-window-id", wire.WindowID)
	probeVersion := identity.version
	if CompareVersions(probeVersion, codexStateProbeMinVersion) < 0 {
		probeVersion = codexStateProbeMinVersion
	}
	req.Header.Set("user-agent", buildCodexCLIUserAgent(probeVersion))
	req.Header.Set("version", probeVersion)
	if state != "" {
		req.Header.Set(openAICodexTurnStateHeader, state)
	}
	resp, err := m.httpDoer.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return codexStateProbeResult{}, err
	}
	defer resp.Body.Close()
	result := codexStateProbeResult{
		State:      strings.TrimSpace(resp.Header.Get(openAICodexTurnStateHeader)),
		StatusCode: resp.StatusCode,
	}
	if result.StatusCode == http.StatusOK && result.State != "" {
		return result, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if strings.Contains(string(raw), "server_is_overloaded") {
		result.ErrorCode = "server_is_overloaded"
	}
	if result.StatusCode >= 400 && result.ErrorCode == "" {
		result.ErrorCode = fmt.Sprintf("http_%d", result.StatusCode)
	}
	if result.StatusCode >= 400 {
		result.ErrorBody = truncateString(string(raw), 300)
	}
	return result, nil
}
