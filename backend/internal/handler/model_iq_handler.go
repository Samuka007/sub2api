package handler

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type modelIQReader interface {
	Get(context.Context) (*service.ModelIQView, error)
	Refresh(context.Context) (*service.ModelIQView, error)
}

type ModelIQHandler struct {
	modelIQService modelIQReader
}

func NewModelIQHandler(modelIQService *service.ModelIQService) *ModelIQHandler {
	return &ModelIQHandler{modelIQService: modelIQService}
}

// Get returns the current GPT model IQ comparison snapshot.
// GET /api/v1/model-iq
func (h *ModelIQHandler) Get(c *gin.Context) {
	if h == nil || h.modelIQService == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable(
			"MODEL_IQ_SERVICE_UNAVAILABLE",
			"model IQ ranking is temporarily unavailable",
		))
		return
	}

	view, err := h.modelIQService.Get(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Refresh forces an upstream refresh unless another successful fetch happened
// within the service cooldown window.
// POST /api/v1/model-iq/refresh
func (h *ModelIQHandler) Refresh(c *gin.Context) {
	if h == nil || h.modelIQService == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable(
			"MODEL_IQ_SERVICE_UNAVAILABLE",
			"model IQ ranking is temporarily unavailable",
		))
		return
	}

	view, err := h.modelIQService.Refresh(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
