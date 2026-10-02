//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func quotaRecoveryObservationFor(target, owner *service.Account) service.QuotaRecoveryObservation {
	credentials := make(map[string]any, len(owner.Credentials))
	for key, value := range owner.Credentials {
		credentials[key] = value
	}
	cloneID := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	return service.QuotaRecoveryObservation{
		AccountID:                            target.ID,
		RateLimitedAt:                        target.RateLimitedAt.UTC(),
		RateLimitResetAt:                     target.RateLimitResetAt.UTC(),
		AccountUpdatedAt:                     target.UpdatedAt.UTC(),
		CredentialOwnerID:                    owner.ID,
		CredentialOwnerUpdatedAt:             owner.UpdatedAt.UTC(),
		CredentialOwnerCredentials:           credentials,
		CredentialOwnerProxyID:               cloneID(owner.ProxyID),
		CredentialOwnerProxyFallbackOriginID: cloneID(owner.ProxyFallbackOriginID),
		CredentialOwnerPlatform:              owner.Platform,
		CredentialOwnerType:                  owner.Type,
		CredentialOwnerParentAccountID:       cloneID(owner.ParentAccountID),
		CredentialOwnerStatus:                owner.Status,
		CredentialOwnerSchedulable:           owner.Schedulable,
		CredentialOwnerQuotaDimension:        owner.QuotaDimensionOrDefault(),
	}
}

func accountCredentialSnapshotFor(account *service.Account) service.AccountCredentialSnapshot {
	credentials := make(map[string]any, len(account.Credentials))
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	cloneID := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	return service.AccountCredentialSnapshot{
		AccountID:             account.ID,
		Credentials:           credentials,
		UpdatedAt:             account.UpdatedAt.UTC(),
		ProxyID:               cloneID(account.ProxyID),
		ProxyFallbackOriginID: cloneID(account.ProxyFallbackOriginID),
		Platform:              account.Platform,
		Type:                  account.Type,
		ParentAccountID:       cloneID(account.ParentAccountID),
		Status:                account.Status,
		Schedulable:           account.Schedulable,
		QuotaDimension:        account.QuotaDimensionOrDefault(),
	}
}

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

func (s *AccountRepoSuite) TestUpdateCredentialsIfUnchangedReturnsGenerationsAndEmitsOutbox() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "quota-recovery-credential-receipt",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
		},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	newCredentials := map[string]any{
		"access_token":   "new-access",
		"refresh_token":  "new-refresh",
		"_token_version": int64(42),
	}

	result, applied, err := s.repo.UpdateCredentialsIfUnchanged(
		s.ctx,
		accountCredentialSnapshotFor(observed),
		newCredentials,
	)
	s.Require().NoError(err)
	s.Require().True(applied)
	s.Require().Equal(observed.UpdatedAt, result.PreviousUpdatedAt)
	s.Require().False(result.UpdatedAt.IsZero())
	s.Require().True(result.UpdatedAt.After(result.PreviousUpdatedAt))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("new-access", got.GetCredential("access_token"))
	s.Require().Equal("new-refresh", got.GetCredential("refresh_token"))
	s.Require().Equal(result.UpdatedAt, got.UpdatedAt)

	var outboxCount int
	s.Require().NoError(scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	))
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestUpdateCredentialsIfUnchangedAllowsUnrelatedGenerationChange() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "quota-recovery-credential-unrelated-change",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{"refresh_token": "same-refresh"},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	_, err = s.client.Account.UpdateOneID(account.ID).
		SetStatus(service.StatusDisabled).
		Save(s.ctx)
	s.Require().NoError(err)
	changed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(changed.UpdatedAt.Equal(observed.UpdatedAt))

	result, applied, err := s.repo.UpdateCredentialsIfUnchanged(
		s.ctx,
		accountCredentialSnapshotFor(observed),
		map[string]any{"refresh_token": "rotated-refresh"},
	)
	s.Require().NoError(err)
	s.Require().True(applied)
	s.Require().Equal(changed.UpdatedAt, result.PreviousUpdatedAt)
	s.Require().True(result.UpdatedAt.After(result.PreviousUpdatedAt))
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusDisabled, got.Status)
	s.Require().Equal("rotated-refresh", got.GetCredential("refresh_token"))
}

func TestUpdateCredentialsIfUnchangedStaysMonotonicAfterWaitingForRowLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	account := mustCreateAccount(t, client, &service.Account{
		Name:        "quota-recovery-credential-lock-generation",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{"refresh_token": "old-refresh"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
	})
	observed, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)

	blockerTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blockerTx.Rollback() })
	laterGeneration := time.Now().UTC().Add(10 * time.Second)
	_, err = blockerTx.ExecContext(ctx,
		"UPDATE accounts SET status = $1, updated_at = $2 WHERE id = $3",
		service.StatusDisabled,
		laterGeneration,
		account.ID,
	)
	require.NoError(t, err)

	type updateResult struct {
		result  service.AccountCredentialConditionalUpdateResult
		applied bool
		err     error
	}
	resultCh := make(chan updateResult, 1)
	go func() {
		result, applied, updateErr := repo.UpdateCredentialsIfUnchanged(
			ctx,
			accountCredentialSnapshotFor(observed),
			map[string]any{"refresh_token": "rotated-refresh"},
		)
		resultCh <- updateResult{result: result, applied: applied, err: updateErr}
	}()

	select {
	case result := <-resultCh:
		_ = blockerTx.Rollback()
		t.Fatalf("conditional credential update bypassed row lock: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, blockerTx.Commit())

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.True(t, result.applied)
		require.WithinDuration(t, laterGeneration, result.result.PreviousUpdatedAt, time.Microsecond)
		require.True(t, result.result.UpdatedAt.After(result.result.PreviousUpdatedAt),
			"the credential update generation must not move backward to the statement start time")
		got, getErr := repo.GetByID(ctx, account.ID)
		require.NoError(t, getErr)
		require.Equal(t, result.result.UpdatedAt, got.UpdatedAt)
		require.Equal(t, service.StatusDisabled, got.Status)
		require.Equal(t, "rotated-refresh", got.GetCredential("refresh_token"))
	case <-ctx.Done():
		t.Fatalf("conditional credential update did not resume after row lock release: %v", ctx.Err())
	}
}

