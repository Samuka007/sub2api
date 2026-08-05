package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
)

// --- Refund Flow ---

var createPaymentProviderFromInstance = provider.CreateProvider

// getOrderProviderInstance looks up the provider instance that processed this order.
// For legacy orders without provider_instance_id, it resolves only when the
// historical instance is uniquely identifiable from the stored order fields.
func (s *PaymentService) getOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return s.resolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.entClient.PaymentProviderInstance.Get(ctx, instID)
}

// getRefundOrderProviderInstance resolves the provider instance for refund paths.
// Refunds must be pinned to an explicit historical binding, so legacy
// "best-effort" provider guessing is intentionally not allowed here.
func (s *PaymentService) getRefundOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return nil, nil
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d refund provider instance id is invalid: %s", o.ID, instIDStr)
	}
	inst, err := s.entClient.PaymentProviderInstance.Get(ctx, instID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, fmt.Errorf("order %d refund provider instance %s is missing", o.ID, instIDStr)
		}
		return nil, err
	}
	return inst, nil
}

func (s *PaymentService) resolveUniqueLegacyOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	paymentType := payment.GetBasePaymentType(strings.TrimSpace(o.PaymentType))
	providerKey := strings.TrimSpace(psStringValue(o.ProviderKey))
	if providerKey != "" {
		instances, err := s.entClient.PaymentProviderInstance.Query().
			Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
		if len(matched) == 1 {
			return matched[0], nil
		}
		return nil, nil
	}

	if paymentType == "" {
		return nil, nil
	}

	instances, err := s.entClient.PaymentProviderInstance.Query().
		All(ctx)
	if err != nil {
		return nil, err
	}

	matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
	if len(matched) == 1 {
		return matched[0], nil
	}
	return nil, nil
}

