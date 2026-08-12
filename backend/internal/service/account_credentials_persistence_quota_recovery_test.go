//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func quotaRecoveryCredentialTestAccount(id int64, updatedAt time.Time, credentials map[string]any) *Account {
	return &Account{
		ID:             id,
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Credentials:    shallowCopyMap(credentials),
		UpdatedAt:      updatedAt,
		Status:         StatusActive,
		Schedulable:    true,
		QuotaDimension: QuotaDimensionGlobal,
	}
}

func quotaRecoveryCredentialTestObservation(target, owner *Account) QuotaRecoveryObservation {
	observation := QuotaRecoveryObservation{
		AccountID:        target.ID,
		AccountUpdatedAt: target.UpdatedAt,
	}
	applyCredentialSnapshotToObservation(&observation, accountCredentialSnapshot(owner), target.ID)
	return observation
}

func quotaRecoveryCredentialProbeContext(
	t *testing.T,
	target *Account,
	owner *Account,
) (context.Context, *quotaRecoveryCredentialRefreshReceipt) {
	t.Helper()
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	t.Cleanup(cancel)
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(deadlineCtx, target, owner)
	require.NoError(t, err)
	return ctx, receipt
}

func TestQuotaRecoveryCredentialReceiptAdvancesNormalOwnerAfterConditionalPersist(t *testing.T) {
	initialUpdatedAt := time.Now().UTC().Add(-time.Minute)
	durableUpdatedAt := initialUpdatedAt.Add(10 * time.Second)
	account := quotaRecoveryCredentialTestAccount(41, initialUpdatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	repo := &refreshAPIAccountRepo{account: account, conditionalUpdatedAt: durableUpdatedAt}
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), account, account)
	require.NoError(t, err)

	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.NoError(t, receipt.applyToObservation(&observation))
	newCredentials := map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}
	require.NoError(t, persistAccountCredentials(ctx, repo, account, newCredentials))
	require.Equal(t, 1, repo.conditionalCASCalls)
	require.Zero(t, repo.updateCredentialsCalls, "quota-scoped persistence must not use unconditional UpdateCredentials")
	require.Equal(t, durableUpdatedAt, account.UpdatedAt)
	require.Equal(t, newCredentials, account.Credentials)

	require.NoError(t, receipt.applyToObservation(&observation))
	require.Equal(t, durableUpdatedAt, observation.AccountUpdatedAt)
	require.Equal(t, durableUpdatedAt, observation.CredentialOwnerUpdatedAt)
	require.Equal(t, newCredentials, observation.CredentialOwnerCredentials)
}

func TestQuotaRecoveryCredentialReceiptAdvancesSparkParentWithoutChangingTargetGeneration(t *testing.T) {
	now := time.Now().UTC()
	parent := quotaRecoveryCredentialTestAccount(51, now.Add(-time.Minute), map[string]any{"access_token": "old"})
	shadow := quotaRecoveryCredentialTestAccount(52, now.Add(-2*time.Minute), map[string]any{})
	shadow.ParentAccountID = &parent.ID
	shadow.QuotaDimension = QuotaDimensionSpark
	durableUpdatedAt := now.Add(time.Second)
	repo := &refreshAPIAccountRepo{account: parent, conditionalUpdatedAt: durableUpdatedAt}
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), shadow, parent)
	require.NoError(t, err)

	observation := quotaRecoveryCredentialTestObservation(shadow, parent)
	require.NoError(t, receipt.applyToObservation(&observation))
	require.NoError(t, persistAccountCredentials(ctx, repo, parent, map[string]any{"access_token": "new"}))
	require.NoError(t, receipt.applyToObservation(&observation))
	require.Equal(t, shadow.UpdatedAt, observation.AccountUpdatedAt)
	require.Equal(t, durableUpdatedAt, observation.CredentialOwnerUpdatedAt)
	require.Equal(t, map[string]any{"access_token": "new"}, observation.CredentialOwnerCredentials)
	require.Equal(t, parent.ID, observation.CredentialOwnerID)
}

func TestQuotaRecoveryCredentialReceiptRejectsMismatchedChainHead(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(61, updatedAt, map[string]any{"access_token": "observed"})
	_, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), account, account)
	require.NoError(t, err)

	observation := quotaRecoveryCredentialTestObservation(account, account)
	observation.CredentialOwnerCredentials = map[string]any{"access_token": "different-at-same-generation"}
	err = receipt.applyToObservation(&observation)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
}