func (s *AccountRepoSuite) TestUpdateCredentialsIfUnchangedRejectsCredentialAndIdentityChanges() {
	tests := []struct {
		name   string
		mutate func(accountID int64) error
	}{
		{
			name: "credentials",
			mutate: func(accountID int64) error {
				_, err := s.client.Account.UpdateOneID(accountID).
					SetCredentials(map[string]any{"refresh_token": "manual-refresh"}).
					Save(s.ctx)
				return err
			},
		},
		{
			name: "platform identity",
			mutate: func(accountID int64) error {
				_, err := s.client.Account.UpdateOneID(accountID).
					SetPlatform(service.PlatformAnthropic).
					Save(s.ctx)
				return err
			},
		},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			account := mustCreateAccount(s.T(), s.client, &service.Account{
				Name:        "quota-recovery-credential-conflict-" + test.name,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeOAuth,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "observed-refresh"},
			})
			observed, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().NoError(test.mutate(account.ID))

			_, applied, err := s.repo.UpdateCredentialsIfUnchanged(
				s.ctx,
				accountCredentialSnapshotFor(observed),
				map[string]any{"refresh_token": "provider-refresh"},
			)
			s.Require().NoError(err)
			s.Require().False(applied)
			got, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().NotEqual("provider-refresh", got.GetCredential("refresh_token"))
		})
	}
}

func TestUpdateCredentialsIfUnchangedRollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{
		Name:        "quota-recovery-credential-outbox-rollback",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Schedulable: true,
		Credentials: map[string]any{"refresh_token": "old-refresh"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
	})
	baseRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	observed, err := baseRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	failingRepo := newAccountRepositoryWithSQL(client, &failAtomicSchedulerOutboxSQLExecutor{sqlExecutor: integrationDB}, nil)

	_, applied, err := failingRepo.UpdateCredentialsIfUnchanged(
		ctx,
		accountCredentialSnapshotFor(observed),
		map[string]any{"refresh_token": "new-refresh"},
	)
	require.Error(t, err)
	require.False(t, applied)
	got, err := baseRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "old-refresh", got.GetCredential("refresh_token"))

	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1",
		account.ID,
	).Scan(&outboxCount))
	require.Zero(t, outboxCount)
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

	wrongGeneration := quotaRecoveryObservationFor(observed, observed)
	wrongGeneration.RateLimitedAt = limitedAt.Add(time.Second)
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
	s.Require().True(got.UpdatedAt.After(observed.UpdatedAt))
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

	cleared, err := s.repo.ClearRateLimitIfUnchanged(s.ctx, quotaRecoveryObservationFor(observed, observed))
	s.Require().NoError(err)
	s.Require().False(cleared)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
}

func (s *AccountRepoSuite) TestClearRateLimitIfUnchangedRejectsDifferentCredentialsAtSameUpdatedAt() {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(30 * time.Minute)
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "quota-recovery-exact-credentials-cas",
		Platform:         service.PlatformOpenAI,
		Type:             service.AccountTypeOAuth,
		Schedulable:      true,
		Credentials:      map[string]any{"access_token": "observed"},
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	observation := quotaRecoveryObservationFor(observed, observed)

	updateResult, err := s.repo.sql.ExecContext(
		s.ctx,
		"UPDATE accounts SET credentials = $1::jsonb, updated_at = $2 WHERE id = $3",
		`{"access_token":"manually-reauthorized"}`,
		observed.UpdatedAt,
		account.ID,
	)
	s.Require().NoError(err)
	rowsAffected, err := updateResult.RowsAffected()
	s.Require().NoError(err)
	s.Require().EqualValues(1, rowsAffected)

	cleared, err := s.repo.ClearRateLimitIfUnchanged(s.ctx, observation)
	s.Require().NoError(err)
	s.Require().False(cleared)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("manually-reauthorized", got.GetCredential("access_token"))
	s.Require().NotNil(got.RateLimitedAt)
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

	observation := quotaRecoveryObservationFor(observedShadow, observedParent)
	cleared, err := s.repo.ClearRateLimitIfUnchanged(s.ctx, observation)
	s.Require().NoError(err)
	s.Require().False(cleared)

	currentParent, err := s.repo.GetByID(s.ctx, updatedParent.ID)
	s.Require().NoError(err)
	observation = quotaRecoveryObservationFor(observedShadow, currentParent)
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

	observation := quotaRecoveryObservationFor(observedShadow, observedParent)
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
	observed, err := failingRepo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)

	cleared, err := failingRepo.ClearRateLimitIfUnchanged(
		context.Background(),
		quotaRecoveryObservationFor(observed, observed),
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
