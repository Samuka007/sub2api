//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type quotaRecoveryPagingRepo struct {
	AccountRepository

	mu         sync.Mutex
	candidates []Account
	afterIDs   []int64
	throughIDs []int64
	appendOnce []Account
}

func (r *quotaRecoveryPagingRepo) QuotaRecoveryCandidateUpperBound(_ context.Context, _ time.Time) (int64, error) {
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

func (r *quotaRecoveryPagingRepo) ListQuotaRecoveryCandidates(
	_ context.Context,
	_ time.Time,
	afterID, throughID int64,
	limit int,
) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterIDs = append(r.afterIDs, afterID)
	r.throughIDs = append(r.throughIDs, throughID)
	page := make([]Account, 0, limit)
	for i := range r.candidates {
		if r.candidates[i].ID <= afterID || r.candidates[i].ID > throughID {
			continue
		}
		page = append(page, r.candidates[i])
		if len(page) == limit {
			break
		}
	}
	if len(r.afterIDs) == 1 && len(r.appendOnce) > 0 {
		r.candidates = append(r.candidates, r.appendOnce...)
		r.appendOnce = nil
	}
	return page, nil
}

func (r *quotaRecoveryPagingRepo) ClearRateLimitIfUnchanged(
	_ context.Context,
	_ QuotaRecoveryObservation,
) (bool, error) {
	return true, nil
}

func (r *quotaRecoveryPagingRepo) afterIDSnapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.afterIDs...)
}

func (r *quotaRecoveryPagingRepo) throughIDSnapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.throughIDs...)
}

func TestQuotaRecoveryRunOnceScansEveryCandidateAcrossPages(t *testing.T) {
	now := time.Now().UTC()
	accounts := make([]Account, 5)
	results := make(map[int64]QuotaRecoveryCheckResult, len(accounts))
	for i := range accounts {
		id := int64(i + 1)
		accounts[i] = quotaRecoveryTestAccount(id, now)
		results[id] = freshQuotaRecoveryResult(QuotaRecoveryExhausted)
	}

	repo := &quotaRecoveryPagingRepo{candidates: accounts}
	checker := &quotaRecoveryCheckerStub{results: results}
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.BatchSize = 2
	svc := newQuotaRecoveryService(repo, checker, nil, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, result.Listed)
	require.Equal(t, 5, result.Checked)
	require.Equal(t, 5, result.Exhausted)
	require.Equal(t, []int64{0, 2, 4}, repo.afterIDSnapshot())
	require.Equal(t, []int64{5, 5, 5}, repo.throughIDSnapshot())
	require.ElementsMatch(t, []int64{1, 2, 3, 4, 5}, checker.callSnapshot())
}

func TestQuotaRecoveryRunOnceDefersCandidatesAddedAfterCycleUpperBound(t *testing.T) {
	now := time.Now().UTC()
	initial := []Account{
		quotaRecoveryTestAccount(1, now),
		quotaRecoveryTestAccount(2, now),
		quotaRecoveryTestAccount(3, now),
	}
	added := []Account{
		quotaRecoveryTestAccount(4, now),
		quotaRecoveryTestAccount(5, now),
	}
	results := make(map[int64]QuotaRecoveryCheckResult, len(initial)+len(added))
	for _, account := range append(append([]Account(nil), initial...), added...) {
		results[account.ID] = freshQuotaRecoveryResult(QuotaRecoveryExhausted)
	}

	repo := &quotaRecoveryPagingRepo{candidates: initial, appendOnce: added}
	checker := &quotaRecoveryCheckerStub{results: results}
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.BatchSize = 2
	svc := newQuotaRecoveryService(repo, checker, nil, cfg)
	svc.jitterFor = func(time.Duration) time.Duration { return 0 }

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, result.Listed)
	require.Equal(t, 3, result.Checked)
	require.Equal(t, 3, result.Exhausted)
	require.Equal(t, []int64{0, 2}, repo.afterIDSnapshot())
	require.Equal(t, []int64{3, 3}, repo.throughIDSnapshot())
	require.ElementsMatch(t, []int64{1, 2, 3}, checker.callSnapshot())
}