func TestQuotaRecoveryCredentialReceiptRequiresConditionalRepository(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(71, updatedAt, map[string]any{"access_token": "old"})
	ctx, _, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), account, account)
	require.NoError(t, err)

	err = persistAccountCredentials(ctx, &mockAccountRepoForGemini{}, account, map[string]any{"access_token": "new"})
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialCASUnavailable)
	require.Equal(t, map[string]any{"access_token": "old"}, account.Credentials)
}

func TestOAuthRefreshAPIQuotaReceiptRejectsManualOwnerChangeBeforeProviderCall(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	observed := quotaRecoveryCredentialTestAccount(81, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	changed := *observed
	changed.Credentials = map[string]any{
		"access_token":  "manual-access",
		"refresh_token": "manual-refresh",
	}
	// Exercise the exact-credentials guard independently from UpdatedAt.
	changed.UpdatedAt = observed.UpdatedAt
	repo := &refreshAPIAccountRepo{account: &changed}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "provider-access", "refresh_token": "provider-refresh"},
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx, _, err := withQuotaRecoveryCredentialRefreshReceipt(probeCtx, observed, observed)
	require.NoError(t, err)

	_, err = NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}).RefreshIfNeeded(ctx, observed, executor, time.Minute)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Zero(t, executor.refreshCalls, "provider token rotation must not start after the owner generation changed")
}

func TestOAuthRefreshAPIQuotaReceiptPersistsAndAdvancesSuccessfulRotation(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	durableUpdatedAt := updatedAt.Add(5 * time.Second)
	observed := quotaRecoveryCredentialTestAccount(91, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	durable := *observed
	durable.Credentials = shallowCopyMap(observed.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable, conditionalUpdatedAt: durableUpdatedAt}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(probeCtx, observed, observed)
	require.NoError(t, err)
	probeDeadline, ok := ctx.Deadline()
	require.True(t, ok)
	repo.beforeConditionalCAS = func(persistCtx context.Context, _ *refreshAPIAccountRepo, _ map[string]any) {
		persistDeadline, hasDeadline := persistCtx.Deadline()
		require.True(t, hasDeadline)
		require.True(t, persistDeadline.Equal(probeDeadline.Add(quotaRecoveryCredentialPersistTimeout)),
			"an active provider rotation must keep the original probe budget plus persistence grace")
	}
	observation := quotaRecoveryCredentialTestObservation(observed, observed)
	require.NoError(t, receipt.applyToObservation(&observation))
	cache := &refreshAPICacheStub{lockResult: true}

	result, err := NewOAuthRefreshAPI(repo, cache).RefreshIfNeeded(ctx, observed, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, 1, repo.conditionalCASCalls)
	require.Equal(t, 1, cache.deleteCalls, "durable rotation must evict the pre-rotation access token before releasing the lock")
	require.NotEmpty(t, result.Account.GetCredential("_token_version"))
	require.NoError(t, receipt.applyToObservation(&observation))
	require.Equal(t, durableUpdatedAt, observation.AccountUpdatedAt)
	require.Equal(t, "new-access", observation.CredentialOwnerCredentials["access_token"])
	require.Contains(t, observation.CredentialOwnerCredentials, "_token_version")
}

func TestQuotaRecoveryCredentialReceiptCASMissLeavesAccountUnchangedAndInvalidatesReceipt(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(101, updatedAt, map[string]any{"access_token": "old"})
	repo := &refreshAPIAccountRepo{account: account, conditionalCASMiss: true}
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(deadlineCtx, account, account)
	require.NoError(t, err)
	observation := quotaRecoveryCredentialTestObservation(account, account)

	err = persistAccountCredentials(ctx, repo, account, map[string]any{"access_token": "new"})
	require.True(t, errors.Is(err, ErrQuotaRecoveryCredentialStateChanged))
	require.Equal(t, map[string]any{"access_token": "old"}, account.Credentials)
	require.Equal(t, updatedAt, account.UpdatedAt)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
	require.Equal(t, map[string]any{"access_token": "old"}, observation.CredentialOwnerCredentials)
}

func TestQuotaRecoveryCredentialReceiptPersistsRotationButInvalidatesProbeAfterUnrelatedChange(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	attemptAccount := quotaRecoveryCredentialTestAccount(111, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	durableAccount := *attemptAccount
	durableAccount.Credentials = shallowCopyMap(attemptAccount.Credentials)
	durableAccount.Status = StatusDisabled
	durableAccount.ProxyID = func() *int64 { value := int64(77); return &value }()
	durableAccount.UpdatedAt = updatedAt.Add(3 * time.Second)
	newUpdatedAt := durableAccount.UpdatedAt.Add(time.Second)
	repo := &refreshAPIAccountRepo{account: &durableAccount, conditionalUpdatedAt: newUpdatedAt}
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), attemptAccount, attemptAccount)
	require.NoError(t, err)
	observation := quotaRecoveryCredentialTestObservation(attemptAccount, attemptAccount)
	require.NoError(t, receipt.applyToObservation(&observation))

	newCredentials := map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}
	require.NoError(t, persistAccountCredentials(ctx, repo, attemptAccount, newCredentials),
		"an irreversible provider rotation must be saved when credentials and identity still match")
	require.Equal(t, newCredentials, durableAccount.Credentials)
	require.Equal(t, newCredentials, attemptAccount.Credentials)
	require.Equal(t, newUpdatedAt, attemptAccount.UpdatedAt)
	require.ErrorIs(t, validateQuotaRecoveryCredentialOwner(ctx, attemptAccount), ErrQuotaRecoveryCredentialStateChanged,
		"an invalid receipt must stop token use immediately after the credential save")
	err = receipt.applyToObservation(&observation)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged,
		"the credential save may succeed, but the quota probe must not clear against a mixed row generation")
}

