package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type inMemoryIdempotencyRepo struct {
	mu     sync.Mutex
	txMu   sync.Mutex
	nextID int64
	data   map[string]*IdempotencyRecord
}

func (r *inMemoryIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, IdempotencyRepository) error,
) error {
	r.txMu.Lock()
	defer r.txMu.Unlock()
	txRepo := cloneInMemoryIdempotencyRepo(r)

	if err := execute(ctx, txRepo); err != nil {
		return err
	}
	commitInMemoryIdempotencyRepo(r, txRepo)
	return nil
}

func cloneInMemoryIdempotencyRepo(source *inMemoryIdempotencyRepo) *inMemoryIdempotencyRepo {
	source.mu.Lock()
	defer source.mu.Unlock()
	cloned := &inMemoryIdempotencyRepo{
		nextID: source.nextID,
		data:   make(map[string]*IdempotencyRecord, len(source.data)),
	}
	for key, record := range source.data {
		cloned.data[key] = cloneRecord(record)
	}
	return cloned
}

func commitInMemoryIdempotencyRepo(destination, source *inMemoryIdempotencyRepo) {
	source.mu.Lock()
	committedNextID := source.nextID
	committedData := make(map[string]*IdempotencyRecord, len(source.data))
	for key, record := range source.data {
		committedData[key] = cloneRecord(record)
	}
	source.mu.Unlock()

	destination.mu.Lock()
	destination.nextID = committedNextID
	destination.data = committedData
	destination.mu.Unlock()
}

func newInMemoryIdempotencyRepo() *inMemoryIdempotencyRepo {
	return &inMemoryIdempotencyRepo{
		nextID: 1,
		data:   make(map[string]*IdempotencyRecord),
	}
}

func (r *inMemoryIdempotencyRepo) key(scope, hash string) string {
	return scope + "|" + hash
}

func cloneRecord(in *IdempotencyRecord) *IdempotencyRecord {
	if in == nil {
		return nil
	}
	out := *in
	if in.ResponseStatus != nil {
		v := *in.ResponseStatus
		out.ResponseStatus = &v
	}
	if in.ResponseBody != nil {
		v := *in.ResponseBody
		out.ResponseBody = &v
	}
	if in.ErrorReason != nil {
		v := *in.ErrorReason
		out.ErrorReason = &v
	}
	if in.LockedUntil != nil {
		v := *in.LockedUntil
		out.LockedUntil = &v
	}
	return &out
}

func (r *inMemoryIdempotencyRepo) CreateProcessing(_ context.Context, record *IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.key(record.Scope, record.IdempotencyKeyHash)
	if _, ok := r.data[k]; ok {
		return false, nil
	}
	rec := cloneRecord(record)
	rec.ID = r.nextID
	rec.CreatedAt = time.Now()
	rec.UpdatedAt = rec.CreatedAt
	r.nextID++
	r.data[k] = rec
	record.ID = rec.ID
	record.CreatedAt = rec.CreatedAt
	record.UpdatedAt = rec.UpdatedAt
	return true, nil
}

func (r *inMemoryIdempotencyRepo) GetByScopeAndKeyHash(_ context.Context, scope, keyHash string) (*IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRecord(r.data[r.key(scope, keyHash)]), nil
}

func (r *inMemoryIdempotencyRepo) TryReclaim(_ context.Context, id int64, fromStatus, newRequestFingerprint string, now, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != fromStatus {
			return false, nil
		}
		if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
			return false, nil
		}
		rec.Status = IdempotencyStatusProcessing
		rec.RequestFingerprint = newRequestFingerprint
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		rec.ErrorReason = nil
		rec.UpdatedAt = time.Now()
		return true, nil
	}
	return false, nil
}

func (r *inMemoryIdempotencyRepo) ExtendProcessingLock(_ context.Context, id int64, requestFingerprint string, expectedLockedUntil, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != IdempotencyStatusProcessing || rec.RequestFingerprint != requestFingerprint || rec.LockedUntil == nil || !rec.LockedUntil.Equal(expectedLockedUntil) {
			return false, nil
		}
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		rec.UpdatedAt = time.Now()
		return true, nil
	}
	return false, nil
}

func (r *inMemoryIdempotencyRepo) MarkSucceeded(_ context.Context, id int64, expectedLockedUntil time.Time, responseStatus int, responseBody string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != IdempotencyStatusProcessing || rec.LockedUntil == nil || !rec.LockedUntil.Equal(expectedLockedUntil) {
			return errors.New("record is not processing")
		}
		rec.Status = IdempotencyStatusSucceeded
		rec.LockedUntil = nil
		rec.ExpiresAt = expiresAt
		rec.UpdatedAt = time.Now()
		rec.ErrorReason = nil
		rec.ResponseStatus = &responseStatus
		rec.ResponseBody = &responseBody
		return nil
	}
	return errors.New("record not found")
}

