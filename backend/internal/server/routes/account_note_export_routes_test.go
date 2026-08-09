package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountNoteExportRouteAdminService struct {
	service.AdminService

	accounts []*service.Account
	calls    int
	ids      []int64
}

func (s *accountNoteExportRouteAdminService) GetAccountsByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	s.calls++
	s.ids = append([]int64(nil), ids...)
	return s.accounts, nil
}

func TestAdminRoutesProtectAndDispatchAccountNoteExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	note := "route note"
	adminService := &accountNoteExportRouteAdminService{accounts: []*service.Account{
		{ID: 7, Platform: service.PlatformOpenAI, Notes: &note},
	}}
	accountHandler := adminhandler.NewAccountHandler(
		adminService,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		switch c.GetHeader("Authorization") {
		case "Bearer admin-token":
			c.Next()
		case "":
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
		default:
			servermiddleware.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
		}
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterAdminRoutes(
		router.Group("/api/v1"),
		&handler.Handlers{Admin: &handler.AdminHandlers{Account: accountHandler}},
		adminAuth,
		auditLog,
		stepUp,
		nil,
		nil,
	)

	for _, test := range []struct {
		name       string
		auth       string
		wantStatus int
	}{
		{name: "unauthenticated", wantStatus: http.StatusUnauthorized},
		{name: "non-admin", auth: "Bearer user-token", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/admin/accounts/export-notes",
				strings.NewReader(`{"account_ids":[7]}`),
			)
			request.Header.Set("Content-Type", "application/json")
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}

			router.ServeHTTP(recorder, request)

			require.Equal(t, test.wantStatus, recorder.Code)
			require.Zero(t, adminService.calls)
			require.Empty(t, recorder.Header().Get("Content-Disposition"))
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/accounts/export-notes",
		strings.NewReader(`{"account_ids":[7]}`),
	)
	request.Header.Set("Authorization", "Bearer admin-token")
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, adminService.calls)
	require.Equal(t, []int64{7}, adminService.ids)
	require.Equal(t, "1", recorder.Header().Get("X-Exported-Count"))
	require.Equal(t, "\uFEFFroute note\n", recorder.Body.String())
}
