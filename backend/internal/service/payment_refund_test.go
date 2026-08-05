//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateRefundRequestRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ORDER").
		SetOutTradeNo("sub2_refund_legacy_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	_, err = svc.validateRefundRequest(ctx, order.ID, user.ID)
	require.Error(t, err)
	require.Equal(t, "USER_REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepareRefundRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy-admin@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-admin-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-admin-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(188).
		SetPayAmount(188).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ADMIN-ORDER").
		SetOutTradeNo("sub2_refund_legacy_admin_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-admin-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepDeductBalanceRequiresForceWhenBalanceIsInsufficient(t *testing.T) {
	for _, tc := range []struct {
		name        string
		balance     float64
		force       bool
		wantDeduct  float64
		wantWarning bool
	}{
		{name: "insufficient balance", balance: 40, wantWarning: true},
		{name: "forced insufficient balance", balance: 40, force: true, wantDeduct: 40},
		{name: "equal balance", balance: 100, wantDeduct: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &RefundPlan{RefundAmount: 100}
			svc := &PaymentService{userRepo: &mockUserRepo{getByIDUser: &User{Balance: tc.balance}}}

			result := svc.prepDeduct(context.Background(), &dbent.PaymentOrder{
				UserID:    1,
				OrderType: payment.OrderTypeBalance,
			}, plan, tc.force)

			if tc.wantWarning {
				require.NotNil(t, result)
				require.False(t, result.Success)
				require.True(t, result.RequireForce)
				require.Equal(t, "user balance is insufficient for deduction, use force", result.Warning)
				require.Zero(t, plan.BalanceToDeduct)
				return
			}
			require.Nil(t, result)
			require.Equal(t, payment.DeductionTypeBalance, plan.DeductionType)
			require.Equal(t, tc.wantDeduct, plan.BalanceToDeduct)
		})
	}
}

func TestExecuteRefundUsesActualAvailableBalanceDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-execute-clamp@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-execute-clamp").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-EXECUTE-CLAMP").
		SetOutTradeNo("refund_execute_clamp").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	repo := &mockUserRepo{
		reserveRefundBalanceFn: func(_ context.Context, id int64, amount float64) (float64, error) {
			require.Equal(t, user.ID, id)
			require.Equal(t, 100.0, amount)
			return 25, nil
		},
		captureRefundBalanceFn: func(_ context.Context, id int64, amount float64) error {
			require.Equal(t, user.ID, id)
			require.Equal(t, 25.0, amount)
			return nil
		},
	}
	plan := &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100,
		Reason: "concurrent spend", Force: true, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 100,
	}

	result, err := (&PaymentService{entClient: client, userRepo: repo}).ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 25.0, plan.BalanceToDeduct)
	require.Equal(t, 25.0, result.BalanceDeducted)
	audit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, audit.Detail, `"balanceDeducted":25`)
}
func TestExecuteRefundRetainsReservationAndRejectsAmbiguousGatewayRetry(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "gateway-retry")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).Save(ctx)
	require.NoError(t, err)

	reserves := 0
	releases := 0
	repo := &mockUserRepo{
		reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) {
			reserves++
			return 100, nil
		},
		releaseRefundBalanceFn: func(context.Context, int64, float64) error {
			releases++
			return nil
		},
	}
	gatewayCalls := 0
	svc := &PaymentService{entClient: client, userRepo: repo, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, &refundProviderTestDouble{
		refundFn: func(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
			gatewayCalls++
			return nil, errors.New("injected gateway timeout")
		},
	})
	defer restore()
	plan := svc.refundFinalizePlan(order)

	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "reservation was retained")
	require.Equal(t, refundResultStatePending, result.State)

	result, err = svc.ExecuteRefund(ctx, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	require.Equal(t, 1, gatewayCalls)
	require.Equal(t, 1, reserves)
	require.Zero(t, releases)
	require.Equal(t, 100.0, plan.BalanceToDeduct)

	reservedAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ(refundBalanceReservedAction)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reservedAudits)
}

