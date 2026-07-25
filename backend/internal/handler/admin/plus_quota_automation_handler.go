package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type PlusQuotaAutomationHandler struct {
	service *service.PlusQuotaAutomationService
}

func NewPlusQuotaAutomationHandler(service *service.PlusQuotaAutomationService) *PlusQuotaAutomationHandler {
	return &PlusQuotaAutomationHandler{service: service}
}

func (h *PlusQuotaAutomationHandler) Get(c *gin.Context) {
	overview, err := h.service.GetOverview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

func (h *PlusQuotaAutomationHandler) Update(c *gin.Context) {
	var input service.PlusQuotaAutomationConfig
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if _, err := h.service.UpdateConfig(c.Request.Context(), input); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	overview, err := h.service.GetOverview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

func (h *PlusQuotaAutomationHandler) Run(c *gin.Context) {
	if err := h.service.TriggerRun(); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	overview, err := h.service.GetOverview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overview)
}

func (h *PlusQuotaAutomationHandler) ListAnomalies(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	result, err := h.service.ListAnomalies(
		c.Request.Context(),
		c.Query("status"),
		c.Query("search"),
		page,
		pageSize,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *PlusQuotaAutomationHandler) ResolveAnomaly(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("accountId"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	anomaly, err := h.service.ResolveAnomaly(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, anomaly)
}

func queryInt(c *gin.Context, key string, fallback int) int {
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
