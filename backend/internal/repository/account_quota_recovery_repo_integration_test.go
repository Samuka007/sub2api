//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) TestListQuotaRecoveryCandidatesFiltersAndPaginatesByID() {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-10 * time.Minute)
	soonReset := now.Add(5 * time.Minute)
	tiedReset := now.Add(10 * time.Minute)
	laterReset := now.Add(15 * time.Minute)

	soon := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-soon",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &soonReset,
	})
	tiedFirst := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-tied-first",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &tiedReset,
	})
	tiedSecond := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-tied-second",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &tiedReset,
	})
	later := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-later",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})

	boundary := now
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-expired",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &boundary,
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-missing-limited-at",
		Schedulable:      true,
		RateLimitResetAt: &laterReset,
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-disabled",
		Status:           service.StatusDisabled,
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-unsupported-api-key",
		Platform:         service.PlatformOpenAI,
		Type:             service.AccountTypeAPIKey,
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-unsupported-platform",
		Platform:         service.PlatformGemini,
		Type:             service.AccountTypeOAuth,
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})
	unschedulable := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-unschedulable",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})
	_, err := s.client.Account.UpdateOneID(unschedulable.ID).SetSchedulable(false).Save(s.ctx)
	s.Require().NoError(err)

	deleted := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-deleted",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &laterReset,
	})
	_, err = s.client.Account.UpdateOneID(deleted.ID).SetDeletedAt(now).Save(s.ctx)
	s.Require().NoError(err)

	upperBound, err := s.repo.QuotaRecoveryCandidateUpperBound(s.ctx, now)
	s.Require().NoError(err)
	s.Require().Equal(later.ID, upperBound)

	all, err := s.repo.ListQuotaRecoveryCandidates(s.ctx, now, 0, upperBound, 10)
	s.Require().NoError(err)
	s.Require().Equal(
		[]int64{soon.ID, tiedFirst.ID, tiedSecond.ID, later.ID},
		idsOfAccounts(all),
	)

	bounded, err := s.repo.ListQuotaRecoveryCandidates(s.ctx, now, 0, upperBound, 2)
	s.Require().NoError(err)
	s.Require().Equal([]int64{soon.ID, tiedFirst.ID}, idsOfAccounts(bounded))
	_, err = s.client.Account.UpdateOneID(soon.ID).SetSchedulable(false).Save(s.ctx)
	s.Require().NoError(err)

	nextPage, err := s.repo.ListQuotaRecoveryCandidates(s.ctx, now, tiedFirst.ID, upperBound, 2)
	s.Require().NoError(err)
	s.Require().Equal([]int64{tiedSecond.ID, later.ID}, idsOfAccounts(nextPage))

	afterLast, err := s.repo.ListQuotaRecoveryCandidates(s.ctx, now, later.ID, upperBound, 2)
	s.Require().NoError(err)
	s.Require().Empty(afterLast)

	empty, err := s.repo.ListQuotaRecoveryCandidates(s.ctx, now, 0, upperBound, 0)
	s.Require().NoError(err)
	s.Require().Empty(empty)
}

