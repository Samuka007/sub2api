package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type accountCredentialsUpdater interface {
	UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error
}

type quotaRecoveryCredentialRefreshReceiptContextKey struct{}

const quotaRecoveryCredentialPersistTimeout = 8 * time.Second

// quotaRecoveryPostSideEffectContext gives an irreversible provider side
// effect enough time to reach durable storage without remaining tied to the
// probe cancellation boundary. An active probe keeps its full persistence
// budget plus a short grace period; an early cancellation starts that grace
// period immediately so shutdown is still bounded.
func quotaRecoveryPostSideEffectContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return quotaRecoveryPostSideEffectContextWithGrace(ctx, quotaRecoveryCredentialPersistTimeout)
}

func quotaRecoveryPostSideEffectContextWithGrace(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if grace <= 0 {
		grace = quotaRecoveryCredentialPersistTimeout
	}
	detached := context.WithoutCancel(ctx)
	now := time.Now()
	deadline, hasDeadline := ctx.Deadline()
	if ctx.Err() != nil || !hasDeadline || !deadline.After(now) {
		return context.WithTimeout(detached, grace)
	}

	persistCtx, cancelPersist := context.WithDeadline(detached, deadline.Add(grace))
	var timerMu sync.Mutex
	var graceTimer *time.Timer
	stopped := false
	stopCancellationGrace := context.AfterFunc(ctx, func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if stopped {
			return
		}
		graceTimer = time.AfterFunc(grace, cancelPersist)
	})
	return persistCtx, func() {
		stopCancellationGrace()
		timerMu.Lock()
		stopped = true
		if graceTimer != nil {
			graceTimer.Stop()
		}
		timerMu.Unlock()
		cancelPersist()
	}
}

// quotaRecoveryCredentialRefreshReceipt is deliberately context-scoped and
// package-private. Only the quota recovery runner should create one. It binds
// every credential write made while probing (OAuth rotation, task_id repair,
// and similar helpers) to one exact credential-owner generation.
type quotaRecoveryCredentialRefreshReceipt struct {
	mu              sync.Mutex
	targetAccountID int64
	initialOwner    AccountCredentialSnapshot
	owner           AccountCredentialSnapshot
	invalid         bool
}

func withQuotaRecoveryCredentialRefreshReceipt(
	ctx context.Context,
	target *Account,
	credentialOwner *Account,
) (context.Context, *quotaRecoveryCredentialRefreshReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if target == nil || target.ID <= 0 || credentialOwner == nil || credentialOwner.ID <= 0 {
		return ctx, nil, fmt.Errorf("%w: invalid account", ErrQuotaRecoveryCredentialStateChanged)
	}
	if target.Type != AccountTypeOAuth || credentialOwner.Type != AccountTypeOAuth ||
		target.Platform != credentialOwner.Platform ||
		(target.Platform != PlatformOpenAI && target.Platform != PlatformAnthropic) {
		return ctx, nil, fmt.Errorf("%w: invalid credential owner identity", ErrQuotaRecoveryCredentialStateChanged)
	}
	if credentialOwner.IsShadow() {
		return ctx, nil, fmt.Errorf("%w: credential owner is a shadow", ErrQuotaRecoveryCredentialStateChanged)
	}
	if target.ID == credentialOwner.ID {
		if target.IsShadow() {
			return ctx, nil, fmt.Errorf("%w: shadow target cannot own credentials", ErrQuotaRecoveryCredentialStateChanged)
		}
	} else if target.ParentAccountID == nil || *target.ParentAccountID != credentialOwner.ID {
		return ctx, nil, fmt.Errorf("%w: credential owner relationship changed", ErrQuotaRecoveryCredentialStateChanged)
	}
	if target.UpdatedAt.IsZero() || credentialOwner.UpdatedAt.IsZero() {
		return ctx, nil, fmt.Errorf("%w: missing account generation", ErrQuotaRecoveryCredentialStateChanged)
	}
	if _, err := json.Marshal(credentialOwner.Credentials); err != nil {
		return ctx, nil, fmt.Errorf("%w: invalid credential document: %v", ErrQuotaRecoveryCredentialStateChanged, err)
	}

	ownerSnapshot := accountCredentialSnapshot(credentialOwner)
	receipt := &quotaRecoveryCredentialRefreshReceipt{
		targetAccountID: target.ID,
		initialOwner:    cloneAccountCredentialSnapshot(ownerSnapshot),
		owner:           ownerSnapshot,
	}
	return context.WithValue(ctx, quotaRecoveryCredentialRefreshReceiptContextKey{}, receipt), receipt, nil
}

