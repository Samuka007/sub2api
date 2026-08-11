package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestSystemOperationLockService_AcquireBusyAndRelease(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	lock1, err := svc.Acquire(context.Background(), "op-1")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	_, err = svc.Acquire(context.Background(), "op-2")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrSystemOperationBusy), infraerrors.Code(err))
	appErr := infraerrors.FromError(err)
	require.Equal(t, "op-1", appErr.Metadata["operation_id"])
	require.NotEmpty(t, appErr.Metadata["retry_after"])

	require.NoError(t, svc.Release(context.Background(), lock1, true, ""))

	lock2, err := svc.Acquire(context.Background(), "op-2")
	require.NoError(t, err)
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

func TestSystemOperationLockService_RenewLease(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  1200 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	time.Sleep(1500 * time.Millisecond)

	updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LockedUntil)
	require.True(t, updated.LockedUntil.After(initialLockedUntil), "locked_until should be renewed while lock is held")
}

type flakySystemLockRenewRepo struct {
	*inMemoryIdempotencyRepo
	extendCalls int32
}

func (r *flakySystemLockRenewRepo) ExtendProcessingLock(ctx context.Context, id int64, requestFingerprint string, expectedLockedUntil, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	call := atomic.AddInt32(&r.extendCalls, 1)
	if call == 1 {
		return false, errors.New("transient extend failure")
	}
	return r.inMemoryIdempotencyRepo.ExtendProcessingLock(ctx, id, requestFingerprint, expectedLockedUntil, newLockedUntil, newExpiresAt)
}

func TestSystemOperationLockService_RenewLeaseContinuesAfterTransientFailure(t *testing.T) {
	repo := &flakySystemLockRenewRepo{inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo()}
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  2400 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew-transient")
	require.NoError(t, err)
	require.NotNil(t, lock)
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	// 首次续租失败后，下一轮应继续尝试并成功更新锁过期时间。
	require.Eventually(t, func() bool {
		updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
		if updated == nil || updated.LockedUntil == nil {
			return false
		}
		return atomic.LoadInt32(&repo.extendCalls) >= 2 && updated.LockedUntil.After(initialLockedUntil)
	}, 4*time.Second, 100*time.Millisecond, "renew loop should continue after transient error")
}

type cancelAwareSystemLockRenewRepo struct {
	*inMemoryIdempotencyRepo
	started     chan struct{}
	startedOnce sync.Once
}

type delayedSystemLockRenewRepo struct {
	*inMemoryIdempotencyRepo
	started     chan struct{}
	continueRun chan struct{}
	finished    chan struct{}
	startedOnce sync.Once
}

type ambiguousCommitSystemLockRenewRepo struct {
	*inMemoryIdempotencyRepo
	extendCalls      int32
	readbackStarted  chan struct{}
	continueReadback chan struct{}
	readbackOnce     sync.Once
}

func (r *ambiguousCommitSystemLockRenewRepo) ExtendProcessingLock(ctx context.Context, id int64, requestFingerprint string, expectedLockedUntil, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	call := atomic.AddInt32(&r.extendCalls, 1)
	ok, err := r.inMemoryIdempotencyRepo.ExtendProcessingLock(ctx, id, requestFingerprint, expectedLockedUntil, newLockedUntil, newExpiresAt)
	if call == 1 && err == nil && ok {
		return false, errors.New("connection lost after commit")
	}
	return ok, err
}

func (r *ambiguousCommitSystemLockRenewRepo) GetByScopeAndKeyHash(ctx context.Context, scope, keyHash string) (*IdempotencyRecord, error) {
	if atomic.LoadInt32(&r.extendCalls) > 0 {
		r.readbackOnce.Do(func() { close(r.readbackStarted) })
		<-r.continueReadback
	}
	return r.inMemoryIdempotencyRepo.GetByScopeAndKeyHash(ctx, scope, keyHash)
}

func (r *delayedSystemLockRenewRepo) ExtendProcessingLock(ctx context.Context, id int64, requestFingerprint string, expectedLockedUntil, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	ok, err := r.inMemoryIdempotencyRepo.ExtendProcessingLock(ctx, id, requestFingerprint, expectedLockedUntil, newLockedUntil, newExpiresAt)
	r.startedOnce.Do(func() { close(r.started) })
	<-r.continueRun
	close(r.finished)
	return ok, err
}