func psFilterLegacyOrderProviderInstances(orderPaymentType string, instances []*dbent.PaymentProviderInstance) []*dbent.PaymentProviderInstance {
	if len(instances) == 0 {
		return nil
	}
	if strings.TrimSpace(orderPaymentType) == "" {
		return instances
	}
	var matched []*dbent.PaymentProviderInstance
	for _, inst := range instances {
		if psLegacyOrderMatchesInstance(orderPaymentType, inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

func psLegacyOrderMatchesInstance(orderPaymentType string, inst *dbent.PaymentProviderInstance) bool {
	if inst == nil {
		return false
	}

	baseType := payment.GetBasePaymentType(strings.TrimSpace(orderPaymentType))
	instanceProviderKey := strings.TrimSpace(inst.ProviderKey)
	if baseType == "" {
		return false
	}

	if baseType == payment.TypeStripe {
		return instanceProviderKey == payment.TypeStripe
	}
	if instanceProviderKey == payment.TypeStripe {
		return false
	}
	if instanceProviderKey == baseType {
		return true
	}
	return payment.InstanceSupportsType(inst.SupportedTypes, baseType)
}

func (s *PaymentService) RequestRefund(ctx context.Context, oid, uid int64, reason string) error {
	o, err := s.validateRefundRequest(ctx, oid, uid)
	if err != nil {
		return err
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.Balance < o.Amount {
		return infraerrors.BadRequest("BALANCE_NOT_ENOUGH", "refund amount exceeds balance")
	}
	nr := strings.TrimSpace(reason)
	now := time.Now()
	by := fmt.Sprintf("%d", uid)
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.UserIDEQ(uid), paymentorder.StatusEQ(OrderStatusCompleted), paymentorder.OrderTypeEQ(payment.OrderTypeBalance)).SetStatus(OrderStatusRefundRequested).SetRefundRequestedAt(now).SetRefundRequestReason(nr).SetRefundRequestedBy(by).SetRefundAmount(o.Amount).Save(ctx)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, oid, "REFUND_REQUESTED", fmt.Sprintf("user:%d", uid), map[string]any{"amount": o.Amount, "reason": nr})
	return nil
}

func (s *PaymentService) validateRefundRequest(ctx context.Context, oid, uid int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != uid {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission")
	}
	if o.OrderType != payment.OrderTypeBalance {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "only balance orders can request refund")
	}
	if o.Status != OrderStatusCompleted {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only completed orders can request refund")
	}
	// Check provider instance allows user refund
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil || inst == nil {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.AllowUserRefund {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "user refund is not enabled for this provider")
	}
	return o, nil
}

func (s *PaymentService) PrepareRefund(ctx context.Context, oid int64, amt float64, reason string, force, deduct bool) (*RefundPlan, *RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	ok := []string{OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundPending, OrderStatusRefundFailed, OrderStatusRefunding}
	if !psSliceContains(ok, o.Status) {
		return nil, nil, infraerrors.BadRequest("INVALID_STATUS", "order status does not allow refund")
	}
	// Check provider instance allows admin refund
	inst, instErr := s.getRefundOrderProviderInstance(ctx, o)
	if instErr != nil {
		slog.Warn("refund: provider instance lookup failed", "orderID", oid, "error", instErr)
		return nil, nil, infraerrors.InternalServer("PROVIDER_LOOKUP_FAILED", "failed to look up payment provider for this order")
	}
	if inst == nil {
		// Legacy order without provider_instance_id — block refund
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.RefundEnabled {
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not enabled for this provider")
	}
	if o.Status == OrderStatusRefunding {
		return nil, &RefundResult{
			Success: false,
			State:   refundResultStatePending,
			Warning: "gateway refund outcome is unknown; query the refund status instead of starting another refund",
		}, nil
	}
	if math.IsNaN(amt) || math.IsInf(amt, 0) {
		return nil, nil, infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund amount")
	}
	if amt <= 0 {
		amt = o.Amount
	}
	if (o.Status == OrderStatusRefundPending || o.Status == OrderStatusRefunding) && o.RefundAmount > 0 {
		amt = o.RefundAmount
	}
	orderCurrency := PaymentOrderCurrency(o)
	if amt-o.Amount > paymentAmountToleranceForCurrency(orderCurrency) {
		return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds recharge")
	}
	ga := calculateGatewayRefundAmount(o.Amount, o.PayAmount, amt, orderCurrency)
	rr := strings.TrimSpace(reason)
	if (o.Status == OrderStatusRefundPending || o.Status == OrderStatusRefunding) && o.RefundReason != nil {
		rr = strings.TrimSpace(*o.RefundReason)
	}
	if rr == "" && o.RefundRequestReason != nil {
		rr = *o.RefundRequestReason
	}
	if rr == "" {
		rr = fmt.Sprintf("refund order:%d", o.ID)
	}
	p := &RefundPlan{OrderID: oid, Order: o, RefundAmount: amt, GatewayAmount: ga, Reason: rr, Force: force, DeductBalance: deduct, DeductionType: payment.DeductionTypeNone}
	if deduct && o.OrderType == payment.OrderTypeBalance {
		reserved, active, reservationErr := latestRefundBalanceReservation(ctx, s.entClient, oid)
		if reservationErr != nil {
			return nil, nil, reservationErr
		}
		if active {
			p.DeductionType = payment.DeductionTypeBalance
			p.BalanceToDeduct = reserved
			p.Force = o.ForceRefund
			return p, nil, nil
		}
	}
	if deduct {
		if er := s.prepDeduct(ctx, o, p, force); er != nil {
			return nil, er, nil
		}
	}
	return p, nil, nil
}

func (s *PaymentService) prepDeduct(ctx context.Context, o *dbent.PaymentOrder, p *RefundPlan, force bool) *RefundResult {
	if o.OrderType == payment.OrderTypeSubscription {
		p.DeductionType = payment.DeductionTypeSubscription
		if o.SubscriptionGroupID != nil && o.SubscriptionDays != nil {
			p.SubDaysToDeduct = *o.SubscriptionDays
			sub, err := s.subscriptionSvc.GetActiveSubscription(ctx, o.UserID, *o.SubscriptionGroupID)
			if err == nil && sub != nil {
				p.SubscriptionID = sub.ID
			} else if !force {
				return &RefundResult{Success: false, Warning: "cannot find active subscription for deduction, use force", RequireForce: true}
			}
		}
		return nil
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		if !force {
			return &RefundResult{Success: false, Warning: "cannot fetch user balance, use force", RequireForce: true}
		}
		return nil
	}
	p.DeductionType = payment.DeductionTypeBalance
	if u.Balance < p.RefundAmount && !force {
		return &RefundResult{Success: false, Warning: "user balance is insufficient for deduction, use force", RequireForce: true}
	}
	p.BalanceToDeduct = math.Max(0, math.Min(p.RefundAmount, u.Balance))
	return nil
}

type refundBalanceRepository interface {
	ReserveRefundBalance(ctx context.Context, id int64, amount float64) (float64, error)
	CaptureRefundBalance(ctx context.Context, id int64, amount float64) error
	ReleaseRefundBalance(ctx context.Context, id int64, amount float64) error
}

func (s *PaymentService) refundBalanceRepo() (refundBalanceRepository, error) {
	repo, ok := s.userRepo.(refundBalanceRepository)
	if !ok {
		return nil, errors.New("user repository does not support refund balance reservations")
	}
	return repo, nil
}

const refundResultStatePending = "pending"

func (s *PaymentService) ExecuteRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	if p == nil || p.Order == nil {
		return nil, infraerrors.BadRequest("INVALID_REFUND_PLAN", "refund plan is missing")
	}
	if p.Order.Status == OrderStatusRefunding {
		return &RefundResult{
			Success: false,
			State:   refundResultStatePending,
			Warning: "gateway refund outcome is unknown; query the refund status instead of starting another refund",
		}, nil
	}
	targetStatus := OrderStatusRefunding
	if p.Order.Status == OrderStatusRefundPending {
		targetStatus = OrderStatusRefundPending
	}
	var provider payment.Provider
	if targetStatus != OrderStatusRefundPending {
		var err error
		provider, err = s.preflightRefundProvider(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	if p.DeductionType == payment.DeductionTypeBalance {
		early, err := s.claimRefundAndReserveBalance(ctx, p, p.Order.Status, targetStatus)
		if err != nil || early != nil {
			return early, err
		}
	} else if err := s.claimRefundIntent(ctx, p, p.Order.Status, targetStatus); err != nil {
		return nil, err
	}
	if targetStatus == OrderStatusRefundPending {
		pendingDetail := s.latestRefundPendingDetail(ctx, p.OrderID)
		if pendingDetail.ProviderStatus == payment.ProviderStatusSuccess || pendingDetail.ProviderStatus == payment.ProviderStatusRefunded {
			return s.finalizePendingRefundSuccess(ctx, p)
		}
		return &RefundResult{Success: false, State: refundResultStatePending, Warning: "gateway refund is pending confirmation; query the refund status to finalize"}, nil
	}
	resp, err := s.gwRefundWithProvider(ctx, p, provider)
	if err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	return s.finishRefund(ctx, p, resp)
}

func (s *PaymentService) claimRefundIntent(ctx context.Context, p *RefundPlan, expectedStatus, targetStatus string) (err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund claim: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)
	update := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus))
	claimed, err := update.
		SetStatus(targetStatus).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		SetForceRefund(p.Force).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("persist refund intent: %w", err)
	}
	if claimed == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if err = writeRefundIntentAuditTx(txCtx, tx.Client(), p); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit refund claim: %w", err)
	}
	return nil
}