func TestQuotaRecoveryCredentialReceiptDoesNotOverwriteConcurrentCredentialChange(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	attemptAccount := quotaRecoveryCredentialTestAccount(121, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	durableAccount := *attemptAccount
	durableAccount.Credentials = map[string]any{
		"access_token":  "manual-access",
		"refresh_token": "manual-refresh",
	}
	durableAccount.UpdatedAt = updatedAt.Add(time.Second)
	repo := &refreshAPIAccountRepo{account: &durableAccount}
	ctx, _ := quotaRecoveryCredentialProbeContext(t, attemptAccount, attemptAccount)

	err := persistAccountCredentials(ctx, repo, attemptAccount, map[string]any{
		"access_token":  "provider-access",
		"refresh_token": "provider-refresh",
	})
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Equal(t, "manual-access", durableAccount.GetCredential("access_token"))
	require.Equal(t, "old-access", attemptAccount.GetCredential("access_token"))
}

func TestQuotaRecoveryCredentialReceiptReconcilesTransientWriteAndReadFailures(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(125, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	repo := &refreshAPIAccountRepo{
		account: account,
		conditionalErrors: []error{
			errors.New("first write unavailable"),
			errors.New("second write unavailable"),
			nil,
		},
		getByIDErrors: []error{
			errors.New("first read unavailable"),
			errors.New("second read unavailable"),
			nil,
		},
	}
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	newCredentials := map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}

	require.NoError(t, persistAccountCredentials(ctx, repo, account, newCredentials))
	require.Equal(t, 3, repo.conditionalCASCalls)
	require.GreaterOrEqual(t, repo.getByIDCalls, 3)
	require.Equal(t, "new-access", account.GetCredential("access_token"))
	require.NoError(t, receipt.applyToObservation(&observation),
		"a proved-old retry that eventually commits may keep the receipt authoritative")
}

func TestQuotaRecoveryCredentialReceiptReconcilesAmbiguousCommittedWrite(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(126, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{
		account:           &durable,
		conditionalErrors: []error{errors.New("commit result unknown")},
		conditionalErrorHook: func(repo *refreshAPIAccountRepo, credentials map[string]any) {
			repo.account.Credentials = cloneCredentialDocument(credentials)
			repo.account.UpdatedAt = updatedAt.Add(time.Second)
		},
	}
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)

	require.NoError(t, persistAccountCredentials(ctx, repo, account, map[string]any{
		"access_token":  "new-access",
		"refresh_token": "new-refresh",
	}))
	require.Equal(t, 1, repo.conditionalCASCalls, "an observed durable commit must not be overwritten by a retry")
	require.Equal(t, "new-access", account.GetCredential("access_token"))
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged,
		"an ambiguous commit is preserved but cannot authorize quota recovery")
}

