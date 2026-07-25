package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type plusQuotaAutomationRepoStub struct {
	mu                sync.Mutex
	accounts          map[int64]*Account
	groupIDs          map[int64][]int64
	getByIDErr        error
	updateExtraErrors []error
}

func (r *plusQuotaAutomationRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (r *plusQuotaAutomationRepoStub) ListByGroup(_ context.Context, groupID int64) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.groupIDs[groupID]
	result := make([]Account, 0, len(ids))
	for _, id := range ids {
		if account := r.accounts[id]; account != nil {
			result = append(result, *account)
		}
	}
	return result, nil
}

func (r *plusQuotaAutomationRepoStub) ListAllWithFilters(
	_ context.Context,
	_, _, _, _ string,
	_ int64,
	_ string,
) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		result = append(result, *account)
	}
	return result, nil
}

func (r *plusQuotaAutomationRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.updateExtraErrors) > 0 {
		err := r.updateExtraErrors[0]
		r.updateExtraErrors = r.updateExtraErrors[1:]
		if err != nil {
			return err
		}
	}
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	return nil
}

type plusQuotaAutomationClientStub struct {
	mu              sync.Mutex
	usage           map[int64]*OpenAIQuotaUsage
	queryErrors     map[int64]error
	resetErrors     map[int64]error
	queryCalls      map[int64]int
	resetCalls      map[int64]int
	resetRequestIDs map[int64][]string
}

func (c *plusQuotaAutomationClientStub) QueryUsageStrict(_ context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queryCalls == nil {
		c.queryCalls = make(map[int64]int)
	}
	c.queryCalls[accountID]++
	if err := c.queryErrors[accountID]; err != nil {
		return nil, err
	}
	return c.usage[accountID], nil
}

func (c *plusQuotaAutomationClientStub) ResetCreditWithRequestID(
	_ context.Context,
	accountID int64,
	requestID string,
) (*OpenAIQuotaResetResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resetCalls == nil {
		c.resetCalls = make(map[int64]int)
	}
	c.resetCalls[accountID]++
	if c.resetRequestIDs == nil {
		c.resetRequestIDs = make(map[int64][]string)
	}
	c.resetRequestIDs[accountID] = append(c.resetRequestIDs[accountID], requestID)
	if err := c.resetErrors[accountID]; err != nil {
		return nil, err
	}
	return &OpenAIQuotaResetResult{Code: "success", WindowsReset: 1}, nil
}

type plusQuotaAutomationSettingsStub struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *plusQuotaAutomationSettingsStub) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *plusQuotaAutomationSettingsStub) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func newPlusQuotaAutomationTestService(
	t *testing.T,
	account *Account,
	usage *OpenAIQuotaUsage,
) (*PlusQuotaAutomationService, *plusQuotaAutomationRepoStub, *plusQuotaAutomationClientStub) {
	t.Helper()
	repo := &plusQuotaAutomationRepoStub{
		accounts: map[int64]*Account{account.ID: account},
		groupIDs: map[int64][]int64{10: {account.ID}},
	}
	client := &plusQuotaAutomationClientStub{
		usage:           map[int64]*OpenAIQuotaUsage{account.ID: usage},
		queryErrors:     make(map[int64]error),
		resetErrors:     make(map[int64]error),
		queryCalls:      make(map[int64]int),
		resetCalls:      make(map[int64]int),
		resetRequestIDs: make(map[int64][]string),
	}
	settings := &plusQuotaAutomationSettingsStub{values: make(map[string]string)}
	svc := NewPlusQuotaAutomationService(repo, client, settings, nil)
	svc.acquireLock = func(context.Context) (func(), bool, error) {
		return func() {}, true, nil
	}
	_, err := svc.UpdateConfig(context.Background(), PlusQuotaAutomationConfig{
		Enabled:              true,
		GroupID:              10,
		IntervalSeconds:      300,
		UtilizationThreshold: 100,
	})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	return svc, repo, client
}

func plusQuotaEligibleTestAccount(id int64) (*Account, *OpenAIQuotaUsage) {
	return &Account{
			ID:       id,
			Name:     "plus-account",
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"email":     "plus@example.com",
				"plan_type": "plus",
			},
			Extra: map[string]any{},
		}, &OpenAIQuotaUsage{
			PlanType: "plus",
			RateLimit: &OpenAIRateLimit{
				Allowed:       false,
				LimitReached:  true,
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100},
			},
			RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 2},
		}
}

