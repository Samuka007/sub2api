package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerModelRadarRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	radar := admin.Group("/model-radar")
	{
		radar.GET("", h.Admin.ModelRadar.Get)
		radar.POST("/refresh", h.Admin.ModelRadar.Refresh)
	}
}