func TestOAuthRefreshAPIQuotaReceiptPersistsRotationAfterCallerCancellation(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	observed := quotaRecoveryCredentialTestAccount(131, updatedAt, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	})
	durable := *observed
	durable.Credentials = shallowCopyMap(observed.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable}
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Minute)
	defer deadlineCancel()
	ctx, cancel := context.WithCancel(deadlineCtx)
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(ctx, observed, observed)
	require.NoError(t, err)
	observation := quotaRecoveryCredentialTestObservation(observed, observed)
	require.NoError(t, receipt.applyToObservation(&observation))
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
		onRefresh:    cancel,
	}
	cache := &refreshAPICacheStub{lockResult: true}

	_, err = NewOAuthRefreshAPI(repo, cache).RefreshIfNeeded(ctx, observed, executor, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, repo.conditionalCASCalls)
	require.NoError(t, repo.conditionalContextErr,
		"the detached persistence context must remain usable after the probe context is canceled")
	require.Equal(t, 1, cache.deleteCalls)
	require.NoError(t, cache.deleteCtxErr, "cache cleanup must be detached from the canceled probe context")
	require.Equal(t, "new-access", durable.GetCredential("access_token"),
		"the detached exact-credential CAS must finish an irreversible provider rotation")
	err = receipt.applyToObservation(&observation)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged,
		"a canceled probe must remain non-authoritative even when credential recovery succeeds")
}

func TestOpenAITokenProviderQuotaReceiptBypassesSharedTokenCache(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(141, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "new-db-token",
		"refresh_token": "new-db-refresh",
		"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)
	cache := newOpenAITokenCacheStub()
	cache.tokens[OpenAITokenCacheKey(account)] = "stale-cache-token"

	provider := NewOpenAITokenProvider(nil, cache, nil)
	token, err := provider.GetAccessToken(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "new-db-token", token)
	require.Zero(t, cache.getCalled, "a quota probe must not consume an unversioned shared cache token")
	require.Zero(t, cache.setCalled, "a quota probe must not republish its token into shared cache")
	require.Equal(t, int32(1), cache.deleteCalled)
	token, err = provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "new-db-token", token, "the first normal request after recovery must not see the stale cache token")
}

func TestClaudeTokenProviderQuotaReceiptBypassesSharedTokenCache(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(142, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "new-db-token",
		"refresh_token": "new-db-refresh",
		"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	account.Platform = PlatformAnthropic
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)
	cache := newClaudeTokenCacheStub()
	cache.tokens[ClaudeTokenCacheKey(account)] = "stale-cache-token"

	provider := NewClaudeTokenProvider(nil, cache, nil)
	token, err := provider.GetAccessToken(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "new-db-token", token)
	require.Zero(t, cache.getCalled, "a quota probe must not consume an unversioned shared cache token")
	require.Zero(t, cache.setCalled, "a quota probe must not republish its token into shared cache")
	require.Equal(t, int32(1), cache.deleteCalled)
	token, err = provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "new-db-token", token, "the first normal request after recovery must not see the stale cache token")
}

