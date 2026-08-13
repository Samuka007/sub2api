//go:build integration

package repository

import (
	"context"
	"net/http"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type plusQuotaCreateResult struct {
	account *service.Account
	err     error
}

type signalingAdminAccountRepository struct {
	service.AdminAccountRepository
	deleteEntered chan struct{}
	createEntered chan struct{}
}

func (r *signalingAdminAccountRepository) DeleteOpenAIPlus401AnomalyAccount(ctx context.Context, accountID int64) error {
	close(r.deleteEntered)
	return r.AdminAccountRepository.DeleteOpenAIPlus401AnomalyAccount(ctx, accountID)
}

func (r *signalingAdminAccountRepository) CreateSparkShadowWithGroups(
	ctx context.Context,
	parentID int64,
	shadow *service.Account,
	groups []service.AccountGroup,
) error {
	close(r.createEntered)
	return r.AdminAccountRepository.CreateSparkShadowWithGroups(ctx, parentID, shadow, groups)
}

func TestCreateShadowWaitsBehindPlus401DeleteAndLeavesNoOrphan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	signalingRepo := &signalingAdminAccountRepository{
		AdminAccountRepository: repo,
		deleteEntered:          make(chan struct{}),
		createEntered:          make(chan struct{}),
	}
	parent := &service.Account{
		Name:        "plus-401-create-delete-race",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Concurrency: 3,
		Priority:    50,
		Schedulable: true,
		Extra: map[string]any{
			service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{
				Status:     service.PlusQuotaAnomalyStatusOpen,
				HTTPStatus: http.StatusUnauthorized,
			},
		},
	}
	require.NoError(t, repo.Create(ctx, parent))
	t.Cleanup(func() { cleanupPlusQuotaCreateDeleteRace(parent.ID) })

	adminService := service.NewAdminService(
		nil,
		nil,
		signalingRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	gateTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gateTx.Rollback() })
	var lockedID int64
	require.NoError(t, gateTx.QueryRowContext(
		ctx,
		"SELECT id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE",
		parent.ID,
	).Scan(&lockedID))
	require.Equal(t, parent.ID, lockedID)

	deleteResult := make(chan error, 1)
	go func() {
		deleteResult <- signalingRepo.DeleteOpenAIPlus401AnomalyAccount(ctx, parent.ID)
	}()
	waitForPlusQuotaOperationEntry(t, ctx, signalingRepo.deleteEntered, "Plus 401 delete")
	assertPlusQuotaOperationWaits(t, deleteResult, "Plus 401 delete")

	createResultCh := make(chan plusQuotaCreateResult, 1)
	go func() {
		shadow, createErr := adminService.CreateShadow(ctx, parent.ID, service.ShadowOptions{Name: "racing-shadow"})
		createResultCh <- plusQuotaCreateResult{account: shadow, err: createErr}
	}()
	waitForPlusQuotaOperationEntry(t, ctx, signalingRepo.createEntered, "CreateShadow")
	assertPlusQuotaCreateWaits(t, createResultCh)

	require.NoError(t, gateTx.Commit())
	select {
	case deleteErr := <-deleteResult:
		require.NoError(t, deleteErr)
	case <-ctx.Done():
		t.Fatal("Plus 401 delete did not resume after the parent lock was released")
	}
	select {
	case result := <-createResultCh:
		require.Nil(t, result.account)
		require.ErrorIs(t, result.err, service.ErrAccountNotFound)
	case <-ctx.Done():
		t.Fatal("CreateShadow did not resume after the parent lock was released")
	}

	var activeOrphanCount int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM accounts
		WHERE parent_account_id = $1 AND deleted_at IS NULL
	`, parent.ID).Scan(&activeOrphanCount))
	require.Zero(t, activeOrphanCount)
}

func waitForPlusQuotaOperationEntry(t *testing.T, ctx context.Context, entered <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("%s did not enter the atomic repository operation", operation)
	}
}

func assertPlusQuotaOperationWaits(t *testing.T, result <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("%s bypassed the parent row lock: %v", operation, err)
	case <-time.After(100 * time.Millisecond):
	}
}

func assertPlusQuotaCreateWaits(t *testing.T, result <-chan plusQuotaCreateResult) {
	t.Helper()
	select {
	case create := <-result:
		t.Fatalf("CreateShadow bypassed the parent row lock: account=%v err=%v", create.account, create.err)
	case <-time.After(100 * time.Millisecond):
	}
}

func cleanupPlusQuotaCreateDeleteRace(parentID int64) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, `
		DELETE FROM scheduler_outbox
		WHERE account_id = $1
			OR account_id IN (SELECT id FROM accounts WHERE parent_account_id = $1)
	`, parentID)
	_, _ = integrationDB.ExecContext(ctx, `
		DELETE FROM account_groups
		WHERE account_id = $1
			OR account_id IN (SELECT id FROM accounts WHERE parent_account_id = $1)
	`, parentID)
	_, _ = integrationDB.ExecContext(ctx, `
		DELETE FROM scheduled_test_plans
		WHERE account_id = $1
			OR account_id IN (SELECT id FROM accounts WHERE parent_account_id = $1)
	`, parentID)
	_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE parent_account_id = $1", parentID)
	_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = $1", parentID)
}

func TestDeleteOpenAIPlus401AnomalyAccountCascadesAtomically(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	cache := &schedulerCacheRecorder{accounts: make(map[int64]*service.Account)}
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, cache)

	parent := mustCreateAccount(t, tx.Client(), plusQuotaDeleteTestAccount("plus-401-parent", service.PlusQuotaAnomalyStatusOpen, http.StatusUnauthorized))
	parentID := parent.ID
	shadow := mustCreateAccount(t, tx.Client(), &service.Account{
		Name:            "plus-401-shadow",
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		Status:          service.StatusActive,
		ParentAccountID: &parentID,
		QuotaDimension:  service.QuotaDimensionSpark,
	})
	group := mustCreateGroup(t, tx.Client(), &service.Group{Name: "plus-401-delete-group"})
	mustBindAccountToGroup(t, tx.Client(), parent.ID, group.ID, 1)
	mustBindAccountToGroup(t, tx.Client(), shadow.ID, group.ID, 1)
	insertPlusQuotaDeleteTestPlan(t, ctx, tx, parent.ID)
	insertPlusQuotaDeleteTestPlan(t, ctx, tx, shadow.ID)
	_, err := tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id IN ($1, $2)", parent.ID, shadow.ID)
	require.NoError(t, err)

	require.NoError(t, repo.DeleteOpenAIPlus401AnomalyAccount(ctx, parent.ID))

	_, err = repo.GetByID(ctx, parent.ID)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	_, err = repo.GetByID(ctx, shadow.ID)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	bindings, err := tx.Client().AccountGroup.Query().
		Where(dbaccountgroup.AccountIDIn(parent.ID, shadow.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, bindings)
	require.Equal(t, int64(0), plusQuotaDeleteTestCount(t, ctx, tx, "SELECT COUNT(*) FROM scheduled_test_plans WHERE account_id IN ($1, $2)", parent.ID, shadow.ID))
	require.Equal(t, int64(2), plusQuotaDeleteTestCount(t, ctx, tx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id IN ($1, $2)", parent.ID, shadow.ID))
	require.Equal(t, []int64{shadow.ID, parent.ID}, cache.deleteIDs)
}

func TestDeleteOpenAIPlus401AnomalyAccountRejectsStaleStateWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		httpStatus int
	}{
		{name: "resolved", status: service.PlusQuotaAnomalyStatusResolved, httpStatus: http.StatusUnauthorized},
		{name: "not 401", status: service.PlusQuotaAnomalyStatusOpen, httpStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			cache := &schedulerCacheRecorder{accounts: make(map[int64]*service.Account)}
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, cache)

			parent := mustCreateAccount(t, tx.Client(), plusQuotaDeleteTestAccount("plus-401-stale-"+tt.name, tt.status, tt.httpStatus))
			parentID := parent.ID
			shadow := mustCreateAccount(t, tx.Client(), &service.Account{
				Name:            "plus-401-stale-shadow-" + tt.name,
				Platform:        service.PlatformOpenAI,
				Type:            service.AccountTypeOAuth,
				Status:          service.StatusActive,
				ParentAccountID: &parentID,
				QuotaDimension:  service.QuotaDimensionSpark,
			})
			group := mustCreateGroup(t, tx.Client(), &service.Group{Name: "plus-401-stale-group-" + tt.name})
			mustBindAccountToGroup(t, tx.Client(), parent.ID, group.ID, 1)
			mustBindAccountToGroup(t, tx.Client(), shadow.ID, group.ID, 1)
			insertPlusQuotaDeleteTestPlan(t, ctx, tx, parent.ID)
			insertPlusQuotaDeleteTestPlan(t, ctx, tx, shadow.ID)
			_, err := tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id IN ($1, $2)", parent.ID, shadow.ID)
			require.NoError(t, err)

			err = repo.DeleteOpenAIPlus401AnomalyAccount(ctx, parent.ID)
			require.ErrorIs(t, err, service.ErrPlusQuotaAnomalyDeleteConflict)

			_, err = repo.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			_, err = repo.GetByID(ctx, shadow.ID)
			require.NoError(t, err)
			bindings, err := tx.Client().AccountGroup.Query().
				Where(dbaccountgroup.AccountIDIn(parent.ID, shadow.ID)).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, bindings)
			require.Equal(t, int64(2), plusQuotaDeleteTestCount(t, ctx, tx, "SELECT COUNT(*) FROM scheduled_test_plans WHERE account_id IN ($1, $2)", parent.ID, shadow.ID))
			require.Zero(t, plusQuotaDeleteTestCount(t, ctx, tx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id IN ($1, $2)", parent.ID, shadow.ID))
			require.Empty(t, cache.deleteIDs)
		})
	}
}

func plusQuotaDeleteTestAccount(name, status string, httpStatus int) *service.Account {
	return &service.Account{
		Name:     name,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Extra: map[string]any{
			service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{
				Status:     status,
				HTTPStatus: httpStatus,
			},
		},
	}
}

func insertPlusQuotaDeleteTestPlan(t *testing.T, ctx context.Context, tx *dbent.Tx, accountID int64) {
	t.Helper()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO scheduled_test_plans
			(account_id, model_id, cron_expression, enabled, max_results, auto_recover, created_at, updated_at)
		VALUES ($1, '', '*/30 * * * *', TRUE, 50, FALSE, NOW(), NOW())
	`, accountID)
	require.NoError(t, err)
}

func plusQuotaDeleteTestCount(t *testing.T, ctx context.Context, tx *dbent.Tx, query string, args ...any) int64 {
	t.Helper()
	rows, err := tx.QueryContext(ctx, query, args...)
	require.NoError(t, err, "count query failed: %s", query)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next(), "count query returned no rows: %s", query)
	var count int64
	require.NoError(t, rows.Scan(&count), "count query scan failed: %s", query)
	return count
}
