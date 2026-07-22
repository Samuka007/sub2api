//go:build unit

package service

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type quotaRecoveryRepoStub struct {
	AccountRepository

	mu         sync.Mutex
	candidates []Account
	upperErr   error
	listErr    error
	listFn     func([]Account)
	listAfter  []int64
	getByIDFn  func(int64) (*Account, error)
	clearFn    func(QuotaRecoveryObservation) (bool, error)
	clearCtxFn func(context.Context, QuotaRecoveryObservation) (bool, error)
	clearCalls []QuotaRecoveryObservation
}

func (r *quotaRecoveryRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.getByIDFn != nil {
		return r.getByIDFn(id)
	}
	return nil, ErrAccountNotFound
}

func (r *quotaRecoveryRepoStub) QuotaRecoveryCandidateUpperBound(_ context.Context, _ time.Time) (int64, error) {
	if r.upperErr != nil {
		return 0, r.upperErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var upperBound int64
	for i := range r.candidates {
		if r.candidates[i].ID > upperBound {
			upperBound = r.candidates[i].ID
		}
	}
	return upperBound, nil
}

func (r *quotaRecoveryRepoStub) ListQuotaRecoveryCandidates(_ context.Context, _ time.Time, afterID, throughID int64, limit int) ([]Account, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.mu.Lock()
	r.listAfter = append(r.listAfter, afterID)
	accounts := make([]Account, 0, limit)
	for i := range r.candidates {
		if r.candidates[i].ID <= afterID || r.candidates[i].ID > throughID {
			continue
		}
		accounts = append(accounts, r.candidates[i])
		if len(accounts) == limit {
			break
		}
	}
	fn := r.listFn
	r.mu.Unlock()
	if fn != nil {
		fn(accounts)
	}
	return accounts, nil
}

func (r *quotaRecoveryRepoStub) listAfterSnapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.listAfter...)
}

func (r *quotaRecoveryRepoStub) ClearRateLimitIfUnchanged(
	ctx context.Context,
	observation QuotaRecoveryObservation,
) (bool, error) {
	r.mu.Lock()
	r.clearCalls = append(r.clearCalls, observation)
	ctxFn := r.clearCtxFn
	fn := r.clearFn
	r.mu.Unlock()
	if ctxFn != nil {
		return ctxFn(ctx, observation)
	}
	if fn != nil {
		return fn(observation)
	}
	return true, nil
}

func (r *quotaRecoveryRepoStub) clearCallSnapshot() []QuotaRecoveryObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]QuotaRecoveryObservation(nil), r.clearCalls...)
}

type quotaRecoveryCheckerStub struct {
	mu      sync.Mutex
	results map[int64]QuotaRecoveryCheckResult
	calls   []int64
	checkFn func(context.Context, *Account) QuotaRecoveryCheckResult
}

func (c *quotaRecoveryCheckerStub) Check(ctx context.Context, account *Account) QuotaRecoveryCheckResult {
	c.mu.Lock()
	c.calls = append(c.calls, account.ID)
	fn := c.checkFn
	result, ok := c.results[account.ID]
	c.mu.Unlock()
	if fn != nil {
		return fn(ctx, account)
	}
	if ok {
		return result
	}
	return QuotaRecoveryCheckResult{Verdict: QuotaRecoveryUnknown, CheckedAt: time.Now().UTC()}
}

func (c *quotaRecoveryCheckerStub) callSnapshot() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.calls...)
}

type quotaRecoveryRuntimeBlockerStub struct {
	mu          sync.Mutex
	cleared     []int64
	generations map[int64]uint64
	blocked     map[int64]bool
}

func (b *quotaRecoveryRuntimeBlockerStub) BlockAccountScheduling(account *Account, _ time.Time, _ string) {
	if account == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generations == nil {
		b.generations = make(map[int64]uint64)
	}
	if b.blocked == nil {
		b.blocked = make(map[int64]bool)
	}
	b.generations[account.ID]++
	b.blocked[account.ID] = true
}

func (b *quotaRecoveryRuntimeBlockerStub) ClearAccountSchedulingBlock(accountID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleared = append(b.cleared, accountID)
	delete(b.blocked, accountID)
	b.generations[accountID]++
}