func TestOpenAITokenProviderQuotaReceiptLockHeldFailsClosed(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(143, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
		"expires_at":    time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable}
	providerCache := newOpenAITokenCacheStub()
	providerCache.tokens[OpenAITokenCacheKey(account)] = "stale-cache-token"
	provider := NewOpenAITokenProvider(repo, providerCache, nil)
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: false}), executor)
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.NoError(t, receipt.applyToObservation(&observation))

	token, err := provider.GetAccessToken(ctx, account)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialRefreshLockHeld)
	require.Empty(t, token)
	require.Zero(t, executor.refreshCalls)
	require.Zero(t, providerCache.getCalled)
	require.Zero(t, providerCache.setCalled)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestOpenAITokenProviderQuotaReceiptRefreshPersistenceErrorDoesNotFallback(t *testing.T) {
	persistErr := errors.New("scheduler outbox unavailable")
	account := quotaRecoveryCredentialTestAccount(144, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
		"expires_at":    time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{
		account:           &durable,
		conditionalErrors: []error{persistErr},
		conditionalErrorHook: func(repo *refreshAPIAccountRepo, _ map[string]any) {
			repo.account.Credentials = map[string]any{
				"access_token":  "manual-token",
				"refresh_token": "manual-refresh",
			}
		},
	}
	providerCache := newOpenAITokenCacheStub()
	providerCache.tokens[OpenAITokenCacheKey(account)] = "stale-cache-token"
	provider := NewOpenAITokenProvider(repo, providerCache, nil)
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "rotated-token", "refresh_token": "rotated-refresh"},
	}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}), executor)
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.NoError(t, receipt.applyToObservation(&observation))

	token, err := provider.GetAccessToken(ctx, account)
	require.Error(t, err)
	require.Empty(t, token, "ProviderRefreshErrorUseExistingToken must not apply inside a quota receipt")
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, 1, repo.conditionalCASCalls)
	require.Equal(t, "manual-token", durable.GetCredential("access_token"))
	require.Zero(t, providerCache.getCalled)
	require.Zero(t, providerCache.setCalled)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestClaudeTokenProviderQuotaReceiptRefreshErrorDoesNotFallback(t *testing.T) {
	refreshErr := errors.New("provider rejected refresh")
	account := quotaRecoveryCredentialTestAccount(145, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
		"expires_at":    time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	account.Platform = PlatformAnthropic
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable}
	provider := NewClaudeTokenProvider(repo, newClaudeTokenCacheStub(), nil)
	executor := &refreshAPIExecutorStub{needsRefresh: true, err: refreshErr}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}), executor)
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.NoError(t, receipt.applyToObservation(&observation))

	token, err := provider.GetAccessToken(ctx, account)
	require.ErrorIs(t, err, refreshErr)
	require.Empty(t, token)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestOpenAITokenProviderQuotaReceiptStopsAfterGenerationDriftedDuringRefresh(t *testing.T) {
	updatedAt := time.Now().UTC().Add(-time.Minute)
	account := quotaRecoveryCredentialTestAccount(146, updatedAt, map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
		"expires_at":    time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{
		account:               &durable,
		conditionalPreviousAt: updatedAt.Add(time.Second),
		conditionalUpdatedAt:  updatedAt.Add(2 * time.Second),
	}
	providerCache := newOpenAITokenCacheStub()
	provider := NewOpenAITokenProvider(repo, providerCache, nil)
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "rotated-token", "refresh_token": "rotated-refresh"},
	}
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}), executor)
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)

	token, err := provider.GetAccessToken(ctx, account)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Empty(t, token, "the invalid receipt must stop quota work immediately after preserving the rotated token")
	require.Equal(t, "rotated-token", durable.GetCredential("access_token"))
	require.Zero(t, providerCache.getCalled)
	require.Zero(t, providerCache.setCalled)
}

func TestOpenAITokenProviderQuotaReceiptRotationDeletesAPIAndProviderCaches(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(1461, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
		"expires_at":    time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable}
	providerCache := newOpenAITokenCacheStub()
	providerCache.tokens[OpenAITokenCacheKey(account)] = "stale-provider-token"
	apiCache := &refreshAPICacheStub{lockResult: true}
	provider := NewOpenAITokenProvider(repo, providerCache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, apiCache), &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "rotated-token", "refresh_token": "rotated-refresh"},
	})
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)

	token, err := provider.GetAccessToken(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "rotated-token", token)
	require.Equal(t, 1, apiCache.deleteCalls, "the durable commit boundary owns API-cache eviction")
	require.Equal(t, int32(1), providerCache.deleteCalled, "the provider must independently evict its configured cache")
	require.Zero(t, providerCache.getCalled)
	require.Zero(t, providerCache.setCalled)
}