func (r *inMemoryIdempotencyRepo) MarkFailedRetryable(_ context.Context, id int64, expectedLockedUntil time.Time, errorReason string, lockedUntil, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != IdempotencyStatusProcessing || rec.LockedUntil == nil || !rec.LockedUntil.Equal(expectedLockedUntil) {
			return errors.New("record is not processing")
		}
		rec.Status = IdempotencyStatusFailedRetryable
		rec.LockedUntil = &lockedUntil
		rec.ExpiresAt = expiresAt
		rec.UpdatedAt = time.Now()
		rec.ErrorReason = &errorReason
		return nil
	}
	return errors.New("record not found")
}

func (r *inMemoryIdempotencyRepo) DeleteExpired(_ context.Context, now time.Time, _ int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var deleted int64
	for k, rec := range r.data {
		if !rec.ExpiresAt.After(now) {
			delete(r.data, k)
			deleted++
		}
	}
	return deleted, nil
}

func TestIdempotencyCoordinator_RequireKey(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:      "test.scope",
		Method:     "POST",
		Route:      "/test",
		ActorScope: "admin:1",
		RequireKey: true,
		Payload:    map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(err), infraerrors.Code(ErrIdempotencyKeyRequired))
}

func TestIdempotencyCoordinator_ReplaySucceededResult(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	execCount := 0
	exec := func(ctx context.Context) (any, error) {
		execCount++
		return map[string]any{"count": execCount}, nil
	}

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-1",
		Payload:        map[string]any{"a": 1},
	}

	first, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	second, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 1, execCount, "second request should replay without executing business logic")

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ClaimTotal)
	require.Equal(t, uint64(1), metrics.ReplayTotal)
}

func TestIdempotencyCoordinatorSameKeyIsIsolatedByActor(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	execCount := 0
	exec := func(context.Context) (any, error) {
		execCount++
		return map[string]any{"count": execCount}, nil
	}
	base := IdempotencyExecuteOptions{
		Scope: "test.scope", Method: "POST", Route: "/test",
		RequireKey: true, IdempotencyKey: "shared-key", Payload: map[string]any{"a": 1},
	}

	firstActor := base
	firstActor.ActorScope = "user:1"
	secondActor := base
	secondActor.ActorScope = "user:2"

	first, err := coordinator.Execute(context.Background(), firstActor, exec)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	second, err := coordinator.Execute(context.Background(), secondActor, exec)
	require.NoError(t, err)
	require.False(t, second.Replayed)
	require.Equal(t, 2, execCount)

	replayed, err := coordinator.Execute(context.Background(), firstActor, exec)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, 2, execCount)
}

func TestIdempotencyCoordinator_ReclaimExpiredSucceededRecord(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.expired",
		Method:         "POST",
		Route:          "/test/expired",
		ActorScope:     "user:99",
		RequireKey:     true,
		IdempotencyKey: "expired-case",
		Payload:        map[string]any{"k": "v"},
	}

	execCount := 0
	exec := func(ctx context.Context) (any, error) {
		execCount++
		return map[string]any{"count": execCount}, nil
	}

	first, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.False(t, first.Replayed)
	require.Equal(t, 1, execCount)

	keyHash := HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey)
	repo.mu.Lock()
	existing := repo.data[repo.key(opts.Scope, keyHash)]
	require.NotNil(t, existing)
	existing.ExpiresAt = time.Now().Add(-time.Second)
	repo.mu.Unlock()

	second, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.False(t, second.Replayed, "expired record should be reclaimed and execute business logic again")
	require.Equal(t, 2, execCount)

	third, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, third)
	require.True(t, third.Replayed)
	payload, ok := third.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2), payload["count"])

	metrics := GetIdempotencyMetricsSnapshot()
	require.GreaterOrEqual(t, metrics.ClaimTotal, uint64(2))
	require.GreaterOrEqual(t, metrics.ReplayTotal, uint64(1))
}

func TestIdempotencyCoordinator_SameKeyDifferentPayloadConflict(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-2",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)

	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-2",
		Payload:        map[string]any{"a": 2},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(err), infraerrors.Code(ErrIdempotencyKeyConflict))

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ConflictTotal)
}

func TestIdempotencyCoordinator_BackoffAfterRetryableFailure(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.FailedRetryBackoff = 2 * time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-3",
		Payload:        map[string]any{"a": 1},
	}

	_, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return nil, infraerrors.InternalServer("UPSTREAM_ERROR", "upstream error")
	})
	require.Error(t, err)

	_, err = coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(err), infraerrors.Code(ErrIdempotencyRetryBackoff))
	require.Greater(t, RetryAfterSecondsFromError(err), 0)

	metrics := GetIdempotencyMetricsSnapshot()
	require.GreaterOrEqual(t, metrics.RetryBackoffTotal, uint64(2))
	require.GreaterOrEqual(t, metrics.ConflictTotal, uint64(1))
	require.GreaterOrEqual(t, metrics.ProcessingDurationCount, uint64(1))
}

