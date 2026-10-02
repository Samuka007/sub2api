package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

const (
	oneClickAccountNotesListProjectionQueryPattern = `(?s)WITH candidates AS MATERIALIZED \(\s*SELECT id, name, COALESCE\(octet_length\(notes\), 0\) AS notes_bytes\s*FROM accounts.*LIMIT \$2\s*\), measured AS \(.*COUNT\(\*\) OVER \(\).*SUM\(notes_bytes\) OVER \(\).*CASE\s*WHEN state_too_large THEN NULL\s*ELSE \(\s*SELECT account\.notes.*state_too_large`
	oneClickAccountNotesLockProjectionQueryPattern = `(?s)WITH candidates AS MATERIALIZED \(\s*SELECT id, name, COALESCE\(octet_length\(notes\), 0\) AS notes_bytes\s*FROM accounts.*LIMIT \$2\s*FOR NO KEY UPDATE\s*\), measured AS \(.*COUNT\(\*\) OVER \(\).*SUM\(notes_bytes\) OVER \(\).*CASE\s*WHEN state_too_large THEN NULL\s*ELSE \(\s*SELECT account\.notes.*state_too_large`
	oneClickAccountNotesOnlyUpdateQueryPattern     = `(?s)^\s*WITH input AS MATERIALIZED \(\s*SELECT id, notes\s*FROM jsonb_to_recordset\(\$1::jsonb\) AS item\(id bigint, notes text\)\s*\)\s*UPDATE accounts AS account\s*SET notes = input\.notes,\s*updated_at = NOW\(\)\s*FROM input\s*WHERE account\.id = input\.id\s*AND account\.deleted_at IS NULL\s*$`
)

type oneClickAccountNoteMutationsMatcher struct {
	want []oneClickAccountNoteMutation
}

type oneClickAccountNotesSchedulerCacheRecorder struct {
	service.SchedulerCache
	setCalls int
}

func (r *oneClickAccountNotesSchedulerCacheRecorder) SetAccount(context.Context, *service.Account) error {
	r.setCalls++
	return nil
}

func (m oneClickAccountNoteMutationsMatcher) Match(value driver.Value) bool {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		return false
	}
	var got []oneClickAccountNoteMutation
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	if len(got) != len(m.want) {
		return false
	}
	for index := range got {
		if got[index] != m.want[index] {
			return false
		}
	}
	return true
}

func newOneClickAccountNotesRepositoryTestClient(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	return newOneClickAccountNotesRepositoryTestClientWithCache(t, nil)
}

func newOneClickAccountNotesRepositoryTestClientWithCache(
	t *testing.T,
	schedulerCache service.SchedulerCache,
) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, schedulerCache), mock
}

