//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// hashedTestValue returns a unique SHA-256 hex string (64 chars) that fits VARCHAR(64) columns.
func hashedTestValue(t *testing.T, prefix string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(uniqueTestValue(t, prefix)))
	return hex.EncodeToString(sum[:])
}

func TestIdempotencyRepo_CreateProcessing_CompeteSameKey(t *testing.T) {
	tx := testTx(t)
	repo := &idempotencyRepository{sql: tx}
	ctx := context.Background()

	now := time.Now().UTC()
	record := &service.IdempotencyRecord{
		Scope:              uniqueTestValue(t, "idem-scope-create"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash"),
		RequestFingerprint: hashedTestValue(t, "idem-fp"),
		Status:             service.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(30 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)
	require.NotZero(t, record.ID)

	duplicate := &service.IdempotencyRecord{
		Scope:              record.Scope,
		IdempotencyKeyHash: record.IdempotencyKeyHash,
		RequestFingerprint: hashedTestValue(t, "idem-fp-other"),
		Status:             service.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(30 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err = repo.CreateProcessing(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, owner, "same scope+key hash should be de-duplicated")
}

func TestIdempotencyRepo_TryReclaim_StatusAndLockWindow(t *testing.T) {
	tx := testTx(t)
	repo := &idempotencyRepository{sql: tx}
	ctx := context.Background()

	now := time.Now().UTC()
	record := &service.IdempotencyRecord{
		Scope:              uniqueTestValue(t, "idem-scope-reclaim"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash-reclaim"),
		RequestFingerprint: hashedTestValue(t, "idem-fp-reclaim"),
		Status:             service.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(10 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)

	require.NoError(t, repo.MarkFailedRetryable(
		ctx,
		record.ID,
		*record.LockedUntil,
		"RETRYABLE_FAILURE",
		now.Add(-2*time.Second),
		now.Add(24*time.Hour),
	))

	newLockedUntil := now.Add(20 * time.Second)
	reclaimed, err := repo.TryReclaim(
		ctx,
		record.ID,
		service.IdempotencyStatusFailedRetryable,
		record.RequestFingerprint,
		now,
		newLockedUntil,
		now.Add(24*time.Hour),
	)
	require.NoError(t, err)
	require.True(t, reclaimed, "failed_retryable + expired lock should allow reclaim")

	got, err := repo.GetByScopeAndKeyHash(ctx, record.Scope, record.IdempotencyKeyHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, service.IdempotencyStatusProcessing, got.Status)
	require.NotNil(t, got.LockedUntil)
	require.True(t, got.LockedUntil.After(now))

	require.NoError(t, repo.MarkFailedRetryable(
		ctx,
		record.ID,
		*got.LockedUntil,
		"RETRYABLE_FAILURE",
		now.Add(20*time.Second),
		now.Add(24*time.Hour),
	))

	reclaimed, err = repo.TryReclaim(
		ctx,
		record.ID,
		service.IdempotencyStatusFailedRetryable,
		record.RequestFingerprint,
		now,
		now.Add(40*time.Second),
		now.Add(24*time.Hour),
	)
	require.NoError(t, err)
	require.False(t, reclaimed, "within lock window should not reclaim")
}

func TestIdempotencyRepo_StatusTransition_ToSucceeded(t *testing.T) {
	tx := testTx(t)
	repo := &idempotencyRepository{sql: tx}
	ctx := context.Background()

	now := time.Now().UTC()
	record := &service.IdempotencyRecord{
		Scope:              uniqueTestValue(t, "idem-scope-success"),
		IdempotencyKeyHash: hashedTestValue(t, "idem-hash-success"),
		RequestFingerprint: hashedTestValue(t, "idem-fp-success"),
		Status:             service.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(10 * time.Second)),
		ExpiresAt:          now.Add(24 * time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)

	require.NoError(t, repo.MarkSucceeded(ctx, record.ID, *record.LockedUntil, 200, `{"ok":true}`, now.Add(24*time.Hour)))

	got, err := repo.GetByScopeAndKeyHash(ctx, record.Scope, record.IdempotencyKeyHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, service.IdempotencyStatusSucceeded, got.Status)
	require.NotNil(t, got.ResponseStatus)
	require.Equal(t, 200, *got.ResponseStatus)
	require.NotNil(t, got.ResponseBody)
	require.Equal(t, `{"ok":true}`, *got.ResponseBody)
	require.Nil(t, got.LockedUntil)
}

func TestIdempotencyRepo_LeaseFenceRejectsStaleTerminalTransitions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		succeed bool
	}{
		{name: "succeeded", succeed: true},
		{name: "failed_retryable", succeed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := NewIdempotencyRepository(integrationEntClient, integrationDB).(*idempotencyRepository)
			scope := uniqueTestValue(t, "idem-lease-fence-"+tc.name)
			keyHash := hashedTestValue(t, "idem-lease-fence-"+tc.name)
			t.Cleanup(func() {
				_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope = $1", scope)
			})

			now := time.Now().UTC()
			record := &service.IdempotencyRecord{
				Scope:              scope,
				IdempotencyKeyHash: keyHash,
				RequestFingerprint: hashedTestValue(t, "idem-lease-fence-fp-"+tc.name),
				Status:             service.IdempotencyStatusProcessing,
				LockedUntil:        ptrTime(now.Add(-time.Second)),
				ExpiresAt:          now.Add(time.Hour),
			}
			owner, err := repo.CreateProcessing(ctx, record)
			require.NoError(t, err)
			require.True(t, owner)
			require.NotNil(t, record.LockedUntil)
			staleToken := *record.LockedUntil

			newToken := now.Add(time.Minute).Truncate(time.Microsecond)
			reclaimed, err := repo.TryReclaim(
				ctx,
				record.ID,
				service.IdempotencyStatusProcessing,
				record.RequestFingerprint,
				now,
				newToken,
				now.Add(time.Hour),
			)
			require.NoError(t, err)
			require.True(t, reclaimed)

			if tc.succeed {
				err = repo.MarkSucceeded(ctx, record.ID, staleToken, 200, `{"stale":true}`, now.Add(time.Hour))
			} else {
				err = repo.MarkFailedRetryable(ctx, record.ID, staleToken, "STALE_FAILURE", now.Add(time.Second), now.Add(time.Hour))
			}
			require.Error(t, err, "a reclaimed lease must reject the previous owner's terminal write")

			current, err := repo.GetByScopeAndKeyHash(ctx, scope, keyHash)
			require.NoError(t, err)
			require.NotNil(t, current)
			require.Equal(t, service.IdempotencyStatusProcessing, current.Status)
			require.NotNil(t, current.LockedUntil)
			require.True(t, current.LockedUntil.Equal(newToken))

			if tc.succeed {
				require.NoError(t, repo.MarkSucceeded(ctx, record.ID, *current.LockedUntil, 200, `{"new":true}`, now.Add(time.Hour)))
			} else {
				require.NoError(t, repo.MarkFailedRetryable(ctx, record.ID, *current.LockedUntil, "NEW_FAILURE", now.Add(time.Second), now.Add(time.Hour)))
			}
		})
	}
}

func TestIdempotencyRepoWithinTransactionAndTerminalCAS(t *testing.T) {
	ctx := context.Background()
	repo := NewIdempotencyRepository(integrationEntClient, integrationDB).(*idempotencyRepository)
	scope := uniqueTestValue(t, "idem-scope-transaction")
	keyHash := hashedTestValue(t, "idem-hash-transaction")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope = $1", scope)
	})

	now := time.Now().UTC()
	record := &service.IdempotencyRecord{
		Scope:              scope,
		IdempotencyKeyHash: keyHash,
		RequestFingerprint: hashedTestValue(t, "idem-fp-transaction"),
		Status:             service.IdempotencyStatusProcessing,
		LockedUntil:        ptrTime(now.Add(30 * time.Second)),
		ExpiresAt:          now.Add(time.Hour),
	}
	owner, err := repo.CreateProcessing(ctx, record)
	require.NoError(t, err)
	require.True(t, owner)
	claimLockedUntil := *record.LockedUntil

	rollbackErr := errors.New("synthetic transaction rollback")
	err = repo.WithinTransaction(ctx, func(txCtx context.Context, txRepo service.IdempotencyRepository) error {
		require.NotNil(t, dbent.TxFromContext(txCtx))
		require.NoError(t, txRepo.MarkSucceeded(txCtx, record.ID, claimLockedUntil, 200, `{"rolled_back":true}`, now.Add(time.Hour)))
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	got, err := repo.GetByScopeAndKeyHash(ctx, scope, keyHash)
	require.NoError(t, err)
	require.Equal(t, service.IdempotencyStatusProcessing, got.Status)
	require.Nil(t, got.ResponseBody)

	require.NoError(t, repo.WithinTransaction(ctx, func(txCtx context.Context, txRepo service.IdempotencyRepository) error {
		require.NotNil(t, dbent.TxFromContext(txCtx))
		return txRepo.MarkSucceeded(txCtx, record.ID, claimLockedUntil, 200, `{"committed":true}`, now.Add(time.Hour))
	}))

	got, err = repo.GetByScopeAndKeyHash(ctx, scope, keyHash)
	require.NoError(t, err)
	require.Equal(t, service.IdempotencyStatusSucceeded, got.Status)
	require.NotNil(t, got.ResponseBody)
	require.Equal(t, `{"committed":true}`, *got.ResponseBody)

	require.Error(t, repo.MarkFailedRetryable(ctx, record.ID, claimLockedUntil, "LATE_FAILURE", now.Add(time.Minute), now.Add(time.Hour)))
	require.Error(t, repo.MarkSucceeded(ctx, record.ID+999999, claimLockedUntil, 200, `{}`, now.Add(time.Hour)))
	unchanged, err := repo.GetByScopeAndKeyHash(ctx, scope, keyHash)
	require.NoError(t, err)
	require.Equal(t, service.IdempotencyStatusSucceeded, unchanged.Status)
	require.Equal(t, `{"committed":true}`, *unchanged.ResponseBody)
}

type failMarkSucceededIdempotencyRepository struct {
	service.IdempotencyRepository
}

func (failMarkSucceededIdempotencyRepository) MarkSucceeded(context.Context, int64, time.Time, int, string, time.Time) error {
	return errors.New("synthetic terminal transition failure")
}

type failMarkSucceededTransactionalIdempotencyRepository struct {
	service.TransactionalIdempotencyRepository
}

func (r failMarkSucceededTransactionalIdempotencyRepository) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	return r.TransactionalIdempotencyRepository.WithinTransaction(
		ctx,
		func(txCtx context.Context, txRepo service.IdempotencyRepository) error {
			return execute(txCtx, failMarkSucceededIdempotencyRepository{IdempotencyRepository: txRepo})
		},
	)
}

func TestOneClickAccountNotesServiceAndIdempotencyTerminalStateAreAtomic(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	initialNote := "before transactional import"
	accountName := fmt.Sprintf("atomic-one-click-%d@example.com", time.Now().UnixNano())
	account := createOneClickAccountNotesIntegrationAccount(t, ctx, accountRepo, accountName, &initialNote)
	desiredNote := accountName + "---after transactional import"
	content := []byte(desiredNote)
	adminService := newOneClickAccountNotesIntegrationAdminService(accountRepo)
	preview, err := adminService.PreviewOneClickAccountNotes(ctx, content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.NotEmpty(t, preview.PreviewDigest)

	baseRepo := NewIdempotencyRepository(integrationEntClient, integrationDB)
	transactionalRepo, ok := baseRepo.(service.TransactionalIdempotencyRepository)
	require.True(t, ok)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.FailedRetryBackoff = time.Millisecond

	apply := func(txCtx context.Context) (any, error) {
		require.NotNil(t, dbent.TxFromContext(txCtx))
		return adminService.ApplyOneClickAccountNotes(txCtx, content, preview.PreviewDigest)
	}

	failureScope := uniqueTestValue(t, "one-click-idem-rollback")
	failureKey := "one-click-rollback-key"
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope IN ($1, $2)", failureScope, failureScope+"-success")
	})
	failingCoordinator := service.NewIdempotencyCoordinator(
		failMarkSucceededTransactionalIdempotencyRepository{TransactionalIdempotencyRepository: transactionalRepo},
		cfg,
	)
	failureOpts := service.IdempotencyExecuteOptions{
		Scope:          failureScope,
		ActorScope:     "admin:atomic-test",
		Method:         "POST",
		Route:          "/api/v1/admin/accounts/one-click-notes/apply",
		IdempotencyKey: failureKey,
		Payload:        map[string]any{"file_sha256": "synthetic"},
		RequireKey:     true,
	}
	_, err = failingCoordinator.ExecuteTransactional(ctx, failureOpts, apply)
	require.Error(t, err)
	require.Equal(t, initialNote, readOneClickAccountNotesIntegrationNote(t, ctx, account.ID).String)

	failureRecord, err := baseRepo.GetByScopeAndKeyHash(
		ctx,
		failureScope,
		service.HashActorScopedIdempotencyKey(failureOpts.ActorScope, failureKey),
	)
	require.NoError(t, err)
	require.NotNil(t, failureRecord)
	require.Equal(t, service.IdempotencyStatusFailedRetryable, failureRecord.Status)

	successOpts := failureOpts
	successOpts.Scope = failureScope + "-success"
	successOpts.IdempotencyKey = "one-click-success-key"
	successCoordinator := service.NewIdempotencyCoordinator(baseRepo, cfg)
	result, err := successCoordinator.ExecuteTransactional(ctx, successOpts, apply)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, desiredNote, readOneClickAccountNotesIntegrationNote(t, ctx, account.ID).String)

	successRecord, err := baseRepo.GetByScopeAndKeyHash(
		ctx,
		successOpts.Scope,
		service.HashActorScopedIdempotencyKey(successOpts.ActorScope, successOpts.IdempotencyKey),
	)
	require.NoError(t, err)
	require.NotNil(t, successRecord)
	require.Equal(t, service.IdempotencyStatusSucceeded, successRecord.Status)
}

