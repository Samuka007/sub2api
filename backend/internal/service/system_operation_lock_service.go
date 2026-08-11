package service

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	systemOperationLockScope = "admin.system.operations.global_lock"
	systemOperationLockKey   = "global-system-operation-lock"
)

var (
	ErrSystemOperationBusy = infraerrors.Conflict("SYSTEM_OPERATION_BUSY", "another system operation is in progress")
)

type SystemOperationLock struct {
	recordID     int64
	operationID  string
	lockedUntil  time.Time
	pendingLease *time.Time
	leaseMu      sync.Mutex

	renewCtx    context.Context
	renewCancel context.CancelFunc

	stopOnce sync.Once
	stopCh   chan struct{}
}

func (l *SystemOperationLock) OperationID() string {
	if l == nil {
		return ""
	}
	return l.operationID
}

type SystemOperationLockService struct {
	repo IdempotencyRepository

	lease         time.Duration
	renewInterval time.Duration
	ttl           time.Duration
}

func NewSystemOperationLockService(repo IdempotencyRepository, cfg IdempotencyConfig) *SystemOperationLockService {
	lease := cfg.ProcessingTimeout
	if lease <= 0 {
		lease = 30 * time.Second
	}
	renewInterval := lease / 3
	if renewInterval < time.Second {
		renewInterval = time.Second
	}
	ttl := cfg.SystemOperationTTL
	if ttl <= 0 {
		ttl = time.Hour
	}

	return &SystemOperationLockService{
		repo:          repo,
		lease:         lease,
		renewInterval: renewInterval,
		ttl:           ttl,
	}
}

func (s *SystemOperationLockService) Acquire(ctx context.Context, operationID string) (*SystemOperationLock, error) {
	if s == nil || s.repo == nil {
		return nil, ErrIdempotencyStoreUnavail
	}
	if operationID == "" {
		return nil, infraerrors.BadRequest("SYSTEM_OPERATION_ID_REQUIRED", "operation id is required")
	}

	now := time.Now()
	expiresAt := now.Add(s.ttl)
	lockedUntil := idempotencyLeaseDeadline(now, s.lease)
	keyHash := HashIdempotencyKey(systemOperationLockKey)

	record := &IdempotencyRecord{
		Scope:              systemOperationLockScope,
		IdempotencyKeyHash: keyHash,
		RequestFingerprint: operationID,
		Status:             IdempotencyStatusProcessing,
		LockedUntil:        &lockedUntil,
		ExpiresAt:          expiresAt,
	}

	owner, err := s.repo.CreateProcessing(ctx, record)
	if err != nil {
		return nil, ErrIdempotencyStoreUnavail.WithCause(err)
	}
	if !owner {
		existing, getErr := s.repo.GetByScopeAndKeyHash(ctx, systemOperationLockScope, keyHash)
		if getErr != nil {
			return nil, ErrIdempotencyStoreUnavail.WithCause(getErr)
		}
		if existing == nil {
			return nil, ErrIdempotencyStoreUnavail
		}
		if existing.Status == IdempotencyStatusProcessing && existing.LockedUntil != nil && existing.LockedUntil.After(now) {
			return nil, s.busyError(existing.RequestFingerprint, existing.LockedUntil, now)
		}
		reclaimed, reclaimErr := s.repo.TryReclaim(
			ctx,
			existing.ID,
			existing.Status,
			operationID,
			now,
			lockedUntil,
			expiresAt,
		)
		if reclaimErr != nil {
			return nil, ErrIdempotencyStoreUnavail.WithCause(reclaimErr)
		}
		if !reclaimed {
			latest, _ := s.repo.GetByScopeAndKeyHash(ctx, systemOperationLockScope, keyHash)
			if latest != nil {
				return nil, s.busyError(latest.RequestFingerprint, latest.LockedUntil, now)
			}
			return nil, ErrSystemOperationBusy
		}
		record.ID = existing.ID
	}

	if record.ID == 0 {
		return nil, ErrIdempotencyStoreUnavail
	}

	renewCtx, renewCancel := context.WithCancel(context.Background())
	lock := &SystemOperationLock{
		recordID:    record.ID,
		operationID: operationID,
		lockedUntil: lockedUntil,
		renewCtx:    renewCtx,
		renewCancel: renewCancel,
		stopCh:      make(chan struct{}),
	}
	go s.renewLoop(lock)

	return lock, nil
}

func (s *SystemOperationLockService) Release(ctx context.Context, lock *SystemOperationLock, succeeded bool, failureReason string) error {
	if s == nil || s.repo == nil || lock == nil {
		return nil
	}

	lock.leaseMu.Lock()
	lock.stopOnce.Do(func() {
		close(lock.stopCh)
		if lock.renewCancel != nil {
			lock.renewCancel()
		}
	})
	expectedLease := lock.lockedUntil
	var pendingLease *time.Time
	if lock.pendingLease != nil {
		candidate := *lock.pendingLease
		pendingLease = &candidate
	}
	lock.leaseMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	expiresAt := time.Now().Add(s.ttl)
	terminalCtx, terminalCancel := idempotencyTerminalWriteContext(ctx)
	defer terminalCancel()

	markTerminal := func(lease time.Time) error {
		if succeeded {
			responseBody := fmt.Sprintf(`{"operation_id":"%s","released":true}`, lock.operationID)
			return s.repo.MarkSucceeded(terminalCtx, lock.recordID, lease, 200, responseBody, expiresAt)
		}

		reason := failureReason
		if reason == "" {
			reason = "SYSTEM_OPERATION_FAILED"
		}
		return s.repo.MarkFailedRetryable(terminalCtx, lock.recordID, lease, reason, time.Now(), expiresAt)
	}

	err := markTerminal(expectedLease)
	if err == nil || pendingLease == nil || pendingLease.Equal(expectedLease) {
		return err
	}

	// A renewal can commit its lease rotation before observing cancellation
	// while its caller is still waiting for the database response. Retrying the
	// terminal CAS with that single in-flight token safely covers both commit
	// orders; status=processing prevents a late renewal from reviving the row.
	return markTerminal(*pendingLease)
}

