-- ASCII case-insensitive exact account-name lookups power the one-click notes
-- preview and apply paths. Keep deleted accounts out of the index because both
-- queries explicitly filter them out.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_lower_name_id_active
    ON accounts (translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'), id)
    WHERE deleted_at IS NULL;
