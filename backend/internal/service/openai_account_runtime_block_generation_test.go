//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRuntimeBlockClearIfGenerationUnchanged(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc.BlockAccountScheduling(account, time.Now().Add(time.Hour), "old-429")

	generation := svc.AccountSchedulingBlockGeneration(account.ID)
	require.NotZero(t, generation)
	require.True(t, svc.ClearAccountSchedulingBlockIfGeneration(account.ID, generation))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlockClearIfGenerationPreservesNewerBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc.BlockAccountScheduling(account, time.Now().Add(time.Hour), "old-429")
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	svc.BlockAccountScheduling(account, time.Now().Add(2*time.Hour), "new-429")
	require.False(t, svc.ClearAccountSchedulingBlockIfGeneration(account.ID, observed))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlockGenerationGuardSerializesNewBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 903, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc.BlockAccountScheduling(account, time.Now().Add(time.Hour), "old-429")
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	release, matched := svc.GuardAccountSchedulingBlockGeneration(account.ID, observed)
	require.True(t, matched)
	require.NotNil(t, release)

	blocked := make(chan struct{})
	go func() {
		svc.BlockAccountScheduling(account, time.Now().Add(2*time.Hour), "new-429")
		close(blocked)
	}()

	select {
	case <-blocked:
		t.Fatal("new block bypassed the quota-recovery generation guard")
	case <-time.After(20 * time.Millisecond):
	}
	release()

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("new block did not proceed after releasing the generation guard")
	}
	require.NotEqual(t, observed, svc.AccountSchedulingBlockGeneration(account.ID))
}

func TestOpenAIRuntimeQuotaRecoveryClearsMatchingQuotaBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 904, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(time.Hour)
	svc.BlockAccountScheduling(account, resetAt, "429")
	limitedAt := time.Now()
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	release, matched := svc.GuardAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	)
	require.True(t, matched)
	require.NotNil(t, release)
	release()

	require.True(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeQuotaRecoveryGuardSerializesNewBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 910, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(time.Hour)
	svc.BlockAccountScheduling(account, resetAt, "429")
	limitedAt := time.Now()
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	release, matched := svc.GuardAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	)
	require.True(t, matched)
	require.NotNil(t, release)

	attempted := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		close(attempted)
		svc.BlockAccountScheduling(account, resetAt.Add(time.Hour), "429")
		close(blocked)
	}()
	<-attempted

	select {
	case <-blocked:
		release()
		t.Fatal("new block bypassed the quota-recovery CAS guard")
	case <-time.After(20 * time.Millisecond):
	}
	release()

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("new block did not proceed after releasing the quota-recovery CAS guard")
	}
	require.NotEqual(t, observed, svc.AccountSchedulingBlockGeneration(account.ID))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeQuotaRecoveryPreservesNonQuotaBlocks(t *testing.T) {
	for _, reason := range []string{"403", "grok upstream temporary error"} {
		t.Run(reason, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := &Account{ID: 905, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			resetAt := time.Now().Add(time.Hour)
			svc.BlockAccountScheduling(account, resetAt, reason)
			limitedAt := time.Now()
			observed := svc.AccountSchedulingBlockGeneration(account.ID)

			release, matched := svc.GuardAccountSchedulingBlockForQuotaRecovery(
				account.ID,
				observed,
				limitedAt,
				resetAt,
			)
			require.False(t, matched)
			require.Nil(t, release)
			require.False(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
				account.ID,
				observed,
				limitedAt,
				resetAt,
			))
			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestOpenAIRuntimeQuotaRecoveryPreservesMixedQuotaAndNonQuotaBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 906, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(time.Hour)
	svc.BlockAccountScheduling(account, resetAt, "429")
	svc.BlockAccountScheduling(account, resetAt.Add(-time.Minute), "403")
	limitedAt := time.Now()
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	require.False(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeQuotaRecoveryPreservesNonQuotaBlockInstalledBeforeQuota(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 909, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(time.Hour)
	svc.BlockAccountScheduling(account, time.Now().Add(time.Minute), "transport_error")
	svc.BlockAccountScheduling(account, resetAt, "429")
	limitedAt := time.Now()
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	require.False(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeQuotaRecoveryAllowsClearAfterShorterNonQuotaBlockExpires(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 908, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(time.Hour)
	svc.BlockAccountScheduling(account, resetAt, "429")
	svc.BlockAccountScheduling(account, time.Now().Add(time.Minute), "403")
	limitedAt := time.Now()
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	metadataValue, ok := svc.openaiAccountRuntimeBlockMetadata.Load(account.ID)
	require.True(t, ok)
	metadata, ok := metadataValue.(openAIAccountRuntimeBlockMetadata)
	require.True(t, ok)
	metadata.nonQuotaUntil = time.Now().Add(-time.Second)
	svc.openaiAccountRuntimeBlockMetadata.Store(account.ID, metadata)

	require.True(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		limitedAt,
		resetAt,
	))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeQuotaRecoveryPreservesNewQuotaBlockAfterCandidateRead(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 907, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	candidateLimitedAt := time.Now().Add(-time.Minute)
	candidateResetAt := time.Now().Add(time.Hour)

	// The candidate was read before this 429. Sampling the generation after the
	// new block is insufficient by itself; the observation timestamp must fence it.
	svc.BlockAccountScheduling(account, candidateResetAt, "429")
	observed := svc.AccountSchedulingBlockGeneration(account.ID)

	release, matched := svc.GuardAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		candidateLimitedAt,
		candidateResetAt,
	)
	require.False(t, matched)
	require.Nil(t, release)
	require.False(t, svc.ClearAccountSchedulingBlockForQuotaRecovery(
		account.ID,
		observed,
		candidateLimitedAt,
		candidateResetAt,
	))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