func (s *AccountRepoSuite) TestClearRateLimitIfUnchangedIsScopedAndEmitsOutbox() {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	overloadUntil := now.Add(20 * time.Minute)
	tempUntil := now.Add(10 * time.Minute)
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-cas",
		Status:           service.StatusActive,
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
		OverloadUntil:    &overloadUntil,
	})
	_, err := s.client.Account.UpdateOneID(account.ID).
		SetTempUnschedulableUntil(tempUntil).
		SetTempUnschedulableReason("keep-this-state").
		Save(s.ctx)
	s.Require().NoError(err)
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)

	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	wrongGeneration := service.QuotaRecoveryObservation{
		AccountID:                account.ID,
		RateLimitedAt:            limitedAt.Add(time.Second),
		RateLimitResetAt:         resetAt,
		AccountUpdatedAt:         observed.UpdatedAt,
		CredentialOwnerID:        account.ID,
		CredentialOwnerUpdatedAt: observed.UpdatedAt,
	}
	cleared, err := s.repo.ClearRateLimitIfUnchanged(s.ctx, wrongGeneration)
	s.Require().NoError(err)
	s.Require().False(cleared)
	s.Require().Empty(cacheRecorder.setAccounts)

	observation := wrongGeneration
	observation.RateLimitedAt = limitedAt
	cleared, err = s.repo.ClearRateLimitIfUnchanged(s.ctx, observation)
	s.Require().NoError(err)
	s.Require().True(cleared)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().True(got.Schedulable)
	s.Require().NotNil(got.OverloadUntil)
	s.Require().WithinDuration(overloadUntil, *got.OverloadUntil, time.Second)
	s.Require().NotNil(got.TempUnschedulableUntil)
	s.Require().WithinDuration(tempUntil, *got.TempUnschedulableUntil, time.Second)
	s.Require().Equal("keep-this-state", got.TempUnschedulableReason)

	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Nil(cacheRecorder.setAccounts[0].RateLimitedAt)
	s.Require().Nil(cacheRecorder.setAccounts[0].RateLimitResetAt)
	s.Require().NotNil(cacheRecorder.setAccounts[0].OverloadUntil)
	s.Require().NotNil(cacheRecorder.setAccounts[0].TempUnschedulableUntil)

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestClearRateLimitIfUnchangedRejectsConcurrentAccountUpdate() {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-identity-cas",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)

	updated, err := s.client.Account.UpdateOneID(account.ID).
		SetCredentials(map[string]any{"access_token": "replacement-token"}).
		Save(s.ctx)
	s.Require().NoError(err)
	s.Require().False(updated.UpdatedAt.Equal(observed.UpdatedAt))

	cleared, err := s.repo.ClearRateLimitIfUnchanged(
		s.ctx,
		service.QuotaRecoveryObservation{
			AccountID:                account.ID,
			RateLimitedAt:            limitedAt,
			RateLimitResetAt:         resetAt,
			AccountUpdatedAt:         observed.UpdatedAt,
			CredentialOwnerID:        account.ID,
			CredentialOwnerUpdatedAt: observed.UpdatedAt,
		},
	)
	s.Require().NoError(err)
	s.Require().False(cleared)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
}

func (s *AccountRepoSuite) TestClearRateLimitIfUnchangedRejectsConcurrentSparkParentUpdate() {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	parent := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "quota-recovery-parent-cas",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "original-token"},
	})
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-shadow-cas",
		Platform:         service.PlatformOpenAI,
		Type:             service.AccountTypeOAuth,
		Schedulable:      true,
		ParentAccountID:  &parent.ID,
		QuotaDimension:   service.QuotaDimensionSpark,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
	})
	observedShadow, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	observedParent, err := s.repo.GetByID(s.ctx, parent.ID)
	s.Require().NoError(err)

	updatedParent, err := s.client.Account.UpdateOneID(parent.ID).
		SetCredentials(map[string]any{"access_token": "replacement-token"}).
		Save(s.ctx)
	s.Require().NoError(err)
	s.Require().False(updatedParent.UpdatedAt.Equal(observedParent.UpdatedAt))

	observation := service.QuotaRecoveryObservation{
		AccountID:                shadow.ID,
		RateLimitedAt:            limitedAt,
		RateLimitResetAt:         resetAt,
		AccountUpdatedAt:         observedShadow.UpdatedAt,
		CredentialOwnerID:        parent.ID,
		CredentialOwnerUpdatedAt: observedParent.UpdatedAt,
	}
	cleared, err := s.repo.ClearRateLimitIfUnchanged(s.ctx, observation)
	s.Require().NoError(err)
	s.Require().False(cleared)

	observation.CredentialOwnerUpdatedAt = updatedParent.UpdatedAt
	cleared, err = s.repo.ClearRateLimitIfUnchanged(s.ctx, observation)
	s.Require().NoError(err)
	s.Require().True(cleared)
}