func TestListOneClickAccountNoteTargetsProjectsOnlyIdentityAndNotes(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(oneClickAccountNotesListProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(1), "one@example.com", nil, 0, false).
			AddRow(int64(2), "two@example.com", "", 0, false))
	repo := newAccountRepositoryWithSQL(nil, db, nil)

	targets, err := repo.ListOneClickAccountNoteTargets(context.Background(), []string{"two@example.com", "one@example.com"})
	require.NoError(t, err)
	require.Equal(t, []service.OneClickAccountNoteTarget{
		{ID: 1, Name: "one@example.com", Notes: nil},
		{ID: 2, Name: "two@example.com", Notes: stringPointerForOneClickAccountNotesTest("")},
	}, targets)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListOneClickAccountNoteTargetsRejectsOversizedStateProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(oneClickAccountNotesListProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(0), "", nil, 0, true))
	repo := newAccountRepositoryWithSQL(nil, db, nil)

	targets, err := repo.ListOneClickAccountNoteTargets(context.Background(), []string{"owner@example.com"})
	require.Nil(t, targets)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRejectsOversizedStateSentinelBeforeUpdate(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	mock.ExpectBegin()
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(0), "", nil, 0, true))
	mock.ExpectRollback()

	err := repo.ApplyOneClickAccountNotes(
		context.Background(),
		[]string{"owner@example.com"},
		[]service.OneClickAccountNoteUpdate{{AccountID: 1, ExpectedName: "owner@example.com", Notes: "desired"}},
	)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesLocksEveryMatchAndUpdatesChangedAccountsOnce(t *testing.T) {
	cache := &oneClickAccountNotesSchedulerCacheRecorder{}
	repo, mock := newOneClickAccountNotesRepositoryTestClientWithCache(t, cache)
	unchanged := "two@example.com---same"
	old := "old-note"
	newLine := "  one@example.com---https://mail.example/open?token=secret  "

	mock.ExpectBegin()
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(1), "ONE@example.com", old, len(old), false).
			AddRow(int64(2), "two@example.com", unchanged, len(unchanged), false))
	mock.ExpectExec(oneClickAccountNotesOnlyUpdateQueryPattern).
		WithArgs(oneClickAccountNoteMutationsMatcher{want: []oneClickAccountNoteMutation{{ID: 1, Notes: newLine}}}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.ApplyOneClickAccountNotes(
		context.Background(),
		[]string{"two@example.com", "one@example.com"},
		[]service.OneClickAccountNoteUpdate{
			{AccountID: 2, ExpectedName: "two@example.com", ExpectedNotes: &unchanged, Notes: unchanged},
			{AccountID: 1, ExpectedName: "ONE@example.com", ExpectedNotes: &old, Notes: newLine},
		},
	)
	require.NoError(t, err)
	require.Zero(t, cache.setCalls, "notes-only updates must not rewrite complete scheduler snapshots")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesCommitsGuardForUnmatchedNamesWithoutWriting(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	mock.ExpectBegin()
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}))
	mock.ExpectCommit()

	err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"unmatched@example.com"}, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRejectsStaleTargetSetBeforeWriting(t *testing.T) {
	tests := []struct {
		name string
		rows *sqlmock.Rows
	}{
		{
			name: "target deleted",
			rows: sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}),
		},
		{
			name: "same-name target added",
			rows: sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
				AddRow(int64(1), "owner@example.com", "old", len("old"), false).
				AddRow(int64(2), "OWNER@example.com", nil, 0, false),
		},
		{
			name: "name changed by case",
			rows: sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
				AddRow(int64(1), "OWNER@example.com", "old", len("old"), false),
		},
		{
			name: "notes changed",
			rows: sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
				AddRow(int64(1), "owner@example.com", "new-current-note", len("new-current-note"), false),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
			old := "old"
			mock.ExpectBegin()
			expectOneClickAccountNotesTableLock(mock)
			mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
				WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
				WillReturnRows(test.rows)
			mock.ExpectRollback()

			err := repo.ApplyOneClickAccountNotes(
				context.Background(),
				[]string{"owner@example.com"},
				[]service.OneClickAccountNoteUpdate{{
					AccountID: 1, ExpectedName: "owner@example.com", ExpectedNotes: &old, Notes: "desired",
				}},
			)
			require.ErrorIs(t, err, service.ErrOneClickAccountNotesPreviewStale)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestApplyOneClickAccountNotesDistinguishesNullAndEmptyNotes(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	mock.ExpectBegin()
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(1), "owner@example.com", "", 0, false))
	mock.ExpectRollback()

	err := repo.ApplyOneClickAccountNotes(
		context.Background(),
		[]string{"owner@example.com"},
		[]service.OneClickAccountNoteUpdate{{
			AccountID: 1, ExpectedName: "owner@example.com", ExpectedNotes: nil, Notes: "desired",
		}},
	)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPreviewStale)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRollsBackPartialBatchResult(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	oldOne := "old-one"
	oldTwo := "old-two"
	mock.ExpectBegin()
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
			AddRow(int64(1), "one@example.com", oldOne, len(oldOne), false).
			AddRow(int64(2), "two@example.com", oldTwo, len(oldTwo), false))
	mock.ExpectExec(oneClickAccountNotesOnlyUpdateQueryPattern).
		WithArgs(oneClickAccountNoteMutationsMatcher{want: []oneClickAccountNoteMutation{
			{ID: 1, Notes: "one desired"},
			{ID: 2, Notes: "two desired"},
		}}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	err := repo.ApplyOneClickAccountNotes(
		context.Background(),
		[]string{"one@example.com", "two@example.com"},
		[]service.OneClickAccountNoteUpdate{
			{AccountID: 1, ExpectedName: "one@example.com", ExpectedNotes: &oldOne, Notes: "one desired"},
			{AccountID: 2, ExpectedName: "two@example.com", ExpectedNotes: &oldTwo, Notes: "two desired"},
		},
	)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPreviewStale)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRollsBackQueryOrUpdateFailure(t *testing.T) {
	t.Run("lock timeout setup", func(t *testing.T) {
		repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
		setupErr := errors.New("set lock timeout failed")
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(setOneClickAccountNotesLockTimeoutQuery)).WillReturnError(setupErr)
		mock.ExpectRollback()

		err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"owner@example.com"}, nil)
		require.ErrorIs(t, err, setupErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("table lock", func(t *testing.T) {
		repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
		lockErr := errors.New("table lock failed")
		mock.ExpectBegin()
		expectOneClickAccountNotesLockTimeout(mock)
		mock.ExpectExec(regexp.QuoteMeta(lockOneClickAccountNotesTableQuery)).WillReturnError(lockErr)
		mock.ExpectRollback()

		err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"owner@example.com"}, nil)
		require.ErrorIs(t, err, lockErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("table lock timeout", func(t *testing.T) {
		repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
		lockErr := &pq.Error{Code: pq.ErrorCode("55P03")}
		mock.ExpectBegin()
		expectOneClickAccountNotesLockTimeout(mock)
		mock.ExpectExec(regexp.QuoteMeta(lockOneClickAccountNotesTableQuery)).WillReturnError(lockErr)
		mock.ExpectRollback()

		err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"owner@example.com"}, nil)
		require.ErrorIs(t, err, service.ErrOneClickAccountNotesBusy)
		require.Equal(t, "ACCOUNT_NOTE_IMPORT_BUSY", infraerrors.Reason(err))
		require.Equal(t, 1, service.RetryAfterSecondsFromError(err))
		var gotPQErr *pq.Error
		require.ErrorAs(t, err, &gotPQErr)
		require.Same(t, lockErr, gotPQErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("lock query", func(t *testing.T) {
		repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
		queryErr := errors.New("lock failed")
		mock.ExpectBegin()
		expectOneClickAccountNotesTableLock(mock)
		mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
			WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
			WillReturnError(queryErr)
		mock.ExpectRollback()

		err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"owner@example.com"}, nil)
		require.ErrorIs(t, err, queryErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("batch update", func(t *testing.T) {
		repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
		updateErr := errors.New("update failed")
		mock.ExpectBegin()
		expectOneClickAccountNotesTableLock(mock)
		mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
			WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}).
				AddRow(int64(1), "owner@example.com", nil, 0, false))
		mock.ExpectExec(oneClickAccountNotesOnlyUpdateQueryPattern).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(updateErr)
		mock.ExpectRollback()

		err := repo.ApplyOneClickAccountNotes(
			context.Background(),
			[]string{"owner@example.com"},
			[]service.OneClickAccountNoteUpdate{{AccountID: 1, ExpectedName: "owner@example.com", Notes: "desired"}},
		)
		require.ErrorIs(t, err, updateErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestApplyOneClickAccountNotesUsesTableLockInsideExistingTransaction(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	mock.ExpectBegin()
	tx, err := repo.client.Tx(context.Background())
	require.NoError(t, err)
	expectOneClickAccountNotesTableLock(mock)
	mock.ExpectQuery(oneClickAccountNotesLockProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "notes", "notes_bytes", "state_too_large"}))

	err = repo.ApplyOneClickAccountNotes(
		dbent.NewTxContext(context.Background(), tx),
		[]string{"unmatched@example.com"},
		nil,
	)
	require.NoError(t, err)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRejectsInvalidPlanBeforeTransaction(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	updates := []service.OneClickAccountNoteUpdate{
		{AccountID: 1, ExpectedName: "owner@example.com", Notes: "one"},
		{AccountID: 1, ExpectedName: "owner@example.com", Notes: "two"},
	}

	err := repo.ApplyOneClickAccountNotes(context.Background(), []string{"owner@example.com"}, updates)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyOneClickAccountNotesRejectsOversizedMutationBeforeTransaction(t *testing.T) {
	repo, mock := newOneClickAccountNotesRepositoryTestClient(t)
	oversized := strings.Repeat("x", service.OneClickAccountNotesMaxMutationBytes+1)

	err := repo.ApplyOneClickAccountNotes(
		context.Background(),
		[]string{"owner@example.com"},
		[]service.OneClickAccountNoteUpdate{{AccountID: 1, ExpectedName: "owner@example.com", Notes: oversized}},
	)
	require.ErrorIs(t, err, service.ErrOneClickAccountNotesPlanTooLarge)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListOneClickAccountNoteTargetsPropagatesQueryFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	queryErr := errors.New("query failed")
	mock.ExpectQuery(oneClickAccountNotesListProjectionQueryPattern).
		WithArgs(sqlmock.AnyArg(), service.OneClickAccountNotesMaxMatchedAccounts+1, service.OneClickAccountNotesMaxTargetStateBytes).
		WillReturnError(queryErr)
	repo := newAccountRepositoryWithSQL(nil, db, nil)

	targets, err := repo.ListOneClickAccountNoteTargets(context.Background(), []string{"owner@example.com"})
	require.Nil(t, targets)
	require.ErrorIs(t, err, queryErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func stringPointerForOneClickAccountNotesTest(value string) *string {
	return &value
}

func expectOneClickAccountNotesTableLock(mock sqlmock.Sqlmock) {
	expectOneClickAccountNotesLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("LOCK TABLE accounts IN EXCLUSIVE MODE")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectOneClickAccountNotesLockTimeout(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta(setOneClickAccountNotesLockTimeoutQuery)).
		WillReturnResult(sqlmock.NewResult(0, 0))
}
