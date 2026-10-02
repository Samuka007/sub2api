package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminRoutesDoNotRegisterModelRadar(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() })
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterAdminRoutes(router.Group("/api/v1"), &handler.Handlers{Admin: &handler.AdminHandlers{}}, adminAuth, auditLog, stepUp, nil, nil)

	for _, route := range router.Routes() {
		require.NotContains(t, route.Path, "/admin/model-radar")
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/model-radar", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/admin/model-radar/refresh", nil),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusNotFound, response.Code)
	}
}
