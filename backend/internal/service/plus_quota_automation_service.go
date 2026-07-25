package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	PlusQuotaAutomationSettingKey      = "openai_plus_quota_automation"
	PlusQuotaAutomationStateSettingKey = "openai_plus_quota_automation_state"

	PlusQuotaAnomalyExtraKey      = "plus_quota_anomaly"
	PlusQuotaLastResetExtraKey    = "plus_quota_auto_reset_last_success_at"
	PlusQuotaResetAttemptExtraKey = "plus_quota_auto_reset_attempt"

	plusQuotaAutomationLockKey      = "openai-plus-quota-automation"
	plusQuotaAutomationPollInterval = 15 * time.Second
	plusQuotaResetCooldown          = 30 * time.Minute
	plusQuotaAutomationConcurrency  = 3
	plusQuotaDefaultIntervalSeconds = 300
	plusQuotaDefaultThreshold       = 100
	plusQuotaMaxPageSize            = 100

	PlusQuotaAnomalyStatusOpen     = "open"
	PlusQuotaAnomalyStatusResolved = "resolved"

	plusQuotaResetAttemptStatusAttempting = "attempting"
	plusQuotaResetAttemptStatusAmbiguous  = "ambiguous"
	plusQuotaResetAttemptStatusSucceeded  = "succeeded"
	plusQuotaResetAttemptStatusReconciled = "reconciled"
)

var (
	ErrPlusQuotaAutomationRunning = infraerrors.New(http.StatusConflict, "PLUS_QUOTA_AUTOMATION_RUNNING", "a Plus quota scan is already running")
	ErrPlusQuotaGroupRequired     = infraerrors.New(http.StatusBadRequest, "PLUS_QUOTA_GROUP_REQUIRED", "an OpenAI group must be selected")
	ErrPlusQuotaAnomalyNotFound   = infraerrors.New(http.StatusNotFound, "PLUS_QUOTA_ANOMALY_NOT_FOUND", "Plus quota anomaly not found")
)

