//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const subscriptionRenewalRaceTimeout = 10 * time.Second

type subscriptionRenewalFixture struct {
	userID         int64
	groupID        int64
	subscriptionID int64
	initialExpiry  time.Time
}

type subscriptionRenewalResult struct {
	sub      *service.UserSubscription
	extended bool
	err      error
}

func TestSubscriptionServiceConcurrentRenewalsWaitForCommitAndAccumulateDays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionRenewalRaceTimeout)
	defer cancel()

	client := testEntClient(t)
	fixture := createSubscriptionRenewalFixture(t, client)
	serviceA := newSubscriptionRenewalService(t, client)
	serviceB := newSubscriptionRenewalService(t, client)

	txA, err := client.Tx(ctx)
	require.NoError(t, err, "begin renewal transaction A")
	t.Cleanup(func() { _ = txA.Rollback() })
	txCtxA := dbent.NewTxContext(ctx, txA)
	blockerPID := querySingleInt(t, txCtxA, txA.Client(), "SELECT pg_backend_pid()")

	first, extended, err := serviceA.AssignOrExtendSubscription(txCtxA, renewalInput(fixture))
	require.NoError(t, err, "renew subscription in transaction A")
	require.True(t, extended, "transaction A must renew the existing subscription")
	require.True(t, first.ExpiresAt.Equal(fixture.initialExpiry.AddDate(0, 0, 7)),
		"transaction A must observe its uncommitted seven-day renewal")

	resultB := renewSubscriptionAsync(ctx, serviceB, fixture)
	waitForSubscriptionRenewalBlockedBy(t, ctx, blockerPID, resultB)
	assertSubscriptionRenewalPending(t, resultB)

	require.NoError(t, txA.Commit(), "commit renewal transaction A")
	second := awaitSubscriptionRenewal(t, ctx, resultB)
	require.NoError(t, second.err, "renew subscription in transaction B")
	require.True(t, second.extended, "transaction B must renew the existing subscription")

	expectedExpiry := fixture.initialExpiry.AddDate(0, 0, 14)
	require.True(t, second.sub.ExpiresAt.Equal(expectedExpiry),
		"transaction B must extend the row version committed by transaction A")
	requirePersistedSubscriptionExpiry(t, ctx, fixture.subscriptionID, expectedExpiry)
}

func TestSubscriptionServiceConcurrentRenewalContinuesAfterRollbackAndAddsDaysOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionRenewalRaceTimeout)
	defer cancel()

	client := testEntClient(t)
	fixture := createSubscriptionRenewalFixture(t, client)
	serviceA := newSubscriptionRenewalService(t, client)
	serviceB := newSubscriptionRenewalService(t, client)

	txA, err := client.Tx(ctx)
	require.NoError(t, err, "begin renewal transaction A")
	t.Cleanup(func() { _ = txA.Rollback() })
	txCtxA := dbent.NewTxContext(ctx, txA)
	blockerPID := querySingleInt(t, txCtxA, txA.Client(), "SELECT pg_backend_pid()")

	first, extended, err := serviceA.AssignOrExtendSubscription(txCtxA, renewalInput(fixture))
	require.NoError(t, err, "renew subscription in transaction A")
	require.True(t, extended, "transaction A must renew the existing subscription")
	require.True(t, first.ExpiresAt.Equal(fixture.initialExpiry.AddDate(0, 0, 7)),
		"transaction A must observe its uncommitted seven-day renewal")

	resultB := renewSubscriptionAsync(ctx, serviceB, fixture)
	waitForSubscriptionRenewalBlockedBy(t, ctx, blockerPID, resultB)
	assertSubscriptionRenewalPending(t, resultB)

	require.NoError(t, txA.Rollback(), "roll back renewal transaction A")
	second := awaitSubscriptionRenewal(t, ctx, resultB)
	require.NoError(t, second.err, "renew subscription in transaction B")
	require.True(t, second.extended, "transaction B must renew the existing subscription")

	expectedExpiry := fixture.initialExpiry.AddDate(0, 0, 7)
	require.True(t, second.sub.ExpiresAt.Equal(expectedExpiry),
		"transaction B must renew the original row after transaction A rolls back")
	requirePersistedSubscriptionExpiry(t, ctx, fixture.subscriptionID, expectedExpiry)
}

