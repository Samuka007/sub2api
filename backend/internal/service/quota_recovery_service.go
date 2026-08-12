package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	quotaRecoverySingletonLockKey        = "sub2api:quota-recovery:single-instance"
	quotaRecoverySingletonHealthInterval = 5 * time.Second
	quotaRecoverySingletonHealthTimeout  = 2 * time.Second
	quotaRecoverySingletonAcquireTimeout = 5 * time.Second
	quotaRecoveryReacquireMinBackoff     = time.Second
	quotaRecoveryReacquireMaxBackoff     = 30 * time.Second
	quotaRecoveryReacquireLogInterval    = time.Minute
	quotaRecoveryFreshnessSkew           = 5 * time.Second

	defaultQuotaRecoveryInterval    = 24 * time.Hour
	defaultQuotaRecoveryBatchSize   = 50
	defaultQuotaRecoveryConcurrency = 3
	defaultQuotaRecoveryTimeout     = 75 * time.Second
)

type quotaRecoveryLifecycleState uint8

const (
	quotaRecoveryStopped quotaRecoveryLifecycleState = iota
	quotaRecoveryRunning
	quotaRecoveryStopping
)

var (
	ErrQuotaRecoveryRepositoryUnavailable = errors.New("quota recovery repository capability is unavailable")
	ErrQuotaRecoveryAlreadyRunning        = errors.New("quota recovery is already running in another process")
	ErrQuotaRecoveryDatabaseUnavailable   = errors.New("quota recovery single-instance database is unavailable")
)

type quotaRecoveryCheckerReader interface {
	Check(ctx context.Context, account *Account) QuotaRecoveryCheckResult
}

// quotaRecoveryRuntimeBlockGuard prevents a successful, but older, quota probe
// from clearing an OpenAI runtime block created by a concurrent newer 429.
type quotaRecoveryRuntimeBlockGuard interface {
	AccountSchedulingBlockGeneration(accountID int64) uint64
	GuardAccountSchedulingBlockForQuotaRecovery(
		accountID int64,
		observedGeneration uint64,
		observedLimitedAt time.Time,
		observedResetAt time.Time,
	) (release func(), matched bool)
	ClearAccountSchedulingBlockForQuotaRecovery(
		accountID int64,
		observedGeneration uint64,
		observedLimitedAt time.Time,
		observedResetAt time.Time,
	) bool
}

// QuotaRecoveryRunResult summarizes one bounded Hermes reconciliation cycle.
type QuotaRecoveryRunResult struct {
	Listed    int
	Checked   int
	Recovered int
	Exhausted int
	Unknown   int
	Skipped   int
	CASMisses int
	Errors    int
}

// QuotaRecoveryStatusConfig describes the effective Hermes scheduler
// configuration. Values are resolved through the same helpers used by the
// worker, so the status endpoint never reports zero values for omitted config.
type QuotaRecoveryStatusConfig struct {
	IntervalSeconds int `json:"interval_seconds"`
	BatchSize       int `json:"batch_size"`
	Concurrency     int `json:"concurrency"`
	TimeoutSeconds  int `json:"timeout_seconds"`
	JitterSeconds   int `json:"jitter_seconds"`
}