func (b *quotaRecoveryRuntimeBlockerStub) AccountSchedulingBlockGeneration(accountID int64) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.generations[accountID]
}

func (b *quotaRecoveryRuntimeBlockerStub) GuardAccountSchedulingBlockForQuotaRecovery(
	accountID int64,
	observedGeneration uint64,
	_ time.Time,
	_ time.Time,
) (func(), bool) {
	b.mu.Lock()
	if b.generations[accountID] != observedGeneration {
		b.mu.Unlock()
		return nil, false
	}
	return b.mu.Unlock, true
}

func (b *quotaRecoveryRuntimeBlockerStub) ClearAccountSchedulingBlockForQuotaRecovery(
	accountID int64,
	observedGeneration uint64,
	_ time.Time,
	_ time.Time,
) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generations[accountID] != observedGeneration || !b.blocked[accountID] {
		return false
	}
	b.cleared = append(b.cleared, accountID)
	delete(b.blocked, accountID)
	b.generations[accountID]++
	return true
}

func (b *quotaRecoveryRuntimeBlockerStub) snapshot() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.cleared...)
}

func (b *quotaRecoveryRuntimeBlockerStub) isBlocked(accountID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blocked[accountID]
}

func quotaRecoveryTestConfig() *config.Config {
	return &config.Config{RunMode: config.RunModeSimple, QuotaRecovery: config.QuotaRecoveryConfig{
		Enabled:         true,
		IntervalSeconds: 600,
		BatchSize:       50,
		Concurrency:     3,
		TimeoutSeconds:  5,
		JitterSeconds:   0,
	}}
}

func quotaRecoveryTestAccount(id int64, now time.Time) Account {
	limitedAt := now.Add(-time.Minute)
	resetAt := now.Add(time.Hour)
	overloadUntil := now.Add(2 * time.Hour)
	tempUntil := now.Add(30 * time.Minute)
	return Account{
		ID:                      id,
		Platform:                PlatformOpenAI,
		Type:                    AccountTypeOAuth,
		Status:                  StatusActive,
		Schedulable:             true,
		UpdatedAt:               now.Add(-2 * time.Minute),
		RateLimitedAt:           &limitedAt,
		RateLimitResetAt:        &resetAt,
		OverloadUntil:           &overloadUntil,
		TempUnschedulableUntil:  &tempUntil,
		TempUnschedulableReason: "keep-me",
	}
}

func freshQuotaRecoveryResult(verdict QuotaRecoveryState) QuotaRecoveryCheckResult {
	return QuotaRecoveryCheckResult{
		Verdict:       verdict,
		Authoritative: verdict != QuotaRecoveryUnknown,
		CheckedAt:     time.Now().UTC(),
		Source:        QuotaRecoverySourceOpenAI,
	}
}

func TestQuotaRecoveryRunOnceRecoversOnlyAuthoritativeAvailable(t *testing.T) {
	now := time.Now().UTC()
	available := quotaRecoveryTestAccount(1, now)
	exhausted := quotaRecoveryTestAccount(2, now)
	unknown := quotaRecoveryTestAccount(3, now)
	originalAvailable := available

	repo := &quotaRecoveryRepoStub{candidates: []Account{available, exhausted, unknown}}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		1: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
		2: freshQuotaRecoveryResult(QuotaRecoveryExhausted),
		3: {Verdict: QuotaRecoveryUnknown, CheckedAt: time.Now().UTC(), Source: QuotaRecoverySourceUnsupported},
	}}
	blocker := &quotaRecoveryRuntimeBlockerStub{}
	blocker.BlockAccountScheduling(&available, available.RateLimitResetAt.UTC(), "old-429")
	blocker.BlockAccountScheduling(&exhausted, exhausted.RateLimitResetAt.UTC(), "old-429")
	blocker.BlockAccountScheduling(&unknown, unknown.RateLimitResetAt.UTC(), "old-429")
	svc := newQuotaRecoveryService(repo, checker, blocker, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, QuotaRecoveryRunResult{
		Listed:    3,
		Checked:   3,
		Recovered: 1,
		Exhausted: 1,
		Unknown:   1,
	}, result)

	calls := repo.clearCallSnapshot()
	require.Len(t, calls, 1)
	require.Equal(t, int64(1), calls[0].AccountID)
	require.Equal(t, available.RateLimitedAt.UTC(), calls[0].RateLimitedAt)
	require.Equal(t, available.RateLimitResetAt.UTC(), calls[0].RateLimitResetAt)
	require.Equal(t, available.UpdatedAt.UTC(), calls[0].AccountUpdatedAt)
	require.Equal(t, available.ID, calls[0].CredentialOwnerID)
	require.Equal(t, available.UpdatedAt.UTC(), calls[0].CredentialOwnerUpdatedAt)
	require.Equal(t, []int64{1}, blocker.snapshot())
	require.False(t, blocker.isBlocked(available.ID))
	require.True(t, blocker.isBlocked(exhausted.ID), "exhausted quota must retain its active 429 block")
	require.True(t, blocker.isBlocked(unknown.ID), "unknown quota must retain its active 429 block")

	// The runner must not mutate any persistent or transient account fields in
	// memory; the repository CAS owns the two permitted column updates.
	require.Equal(t, originalAvailable, available)
}

