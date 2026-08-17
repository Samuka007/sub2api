//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runPermissionRouter(t *testing.T, roles []string, permission string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 1, Roles: roles})
		c.Next()
	})
	router.Use(RequireAdminPermission(permission))
	router.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	return rec
}

func TestRequireAdminPermission(t *testing.T) {
	t.Run("super_admin passes any permission", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{domain.RoleSuperAdmin}, domain.PermissionSuperAdmin)
		require.Equal(t, http.StatusOK, rec.Code)
		rec = runPermissionRouter(t, []string{domain.RoleSuperAdmin}, domain.PermissionAccountsManage)
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("billing admin passes billing permissions", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{domain.RoleBillingAdmin}, domain.PermissionUsersBalanceAdjust)
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("billing admin denied upstream permission", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{domain.RoleBillingAdmin}, domain.PermissionAccountsManage)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("upstream admin passes upstream permission", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{domain.RoleUpstreamAdmin}, domain.PermissionGroupsManage)
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("upstream admin denied billing permission", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{domain.RoleUpstreamAdmin}, domain.PermissionUsersBalanceAdjust)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("regular user denied", func(t *testing.T) {
		rec := runPermissionRouter(t, []string{}, domain.PermissionUsersRead)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("missing context is unauthorized", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(RequireAdminPermission(domain.PermissionUsersRead))
		router.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