func TestIdempotencyCoordinator_ConcurrentSameKeySingleSideEffect(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ProcessingTimeout = 2 * time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.concurrent",
		Method:         "POST",
		Route:          "/test/concurrent",
		ActorScope:     "user:7",
		RequireKey:     true,
		IdempotencyKey: "concurrent-case",
		Payload:        map[string]any{"v": 1},
	}

	var execCount int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
				atomic.AddInt32(&execCount, 1)
				time.Sleep(80 * time.Millisecond)
				return map[string]any{"ok": true}, nil
			})
		}()
	}
	wg.Wait()

	replayed, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		atomic.AddInt32(&execCount, 1)
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, int32(1), atomic.LoadInt32(&execCount), "concurrent same-key requests should execute business side-effect once")

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ClaimTotal)
	require.Equal(t, uint64(1), metrics.ReplayTotal)
	require.GreaterOrEqual(t, metrics.ConflictTotal, uint64(1))
}

type failingIdempotencyRepo struct{}

func (failingIdempotencyRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return false, errors.New("store unavailable")
}
func (failingIdempotencyRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return nil, errors.New("store unavailable")
}
func (failingIdempotencyRepo) TryReclaim(context.Context, int64, string, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}
func (failingIdempotencyRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}
func (failingIdempotencyRepo) MarkSucceeded(context.Context, int64, time.Time, int, string, time.Time) error {
	return errors.New("store unavailable")
}
func (failingIdempotencyRepo) MarkFailedRetryable(context.Context, int64, time.Time, string, time.Time, time.Time) error {
	return errors.New("store unavailable")
}
func (failingIdempotencyRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("store unavailable")
}

func TestIdempotencyCoordinator_StoreUnavailableMetrics(t *testing.T) {
	resetIdempotencyMetricsForTest()
	coordinator := NewIdempotencyCoordinator(failingIdempotencyRepo{}, DefaultIdempotencyConfig())

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope.unavailable",
		Method:         "POST",
		Route:          "/test/unavailable",
		ActorScope:     "admin:1",
		RequireKey:     true,
		IdempotencyKey: "case-unavailable",
		Payload:        map[string]any{"v": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))
	require.GreaterOrEqual(t, GetIdempotencyMetricsSnapshot().StoreUnavailableTotal, uint64(1))
}

type utf8RejectingIdempotencyRepo struct {
	inMemoryIdempotencyRepo
}

func newUTF8RejectingIdempotencyRepo() *utf8RejectingIdempotencyRepo {
	return &utf8RejectingIdempotencyRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
}

func (r *utf8RejectingIdempotencyRepo) MarkSucceeded(ctx context.Context, id int64, expectedLockedUntil time.Time, responseStatus int, responseBody string, expiresAt time.Time) error {
	if !utf8.ValidString(responseBody) {
		return errors.New(`pq: invalid byte sequence for encoding "UTF8": 0xe8 0xb4 0x2e`)
	}
	return r.inMemoryIdempotencyRepo.MarkSucceeded(ctx, id, expectedLockedUntil, responseStatus, responseBody, expiresAt)
}

func TestIdempotencyCoordinator_TruncatedStoredResponseRemainsUTF8(t *testing.T) {
	repo := newUTF8RejectingIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.MaxStoredResponseLen = len(`{"message":"`) + 2
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.truncate_utf8",
		Method:         "POST",
		Route:          "/api/v1/accounts/import/codex-session",
		ActorScope:     "admin:1",
		RequireKey:     true,
		IdempotencyKey: "truncate-utf8",
		Payload:        map[string]any{"content": "codex-session"},
	}

	result, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return map[string]any{"message": strings.Repeat("\u8d26", 8)}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	stored, err := repo.GetByScopeAndKeyHash(context.Background(), opts.Scope, HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey))
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.ResponseBody)
	require.True(t, utf8.ValidString(*stored.ResponseBody))
	require.Contains(t, *stored.ResponseBody, "...(truncated)")
}

func TestDefaultIdempotencyCoordinatorAndTTLs(t *testing.T) {
	SetDefaultIdempotencyCoordinator(nil)
	require.Nil(t, DefaultIdempotencyCoordinator())
	require.Equal(t, DefaultIdempotencyConfig().DefaultTTL, DefaultWriteIdempotencyTTL())
	require.Equal(t, DefaultIdempotencyConfig().SystemOperationTTL, DefaultSystemOperationIdempotencyTTL())

	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		DefaultTTL:         2 * time.Hour,
		SystemOperationTTL: 15 * time.Minute,
		ProcessingTimeout:  10 * time.Second,
		FailedRetryBackoff: 3 * time.Second,
		ObserveOnly:        false,
	})
	SetDefaultIdempotencyCoordinator(coordinator)
	t.Cleanup(func() {
		SetDefaultIdempotencyCoordinator(nil)
	})

	require.Same(t, coordinator, DefaultIdempotencyCoordinator())
	require.Equal(t, 2*time.Hour, DefaultWriteIdempotencyTTL())
	require.Equal(t, 15*time.Minute, DefaultSystemOperationIdempotencyTTL())
}

