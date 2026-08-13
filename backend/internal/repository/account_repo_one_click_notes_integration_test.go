//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestApplyOneClickAccountNotesPersistsExactLinesInPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cache := &oneClickAccountNotesSchedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, cache)
	baseName := fmt.Sprintf("one-click-notes-success-%d@example.com", time.Now().UnixNano())
	line := "  " + baseName + "---中文 https://mail.example.test/inbox?token=synthetic&value=1  "
	secondOldNotes := "second account old note"
	unrelatedNotes := "unrelated note"
	deletedNotes := "deleted note"
	first := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, strings.ToUpper(baseName), nil)
	second := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, baseName, &secondOldNotes)
	unrelated := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, "unrelated-"+baseName, &unrelatedNotes)
	deleted := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, "DELETED-"+strings.ToUpper(baseName), &deletedNotes)
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET name = $1, deleted_at = NOW() WHERE id = $2", baseName, deleted.ID)
	require.NoError(t, err)
	configureOneClickAccountNotesIntegrationSentinels(t, ctx, first.ID, second.ID)
	firstBefore := readOneClickAccountNotesIntegrationSnapshot(t, ctx, first.ID)
	secondBefore := readOneClickAccountNotesIntegrationSnapshot(t, ctx, second.ID)
	cacheCallsBefore := cache.setCalls

	adminService := newOneClickAccountNotesIntegrationAdminService(repo)
	preview, err := adminService.PreviewOneClickAccountNotes(ctx, []byte(line))
	require.NoError(t, err)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 2, preview.MatchedAccounts)
	require.Equal(t, 2, preview.WillUpdateAccounts)
	require.True(t, preview.CanApply)
	require.NotEmpty(t, preview.PreviewDigest)

	result, err := adminService.ApplyOneClickAccountNotes(ctx, []byte(line), preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 2, result.MatchedAccounts)
	require.Equal(t, 2, result.UpdatedAccounts)

	firstAfter := readOneClickAccountNotesIntegrationSnapshot(t, ctx, first.ID)
	secondAfter := readOneClickAccountNotesIntegrationSnapshot(t, ctx, second.ID)
	require.Equal(t, line, firstAfter.notes.String)
	require.Equal(t, line, secondAfter.notes.String)
	require.Equal(t, firstBefore.nonNotes, firstAfter.nonNotes)
	require.Equal(t, secondBefore.nonNotes, secondAfter.nonNotes)
	require.Equal(t, firstBefore.accountGroups, firstAfter.accountGroups)
	require.Equal(t, secondBefore.accountGroups, secondAfter.accountGroups)
	require.Equal(t, firstBefore.schedulerOutbox, firstAfter.schedulerOutbox)
	require.Equal(t, secondBefore.schedulerOutbox, secondAfter.schedulerOutbox)
	require.True(t, firstAfter.updatedAt.After(firstBefore.updatedAt))
	require.True(t, secondAfter.updatedAt.After(secondBefore.updatedAt))
	require.Equal(t, unrelatedNotes, readOneClickAccountNotesIntegrationNote(t, ctx, unrelated.ID).String)
	require.Equal(t, deletedNotes, readOneClickAccountNotesIntegrationNote(t, ctx, deleted.ID).String)
	require.Equal(t, cacheCallsBefore, cache.setCalls, "notes-only updates must not rewrite complete scheduler snapshots")
}

func TestOneClickAccountNotesUsesConsistentASCIIFoldingInPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	suffix := fmt.Sprintf("-%d@example.com", time.Now().UnixNano())
	matchName := "k" + suffix
	asciiAccount := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, "K"+suffix, nil)
	unicodeAccount := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, "\u212A"+suffix, nil)
	line := matchName + "---ASCII-only case-insensitive match"

	adminService := newOneClickAccountNotesIntegrationAdminService(repo)
	preview, err := adminService.PreviewOneClickAccountNotes(ctx, []byte(line))
	require.NoError(t, err)
	require.Equal(t, 1, preview.MatchedAccounts)
	require.Equal(t, 1, preview.WillUpdateAccounts)

	result, err := adminService.ApplyOneClickAccountNotes(ctx, []byte(line), preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedAccounts)
	require.Equal(t, line, readOneClickAccountNotesIntegrationNote(t, ctx, asciiAccount.ID).String)
	require.False(t, readOneClickAccountNotesIntegrationNote(t, ctx, unicodeAccount.ID).Valid)
}