func TestPrepareRefundReturnsStructuredPendingStateForInProgressOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "prepare-refunding-state")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	plan, result, err := svc.PrepareRefund(ctx, order.ID, 100, "retry", false, true)
	require.NoError(t, err)
	require.Nil(t, plan)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, refundResultStatePending, result.State)
}

func TestQueryAndFinalizeRefundRecoversRefundingWithoutReissuingGatewayRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refunding-query-recovery")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)

	refundCalls := 0
	queryCalls := 0
	repo := &mockUserRepo{
		reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) { return 100, nil },
		captureRefundBalanceFn: func(context.Context, int64, float64) error { return nil },
	}
	svc := &PaymentService{entClient: client, userRepo: repo, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundProviderTestDouble: refundProviderTestDouble{refundFn: func(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
			refundCalls++
			return &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, nil
		}},
		queryFn: func(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
			queryCalls++
			return &payment.RefundResponse{RefundID: "rf_recovered", Status: payment.ProviderStatusSuccess}, nil
		},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, refundCalls)
	require.Equal(t, 1, queryCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
}

func TestExecuteRefundReleasesReservationOnDefinitiveGatewayFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "gateway-failed")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).Save(ctx)
	require.NoError(t, err)

	reserves := 0
	gatewayCalls := 0
	releases := 0
	repo := &mockUserRepo{
		reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) {
			reserves++
			return 100, nil
		},
		releaseRefundBalanceFn: func(_ context.Context, _ int64, amount float64) error {
			releases++
			require.Equal(t, 100.0, amount)
			return nil
		},
	}
	svc := &PaymentService{entClient: client, userRepo: repo, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, &refundProviderTestDouble{
		refundFn: func(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
			gatewayCalls++
			return &payment.RefundResponse{Status: payment.ProviderStatusFailed}, nil
		},
	})
	defer restore()

	result, err := svc.ExecuteRefund(ctx, svc.refundFinalizePlan(order))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, reserves)
	require.Equal(t, 1, releases)
	require.Equal(t, 1, gatewayCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
}
func TestPrepareRefundRestoresActiveReservationAndPersistedIntent(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "prepare-active-reservation")
	_, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction(refundBalanceReservedAction).
		SetOperator("admin").
		SetDetail(`{"balanceReserved":75}`).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{}}
	plan, early, err := svc.PrepareRefund(ctx, order.ID, 999, "changed reason", true, true)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NotNil(t, plan)
	require.Equal(t, order.RefundAmount, plan.RefundAmount)
	require.Equal(t, "pending refund", plan.Reason)
	require.Equal(t, payment.DeductionTypeBalance, plan.DeductionType)
	require.Equal(t, 75.0, plan.BalanceToDeduct)
	require.Equal(t, order.ForceRefund, plan.Force)
}

func TestGwRefundRejectsAlipayMerchantIdentitySnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-snapshot-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-snapshot-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-mismatch-instance").
		SetConfig(encryptWebhookProviderConfig(t, map[string]string{
			"appId":      "runtime-alipay-app",
			"privateKey": "runtime-private-key",
		})).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SNAPSHOT-MISMATCH-ORDER").
		SetOutTradeNo("sub2_refund_snapshot_mismatch_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-snapshot-mismatch").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"merchant_app_id":      "expected-alipay-app",
		}).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:    client,
		loadBalancer: newWebhookProviderTestLoadBalancer(client),
	}

	_, err = svc.preflightRefundProvider(ctx, &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.Amount,
		Reason:        "snapshot mismatch",
	})
	require.ErrorContains(t, err, "alipay app_id mismatch")
}

func TestCalculateGatewayRefundAmountUsesCurrencyPrecision(t *testing.T) {
	require.InDelta(t, 6.173, calculateGatewayRefundAmount(100, 12.345, 50, "KWD"), 1e-12)
	require.InDelta(t, 12.345, calculateGatewayRefundAmount(100, 12.345, 100, "KWD"), 1e-12)
	require.InDelta(t, 52, calculateGatewayRefundAmount(100, 103, 50, "JPY"), 1e-12)
}