func createSubscriptionRenewalFixture(t *testing.T, client *dbent.Client) subscriptionRenewalFixture {
	t.Helper()

	unique := time.Now().UnixNano()
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("subscription-renewal-lock-%d@example.com", unique),
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             fmt.Sprintf("subscription-renewal-lock-%d", unique),
		RateMultiplier:   1,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	initialExpiry := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, 30)
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:    user.ID,
		GroupID:   group.ID,
		StartsAt:  initialExpiry.AddDate(0, 0, -31),
		ExpiresAt: initialExpiry,
		Status:    service.SubscriptionStatusActive,
	})

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), subscriptionRenewalRaceTimeout)
		defer cancel()
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM user_subscriptions WHERE id = $1", subscription.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM groups WHERE id = $1", group.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	return subscriptionRenewalFixture{
		userID:         user.ID,
		groupID:        group.ID,
		subscriptionID: subscription.ID,
		initialExpiry:  initialExpiry,
	}
}

func newSubscriptionRenewalService(t *testing.T, client *dbent.Client) *service.SubscriptionService {
	t.Helper()

	svc := service.NewSubscriptionService(
		NewGroupRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil,
		client,
		nil,
	)
	t.Cleanup(svc.Stop)
	return svc
}

func renewalInput(fixture subscriptionRenewalFixture) *service.AssignSubscriptionInput {
	return &service.AssignSubscriptionInput{
		UserID:       fixture.userID,
		GroupID:      fixture.groupID,
		ValidityDays: 7,
	}
}

func renewSubscriptionAsync(
	ctx context.Context,
	svc *service.SubscriptionService,
	fixture subscriptionRenewalFixture,
) <-chan subscriptionRenewalResult {
	resultCh := make(chan subscriptionRenewalResult, 1)
	go func() {
		sub, extended, err := svc.AssignOrExtendSubscription(ctx, renewalInput(fixture))
		resultCh <- subscriptionRenewalResult{sub: sub, extended: extended, err: err}
	}()
	return resultCh
}

func waitForSubscriptionRenewalBlockedBy(
	t *testing.T,
	ctx context.Context,
	blockerPID int,
	resultCh <-chan subscriptionRenewalResult,
) {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			t.Fatalf("transaction B completed before waiting on transaction A's row lock: %+v", result)
		default:
		}

		var blocked bool
		err := integrationDB.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity AS waiter
				WHERE waiter.datname = current_database()
				  AND waiter.state = 'active'
				  AND waiter.wait_event_type = 'Lock'
				  AND $1::integer = ANY(pg_blocking_pids(waiter.pid))
				  AND waiter.query ILIKE '%user_subscriptions%'
			)
		`, blockerPID).Scan(&blocked)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("timed out observing PostgreSQL renewal lock wait: %v", ctx.Err())
			}
			require.NoError(t, err, "inspect PostgreSQL renewal lock wait")
		}
		if blocked {
			return
		}

		select {
		case result := <-resultCh:
			t.Fatalf("transaction B completed before waiting on transaction A's row lock: %+v", result)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("transaction B did not wait on transaction A's subscription row lock: %v", ctx.Err())
		}
	}
}

func assertSubscriptionRenewalPending(t *testing.T, resultCh <-chan subscriptionRenewalResult) {
	t.Helper()

	select {
	case result := <-resultCh:
		t.Fatalf("transaction B completed while transaction A still held the row lock: %+v", result)
	default:
	}
}

func awaitSubscriptionRenewal(
	t *testing.T,
	ctx context.Context,
	resultCh <-chan subscriptionRenewalResult,
) subscriptionRenewalResult {
	t.Helper()

	select {
	case result := <-resultCh:
		return result
	case <-ctx.Done():
		t.Fatalf("transaction B did not complete after transaction A released the row lock: %v", ctx.Err())
		return subscriptionRenewalResult{}
	}
}

func requirePersistedSubscriptionExpiry(
	t *testing.T,
	ctx context.Context,
	subscriptionID int64,
	expected time.Time,
) {
	t.Helper()

	var persisted time.Time
	err := integrationDB.QueryRowContext(
		ctx,
		"SELECT expires_at FROM user_subscriptions WHERE id = $1",
		subscriptionID,
	).Scan(&persisted)
	require.NoError(t, err, "query persisted subscription expiry")
	require.True(t, persisted.Equal(expected),
		"persisted expiry mismatch: got %s, want %s", persisted, expected)
}