func TestOAuthRefreshAPIQuotaReceiptCacheDeleteFailurePreservesRotationButFailsClosed(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(1462, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-db-token",
		"refresh_token": "old-db-refresh",
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	repo := &refreshAPIAccountRepo{account: &durable}
	cache := &refreshAPICacheStub{lockResult: true, deleteErr: errors.New("redis delete unavailable")}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "rotated-token", "refresh_token": "rotated-refresh"},
	}
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)

	_, err := NewOAuthRefreshAPI(repo, cache).RefreshIfNeeded(ctx, account, executor, time.Minute)
	require.Error(t, err)
	require.Equal(t, "rotated-token", durable.GetCredential("access_token"),
		"the irreversible rotation must remain durable even when stale-cache deletion fails")
	require.Equal(t, 1, cache.deleteCalls)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestOAuthRefreshAPIQuotaReceiptLockFailuresAreFailClosed(t *testing.T) {
	newAttempt := func(id int64) (*Account, *refreshAPIAccountRepo, *refreshAPIExecutorStub) {
		account := quotaRecoveryCredentialTestAccount(id, time.Now().UTC().Add(-time.Minute), map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh",
		})
		durable := *account
		durable.Credentials = shallowCopyMap(account.Credentials)
		return account, &refreshAPIAccountRepo{account: &durable}, &refreshAPIExecutorStub{needsRefresh: true}
	}

	t.Run("distributed cache missing", func(t *testing.T) {
		account, repo, executor := newAttempt(147)
		ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
		_, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(ctx, account, executor, time.Minute)
		require.ErrorIs(t, err, ErrQuotaRecoveryCredentialRefreshLockUnavailable)
		require.Zero(t, executor.refreshCalls)
		observation := quotaRecoveryCredentialTestObservation(account, account)
		require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
	})

	t.Run("lock acquisition error", func(t *testing.T) {
		account, repo, executor := newAttempt(148)
		ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
		cache := &refreshAPICacheStub{lockErr: errors.New("redis unavailable")}
		_, err := NewOAuthRefreshAPI(repo, cache).RefreshIfNeeded(ctx, account, executor, time.Minute)
		require.ErrorIs(t, err, ErrQuotaRecoveryCredentialRefreshLockUnavailable)
		require.Zero(t, executor.refreshCalls)
		observation := quotaRecoveryCredentialTestObservation(account, account)
		require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
	})

	t.Run("deadline missing", func(t *testing.T) {
		account, repo, executor := newAttempt(149)
		ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(context.Background(), account, account)
		require.NoError(t, err)
		_, err = NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}).RefreshIfNeeded(ctx, account, executor, time.Minute)
		require.ErrorIs(t, err, ErrQuotaRecoveryCredentialRefreshLockUnavailable)
		require.Zero(t, executor.refreshCalls)
		observation := quotaRecoveryCredentialTestObservation(account, account)
		require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
	})
}

func TestOAuthRefreshAPIQuotaReceiptLockTTLCoversDeadlineAndPersistence(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(150, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token":  "old-token",
		"refresh_token": "old-refresh",
	})
	repo := &refreshAPIAccountRepo{account: account}
	cache := &refreshAPICacheStub{lockResult: false}
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)

	result, err := NewOAuthRefreshAPI(repo, cache, 5*time.Second).RefreshIfNeeded(ctx, account, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.LockHeld)
	require.GreaterOrEqual(t, cache.lockTTL,
		time.Until(deadline)+quotaRecoveryCredentialPersistTimeout+defaultRefreshPostPersistCleanupTimeout+
			defaultRefreshLockReleaseTimeout+quotaRecoveryRefreshLockSafetyMargin,
		"the distributed lease must remain valid through detached persistence and lock cleanup")
}

func TestOAuthRefreshAPINormalPathKeepsConfiguredLockTTL(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(151, time.Now().UTC(), map[string]any{"access_token": "token"})
	cache := &refreshAPICacheStub{lockResult: false}
	configuredTTL := 17 * time.Second
	result, err := NewOAuthRefreshAPI(&refreshAPIAccountRepo{account: account}, cache, configuredTTL).
		RefreshIfNeeded(context.Background(), account, &refreshAPIExecutorStub{}, time.Minute)
	require.NoError(t, err)
	require.True(t, result.LockHeld)
	require.Equal(t, configuredTTL, cache.lockTTL)
}

func TestEnsureAgentIdentityTaskQuotaReceiptRejectsRereadChangeBeforeRegistration(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := quotaRecoveryCredentialTestAccount(152, time.Now().UTC().Add(-time.Minute), map[string]any{
		"auth_mode":         OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  key.runtimeID,
		"agent_private_key": privateKey,
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	durable.UpdatedAt = account.UpdatedAt.Add(time.Second)
	repo := &refreshAPIAccountRepo{account: &durable}
	registerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		registerCalls++
	}))
	defer server.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)

	err := ensureAgentIdentityTaskForAccount(ctx, repo, nil, &sync.Mutex{}, account, "")
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Zero(t, registerCalls)
	require.Zero(t, repo.conditionalCASCalls)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestEnsureAgentIdentityTaskQuotaReceiptRequiresSuccessfulReread(t *testing.T) {
	tests := []struct {
		name string
		repo func() *refreshAPIAccountRepo
	}{
		{
			name: "read error",
			repo: func() *refreshAPIAccountRepo {
				return &refreshAPIAccountRepo{getByIDErr: errors.New("database unavailable")}
			},
		},
		{
			name: "missing account",
			repo: func() *refreshAPIAccountRepo {
				return &refreshAPIAccountRepo{}
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := quotaRecoveryCredentialTestAccount(int64(1521+index), time.Now().UTC().Add(-time.Minute), map[string]any{
				"auth_mode":         OpenAIAuthModeAgentIdentity,
				"agent_runtime_id":  key.runtimeID,
				"agent_private_key": privateKey,
			})
			registerCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				registerCalls++
				_, _ = w.Write([]byte(`{"task_id":"unexpected"}`))
			}))
			defer server.Close()
			oldBase := openAIAgentIdentityAuthAPIBaseURL
			openAIAgentIdentityAuthAPIBaseURL = server.URL
			t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })
			ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)

			err := ensureAgentIdentityTaskForAccount(ctx, test.repo(), nil, &sync.Mutex{}, account, "")
			require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
			require.Zero(t, registerCalls)
			observation := quotaRecoveryCredentialTestObservation(account, account)
			require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
		})
	}
}

