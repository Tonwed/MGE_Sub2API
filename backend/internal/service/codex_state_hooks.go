package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *OpenAIGatewayService) SetCodexStateManager(manager *CodexStateManager) {
	if s == nil {
		return
	}
	s.codexStateManager = manager
	slog.Info("codex_state.manager_configured", "enabled", manager != nil)
}

func (s *OpenAIGatewayService) injectCodexTurnState(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	model string,
	headers http.Header,
) {
	if s == nil || s.codexStateManager == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return
	}
	slog.Debug("codex_state.inject_hook", "account_id", account.ID, "model", model)
	if c == nil {
		return
	}
	s.codexStateManager.InjectHeader(ctx, account, model, headers)
	if injected := strings.TrimSpace(headers.Get(openAICodexTurnStateHeader)); injected != "" {
		slog.Debug("codex_state.injected",
			"account_id", account.ID,
			"model", model,
			"state_len", len(injected),
		)
	}
}

func (s *OpenAIGatewayService) observeCodexTurnState(
	ctx context.Context,
	account *Account,
	model string,
	headers http.Header,
) {
	if s == nil || s.codexStateManager == nil || account == nil || headers == nil {
		return
	}
	slog.Debug("codex_state.observe_hook", "account_id", account.ID, "model", model)
	state := strings.TrimSpace(headers.Get(openAICodexTurnStateHeader))
	errorCode := ""
	if state == "" {
		slog.Debug("codex_state.observed", "account_id", account.ID, "model", model, "state_len", 0)
		return
	}
	slog.Debug("codex_state.observed",
		"account_id", account.ID,
		"model", model,
		"state_len", len(state),
	)
	s.codexStateManager.Observe(ctx, account, model, state, errorCode)
}