func TestNormalizeIdempotencyKeyAndFingerprint(t *testing.T) {
	key, err := NormalizeIdempotencyKey("  abc-123  ")
	require.NoError(t, err)
	require.Equal(t, "abc-123", key)

	key, err = NormalizeIdempotencyKey("")
	require.NoError(t, err)
	require.Equal(t, "", key)

	_, err = NormalizeIdempotencyKey(string(make([]byte, 129)))
	require.Error(t, err)

	_, err = NormalizeIdempotencyKey("bad\nkey")
	require.Error(t, err)

	fp1, err := BuildIdempotencyFingerprint("", "", "", map[string]any{"a": 1})
	require.NoError(t, err)
	require.NotEmpty(t, fp1)
	fp2, err := BuildIdempotencyFingerprint("POST", "/", "anonymous", map[string]any{"a": 1})
	require.NoError(t, err)
	require.Equal(t, fp1, fp2)

	_, err = BuildIdempotencyFingerprint("POST", "/x", "u:1", map[string]any{"bad": make(chan int)})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyInvalidPayload), infraerrors.Code(err))
}

func TestRetryAfterSecondsFromErrorBranches(t *testing.T) {
	require.Equal(t, 0, RetryAfterSecondsFromError(nil))
	require.Equal(t, 0, RetryAfterSecondsFromError(errors.New("plain")))

	err := ErrIdempotencyInProgress.WithMetadata(map[string]string{"retry_after": "12"})
	require.Equal(t, 12, RetryAfterSecondsFromError(err))

	err = ErrIdempotencyInProgress.WithMetadata(map[string]string{"retry_after": "bad"})
	require.Equal(t, 0, RetryAfterSecondsFromError(err))
}

func TestIdempotencyCoordinator_ExecuteNilExecutorAndNoKeyPassThrough(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Payload:        map[string]any{"a": 1},
	}, nil)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_EXECUTOR_NIL", infraerrors.Reason(err))

	called := 0
	result, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:      "scope",
		RequireKey: true,
		Payload:    map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		called++
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.NotNil(t, result)
	require.False(t, result.Replayed)
}

type noIDOwnerRepo struct{}

func (noIDOwnerRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return true, nil
}
func (noIDOwnerRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return nil, nil
}
func (noIDOwnerRepo) TryReclaim(context.Context, int64, string, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (noIDOwnerRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (noIDOwnerRepo) MarkSucceeded(context.Context, int64, time.Time, int, string, time.Time) error {
	return nil
}
func (noIDOwnerRepo) MarkFailedRetryable(context.Context, int64, time.Time, string, time.Time, time.Time) error {
	return nil
}
func (noIDOwnerRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) { return 0, nil }

func TestIdempotencyCoordinator_RepoNilScopeRequiredAndRecordIDMissing(t *testing.T) {
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(nil, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	coordinator = NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg)
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		IdempotencyKey: "k2",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_SCOPE_REQUIRED", infraerrors.Reason(err))

	coordinator = NewIdempotencyCoordinator(noIDOwnerRepo{}, cfg)
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-no-id",
		IdempotencyKey: "k3",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))
}

type conflictBranchRepo struct {
	existing      *IdempotencyRecord
	tryReclaimErr error
	tryReclaimOK  bool
}

func (r *conflictBranchRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return false, nil
}
func (r *conflictBranchRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return cloneRecord(r.existing), nil
}
func (r *conflictBranchRepo) TryReclaim(context.Context, int64, string, string, time.Time, time.Time, time.Time) (bool, error) {
	if r.tryReclaimErr != nil {
		return false, r.tryReclaimErr
	}
	return r.tryReclaimOK, nil
}
func (r *conflictBranchRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (r *conflictBranchRepo) MarkSucceeded(context.Context, int64, time.Time, int, string, time.Time) error {
	return nil
}
func (r *conflictBranchRepo) MarkFailedRetryable(context.Context, int64, time.Time, string, time.Time, time.Time) error {
	return nil
}
func (r *conflictBranchRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func TestIdempotencyCoordinator_ConflictBranchesAndDecodeError(t *testing.T) {
	now := time.Now()
	fp, err := BuildIdempotencyFingerprint("POST", "/x", "u:1", map[string]any{"a": 1})
	require.NoError(t, err)
	badBody := "{bad-json"
	repo := &conflictBranchRepo{
		existing: &IdempotencyRecord{
			ID:                 1,
			Scope:              "scope",
			IdempotencyKeyHash: HashIdempotencyKey("k"),
			RequestFingerprint: fp,
			Status:             IdempotencyStatusSucceeded,
			ResponseBody:       &badBody,
			ExpiresAt:          now.Add(time.Hour),
		},
	}
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.existing = &IdempotencyRecord{
		ID:                 2,
		Scope:              "scope",
		IdempotencyKeyHash: HashIdempotencyKey("k"),
		RequestFingerprint: fp,
		Status:             "unknown",
		ExpiresAt:          now.Add(time.Hour),
	}
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyKeyConflict), infraerrors.Code(err))

	repo.existing = &IdempotencyRecord{
		ID:                 3,
		Scope:              "scope",
		IdempotencyKeyHash: HashIdempotencyKey("k"),
		RequestFingerprint: fp,
		Status:             IdempotencyStatusFailedRetryable,
		LockedUntil:        ptrTime(now.Add(-time.Second)),
		ExpiresAt:          now.Add(time.Hour),
	}
	repo.tryReclaimErr = errors.New("reclaim down")
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.tryReclaimErr = nil
	repo.tryReclaimOK = false
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyInProgress), infraerrors.Code(err))
}