type blockingTransactionalIdempotencyRepository struct {
	service.TransactionalIdempotencyRepository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type transactionalCompletionGate struct {
	service.TransactionalIdempotencyRepository
	completed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *transactionalCompletionGate) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	err := r.TransactionalIdempotencyRepository.WithinTransaction(ctx, execute)
	r.once.Do(func() { close(r.completed) })
	<-r.release
	return err
}

func TestTransactionalIdempotencyStaleFailureCannotOverwriteReclaimedLease(t *testing.T) {
	ctx := context.Background()
	baseRepo := NewIdempotencyRepository(integrationEntClient, integrationDB)
	transactionalRepo, ok := baseRepo.(service.TransactionalIdempotencyRepository)
	require.True(t, ok)

	scope := uniqueTestValue(t, "idem-stale-failure-owner")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope = $1", scope)
	})

	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.ProcessingTimeout = 5 * time.Second
	const idempotencyKey = "stale-failure-owner-key"
	opts := service.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     "admin:stale-failure-owner",
		Method:         "POST",
		Route:          "/transactional-stale-failure-owner",
		IdempotencyKey: idempotencyKey,
		Payload:        map[string]any{"case": "stale-failure"},
		RequireKey:     true,
	}
	keyHash := service.HashActorScopedIdempotencyKey(opts.ActorScope, idempotencyKey)

	oldCompleted := make(chan struct{})
	oldRelease := make(chan struct{})
	oldGate := &transactionalCompletionGate{
		TransactionalIdempotencyRepository: transactionalRepo,
		completed:                          oldCompleted,
		release:                            oldRelease,
	}
	oldCoordinator := service.NewIdempotencyCoordinator(oldGate, cfg)
	oldOwnerErr := errors.New("old owner executor failed")
	oldDone := make(chan error, 1)
	go func() {
		_, err := oldCoordinator.ExecuteTransactional(ctx, opts, func(context.Context) (any, error) {
			return nil, oldOwnerErr
		})
		oldDone <- err
	}()

	select {
	case <-oldCompleted:
	case <-time.After(5 * time.Second):
		t.Fatal("old owner did not finish and roll back its transaction")
	}

	_, err := integrationDB.ExecContext(ctx, `
		UPDATE idempotency_records
		SET locked_until = NOW() - INTERVAL '1 second',
			expires_at = NOW() - INTERVAL '1 second'
		WHERE scope = $1 AND idempotency_key_hash = $2
	`, scope, keyHash)
	require.NoError(t, err)

	newEntered := make(chan struct{})
	newRelease := make(chan struct{})
	newGate := &blockingTransactionalIdempotencyRepository{
		TransactionalIdempotencyRepository: transactionalRepo,
		entered:                            newEntered,
		release:                            newRelease,
	}
	newCoordinator := service.NewIdempotencyCoordinator(newGate, cfg)
	newDone := make(chan error, 1)
	go func() {
		_, err := newCoordinator.ExecuteTransactional(ctx, opts, func(context.Context) (any, error) {
			return map[string]any{"owner": "new"}, nil
		})
		newDone <- err
	}()

	select {
	case <-newEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("new owner did not reclaim the expired lease before its transaction")
	}

	close(oldRelease)
	select {
	case err := <-oldDone:
		require.ErrorIs(t, err, oldOwnerErr)
	case <-time.After(5 * time.Second):
		t.Fatal("old owner did not finish after its stale terminal write")
	}

	current, err := baseRepo.GetByScopeAndKeyHash(ctx, scope, keyHash)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, service.IdempotencyStatusProcessing, current.Status)
	require.NotNil(t, current.LockedUntil)

	close(newRelease)
	select {
	case err := <-newDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("new owner did not complete after the old owner was fenced")
	}

	final, err := baseRepo.GetByScopeAndKeyHash(ctx, scope, keyHash)
	require.NoError(t, err)
	require.NotNil(t, final)
	require.Equal(t, service.IdempotencyStatusSucceeded, final.Status)
}

