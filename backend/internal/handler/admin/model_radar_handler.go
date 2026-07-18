package admin

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ModelRadarHandler struct {
	modelRadarService *service.ModelRadarService
}

func NewModelRadarHandler(modelRadarService *service.ModelRadarService) *ModelRadarHandler {
	return &ModelRadarHandler{modelRadarService: modelRadarService}
}

// Get returns the most recent cached public radar snapshot.
// GET /api/v1/admin/model-radar
func (h *ModelRadarHandler) Get(c *gin.Context) {
	if h == nil || h.modelRadarService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Model radar service is not available")
		return
	}

	view, err := h.modelRadarService.Get(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Unable to retrieve model radar data")
		return
	}
	response.Success(c, view)
}

// Refresh fetches a new public snapshot when the source cooldown permits it.
// POST /api/v1/admin/model-radar/refresh
func (h *ModelRadarHandler) Refresh(c *gin.Context) {
	if h == nil || h.modelRadarService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Model radar service is not available")
		return
	}

	view, err := h.modelRadarService.Refresh(c.Request.Context())
	if err != nil {
		if errors.Is(err, service.ErrModelRadarRefreshTooSoon) {
			response.Error(c, http.StatusTooManyRequests, "Model radar was refreshed recently")
			return
		}
		response.Error(c, http.StatusBadGateway, "Unable to refresh model radar data")
		return
	}
	response.Success(c, view)
}