func TestFormatGatewayRefundAmountUsesOrderCurrency(t *testing.T) {
	order := &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			"currency": "KWD",
		},
	}

	require.Equal(t, "12.345", formatGatewayRefundAmount(12.345, order))
}

func TestValidateRefundProviderResponseAcceptsPending(t *testing.T) {
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusPending}))
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusSuccess}))
	require.Error(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusFailed}))
	require.Error(t, validateRefundProviderResponse(nil))
}

func TestFinishRefundPendingDefersDeductionUntilFinalConfirmation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-pending-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-PENDING-ORDER").
		SetOutTradeNo("sub2_refund_pending_order").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefunding).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{}
	userRepo.updateBalanceFn = func(context.Context, int64, float64) error {
		t.Fatal("pending refund must not deduct or roll back balance before final confirmation")
		return nil
	}
	svc := &PaymentService{
		entClient: client,
		userRepo:  userRepo,
	}
	plan := &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    40,
		GatewayAmount:   40,
		Reason:          "gateway accepted but not final",
		Force:           true,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: 40,
	}

	result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "pending confirmation")
	require.Equal(t, 40.0, plan.BalanceToDeduct)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Equal(t, 40.0, reloaded.RefundAmount)
	require.NotNil(t, reloaded.RefundReason)
	require.Equal(t, "gateway accepted but not final", *reloaded.RefundReason)
	require.Nil(t, reloaded.RefundAt)

	pendingAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, pendingAudits)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, successAudits)
}

func TestFinishRefundSuccessStatusesFinalize(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)

			user, err := client.User.Create().
				SetEmail("refund-success-" + status + "@example.com").
				SetPasswordHash("hash").
				SetUsername("refund-success-" + status).
				Save(ctx)
			require.NoError(t, err)

			order, err := client.PaymentOrder.Create().
				SetUserID(user.ID).
				SetUserEmail(user.Email).
				SetUserName(user.Username).
				SetAmount(100).
				SetPayAmount(100).
				SetFeeRate(0).
				SetRechargeCode("REFUND-SUCCESS-" + status).
				SetOutTradeNo("sub2_refund_success_" + status).
				SetPaymentType(payment.TypeStripe).
				SetPaymentTradeNo("pi_refund_success_" + status).
				SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusRefunding).
				SetExpiresAt(time.Now().Add(time.Hour)).
				SetPaidAt(time.Now()).
				SetClientIP("127.0.0.1").
				SetSrcHost("api.example.com").
				Save(ctx)
			require.NoError(t, err)

			svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{
				captureRefundBalanceFn: func(_ context.Context, _ int64, _ float64) error {
					return nil
				},
			}}
			plan := &RefundPlan{
				OrderID:         order.ID,
				Order:           order,
				RefundAmount:    100,
				GatewayAmount:   100,
				Reason:          "final success",
				DeductionType:   payment.DeductionTypeBalance,
				BalanceToDeduct: 100,
			}

			result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: status})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Success)
			require.Equal(t, 100.0, result.BalanceDeducted)

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunded, reloaded.Status)
			require.NotNil(t, reloaded.RefundAt)

			successAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, successAudits)
			pendingAudit, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
				Only(ctx)
			require.NoError(t, err)
			require.Contains(t, pendingAudit.Detail, `"providerStatus":"`+status+`"`)
		})
	}
}