type markBehaviorRepo struct {
	inMemoryIdempotencyRepo
	failMarkSucceeded        bool
	failMarkFailed           bool
	markSucceededCtx         error
	markSucceededDeadline    time.Time
	markSucceededHasDeadline bool
	markFailedCtx            error
	markFailedDeadline       time.Time
	markFailedHasDeadline    bool
}

func (r *markBehaviorRepo) MarkSucceeded(ctx context.Context, id int64, expectedLockedUntil time.Time, responseStatus int, responseBody string, expiresAt time.Time) error {
	r.markSucceededCtx = ctx.Err()
	r.markSucceededDeadline, r.markSucceededHasDeadline = ctx.Deadline()
	if r.markSucceededCtx != nil {
		return errors.New("mark succeeded received canceled context")
	}
	if r.failMarkSucceeded {
		return errors.New("mark succeeded failed")
	}
	return r.inMemoryIdempotencyRepo.MarkSucceeded(ctx, id, expectedLockedUntil, responseStatus, responseBody, expiresAt)
}

func (r *markBehaviorRepo) MarkFailedRetryable(ctx context.Context, id int64, expectedLockedUntil time.Time, errorReason string, lockedUntil, expiresAt time.Time) error {
	r.markFailedCtx = ctx.Err()
	r.markFailedDeadline, r.markFailedHasDeadline = ctx.Deadline()
	if r.markFailedCtx != nil {
		return errors.New("mark failed retryable received canceled context")
	}
	if r.failMarkFailed {
		return errors.New("mark failed retryable failed")
	}
	return r.inMemoryIdempotencyRepo.MarkFailedRetryable(ctx, id, expectedLockedUntil, errorReason, lockedUntil, expiresAt)
}

func requireIdempotencyTerminalWriteDeadline(
	t *testing.T,
	deadline time.Time,
	hasDeadline bool,
	executeStartedAt time.Time,
	executeFinishedAt time.Time,
) {
	t.Helper()
	require.True(t, hasDeadline)
	require.False(t, deadline.Before(executeStartedAt.Add(idempotencyTerminalWriteTimeout)))
	require.False(t, deadline.After(executeFinishedAt.Add(idempotencyTerminalWriteTimeout)))
}

func TestIdempotencyCoordinator_PersistsSuccessAfterRequestCancellation(t *testing.T) {
	repo := &markBehaviorRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
	config := DefaultIdempotencyConfig()
	config.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, config)
	ctx, cancel := context.WithCancel(context.Background())
	executions := 0
	opts := IdempotencyExecuteOptions{
		Scope:          "scope-canceled-success",
		IdempotencyKey: "stable-key",
		Method:         "POST",
		Route:          "/admin/write",
		ActorScope:     "admin:1",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}

	executeStartedAt := time.Now()
	result, err := coordinator.Execute(ctx, opts, func(context.Context) (any, error) {
		executions++
		cancel()
		return map[string]any{"updated": 1}, nil
	})
	executeFinishedAt := time.Now()
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.NoError(t, repo.markSucceededCtx)
	requireIdempotencyTerminalWriteDeadline(
		t,
		repo.markSucceededDeadline,
		repo.markSucceededHasDeadline,
		executeStartedAt,
		executeFinishedAt,
	)

	replayed, err := coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		executions++
		return nil, errors.New("must not execute during replay")
	})
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, 1, executions)
}