func TestClearRateLimitIfUnchangedWaitsForConcurrentSparkParentUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	parent := mustCreateAccount(t, client, &service.Account{
		Name:        "quota-recovery-parent-lock",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "original-token"},
	})
	shadow := mustCreateAccount(t, client, &service.Account{
		Name:             "quota-recovery-shadow-lock",
		Platform:         service.PlatformOpenAI,
		Type:             service.AccountTypeOAuth,
		Schedulable:      true,
		ParentAccountID:  &parent.ID,
		QuotaDimension:   service.QuotaDimensionSpark,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id IN ($1, $2)", shadow.ID, parent.ID)
		_ = client.Account.DeleteOneID(shadow.ID).Exec(context.Background())
		_ = client.Account.DeleteOneID(parent.ID).Exec(context.Background())
	})
	observedShadow, err := repo.GetByID(ctx, shadow.ID)
	require.NoError(t, err)
	observedParent, err := repo.GetByID(ctx, parent.ID)
	require.NoError(t, err)

	parentTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = parentTx.Rollback() })
	newParentUpdatedAt := observedParent.UpdatedAt.Add(time.Second)
	updateResult, err := parentTx.ExecContext(
		ctx,
		"UPDATE accounts SET credentials = $1::jsonb, updated_at = $2 WHERE id = $3",
		`{"access_token":"replacement-token"}`,
		newParentUpdatedAt,
		parent.ID,
	)
	require.NoError(t, err)
	rowsAffected, err := updateResult.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rowsAffected)

	observation := service.QuotaRecoveryObservation{
		AccountID:                shadow.ID,
		RateLimitedAt:            limitedAt,
		RateLimitResetAt:         resetAt,
		AccountUpdatedAt:         observedShadow.UpdatedAt,
		CredentialOwnerID:        parent.ID,
		CredentialOwnerUpdatedAt: observedParent.UpdatedAt,
	}
	type clearResult struct {
		cleared bool
		err     error
	}
	resultCh := make(chan clearResult, 1)
	go func() {
		cleared, clearErr := repo.ClearRateLimitIfUnchanged(ctx, observation)
		resultCh <- clearResult{cleared: cleared, err: clearErr}
	}()

	select {
	case result := <-resultCh:
		_ = parentTx.Rollback()
		t.Fatalf("clear bypassed credential-owner row lock: result=%+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, parentTx.Commit())

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.False(t, result.cleared)
	case <-time.After(5 * time.Second):
		t.Fatal("clear did not resume after credential-owner update committed")
	}
}

func TestClearRateLimitIfUnchangedRollsBackWhenOutboxInsertFails(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{
		Name:             "quota-recovery-outbox-rollback",
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
	})
	failingRepo := newAccountRepositoryWithSQL(client, &failAtomicSchedulerOutboxSQLExecutor{
		sqlExecutor: integrationDB,
	}, nil)

	cleared, err := failingRepo.ClearRateLimitIfUnchanged(
		context.Background(),
		service.QuotaRecoveryObservation{
			AccountID:                account.ID,
			RateLimitedAt:            limitedAt,
			RateLimitResetAt:         resetAt,
			AccountUpdatedAt:         account.UpdatedAt,
			CredentialOwnerID:        account.ID,
			CredentialOwnerUpdatedAt: account.UpdatedAt,
		},
	)
	require.Error(t, err)
	require.False(t, cleared)

	got, err := failingRepo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateLimitedAt)
	require.NotNil(t, got.RateLimitResetAt)
	require.WithinDuration(t, limitedAt, *got.RateLimitedAt, time.Second)
	require.WithinDuration(t, resetAt, *got.RateLimitResetAt, time.Second)

	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1",
		account.ID,
	).Scan(&outboxCount))
	require.Zero(t, outboxCount)
}
