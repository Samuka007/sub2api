package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type idempotencyRepository struct {
	client *dbent.Client
	sql    sqlExecutor
}

func NewIdempotencyRepository(client *dbent.Client, sqlDB *sql.DB) service.IdempotencyRepository {
	return &idempotencyRepository{client: client, sql: sqlDB}
}

func (r *idempotencyRepository) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	if execute == nil {
		return errors.New("idempotency transaction executor is nil")
	}
	if existing := dbent.TxFromContext(ctx); existing != nil {
		txClient := existing.Client()
		return execute(ctx, &idempotencyRepository{client: txClient, sql: txClient})
	}
	if r == nil || r.client == nil {
		return errors.New("idempotency transaction client is unavailable")
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	txClient := tx.Client()
	txRepo := &idempotencyRepository{client: txClient, sql: txClient}
	if err := execute(txCtx, txRepo); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *idempotencyRepository) CreateProcessing(ctx context.Context, record *service.IdempotencyRecord) (bool, error) {
	if record == nil {
		return false, nil
	}
	query := `
		INSERT INTO idempotency_records (
			scope, idempotency_key_hash, request_fingerprint, status, locked_until, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (scope, idempotency_key_hash) DO NOTHING
		RETURNING id, locked_until, created_at, updated_at
	`
	var lockedUntil sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	err := scanSingleRow(ctx, r.sql, query, []any{
		record.Scope,
		record.IdempotencyKeyHash,
		record.RequestFingerprint,
		record.Status,
		record.LockedUntil,
		record.ExpiresAt,
	}, &record.ID, &lockedUntil, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	record.CreatedAt = createdAt
	record.UpdatedAt = updatedAt
	if lockedUntil.Valid {
		v := lockedUntil.Time
		record.LockedUntil = &v
	} else {
		record.LockedUntil = nil
	}
	return true, nil
}

func (r *idempotencyRepository) GetByScopeAndKeyHash(ctx context.Context, scope, keyHash string) (*service.IdempotencyRecord, error) {
	query := `
		SELECT
			id, scope, idempotency_key_hash, request_fingerprint, status, response_status,
			response_body, error_reason, locked_until, expires_at, created_at, updated_at
		FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
	`
	record := &service.IdempotencyRecord{}
	var responseStatus sql.NullInt64
	var responseBody sql.NullString
	var errorReason sql.NullString
	var lockedUntil sql.NullTime
	err := scanSingleRow(ctx, r.sql, query, []any{scope, keyHash},
		&record.ID,
		&record.Scope,
		&record.IdempotencyKeyHash,
		&record.RequestFingerprint,
		&record.Status,
		&responseStatus,
		&responseBody,
		&errorReason,
		&lockedUntil,
		&record.ExpiresAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if responseStatus.Valid {
		v := int(responseStatus.Int64)
		record.ResponseStatus = &v
	}
	if responseBody.Valid {
		v := responseBody.String
		record.ResponseBody = &v
	}
	if errorReason.Valid {
		v := errorReason.String
		record.ErrorReason = &v
	}
	if lockedUntil.Valid {
		v := lockedUntil.Time
		record.LockedUntil = &v
	}
	return record, nil
}

func (r *idempotencyRepository) TryReclaim(
	ctx context.Context,
	id int64,
	fromStatus string,
	newRequestFingerprint string,
	now, newLockedUntil, newExpiresAt time.Time,
) (bool, error) {
	query := `
		UPDATE idempotency_records
		SET status = $2,
			request_fingerprint = $3,
			locked_until = $4,
			error_reason = NULL,
			updated_at = NOW(),
			expires_at = $5
		WHERE id = $1
			AND status = $6
			AND (locked_until IS NULL OR locked_until <= $7)
	`
	res, err := r.sql.ExecContext(ctx, query,
		id,
		service.IdempotencyStatusProcessing,
		newRequestFingerprint,
		newLockedUntil,
		newExpiresAt,
		fromStatus,
		now,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *idempotencyRepository) ExtendProcessingLock(
	ctx context.Context,
	id int64,
	requestFingerprint string,
	expectedLockedUntil,
	newLockedUntil,
	newExpiresAt time.Time,
) (bool, error) {
	query := `
		UPDATE idempotency_records
		SET locked_until = $2,
			expires_at = $3,
			updated_at = NOW()
		WHERE id = $1
			AND status = $4
			AND request_fingerprint = $5
			AND locked_until = $6
	`
	res, err := r.sql.ExecContext(
		ctx,
		query,
		id,
		newLockedUntil,
		newExpiresAt,
		service.IdempotencyStatusProcessing,
		requestFingerprint,
		expectedLockedUntil,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *idempotencyRepository) MarkSucceeded(ctx context.Context, id int64, expectedLockedUntil time.Time, responseStatus int, responseBody string, expiresAt time.Time) error {
	query := `
		UPDATE idempotency_records
		SET status = $2,
			response_status = $3,
			response_body = $4,
			error_reason = NULL,
			locked_until = NULL,
			expires_at = $5,
			updated_at = NOW()
		WHERE id = $1
			AND status = $6
			AND locked_until = $7
	`
	result, err := r.sql.ExecContext(ctx, query,
		id,
		service.IdempotencyStatusSucceeded,
		responseStatus,
		responseBody,
		expiresAt,
		service.IdempotencyStatusProcessing,
		expectedLockedUntil,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("idempotency succeeded transition was not applied")
	}
	return nil
}

func (r *idempotencyRepository) MarkFailedRetryable(ctx context.Context, id int64, expectedLockedUntil time.Time, errorReason string, lockedUntil, expiresAt time.Time) error {
	query := `
		UPDATE idempotency_records
		SET status = $2,
			error_reason = $3,
			locked_until = $4,
			expires_at = $5,
			updated_at = NOW()
		WHERE id = $1
			AND status = $6
			AND locked_until = $7
	`
	result, err := r.sql.ExecContext(ctx, query,
		id,
		service.IdempotencyStatusFailedRetryable,
		errorReason,
		lockedUntil,
		expiresAt,
		service.IdempotencyStatusProcessing,
		expectedLockedUntil,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("idempotency failed_retryable transition was not applied")
	}
	return nil
}

func (r *idempotencyRepository) DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 500
	}
	query := `
		WITH victims AS (
			SELECT id
			FROM idempotency_records
			WHERE expires_at <= $1
			ORDER BY expires_at ASC
			LIMIT $2
		)
		DELETE FROM idempotency_records
		WHERE id IN (SELECT id FROM victims)
	`
	res, err := r.sql.ExecContext(ctx, query, now, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