func TestIdempotencyCoordinator_PersistsRetryableFailureAfterRequestCancellation(t *testing.T) {
	repo := &markBehaviorRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
	config := DefaultIdempotencyConfig()
	config.ObserveOnly = false
	config.FailedRetryBackoff = time.Minute
	coordinator := NewIdempotencyCoordinator(repo, config)
	ctx, cancel := context.WithCancel(context.Background())
	executions := 0
	opts := IdempotencyExecuteOptions{
		Scope:          "scope-canceled-failure",
		IdempotencyKey: "stable-failure-key",
		Method:         "POST",
		Route:          "/admin/write",
		ActorScope:     "admin:1",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}
	sentinelErr := errors.New("sentinel execution failure")

	executeStartedAt := time.Now()
	result, err := coordinator.Execute(ctx, opts, func(context.Context) (any, error) {
		executions++
		cancel()
		return nil, sentinelErr
	})
	executeFinishedAt := time.Now()
	require.Nil(t, result)
	require.ErrorIs(t, err, sentinelErr)
	require.Equal(t, 1, executions)
	require.NoError(t, repo.markFailedCtx)
	requireIdempotencyTerminalWriteDeadline(
		t,
		repo.markFailedDeadline,
		repo.markFailedHasDeadline,
		executeStartedAt,
		executeFinishedAt,
	)

	keyHash := HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey)
	stored, err := repo.GetByScopeAndKeyHash(context.Background(), opts.Scope, keyHash)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, IdempotencyStatusFailedRetryable, stored.Status)
	require.NotNil(t, stored.LockedUntil)
	require.True(t, stored.LockedUntil.After(time.Now()))

	result, err = coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		executions++
		return map[string]any{"updated": 1}, nil
	})
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyRetryBackoff), infraerrors.Code(err))
	require.Greater(t, RetryAfterSecondsFromError(err), 0)
	require.Equal(t, 1, executions)

	past := time.Now().Add(-time.Second)
	repo.mu.Lock()
	repo.data[repo.key(opts.Scope, keyHash)].LockedUntil = &past
	repo.mu.Unlock()

	result, err = coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
		executions++
		return map[string]any{"updated": executions}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Replayed)
	require.Equal(t, 2, executions)

	stored, err = repo.GetByScopeAndKeyHash(context.Background(), opts.Scope, keyHash)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, IdempotencyStatusSucceeded, stored.Status)
}

func TestIdempotencyCoordinator_MarkAndMarshalBranches(t *testing.T) {
	repo := &markBehaviorRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	repo.failMarkSucceeded = true
	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-success",
		IdempotencyKey: "k1",
		Method:         "POST",
		Route:          "/ok",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.failMarkSucceeded = false
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-marshal",
		IdempotencyKey: "k2",
		Method:         "POST",
		Route:          "/bad",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"bad": make(chan int)}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))

	repo.failMarkFailed = true
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-fail",
		IdempotencyKey: "k3",
		Method:         "POST",
		Route:          "/fail",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return nil, errors.New("plain failure")
	})
	require.Error(t, err)
	require.Equal(t, "plain failure", err.Error())
}

func TestIdempotencyCoordinator_HelperBranches(t *testing.T) {
	c := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		DefaultTTL:           time.Hour,
		SystemOperationTTL:   time.Hour,
		ProcessingTimeout:    time.Second,
		FailedRetryBackoff:   time.Second,
		MaxStoredResponseLen: 12,
		ObserveOnly:          false,
	})

	// conflictWithRetryAfter without locked_until should return base error.
	base := ErrIdempotencyInProgress
	err := c.conflictWithRetryAfter(base, nil, time.Now())
	require.Equal(t, infraerrors.Code(base), infraerrors.Code(err))

	// marshalStoredResponse should truncate.
	body, err := c.marshalStoredResponse(map[string]any{"long": "abcdefghijklmnopqrstuvwxyz"})
	require.NoError(t, err)
	require.Contains(t, body, "...(truncated)")

	// decodeStoredResponse empty and invalid json.
	out, err := c.decodeStoredResponse(nil)
	require.NoError(t, err)
	_, ok := out.(map[string]any)
	require.True(t, ok)

	invalid := "{invalid"
	_, err = c.decodeStoredResponse(&invalid)
	require.Error(t, err)
}

func TestIdempotencyCoordinatorExecuteTransactionalRequiresKeyAndCapability(t *testing.T) {
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = true
	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg)
	var calls int

	_, err := coordinator.ExecuteTransactional(context.Background(), IdempotencyExecuteOptions{
		Scope:      "transactional-require-key",
		ActorScope: "admin:1",
		Method:     "POST",
		Route:      "/transactional",
		Payload:    map[string]any{"value": 1},
		RequireKey: true,
	}, func(context.Context) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	require.ErrorIs(t, err, ErrIdempotencyKeyRequired)
	require.Zero(t, calls)

	coordinator = NewIdempotencyCoordinator(failingIdempotencyRepo{}, cfg)
	_, err = coordinator.ExecuteTransactional(context.Background(), IdempotencyExecuteOptions{
		Scope:          "transactional-capability",
		ActorScope:     "admin:1",
		Method:         "POST",
		Route:          "/transactional",
		IdempotencyKey: "capability-key",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}, func(context.Context) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))
	require.Zero(t, calls)
}