func quotaRecoveryCredentialRefreshReceiptFromContext(ctx context.Context) *quotaRecoveryCredentialRefreshReceipt {
	if ctx == nil {
		return nil
	}
	receipt, _ := ctx.Value(quotaRecoveryCredentialRefreshReceiptContextKey{}).(*quotaRecoveryCredentialRefreshReceipt)
	return receipt
}

func accountCredentialSnapshot(account *Account) AccountCredentialSnapshot {
	if account == nil {
		return AccountCredentialSnapshot{}
	}
	return AccountCredentialSnapshot{
		AccountID:             account.ID,
		Credentials:           cloneCredentialDocument(account.Credentials),
		UpdatedAt:             account.UpdatedAt.UTC(),
		ProxyID:               cloneCredentialInt64Pointer(account.ProxyID),
		ProxyFallbackOriginID: cloneCredentialInt64Pointer(account.ProxyFallbackOriginID),
		Platform:              account.Platform,
		Type:                  account.Type,
		ParentAccountID:       cloneCredentialInt64Pointer(account.ParentAccountID),
		Status:                account.Status,
		Schedulable:           account.Schedulable,
		QuotaDimension:        account.QuotaDimensionOrDefault(),
	}
}

func cloneAccountCredentialSnapshot(snapshot AccountCredentialSnapshot) AccountCredentialSnapshot {
	snapshot.Credentials = cloneCredentialDocument(snapshot.Credentials)
	snapshot.ProxyID = cloneCredentialInt64Pointer(snapshot.ProxyID)
	snapshot.ProxyFallbackOriginID = cloneCredentialInt64Pointer(snapshot.ProxyFallbackOriginID)
	snapshot.ParentAccountID = cloneCredentialInt64Pointer(snapshot.ParentAccountID)
	return snapshot
}

