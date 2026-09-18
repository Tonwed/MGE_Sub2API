package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type CodexStateHandler struct {
	service *service.CodexStateAdminService
}

func NewCodexStateHandler(stateService *service.CodexStateAdminService) *CodexStateHandler {
	return &CodexStateHandler{service: stateService}
}

type updateCodexStateAccountRequest struct {
	AutoMint             *bool    `json:"auto_mint"`
	Concurrency          *int     `json:"concurrency"`
	RefreshBeforeMinutes *int     `json:"refresh_before_minutes"`
	NormalProxyID        *int64   `json:"normal_proxy_id"`
	StateProxyIDs        *[]int64 `json:"state_proxy_ids"`
}

type triggerCodexStateMintRequest struct {
	Model string `json:"model"`
}

func (h *CodexStateHandler) Overview(c *gin.Context) {
	overview, err := h.service.Overview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

func (h *CodexStateHandler) UpdateAccount(c *gin.Context) {
	accountID, ok := codexStateAccountID(c)
	if !ok {
		return
	}
	var req updateCodexStateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	account, err := h.service.UpdateConfig(c.Request.Context(), accountID, service.CodexStateAdminUpdateInput{
		AutoMint:             req.AutoMint,
		Concurrency:          req.Concurrency,
		RefreshBeforeMinutes: req.RefreshBeforeMinutes,
		NormalProxyID:        req.NormalProxyID,
		StateProxyIDs:        req.StateProxyIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, account)
}

func (h *CodexStateHandler) TriggerMint(c *gin.Context) {
	accountID, ok := codexStateAccountID(c)
	if !ok {
		return
	}
	var req triggerCodexStateMintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.TriggerMint(c.Request.Context(), accountID, req.Model); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"started": true})
}

func codexStateAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}