func TestIdempotencyCoordinatorExecuteTransactionalCommitsAndReplays(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{
		Scope:          "transactional-success",
		ActorScope:     "admin:7",
		Method:         "POST",
		Route:          "/transactional",
		IdempotencyKey: "success-key",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}
	var calls int
	execute := func(context.Context) (any, error) {
		calls++
		return map[string]any{"updated": 2}, nil
	}

	first, err := coordinator.ExecuteTransactional(context.Background(), opts, execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, 1, calls)

	replayed, err := coordinator.ExecuteTransactional(context.Background(), opts, execute)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, 1, calls)

	record, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		opts.Scope,
		HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey),
	)
	require.NoError(t, err)
	require.Equal(t, IdempotencyStatusSucceeded, record.Status)
}

func TestIdempotencyCoordinatorExecuteTransactionalRollsBackTerminalStateOnExecutorFailure(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.FailedRetryBackoff = time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{
		Scope:          "transactional-failure",
		ActorScope:     "admin:8",
		Method:         "POST",
		Route:          "/transactional",
		IdempotencyKey: "failure-key",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}
	executorErr := errors.New("synthetic transactional executor failure")

	_, err := coordinator.ExecuteTransactional(context.Background(), opts, func(context.Context) (any, error) {
		return nil, executorErr
	})
	require.ErrorIs(t, err, executorErr)

	record, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		opts.Scope,
		HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey),
	)
	require.NoError(t, err)
	require.Equal(t, IdempotencyStatusFailedRetryable, record.Status)
	require.Nil(t, record.ResponseBody)
}

func TestIdempotencyCoordinatorRetryAfterHintControlsTransactionalBackoff(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.FailedRetryBackoff = 5 * time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{
		Scope:          "transactional-busy-backoff",
		ActorScope:     "admin:one-click-notes",
		Method:         "POST",
		Route:          "/transactional-busy-backoff",
		IdempotencyKey: "busy-key",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}
	busyErr := infraerrors.TooManyRequests(
		"ACCOUNT_NOTE_IMPORT_BUSY",
		"account note import is busy",
	).WithMetadata(map[string]string{"retry_after": "1"})

	_, err := coordinator.ExecuteTransactional(context.Background(), opts, func(context.Context) (any, error) {
		return nil, busyErr
	})
	require.ErrorIs(t, err, busyErr)
	require.Equal(t, 1, RetryAfterSecondsFromError(err))

	record, err := repo.GetByScopeAndKeyHash(
		context.Background(),
		opts.Scope,
		HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey),
	)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, IdempotencyStatusFailedRetryable, record.Status)
	require.NotNil(t, record.LockedUntil)
	remaining := time.Until(*record.LockedUntil)
	require.Greater(t, remaining, 0*time.Second)
	require.Less(t, remaining, 2*time.Second, "persisted backoff must honor Retry-After instead of the five-second default")
}

func TestIdempotencyCoordinatorProcessingLeaseReclaimIsTransactionalOnly(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	ctx := context.Background()

	makeProcessingRecord := func(scope, key string) IdempotencyExecuteOptions {
		opts := IdempotencyExecuteOptions{
			Scope:          scope,
			ActorScope:     "admin:9",
			Method:         "POST",
			Route:          "/transactional",
			IdempotencyKey: key,
			Payload:        map[string]any{"value": scope},
			RequireKey:     true,
		}
		fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
		require.NoError(t, err)
		past := time.Now().Add(-time.Second)
		created, err := repo.CreateProcessing(ctx, &IdempotencyRecord{
			Scope:              opts.Scope,
			IdempotencyKeyHash: HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey),
			RequestFingerprint: fingerprint,
			Status:             IdempotencyStatusProcessing,
			LockedUntil:        &past,
			ExpiresAt:          time.Now().Add(time.Hour),
		})
		require.NoError(t, err)
		require.True(t, created)
		return opts
	}

	genericOpts := makeProcessingRecord("generic-processing", "generic-key")
	var genericCalls int
	_, err := coordinator.Execute(ctx, genericOpts, func(context.Context) (any, error) {
		genericCalls++
		return map[string]any{"ok": true}, nil
	})
	require.Equal(t, infraerrors.Code(ErrIdempotencyInProgress), infraerrors.Code(err))
	require.Zero(t, genericCalls)

	transactionalOpts := makeProcessingRecord("transactional-processing", "transactional-key")
	var transactionalCalls int
	result, err := coordinator.ExecuteTransactional(ctx, transactionalOpts, func(context.Context) (any, error) {
		transactionalCalls++
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, 1, transactionalCalls)
}

var errSyntheticAmbiguousCommit = errors.New("synthetic ambiguous transaction commit")