func (r *cancelAwareSystemLockRenewRepo) ExtendProcessingLock(ctx context.Context, id int64, requestFingerprint string, expectedLockedUntil, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.startedOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	return false, ctx.Err()
}

func TestSystemOperationLockService_ReleaseFinalizesWhileCommittedRenewalResponseIsDelayed(t *testing.T) {
	repo := &delayedSystemLockRenewRepo{
		inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo(),
		started:                 make(chan struct{}),
		continueRun:             make(chan struct{}),
		finished:                make(chan struct{}),
	}
	var continueOnce sync.Once
	continueRenewal := func() {
		continueOnce.Do(func() { close(repo.continueRun) })
	}
	t.Cleanup(continueRenewal)

	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  3 * time.Second,
	})

	lock, err := svc.Acquire(context.Background(), "op-delayed-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)

	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not start")
	}

	released := make(chan error, 1)
	go func() {
		released <- svc.Release(context.Background(), lock, true, "")
	}()

	select {
	case err := <-released:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("release waited for the delayed renewal response")
	}

	record, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, IdempotencyStatusSucceeded, record.Status)

	continueRenewal()
	select {
	case <-repo.finished:
	case <-time.After(time.Second):
		t.Fatal("renewal did not finish after it was released")
	}
	record, err = repo.GetByScopeAndKeyHash(
		context.Background(),
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, IdempotencyStatusSucceeded, record.Status, "late renewal response must not revive the terminal record")
}

func TestSystemOperationLockService_ReleaseUsesCandidateAfterAmbiguousRenewalCommit(t *testing.T) {
	repo := &ambiguousCommitSystemLockRenewRepo{
		inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo(),
		readbackStarted:         make(chan struct{}),
		continueReadback:        make(chan struct{}),
	}
	var continueOnce sync.Once
	continueReadback := func() {
		continueOnce.Do(func() { close(repo.continueReadback) })
	}
	t.Cleanup(continueReadback)

	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  3 * time.Second,
	})
	lock, err := svc.Acquire(context.Background(), "op-ambiguous-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)

	select {
	case <-repo.readbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not attempt to reconcile the ambiguous commit")
	}

	released := make(chan error, 1)
	go func() {
		released <- svc.Release(context.Background(), lock, true, "")
	}()
	select {
	case err := <-released:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("release waited for the blocked renewal readback")
	}

	record, err := repo.inMemoryIdempotencyRepo.GetByScopeAndKeyHash(
		context.Background(),
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, IdempotencyStatusSucceeded, record.Status)

	continueReadback()
}

func TestSystemOperationLockService_ReleaseCancelsInFlightRenewal(t *testing.T) {
	repo := &cancelAwareSystemLockRenewRepo{
		inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo(),
		started:                 make(chan struct{}),
	}
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  3 * time.Second,
	})

	lock, err := svc.Acquire(context.Background(), "op-cancel-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)

	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not start")
	}

	releaseCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, svc.Release(releaseCtx, lock, true, ""))

	record, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, IdempotencyStatusSucceeded, record.Status)
}

func TestSystemOperationLockService_SameOperationIDRetryWhileRunning(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	lock1, err := svc.Acquire(context.Background(), "op-same")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	_, err = svc.Acquire(context.Background(), "op-same")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrSystemOperationBusy), infraerrors.Code(err))
	appErr := infraerrors.FromError(err)
	require.Equal(t, "op-same", appErr.Metadata["operation_id"])

	require.NoError(t, svc.Release(context.Background(), lock1, true, ""))

	lock2, err := svc.Acquire(context.Background(), "op-same")
	require.NoError(t, err)
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

func TestSystemOperationLockService_RecoverAfterLeaseExpired(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  300 * time.Millisecond,
	})

	lock1, err := svc.Acquire(context.Background(), "op-crashed")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	// 模拟实例异常：停止续租，不调用 Release。
	lock1.stopOnce.Do(func() {
		close(lock1.stopCh)
	})

	time.Sleep(450 * time.Millisecond)

	lock2, err := svc.Acquire(context.Background(), "op-recovered")
	require.NoError(t, err, "expired lease should allow a new operation to reclaim lock")
	require.NotNil(t, lock2)
	recovered, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, "op-recovered", recovered.RequestFingerprint, "reclaim must fence the new operation identity")
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