func TestOneClickAccountNotesRejectsCumulativeMultibyteStateInPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	name := fmt.Sprintf("one-click-notes-multibyte-%d@example.com", time.Now().UnixNano())
	first := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, strings.ToUpper(name), nil)
	second := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, name, nil)

	// Each note is below the byte limit, while their cumulative UTF-8 octet
	// length is just above it. A character-counting length(notes) mutation would
	// incorrectly admit this state because each 界 occupies three bytes.
	perAccountNotes := strings.Repeat("界", service.OneClickAccountNotesMaxTargetStateBytes/6+1)
	_, err := integrationDB.ExecContext(
		ctx,
		"UPDATE accounts SET notes = $1 WHERE id IN ($2, $3)",
		perAccountNotes,
		first.ID,
		second.ID,
	)
	require.NoError(t, err)

	var totalCharacters, totalOctets int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(length(notes)), 0), COALESCE(SUM(octet_length(notes)), 0)
		FROM accounts
		WHERE id IN ($1, $2)
	`, first.ID, second.ID).Scan(&totalCharacters, &totalOctets))
	require.Less(t, totalCharacters, int64(service.OneClickAccountNotesMaxTargetStateBytes))
	require.Greater(t, totalOctets, int64(service.OneClickAccountNotesMaxTargetStateBytes))
	requireOneClickAccountNotesOversizedSentinel(
		t,
		ctx,
		integrationDB,
		listOneClickAccountNoteTargetsQuery,
		name,
	)
	lockTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockTx.Rollback() })
	_, err = lockTx.ExecContext(ctx, lockOneClickAccountNotesTableQuery)
	require.NoError(t, err)
	requireOneClickAccountNotesOversizedSentinel(
		t,
		ctx,
		lockTx,
		lockOneClickAccountNoteTargetsQuery,
		name,
	)
	require.NoError(t, lockTx.Rollback())

	content := []byte(name + "---desired note")
	adminService := newOneClickAccountNotesIntegrationAdminService(repo)
	preview, err := adminService.PreviewOneClickAccountNotes(ctx, content)
	require.Nil(t, preview)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)
	result, err := adminService.ApplyOneClickAccountNotes(ctx, content, "synthetic-preview-digest")
	require.Nil(t, result)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)

	err = repo.ApplyOneClickAccountNotes(
		ctx,
		[]string{name},
		[]service.OneClickAccountNoteUpdate{
			{AccountID: first.ID, ExpectedName: first.Name, ExpectedNotes: nil, Notes: "desired note"},
			{AccountID: second.ID, ExpectedName: second.Name, ExpectedNotes: nil, Notes: "desired note"},
		},
	)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)
}

func TestAccountUpdateWithoutNotesIntentPreservesImportedNote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	name := fmt.Sprintf("one-click-notes-stale-editor-%d@example.com", time.Now().UnixNano())
	oldNotes := "note loaded by the ordinary editor"
	account := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, name, &oldNotes)

	staleEditor, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	staleEditor.Priority = 73
	importedLine := name + "---imported note"
	require.NoError(t, repo.ApplyOneClickAccountNotes(
		ctx,
		[]string{name},
		[]service.OneClickAccountNoteUpdate{{
			AccountID:     account.ID,
			ExpectedName:  account.Name,
			ExpectedNotes: &oldNotes,
			Notes:         importedLine,
		}},
	))

	require.NoError(t, repo.UpdateWithAccountBillingSettingsAndNotesIntent(
		ctx,
		staleEditor,
		nil,
		nil,
		nil,
		false,
	))
	require.Equal(t, importedLine, readOneClickAccountNotesIntegrationNote(t, ctx, account.ID).String)
	var priority int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT priority FROM accounts WHERE id = $1", account.ID).Scan(&priority))
	require.Equal(t, 73, priority)

	explicitNotes := "ordinary editor explicitly replaced the note"
	staleEditor.Notes = &explicitNotes
	require.NoError(t, repo.UpdateWithAccountBillingSettingsAndNotesIntent(
		ctx,
		staleEditor,
		nil,
		nil,
		nil,
		true,
	))
	require.Equal(t, explicitNotes, readOneClickAccountNotesIntegrationNote(t, ctx, account.ID).String)
}

func TestApplyOneClickAccountNotesExclusiveLockRejectsConcurrentMatchPhantoms(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *dbent.Tx, *accountRepository, string) int64
	}{
		{
			name: "inserted match",
			mutate: func(ctx context.Context, tx *dbent.Tx, _ *accountRepository, matchName string) int64 {
				account := oneClickAccountNotesIntegrationAccount(strings.ToUpper(matchName), nil)
				require.NoError(t, createAccountRecord(ctx, tx.Client(), account))
				return account.ID
			},
		},
		{
			name: "renamed into match",
			mutate: func(ctx context.Context, tx *dbent.Tx, repo *accountRepository, matchName string) int64 {
				other := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, "rename-source-"+matchName, nil)
				_, err := tx.Client().Account.UpdateOneID(other.ID).SetName(strings.ToUpper(matchName)).Save(ctx)
				require.NoError(t, err)
				return other.ID
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
			matchName := fmt.Sprintf("one-click-notes-phantom-%d@example.com", time.Now().UnixNano())
			existing := createOneClickAccountNotesIntegrationAccount(t, ctx, repo, matchName, nil)
			mutationTx, err := integrationEntClient.Tx(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = mutationTx.Rollback() })
			phantomID := test.mutate(ctx, mutationTx, repo, matchName)
			t.Cleanup(func() { cleanupOneClickAccountNotesIntegrationAccount(phantomID) })

			applyResult := make(chan error, 1)
			go func() {
				applyResult <- repo.ApplyOneClickAccountNotes(
					ctx,
					[]string{matchName},
					[]service.OneClickAccountNoteUpdate{{
						AccountID: existing.ID, ExpectedName: existing.Name, ExpectedNotes: nil, Notes: "desired import note",
					}},
				)
			}()

			require.Eventually(t, func() bool {
				var waiting bool
				err := integrationDB.QueryRowContext(ctx, `
					SELECT EXISTS (
						SELECT 1
						FROM pg_locks
						WHERE relation = 'accounts'::regclass
							AND mode = 'ExclusiveLock'
							AND NOT granted
					)
				`).Scan(&waiting)
				return err == nil && waiting
			}, 2*time.Second, 20*time.Millisecond, "apply must wait for the EXCLUSIVE accounts table lock")
			require.NoError(t, mutationTx.Commit())

			select {
			case err := <-applyResult:
				require.ErrorIs(t, err, service.ErrOneClickAccountNotesPreviewStale)
			case <-ctx.Done():
				t.Fatal("apply did not resume after the concurrent account mutation committed")
			}
			require.False(t, readOneClickAccountNotesIntegrationNote(t, ctx, existing.ID).Valid)
		})
	}
}

// This reproduces the lock order used by ordinary account edits: take a row
// lock first, then perform an UPDATE. The import must wait before acquiring any
// conflicting lock, allowing the ordinary edit to finish without a lock
// upgrade cycle. It then observes the changed note and fails stale.
func TestApplyOneClickAccountNotesExclusiveTableLockAvoidsAccountUpdateDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name:        fmt.Sprintf("one-click-notes-lock-%d@example.com", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Concurrency: 1,
		Priority:    50,
		Schedulable: true,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})

	ordinaryEdit, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ordinaryEdit.Rollback() })
	var lockedID int64
	require.NoError(t, ordinaryEdit.QueryRowContext(
		ctx,
		"SELECT id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE",
		account.ID,
	).Scan(&lockedID))
	require.Equal(t, account.ID, lockedID)

	applyResult := make(chan error, 1)
	go func() {
		applyResult <- repo.ApplyOneClickAccountNotes(
			ctx,
			[]string{account.Name},
			[]service.OneClickAccountNoteUpdate{{
				AccountID: account.ID, ExpectedName: account.Name, ExpectedNotes: nil, Notes: "desired import note",
			}},
		)
	}()
	select {
	case err := <-applyResult:
		t.Fatalf("import returned before the existing row lock was released: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	concurrentNotes := "ordinary edit won"
	_, err = ordinaryEdit.ExecContext(ctx, "UPDATE accounts SET notes = $1, updated_at = NOW() WHERE id = $2", concurrentNotes, account.ID)
	require.NoError(t, err, "ordinary account edit must not deadlock while import waits for its initial table lock")
	require.NoError(t, ordinaryEdit.Commit())

	select {
	case err := <-applyResult:
		require.ErrorIs(t, err, service.ErrOneClickAccountNotesPreviewStale)
	case <-ctx.Done():
		t.Fatal("import did not resume after the ordinary edit committed")
	}
	var storedNotes string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT notes FROM accounts WHERE id = $1", account.ID).Scan(&storedNotes))
	require.Equal(t, concurrentNotes, storedNotes)
}

func TestApplyOneClickAccountNotesLockTimeoutReleasesQueuedAccountWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	suffix := time.Now().UnixNano()
	blocker := createOneClickAccountNotesIntegrationAccount(
		t,
		ctx,
		repo,
		fmt.Sprintf("one-click-notes-timeout-blocker-%d@example.com", suffix),
		nil,
	)
	importTarget := createOneClickAccountNotesIntegrationAccount(
		t,
		ctx,
		repo,
		fmt.Sprintf("one-click-notes-timeout-import-%d@example.com", suffix),
		nil,
	)
	writerTarget := createOneClickAccountNotesIntegrationAccount(
		t,
		ctx,
		repo,
		fmt.Sprintf("one-click-notes-timeout-writer-%d@example.com", suffix),
		nil,
	)

	blockerTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blockerTx.Rollback() })
	var blockerPID int
	require.NoError(t, blockerTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID))
	_, err = blockerTx.ExecContext(ctx, "UPDATE accounts SET priority = priority WHERE id = $1", blocker.ID)
	require.NoError(t, err)

	importTx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = importTx.Rollback() })
	var importPID int
	rows, err := importTx.Client().QueryContext(ctx, "SELECT pg_backend_pid()")
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&importPID))
	require.NoError(t, rows.Close())
	importResult := make(chan error, 1)
	go func() {
		importResult <- repo.ApplyOneClickAccountNotes(
			dbent.NewTxContext(ctx, importTx),
			[]string{importTarget.Name},
			[]service.OneClickAccountNoteUpdate{{
				AccountID: importTarget.ID, ExpectedName: importTarget.Name, ExpectedNotes: nil, Notes: "desired import note",
			}},
		)
	}()

	require.Eventually(t, func() bool {
		return oneClickAccountNotesIntegrationPIDBlocks(ctx, blockerPID, importPID)
	}, 750*time.Millisecond, 10*time.Millisecond, "the existing account writer must block the import table lock")

	writerTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writerTx.Rollback() })
	var writerPID int
	require.NoError(t, writerTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&writerPID))
	writerResult := make(chan error, 1)
	go func() {
		_, writeErr := writerTx.ExecContext(
			ctx,
			"UPDATE accounts SET priority = priority + 1 WHERE id = $1",
			writerTarget.ID,
		)
		writerResult <- writeErr
	}()

	require.Eventually(t, func() bool {
		return oneClickAccountNotesIntegrationPIDBlocks(ctx, importPID, writerPID)
	}, 750*time.Millisecond, 10*time.Millisecond, "the queued import table lock must block the later ordinary writer")

	select {
	case err := <-importResult:
		require.ErrorIs(t, err, service.ErrOneClickAccountNotesBusy)
		require.Equal(t, 1, service.RetryAfterSecondsFromError(err))
	case <-time.After(2 * time.Second):
		t.Fatal("import did not honor its product lock timeout")
	}

	// The blocker transaction remains open. Removing the timed-out EXCLUSIVE
	// request from the lock queue must be sufficient for the compatible writer.
	var blockerStillOpen int
	require.NoError(t, blockerTx.QueryRowContext(ctx, "SELECT 1").Scan(&blockerStillOpen))
	require.Equal(t, 1, blockerStillOpen)
	select {
	case err := <-writerResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ordinary account writer remained queued after the import lock timed out")
	}
	require.NoError(t, writerTx.Commit())

	var priority int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT priority FROM accounts WHERE id = $1", writerTarget.ID).Scan(&priority))
	require.Equal(t, 51, priority)
	require.NoError(t, importTx.Rollback())
	require.NoError(t, blockerTx.Rollback())
}

func oneClickAccountNotesIntegrationPIDBlocks(ctx context.Context, blockerPID, blockedPID int) bool {
	var blocked bool
	err := integrationDB.QueryRowContext(
		ctx,
		"SELECT $1 = ANY(pg_blocking_pids($2))",
		blockerPID,
		blockedPID,
	).Scan(&blocked)
	return err == nil && blocked
}

type oneClickAccountNotesIntegrationSnapshot struct {
	nonNotes        string
	accountGroups   string
	schedulerOutbox string
	notes           sql.NullString
	updatedAt       time.Time
}

type oneClickAccountNotesIntegrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func requireOneClickAccountNotesOversizedSentinel(
	t *testing.T,
	ctx context.Context,
	queryer oneClickAccountNotesIntegrationQueryer,
	query string,
	matchName string,
) {
	t.Helper()
	rows, err := queryer.QueryContext(
		ctx,
		query,
		pq.Array([]string{matchName}),
		service.OneClickAccountNotesMaxMatchedAccounts+1,
		service.OneClickAccountNotesMaxTargetStateBytes,
	)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	rowCount := 0
	for rows.Next() {
		rowCount++
		var id, notesBytes int64
		var name string
		var notes sql.NullString
		var stateTooLarge bool
		require.NoError(t, rows.Scan(&id, &name, &notes, &notesBytes, &stateTooLarge))
		require.Zero(t, id)
		require.Empty(t, name)
		require.False(t, notes.Valid)
		require.Zero(t, notesBytes)
		require.True(t, stateTooLarge)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, rowCount, "oversized target state must return one lightweight sentinel row")
}

func newOneClickAccountNotesIntegrationAdminService(repo *accountRepository) service.AdminService {
	return service.NewAdminService(
		nil,                  // userRepo
		nil,                  // groupRepo
		repo,                 // accountRepo
		nil,                  // proxyRepo
		nil,                  // apiKeyRepo
		nil,                  // redeemCodeRepo
		nil,                  // userGroupRateRepo
		nil,                  // userRPMCache
		nil,                  // billingCacheService
		nil,                  // proxyProber
		nil,                  // proxyLatencyCache
		nil,                  // authCacheInvalidator
		integrationEntClient, // entClient
		nil,                  // settingService
		nil,                  // defaultSubAssigner
		nil,                  // userSubRepo
		nil,                  // privacyClientFactory
		nil,                  // runtimeBlocker
		nil,                  // affiliateService
		nil,                  // compositeRouteRepo
		nil,                  // compositeResolver
		nil,                  // channelCacheInvalidator
	)
}

func configureOneClickAccountNotesIntegrationSentinels(
	t *testing.T,
	ctx context.Context,
	accountIDs ...int64,
) {
	t.Helper()
	suffix := time.Now().UnixNano()
	var proxyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO proxies (name, protocol, host, port, status)
		VALUES ($1, 'http', '192.0.2.1', 6553, 'disabled')
		RETURNING id
	`, fmt.Sprintf("one-click-notes-proxy-%d", suffix)).Scan(&proxyID))
	var groupID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO groups (name, description, rate_multiplier, is_exclusive, status)
		VALUES ($1, 'synthetic one-click notes invariant fixture', 1.375, TRUE, 'disabled')
		RETURNING id
	`, fmt.Sprintf("one-click-notes-group-%d", suffix)).Scan(&groupID))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM groups WHERE id = $1", groupID)
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM proxies WHERE id = $1", proxyID)
	})

	for index, accountID := range accountIDs {
		credentials := fmt.Sprintf(`{"fixture_marker":"credentials-preserved-%d"}`, index)
		extra := fmt.Sprintf(`{"fixture_marker":"extra-preserved-%d","nested":{"value":%d}}`, index, index+1)
		status := "disabled"
		schedulable := false
		if index%2 == 1 {
			status = "error"
			schedulable = true
		}
		_, err := integrationDB.ExecContext(ctx, `
			UPDATE accounts
			SET credentials = $1::jsonb,
				extra = $2::jsonb,
				proxy_id = $3,
				concurrency = $4,
				priority = $5,
				status = $6,
				schedulable = $7,
				error_message = $8,
				load_factor = $9,
				rate_multiplier = $10,
				updated_at = NOW() - INTERVAL '1 hour'
			WHERE id = $11
		`,
			credentials,
			extra,
			proxyID,
			7+index,
			17+index,
			status,
			schedulable,
			fmt.Sprintf("synthetic preserved error %d", index),
			11+index,
			1.25+float64(index)/10,
			accountID,
		)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `
			INSERT INTO account_groups (account_id, group_id, priority, created_at)
			VALUES ($1, $2, $3, NOW())
		`, accountID, groupID, 31+index)
		require.NoError(t, err)
	}
}

func readOneClickAccountNotesIntegrationSnapshot(
	t *testing.T,
	ctx context.Context,
	accountID int64,
) oneClickAccountNotesIntegrationSnapshot {
	t.Helper()
	var snapshot oneClickAccountNotesIntegrationSnapshot
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			(to_jsonb(account_row) - ARRAY['notes', 'updated_at']::text[])::text,
			account_row.notes,
			account_row.updated_at
		FROM accounts AS account_row
		WHERE account_row.id = $1
	`, accountID).Scan(&snapshot.nonNotes, &snapshot.notes, &snapshot.updatedAt))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE(
			jsonb_agg(to_jsonb(account_group_row) ORDER BY account_group_row.group_id),
			'[]'::jsonb
		)::text
		FROM account_groups AS account_group_row
		WHERE account_group_row.account_id = $1
	`, accountID).Scan(&snapshot.accountGroups))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE(
			jsonb_agg(to_jsonb(outbox_row) ORDER BY outbox_row.id),
			'[]'::jsonb
		)::text
		FROM scheduler_outbox AS outbox_row
		WHERE outbox_row.account_id = $1
	`, accountID).Scan(&snapshot.schedulerOutbox))
	return snapshot
}

func oneClickAccountNotesIntegrationAccount(name string, notes *string) *service.Account {
	return &service.Account{
		Name:        name,
		Notes:       notes,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Concurrency: 1,
		Priority:    50,
		Schedulable: true,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
}

func createOneClickAccountNotesIntegrationAccount(
	t *testing.T,
	ctx context.Context,
	repo *accountRepository,
	name string,
	notes *string,
) *service.Account {
	t.Helper()
	account := oneClickAccountNotesIntegrationAccount(name, notes)
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupOneClickAccountNotesIntegrationAccount(account.ID) })
	return account
}

func cleanupOneClickAccountNotesIntegrationAccount(accountID int64) {
	if accountID <= 0 {
		return
	}
	_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", accountID)
	_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", accountID)
}

func readOneClickAccountNotesIntegrationNote(t *testing.T, ctx context.Context, accountID int64) sql.NullString {
	t.Helper()
	var notes sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT notes FROM accounts WHERE id = $1", accountID).Scan(&notes))
	return notes
}
