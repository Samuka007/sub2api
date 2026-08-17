//go:build unit

package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasAdminRole(t *testing.T) {
	require.True(t, HasAdminRole([]string{RoleSuperAdmin}))
	require.True(t, HasAdminRole([]string{RoleBillingAdmin}))
	require.True(t, HasAdminRole([]string{RoleUpstreamAdmin}))
	require.True(t, HasAdminRole([]string{RoleBillingAdmin, RoleUpstreamAdmin}))
	require.False(t, HasAdminRole(nil))
	require.False(t, HasAdminRole([]string{}))
	require.False(t, HasAdminRole([]string{"user"}))
}

func TestHasPermission_SuperShortCircuits(t *testing.T) {
	for _, perm := range []string{
		PermissionSuperAdmin,
		PermissionUsersRead,
		PermissionUsersManage,
		PermissionUsersBalanceAdjust,
		PermissionRedeemCodesManage,
		PermissionPromoCodesManage,
		PermissionAccountsManage,
		PermissionGroupsManage,
		PermissionProxiesManage,
		PermissionChannelsManage,
		PermissionOpenAIOAuthManage,
		PermissionGeminiOAuthManage,
		PermissionAntigravityOAuthManage,
		PermissionGrokOAuthManage,
	} {
		require.True(t, HasPermission([]string{RoleSuperAdmin}, perm), "super_admin must grant %s", perm)
	}
}

func TestHasPermission_SingleRoleBoundaries(t *testing.T) {
	// billing_admin：计费能力
	require.True(t, HasPermission([]string{RoleBillingAdmin}, PermissionUsersRead))
	require.True(t, HasPermission([]string{RoleBillingAdmin}, PermissionUsersBalanceAdjust))
	require.True(t, HasPermission([]string{RoleBillingAdmin}, PermissionRedeemCodesManage))
	require.True(t, HasPermission([]string{RoleBillingAdmin}, PermissionPromoCodesManage))
	// billing_admin：不得越权
	require.False(t, HasPermission([]string{RoleBillingAdmin}, PermissionUsersManage))
	require.False(t, HasPermission([]string{RoleBillingAdmin}, PermissionAccountsManage))
	require.False(t, HasPermission([]string{RoleBillingAdmin}, PermissionGroupsManage))
	require.False(t, HasPermission([]string{RoleBillingAdmin}, PermissionSuperAdmin))

	// upstream_admin：上游能力
	require.True(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionAccountsManage))
	require.True(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionGroupsManage))
	require.True(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionProxiesManage))
	require.True(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionChannelsManage))
	// upstream_admin：不得越权
	require.False(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionUsersBalanceAdjust))
	require.False(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionRedeemCodesManage))
	require.False(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionPromoCodesManage))
	require.False(t, HasPermission([]string{RoleUpstreamAdmin}, PermissionSuperAdmin))
}

func TestHasPermission_UnionOfRoles(t *testing.T) {
	roles := []string{RoleBillingAdmin, RoleUpstreamAdmin}
	// 并集：两类能力都可访问
	require.True(t, HasPermission(roles, PermissionUsersBalanceAdjust))
	require.True(t, HasPermission(roles, PermissionRedeemCodesManage))
	require.True(t, HasPermission(roles, PermissionAccountsManage))
	require.True(t, HasPermission(roles, PermissionGroupsManage))
	// 并集不放大：系统设置仍被拒绝
	require.False(t, HasPermission(roles, PermissionSuperAdmin))
	require.False(t, HasPermission(roles, PermissionUsersManage))
}

func TestNormalizeAdminRoles(t *testing.T) {
	require.Equal(t, []string{"billing_admin", "super_admin"}, NormalizeAdminRoles([]string{"super_admin", "billing_admin", "super_admin"}))
	require.Equal(t, []string{}, NormalizeAdminRoles(nil))
	require.Equal(t, []string{"super_admin"}, NormalizeAdminRoles([]string{"super_admin", "unknown"}))
}

func TestValidateAdminRoles(t *testing.T) {
	require.NoError(t, ValidateAdminRoles(nil))
	require.NoError(t, ValidateAdminRoles([]string{RoleSuperAdmin, RoleBillingAdmin, RoleUpstreamAdmin}))
	require.Error(t, ValidateAdminRoles([]string{"admin"}))
	require.Error(t, ValidateAdminRoles([]string{RoleSuperAdmin, "root"}))
}

func TestLegacyRoleForRoles(t *testing.T) {
	require.Equal(t, RoleAdmin, LegacyRoleForRoles([]string{RoleSuperAdmin}))
	require.Equal(t, RoleAdmin, LegacyRoleForRoles([]string{RoleBillingAdmin, RoleSuperAdmin}))
	require.Equal(t, RoleUser, LegacyRoleForRoles([]string{RoleBillingAdmin}))
	require.Equal(t, RoleUser, LegacyRoleForRoles([]string{RoleUpstreamAdmin}))
	require.Equal(t, RoleUser, LegacyRoleForRoles(nil))
}

func TestRolesSummary(t *testing.T) {
	require.Equal(t, "billing_admin,super_admin", RolesSummary([]string{"super_admin", "billing_admin"}))
	require.Equal(t, "user", RolesSummary(nil))
	require.Equal(t, "user", RolesSummary([]string{}))
}