const (
	refundIntentAction          = "REFUND_INTENT"
	refundBalanceReservedAction = "REFUND_BALANCE_RESERVED"
	refundBalanceCapturedAction = "REFUND_BALANCE_CAPTURED"
	refundBalanceReleasedAction = "REFUND_BALANCE_RELEASED"
)

type refundIntentDetail struct {
	DeductBalance   bool    `json:"deductBalance"`
	DeductionType   string  `json:"deductionType"`
	GatewayAmount   float64 `json:"gatewayAmount"`
	BalanceToDeduct float64 `json:"balanceToDeduct"`
	SubDaysToDeduct int     `json:"subDaysToDeduct"`
	SubscriptionID  int64   `json:"subscriptionID"`
}

type refundBalanceReservationDetail struct {
	BalanceReserved float64 `json:"balanceReserved"`
}

func (s *PaymentService) claimRefundAndReserveBalance(ctx context.Context, p *RefundPlan, expectedStatus, targetStatus string) (_ *RefundResult, err error) {
	repo, err := s.refundBalanceRepo()
	if err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund reservation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)
	update := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus))
	claimed, err := update.
		SetStatus(targetStatus).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		SetForceRefund(p.Force).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("persist refund intent: %w", err)
	}
	if claimed == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	reserved, active, err := latestRefundBalanceReservation(txCtx, tx.Client(), p.OrderID)
	if err != nil {
		return nil, err
	}
	if !active {
		reserved, err = repo.ReserveRefundBalance(txCtx, p.Order.UserID, p.RefundAmount)
		if err != nil {
			return nil, fmt.Errorf("reserve refund balance: %w", err)
		}
		if !p.Force && reserved+1e-9 < p.RefundAmount {
			_ = tx.Rollback()
			return &RefundResult{
				Success:      false,
				Warning:      "user balance is insufficient for deduction, use force",
				RequireForce: true,
			}, nil
		}
		if err = writeRefundBalanceAuditTx(txCtx, tx.Client(), p.OrderID, refundBalanceReservedAction, reserved); err != nil {
			return nil, err
		}
	}
	p.BalanceToDeduct = reserved
	if err = writeRefundIntentAuditTx(txCtx, tx.Client(), p); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund reservation: %w", err)
	}
	return nil, nil
}