func TestPlusQuotaAutomationRunResetsEligibleAccount(t *testing.T) {
	account := &Account{
		ID:       1,
		Name:     "plus-one",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"email":     "plus@example.com",
			"plan_type": "plus",
		},
		Extra: map[string]any{},
	}
	usage := &OpenAIQuotaUsage{
		PlanType: "plus",
		RateLimit: &OpenAIRateLimit{
			Allowed:       false,
			LimitReached:  true,
			PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 1},
	}
	svc, repo, client := newPlusQuotaAutomationTestService(t, account, usage)
	fixedNow := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixedNow }

	state, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, state.Scanned)
	require.Equal(t, 1, state.Eligible)
	require.Equal(t, 1, state.AtLimit)
	require.Equal(t, 1, state.ResetCount)
	require.Zero(t, state.Unauthorized)
	require.Equal(t, 1, client.resetCalls[account.ID])
	require.Len(t, client.resetRequestIDs[account.ID], 1)
	require.NotEmpty(t, client.resetRequestIDs[account.ID][0])

	repo.mu.Lock()
	lastReset, ok := repo.accounts[account.ID].Extra[PlusQuotaLastResetExtraKey].(string)
	attempt, attemptErr := plusQuotaResetAttemptFromAccount(repo.accounts[account.ID])
	repo.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, fixedNow.Format(time.RFC3339Nano), lastReset)
	require.NoError(t, attemptErr)
	require.Equal(t, plusQuotaResetAttemptStatusSucceeded, attempt.Status)
	require.Equal(t, 1, attempt.AttemptCount)
}

func TestPlusQuotaAutomationDoesNotCallResetWhenAttemptPersistenceFails(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(11)
	svc, repo, client := newPlusQuotaAutomationTestService(t, account, usage)
	repo.updateExtraErrors = []error{errors.New("database unavailable")}

	state, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, state.Failed)
	require.Zero(t, state.ResetCount)
	require.Zero(t, client.resetCalls[account.ID])
}

func TestPlusQuotaAutomationReusesRequestIDAfterAmbiguousReset(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(12)
	svc, _, client := newPlusQuotaAutomationTestService(t, account, usage)
	client.resetErrors[account.ID] = infraerrors.New(http.StatusBadGateway, "OPENAI_QUOTA_RESET_REQUEST_FAILED", "timeout")

	firstState, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, firstState.Failed)
	require.Len(t, client.resetRequestIDs[account.ID], 1)
	firstRequestID := client.resetRequestIDs[account.ID][0]

	delete(client.resetErrors, account.ID)
	secondState, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, secondState.ResetCount)
	require.Equal(t, []string{firstRequestID, firstRequestID}, client.resetRequestIDs[account.ID])

	attempt, err := plusQuotaResetAttemptFromAccount(account)
	require.NoError(t, err)
	require.Equal(t, plusQuotaResetAttemptStatusSucceeded, attempt.Status)
	require.Equal(t, 2, attempt.AttemptCount)
}

func TestPlusQuotaAutomationReusesRequestIDWhenSuccessPersistenceFails(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(13)
	svc, repo, client := newPlusQuotaAutomationTestService(t, account, usage)
	persistErr := errors.New("failed to save success")
	repo.updateExtraErrors = []error{nil, persistErr}

	firstState, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, firstState.ResetCount)
	require.Equal(t, 1, firstState.Failed)
	firstRequestID := client.resetRequestIDs[account.ID][0]

	secondState, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, secondState.ResetCount)
	require.Equal(t, []string{firstRequestID, firstRequestID}, client.resetRequestIDs[account.ID])
}

func TestPlusQuotaAutomationRecordsAndAutomaticallyResolves401(t *testing.T) {
	account := &Account{
		ID:       2,
		Name:     "plus-two",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"email":     "expired@example.com",
			"plan_type": "plus",
		},
		Extra: map[string]any{},
	}
	usage := &OpenAIQuotaUsage{
		PlanType: "plus",
		RateLimit: &OpenAIRateLimit{
			Allowed:       true,
			PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 20},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{},
	}
	svc, _, client := newPlusQuotaAutomationTestService(t, account, usage)
	firstSeen := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return firstSeen }
	client.queryErrors[account.ID] = infraerrors.Unauthorized("OPENAI_QUOTA_UPSTREAM_ERROR", "unauthorized")

	state, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, state.Unauthorized)

	list, err := svc.ListAnomalies(context.Background(), PlusQuotaAnomalyStatusOpen, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, list.Total)
	require.Equal(t, "expired@example.com", list.Items[0].Email)
	require.Equal(t, "query", list.Items[0].Stage)
	require.Equal(t, 1, list.Items[0].Count)

	listByAccountID, err := svc.ListAnomalies(context.Background(), PlusQuotaAnomalyStatusOpen, "2", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, listByAccountID.Total)
	require.Equal(t, account.ID, listByAccountID.Items[0].AccountID)

	delete(client.queryErrors, account.ID)
	secondSeen := firstSeen.Add(time.Hour)
	svc.now = func() time.Time { return secondSeen }
	state, err = svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Zero(t, state.Unauthorized)

	openList, err := svc.ListAnomalies(context.Background(), PlusQuotaAnomalyStatusOpen, "", 1, 20)
	require.NoError(t, err)
	require.Zero(t, openList.Total)
	resolvedList, err := svc.ListAnomalies(context.Background(), PlusQuotaAnomalyStatusResolved, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, resolvedList.Total)
	require.NotNil(t, resolvedList.Items[0].ResolvedAt)
	require.Equal(t, secondSeen, *resolvedList.Items[0].ResolvedAt)
}

