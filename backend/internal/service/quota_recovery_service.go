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
	quotaRecoveryFreshnessSkew           = 5 * time.Second

	defaultQuotaRecoveryInterval    = 24 * time.Hour
	defaultQuotaRecoveryBatchSize   = 50
	defaultQuotaRecoveryConcurrency = 3
	defaultQuotaRecoveryTimeout     = 25 * time.Second
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

	cycleGate chan struct{}
	mu        sync.Mutex
	state     quotaRecoveryLifecycleState
	cancel    context.CancelFunc
	done      chan struct{}
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
	return &QuotaRecoveryService{
		accountRepo:         accountRepo,
		checker:             checker,
		runtimeBlocker:      runtimeBlocker,
		cfg:                 cfg,
		now:                 time.Now,
		leaseHealthInterval: quotaRecoverySingletonHealthInterval,
		cycleGate:           make(chan struct{}, 1),
		jitterFor: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max) + 1))
		},
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
			s.mu.Unlock()
			return nil
		}
		if s.db == nil {
			s.mu.Unlock()
			return ErrQuotaRecoveryDatabaseUnavailable
		}
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		go s.runWithSingletonLease(ctx, cancel, done, lease)
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
		if s.cancel != nil {
			s.cancel()
		}
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *QuotaRecoveryService) runWithSingletonLease(
	ctx context.Context,
	cancel context.CancelFunc,
	done chan struct{},
	lease *dbAdvisoryLockLease,
) {
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		s.monitorSingletonLease(ctx, cancel, done, lease)
	}()

	s.runLoop(ctx)
	cancel()
	<-monitorDone
	if err := lease.Release(); err != nil {
		slog.Warn("quota_recovery_singleton_release_failed", "error", err)
	}

	s.mu.Lock()
	if s.done == done {
		s.state = quotaRecoveryStopped
		s.cancel = nil
		s.done = nil
	}
	close(done)
	s.mu.Unlock()
}

func (s *QuotaRecoveryService) monitorSingletonLease(
	ctx context.Context,
	cancel context.CancelFunc,
	done chan struct{},
	lease *dbAdvisoryLockLease,
) {
	interval := s.leaseHealthInterval
	if interval <= 0 {
		interval = quotaRecoverySingletonHealthInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, quotaRecoverySingletonHealthTimeout)
			err := lease.Ping(pingCtx)
			pingCancel()
			if err == nil {
				continue
			}
			if ctx.Err() == nil {
				s.mu.Lock()
				shouldStop := s.done == done && s.state == quotaRecoveryRunning
				if shouldStop {
					s.state = quotaRecoveryStopping
				}
				s.mu.Unlock()
				if shouldStop {
					slog.Error("quota_recovery_singleton_connection_lost",
						"error", err,
						"restart_required", true,
					)
					cancel()
				}
			}
			return
		}
	}
}

func (s *QuotaRecoveryService) runLoop(ctx context.Context) {
	for {
		cycleStartedAt := time.Now()
		result, err := s.RunOnce(ctx)
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
	result := QuotaRecoveryRunResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || !s.enabled() {
		return result, nil
	}
	repo, ok := s.accountRepo.(QuotaRecoveryAccountRepository)
	if !ok || repo == nil {
		return result, ErrQuotaRecoveryRepositoryUnavailable
	}
	if s.checker == nil {
		return result, fmt.Errorf("quota recovery checker is unavailable")
	}

	select {
	case s.cycleGate <- struct{}{}:
		defer func() { <-s.cycleGate }()
	case <-ctx.Done():
		return result, ctx.Err()
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

	observation, err := s.quotaRecoveryObservation(ctx, account)
	if err != nil {
		outcome.err = err
		return outcome
	}

	checkCtx, cancel := context.WithTimeout(ctx, s.timeout())
	checkResult := s.checker.Check(checkCtx, account)
	checkErr := checkCtx.Err()
	cancel()
	outcome.checked = true
	outcome.verdict = checkResult.Verdict
	if checkErr != nil {
		outcome.verdict = QuotaRecoveryUnknown
		outcome.err = checkErr
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

func (s *QuotaRecoveryService) quotaRecoveryObservation(ctx context.Context, account *Account) (QuotaRecoveryObservation, error) {
	observation := QuotaRecoveryObservation{
		AccountID:                account.ID,
		RateLimitedAt:            account.RateLimitedAt.UTC(),
		RateLimitResetAt:         account.RateLimitResetAt.UTC(),
		AccountUpdatedAt:         account.UpdatedAt.UTC(),
		CredentialOwnerID:        account.ID,
		CredentialOwnerUpdatedAt: account.UpdatedAt.UTC(),
	}
	if !account.IsShadow() {
		return observation, nil
	}
	ownerCtx, ownerCancel := context.WithTimeout(ctx, s.timeout())
	credentialOwner, err := resolveCredentialAccount(ownerCtx, s.accountRepo, account)
	if err == nil {
		err = ownerCtx.Err()
	}
	ownerCancel()
	if err != nil {
		return QuotaRecoveryObservation{}, fmt.Errorf("resolve quota recovery credential owner: %w", err)
	}
	if credentialOwner == nil || credentialOwner.ID <= 0 {
		return QuotaRecoveryObservation{}, fmt.Errorf("resolve quota recovery credential owner: invalid account")
	}
	observation.CredentialOwnerID = credentialOwner.ID
	observation.CredentialOwnerUpdatedAt = credentialOwner.UpdatedAt.UTC()
	return observation, nil
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

func (s *QuotaRecoveryService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}