type ambiguousCommitIdempotencyRepo struct {
	*inMemoryIdempotencyRepo
	commitBeforeError  bool
	failReadAfterError bool
	attempts           atomic.Int32
	readUnavailable    atomic.Bool
}

func (r *ambiguousCommitIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, IdempotencyRepository) error,
) error {
	if r.attempts.Add(1) != 1 {
		return r.inMemoryIdempotencyRepo.WithinTransaction(ctx, execute)
	}

	if r.commitBeforeError {
		if err := r.inMemoryIdempotencyRepo.WithinTransaction(ctx, execute); err != nil {
			return err
		}
	} else {
		txRepo := cloneInMemoryIdempotencyRepo(r.inMemoryIdempotencyRepo)
		if err := execute(ctx, txRepo); err != nil {
			return err
		}
	}
	if r.failReadAfterError {
		r.readUnavailable.Store(true)
	}
	return errSyntheticAmbiguousCommit
}

func (r *ambiguousCommitIdempotencyRepo) GetByScopeAndKeyHash(
	ctx context.Context,
	scope string,
	keyHash string,
) (*IdempotencyRecord, error) {
	if r.readUnavailable.Load() {
		return nil, errors.New("synthetic idempotency read outage")
	}
	return r.inMemoryIdempotencyRepo.GetByScopeAndKeyHash(ctx, scope, keyHash)
}

func TestIdempotencyCoordinatorTransactionalCommitRecovery(t *testing.T) {
	tests := []struct {
		name              string
		commitBeforeError bool
		wantReplay        bool
		wantStatus        string
		wantErrorCode     int
	}{
		{
			name:              "commit completed before transport error",
			commitBeforeError: true,
			wantReplay:        true,
			wantStatus:        IdempotencyStatusSucceeded,
		},
		{
			name:              "transaction rolled back before commit error",
			commitBeforeError: false,
			wantStatus:        IdempotencyStatusFailedRetryable,
			wantErrorCode:     infraerrors.Code(ErrIdempotencyStoreUnavail),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := newInMemoryIdempotencyRepo()
			repo := &ambiguousCommitIdempotencyRepo{
				inMemoryIdempotencyRepo: base,
				commitBeforeError:       test.commitBeforeError,
			}
			cfg := DefaultIdempotencyConfig()
			cfg.ObserveOnly = false
			coordinator := NewIdempotencyCoordinator(repo, cfg)
			opts := IdempotencyExecuteOptions{
				Scope:          "ambiguous-commit-" + test.name,
				ActorScope:     "admin:10",
				Method:         "POST",
				Route:          "/transactional",
				IdempotencyKey: "ambiguous-key",
				Payload:        map[string]any{"value": 1},
				RequireKey:     true,
			}
			var calls int
			result, err := coordinator.ExecuteTransactional(context.Background(), opts, func(context.Context) (any, error) {
				calls++
				return map[string]any{"ok": true}, nil
			})

			if test.wantErrorCode == 0 {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, test.wantReplay, result.Replayed)
			} else {
				require.Equal(t, test.wantErrorCode, infraerrors.Code(err))
				require.Nil(t, result)
			}
			require.Equal(t, 1, calls)

			record, readErr := base.GetByScopeAndKeyHash(
				context.Background(),
				opts.Scope,
				HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey),
			)
			require.NoError(t, readErr)
			require.Equal(t, test.wantStatus, record.Status)
		})
	}
}

func TestIdempotencyCoordinatorTransactionalCommitReadOutageCanReclaimExpiredLease(t *testing.T) {
	base := newInMemoryIdempotencyRepo()
	repo := &ambiguousCommitIdempotencyRepo{
		inMemoryIdempotencyRepo: base,
		commitBeforeError:       false,
		failReadAfterError:      true,
	}
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{
		Scope:          "ambiguous-read-outage",
		ActorScope:     "admin:11",
		Method:         "POST",
		Route:          "/transactional",
		IdempotencyKey: "read-outage-key",
		Payload:        map[string]any{"value": 1},
		RequireKey:     true,
	}
	var calls int
	execute := func(context.Context) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	}

	result, err := coordinator.ExecuteTransactional(context.Background(), opts, execute)
	require.Nil(t, result)
	require.Equal(t, infraerrors.Code(ErrIdempotencyStoreUnavail), infraerrors.Code(err))
	require.Equal(t, 1, calls)

	repo.readUnavailable.Store(false)
	keyHash := HashActorScopedIdempotencyKey(opts.ActorScope, opts.IdempotencyKey)
	base.mu.Lock()
	record := base.data[base.key(opts.Scope, keyHash)]
	require.NotNil(t, record)
	past := time.Now().Add(-time.Second)
	record.LockedUntil = &past
	base.mu.Unlock()

	result, err = coordinator.ExecuteTransactional(context.Background(), opts, execute)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Replayed)
	require.Equal(t, 2, calls)
}
