package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Admin role constants. These are the canonical multi-role identifiers stored in
// users.roles (JSONB) and carried in JWT claims / auth context. `RoleAdmin` and
// `RoleUser` remain only for the legacy users.role compat column (see below).
const (
	RoleSuperAdmin    = "super_admin"
	RoleBillingAdmin  = "billing_admin"
	RoleUpstreamAdmin = "upstream_admin"
)

// Admin permission constants. Routes and handlers authorize against these stable
// domain-action identifiers instead of comparing role strings, so adding or
// splitting roles later does not require rewriting handlers.
//
// `PermissionSuperAdmin` is a sentinel satisfied only by the super_admin role
// (via short-circuit in HasPermission) and marks routes that no sub-role may reach.
const (
	PermissionSuperAdmin             = "admin.super"
	PermissionUsersRead              = "admin.users.read"
	PermissionUsersManage            = "admin.users.manage"
	PermissionUsersBalanceAdjust     = "admin.users.balance.adjust"
	PermissionRedeemCodesManage      = "admin.redeem_codes.manage"
	PermissionPromoCodesManage       = "admin.promo_codes.manage"
	PermissionAccountsManage         = "admin.accounts.manage"
	PermissionGroupsManage           = "admin.groups.manage"
	PermissionProxiesManage          = "admin.proxies.manage"
	PermissionChannelsManage         = "admin.channels.manage"
	PermissionOpenAIOAuthManage      = "admin.openai_oauth.manage"
	PermissionGeminiOAuthManage      = "admin.gemini_oauth.manage"
	PermissionAntigravityOAuthManage = "admin.antigravity_oauth.manage"
	PermissionGrokOAuthManage        = "admin.grok_oauth.manage"
)

// adminRolePermissions maps each non-super admin role to the permissions it grants.
// Multiple roles on a user combine as a union; super_admin is intentionally absent
// because it short-circuits every permission.
var adminRolePermissions = map[string][]string{
	RoleBillingAdmin: {
		PermissionUsersRead,
		PermissionUsersBalanceAdjust,
		PermissionRedeemCodesManage,
		PermissionPromoCodesManage,
	},
	RoleUpstreamAdmin: {
		PermissionAccountsManage,
		PermissionGroupsManage,
		PermissionProxiesManage,
		PermissionChannelsManage,
		PermissionOpenAIOAuthManage,
		PermissionGeminiOAuthManage,
		PermissionAntigravityOAuthManage,
		PermissionGrokOAuthManage,
	},
}

// ValidAdminRoles is the authoritative set of assignable admin roles.
var ValidAdminRoles = []string{
	RoleSuperAdmin,
	RoleBillingAdmin,
	RoleUpstreamAdmin,
}

// IsAdminRole reports whether role is one of the assignable admin roles.
func IsAdminRole(role string) bool {
	switch role {
	case RoleSuperAdmin, RoleBillingAdmin, RoleUpstreamAdmin:
		return true
	default:
		return false
	}
}

// HasAdminRole reports whether roles contains at least one admin role.
func HasAdminRole(roles []string) bool {
	for _, r := range roles {
		if IsAdminRole(r) {
			return true
		}
	}
	return false
}

// HasPermission reports whether the role set grants the given permission.
// super_admin short-circuits every permission; the PermissionSuperAdmin sentinel is
// only satisfiable by super_admin and never appears in a sub-role's permission set.
func HasPermission(roles []string, permission string) bool {
	for _, r := range roles {
		if r == RoleSuperAdmin {
			return true
		}
		for _, p := range adminRolePermissions[r] {
			if p == permission {
				return true
			}
		}
	}
	return false
}

// NormalizeAdminRoles trims, deduplicates, sorts, and drops unknown values.
// It is the single entry point for role validation/normalization; unknown roles are
// rejected by ValidateAdminRoles before storage, so this function assumes valid input
// but still defensively filters.
func NormalizeAdminRoles(roles []string) []string {
	seen := make(map[string]struct{}, len(roles))
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if !IsAdminRole(r) {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// ValidateAdminRoles returns an error naming the first unknown role, or nil when
// every role is a known admin role.
func ValidateAdminRoles(roles []string) error {
	for _, r := range roles {
		if !IsAdminRole(r) {
			return fmt.Errorf("invalid admin role: %q", r)
		}
	}
	return nil
}

// LegacyRoleForRoles derives the legacy users.role compat column value from a
// normalized role set: "admin" when super_admin is present, "user" otherwise.
// The legacy column must never be used for authorization; it exists only so that
// pre-rollout queries and filters that group "super admin vs everyone else" keep
// working across the deployment window.
func LegacyRoleForRoles(roles []string) string {
	for _, r := range roles {
		if r == RoleSuperAdmin {
			return RoleAdmin
		}
	}
	return RoleUser
}

// RolesSummary renders a stable, sortable summary of the role set for audit/log
// fields that accept a single string ("super_admin,billing_admin" or "user").
func RolesSummary(roles []string) string {
	normalized := NormalizeAdminRoles(roles)
	if len(normalized) == 0 {
		return RoleUser
	}
	return strings.Join(normalized, ",")
}