func (s *SystemOperationLockService) renewLoop(lock *SystemOperationLock) {
	renewBase := lock.renewCtx
	if renewBase == nil {
		renewBase = context.Background()
	}
	ticker := time.NewTicker(s.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			resolved, resolveErr := s.resolvePendingLease(renewBase, lock)
			if resolveErr != nil {
				if renewBase.Err() != nil {
					return
				}
				logger.LegacyPrintf("service.system_operation_lock", "[SystemOperationLock] resolve pending renewal failed operation_id=%s err=%v", lock.operationID, resolveErr)
				continue
			}
			if !resolved {
				logger.LegacyPrintf("service.system_operation_lock", "[SystemOperationLock] renew stopped operation_id=%s reason=ownership_lost", lock.operationID)
				return
			}

			now := time.Now()
			newLockedUntil := idempotencyLeaseDeadline(now, s.lease)
			lock.leaseMu.Lock()
			if renewBase.Err() != nil {
				lock.leaseMu.Unlock()
				return
			}
			expectedLockedUntil := lock.lockedUntil
			pendingLease := newLockedUntil
			lock.pendingLease = &pendingLease
			lock.leaseMu.Unlock()

			ctx, cancel := context.WithTimeout(renewBase, 2*time.Second)
			ok, err := s.repo.ExtendProcessingLock(
				ctx,
				lock.recordID,
				lock.operationID,
				expectedLockedUntil,
				newLockedUntil,
				now.Add(s.ttl),
			)
			cancel()

			lock.leaseMu.Lock()
			if err == nil && ok {
				lock.lockedUntil = newLockedUntil
				lock.pendingLease = nil
			} else if err == nil {
				lock.pendingLease = nil
			}
			lock.leaseMu.Unlock()
			if renewBase.Err() != nil {
				return
			}
			if err != nil {
				logger.LegacyPrintf("service.system_operation_lock", "[SystemOperationLock] renew failed operation_id=%s err=%v", lock.operationID, err)
				// 瞬时故障不应导致续租协程退出，下一轮继续尝试续租。
				continue
			}
			if !ok {
				logger.LegacyPrintf("service.system_operation_lock", "[SystemOperationLock] renew stopped operation_id=%s reason=ownership_lost", lock.operationID)
				return
			}
		case <-lock.stopCh:
			return
		}
	}
}

// resolvePendingLease reconciles an uncertain UPDATE result before issuing a
// new renewal. The read is fenced by the same record identity and lets the
// loop distinguish an error-before-commit from an error-after-commit without
// guessing which lease token PostgreSQL accepted.
func (s *SystemOperationLockService) resolvePendingLease(ctx context.Context, lock *SystemOperationLock) (bool, error) {
	lock.leaseMu.Lock()
	if lock.pendingLease == nil {
		lock.leaseMu.Unlock()
		return true, nil
	}
	currentLease := lock.lockedUntil
	pendingLease := *lock.pendingLease
	lock.leaseMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	record, err := s.repo.GetByScopeAndKeyHash(
		readCtx,
		systemOperationLockScope,
		HashIdempotencyKey(systemOperationLockKey),
	)
	cancel()
	if err != nil {
		return false, err
	}
	if record == nil ||
		record.Status != IdempotencyStatusProcessing ||
		record.RequestFingerprint != lock.operationID ||
		record.LockedUntil == nil {
		return false, nil
	}

	lock.leaseMu.Lock()
	defer lock.leaseMu.Unlock()
	if lock.pendingLease == nil || !lock.pendingLease.Equal(pendingLease) {
		return true, nil
	}
	switch {
	case record.LockedUntil.Equal(pendingLease):
		lock.lockedUntil = pendingLease
		lock.pendingLease = nil
		return true, nil
	case record.LockedUntil.Equal(currentLease):
		lock.pendingLease = nil
		return true, nil
	default:
		return false, nil
	}
}

func (s *SystemOperationLockService) busyError(operationID string, lockedUntil *time.Time, now time.Time) error {
	metadata := make(map[string]string)
	if operationID != "" {
		metadata["operation_id"] = operationID
	}
	if lockedUntil != nil {
		sec := int(lockedUntil.Sub(now).Seconds())
		if sec <= 0 {
			sec = 1
		}
		metadata["retry_after"] = strconv.Itoa(sec)
	}
	if len(metadata) == 0 {
		return ErrSystemOperationBusy
	}
	return ErrSystemOperationBusy.WithMetadata(metadata)
}