func TestQuotaRecoveryRunOnceFailsClosedForWeakOrStaleEvidence(t *testing.T) {
	now := time.Now().UTC()
	repo := &quotaRecoveryRepoStub{candidates: []Account{
		quotaRecoveryTestAccount(1, now),
		quotaRecoveryTestAccount(2, now),
	}}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		1: {
			Verdict:       QuotaRecoveryAvailable,
			Authoritative: false,
			CheckedAt:     time.Now().UTC(),
		},
		2: {
			Verdict:       QuotaRecoveryAvailable,
			Authoritative: true,
			CheckedAt:     time.Now().Add(-time.Hour).UTC(),
		},
	}}
	svc := newQuotaRecoveryService(repo, checker, nil, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Checked)
	require.Equal(t, 2, result.Unknown)
	require.Zero(t, result.Recovered)
	require.Empty(t, repo.clearCallSnapshot())
}

func TestQuotaRecoveryRunOnceCASMissPreservesConcurrentRateLimit(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(7, now)
	concurrentLimitedAt := now.Add(time.Second)
	concurrentResetAt := now.Add(3 * time.Hour)
	repo := &quotaRecoveryRepoStub{
		candidates: []Account{account},
		clearFn: func(observation QuotaRecoveryObservation) (bool, error) {
			require.NotEqual(t, concurrentLimitedAt, observation.RateLimitedAt)
			require.NotEqual(t, concurrentResetAt, observation.RateLimitResetAt)
			require.Equal(t, account.UpdatedAt.UTC(), observation.AccountUpdatedAt)
			return false, nil
		},
	}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		7: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
	}}
	blocker := &quotaRecoveryRuntimeBlockerStub{}
	svc := newQuotaRecoveryService(repo, checker, blocker, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.CASMisses)
	require.Zero(t, result.Recovered)
	require.Empty(t, blocker.snapshot())
}

func TestQuotaRecoveryRunOnceCASTimeoutReleasesRuntimeGuard(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(72, now)
	clearStarted := make(chan struct{})
	repo := &quotaRecoveryRepoStub{
		candidates: []Account{account},
		clearCtxFn: func(ctx context.Context, _ QuotaRecoveryObservation) (bool, error) {
			close(clearStarted)
			<-ctx.Done()
			return false, ctx.Err()
		},
	}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		account.ID: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
	}}
	blocker := &quotaRecoveryRuntimeBlockerStub{}
	blocker.BlockAccountScheduling(&account, account.RateLimitResetAt.UTC(), "old-429")
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.TimeoutSeconds = 1
	svc := newQuotaRecoveryService(repo, checker, blocker, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	type runResult struct {
		result QuotaRecoveryRunResult
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, err := svc.RunOnce(context.Background())
		runDone <- runResult{result: result, err: err}
	}()
	<-clearStarted

	blockDone := make(chan struct{})
	go func() {
		blocker.BlockAccountScheduling(&account, now.Add(2*time.Hour), "new-429")
		close(blockDone)
	}()
	select {
	case <-blockDone:
		require.Fail(t, "BlockAccountScheduling completed while the CAS guard was held")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case run := <-runDone:
		require.NoError(t, run.err)
		require.Equal(t, 1, run.result.Checked)
		require.Equal(t, 1, run.result.Errors)
		require.Zero(t, run.result.Recovered)
	case <-time.After(2 * time.Second):
		require.Fail(t, "RunOnce did not return after the CAS deadline")
	}
	select {
	case <-blockDone:
	case <-time.After(time.Second):
		require.Fail(t, "BlockAccountScheduling remained blocked after the CAS deadline")
	}
	require.True(t, blocker.isBlocked(account.ID))
}