func latestRefundBalanceReservation(ctx context.Context, client *dbent.Client, orderID int64) (float64, bool, error) {
	entry, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)),
			paymentauditlog.ActionIn(refundBalanceReservedAction, refundBalanceCapturedAction, refundBalanceReleasedAction),
		).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).
		First(ctx)
	if dbent.IsNotFound(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read refund balance reservation: %w", err)
	}
	if entry.Action != refundBalanceReservedAction {
		return 0, false, nil
	}
	var detail refundBalanceReservationDetail
	if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		return 0, false, fmt.Errorf("decode refund balance reservation: %w", err)
	}
	return detail.BalanceReserved, true, nil
}

func writeRefundBalanceAuditTx(ctx context.Context, client *dbent.Client, orderID int64, action string, amount float64) error {
	detail, err := json.Marshal(refundBalanceReservationDetail{BalanceReserved: amount})
	if err != nil {
		return fmt.Errorf("marshal refund balance audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction(action).
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return fmt.Errorf("write refund balance audit: %w", err)
	}
	return nil
}

func writeRefundIntentAuditTx(ctx context.Context, client *dbent.Client, p *RefundPlan) error {
	detail, err := json.Marshal(refundIntentDetail{
		DeductBalance: p.DeductBalance, DeductionType: p.DeductionType,
		GatewayAmount: p.GatewayAmount, BalanceToDeduct: p.BalanceToDeduct,
		SubDaysToDeduct: p.SubDaysToDeduct, SubscriptionID: p.SubscriptionID,
	})
	if err != nil {
		return fmt.Errorf("marshal refund intent audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction(refundIntentAction).
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return fmt.Errorf("write refund intent audit: %w", err)
	}
	return nil
}

func latestRefundIntent(ctx context.Context, client *dbent.Client, orderID int64) (refundIntentDetail, bool, error) {
	entry, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ(refundIntentAction)).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).
		First(ctx)
	if dbent.IsNotFound(err) {
		return refundIntentDetail{}, false, nil
	}
	if err != nil {
		return refundIntentDetail{}, false, fmt.Errorf("read refund intent: %w", err)
	}
	var detail refundIntentDetail
	if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		return refundIntentDetail{}, false, fmt.Errorf("decode refund intent: %w", err)
	}
	return detail, true, nil
}

func (s *PaymentService) preflightRefundProvider(ctx context.Context, p *RefundPlan) (payment.Provider, error) {
	if p.Order.PaymentTradeNo == "" {
		return nil, nil
	}
	provider, err := s.getRefundProvider(ctx, p.Order)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	if err := validateProviderSnapshotMetadata(p.Order, provider.ProviderKey(), providerMerchantIdentityMetadata(provider)); err != nil {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_PROVIDER_METADATA_MISMATCH", "admin", map[string]any{
			"detail": err.Error(),
		})
		return nil, err
	}
	return provider, nil
}