type plusQuotaAutomationAccountRepository interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	ListAllWithFilters(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error)
	ListByGroup(ctx context.Context, groupID int64) ([]Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type plusQuotaAutomationClient interface {
	QueryUsageStrict(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
	ResetCreditWithRequestID(ctx context.Context, accountID int64, requestID string) (*OpenAIQuotaResetResult, error)
}

type plusQuotaAutomationSettingStore interface {
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

type plusQuotaAutomationLockAcquirer func(ctx context.Context) (release func(), acquired bool, err error)

type PlusQuotaAutomationConfig struct {
	Enabled              bool    `json:"enabled"`
	GroupID              int64   `json:"group_id"`
	IntervalSeconds      int     `json:"interval_seconds"`
	UtilizationThreshold float64 `json:"utilization_threshold"`
}

type PlusQuotaAutomationState struct {
	Running       bool       `json:"running"`
	Trigger       string     `json:"trigger,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Scanned       int        `json:"scanned"`
	Eligible      int        `json:"eligible"`
	AtLimit       int        `json:"at_limit"`
	ResetCount    int        `json:"reset_count"`
	Unauthorized  int        `json:"unauthorized"`
	Failed        int        `json:"failed"`
	NoCredits     int        `json:"no_credits"`
	Cooldown      int        `json:"cooldown"`
	Skipped       int        `json:"skipped"`
	LastError     string     `json:"last_error,omitempty"`
	SkippedReason string     `json:"skipped_reason,omitempty"`
}

type plusQuotaAutomationStateRecord struct {
	PlusQuotaAutomationState
	CadenceSignature string     `json:"cadence_signature,omitempty"`
	NextRunAt        *time.Time `json:"next_run_at,omitempty"`
}

type plusQuotaResetAttempt struct {
	RequestID     string     `json:"request_id"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	LastAttemptAt time.Time  `json:"last_attempt_at"`
	AttemptCount  int        `json:"attempt_count"`
	LastErrorCode string     `json:"last_error_code,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type PlusQuotaAutomationOverview struct {
	Config    PlusQuotaAutomationConfig `json:"config"`
	State     PlusQuotaAutomationState  `json:"state"`
	NextRunAt *time.Time                `json:"next_run_at,omitempty"`
}

type PlusQuotaAnomaly struct {
	AccountID       int64      `json:"account_id"`
	AccountName     string     `json:"account_name"`
	Email           string     `json:"email"`
	GroupID         int64      `json:"group_id"`
	Stage           string     `json:"stage"`
	HTTPStatus      int        `json:"http_status"`
	FirstDetectedAt time.Time  `json:"first_detected_at"`
	LastDetectedAt  time.Time  `json:"last_detected_at"`
	Count           int        `json:"count"`
	Status          string     `json:"status"`
	LastError       string     `json:"last_error"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

type PlusQuotaAnomalyList struct {
	Items    []PlusQuotaAnomaly `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

type PlusQuotaAutomationService struct {
	accountRepo plusQuotaAutomationAccountRepository
	quota       plusQuotaAutomationClient
	settings    plusQuotaAutomationSettingStore
	acquireLock plusQuotaAutomationLockAcquirer

	parentCtx context.Context
	cancel    context.CancelFunc
	wakeCh    chan struct{}
	runGate   chan struct{}

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	wg          sync.WaitGroup

	runtimeMu sync.RWMutex
	running   *PlusQuotaAutomationState

	now func() time.Time
}

func NewPlusQuotaAutomationService(
	accountRepo plusQuotaAutomationAccountRepository,
	quota plusQuotaAutomationClient,
	settings plusQuotaAutomationSettingStore,
	db *sql.DB,
) *PlusQuotaAutomationService {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &PlusQuotaAutomationService{
		accountRepo: accountRepo,
		quota:       quota,
		settings:    settings,
		parentCtx:   ctx,
		cancel:      cancel,
		wakeCh:      make(chan struct{}, 1),
		runGate:     make(chan struct{}, 1),
		now:         time.Now,
	}
	if db != nil {
		svc.acquireLock = func(ctx context.Context) (func(), bool, error) {
			return tryAcquireDBAdvisoryLockWithError(ctx, db, hashAdvisoryLockID(plusQuotaAutomationLockKey))
		}
	}
	return svc
}

func ProvidePlusQuotaAutomationService(
	accountRepo AccountRepository,
	quota *OpenAIQuotaService,
	settings SettingRepository,
	db *sql.DB,
) *PlusQuotaAutomationService {
	svc := NewPlusQuotaAutomationService(accountRepo, quota, settings, db)
	svc.Start()
	return svc
}

func defaultPlusQuotaAutomationConfig() PlusQuotaAutomationConfig {
	return PlusQuotaAutomationConfig{
		IntervalSeconds:      plusQuotaDefaultIntervalSeconds,
		UtilizationThreshold: plusQuotaDefaultThreshold,
	}
}

func normalizePlusQuotaAutomationConfig(cfg PlusQuotaAutomationConfig) PlusQuotaAutomationConfig {
	if cfg.IntervalSeconds == 0 {
		cfg.IntervalSeconds = plusQuotaDefaultIntervalSeconds
	}
	if cfg.UtilizationThreshold == 0 {
		cfg.UtilizationThreshold = plusQuotaDefaultThreshold
	}
	return cfg
}

func validatePlusQuotaAutomationConfig(cfg PlusQuotaAutomationConfig) error {
	if cfg.Enabled && cfg.GroupID <= 0 {
		return ErrPlusQuotaGroupRequired
	}
	if cfg.GroupID < 0 {
		return infraerrors.New(http.StatusBadRequest, "PLUS_QUOTA_INVALID_GROUP", "group_id must be zero or a positive integer")
	}
	if cfg.IntervalSeconds < 60 || cfg.IntervalSeconds > 86400 {
		return infraerrors.New(http.StatusBadRequest, "PLUS_QUOTA_INVALID_INTERVAL", "interval_seconds must be between 60 and 86400")
	}
	if cfg.UtilizationThreshold < 1 || cfg.UtilizationThreshold > 100 {
		return infraerrors.New(http.StatusBadRequest, "PLUS_QUOTA_INVALID_THRESHOLD", "utilization_threshold must be between 1 and 100")
	}
	return nil
}

func (s *PlusQuotaAutomationService) GetConfig(ctx context.Context) (PlusQuotaAutomationConfig, error) {
	cfg := defaultPlusQuotaAutomationConfig()
	if s == nil || s.settings == nil {
		return cfg, infraerrors.New(http.StatusInternalServerError, "PLUS_QUOTA_SETTINGS_UNAVAILABLE", "Plus quota settings are unavailable")
	}
	raw, err := s.settings.GetValue(ctx, PlusQuotaAutomationSettingKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("decode Plus quota automation settings: %w", err)
	}
	cfg = normalizePlusQuotaAutomationConfig(cfg)
	if err := validatePlusQuotaAutomationConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (s *PlusQuotaAutomationService) UpdateConfig(ctx context.Context, cfg PlusQuotaAutomationConfig) (PlusQuotaAutomationConfig, error) {
	cfg = normalizePlusQuotaAutomationConfig(cfg)
	if err := validatePlusQuotaAutomationConfig(cfg); err != nil {
		return PlusQuotaAutomationConfig{}, err
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return PlusQuotaAutomationConfig{}, err
	}
	if s == nil || s.settings == nil {
		return PlusQuotaAutomationConfig{}, infraerrors.New(http.StatusInternalServerError, "PLUS_QUOTA_SETTINGS_UNAVAILABLE", "Plus quota settings are unavailable")
	}
	if err := s.settings.Set(ctx, PlusQuotaAutomationSettingKey, string(payload)); err != nil {
		return PlusQuotaAutomationConfig{}, err
	}
	s.signalWake()
	return cfg, nil
}

func (s *PlusQuotaAutomationService) GetOverview(ctx context.Context) (*PlusQuotaAutomationOverview, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.loadStateRecord(ctx)
	if err != nil {
		return nil, err
	}
	state := record.PlusQuotaAutomationState
	s.runtimeMu.RLock()
	if s.running != nil {
		state = *s.running
	}
	s.runtimeMu.RUnlock()
	return &PlusQuotaAutomationOverview{
		Config:    cfg,
		State:     state,
		NextRunAt: cloneTimePointer(record.NextRunAt),
	}, nil
}

func (s *PlusQuotaAutomationService) loadStateRecord(ctx context.Context) (plusQuotaAutomationStateRecord, error) {
	if s == nil || s.settings == nil {
		return plusQuotaAutomationStateRecord{}, infraerrors.New(http.StatusInternalServerError, "PLUS_QUOTA_SETTINGS_UNAVAILABLE", "Plus quota settings are unavailable")
	}
	raw, err := s.settings.GetValue(ctx, PlusQuotaAutomationStateSettingKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return plusQuotaAutomationStateRecord{}, nil
		}
		return plusQuotaAutomationStateRecord{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return plusQuotaAutomationStateRecord{}, nil
	}
	var record plusQuotaAutomationStateRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return plusQuotaAutomationStateRecord{}, fmt.Errorf("decode Plus quota automation state: %w", err)
	}
	return record, nil
}

func (s *PlusQuotaAutomationService) saveStateRecord(ctx context.Context, record plusQuotaAutomationStateRecord) error {
	if s == nil || s.settings == nil {
		return nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.settings.Set(ctx, PlusQuotaAutomationStateSettingKey, string(payload))
}

func (s *PlusQuotaAutomationService) Start() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runLoop()
	}()
}

func (s *PlusQuotaAutomationService) Stop() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.cancel()
	}
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

func (s *PlusQuotaAutomationService) TriggerRun() error {
	if s == nil {
		return infraerrors.New(http.StatusInternalServerError, "PLUS_QUOTA_AUTOMATION_UNAVAILABLE", "Plus quota automation is unavailable")
	}
	cfg, err := s.GetConfig(context.Background())
	if err != nil {
		return err
	}
	if cfg.GroupID <= 0 {
		return ErrPlusQuotaGroupRequired
	}
	if err := s.acquireTrackedRunGate(); err != nil {
		return err
	}
	releaseLock, err := s.acquireExecutionLock(context.Background())
	if err != nil {
		s.releaseTrackedRunGate()
		return err
	}
	if err := s.stoppedError(); err != nil {
		releaseLock()
		s.releaseTrackedRunGate()
		return err
	}

	record, err := s.loadStateRecord(context.Background())
	if err != nil {
		releaseLock()
		s.releaseTrackedRunGate()
		return err
	}
	state, record, err := s.beginRunLocked(context.Background(), cfg, "manual", record)
	if err != nil {
		releaseLock()
		s.releaseTrackedRunGate()
		return err
	}
	go func() {
		defer s.releaseTrackedRunGate()
		defer releaseLock()
		if _, runErr := s.executeRunLocked(s.parentCtx, cfg, state, record); runErr != nil {
			slog.Warn("plus_quota_automation_manual_run_failed", "error", runErr)
		}
	}()
	return nil
}

func (s *PlusQuotaAutomationService) RunOnce(ctx context.Context, trigger string) (*PlusQuotaAutomationState, error) {
	if err := s.acquireTrackedRunGate(); err != nil {
		return nil, err
	}
	defer s.releaseTrackedRunGate()
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.GroupID <= 0 {
		return nil, ErrPlusQuotaGroupRequired
	}
	releaseLock, err := s.acquireExecutionLock(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseLock()
	if err := s.stoppedError(); err != nil {
		return nil, err
	}
	record, err := s.loadStateRecord(ctx)
	if err != nil {
		return nil, err
	}
	return s.runCycleLocked(ctx, cfg, trigger, record)
}

func (s *PlusQuotaAutomationService) acquireExecutionLock(ctx context.Context) (func(), error) {
	if s.acquireLock == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "PLUS_QUOTA_LOCK_UNAVAILABLE", "Plus quota automation lock is unavailable")
	}
	lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
	release, acquired, err := s.acquireLock(lockCtx)
	lockCancel()
	if err != nil {
		return nil, fmt.Errorf("acquire Plus quota automation lock: %w", err)
	}
	if !acquired {
		return nil, ErrPlusQuotaAutomationRunning
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

func plusQuotaAutomationConfigSignature(cfg PlusQuotaAutomationConfig) string {
	return fmt.Sprintf("%t:%d:%d:%g", cfg.Enabled, cfg.GroupID, cfg.IntervalSeconds, cfg.UtilizationThreshold)
}

func (s *PlusQuotaAutomationService) runCycleLocked(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	trigger string,
	record plusQuotaAutomationStateRecord,
) (*PlusQuotaAutomationState, error) {
	state, record, err := s.beginRunLocked(ctx, cfg, trigger, record)
	if err != nil {
		return nil, err
	}
	return s.executeRunLocked(ctx, cfg, state, record)
}

func (s *PlusQuotaAutomationService) beginRunLocked(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	trigger string,
	record plusQuotaAutomationStateRecord,
) (*PlusQuotaAutomationState, plusQuotaAutomationStateRecord, error) {
	startedAt := s.currentTime().UTC()
	state := &PlusQuotaAutomationState{
		Running:   true,
		Trigger:   trigger,
		StartedAt: &startedAt,
	}
	record.PlusQuotaAutomationState = *state
	record.CadenceSignature = plusQuotaAutomationConfigSignature(cfg)
	if err := s.saveStateRecord(context.WithoutCancel(ctx), record); err != nil {
		return nil, record, fmt.Errorf("save Plus quota automation start state: %w", err)
	}
	s.setRunningState(state)
	return state, record, nil
}

func (s *PlusQuotaAutomationService) executeRunLocked(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	state *PlusQuotaAutomationState,
	record plusQuotaAutomationStateRecord,
) (*PlusQuotaAutomationState, error) {
	finish := func(runErr error) (*PlusQuotaAutomationState, error) {
		completedAt := s.currentTime().UTC()
		state.Running = false
		state.CompletedAt = &completedAt
		if runErr != nil {
			state.LastError = publicPlusQuotaRunError(runErr)
		}
		record.PlusQuotaAutomationState = *state
		record.CadenceSignature = plusQuotaAutomationConfigSignature(cfg)
		if cfg.Enabled {
			nextRunAt := completedAt.Add(time.Duration(cfg.IntervalSeconds) * time.Second)
			record.NextRunAt = &nextRunAt
		} else {
			record.NextRunAt = nil
		}
		if saveErr := s.saveStateRecord(context.WithoutCancel(ctx), record); saveErr != nil {
			slog.Warn("plus_quota_automation_state_save_failed", "phase", "complete", "error", saveErr)
			if runErr == nil {
				runErr = saveErr
			}
		}
		s.setRunningState(nil)
		return state, runErr
	}

	accounts, err := s.accountRepo.ListByGroup(ctx, cfg.GroupID)
	if err != nil {
		return finish(fmt.Errorf("list Plus quota group accounts: %w", err))
	}
	s.scanAccounts(ctx, cfg, accounts, state)
	if err := ctx.Err(); err != nil {
		return finish(err)
	}
	return finish(nil)
}

type plusQuotaAccountOutcome struct {
	scanned      int
	eligible     int
	atLimit      int
	resetCount   int
	unauthorized int
	failed       int
	noCredits    int
	cooldown     int
	skipped      int
}

func (s *PlusQuotaAutomationService) scanAccounts(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	accounts []Account,
	state *PlusQuotaAutomationState,
) {
	if len(accounts) == 0 {
		return
	}
	workerCount := plusQuotaAutomationConcurrency
	if len(accounts) < workerCount {
		workerCount = len(accounts)
	}
	jobs := make(chan Account)
	results := make(chan plusQuotaAccountOutcome, len(accounts))
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case account, ok := <-jobs:
					if !ok {
						return
					}
					results <- s.processAccount(ctx, cfg, &account)
				}
			}
		}()
	}

sendLoop:
	for _, account := range accounts {
		select {
		case <-ctx.Done():
			break sendLoop
		case jobs <- account:
		}
	}
	close(jobs)
	workers.Wait()
	close(results)
	for result := range results {
		state.Scanned += result.scanned
		state.Eligible += result.eligible
		state.AtLimit += result.atLimit
		state.ResetCount += result.resetCount
		state.Unauthorized += result.unauthorized
		state.Failed += result.failed
		state.NoCredits += result.noCredits
		state.Cooldown += result.cooldown
		state.Skipped += result.skipped
	}
}

func (s *PlusQuotaAutomationService) processAccount(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	account *Account,
) plusQuotaAccountOutcome {
	outcome := plusQuotaAccountOutcome{scanned: 1}
	if account == nil || !account.IsOpenAIOAuth() || account.IsShadow() {
		outcome.skipped = 1
		return outcome
	}

	usage, err := s.quota.QueryUsageStrict(ctx, account.ID)
	if err != nil {
		if infraerrors.Code(err) == http.StatusUnauthorized {
			outcome.unauthorized = 1
			stage := plusQuotaUnauthorizedQueryStage(err)
			if recordErr := s.recordUnauthorized(ctx, account, cfg.GroupID, stage); recordErr != nil {
				outcome.failed++
				slog.Warn("plus_quota_anomaly_record_failed", "account_id", account.ID, "stage", stage, "error", recordErr)
			}
			return outcome
		}
		outcome.failed = 1
		slog.Warn("plus_quota_query_failed", "account_id", account.ID, "error", err)
		return outcome
	}
	attempt, err := plusQuotaResetAttemptFromAccount(account)
	if err != nil {
		outcome.failed = 1
		slog.Warn("plus_quota_reset_attempt_parse_failed", "account_id", account.ID, "error", err)
		return outcome
	}

	planType := strings.TrimSpace(usage.PlanType)
	if planType == "" {
		planType = strings.TrimSpace(account.GetCredential("plan_type"))
	}
	if !strings.EqualFold(planType, "plus") {
		outcome.skipped = 1
		if err := s.markAuthorized(ctx, account, nil, reconciledPlusQuotaResetAttempt(attempt, s.currentTime().UTC())); err != nil {
			outcome.failed++
		}
		return outcome
	}
	outcome.eligible = 1

	if !plusQuotaRateLimitReached(usage.RateLimit, cfg.UtilizationThreshold) {
		if err := s.markAuthorized(ctx, account, nil, reconciledPlusQuotaResetAttempt(attempt, s.currentTime().UTC())); err != nil {
			outcome.failed++
		}
		return outcome
	}
	outcome.atLimit = 1

	if usage.RateLimitResetCredits == nil || usage.RateLimitResetCredits.AvailableCount <= 0 {
		outcome.noCredits = 1
		if err := s.markAuthorized(ctx, account, nil, nil); err != nil {
			outcome.failed++
		}
		return outcome
	}
	if lastReset, ok := plusQuotaLastResetAt(account.Extra); ok && s.currentTime().Before(lastReset.Add(plusQuotaResetCooldown)) {
		outcome.cooldown = 1
		if err := s.markAuthorized(ctx, account, nil, nil); err != nil {
			outcome.failed++
		}
		return outcome
	}

	now := s.currentTime().UTC()
	if !isActivePlusQuotaResetAttempt(attempt) {
		requestID, generateErr := generateRedeemRequestID()
		if generateErr != nil {
			outcome.failed = 1
			slog.Warn("plus_quota_reset_request_id_failed", "account_id", account.ID, "error", generateErr)
			return outcome
		}
		attempt = &plusQuotaResetAttempt{
			RequestID: requestID,
			CreatedAt: now,
		}
	}
	attempt.Status = plusQuotaResetAttemptStatusAttempting
	attempt.LastAttemptAt = now
	attempt.AttemptCount++
	attempt.LastErrorCode = ""
	attempt.CompletedAt = nil
	if err := s.persistPlusQuotaResetAttempt(ctx, account, attempt); err != nil {
		outcome.failed = 1
		slog.Warn("plus_quota_reset_attempt_save_failed", "account_id", account.ID, "phase", "before_request", "error", err)
		return outcome
	}

	if _, err := s.quota.ResetCreditWithRequestID(ctx, account.ID, attempt.RequestID); err != nil {
		attempt.Status = plusQuotaResetAttemptStatusAmbiguous
		attempt.LastErrorCode = strings.TrimSpace(infraerrors.Reason(err))
		persistCtx, persistCancel := plusQuotaPersistenceContext(ctx)
		persistErr := s.persistPlusQuotaResetAttempt(persistCtx, account, attempt)
		persistCancel()
		if persistErr != nil {
			outcome.failed++
			slog.Warn("plus_quota_reset_attempt_save_failed", "account_id", account.ID, "phase", "after_error", "error", persistErr)
		}
		if infraerrors.Code(err) == http.StatusUnauthorized {
			outcome.unauthorized = 1
			persistCtx, persistCancel := plusQuotaPersistenceContext(ctx)
			recordErr := s.recordUnauthorized(persistCtx, account, cfg.GroupID, "reset")
			persistCancel()
			if recordErr != nil {
				outcome.failed++
				slog.Warn("plus_quota_anomaly_record_failed", "account_id", account.ID, "stage", "reset", "error", recordErr)
			}
			return outcome
		}
		outcome.failed = 1
		slog.Warn("plus_quota_reset_failed", "account_id", account.ID, "error", err)
		return outcome
	}

	outcome.resetCount = 1
	resetAt := s.currentTime().UTC()
	attempt.Status = plusQuotaResetAttemptStatusSucceeded
	attempt.LastErrorCode = ""
	attempt.CompletedAt = &resetAt
	persistCtx, persistCancel := plusQuotaPersistenceContext(ctx)
	err = s.markAuthorized(persistCtx, account, &resetAt, attempt)
	persistCancel()
	if err != nil {
		outcome.failed++
		slog.Warn("plus_quota_reset_state_save_failed", "account_id", account.ID, "error", err)
	}
	return outcome
}

func plusQuotaUnauthorizedQueryStage(err error) string {
	if infraerrors.Reason(err) == openAIQuotaResetCreditsUpstreamErrorReason {
		return "credits"
	}
	return "query"
}

func plusQuotaPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func isActivePlusQuotaResetAttempt(attempt *plusQuotaResetAttempt) bool {
	if attempt == nil || strings.TrimSpace(attempt.RequestID) == "" {
		return false
	}
	return attempt.Status == plusQuotaResetAttemptStatusAttempting ||
		attempt.Status == plusQuotaResetAttemptStatusAmbiguous
}

func reconciledPlusQuotaResetAttempt(attempt *plusQuotaResetAttempt, now time.Time) *plusQuotaResetAttempt {
	if !isActivePlusQuotaResetAttempt(attempt) {
		return nil
	}
	reconciled := *attempt
	reconciled.Status = plusQuotaResetAttemptStatusReconciled
	reconciled.CompletedAt = &now
	return &reconciled
}

func (s *PlusQuotaAutomationService) persistPlusQuotaResetAttempt(
	ctx context.Context,
	account *Account,
	attempt *plusQuotaResetAttempt,
) error {
	if account == nil || attempt == nil {
		return nil
	}
	stored := *attempt
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		PlusQuotaResetAttemptExtraKey: stored,
	}); err != nil {
		return err
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[PlusQuotaResetAttemptExtraKey] = stored
	return nil
}

func plusQuotaRateLimitReached(rateLimit *OpenAIRateLimit, threshold float64) bool {
	if rateLimit == nil {
		return false
	}
	if rateLimit.LimitReached || (rateLimit.allowedPresent && !rateLimit.Allowed) {
		return true
	}
	return plusQuotaWindowReached(rateLimit.PrimaryWindow, threshold) ||
		plusQuotaWindowReached(rateLimit.SecondaryWindow, threshold)
}

func plusQuotaWindowReached(window *OpenAIRateLimitWindow, threshold float64) bool {
	return window != nil && window.UsedPercent >= threshold
}

func (s *PlusQuotaAutomationService) recordUnauthorized(
	ctx context.Context,
	account *Account,
	groupID int64,
	stage string,
) error {
	now := s.currentTime().UTC()
	anomaly, _ := plusQuotaAnomalyFromAccount(account)
	if anomaly == nil {
		anomaly = &PlusQuotaAnomaly{
			AccountID:       account.ID,
			FirstDetectedAt: now,
		}
	}
	anomaly.AccountID = account.ID
	anomaly.AccountName = account.Name
	if email := plusQuotaAccountEmail(account); email != "" {
		anomaly.Email = email
	}
	anomaly.GroupID = groupID
	anomaly.Stage = stage
	anomaly.HTTPStatus = http.StatusUnauthorized
	anomaly.LastDetectedAt = now
	anomaly.Count++
	anomaly.Status = PlusQuotaAnomalyStatusOpen
	anomaly.LastError = "upstream returned 401"
	anomaly.ResolvedAt = nil
	return s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		PlusQuotaAnomalyExtraKey: anomaly,
	})
}

func (s *PlusQuotaAutomationService) markAuthorized(
	ctx context.Context,
	account *Account,
	resetAt *time.Time,
	attempt *plusQuotaResetAttempt,
) error {
	updates := make(map[string]any)
	if resetAt != nil {
		updates[PlusQuotaLastResetExtraKey] = resetAt.UTC().Format(time.RFC3339Nano)
	}
	if attempt != nil {
		updates[PlusQuotaResetAttemptExtraKey] = *attempt
	}
	anomaly, err := plusQuotaAnomalyFromAccount(account)
	if err != nil {
		return err
	}
	if anomaly != nil && anomaly.Status == PlusQuotaAnomalyStatusOpen {
		now := s.currentTime().UTC()
		anomaly.Status = PlusQuotaAnomalyStatusResolved
		anomaly.ResolvedAt = &now
		updates[PlusQuotaAnomalyExtraKey] = anomaly
	}
	if len(updates) == 0 {
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, account.ID, updates)
}

func plusQuotaResetAttemptFromAccount(account *Account) (*plusQuotaResetAttempt, error) {
	if account == nil || account.Extra == nil {
		return nil, nil
	}
	raw, ok := account.Extra[PlusQuotaResetAttemptExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var attempt plusQuotaResetAttempt
	if err := json.Unmarshal(payload, &attempt); err != nil {
		return nil, err
	}
	if strings.TrimSpace(attempt.RequestID) == "" {
		return nil, errors.New("plus quota reset attempt is missing request_id")
	}
	switch attempt.Status {
	case plusQuotaResetAttemptStatusAttempting,
		plusQuotaResetAttemptStatusAmbiguous,
		plusQuotaResetAttemptStatusSucceeded,
		plusQuotaResetAttemptStatusReconciled:
	default:
		return nil, fmt.Errorf("plus quota reset attempt has invalid status %q", attempt.Status)
	}
	return &attempt, nil
}

func plusQuotaAnomalyFromAccount(account *Account) (*PlusQuotaAnomaly, error) {
	if account == nil || account.Extra == nil {
		return nil, nil
	}
	raw, ok := account.Extra[PlusQuotaAnomalyExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var anomaly PlusQuotaAnomaly
	if err := json.Unmarshal(payload, &anomaly); err != nil {
		return nil, err
	}
	if anomaly.AccountID == 0 {
		anomaly.AccountID = account.ID
	}
	if anomaly.Status == "" {
		anomaly.Status = PlusQuotaAnomalyStatusOpen
	}
	return &anomaly, nil
}

func plusQuotaAccountEmail(account *Account) string {
	if account == nil {
		return ""
	}
	return strings.TrimSpace(account.GetCredential("email"))
}

func plusQuotaLastResetAt(extra map[string]any) (time.Time, bool) {
	if extra == nil {
		return time.Time{}, false
	}
	raw, ok := extra[PlusQuotaLastResetExtraKey]
	if !ok || raw == nil {
		return time.Time{}, false
	}
	switch value := raw.(type) {
	case time.Time:
		return value, !value.IsZero()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}

func (s *PlusQuotaAutomationService) ListAnomalies(
	ctx context.Context,
	status string,
	search string,
	page int,
	pageSize int,
) (*PlusQuotaAnomalyList, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > plusQuotaMaxPageSize {
		pageSize = plusQuotaMaxPageSize
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = PlusQuotaAnomalyStatusOpen
	}
	if status != PlusQuotaAnomalyStatusOpen && status != PlusQuotaAnomalyStatusResolved && status != "all" {
		return nil, infraerrors.New(http.StatusBadRequest, "PLUS_QUOTA_INVALID_ANOMALY_STATUS", "status must be open, resolved, or all")
	}

	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, "", "", 0, "")
	if err != nil {
		return nil, err
	}
	search = strings.ToLower(strings.TrimSpace(search))
	items := make([]PlusQuotaAnomaly, 0)
	for i := range accounts {
		account := &accounts[i]
		anomaly, parseErr := plusQuotaAnomalyFromAccount(account)
		if parseErr != nil {
			slog.Warn("plus_quota_anomaly_parse_failed", "account_id", account.ID, "error", parseErr)
			continue
		}
		if anomaly == nil || (status != "all" && anomaly.Status != status) {
			continue
		}
		anomaly.AccountID = account.ID
		anomaly.AccountName = account.Name
		if email := plusQuotaAccountEmail(account); email != "" {
			anomaly.Email = email
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(anomaly.AccountName), search) &&
			!strings.Contains(strings.ToLower(anomaly.Email), search) &&
			!strings.Contains(strconv.FormatInt(account.ID, 10), search) {
			continue
		}
		items = append(items, *anomaly)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastDetectedAt.After(items[j].LastDetectedAt)
	})
	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return &PlusQuotaAnomalyList{
		Items:    items[start:end],
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *PlusQuotaAutomationService) ResolveAnomaly(ctx context.Context, accountID int64) (*PlusQuotaAnomaly, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, ErrPlusQuotaAnomalyNotFound
		}
		return nil, err
	}
	if account == nil {
		return nil, ErrPlusQuotaAnomalyNotFound
	}
	anomaly, err := plusQuotaAnomalyFromAccount(account)
	if err != nil {
		return nil, err
	}
	if anomaly == nil {
		return nil, ErrPlusQuotaAnomalyNotFound
	}
	now := s.currentTime().UTC()
	anomaly.Status = PlusQuotaAnomalyStatusResolved
	anomaly.ResolvedAt = &now
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		PlusQuotaAnomalyExtraKey: anomaly,
	}); err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, ErrPlusQuotaAnomalyNotFound
		}
		return nil, err
	}
	anomaly.AccountID = account.ID
	anomaly.AccountName = account.Name
	if email := plusQuotaAccountEmail(account); email != "" {
		anomaly.Email = email
	}
	return anomaly, nil
}

func (s *PlusQuotaAutomationService) runLoop() {
	ticker := time.NewTicker(plusQuotaAutomationPollInterval)
	defer ticker.Stop()

	reconcile := func(now time.Time) {
		cfg, err := s.GetConfig(s.parentCtx)
		if err != nil {
			slog.Warn("plus_quota_automation_settings_load_failed", "error", err)
			return
		}
		if err := s.reconcileScheduledRun(s.parentCtx, cfg, now); err != nil && !errors.Is(err, ErrPlusQuotaAutomationRunning) {
			slog.Warn("plus_quota_automation_scheduled_run_failed", "error", err)
		}
	}

	reconcile(s.currentTime().UTC())
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			reconcile(s.currentTime().UTC())
		case <-s.wakeCh:
			reconcile(s.currentTime().UTC())
		}
	}
}

func (s *PlusQuotaAutomationService) reconcileScheduledRun(
	ctx context.Context,
	cfg PlusQuotaAutomationConfig,
	now time.Time,
) error {
	if !s.tryAcquireRunGate() {
		return ErrPlusQuotaAutomationRunning
	}
	defer s.releaseRunGate()

	releaseLock, err := s.acquireExecutionLock(ctx)
	if err != nil {
		return err
	}
	defer releaseLock()

	record, err := s.loadStateRecord(ctx)
	if err != nil {
		return err
	}
	now = now.UTC()
	signature := plusQuotaAutomationConfigSignature(cfg)
	changed := false
	if record.Running {
		record.Running = false
		record.CompletedAt = &now
		record.LastError = "previous Plus quota scan did not complete"
		changed = true
	}

	if !cfg.Enabled {
		if record.CadenceSignature != signature || record.NextRunAt != nil {
			record.CadenceSignature = signature
			record.NextRunAt = nil
			changed = true
		}
		if changed {
			return s.saveStateRecord(context.WithoutCancel(ctx), record)
		}
		return nil
	}

	if record.CadenceSignature != signature || record.NextRunAt == nil {
		nextRunAt := now.Add(time.Duration(cfg.IntervalSeconds) * time.Second)
		record.CadenceSignature = signature
		record.NextRunAt = &nextRunAt
		return s.saveStateRecord(context.WithoutCancel(ctx), record)
	}
	if now.Before(*record.NextRunAt) {
		if changed {
			return s.saveStateRecord(context.WithoutCancel(ctx), record)
		}
		return nil
	}

	_, err = s.runCycleLocked(ctx, cfg, "scheduled", record)
	return err
}

func (s *PlusQuotaAutomationService) tryAcquireRunGate() bool {
	select {
	case s.runGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *PlusQuotaAutomationService) acquireTrackedRunGate() error {
	if s == nil {
		return infraerrors.New(http.StatusInternalServerError, "PLUS_QUOTA_AUTOMATION_UNAVAILABLE", "Plus quota automation is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return infraerrors.New(http.StatusServiceUnavailable, "PLUS_QUOTA_AUTOMATION_STOPPED", "Plus quota automation is stopped")
	}
	if !s.tryAcquireRunGate() {
		return ErrPlusQuotaAutomationRunning
	}
	s.wg.Add(1)
	return nil
}

func (s *PlusQuotaAutomationService) releaseTrackedRunGate() {
	s.releaseRunGate()
	s.wg.Done()
}

func (s *PlusQuotaAutomationService) stoppedError() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.stopped {
		return nil
	}
	return infraerrors.New(http.StatusServiceUnavailable, "PLUS_QUOTA_AUTOMATION_STOPPED", "Plus quota automation is stopped")
}

func (s *PlusQuotaAutomationService) releaseRunGate() {
	select {
	case <-s.runGate:
	default:
	}
}

func (s *PlusQuotaAutomationService) signalWake() {
	if s == nil {
		return
	}
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *PlusQuotaAutomationService) setRunningState(state *PlusQuotaAutomationState) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if state == nil {
		s.running = nil
		return
	}
	copyState := *state
	s.running = &copyState
}

func (s *PlusQuotaAutomationService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func publicPlusQuotaRunError(err error) string {
	if err == nil {
		return ""
	}
	if message := strings.TrimSpace(infraerrors.Message(err)); message != "" && message != infraerrors.UnknownMessage {
		return message
	}
	return "Plus quota scan failed"
}