func TestQuotaRecoveryRunOnceAcceptsCASCommittedAtDeadline(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(73, now)
	repo := &quotaRecoveryRepoStub{
		candidates: []Account{account},
		clearCtxFn: func(ctx context.Context, _ QuotaRecoveryObservation) (bool, error) {
			<-ctx.Done()
			return true, nil
		},
	}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		account.ID: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
	}}
	blocker := &quotaRecoveryRuntimeBlockerStub{}
	blocker.BlockAccountScheduling(&account, account.RateLimitResetAt.UTC(), "old-429")
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.TimeoutSeconds = 1
	svc := newQuotaRecoveryService(repo, checker, blocker, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.Recovered)
	require.Zero(t, result.Errors)
	require.False(t, blocker.isBlocked(account.ID), "a committed CAS must clear the matching runtime block")
}

func TestQuotaRecoveryRunOnceBindsSparkShadowToCredentialOwnerVersion(t *testing.T) {
	now := time.Now().UTC()
	parentUpdatedAt := now.Add(-time.Hour)
	parent := Account{
		ID:        70,
		Platform:  PlatformOpenAI,
		Type:      AccountTypeOAuth,
		UpdatedAt: parentUpdatedAt,
	}
	shadow := quotaRecoveryTestAccount(71, now)
	shadow.UpdatedAt = now.Add(-30 * time.Minute)
	shadow.ParentAccountID = &parent.ID
	shadow.QuotaDimension = QuotaDimensionSpark

	repo := &quotaRecoveryRepoStub{candidates: []Account{shadow}}
	repo.getByIDFn = func(id int64) (*Account, error) {
		require.Equal(t, parent.ID, id)
		copyParent := parent
		return &copyParent, nil
	}
	repo.clearFn = func(observation QuotaRecoveryObservation) (bool, error) {
		require.Equal(t, shadow.ID, observation.AccountID)
		require.Equal(t, shadow.UpdatedAt.UTC(), observation.AccountUpdatedAt)
		require.Equal(t, parent.ID, observation.CredentialOwnerID)
		require.Equal(t, parentUpdatedAt.UTC(), observation.CredentialOwnerUpdatedAt)
		return observation.CredentialOwnerUpdatedAt.Equal(parent.UpdatedAt), nil
	}
	checker := &quotaRecoveryCheckerStub{checkFn: func(_ context.Context, _ *Account) QuotaRecoveryCheckResult {
		parent.UpdatedAt = now.Add(time.Minute)
		return freshQuotaRecoveryResult(QuotaRecoveryAvailable)
	}}
	svc := newQuotaRecoveryService(repo, checker, nil, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.CASMisses)
	require.Zero(t, result.Recovered)
}

func TestQuotaRecoveryRunOnceDoesNotClearNewerRuntimeBlock(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(8, now)
	repo := &quotaRecoveryRepoStub{candidates: []Account{account}}
	blocker := &quotaRecoveryRuntimeBlockerStub{}
	blocker.BlockAccountScheduling(&account, account.RateLimitResetAt.UTC(), "old-429")
	checker := &quotaRecoveryCheckerStub{checkFn: func(_ context.Context, checked *Account) QuotaRecoveryCheckResult {
		blocker.BlockAccountScheduling(checked, now.Add(2*time.Hour), "new-429")
		return freshQuotaRecoveryResult(QuotaRecoveryAvailable)
	}}
	svc := newQuotaRecoveryService(repo, checker, blocker, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, result.Recovered)
	require.Equal(t, 1, result.Unknown)
	require.Empty(t, repo.clearCallSnapshot(), "a changed runtime generation must fence the persistent clear")
	require.Empty(t, blocker.snapshot(), "the newer runtime generation must remain blocked")
	require.True(t, blocker.isBlocked(account.ID))
}