func (s *PaymentService) gwRefundWithProvider(ctx context.Context, p *RefundPlan, provider payment.Provider) (*payment.RefundResponse, error) {
	if p.Order.PaymentTradeNo == "" {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_NO_TRADE_NO", "admin", map[string]any{"detail": "skipped"})
		return &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, nil
	}
	if provider == nil {
		return nil, errors.New("refund provider is missing after preflight")
	}
	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	resp, err := provider.Refund(ctx, payment.RefundRequest{
		TradeNo: p.Order.PaymentTradeNo,
		OrderID: p.Order.OutTradeNo,
		Amount:  formatGatewayRefundAmount(p.GatewayAmount, p.Order),
		Reason:  p.Reason,
	})
	finishProviderCall()
	if err != nil {
		if resp != nil {
			switch strings.TrimSpace(resp.Status) {
			case payment.ProviderStatusPending, payment.ProviderStatusFailed:
				return resp, nil
			}
		}
		return nil, err
	}
	return resp, nil
}

func formatGatewayRefundAmount(amount float64, order *dbent.PaymentOrder) string {
	return payment.FormatAmountForCurrency(amount, PaymentOrderCurrency(order))
}

func validateRefundProviderResponse(resp *payment.RefundResponse) error {
	if resp == nil {
		return fmt.Errorf("payment refund response missing")
	}
	status := strings.TrimSpace(resp.Status)
	switch status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded, payment.ProviderStatusPending:
		return nil
	case payment.ProviderStatusFailed:
		return fmt.Errorf("payment refund failed: status %s", status)
	default:
		return fmt.Errorf("payment refund returned unknown status: %s", status)
	}
}