// QuotaRecoveryRunStatus is a redacted summary of one Hermes reconciliation
// cycle. It deliberately contains no account identifiers or provider payloads.
type QuotaRecoveryRunStatus struct {
	Trigger     string     `json:"trigger"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DurationMS  int64      `json:"duration_ms"`
	Listed      int        `json:"listed"`
	Checked     int        `json:"checked"`
	Recovered   int        `json:"recovered"`
	Exhausted   int        `json:"exhausted"`
	Unknown     int        `json:"unknown"`
	Skipped     int        `json:"skipped"`
	CASMisses   int        `json:"cas_misses"`
	Errors      int        `json:"errors"`
	LastError   string     `json:"last_error,omitempty"`
}

// QuotaRecoveryStatus is the read-only operational snapshot exposed to admin
// tooling. Runtime history is intentionally process-local; a nil current/last
// run means that this process has not observed a completed cycle yet.
type QuotaRecoveryStatus struct {
	Enabled             bool                      `json:"enabled"`
	Status              string                    `json:"status"`
	Healthy             bool                      `json:"healthy"`
	HealthReason        string                    `json:"health_reason,omitempty"`
	LifecycleState      string                    `json:"lifecycle_state"`
	LeaseHeld           bool                      `json:"lease_held"`
	LeaseHealthy        bool                      `json:"lease_healthy"`
	StartedAt           *time.Time                `json:"started_at,omitempty"`
	ObservedAt          time.Time                 `json:"observed_at"`
	LastLeaseAcquiredAt *time.Time                `json:"last_lease_acquired_at,omitempty"`
	LastLeaseLostAt     *time.Time                `json:"last_lease_lost_at,omitempty"`
	LastReacquiredAt    *time.Time                `json:"last_reacquired_at,omitempty"`
	NextRunAt           *time.Time                `json:"next_run_at,omitempty"`
	CurrentRun          *QuotaRecoveryRunStatus   `json:"current_run"`
	LastRun             *QuotaRecoveryRunStatus   `json:"last_run"`
	Config              QuotaRecoveryStatusConfig `json:"config"`
}

// QuotaRecoveryService reconciles account-level rate-limit timestamps against
// authoritative provider quota APIs. Normal reset_at expiry remains the primary
// recovery path; this service only permits evidence-backed early recovery.
type QuotaRecoveryService struct {
	accountRepo    AccountRepository
	checker        quotaRecoveryCheckerReader
	runtimeBlocker AccountRuntimeBlocker
	cfg            *config.Config
	db             *sql.DB

	now                 func() time.Time
	jitterFor           func(time.Duration) time.Duration
	leaseHealthInterval time.Duration
	reacquireDelayFor   func(int) time.Duration
	logger              *slog.Logger

	cycleGate chan struct{}
	mu        sync.Mutex
	state     quotaRecoveryLifecycleState
	cancel    context.CancelFunc
	done      chan struct{}

	statusMu            sync.RWMutex
	statusLifecycle     string
	statusStartedAt     *time.Time
	statusLeaseHeld     bool
	statusLeaseHealthy  bool
	statusLeaseAcquired *time.Time
	statusLeaseLost     *time.Time
	statusReacquired    *time.Time
	statusNextRunAt     *time.Time
	statusCurrentRun    *QuotaRecoveryRunStatus
	statusLastRun       *QuotaRecoveryRunStatus
	statusObservedAt    time.Time
}

func NewQuotaRecoveryService(
	accountRepo AccountRepository,
	checker *QuotaRecoveryChecker,
	runtimeBlocker AccountRuntimeBlocker,
	cfg *config.Config,
) *QuotaRecoveryService {
	return newQuotaRecoveryService(accountRepo, checker, runtimeBlocker, cfg)
}

func newQuotaRecoveryService(
	accountRepo AccountRepository,
	checker quotaRecoveryCheckerReader,
	runtimeBlocker AccountRuntimeBlocker,
	cfg *config.Config,
) *QuotaRecoveryService {
	createdAt := time.Now().UTC()
	return &QuotaRecoveryService{
		accountRepo:         accountRepo,
		checker:             checker,
		runtimeBlocker:      runtimeBlocker,
		cfg:                 cfg,
		now:                 time.Now,
		leaseHealthInterval: quotaRecoverySingletonHealthInterval,
		cycleGate:           make(chan struct{}, 1),
		statusLifecycle:     "stopped",
		statusStartedAt:     nil,
		statusNextRunAt:     nil,
		statusCurrentRun:    nil,
		jitterFor: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max) + 1))
		},
		// Keep the creation timestamp separate from started_at: a disabled
		// instance can still report an observed snapshot without implying that
		// Hermes ever acquired a lease.
		statusObservedAt: createdAt,
	}
}

// ProvideQuotaRecoveryService constructs and starts Hermes in standard or
// simple run mode. Hermes is supported only in single-application-process
// deployments: Start's PostgreSQL advisory lock prevents duplicate runners but
// cannot invalidate process-local runtime blocks in other application
// processes. Configuration is opt-in, so construction is side-effect free
// unless quota_recovery.enabled is true.
func ProvideQuotaRecoveryService(
	accountRepo AccountRepository,
	checker *QuotaRecoveryChecker,
	runtimeBlocker AccountRuntimeBlocker,
	cfg *config.Config,
	db *sql.DB,
) (*QuotaRecoveryService, error) {
	svc := NewQuotaRecoveryService(accountRepo, checker, runtimeBlocker, cfg)
	svc.db = db
	if err := svc.Start(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *QuotaRecoveryService) Start() error {
	if s == nil || !s.enabled() {
		return nil
	}
	for {
		s.mu.Lock()
		if s.state == quotaRecoveryStopping {
			done := s.done
			s.mu.Unlock()
			if done != nil {
				<-done
			}
			continue
		}
		if s.state == quotaRecoveryRunning {
			s.touchStatus(s.currentTime().UTC())
			s.mu.Unlock()
			return nil
		}
		if s.db == nil {
			s.mu.Unlock()
			return ErrQuotaRecoveryDatabaseUnavailable
		}
		s.lifecycleLogger().Info("quota_recovery_start",
			"lock_id", hashAdvisoryLockID(quotaRecoverySingletonLockKey),
		)
		lockCtx, lockCancel := context.WithTimeout(context.Background(), quotaRecoverySingletonAcquireTimeout)
		lease, acquired, err := tryAcquireDBAdvisoryLockLease(
			lockCtx,
			s.db,
			hashAdvisoryLockID(quotaRecoverySingletonLockKey),
		)
		lockCancel()
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("acquire quota recovery single-instance lock: %w", err)
		}
		if !acquired {
			s.mu.Unlock()
			return ErrQuotaRecoveryAlreadyRunning
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		s.state = quotaRecoveryRunning
		s.cancel = cancel
		s.done = done
		s.markLeaseAcquired(s.currentTime().UTC())
		s.lifecycleLogger().Info("quota_recovery_singleton_acquired",
			"lock_id", hashAdvisoryLockID(quotaRecoverySingletonLockKey),
		)
		go s.runSingletonSupervisor(ctx, done, lease)
		s.mu.Unlock()
		return nil
	}
}

func (s *QuotaRecoveryService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.state == quotaRecoveryStopped {
		s.mu.Unlock()
		return
	}
	done := s.done
	if s.state == quotaRecoveryRunning {
		s.state = quotaRecoveryStopping
		s.markStatusLifecycle("stopping", s.currentTime().UTC())
		if s.cancel != nil {
			s.cancel()
		}
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *QuotaRecoveryService) runSingletonSupervisor(
	ctx context.Context,
	done chan struct{},
	lease *dbAdvisoryLockLease,
) {
	defer s.finishSingletonSupervisor(done)

	for {
		lostAt, leaseErr := s.runWithSingletonLease(ctx, lease)
		if err := lease.Release(); err != nil {
			s.lifecycleLogger().Warn("quota_recovery_singleton_release_failed", "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if leaseErr == nil {
			return
		}

		var ok bool
		lease, ok = s.reacquireSingletonLease(ctx, lostAt)
		if !ok {
			return
		}
	}
}

func (s *QuotaRecoveryService) finishSingletonSupervisor(done chan struct{}) {
	s.mu.Lock()
	if s.done == done {
		s.state = quotaRecoveryStopped
		s.cancel = nil
		s.done = nil
		s.markStopped(s.currentTime().UTC())
	}
	close(done)
	s.mu.Unlock()
}

func (s *QuotaRecoveryService) runWithSingletonLease(
	ctx context.Context,
	lease *dbAdvisoryLockLease,
) (time.Time, error) {
	leaseCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		s.runLoop(leaseCtx)
	}()

	leaseErr := s.monitorSingletonLease(leaseCtx, lease)
	var lostAt time.Time
	if leaseErr != nil {
		lostAt = s.currentTime().UTC()
		s.markLeaseLost(lostAt.UTC())
		s.lifecycleLogger().Error("quota_recovery_singleton_lease_lost",
			"error", leaseErr,
			"automatic_recovery", true,
		)
	}
	cancel()
	<-runDone
	return lostAt, leaseErr
}

func (s *QuotaRecoveryService) monitorSingletonLease(
	ctx context.Context,
	lease *dbAdvisoryLockLease,
) error {
	interval := s.singletonHealthInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, quotaRecoverySingletonHealthTimeout)
			err := lease.Ping(pingCtx)
			pingCancel()
			if err == nil {
				s.touchStatus(s.currentTime().UTC())
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (s *QuotaRecoveryService) reacquireSingletonLease(
	ctx context.Context,
	lostAt time.Time,
) (*dbAdvisoryLockLease, bool) {
	var failureLogs quotaRecoveryLogLimiter
	var retryIn time.Duration
	for attempt := 1; ; attempt++ {
		if !waitForQuotaRecoveryRetry(ctx, retryIn) {
			return nil, false
		}

		db := s.singletonDatabase()
		if db == nil {
			retryIn = s.singletonReacquireDelay(attempt)
			if failureLogs.Allow(time.Now()) {
				s.lifecycleLogger().Warn("quota_recovery_singleton_reacquire_failed",
					"attempt", attempt,
					"reason", "database_unavailable",
					"retry_in", retryIn,
				)
			}
			continue
		}

		lockCtx, lockCancel := context.WithTimeout(ctx, quotaRecoverySingletonAcquireTimeout)
		lease, acquired, err := tryAcquireDBAdvisoryLockLease(
			lockCtx,
			db,
			hashAdvisoryLockID(quotaRecoverySingletonLockKey),
		)
		lockCancel()
		if ctx.Err() != nil {
			if lease != nil {
				_ = lease.Release()
			}
			return nil, false
		}
		if err == nil && acquired {
			s.markLeaseReacquired(s.currentTime().UTC())
			s.lifecycleLogger().Info("quota_recovery_singleton_reacquired",
				"lock_id", hashAdvisoryLockID(quotaRecoverySingletonLockKey),
				"attempts", attempt,
				"downtime", time.Since(lostAt),
			)
			return lease, true
		}

		retryIn = s.singletonReacquireDelay(attempt)
		if failureLogs.Allow(time.Now()) {
			reason := "lock_held"
			attrs := []any{
				"attempt", attempt,
				"reason", reason,
				"retry_in", retryIn,
			}
			if err != nil {
				attrs[3] = "acquire_error"
				attrs = append(attrs, "error", err)
			}
			s.lifecycleLogger().Warn("quota_recovery_singleton_reacquire_failed", attrs...)
		}
	}
}

type quotaRecoveryLogLimiter struct {
	last time.Time
}

func (l *quotaRecoveryLogLimiter) Allow(now time.Time) bool {
	if l.last.IsZero() || !now.Before(l.last.Add(quotaRecoveryReacquireLogInterval)) {
		l.last = now
		return true
	}
	return false
}

func waitForQuotaRecoveryRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *QuotaRecoveryService) runLoop(ctx context.Context) {
	for {
		cycleStartedAt := s.currentTime().UTC()
		result, err := s.runOnce(ctx, "scheduled", true)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("quota_recovery_run_failed", "error", err)
		} else if err == nil {
			slog.Info("quota_recovery_run_completed",
				"listed", result.Listed,
				"checked", result.Checked,
				"recovered", result.Recovered,
				"exhausted", result.Exhausted,
				"unknown", result.Unknown,
				"cas_misses", result.CASMisses,
				"errors", result.Errors,
			)
		}
		untilNextCycle := time.Until(cycleStartedAt.Add(s.interval()))
		s.setNextRunAt(cycleStartedAt.Add(s.interval()))
		if untilNextCycle < 0 {
			untilNextCycle = 0
		}
		timer := time.NewTimer(untilNextCycle)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// RunOnce executes one complete, paginated reconciliation cycle. It is
// exported so local validation and operational tooling can exercise the exact
// scheduled path without waiting for the interval timer.
func (s *QuotaRecoveryService) RunOnce(ctx context.Context) (QuotaRecoveryRunResult, error) {
	return s.runOnce(ctx, "manual", false)
}

func (s *QuotaRecoveryService) runOnce(
	ctx context.Context,
	trigger string,
	scheduled bool,
) (result QuotaRecoveryRunResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || !s.enabled() {
		return result, nil
	}
	// Acquire the same cycle gate before publishing a current-run snapshot.
	// Concurrent manual callers may wait here; publishing before the gate would
	// let a waiter overwrite the status of the cycle that is actually running.
	select {
	case s.cycleGate <- struct{}{}:
		defer func() { <-s.cycleGate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	s.beginRunStatus(trigger, s.currentTime().UTC(), scheduled)
	defer func() {
		s.finishRunStatus(result, err)
	}()
	repo, ok := s.accountRepo.(QuotaRecoveryAccountRepository)
	if !ok || repo == nil {
		return result, ErrQuotaRecoveryRepositoryUnavailable
	}
	if s.checker == nil {
		return result, fmt.Errorf("quota recovery checker is unavailable")
	}

	now := s.currentTime().UTC()
	pageSize := s.batchSize()
	slots := make(chan struct{}, s.concurrency())
	upperBoundCtx, upperBoundCancel := context.WithTimeout(ctx, s.timeout())
	throughID, err := repo.QuotaRecoveryCandidateUpperBound(upperBoundCtx, now)
	if err == nil {
		err = upperBoundCtx.Err()
	}
	upperBoundCancel()
	if err != nil {
		return result, fmt.Errorf("find quota recovery candidate upper bound: %w", err)
	}
	if throughID <= 0 {
		return result, nil
	}
	var afterID int64
	for {
		listCtx, listCancel := context.WithTimeout(ctx, s.timeout())
		accounts, err := repo.ListQuotaRecoveryCandidates(listCtx, now, afterID, throughID, pageSize)
		if err == nil {
			err = listCtx.Err()
		}
		listCancel()
		if err != nil {
			return result, fmt.Errorf("list quota recovery candidates after id %d: %w", afterID, err)
		}
		if len(accounts) > pageSize {
			accounts = accounts[:pageSize]
		}
		if len(accounts) == 0 {
			break
		}

		pageLastID := afterID
		eligible := make([]Account, 0, len(accounts))
		for i := range accounts {
			if accounts[i].ID > pageLastID {
				pageLastID = accounts[i].ID
			}
			if isQuotaRecoveryCandidate(&accounts[i], now) {
				eligible = append(eligible, accounts[i])
				continue
			}
			result.Skipped++
		}
		if pageLastID <= afterID {
			return result, fmt.Errorf("quota recovery candidate pagination did not advance after id %d", afterID)
		}
		result.Listed += len(accounts)

		outcomes := make(chan quotaRecoveryOutcome, len(eligible))
		var wg sync.WaitGroup
		for i := range eligible {
			account := eligible[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				outcomes <- s.reconcileAccount(ctx, repo, slots, &account)
			}()
		}
		wg.Wait()
		close(outcomes)

		for outcome := range outcomes {
			if outcome.checked {
				result.Checked++
			}
			switch outcome.verdict {
			case QuotaRecoveryExhausted:
				result.Exhausted++
			case QuotaRecoveryUnknown:
				result.Unknown++
			}
			if outcome.recovered {
				result.Recovered++
			}
			if outcome.casMiss {
				result.CASMisses++
			}
			if outcome.err != nil {
				result.Errors++
				slog.Warn("quota_recovery_account_failed",
					"account_id", outcome.accountID,
					"error", outcome.err,
				)
			}
		}

		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(accounts) < pageSize {
			break
		}
		afterID = pageLastID
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

type quotaRecoveryOutcome struct {
	accountID int64
	checked   bool
	verdict   QuotaRecoveryState
	recovered bool
	casMiss   bool
	err       error
}

func (s *QuotaRecoveryService) reconcileAccount(
	ctx context.Context,
	repo QuotaRecoveryAccountRepository,
	slots chan struct{},
	account *Account,
) quotaRecoveryOutcome {
	outcome := quotaRecoveryOutcome{accountID: account.ID, verdict: QuotaRecoveryUnknown}
	runtimeGuard, canGuardRuntimeBlock := s.runtimeBlocker.(quotaRecoveryRuntimeBlockGuard)
	var observedRuntimeGeneration uint64
	if canGuardRuntimeBlock {
		observedRuntimeGeneration = runtimeGuard.AccountSchedulingBlockGeneration(account.ID)
	}
	if delay := s.randomJitter(); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			outcome.err = ctx.Err()
			return outcome
		case <-timer.C:
		}
	}

	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		outcome.err = ctx.Err()
		return outcome
	}

	observation, credentialOwner, err := s.quotaRecoveryObservation(ctx, account)
	if err != nil {
		outcome.err = err
		return outcome
	}

	checkTimeoutCtx, cancel := context.WithTimeout(ctx, s.timeout())
	checkCtx, credentialReceipt, err := withQuotaRecoveryCredentialRefreshReceipt(
		checkTimeoutCtx,
		account,
		credentialOwner,
	)
	if err != nil {
		cancel()
		outcome.err = fmt.Errorf("start quota recovery credential receipt: %w", err)
		return outcome
	}
	if err := credentialReceipt.applyToObservation(&observation); err != nil {
		cancel()
		outcome.err = fmt.Errorf("bind quota recovery credential receipt: %w", err)
		return outcome
	}
	checkResult := s.checker.Check(checkCtx, account)
	checkErr := checkCtx.Err()
	receiptErr := credentialReceipt.applyToObservation(&observation)
	cancel()
	outcome.checked = true
	outcome.verdict = checkResult.Verdict
	if checkErr != nil {
		outcome.verdict = QuotaRecoveryUnknown
		outcome.err = checkErr
		return outcome
	}
	if receiptErr != nil {
		outcome.verdict = QuotaRecoveryUnknown
		outcome.err = fmt.Errorf("finalize quota recovery credential receipt: %w", receiptErr)
		return outcome
	}

	if checkResult.Verdict != QuotaRecoveryAvailable ||
		!checkResult.Authoritative ||
		!s.freshCheckResult(checkResult) {
		if checkResult.Verdict == QuotaRecoveryAvailable {
			outcome.verdict = QuotaRecoveryUnknown
		}
		return outcome
	}
	if err := ctx.Err(); err != nil {
		outcome.verdict = QuotaRecoveryUnknown
		outcome.err = err
		return outcome
	}

	var releaseGenerationGuard func()
	if canGuardRuntimeBlock {
		var matched bool
		releaseGenerationGuard, matched = runtimeGuard.GuardAccountSchedulingBlockForQuotaRecovery(
			account.ID,
			observedRuntimeGeneration,
			account.RateLimitedAt.UTC(),
			account.RateLimitResetAt.UTC(),
		)
		if !matched {
			outcome.verdict = QuotaRecoveryUnknown
			return outcome
		}
	}

	cleared, err := s.clearRateLimitIfUnchanged(ctx, repo, observation, releaseGenerationGuard)
	if err != nil {
		outcome.err = fmt.Errorf("compare-and-clear rate limit: %w", err)
		return outcome
	}
	if !cleared {
		outcome.casMiss = true
		return outcome
	}

	outcome.recovered = true
	if canGuardRuntimeBlock {
		// In the supported single-process deployment, clear the process-local
		// runtime bridge only when no newer block appeared during the check.
		runtimeGuard.ClearAccountSchedulingBlockForQuotaRecovery(
			account.ID,
			observedRuntimeGeneration,
			account.RateLimitedAt.UTC(),
			account.RateLimitResetAt.UTC(),
		)
	}
	slog.Info("quota_recovery_account_recovered",
		"account_id", account.ID,
		"platform", account.Platform,
		"source", checkResult.Source,
	)
	return outcome
}

func (s *QuotaRecoveryService) clearRateLimitIfUnchanged(
	ctx context.Context,
	repo QuotaRecoveryAccountRepository,
	observation QuotaRecoveryObservation,
	releaseGenerationGuard func(),
) (bool, error) {
	if releaseGenerationGuard != nil {
		defer releaseGenerationGuard()
	}
	clearCtx, clearCancel := context.WithTimeout(ctx, s.timeout())
	defer clearCancel()
	cleared, err := repo.ClearRateLimitIfUnchanged(clearCtx, observation)
	if err != nil {
		return false, err
	}
	return cleared, nil
}

func (s *QuotaRecoveryService) quotaRecoveryObservation(ctx context.Context, account *Account) (QuotaRecoveryObservation, *Account, error) {
	observation := QuotaRecoveryObservation{
		AccountID:        account.ID,
		RateLimitedAt:    account.RateLimitedAt.UTC(),
		RateLimitResetAt: account.RateLimitResetAt.UTC(),
		AccountUpdatedAt: account.UpdatedAt.UTC(),
	}
	credentialOwner := account
	if !account.IsShadow() {
		applyCredentialSnapshotToObservation(&observation, accountCredentialSnapshot(credentialOwner), account.ID)
		return observation, credentialOwner, nil
	}
	ownerCtx, ownerCancel := context.WithTimeout(ctx, s.timeout())
	credentialOwner, err := resolveCredentialAccount(ownerCtx, s.accountRepo, account)
	if err == nil {
		err = ownerCtx.Err()
	}
	ownerCancel()
	if err != nil {
		return QuotaRecoveryObservation{}, nil, fmt.Errorf("resolve quota recovery credential owner: %w", err)
	}
	if credentialOwner == nil || credentialOwner.ID <= 0 {
		return QuotaRecoveryObservation{}, nil, fmt.Errorf("resolve quota recovery credential owner: invalid account")
	}
	applyCredentialSnapshotToObservation(&observation, accountCredentialSnapshot(credentialOwner), account.ID)
	return observation, credentialOwner, nil
}

func isQuotaRecoveryCandidate(account *Account, now time.Time) bool {
	return account != nil &&
		account.ID > 0 &&
		account.Type == AccountTypeOAuth &&
		(account.Platform == PlatformOpenAI || account.Platform == PlatformAnthropic) &&
		account.Status == StatusActive &&
		account.Schedulable &&
		account.RateLimitedAt != nil &&
		account.RateLimitResetAt != nil &&
		account.RateLimitResetAt.After(now)
}

func (s *QuotaRecoveryService) freshCheckResult(result QuotaRecoveryCheckResult) bool {
	if result.CheckedAt.IsZero() {
		return false
	}
	age := s.currentTime().UTC().Sub(result.CheckedAt.UTC())
	return age >= -quotaRecoveryFreshnessSkew && age <= s.timeout()+quotaRecoveryFreshnessSkew
}

func (s *QuotaRecoveryService) enabled() bool {
	return s != nil &&
		s.cfg != nil &&
		s.cfg.QuotaRecovery.Enabled
}

func (s *QuotaRecoveryService) interval() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.QuotaRecovery.IntervalSeconds > 0 {
		return time.Duration(s.cfg.QuotaRecovery.IntervalSeconds) * time.Second
	}
	return defaultQuotaRecoveryInterval
}

func (s *QuotaRecoveryService) batchSize() int {
	if s != nil && s.cfg != nil && s.cfg.QuotaRecovery.BatchSize > 0 {
		return s.cfg.QuotaRecovery.BatchSize
	}
	return defaultQuotaRecoveryBatchSize
}

func (s *QuotaRecoveryService) concurrency() int {
	if s != nil && s.cfg != nil && s.cfg.QuotaRecovery.Concurrency > 0 {
		return s.cfg.QuotaRecovery.Concurrency
	}
	return defaultQuotaRecoveryConcurrency
}

func (s *QuotaRecoveryService) timeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.QuotaRecovery.TimeoutSeconds > 0 {
		return time.Duration(s.cfg.QuotaRecovery.TimeoutSeconds) * time.Second
	}
	return defaultQuotaRecoveryTimeout
}

func (s *QuotaRecoveryService) maxJitter() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.QuotaRecovery.JitterSeconds > 0 {
		return time.Duration(s.cfg.QuotaRecovery.JitterSeconds) * time.Second
	}
	return 0
}

func (s *QuotaRecoveryService) randomJitter() time.Duration {
	if s == nil || s.jitterFor == nil {
		return 0
	}
	return s.jitterFor(s.maxJitter())
}

func (s *QuotaRecoveryService) singletonDatabase() *sql.DB {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

func (s *QuotaRecoveryService) singletonHealthInterval() time.Duration {
	if s == nil {
		return quotaRecoverySingletonHealthInterval
	}
	s.mu.Lock()
	interval := s.leaseHealthInterval
	s.mu.Unlock()
	if interval <= 0 {
		return quotaRecoverySingletonHealthInterval
	}
	return interval
}

func (s *QuotaRecoveryService) singletonReacquireDelay(attempt int) time.Duration {
	if s != nil {
		s.mu.Lock()
		delayFor := s.reacquireDelayFor
		s.mu.Unlock()
		if delayFor != nil {
			return delayFor(attempt)
		}
	}

	backoff := quotaRecoveryReacquireMinBackoff
	for step := 1; step < attempt && backoff < quotaRecoveryReacquireMaxBackoff; step++ {
		if backoff >= quotaRecoveryReacquireMaxBackoff/2 {
			backoff = quotaRecoveryReacquireMaxBackoff
			break
		}
		backoff *= 2
	}
	if backoff > quotaRecoveryReacquireMaxBackoff {
		backoff = quotaRecoveryReacquireMaxBackoff
	}

	// Equal jitter preserves a meaningful lower bound while avoiding a thundering
	// herd when multiple processes notice the same database interruption.
	half := backoff / 2
	return half + time.Duration(rand.Int64N(int64(backoff-half)+1))
}

// GetStatus returns a process-local, redacted Hermes status snapshot. It is a
// read-only operation: it never acquires a lease, queries the account
// repository, or starts a reconciliation cycle.
func (s *QuotaRecoveryService) GetStatus() QuotaRecoveryStatus {
	if s == nil {
		return QuotaRecoveryStatus{
			Status:         "disabled",
			LifecycleState: "stopped",
			HealthReason:   "service_unavailable",
			Config: QuotaRecoveryStatusConfig{
				IntervalSeconds: int(defaultQuotaRecoveryInterval / time.Second),
				BatchSize:       defaultQuotaRecoveryBatchSize,
				Concurrency:     defaultQuotaRecoveryConcurrency,
				TimeoutSeconds:  int(defaultQuotaRecoveryTimeout / time.Second),
			},
		}
	}

	enabled := s.enabled()
	configSnapshot := QuotaRecoveryStatusConfig{
		IntervalSeconds: int(s.interval() / time.Second),
		BatchSize:       s.batchSize(),
		Concurrency:     s.concurrency(),
		TimeoutSeconds:  int(s.timeout() / time.Second),
		JitterSeconds:   int(s.maxJitter() / time.Second),
	}

	s.statusMu.RLock()
	lifecycle := s.statusLifecycle
	startedAt := cloneQuotaRecoveryTime(s.statusStartedAt)
	leaseHeld := s.statusLeaseHeld
	leaseHealthy := s.statusLeaseHealthy
	leaseAcquiredAt := cloneQuotaRecoveryTime(s.statusLeaseAcquired)
	leaseLostAt := cloneQuotaRecoveryTime(s.statusLeaseLost)
	reacquiredAt := cloneQuotaRecoveryTime(s.statusReacquired)
	nextRunAt := cloneQuotaRecoveryTime(s.statusNextRunAt)
	currentRun := cloneQuotaRecoveryRunStatus(s.statusCurrentRun)
	lastRun := cloneQuotaRecoveryRunStatus(s.statusLastRun)
	observedAt := s.statusObservedAt
	s.statusMu.RUnlock()

	if lifecycle == "" {
		s.mu.Lock()
		lifecycle = quotaRecoveryLifecycleName(s.state)
		s.mu.Unlock()
	}
	if observedAt.IsZero() {
		observedAt = s.currentTime().UTC()
	}
	status, healthy, healthReason := quotaRecoveryStatusHealth(
		enabled,
		lifecycle,
		leaseHeld,
		leaseHealthy,
		lastRun,
	)
	return QuotaRecoveryStatus{
		Enabled:             enabled,
		Status:              status,
		Healthy:             healthy,
		HealthReason:        healthReason,
		LifecycleState:      lifecycle,
		LeaseHeld:           leaseHeld,
		LeaseHealthy:        leaseHealthy,
		StartedAt:           startedAt,
		ObservedAt:          observedAt.UTC(),
		LastLeaseAcquiredAt: leaseAcquiredAt,
		LastLeaseLostAt:     leaseLostAt,
		LastReacquiredAt:    reacquiredAt,
		NextRunAt:           nextRunAt,
		CurrentRun:          currentRun,
		LastRun:             lastRun,
		Config:              configSnapshot,
	}
}

func quotaRecoveryLifecycleName(state quotaRecoveryLifecycleState) string {
	switch state {
	case quotaRecoveryRunning:
		return "running"
	case quotaRecoveryStopping:
		return "stopping"
	default:
		return "stopped"
	}
}

func quotaRecoveryStatusHealth(
	enabled bool,
	lifecycle string,
	leaseHeld bool,
	leaseHealthy bool,
	lastRun *QuotaRecoveryRunStatus,
) (status string, healthy bool, reason string) {
	if !enabled {
		return "disabled", false, "disabled_by_configuration"
	}
	if lifecycle == "reacquiring" {
		return "error", false, "singleton_lease_reacquiring"
	}
	if lifecycle == "stopping" {
		return "unknown", false, "stopping"
	}
	if lifecycle != "running" {
		return "unknown", false, "not_running"
	}
	if !leaseHeld || !leaseHealthy {
		return "error", false, "singleton_lease_unhealthy"
	}
	if lastRun == nil {
		// A healthy worker is allowed to report unknown history while it waits
		// for its first cycle; this is not treated as a runtime failure.
		return "unknown", true, "awaiting_first_run"
	}
	if lastRun.LastError != "" {
		return "warning", true, "last_run_failed"
	}
	if lastRun.Errors > 0 {
		return "warning", true, "last_run_account_errors"
	}
	return "healthy", true, "running"
}

func cloneQuotaRecoveryTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func cloneQuotaRecoveryRunStatus(value *QuotaRecoveryRunStatus) *QuotaRecoveryRunStatus {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.CompletedAt = cloneQuotaRecoveryTime(value.CompletedAt)
	return &cloned
}

func quotaRecoveryStatusTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func (s *QuotaRecoveryService) touchStatusLocked(at time.Time) {
	s.statusObservedAt = quotaRecoveryStatusTime(at)
}

func (s *QuotaRecoveryService) touchStatus(at time.Time) {
	if s == nil {
		return
	}
	s.statusMu.Lock()
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) markStatusLifecycle(lifecycle string, at time.Time) {
	if s == nil {
		return
	}
	s.statusMu.Lock()
	s.statusLifecycle = lifecycle
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) markLeaseAcquired(at time.Time) {
	if s == nil {
		return
	}
	at = quotaRecoveryStatusTime(at)
	s.statusMu.Lock()
	s.statusLifecycle = "running"
	s.statusLeaseHeld = true
	s.statusLeaseHealthy = true
	s.statusLeaseAcquired = cloneQuotaRecoveryTime(&at)
	if s.statusStartedAt == nil {
		s.statusStartedAt = cloneQuotaRecoveryTime(&at)
	}
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) markLeaseLost(at time.Time) {
	if s == nil {
		return
	}
	at = quotaRecoveryStatusTime(at)
	s.statusMu.Lock()
	s.statusLifecycle = "reacquiring"
	s.statusLeaseHeld = false
	s.statusLeaseHealthy = false
	s.statusLeaseLost = cloneQuotaRecoveryTime(&at)
	// The previous schedule is no longer authoritative while the singleton
	// lease is lost; it will be recomputed when the worker reacquires the lease.
	s.statusNextRunAt = nil
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) markLeaseReacquired(at time.Time) {
	if s == nil {
		return
	}
	at = quotaRecoveryStatusTime(at)
	s.statusMu.Lock()
	s.statusLifecycle = "running"
	s.statusLeaseHeld = true
	s.statusLeaseHealthy = true
	s.statusLeaseAcquired = cloneQuotaRecoveryTime(&at)
	s.statusReacquired = cloneQuotaRecoveryTime(&at)
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) markStopped(at time.Time) {
	if s == nil {
		return
	}
	s.statusMu.Lock()
	s.statusLifecycle = "stopped"
	s.statusLeaseHeld = false
	s.statusLeaseHealthy = false
	s.statusNextRunAt = nil
	s.touchStatusLocked(at)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) beginRunStatus(trigger string, startedAt time.Time, scheduled bool) {
	if s == nil {
		return
	}
	startedAt = quotaRecoveryStatusTime(startedAt)
	run := &QuotaRecoveryRunStatus{Trigger: trigger, StartedAt: startedAt}
	s.statusMu.Lock()
	s.statusCurrentRun = run
	if scheduled {
		next := startedAt.Add(s.interval())
		s.statusNextRunAt = cloneQuotaRecoveryTime(&next)
	}
	s.touchStatusLocked(startedAt)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) finishRunStatus(result QuotaRecoveryRunResult, runErr error) {
	if s == nil {
		return
	}
	completedAt := quotaRecoveryStatusTime(s.currentTime())
	s.statusMu.Lock()
	run := cloneQuotaRecoveryRunStatus(s.statusCurrentRun)
	if errors.Is(runErr, context.Canceled) {
		// A normal stop or singleton lease loss cancels the in-flight cycle.
		// Keep the last fully observed cycle intact so the status page does not
		// present partial counters as a completed run.
		s.statusCurrentRun = nil
		s.touchStatusLocked(completedAt)
		s.statusMu.Unlock()
		return
	}
	if run == nil {
		run = &QuotaRecoveryRunStatus{Trigger: "unknown", StartedAt: completedAt}
	}
	run.CompletedAt = cloneQuotaRecoveryTime(&completedAt)
	run.DurationMS = completedAt.Sub(run.StartedAt).Milliseconds()
	if run.DurationMS < 0 {
		run.DurationMS = 0
	}
	run.Listed = result.Listed
	run.Checked = result.Checked
	run.Recovered = result.Recovered
	run.Exhausted = result.Exhausted
	run.Unknown = result.Unknown
	run.Skipped = result.Skipped
	run.CASMisses = result.CASMisses
	run.Errors = result.Errors
	run.LastError = quotaRecoveryPublicRunError(runErr)
	s.statusCurrentRun = nil
	s.statusLastRun = run
	s.touchStatusLocked(completedAt)
	s.statusMu.Unlock()
}

func (s *QuotaRecoveryService) setNextRunAt(next time.Time) {
	if s == nil {
		return
	}
	next = quotaRecoveryStatusTime(next)
	s.statusMu.Lock()
	if s.statusLifecycle == "running" {
		s.statusNextRunAt = cloneQuotaRecoveryTime(&next)
		s.touchStatusLocked(s.currentTime().UTC())
	}
	s.statusMu.Unlock()
}

func quotaRecoveryPublicRunError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "run timed out"
	}
	if errors.Is(err, ErrQuotaRecoveryRepositoryUnavailable) {
		return "repository unavailable"
	}
	if errors.Is(err, ErrQuotaRecoveryDatabaseUnavailable) {
		return "database unavailable"
	}
	if errors.Is(err, ErrQuotaRecoveryAlreadyRunning) {
		return "already running"
	}
	return "reconciliation failed"
}

func (s *QuotaRecoveryService) lifecycleLogger() *slog.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

func (s *QuotaRecoveryService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}