func TestQueryAndFinalizeRefundFinalizesProviderStatuses(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           string
		wantStatus       string
		wantDeduct       float64
		available        float64
		wantSuccess      bool
		wantRequireForce bool
	}{
		{name: "success", status: payment.ProviderStatusSuccess, wantStatus: OrderStatusRefunded, wantDeduct: 100, available: 100, wantSuccess: true},
		{name: "balance fell", status: payment.ProviderStatusSuccess, wantStatus: OrderStatusRefundPending, available: 35, wantRequireForce: true},
		{name: "failed", status: payment.ProviderStatusFailed, wantStatus: OrderStatusRefundFailed, available: 100},
		{name: "pending", status: payment.ProviderStatusPending, wantStatus: OrderStatusRefundPending, available: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-"+tc.name)

			var deducted float64
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{
					reserveRefundBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
						return tc.available, nil
					},
					captureRefundBalanceFn: func(ctx context.Context, id int64, amount float64) error {
						deducted += amount
						return nil
					},
				},
			}
			restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: tc.status},
			})
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.wantSuccess, result.Success)
			require.Equal(t, tc.wantRequireForce, result.RequireForce)
			if tc.status == payment.ProviderStatusPending {
				require.Equal(t, refundResultStatePending, result.State)
			}
			require.Equal(t, tc.wantDeduct, deducted)
			if tc.wantSuccess {
				require.Equal(t, tc.wantDeduct, result.BalanceDeducted)
				audit, err := client.PaymentAuditLog.Query().
					Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
					Only(ctx)
				require.NoError(t, err)
				require.Contains(t, audit.Detail, fmt.Sprintf(`"balanceDeducted":%v`, tc.wantDeduct))
			}

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, reloaded.Status)
		})
	}
}

func TestQueryAndFinalizeRefundForceCompletesAfterBalanceChanged(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-force-finalize")
	queryCalls := 0
	captured := 0.0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) { return 35, nil },
			captureRefundBalanceFn: func(_ context.Context, _ int64, amount float64) error {
				captured += amount
				return nil
			},
		},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		queryFn: func(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
			queryCalls++
			return &payment.RefundResponse{RefundID: "rf_force", Status: payment.ProviderStatusSuccess}, nil
		},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.NoError(t, err)
	require.True(t, result.RequireForce)
	require.Zero(t, queryCalls)

	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID, true)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.InDelta(t, 35, captured, 1e-9)
	require.Equal(t, 1, queryCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	require.True(t, reloaded.ForceRefund)
}

func TestFinalizePendingRefundSuccessRejectsStaleCallerBeforeSecondDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "finalize-stale")

	captures := 0
	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{captureRefundBalanceFn: func(ctx context.Context, id int64, amount float64) error {
			require.NotNil(t, dbent.TxFromContext(ctx))
			captures++
			return nil
		}},
	}
	plan := svc.refundFinalizePlan(order)
	plan.BalanceToDeduct = 100
	first, err := svc.finalizePendingRefundSuccess(ctx, plan)
	require.NoError(t, err)
	require.True(t, first.Success)

	second, err := svc.finalizePendingRefundSuccess(ctx, plan)
	require.Nil(t, second)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	require.Equal(t, 1, captures)

	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, successAudits)
}

func TestFinalizePendingRefundSuccessRollsBackPostDeductionFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "finalize-rollback")
	_, err := client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{captureRefundBalanceFn: func(ctx context.Context, id int64, amount float64) error {
			tx := dbent.TxFromContext(ctx)
			require.NotNil(t, tx)
			if _, updateErr := tx.Client().User.UpdateOneID(id).AddBalance(-amount).Save(ctx); updateErr != nil {
				return updateErr
			}
			return errors.New("injected failure after deduction")
		}},
	}
	plan := svc.refundFinalizePlan(order)
	plan.BalanceToDeduct = 100
	result, err := svc.finalizePendingRefundSuccess(ctx, plan)
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected failure after deduction")

	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, successAudits)
}

func TestFinalizeImmediateRefundSuccessRollsBackAndRetriesAfterDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "immediate-retry")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)

	attempts := 0
	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{captureRefundBalanceFn: func(ctx context.Context, id int64, amount float64) error {
			tx := dbent.TxFromContext(ctx)
			require.NotNil(t, tx)
			attempts++
			if _, updateErr := tx.Client().User.UpdateOneID(id).AddBalance(-amount).Save(ctx); updateErr != nil {
				return updateErr
			}
			if attempts == 1 {
				return errors.New("injected finalization failure")
			}
			return nil
		}},
	}
	plan := svc.refundFinalizePlan(order)
	plan.BalanceToDeduct = 100
	providerSuccess := &payment.RefundResponse{Status: payment.ProviderStatusSuccess}

	first, err := svc.finishRefund(ctx, plan, providerSuccess)
	require.Nil(t, first)
	require.ErrorContains(t, err, "injected finalization failure")
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)

	second, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.NoError(t, err)
	require.True(t, second.Success)
	user, err = client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, successAudits)
}