func (s *PaymentService) finishRefund(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	status := ""
	if resp != nil {
		status = strings.TrimSpace(resp.Status)
	}
	if status == payment.ProviderStatusFailed {
		return s.finalizeRefundFailed(ctx, p, OrderStatusRefunding, fmt.Errorf("payment refund failed: status %s", status))
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	if err := s.persistRefundProviderOutcome(ctx, p, OrderStatusRefunding, resp); err != nil {
		return nil, err
	}
	if status == payment.ProviderStatusSuccess || status == payment.ProviderStatusRefunded {
		return s.finalizePendingRefundSuccess(ctx, p)
	}
	return &RefundResult{Success: false, State: refundResultStatePending, Warning: "gateway refund is pending confirmation"}, nil
}

func (s *PaymentService) QueryAndFinalizeRefund(ctx context.Context, oid int64, force bool) (*RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status != OrderStatusRefundPending && o.Status != OrderStatusRefunding {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only pending or in-progress refunds can be finalized")
	}

	plan, intentPersisted, err := s.refundFinalizePlanFromIntent(ctx, o)
	if err != nil {
		return nil, err
	}
	plan.Force = plan.Force || force

	pendingDetail := s.latestRefundPendingDetail(ctx, oid)
	// Legacy pending audits record deductionRollbackOK=false when the original
	// balance or subscription deduction could not be rolled back. Without a
	// persisted intent, that deduction is still in effect, so finalization must
	// persist and reuse a no-deduction intent instead of applying it again.
	if !intentPersisted && !pendingDetail.DeductionRollbackOK {
		plan.DeductBalance = false
		plan.DeductionType = payment.DeductionTypeNone
		plan.BalanceToDeduct = 0
		plan.SubDaysToDeduct = 0
		plan.SubscriptionID = 0
	}
	providerOutcomePersisted := pendingDetail.ProviderStatus == payment.ProviderStatusSuccess || pendingDetail.ProviderStatus == payment.ProviderStatusRefunded
	var queryProvider payment.RefundQueryProvider
	if !providerOutcomePersisted && strings.TrimSpace(o.PaymentTradeNo) != "" {
		prov, err := s.preflightRefundProvider(ctx, plan)
		if err != nil {
			return nil, err
		}
		var ok bool
		queryProvider, ok = prov.(payment.RefundQueryProvider)
		if !ok {
			return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "this payment provider does not support refund status query; verify the gateway outcome manually")
		}
	}

	if plan.DeductionType == payment.DeductionTypeBalance && (intentPersisted || o.OrderType == payment.OrderTypeBalance) {
		early, err := s.claimRefundAndReserveBalance(ctx, plan, o.Status, o.Status)
		if err != nil || early != nil {
			return early, err
		}
	} else {
		if !intentPersisted && plan.DeductBalance && o.OrderType != payment.OrderTypeBalance {
			if early := s.prepDeduct(ctx, o, plan, plan.Force); early != nil {
				return early, nil
			}
		}
		if err := s.claimRefundIntent(ctx, plan, o.Status, o.Status); err != nil {
			return nil, err
		}
	}
	if providerOutcomePersisted {
		return s.finalizeRefundSuccess(ctx, plan, o.Status)
	}
	if strings.TrimSpace(o.PaymentTradeNo) == "" {
		resp := &payment.RefundResponse{Status: payment.ProviderStatusSuccess}
		if err := s.persistRefundProviderOutcome(ctx, plan, o.Status, resp); err != nil {
			return nil, err
		}
		return s.finalizePendingRefundSuccess(ctx, plan)
	}

	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	resp, err := queryProvider.QueryRefund(ctx, payment.RefundQueryRequest{
		TradeNo:  o.PaymentTradeNo,
		OrderID:  o.OutTradeNo,
		RefundID: pendingDetail.RefundID,
		Amount:   formatGatewayRefundAmount(plan.GatewayAmount, o),
	})
	finishProviderCall()
	if err != nil {
		return nil, fmt.Errorf("query refund: %w", err)
	}
	status := ""
	if resp != nil {
		status = strings.TrimSpace(resp.Status)
	}
	if status == payment.ProviderStatusFailed {
		return s.finalizeRefundFailed(ctx, plan, o.Status, fmt.Errorf("payment refund failed: status %s", status))
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.handleGwFail(ctx, plan, err)
	}
	if err := s.persistRefundProviderOutcome(ctx, plan, o.Status, resp); err != nil {
		return nil, err
	}
	if status == payment.ProviderStatusSuccess || status == payment.ProviderStatusRefunded {
		return s.finalizePendingRefundSuccess(ctx, plan)
	}
	return &RefundResult{Success: false, State: refundResultStatePending, Warning: "gateway refund is still pending confirmation"}, nil
}

func (s *PaymentService) finalizePendingRefundSuccess(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	return s.finalizeRefundSuccess(ctx, p, OrderStatusRefundPending)
}

func (s *PaymentService) finalizeRefundSuccess(ctx context.Context, p *RefundPlan, expectedStatus string) (_ *RefundResult, err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund finalization: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)

	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus)).
		SetStatus(OrderStatusRefunding).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("claim refund finalization: %w", err)
	}
	if claimed == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if err := s.applyRefundFinalDeduction(txCtx, p); err != nil {
		return nil, err
	}
	result, err := s.markRefundOkTx(txCtx, tx.Client(), p)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund finalization: %w", err)
	}
	s.invalidateRefundSubscriptionCaches(p)
	return result, nil
}

func (s *PaymentService) invalidateRefundSubscriptionCaches(p *RefundPlan) {
	if p.DeductionType != payment.DeductionTypeSubscription || p.Order.SubscriptionGroupID == nil {
		return
	}
	if err := s.subscriptionSvc.invalidateSubscriptionCaches(p.Order.UserID, *p.Order.SubscriptionGroupID); err != nil {
		slog.Error("refund subscription cache invalidation failed", "orderID", p.OrderID, "error", err)
	}
}