func TestPlusQuotaAutomationRecordsResetCreditDetails401AtCreditsStage(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(21)
	svc, _, client := newPlusQuotaAutomationTestService(t, account, usage)
	client.queryErrors[account.ID] = infraerrors.Unauthorized(
		openAIQuotaResetCreditsUpstreamErrorReason,
		"reset-credit details unauthorized",
	)

	state, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, state.Unauthorized)

	list, err := svc.ListAnomalies(context.Background(), PlusQuotaAnomalyStatusOpen, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, list.Total)
	require.Equal(t, "credits", list.Items[0].Stage)
}

func TestPlusQuotaAutomationResolveAnomalyPreservesRepositoryErrors(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(22)
	svc, repo, _ := newPlusQuotaAutomationTestService(t, account, usage)
	storageErr := errors.New("database timeout")
	repo.getByIDErr = storageErr

	_, err := svc.ResolveAnomaly(context.Background(), account.ID)
	require.ErrorIs(t, err, storageErr)
	require.NotErrorIs(t, err, ErrPlusQuotaAnomalyNotFound)

	repo.getByIDErr = ErrAccountNotFound
	_, err = svc.ResolveAnomaly(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrPlusQuotaAnomalyNotFound)
}

func TestPlusQuotaAutomationCooldownPreventsRepeatedCreditConsumption(t *testing.T) {
	lastReset := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	account := &Account{
		ID:       3,
		Name:     "plus-three",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"email":     "cooldown@example.com",
			"plan_type": "plus",
		},
		Extra: map[string]any{
			PlusQuotaLastResetExtraKey: lastReset.Format(time.RFC3339Nano),
		},
	}
	usage := &OpenAIQuotaUsage{
		PlanType: "plus",
		RateLimit: &OpenAIRateLimit{
			LimitReached:  true,
			PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 2},
	}
	svc, _, client := newPlusQuotaAutomationTestService(t, account, usage)
	svc.now = func() time.Time { return lastReset.Add(10 * time.Minute) }

	state, err := svc.RunOnce(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 1, state.Cooldown)
	require.Zero(t, state.ResetCount)
	require.Zero(t, client.resetCalls[account.ID])
}

func TestPlusQuotaAutomationSharedCadencePreventsStaggeredDuplicateRuns(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(31)
	repo := &plusQuotaAutomationRepoStub{
		accounts: map[int64]*Account{account.ID: account},
		groupIDs: map[int64][]int64{10: {account.ID}},
	}
	client := &plusQuotaAutomationClientStub{
		usage:           map[int64]*OpenAIQuotaUsage{account.ID: usage},
		queryErrors:     make(map[int64]error),
		resetErrors:     make(map[int64]error),
		queryCalls:      make(map[int64]int),
		resetCalls:      make(map[int64]int),
		resetRequestIDs: make(map[int64][]string),
	}
	settings := &plusQuotaAutomationSettingsStub{values: make(map[string]string)}

	var lockMu sync.Mutex
	locked := false
	acquireLock := func(context.Context) (func(), bool, error) {
		lockMu.Lock()
		if locked {
			lockMu.Unlock()
			return nil, false, nil
		}
		locked = true
		lockMu.Unlock()
		var once sync.Once
		return func() {
			once.Do(func() {
				lockMu.Lock()
				locked = false
				lockMu.Unlock()
			})
		}, true, nil
	}

	first := NewPlusQuotaAutomationService(repo, client, settings, nil)
	second := NewPlusQuotaAutomationService(repo, client, settings, nil)
	first.acquireLock = acquireLock
	second.acquireLock = acquireLock
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)

	cfg, err := first.UpdateConfig(context.Background(), PlusQuotaAutomationConfig{
		Enabled:              true,
		GroupID:              10,
		IntervalSeconds:      300,
		UtilizationThreshold: 100,
	})
	require.NoError(t, err)
	start := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	firstNow := start
	secondNow := start.Add(100 * time.Second)
	first.now = func() time.Time { return firstNow }
	second.now = func() time.Time { return secondNow }

	require.NoError(t, first.reconcileScheduledRun(context.Background(), cfg, firstNow))
	require.NoError(t, second.reconcileScheduledRun(context.Background(), cfg, secondNow))
	require.Zero(t, client.queryCalls[account.ID])

	firstNow = start.Add(300 * time.Second)
	require.NoError(t, first.reconcileScheduledRun(context.Background(), cfg, firstNow))
	secondNow = start.Add(301 * time.Second)
	require.NoError(t, second.reconcileScheduledRun(context.Background(), cfg, secondNow))

	require.Equal(t, 1, client.queryCalls[account.ID])
	require.Equal(t, 1, client.resetCalls[account.ID])
	overview, err := second.GetOverview(context.Background())
	require.NoError(t, err)
	require.NotNil(t, overview.NextRunAt)
	require.Equal(t, start.Add(600*time.Second), *overview.NextRunAt)
}