func TestQueryAndFinalizeRefundUnsupportedProviderReturnsClearError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-unsupported")
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, refundProviderTestDouble{})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_QUERY_UNSUPPORTED", infraerrors.Reason(err))
}

func TestQueryAndFinalizeRefundPreservesNoDeductionIntent(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-no-deduction")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	plan := svc.refundFinalizePlan(order)
	plan.DeductBalance = false
	plan.DeductionType = payment.DeductionTypeNone
	require.NoError(t, svc.claimRefundIntent(ctx, plan, OrderStatusCompleted, OrderStatusRefundPending))

	queryCalls := 0
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		queryFn: func(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
			queryCalls++
			return &payment.RefundResponse{RefundID: "rf_no_deduction", Status: payment.ProviderStatusSuccess}, nil
		},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, result.BalanceDeducted)
	require.Equal(t, 1, queryCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
}

func TestQueryAndFinalizeRefundRevalidatesProviderBeforeBalanceReservation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-provider-disabled")
	instanceID, err := strconv.ParseInt(order.ProviderInstanceID, 10, 64)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.UpdateOneID(instanceID).SetRefundEnabled(false).Save(ctx)
	require.NoError(t, err)

	reserveCalls := 0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) {
			reserveCalls++
			return 100, nil
		}},
	}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.Nil(t, result)
	require.Error(t, err)
	require.Zero(t, reserveCalls)
	reloaded, reloadErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, reloadErr)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
}

func TestQueryAndFinalizeRefundResumesClaimedFinalizationFromPersistedProviderSuccess(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-persisted-success")
	_, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_persisted","providerStatus":"success"}`).
		Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)

	queryCalls := 0
	captured := 0.0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			reserveRefundBalanceFn: func(context.Context, int64, float64) (float64, error) { return 100, nil },
			captureRefundBalanceFn: func(_ context.Context, _ int64, amount float64) error {
				captured += amount
				return nil
			},
		},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		queryFn: func(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
			queryCalls++
			return nil, errors.New("query must not run after persisted success")
		},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, false)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, queryCalls)
	require.InDelta(t, 100, captured, 1e-9)
}

func createPendingRefundOrderForTest(t *testing.T, ctx context.Context, client *dbent.Client, suffix string) *dbent.PaymentOrder {
	t.Helper()

	user, err := client.User.Create().
		SetEmail(suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername(suffix).
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName(suffix + "-provider").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-" + suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(100).
		SetRefundReason("pending refund").
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_test","deductionRollbackOK":true}`).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func replacePaymentProviderFactoryForTest(t *testing.T, prov payment.Provider) func() {
	t.Helper()
	original := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(providerKey, instanceID string, config map[string]string) (payment.Provider, error) {
		return prov, nil
	}
	return func() { createPaymentProviderFromInstance = original }
}

type refundProviderTestDouble struct {
	refundFn func(context.Context, payment.RefundRequest) (*payment.RefundResponse, error)
}

func (refundProviderTestDouble) Name() string { return "refund-test" }
func (refundProviderTestDouble) ProviderKey() string {
	return payment.TypeStripe
}
func (refundProviderTestDouble) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeStripe}
}
func (refundProviderTestDouble) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, nil
}
func (p refundProviderTestDouble) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	if p.refundFn != nil {
		return p.refundFn(ctx, req)
	}
	return nil, nil
}

type refundQueryProviderTestDouble struct {
	refundProviderTestDouble
	refundResponse *payment.RefundResponse
	queryFn        func(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error)
}

func (p *refundQueryProviderTestDouble) QueryRefund(ctx context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	if p.queryFn != nil {
		return p.queryFn(ctx, req)
	}
	return p.refundResponse, nil
}
