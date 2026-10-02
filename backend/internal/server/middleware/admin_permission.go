package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin"
)

// RequireAdminPermission 在 adminAuth 之后按权限授权。
// 认证失败保持 401；认证成功但权限不足返回 403。super_admin 短路通过任意权限。
// 必须在 adminAuth（或其等价物）之后挂载，因为它读取 context 中的角色集合。
func RequireAdminPermission(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		roles, ok := GetAdminRolesFromContext(c)
		if !ok {
			AbortWithError(c, 401, "UNAUTHORIZED", "User not found in context")
			return
		}
		if !domain.HasPermission(roles, permission) {
			AbortWithError(c, 403, "FORBIDDEN", "Insufficient admin permissions")
			return
		}
		c.Next()
	}
}