func TestQuotaRecoveryRunOncePreservesQuotaBlockCreatedAfterCandidateRead(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(9, now)
	blocker := &OpenAIGatewayService{}
	repo := &quotaRecoveryRepoStub{candidates: []Account{account}}
	repo.listFn = func(candidates []Account) {
		require.Len(t, candidates, 1)
		blocker.BlockAccountScheduling(
			&candidates[0],
			candidates[0].RateLimitResetAt.UTC(),
			"429",
		)
	}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		account.ID: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
	}}
	svc := newQuotaRecoveryService(repo, checker, blocker, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.Unknown)
	require.Zero(t, result.Recovered)
	require.Empty(t, repo.clearCallSnapshot(), "a post-read 429 must fence the persistent CAS")
	require.True(t, blocker.isOpenAIAccountRuntimeBlocked(&account), "the post-read runtime block must remain active")
}

func TestQuotaRecoveryRunOncePreservesActiveNonQuotaRuntimeBlock(t *testing.T) {
	for i, reason := range []string{"403", "transport_error"} {
		t.Run(reason, func(t *testing.T) {
			now := time.Now().UTC()
			account := quotaRecoveryTestAccount(int64(20+i), now)
			blocker := &OpenAIGatewayService{}
			blocker.BlockAccountScheduling(&account, now.Add(30*time.Minute), reason)
			repo := &quotaRecoveryRepoStub{candidates: []Account{account}}
			checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
				account.ID: freshQuotaRecoveryResult(QuotaRecoveryAvailable),
			}}
			svc := newQuotaRecoveryService(repo, checker, blocker, quotaRecoveryTestConfig())
			svc.jitterFor = func(time.Duration) time.Duration { return 0 }

			result, err := svc.RunOnce(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, result.Checked)
			require.Equal(t, 1, result.Unknown)
			require.Zero(t, result.Recovered)
			require.Empty(t, repo.clearCallSnapshot())
			require.True(t, blocker.isOpenAIAccountRuntimeBlocked(&account))
		})
	}
}

func TestQuotaRecoveryRunOnceDefensivelySkipsIneligibleRows(t *testing.T) {
	now := time.Now().UTC()
	disabled := quotaRecoveryTestAccount(1, now)
	disabled.Status = StatusDisabled
	unschedulable := quotaRecoveryTestAccount(2, now)
	unschedulable.Schedulable = false
	expired := quotaRecoveryTestAccount(3, now)
	resetAt := now.Add(-time.Second)
	expired.RateLimitResetAt = &resetAt
	missingObservation := quotaRecoveryTestAccount(4, now)
	missingObservation.RateLimitedAt = nil

	repo := &quotaRecoveryRepoStub{candidates: []Account{disabled, unschedulable, expired, missingObservation}}
	checker := &quotaRecoveryCheckerStub{}
	svc := newQuotaRecoveryService(repo, checker, nil, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, result.Listed)
	require.Equal(t, 4, result.Skipped)
	require.Zero(t, result.Checked)
	require.Empty(t, checker.callSnapshot())
	require.Empty(t, repo.clearCallSnapshot())
}