func (r *blockingTransactionalIdempotencyRepository) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.TransactionalIdempotencyRepository.WithinTransaction(ctx, execute)
}

func TestTransactionalIdempotencyExpiredLeaseDoesNotAllowOldOwnerDoubleExecution(t *testing.T) {
	ctx := context.Background()
	baseRepo := NewIdempotencyRepository(integrationEntClient, integrationDB)
	transactionalRepo, ok := baseRepo.(service.TransactionalIdempotencyRepository)
	require.True(t, ok)

	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	initialNote := "before lease reclaim"
	account := createOneClickAccountNotesIntegrationAccount(
		t,
		ctx,
		accountRepo,
		fmt.Sprintf("lease-reclaim-%d@example.com", time.Now().UnixNano()),
		&initialNote,
	)
	desiredNote := "after lease reclaim"
	scope := uniqueTestValue(t, "idem-lease-fencing")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope = $1", scope)
	})

	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.ProcessingTimeout = 80 * time.Millisecond
	opts := service.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     "admin:lease-fencing",
		Method:         "POST",
		Route:          "/transactional-lease-fencing",
		IdempotencyKey: "same-key",
		Payload:        map[string]any{"account_id": account.ID},
		RequireKey:     true,
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	oldOwnerRepo := &blockingTransactionalIdempotencyRepository{
		TransactionalIdempotencyRepository: transactionalRepo,
		entered:                            entered,
		release:                            release,
	}
	oldCoordinator := service.NewIdempotencyCoordinator(oldOwnerRepo, cfg)
	newCoordinator := service.NewIdempotencyCoordinator(baseRepo, cfg)

	var executions atomic.Int32
	execute := func(txCtx context.Context) (any, error) {
		executions.Add(1)
		tx := dbent.TxFromContext(txCtx)
		if tx == nil {
			return nil, errors.New("transaction context missing")
		}
		result, err := tx.Client().ExecContext(
			txCtx,
			"UPDATE accounts SET notes = $1, updated_at = NOW() WHERE id = $2",
			desiredNote,
			account.ID,
		)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			return nil, errors.New("synthetic account update was not applied")
		}
		return map[string]any{"updated_accounts": 1}, nil
	}

	type executionResult struct {
		result *service.IdempotencyExecuteResult
		err    error
	}
	oldDone := make(chan executionResult, 1)
	go func() {
		result, err := oldCoordinator.ExecuteTransactional(ctx, opts, execute)
		oldDone <- executionResult{result: result, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old owner did not reach transaction boundary")
	}
	time.Sleep(cfg.ProcessingTimeout + 40*time.Millisecond)

	newResult, err := newCoordinator.ExecuteTransactional(ctx, opts, execute)
	require.NoError(t, err)
	require.NotNil(t, newResult)
	require.False(t, newResult.Replayed)

	releaseOnce.Do(func() { close(release) })
	select {
	case oldResult := <-oldDone:
		require.NoError(t, oldResult.err)
		require.NotNil(t, oldResult.result)
		require.True(t, oldResult.result.Replayed)
	case <-time.After(5 * time.Second):
		t.Fatal("old owner did not finish after release")
	}

	require.Equal(t, int32(1), executions.Load())
	require.Equal(t, desiredNote, readOneClickAccountNotesIntegrationNote(t, ctx, account.ID).String)
}