func cloneCredentialDocument(credentials map[string]any) map[string]any {
	raw, err := json.Marshal(credentials)
	if err != nil {
		return shallowCopyMap(credentials)
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil || cloned == nil {
		return map[string]any{}
	}
	return cloned
}

func cloneCredentialInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func accountMatchesCredentialSnapshot(account *Account, expected AccountCredentialSnapshot) bool {
	if account == nil ||
		account.ID != expected.AccountID ||
		!account.UpdatedAt.Equal(expected.UpdatedAt) ||
		!equalInt64Pointers(account.ProxyID, expected.ProxyID) ||
		!equalInt64Pointers(account.ProxyFallbackOriginID, expected.ProxyFallbackOriginID) ||
		account.Platform != expected.Platform ||
		account.Type != expected.Type ||
		!equalInt64Pointers(account.ParentAccountID, expected.ParentAccountID) ||
		account.Status != expected.Status ||
		account.Schedulable != expected.Schedulable ||
		account.QuotaDimensionOrDefault() != expected.QuotaDimension {
		return false
	}
	return credentialDocumentsEqual(account.Credentials, expected.Credentials)
}

func credentialDocumentsEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func equalInt64Pointers(left, right *int64) bool {
	return (left == nil && right == nil) ||
		(left != nil && right != nil && *left == *right)
}

func validateQuotaRecoveryCredentialOwner(ctx context.Context, account *Account) error {
	receipt := quotaRecoveryCredentialRefreshReceiptFromContext(ctx)
	if receipt == nil {
		return nil
	}
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if receipt.invalid {
		return ErrQuotaRecoveryCredentialStateChanged
	}
	if !accountMatchesCredentialSnapshot(account, receipt.owner) {
		return ErrQuotaRecoveryCredentialStateChanged
	}
	return nil
}

func invalidateQuotaRecoveryCredentialRefreshReceipt(ctx context.Context) bool {
	receipt := quotaRecoveryCredentialRefreshReceiptFromContext(ctx)
	if receipt == nil {
		return false
	}
	receipt.mu.Lock()
	receipt.invalid = true
	receipt.mu.Unlock()
	return true
}

func deleteQuotaRecoveryCachedAccessToken(
	ctx context.Context,
	cache GeminiTokenCache,
	cacheKey string,
	account *Account,
) error {
	if quotaRecoveryCredentialRefreshReceiptFromContext(ctx) == nil || cache == nil {
		return nil
	}
	cleanupParent := context.Background()
	if ctx != nil {
		cleanupParent = context.WithoutCancel(ctx)
	}
	cleanupCtx, cancel := context.WithTimeout(cleanupParent, defaultRefreshPostPersistCleanupTimeout)
	defer cancel()
	if err := cache.DeleteAccessToken(cleanupCtx, cacheKey); err != nil {
		invalidateQuotaRecoveryCredentialRefreshReceipt(ctx)
		accountID := int64(0)
		platform := ""
		if account != nil {
			accountID = account.ID
			platform = account.Platform
		}
		slog.Error("quota_recovery_access_token_cache_delete_failed",
			"account_id", accountID,
			"platform", platform,
			"error", err,
		)
		return fmt.Errorf("delete stale quota recovery access-token cache: %w", err)
	}
	return nil
}

func (r *quotaRecoveryCredentialRefreshReceipt) applyToObservation(observation *QuotaRecoveryObservation) error {
	if r == nil || observation == nil {
		return fmt.Errorf("%w: missing credential refresh receipt", ErrQuotaRecoveryCredentialStateChanged)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invalid {
		return fmt.Errorf("%w: credential owner changed during provider call", ErrQuotaRecoveryCredentialStateChanged)
	}
	if observation.AccountID != r.targetAccountID ||
		!observationMatchesCredentialSnapshot(observation, r.initialOwner, r.targetAccountID) {
		return fmt.Errorf("%w: receipt identity mismatch", ErrQuotaRecoveryCredentialStateChanged)
	}

	applyCredentialSnapshotToObservation(observation, r.owner, r.targetAccountID)
	return nil
}

func observationMatchesCredentialSnapshot(
	observation *QuotaRecoveryObservation,
	snapshot AccountCredentialSnapshot,
	targetAccountID int64,
) bool {
	if observation == nil ||
		observation.CredentialOwnerID != snapshot.AccountID ||
		!observation.CredentialOwnerUpdatedAt.Equal(snapshot.UpdatedAt) ||
		!credentialDocumentsEqual(observation.CredentialOwnerCredentials, snapshot.Credentials) ||
		!equalInt64Pointers(observation.CredentialOwnerProxyID, snapshot.ProxyID) ||
		!equalInt64Pointers(observation.CredentialOwnerProxyFallbackOriginID, snapshot.ProxyFallbackOriginID) ||
		observation.CredentialOwnerPlatform != snapshot.Platform ||
		observation.CredentialOwnerType != snapshot.Type ||
		!equalInt64Pointers(observation.CredentialOwnerParentAccountID, snapshot.ParentAccountID) ||
		observation.CredentialOwnerStatus != snapshot.Status ||
		observation.CredentialOwnerSchedulable != snapshot.Schedulable ||
		observation.CredentialOwnerQuotaDimension != snapshot.QuotaDimension {
		return false
	}
	return targetAccountID != snapshot.AccountID || observation.AccountUpdatedAt.Equal(snapshot.UpdatedAt)
}

func applyCredentialSnapshotToObservation(
	observation *QuotaRecoveryObservation,
	snapshot AccountCredentialSnapshot,
	targetAccountID int64,
) {
	observation.CredentialOwnerID = snapshot.AccountID
	observation.CredentialOwnerUpdatedAt = snapshot.UpdatedAt
	observation.CredentialOwnerCredentials = cloneCredentialDocument(snapshot.Credentials)
	observation.CredentialOwnerProxyID = cloneCredentialInt64Pointer(snapshot.ProxyID)
	observation.CredentialOwnerProxyFallbackOriginID = cloneCredentialInt64Pointer(snapshot.ProxyFallbackOriginID)
	observation.CredentialOwnerPlatform = snapshot.Platform
	observation.CredentialOwnerType = snapshot.Type
	observation.CredentialOwnerParentAccountID = cloneCredentialInt64Pointer(snapshot.ParentAccountID)
	observation.CredentialOwnerStatus = snapshot.Status
	observation.CredentialOwnerSchedulable = snapshot.Schedulable
	observation.CredentialOwnerQuotaDimension = snapshot.QuotaDimension
	if targetAccountID == snapshot.AccountID {
		observation.AccountUpdatedAt = snapshot.UpdatedAt
	}
}

func persistAccountCredentials(ctx context.Context, repo AccountRepository, account *Account, credentials map[string]any) error {
	receipt := quotaRecoveryCredentialRefreshReceiptFromContext(ctx)
	if receipt != nil {
		if repo == nil || account == nil {
			invalidateQuotaRecoveryCredentialRefreshReceipt(ctx)
			return ErrQuotaRecoveryCredentialCASUnavailable
		}
		conditionalRepo, ok := repo.(AccountCredentialConditionalUpdateRepository)
		if !ok {
			invalidateQuotaRecoveryCredentialRefreshReceipt(ctx)
			return ErrQuotaRecoveryCredentialCASUnavailable
		}

		receipt.mu.Lock()
		defer receipt.mu.Unlock()
		if !accountMatchesCredentialSnapshot(account, receipt.owner) {
			receipt.invalid = true
			return ErrQuotaRecoveryCredentialStateChanged
		}
		expected := cloneAccountCredentialSnapshot(receipt.owner)
		persistedCredentials := cloneCredentialDocument(credentials)
		result, applied, err := conditionalRepo.UpdateCredentialsIfUnchanged(
			ctx,
			expected,
			persistedCredentials,
		)
		if err == nil && applied {
			return applyQuotaRecoveryCredentialPersistResult(account, receipt, persistedCredentials, result)
		}
		return reconcileQuotaRecoveryCredentialPersist(
			ctx,
			repo,
			conditionalRepo,
			account,
			receipt,
			expected,
			persistedCredentials,
			err,
		)
	}

	if repo == nil || account == nil {
		return nil
	}

	// 安全不变量:spark 影子账号恒不持凭据(凭据透传母账号)。这是凭据写入的唯一汇聚点
	// (token 刷新 / 订阅补全 / CRS 创建后刷新等全部经此),在此对影子早返 no-op 是
	// defense-in-depth——即便某条上游路径漏判,也不会把凭据落到影子行(外审第6轮 P1)。
	if account.IsCredentialShadow() {
		slog.Warn("skip persisting credentials to spark shadow account",
			"account_id", account.ID, "parent_id", *account.ParentAccountID)
		return nil
	}

	account.Credentials = shallowCopyMap(credentials)
	if updater, ok := any(repo).(accountCredentialsUpdater); ok {
		return updater.UpdateCredentials(ctx, account.ID, account.Credentials)
	}
	return updateAccountWithNotesIntent(ctx, repo, account, false)
}

func applyQuotaRecoveryCredentialPersistResult(
	account *Account,
	receipt *quotaRecoveryCredentialRefreshReceipt,
	credentials map[string]any,
	result AccountCredentialConditionalUpdateResult,
) error {
	if result.PreviousUpdatedAt.IsZero() || result.UpdatedAt.IsZero() ||
		!result.UpdatedAt.After(result.PreviousUpdatedAt) {
		receipt.invalid = true
		return fmt.Errorf("%w: conditional update omitted a monotonic durable generation", ErrQuotaRecoveryCredentialStateChanged)
	}
	previousReceiptUpdatedAt := receipt.owner.UpdatedAt
	updatedAt := result.UpdatedAt.UTC()
	account.Credentials = cloneCredentialDocument(credentials)
	account.UpdatedAt = updatedAt
	receipt.owner.Credentials = cloneCredentialDocument(credentials)
	receipt.owner.UpdatedAt = updatedAt
	if !result.PreviousUpdatedAt.Equal(previousReceiptUpdatedAt) {
		receipt.invalid = true
	}
	return nil
}

func reconcileQuotaRecoveryCredentialPersist(
	ctx context.Context,
	repo AccountRepository,
	conditionalRepo AccountCredentialConditionalUpdateRepository,
	account *Account,
	receipt *quotaRecoveryCredentialRefreshReceipt,
	expected AccountCredentialSnapshot,
	credentials map[string]any,
	firstErr error,
) error {
	if _, ok := ctx.Deadline(); !ok {
		receipt.invalid = true
		return fmt.Errorf("%w: credential persistence reconciliation requires a deadline", ErrQuotaRecoveryCredentialStateChanged)
	}
	lastErr := firstErr
	if lastErr == nil {
		lastErr = ErrQuotaRecoveryCredentialStateChanged
	}
	backoff := 50 * time.Millisecond
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			receipt.invalid = true
			return errors.Join(ErrQuotaRecoveryCredentialStateChanged, lastErr, ctxErr)
		}

		durable, readErr := repo.GetByID(ctx, expected.AccountID)
		if readErr == nil && durable == nil {
			readErr = errors.New("account not found")
		}
		if readErr == nil {
			switch {
			case accountMatchesCredentialDocumentAndIdentity(durable, expected, credentials):
				receipt.invalid = true
				return advanceQuotaRecoveryReceiptFromDurable(account, receipt, durable)
			case !accountMatchesCredentialDocumentAndIdentity(durable, expected, expected.Credentials):
				receipt.invalid = true
				return ErrQuotaRecoveryCredentialStateChanged
			default:
				retryExpected := accountCredentialSnapshot(durable)
				result, applied, updateErr := conditionalRepo.UpdateCredentialsIfUnchanged(ctx, retryExpected, credentials)
				if updateErr == nil && applied {
					return applyQuotaRecoveryCredentialPersistResult(account, receipt, credentials, result)
				}
				if updateErr != nil {
					lastErr = updateErr
				} else {
					lastErr = ErrQuotaRecoveryCredentialStateChanged
				}
			}
		} else {
			lastErr = readErr
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			receipt.invalid = true
			return errors.Join(ErrQuotaRecoveryCredentialStateChanged, lastErr, ctx.Err())
		case <-timer.C:
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
			if backoff > 500*time.Millisecond {
				backoff = 500 * time.Millisecond
			}
		}
	}
}

