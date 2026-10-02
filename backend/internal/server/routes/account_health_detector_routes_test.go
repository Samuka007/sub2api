package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminRoutesRegisterAccountHealthDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() })
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterAdminRoutes(
		router.Group("/api/v1"),
		&handler.Handlers{Admin: &handler.AdminHandlers{}},
		adminAuth,
		auditLog,
		stepUp,
		nil,
		nil,
	)

	routes := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	_, groupCandidatesRegistered := routes[http.MethodGet+" /api/v1/admin/groups/account-health-candidates"]
	require.True(t, groupCandidatesRegistered)
	_, accountCandidatesRegistered := routes[http.MethodGet+" /api/v1/admin/accounts/account-health-candidates"]
	require.True(t, accountCandidatesRegistered)
	_, accountDetectionRegistered := routes[http.MethodPost+" /api/v1/admin/accounts/:id/account-health-detection"]
	require.True(t, accountDetectionRegistered)
	_, legacyGroupDetectionRegistered := routes[http.MethodPost+" /api/v1/admin/groups/:id/account-health-detection"]
	require.False(t, legacyGroupDetectionRegistered)
}