func (s *PaymentService) refundFinalizePlan(o *dbent.PaymentOrder) *RefundPlan {
	refundAmount := o.RefundAmount
	reason := strings.TrimSpace(psStringValue(o.RefundReason))
	if reason == "" {
		reason = fmt.Sprintf("refund order:%d", o.ID)
	}
	return &RefundPlan{
		OrderID:         o.ID,
		Order:           o,
		RefundAmount:    refundAmount,
		GatewayAmount:   calculateGatewayRefundAmount(o.Amount, o.PayAmount, refundAmount, PaymentOrderCurrency(o)),
		Reason:          reason,
		Force:           o.ForceRefund,
		DeductBalance:   true,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: 0,
	}
}

func (s *PaymentService) refundFinalizePlanFromIntent(ctx context.Context, o *dbent.PaymentOrder) (*RefundPlan, bool, error) {
	plan := s.refundFinalizePlan(o)
	intent, found, err := latestRefundIntent(ctx, s.entClient, o.ID)
	if err != nil || !found {
		return plan, found, err
	}
	plan.DeductBalance = intent.DeductBalance
	plan.DeductionType = intent.DeductionType
	plan.GatewayAmount = intent.GatewayAmount
	plan.BalanceToDeduct = intent.BalanceToDeduct
	plan.SubDaysToDeduct = intent.SubDaysToDeduct
	plan.SubscriptionID = intent.SubscriptionID
	return plan, true, nil
}

func (s *PaymentService) applyRefundFinalDeduction(ctx context.Context, p *RefundPlan) error {
	if p.DeductionType == payment.DeductionTypeBalance {
		repo, err := s.refundBalanceRepo()
		if err != nil {
			return err
		}
		if p.BalanceToDeduct > 0 {
			if err := repo.CaptureRefundBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
				return fmt.Errorf("capture refund balance: %w", err)
			}
		}
		tx := dbent.TxFromContext(ctx)
		if tx == nil {
			return errors.New("refund balance capture requires transaction")
		}
		if err := writeRefundBalanceAuditTx(ctx, tx.Client(), p.OrderID, refundBalanceCapturedAction, p.BalanceToDeduct); err != nil {
			return err
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if _, err := s.subscriptionSvc.extendSubscription(ctx, p.SubscriptionID, -p.SubDaysToDeduct, true); err != nil {
			if errors.Is(err, ErrAdjustWouldExpire) {
				if _, revokeErr := s.subscriptionSvc.revokeSubscription(ctx, p.SubscriptionID, true); revokeErr != nil {
					return fmt.Errorf("revoke subscription: %w", revokeErr)
				}
			} else {
				return fmt.Errorf("deduct subscription days: %w", err)
			}
		}
	}
	return nil
}

func (s *PaymentService) finalizeRefundFailed(ctx context.Context, p *RefundPlan, expectedStatus string, gErr error) (_ *RefundResult, err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin failed refund finalization: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus)).
		SetStatus(OrderStatusRefunding).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("claim failed refund finalization: %w", err)
	}
	if claimed == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if p.DeductionType == payment.DeductionTypeBalance {
		repo, repoErr := s.refundBalanceRepo()
		if repoErr != nil {
			return nil, repoErr
		}
		if p.BalanceToDeduct > 0 {
			if releaseErr := repo.ReleaseRefundBalance(txCtx, p.Order.UserID, p.BalanceToDeduct); releaseErr != nil {
				return nil, fmt.Errorf("release refund balance: %w", releaseErr)
			}
		}
		if err = writeRefundBalanceAuditTx(txCtx, tx.Client(), p.OrderID, refundBalanceReleasedAction, p.BalanceToDeduct); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	if _, err = tx.PaymentOrder.UpdateOneID(p.OrderID).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(now).
		SetFailedReason(psErrMsg(gErr)).
		Save(txCtx); err != nil {
		return nil, fmt.Errorf("mark refund failed: %w", err)
	}
	detail, err := json.Marshal(map[string]any{"detail": psErrMsg(gErr)})
	if err != nil {
		return nil, fmt.Errorf("marshal failed refund audit: %w", err)
	}
	if _, err = tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction("REFUND_FAILED").
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(txCtx); err != nil {
		return nil, fmt.Errorf("write failed refund audit: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit failed refund finalization: %w", err)
	}
	return &RefundResult{Success: false, Warning: "gateway refund failed: " + psErrMsg(gErr)}, nil
}