func TestQuotaRecoveryRunOnceHonorsConcurrencyLimit(t *testing.T) {
	now := time.Now().UTC()
	accounts := make([]Account, 6)
	for i := range accounts {
		accounts[i] = quotaRecoveryTestAccount(int64(i+1), now)
	}
	repo := &quotaRecoveryRepoStub{candidates: accounts}
	var active atomic.Int64
	var peak atomic.Int64
	checker := &quotaRecoveryCheckerStub{checkFn: func(_ context.Context, _ *Account) QuotaRecoveryCheckResult {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := peak.Load()
			if current <= seen || peak.CompareAndSwap(seen, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		return freshQuotaRecoveryResult(QuotaRecoveryExhausted)
	}}
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.Concurrency = 2
	svc := newQuotaRecoveryService(repo, checker, nil, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 6, result.Checked)
	require.LessOrEqual(t, peak.Load(), int64(2))
}

func TestQuotaRecoveryRunOnceCycleWaitHonorsContextCancellation(t *testing.T) {
	now := time.Now().UTC()
	account := quotaRecoveryTestAccount(81, now)
	repo := &quotaRecoveryRepoStub{candidates: []Account{account}}
	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	checker := &quotaRecoveryCheckerStub{checkFn: func(_ context.Context, _ *Account) QuotaRecoveryCheckResult {
		close(checkStarted)
		<-releaseCheck
		return freshQuotaRecoveryResult(QuotaRecoveryExhausted)
	}}
	svc := newQuotaRecoveryService(repo, checker, nil, quotaRecoveryTestConfig())
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.RunOnce(context.Background())
		firstDone <- err
	}()
	<-checkStarted

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := svc.RunOnce(waitCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	require.Equal(t, []int64{account.ID}, checker.callSnapshot())

	close(releaseCheck)
	require.NoError(t, <-firstDone)
}

func TestQuotaRecoveryRunOnceCancellationDuringCheckDoesNotClearOrReadNextPage(t *testing.T) {
	now := time.Now().UTC()
	repo := &quotaRecoveryRepoStub{candidates: []Account{
		quotaRecoveryTestAccount(91, now),
		quotaRecoveryTestAccount(92, now),
	}}
	checker := &quotaRecoveryCheckerStub{checkFn: func(ctx context.Context, _ *Account) QuotaRecoveryCheckResult {
		<-ctx.Done()
		return freshQuotaRecoveryResult(QuotaRecoveryAvailable)
	}}
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.BatchSize = 1
	svc := newQuotaRecoveryService(repo, checker, nil, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	runCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := svc.RunOnce(runCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.Unknown)
	require.Equal(t, 1, result.Errors)
	require.Empty(t, repo.clearCallSnapshot())
	require.Equal(t, []int64{0}, repo.listAfterSnapshot())
}

func TestQuotaRecoveryRunOnceRunsInStandardMode(t *testing.T) {
	now := time.Now().UTC()
	repo := &quotaRecoveryRepoStub{candidates: []Account{quotaRecoveryTestAccount(1, now)}}
	checker := &quotaRecoveryCheckerStub{results: map[int64]QuotaRecoveryCheckResult{
		1: {Verdict: QuotaRecoveryExhausted, Authoritative: true, CheckedAt: now},
	}}
	cfg := quotaRecoveryTestConfig()
	cfg.RunMode = config.RunModeStandard
	svc := newQuotaRecoveryService(repo, checker, nil, cfg)
	svc.now = func() time.Time { return now }
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Listed)
	require.Equal(t, 1, result.Checked)
	require.Equal(t, 1, result.Exhausted)
	require.Equal(t, []int64{1}, checker.callSnapshot())
}

func TestProvideQuotaRecoveryServiceStartsAndStopsInStandardMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	cfg := quotaRecoveryTestConfig()
	cfg.RunMode = config.RunModeStandard
	svc, err := ProvideQuotaRecoveryService(
		&quotaRecoveryRepoStub{},
		NewQuotaRecoveryChecker(nil, nil),
		nil,
		cfg,
		db,
	)
	require.NoError(t, err)
	svc.Stop()

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQuotaRecoveryServiceStartStopOwnsSingletonLeaseOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	svc := newQuotaRecoveryService(&quotaRecoveryRepoStub{}, &quotaRecoveryCheckerStub{}, nil, quotaRecoveryTestConfig())
	svc.db = db
	require.NoError(t, svc.Start())
	require.NoError(t, svc.Start(), "repeated Start must reuse the running lease")

	var stops sync.WaitGroup
	stops.Add(2)
	go func() {
		defer stops.Done()
		svc.Stop()
	}()
	go func() {
		defer stops.Done()
		svc.Stop()
	}()
	stops.Wait()
	svc.Stop()

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQuotaRecoveryServiceStartRejectsHeldSingletonLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))

	svc := newQuotaRecoveryService(&quotaRecoveryRepoStub{}, &quotaRecoveryCheckerStub{}, nil, quotaRecoveryTestConfig())
	svc.db = db
	require.ErrorIs(t, svc.Start(), ErrQuotaRecoveryAlreadyRunning)
	svc.Stop()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQuotaRecoveryServiceStartRequiresDatabase(t *testing.T) {
	svc := newQuotaRecoveryService(&quotaRecoveryRepoStub{}, &quotaRecoveryCheckerStub{}, nil, quotaRecoveryTestConfig())
	require.ErrorIs(t, svc.Start(), ErrQuotaRecoveryDatabaseUnavailable)
}