func TestEnsureAgentIdentityTaskQuotaReceiptPersistsTaskAfterCallerCancellation(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := quotaRecoveryCredentialTestAccount(1529, time.Now().UTC().Add(-time.Minute), map[string]any{
		"auth_mode":         OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  key.runtimeID,
		"agent_private_key": privateKey,
	})
	durable := *account
	durable.Credentials = shallowCopyMap(account.Credentials)
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Minute)
	defer deadlineCancel()
	probeCtx, cancelProbe := context.WithCancel(deadlineCtx)
	ctx, receipt, err := withQuotaRecoveryCredentialRefreshReceipt(probeCtx, account, account)
	require.NoError(t, err)
	probeDeadline, ok := ctx.Deadline()
	require.True(t, ok)
	repo := &refreshAPIAccountRepo{account: &durable}
	repo.beforeConditionalCAS = func(persistCtx context.Context, _ *refreshAPIAccountRepo, _ map[string]any) {
		persistDeadline, hasDeadline := persistCtx.Deadline()
		require.True(t, hasDeadline)
		require.True(t, persistDeadline.Equal(probeDeadline.Add(quotaRecoveryCredentialPersistTimeout)),
			"task registration must keep the active probe budget plus persistence grace")
		cancelProbe()
		<-probeCtx.Done()
		require.NoError(t, persistCtx.Err(), "task-id persistence must be detached from the canceled probe")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"task_id":"task-after-cancel"}`))
	}))
	defer server.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })
	wsInvalidator := &agentIdentityWSInvalidationRecorder{}

	err = ensureAgentIdentityTaskForAccount(ctx, repo, wsInvalidator, &sync.Mutex{}, account, "")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "task-after-cancel", durable.GetCredential("task_id"),
		"a registered task id must be durably saved even when the probe is canceled")
	require.NoError(t, repo.conditionalContextErr)
	require.Equal(t, []int64{account.ID}, wsInvalidator.accountIDs,
		"durable task replacement must invalidate websocket state before returning cancellation")
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestQuotaRecoveryPostSideEffectContextPreservesBudgetAndBoundsEarlyCancellation(t *testing.T) {
	const grace = 100 * time.Millisecond
	parentDeadline := time.Now().Add(time.Second)
	parentCtx, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	persistCtx, cancelPersist := quotaRecoveryPostSideEffectContextWithGrace(parentCtx, grace)
	defer cancelPersist()
	persistDeadline, ok := persistCtx.Deadline()
	require.True(t, ok)
	require.True(t, persistDeadline.Equal(parentDeadline.Add(grace)))

	cancelParent()
	select {
	case <-persistCtx.Done():
		t.Fatal("detached persistence was canceled without its grace period")
	case <-time.After(grace / 4):
	}
	select {
	case <-persistCtx.Done():
		require.ErrorIs(t, persistCtx.Err(), context.Canceled)
	case <-time.After(3 * grace):
		t.Fatal("detached persistence exceeded its cancellation grace period")
	}
}

func TestOpenAIQuotaAgentIdentityReceiptMismatchStopsBeforeUpstream(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(153, time.Now().UTC().Add(-time.Minute), map[string]any{
		"auth_mode":          OpenAIAuthModeAgentIdentity,
		"chatgpt_account_id": "observed-account",
	})
	durable := *account
	durable.Credentials = map[string]any{
		"auth_mode":          OpenAIAuthModeAgentIdentity,
		"chatgpt_account_id": "reauthorized-account",
	}
	durable.UpdatedAt = account.UpdatedAt.Add(time.Second)
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: &durable}}
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls++
	}))
	defer server.Close()
	service := NewOpenAIQuotaService(repo, nil, nil, newQuotaRedirectingFactory(server))
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)

	_, err := service.QueryUsageStrict(ctx, account.ID)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Zero(t, upstreamCalls, "receipt provenance must be checked before quota or reset-credit requests")
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestOpenAIQuotaAgentIdentityReceiptRejectsReauthorizationAfterPrepare(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := quotaRecoveryCredentialTestAccount(1531, time.Now().UTC().Add(-time.Minute), map[string]any{
		"auth_mode":          OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":   key.runtimeID,
		"agent_private_key":  privateKey,
		"task_id":            "observed-task",
		"chatgpt_account_id": "observed-account",
	})
	reauthorized := *account
	reauthorized.Credentials = shallowCopyMap(account.Credentials)
	reauthorized.Credentials["task_id"] = "reauthorized-task"
	reauthorized.Credentials["chatgpt_account_id"] = "reauthorized-account"
	reauthorized.UpdatedAt = account.UpdatedAt.Add(time.Second)
	repo := &refreshAPIAccountRepo{
		account: account,
		getByIDAccounts: []*Account{
			account,       // prepareUpstreamCall binds the observed generation.
			&reauthorized, // isAgentIdentityAccount sees the post-prepare edit.
			&reauthorized, // buildCodexQuotaHeaders must reject it before I/O.
		},
	}
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls++
	}))
	defer server.Close()
	service := NewOpenAIQuotaService(repo, nil, nil, newQuotaRedirectingFactory(server))
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)

	_, err := service.QueryUsageStrict(ctx, account.ID)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Equal(t, 3, repo.getByIDCalls, "the post-prepare owner reread must guard header construction")
	require.Zero(t, upstreamCalls, "reauthorized agent credentials must not be used for quota or reset-credit I/O")
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestAnthropicQuotaReceiptMismatchStopsBeforeUsageFetch(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(154, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token": "observed-token",
	})
	account.Platform = PlatformAnthropic
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	account.Credentials = map[string]any{"access_token": "reauthorized-token"}
	fetcher := &quotaRecoveryClaudeUsageFetcherStub{response: anthropicUsageForRecovery(10, 20)}
	service := &AccountUsageService{usageFetcher: fetcher}

	_, err := service.QueryAnthropicOAuthUsage(ctx, account)
	require.ErrorIs(t, err, ErrQuotaRecoveryCredentialStateChanged)
	require.Zero(t, fetcher.calls)
	observation := quotaRecoveryCredentialTestObservation(account, account)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}

func TestAnthropicQuotaReceiptDeletesStaleSharedTokenBeforeUsageFetch(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(155, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token": "new-db-token",
	})
	account.Platform = PlatformAnthropic
	cache := newClaudeTokenCacheStub()
	cache.tokens[ClaudeTokenCacheKey(account)] = "stale-cache-token"
	fetcher := &quotaRecoveryClaudeUsageFetcherStub{response: anthropicUsageForRecovery(10, 20)}
	service := &AccountUsageService{usageFetcher: fetcher, tokenCache: cache}
	ctx, _ := quotaRecoveryCredentialProbeContext(t, account, account)

	_, err := service.QueryAnthropicOAuthUsage(ctx, account)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.calls)
	require.Equal(t, "new-db-token", fetcher.opts.AccessToken)
	require.Zero(t, cache.getCalled)
	require.Zero(t, cache.setCalled)
	require.Equal(t, int32(1), cache.deleteCalled)
}

func TestAnthropicQuotaReceiptCacheDeleteFailureStopsBeforeUsageFetch(t *testing.T) {
	account := quotaRecoveryCredentialTestAccount(156, time.Now().UTC().Add(-time.Minute), map[string]any{
		"access_token": "new-db-token",
	})
	account.Platform = PlatformAnthropic
	cache := newClaudeTokenCacheStub()
	cache.deleteErr = errors.New("redis delete unavailable")
	fetcher := &quotaRecoveryClaudeUsageFetcherStub{response: anthropicUsageForRecovery(10, 20)}
	service := &AccountUsageService{usageFetcher: fetcher, tokenCache: cache}
	ctx, receipt := quotaRecoveryCredentialProbeContext(t, account, account)
	observation := quotaRecoveryCredentialTestObservation(account, account)

	_, err := service.QueryAnthropicOAuthUsage(ctx, account)
	require.Error(t, err)
	require.Zero(t, fetcher.calls)
	require.Equal(t, int32(1), cache.deleteCalled)
	require.ErrorIs(t, receipt.applyToObservation(&observation), ErrQuotaRecoveryCredentialStateChanged)
}