func accountMatchesCredentialDocumentAndIdentity(
	account *Account,
	expected AccountCredentialSnapshot,
	credentials map[string]any,
) bool {
	return account != nil &&
		account.ID == expected.AccountID &&
		account.Platform == expected.Platform &&
		account.Type == expected.Type &&
		equalInt64Pointers(account.ParentAccountID, expected.ParentAccountID) &&
		account.QuotaDimensionOrDefault() == expected.QuotaDimension &&
		credentialDocumentsEqual(account.Credentials, credentials)
}

func advanceQuotaRecoveryReceiptFromDurable(
	account *Account,
	receipt *quotaRecoveryCredentialRefreshReceipt,
	durable *Account,
) error {
	if durable == nil || durable.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: durable credential generation is missing", ErrQuotaRecoveryCredentialStateChanged)
	}
	account.Credentials = cloneCredentialDocument(durable.Credentials)
	account.UpdatedAt = durable.UpdatedAt.UTC()
	receipt.owner = accountCredentialSnapshot(durable)
	return nil
}

// sparkShadowAllowedCredentialKeys 是 spark 影子账号唯一可写的凭据键集合(仅模型映射)。
// 校验(isAllowed)与 sanitize 共用此单一来源,避免两处独立硬编码列表漂移。
var sparkShadowAllowedCredentialKeys = map[string]struct{}{
	"model_mapping":         {},
	"compact_model_mapping": {},
}

func isAllowedSparkShadowCredentialsUpdate(credentials map[string]any) bool {
	if credentials == nil {
		return true
	}
	for key := range credentials {
		if _, ok := sparkShadowAllowedCredentialKeys[key]; !ok {
			return false
		}
	}
	return true
}

func sanitizeSparkShadowCredentials(credentials map[string]any) map[string]any {
	if len(credentials) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(sparkShadowAllowedCredentialKeys))
	for key := range sparkShadowAllowedCredentialKeys {
		if value, ok := credentials[key]; ok && value != nil {
			out[key] = value
		}
	}
	return out
}