func TestQuotaRecoveryServiceStartDuringStopRestartsAfterLeaseRelease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	for range 2 {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
			WithArgs(lockID).
			WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
			WithArgs(lockID).
			WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))
	}

	account := quotaRecoveryTestAccount(1, time.Now().UTC())
	checkStarted := make(chan struct{})
	checkCanceled := make(chan struct{})
	allowCheckReturn := make(chan struct{})
	var checkCalls atomic.Int64
	checker := &quotaRecoveryCheckerStub{checkFn: func(ctx context.Context, _ *Account) QuotaRecoveryCheckResult {
		if checkCalls.Add(1) != 1 {
			return freshQuotaRecoveryResult(QuotaRecoveryExhausted)
		}
		close(checkStarted)
		<-ctx.Done()
		close(checkCanceled)
		<-allowCheckReturn
		return QuotaRecoveryCheckResult{Verdict: QuotaRecoveryUnknown, CheckedAt: time.Now().UTC()}
	}}
	svc := newQuotaRecoveryService(
		&quotaRecoveryRepoStub{candidates: []Account{account}},
		checker,
		nil,
		quotaRecoveryTestConfig(),
	)
	svc.db = db
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }
	require.NoError(t, svc.Start())
	<-checkStarted

	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	<-checkCanceled

	startDone := make(chan error, 1)
	go func() { startDone <- svc.Start() }()
	select {
	case err := <-startDone:
		close(allowCheckReturn)
		<-stopDone
		require.Failf(t, "Start returned while Stop was incomplete", "error: %v", err)
		return
	case <-time.After(50 * time.Millisecond):
	}

	close(allowCheckReturn)
	<-stopDone
	require.NoError(t, <-startDone)
	svc.Stop()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQuotaRecoveryServiceStartDuringLeaseLossWaitsForCleanup(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	restartDB, restartMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = restartDB.Close() })

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectPing().WillReturnError(errors.New("singleton connection lost"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(lockID).
		WillReturnError(errors.New("singleton connection lost"))
	restartMock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	restartMock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	account := quotaRecoveryTestAccount(1, time.Now().UTC())
	checkStarted := make(chan struct{})
	checkCanceled := make(chan struct{})
	allowCheckReturn := make(chan struct{})
	var checkCalls atomic.Int64
	checker := &quotaRecoveryCheckerStub{checkFn: func(ctx context.Context, _ *Account) QuotaRecoveryCheckResult {
		if checkCalls.Add(1) != 1 {
			return freshQuotaRecoveryResult(QuotaRecoveryExhausted)
		}
		close(checkStarted)
		<-ctx.Done()
		close(checkCanceled)
		<-allowCheckReturn
		return QuotaRecoveryCheckResult{Verdict: QuotaRecoveryUnknown, CheckedAt: time.Now().UTC()}
	}}
	svc := newQuotaRecoveryService(
		&quotaRecoveryRepoStub{candidates: []Account{account}},
		checker,
		nil,
		quotaRecoveryTestConfig(),
	)
	svc.db = db
	svc.leaseHealthInterval = time.Millisecond
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }
	require.NoError(t, svc.Start())
	<-checkStarted
	<-checkCanceled

	svc.mu.Lock()
	require.Equal(t, quotaRecoveryStopping, svc.state)
	svc.mu.Unlock()

	startDone := make(chan error, 1)
	go func() { startDone <- svc.Start() }()
	select {
	case err := <-startDone:
		close(allowCheckReturn)
		require.Failf(t, "Start returned before the lost lease was cleaned up", "error: %v", err)
		return
	case <-time.After(50 * time.Millisecond):
	}

	svc.mu.Lock()
	svc.db = restartDB
	svc.leaseHealthInterval = time.Hour
	svc.mu.Unlock()
	close(allowCheckReturn)
	require.NoError(t, <-startDone)
	svc.Stop()
	require.NoError(t, mock.ExpectationsWereMet())
	require.NoError(t, restartMock.ExpectationsWereMet())
}