type systemLockRepoStub struct {
	createOwner bool
	createErr   error
	existing    *IdempotencyRecord
	getErr      error
	reclaimOK   bool
	reclaimErr  error
	markSuccErr error
	markFailErr error
}

func (s *systemLockRepoStub) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	if s.createErr != nil {
		return false, s.createErr
	}
	return s.createOwner, nil
}

func (s *systemLockRepoStub) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return cloneRecord(s.existing), nil
}

func (s *systemLockRepoStub) TryReclaim(context.Context, int64, string, string, time.Time, time.Time, time.Time) (bool, error) {
	if s.reclaimErr != nil {
		return false, s.reclaimErr
	}
	return s.reclaimOK, nil
}

func (s *systemLockRepoStub) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (s *systemLockRepoStub) MarkSucceeded(context.Context, int64, time.Time, int, string, time.Time) error {
	return s.markSuccErr
}

func (s *systemLockRepoStub) MarkFailedRetryable(context.Context, int64, time.Time, string, time.Time, time.Time) error {
	return s.markFailErr
}

func (s *systemLockRepoStub) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func TestSystemOperationLockService_InputAndStoreErrorBranches(t *testing.T) {
	var nilSvc *SystemOperationLockService
	_, err := nilSvc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	svc := &SystemOperationLockService{repo: nil}
	_, err = svc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	svc = NewSystemOperationLockService(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	_, err = svc.Acquire(context.Background(), "")
	require.Error(t, err)
	require.Equal(t, "SYSTEM_OPERATION_ID_REQUIRED", infraerrors.Reason(err))

	badStore := &systemLockRepoStub{createErr: errors.New("db down")}
	svc = NewSystemOperationLockService(badStore, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	_, err = svc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))
}

func TestSystemOperationLockService_ExistingNilAndReclaimBranches(t *testing.T) {
	now := time.Now()
	repo := &systemLockRepoStub{
		createOwner: false,
	}
	svc := NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	_, err := svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.existing = &IdempotencyRecord{
		ID:                 1,
		Scope:              systemOperationLockScope,
		IdempotencyKeyHash: HashIdempotencyKey(systemOperationLockKey),
		RequestFingerprint: "other-op",
		Status:             IdempotencyStatusFailedRetryable,
		LockedUntil:        ptrTime(now.Add(-time.Second)),
		ExpiresAt:          now.Add(time.Hour),
	}
	repo.reclaimErr = errors.New("reclaim failed")
	_, err = svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.reclaimErr = nil
	repo.reclaimOK = false
	_, err = svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrSystemOperationBusy), infraerrors.Code(err))
}

func TestSystemOperationLockService_ReleaseBranchesAndOperationID(t *testing.T) {
	require.Equal(t, "", (*SystemOperationLock)(nil).OperationID())

	svc := NewSystemOperationLockService(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	lock, err := svc.Acquire(context.Background(), "op")
	require.NoError(t, err)
	require.NotNil(t, lock)

	require.NoError(t, svc.Release(context.Background(), lock, false, ""))

	svc = NewSystemOperationLockService(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	lock, err = svc.Acquire(context.Background(), "op-success")
	require.NoError(t, err)
	require.NoError(t, svc.Release(context.Background(), lock, true, ""))

	repo := &systemLockRepoStub{
		createOwner: true,
		markSuccErr: errors.New("mark succeeded failed"),
		markFailErr: errors.New("mark failed failed"),
	}
	svc = NewSystemOperationLockService(repo, IdempotencyConfig{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	lock = &SystemOperationLock{recordID: 1, operationID: "op2", stopCh: make(chan struct{})}
	require.Error(t, svc.Release(context.Background(), lock, true, ""))
	lock = &SystemOperationLock{recordID: 1, operationID: "op3", stopCh: make(chan struct{})}
	require.Error(t, svc.Release(context.Background(), lock, false, "BAD"))

	var nilLockSvc *SystemOperationLockService
	require.NoError(t, nilLockSvc.Release(context.Background(), nil, true, ""))

	err = svc.busyError("", nil, time.Now())
	require.Equal(t, infraerrors.Code(ErrSystemOperationBusy), infraerrors.Code(err))
}