type refundPendingAuditDetail struct {
	RefundID            string `json:"refundID"`
	ProviderStatus      string `json:"providerStatus"`
	DeductionRollbackOK bool   `json:"deductionRollbackOK"`
}

func (s *PaymentService) latestRefundPendingDetail(ctx context.Context, oid int64) refundPendingAuditDetail {
	logEntry, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).
		First(ctx)
	if err != nil || logEntry == nil {
		return refundPendingAuditDetail{DeductionRollbackOK: true}
	}
	detail := refundPendingAuditDetail{DeductionRollbackOK: true}
	_ = json.Unmarshal([]byte(logEntry.Detail), &detail)
	detail.RefundID = strings.TrimSpace(detail.RefundID)
	detail.ProviderStatus = strings.TrimSpace(detail.ProviderStatus)
	return detail
}

// getRefundProvider creates a provider using the order's original instance config.
// Delegates to getOrderProvider which handles instance lookup and fallback.
func (s *PaymentService) getRefundProvider(ctx context.Context, o *dbent.PaymentOrder) (payment.Provider, error) {
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("refund provider instance is unavailable for order %d", o.ID)
	}
	return s.createProviderFromInstance(ctx, inst)
}

func (s *PaymentService) handleGwFail(ctx context.Context, p *RefundPlan, gErr error) (*RefundResult, error) {
	s.writeAuditLog(ctx, p.OrderID, "REFUND_GATEWAY_FAILED", "admin", map[string]any{
		"detail":             psErrMsg(gErr),
		"balanceReservation": p.BalanceToDeduct,
		"recovery":           "query_refund_status",
	})
	return &RefundResult{
		Success: false,
		State:   refundResultStatePending,
		Warning: "gateway outcome is not finalized; refund balance reservation was retained; query the refund status before any further action: " + psErrMsg(gErr),
	}, nil
}

func (s *PaymentService) markRefundOkTx(ctx context.Context, client *dbent.Client, p *RefundPlan) (*RefundResult, error) {
	fs := OrderStatusRefunded
	if p.RefundAmount < p.Order.Amount {
		fs = OrderStatusPartiallyRefunded
	}
	now := time.Now()
	_, err := client.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(fs).SetRefundAmount(p.RefundAmount).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	detail, err := json.Marshal(map[string]any{"refundAmount": p.RefundAmount, "reason": p.Reason, "balanceDeducted": p.BalanceToDeduct, "force": p.Force})
	if err != nil {
		return nil, fmt.Errorf("marshal refund audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction("REFUND_SUCCESS").
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return nil, fmt.Errorf("write refund audit: %w", err)
	}
	return &RefundResult{Success: true, BalanceDeducted: p.BalanceToDeduct, SubDaysDeducted: p.SubDaysToDeduct}, nil
}

func (s *PaymentService) persistRefundProviderOutcome(ctx context.Context, p *RefundPlan, expectedStatus string, resp *payment.RefundResponse) (err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund outcome persistence: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus)).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		ClearRefundAt().
		SetForceRefund(p.Force).
		ClearFailedAt().
		ClearFailedReason().
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("persist refund provider outcome: %w", err)
	}
	if claimed == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	providerStatus := ""
	if resp != nil {
		providerStatus = strings.TrimSpace(resp.Status)
	}
	detail, err := json.Marshal(map[string]any{
		"refundID":            refundResponseID(resp),
		"providerStatus":      providerStatus,
		"refundAmount":        p.RefundAmount,
		"reason":              p.Reason,
		"force":               p.Force,
		"balanceDeducted":     0,
		"subDaysDeducted":     0,
		"balanceRolledBack":   0,
		"subDaysRolledBack":   0,
		"deductionRollbackOK": true,
		"balanceReserved":     p.BalanceToDeduct,
	})
	if err != nil {
		return fmt.Errorf("marshal refund provider outcome: %w", err)
	}
	if _, err = tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction("REFUND_PENDING").
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(txCtx); err != nil {
		return fmt.Errorf("write refund provider outcome audit: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit refund provider outcome: %w", err)
	}
	return nil
}

func refundResponseID(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.RefundID)
}
