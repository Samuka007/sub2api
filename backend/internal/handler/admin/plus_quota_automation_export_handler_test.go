package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type plusQuotaExportAccountRepoStub struct {
	accounts         []service.Account
	err              error
	deleteAnomalyErr error
	deleteAnomalyIDs []int64
}

func (s *plusQuotaExportAccountRepoStub) GetByID(context.Context, int64) (*service.Account, error) {
	return nil, nil
}

func (s *plusQuotaExportAccountRepoStub) ListAllWithFilters(
	context.Context,
	string,
	string,
	string,
	string,
	int64,
	string,
) ([]service.Account, error) {
	return s.accounts, s.err
}

func (s *plusQuotaExportAccountRepoStub) ListByGroup(context.Context, int64) ([]service.Account, error) {
	return nil, nil
}

func (s *plusQuotaExportAccountRepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func (s *plusQuotaExportAccountRepoStub) DeleteOpenAIPlus401AnomalyAccount(_ context.Context, accountID int64) error {
	s.deleteAnomalyIDs = append(s.deleteAnomalyIDs, accountID)
	return s.deleteAnomalyErr
}

type plusQuotaExportClientStub struct{}

func (plusQuotaExportClientStub) QueryUsageStrict(context.Context, int64) (*service.OpenAIQuotaUsage, error) {
	return nil, nil
}

func (plusQuotaExportClientStub) ResetCreditWithRequestID(
	context.Context,
	int64,
	string,
) (*service.OpenAIQuotaResetResult, error) {
	return nil, nil
}

type plusQuotaExportSettingsStub struct{}

func (plusQuotaExportSettingsStub) GetValue(context.Context, string) (string, error) {
	return "", nil
}

func (plusQuotaExportSettingsStub) Set(context.Context, string, string) error {
	return nil
}

func plusQuotaExportRouter(repo *plusQuotaExportAccountRepoStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	automationService := service.NewPlusQuotaAutomationService(
		repo,
		plusQuotaExportClientStub{},
		plusQuotaExportSettingsStub{},
		nil,
	)
	handler := NewPlusQuotaAutomationHandler(automationService)
	router := gin.New()
	router.GET("/api/v1/admin/openai/plus-quota-anomalies/export-notes", handler.ExportAnomalyNotes)
	router.DELETE("/api/v1/admin/openai/plus-quota-anomalies/:accountId/account", handler.DeleteAnomalyAccount)
	return router
}

func TestPlusQuotaAutomationDeleteAnomalyAccount(t *testing.T) {
	tests := []struct {
		name       string
		accountID  string
		deleteErr  error
		wantStatus int
		wantCalls  []int64
	}{
		{name: "success", accountID: "42", wantStatus: http.StatusOK, wantCalls: []int64{42}},
		{name: "stale anomaly", accountID: "42", deleteErr: service.ErrPlusQuotaAnomalyDeleteConflict, wantStatus: http.StatusConflict, wantCalls: []int64{42}},
		{name: "missing account", accountID: "42", deleteErr: service.ErrPlusQuotaAnomalyNotFound, wantStatus: http.StatusNotFound, wantCalls: []int64{42}},
		{name: "invalid account ID", accountID: "invalid", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &plusQuotaExportAccountRepoStub{deleteAnomalyErr: tt.deleteErr}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodDelete,
				"/api/v1/admin/openai/plus-quota-anomalies/"+tt.accountID+"/account",
				nil,
			)
			plusQuotaExportRouter(repo).ServeHTTP(recorder, request)

			require.Equal(t, tt.wantStatus, recorder.Code)
			require.Equal(t, tt.wantCalls, repo.deleteAnomalyIDs)
		})
	}
}

func TestPlusQuotaAutomationExportAnomalyNotes(t *testing.T) {
	firstNote := " first\r\naccount\t note "
	secondNote := "second account note"
	resolvedNote := "resolved account note"
	repo := &plusQuotaExportAccountRepoStub{accounts: []service.Account{
		{
			ID:    2,
			Notes: &secondNote,
			Extra: map[string]any{
				service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{Status: service.PlusQuotaAnomalyStatusOpen},
			},
		},
		{
			ID:    1,
			Notes: &firstNote,
			Extra: map[string]any{
				service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{Status: service.PlusQuotaAnomalyStatusOpen},
			},
		},
		{
			ID:    3,
			Notes: &resolvedNote,
			Extra: map[string]any{
				service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{Status: service.PlusQuotaAnomalyStatusResolved},
			},
		},
	}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/plus-quota-anomalies/export-notes", nil)
	plusQuotaExportRouter(repo).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "2", recorder.Header().Get("X-Exported-Count"))
	require.Regexp(
		t,
		`^attachment; filename="sub2api-plus-anomaly-account-notes-\d{14}\.txt"$`,
		recorder.Header().Get("Content-Disposition"),
	)
	require.Equal(t, "\uFEFFfirst account note\nsecond account note", recorder.Body.String())
}

func TestPlusQuotaAutomationExportAnomalyNotesReturnsNoContent(t *testing.T) {
	emptyNote := " \r\n\t "
	repo := &plusQuotaExportAccountRepoStub{accounts: []service.Account{{
		ID:    1,
		Notes: &emptyNote,
		Extra: map[string]any{
			service.PlusQuotaAnomalyExtraKey: service.PlusQuotaAnomaly{Status: service.PlusQuotaAnomalyStatusOpen},
		},
	}}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/plus-quota-anomalies/export-notes", nil)
	plusQuotaExportRouter(repo).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
	require.Empty(t, recorder.Body.String())
}

func TestPlusQuotaAutomationExportAnomalyNotesReportsRepositoryErrors(t *testing.T) {
	repo := &plusQuotaExportAccountRepoStub{err: errors.New("database unavailable")}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/plus-quota-anomalies/export-notes", nil)
	plusQuotaExportRouter(repo).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}