func TestPlusQuotaAutomationLockContenderDoesNotOverwriteSharedState(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(32)
	svc, _, _ := newPlusQuotaAutomationTestService(t, account, usage)
	startedAt := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	record := plusQuotaAutomationStateRecord{
		PlusQuotaAutomationState: PlusQuotaAutomationState{
			Running:   true,
			Trigger:   "scheduled",
			StartedAt: &startedAt,
		},
		CadenceSignature: "shared",
	}
	require.NoError(t, svc.saveStateRecord(context.Background(), record))
	svc.acquireLock = func(context.Context) (func(), bool, error) {
		return nil, false, nil
	}

	before, err := svc.settings.GetValue(context.Background(), PlusQuotaAutomationStateSettingKey)
	require.NoError(t, err)
	err = svc.TriggerRun()
	require.ErrorIs(t, err, ErrPlusQuotaAutomationRunning)
	after, err := svc.settings.GetValue(context.Background(), PlusQuotaAutomationStateSettingKey)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPlusQuotaAutomationStopWaitsForTriggerCleanup(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(33)
	svc, _, _ := newPlusQuotaAutomationTestService(t, account, usage)

	lockEntered := make(chan struct{})
	allowAcquire := make(chan struct{})
	releaseEntered := make(chan struct{})
	allowRelease := make(chan struct{})
	var allowAcquireOnce sync.Once
	var allowReleaseOnce sync.Once
	t.Cleanup(func() {
		allowAcquireOnce.Do(func() { close(allowAcquire) })
		allowReleaseOnce.Do(func() { close(allowRelease) })
	})
	svc.acquireLock = func(context.Context) (func(), bool, error) {
		close(lockEntered)
		<-allowAcquire
		return func() {
			close(releaseEntered)
			<-allowRelease
		}, true, nil
	}

	triggerDone := make(chan error, 1)
	go func() {
		triggerDone <- svc.TriggerRun()
	}()
	select {
	case <-lockEntered:
	case <-time.After(time.Second):
		t.Fatal("TriggerRun did not reach the distributed lock")
	}

	firstStopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(firstStopDone)
	}()
	select {
	case <-svc.parentCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the service context")
	}
	secondStopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(secondStopDone)
	}()

	allowAcquireOnce.Do(func() { close(allowAcquire) })
	select {
	case <-releaseEntered:
	case <-time.After(time.Second):
		t.Fatal("TriggerRun did not release the distributed lock")
	}
	select {
	case <-firstStopDone:
		t.Fatal("first Stop returned before TriggerRun released its resources")
	case <-secondStopDone:
		t.Fatal("second Stop returned before TriggerRun released its resources")
	case <-time.After(50 * time.Millisecond):
	}

	allowReleaseOnce.Do(func() { close(allowRelease) })
	select {
	case err := <-triggerDone:
		require.Equal(t, "PLUS_QUOTA_AUTOMATION_STOPPED", infraerrors.Reason(err))
	case <-time.After(time.Second):
		t.Fatal("TriggerRun did not finish after releasing the distributed lock")
	}
	for name, done := range map[string]<-chan struct{}{
		"first Stop":  firstStopDone,
		"second Stop": secondStopDone,
	} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish after TriggerRun cleanup", name)
		}
	}

	err := svc.TriggerRun()
	require.Equal(t, "PLUS_QUOTA_AUTOMATION_STOPPED", infraerrors.Reason(err))
}

func TestPlusQuotaAutomationRunOnceRejectsAfterStop(t *testing.T) {
	account, usage := plusQuotaEligibleTestAccount(34)
	svc, _, client := newPlusQuotaAutomationTestService(t, account, usage)
	svc.Stop()

	lockCalls := 0
	svc.acquireLock = func(context.Context) (func(), bool, error) {
		lockCalls++
		return func() {}, true, nil
	}

	state, err := svc.RunOnce(context.Background(), "test")
	require.Nil(t, state)
	require.Equal(t, "PLUS_QUOTA_AUTOMATION_STOPPED", infraerrors.Reason(err))
	require.Zero(t, lockCalls)
	require.Zero(t, client.queryCalls[account.ID])
	require.Zero(t, client.resetCalls[account.ID])
}
