package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountNameLookupIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("196_add_account_name_lookup_index_notx.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_lower_name_id_active")
	require.Contains(t, sql, "ON accounts (translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'), id)")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
}
