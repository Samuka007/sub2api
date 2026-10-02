package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin"
)

// AuthSubject is the minimal authenticated identity stored in gin context.
// Decision: {UserID int64, Concurrency int, Roles []string}
type AuthSubject struct {
	UserID      int64
	Concurrency int
	// Roles is the canonical multi-role set for the authenticated identity.
	// Authorization reads Roles exclusively; never compare the legacy role string.
	Roles []string
}

func GetAuthSubjectFromContext(c *gin.Context) (AuthSubject, bool) {
	value, exists := c.Get(string(ContextKeyUser))
	if !exists {
		return AuthSubject{}, false
	}
	subject, ok := value.(AuthSubject)
	return subject, ok
}

// GetUserRoleFromContext returns the legacy role summary derived from the subject's
// role set ("admin" for any admin role, "user" otherwise). Prefer
// GetAdminRolesFromContext / HasAdminPermission for new authorization logic.
func GetUserRoleFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(string(ContextKeyUserRole))
	if !exists {
		return "", false
	}
	role, ok := value.(string)
	return role, ok
}

// GetAdminRolesFromContext returns the canonical role set from context.
func GetAdminRolesFromContext(c *gin.Context) ([]string, bool) {
	subject, ok := GetAuthSubjectFromContext(c)
	if !ok {
		return nil, false
	}
	return subject.Roles, true
}

// IsAdminContext reports whether the authenticated identity has any admin role.
func IsAdminContext(c *gin.Context) bool {
	roles, ok := GetAdminRolesFromContext(c)
	return ok && domain.HasAdminRole(roles)
}

// HasAdminPermission reports whether the authenticated identity grants the permission.
func HasAdminPermission(c *gin.Context, permission string) bool {
	roles, ok := GetAdminRolesFromContext(c)
	return ok && domain.HasPermission(roles, permission)
}
