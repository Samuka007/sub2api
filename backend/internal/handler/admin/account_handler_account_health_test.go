//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountHealthAccountAdminServiceStub struct {
	service.AdminService
	groups          map[int64]*service.Group
	accountsByGroup map[int64][]service.Account
	accounts        map[int64]*service.Account
	detection       *service.GroupAccountHealthDetection
	detectionErr    error
	detectionCalls  []int64
}

func (s *accountHealthAccountAdminServiceStub) GetGroup(_ context.Context, id int64) (*service.Group, error) {
	if group := s.groups[id]; group != nil {
		return group, nil
	}
	return nil, service.ErrGroupNotFound
}

func (s *accountHealthAccountAdminServiceStub) ListAccounts(
	_ context.Context,
	page, pageSize int,
	_, _, _, _ string,
	groupID int64,
	_, _, _ string,
) ([]service.Account, int64, error) {
	accounts := s.accountsByGroup[groupID]
	start := (page - 1) * pageSize
	if start >= len(accounts) {
		return []service.Account{}, int64(len(accounts)), nil
	}
	end := start + pageSize
	if end > len(accounts) {
		end = len(accounts)
	}
	return append([]service.Account(nil), accounts[start:end]...), int64(len(accounts)), nil
}

func (s *accountHealthAccountAdminServiceStub) ListAccountHealthCandidates(_ context.Context, groupIDs []int64) ([]service.AccountHealthCandidate, error) {
	candidates := make([]service.AccountHealthCandidate, 0)
	indexes := make(map[int64]int)
	for _, groupID := range groupIDs {
		group := s.groups[groupID]
		if group == nil {
			return nil, service.ErrGroupNotFound
		}
		if !strings.EqualFold(group.Platform, service.PlatformOpenAI) {
			return nil, service.ErrAccountHealthUnsupportedGroup
		}
		for _, account := range s.accountsByGroup[groupID] {
			if index, exists := indexes[account.ID]; exists {
				candidates[index].GroupIDs = append(candidates[index].GroupIDs, groupID)
				candidates[index].GroupNames = append(candidates[index].GroupNames, group.Name)
				continue
			}
			indexes[account.ID] = len(candidates)
			candidates = append(candidates, service.AccountHealthCandidate{
				ID: account.ID, Name: account.Name, Platform: account.Platform,
				Type: account.Type, Status: account.Status,
				GroupID: groupID, GroupName: group.Name,
				GroupIDs: []int64{groupID}, GroupNames: []string{group.Name},
			})
		}
	}
	return candidates, nil
}

func (s *accountHealthAccountAdminServiceStub) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if account := s.accounts[id]; account != nil {
		return account, nil
	}
	return nil, service.ErrAccountNotFound
}

func (s *accountHealthAccountAdminServiceStub) DetectAccountHealth(_ context.Context, accountID, groupID int64) (*service.GroupAccountHealthDetection, error) {
	account := s.accounts[accountID]
	if account == nil {
		return nil, service.ErrAccountNotFound
	}
	if !strings.EqualFold(account.Platform, service.PlatformOpenAI) {
		return nil, service.ErrAccountHealthUnsupportedAccount
	}
	bound := false
	for _, candidate := range account.GroupIDs {
		bound = bound || candidate == groupID
	}
	if !bound {
		return nil, service.ErrAccountHealthGroupMismatch
	}
	s.detectionCalls = append(s.detectionCalls, groupID)
	if s.detectionErr != nil {
		return nil, s.detectionErr
	}
	result := *s.detection
	result.AccountID = account.ID
	result.AccountName = account.Name
	return &result, nil
}

func setupAccountHealthAccountRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &AccountHandler{adminService: svc}
	router.GET("/api/v1/admin/accounts/account-health-candidates", handler.ListAccountHealthCandidates)
	router.POST("/api/v1/admin/accounts/:id/account-health-detection", handler.DetectAccountHealth)
	return router
}

func TestAccountHealthCandidatesReturnSelectedGroupAccountsWithoutSecrets(t *testing.T) {
	svc := &accountHealthAccountAdminServiceStub{
		groups: map[int64]*service.Group{
			1: {ID: 1, Name: "Group one", Platform: service.PlatformOpenAI, Description: "secret-one"},
			2: {ID: 2, Name: "Group two", Platform: service.PlatformOpenAI, Description: "secret-two"},
		},
		accountsByGroup: map[int64][]service.Account{
			1: {
				{ID: 10, Name: "alpha@example.com", Platform: service.PlatformOpenAI, Type: "oauth", Status: service.StatusActive, Credentials: map[string]any{"access_token": "secret-token"}},
			},
			2: {
				{ID: 10, Name: "alpha@example.com", Platform: service.PlatformOpenAI, Type: "oauth", Status: service.StatusActive},
				{ID: 20, Name: "beta@example.com", Platform: service.PlatformOpenAI, Type: "oauth", Status: service.StatusDisabled},
			},
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/account-health-candidates?group_ids=1,2&page=1&page_size=20", nil)

	setupAccountHealthAccountRouter(svc).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "secret-one")
	require.NotContains(t, recorder.Body.String(), "secret-two")
	require.NotContains(t, recorder.Body.String(), "secret-token")

	var envelope struct {
		Data struct {
			Items []map[string]json.RawMessage `json:"items"`
			Total int64                        `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, int64(2), envelope.Data.Total)
	require.Len(t, envelope.Data.Items, 2)
	requireExactJSONKeys(t, envelope.Data.Items[0],
		"id", "name", "platform", "type", "status",
		"group_id", "group_name", "group_ids", "group_names",
	)
	var first service.AccountHealthCandidate
	require.NoError(t, json.Unmarshal(mustJSON(t, envelope.Data.Items[0]), &first))
	require.Equal(t, int64(10), first.ID)
	require.Equal(t, []int64{1, 2}, first.GroupIDs)
	require.Equal(t, []string{"Group one", "Group two"}, first.GroupNames)
}

func TestAccountHealthCandidatesRejectInvalidOrUnsupportedGroups(t *testing.T) {
	for name, path := range map[string]string{
		"missing": "/api/v1/admin/accounts/account-health-candidates",
		"invalid": "/api/v1/admin/accounts/account-health-candidates?group_ids=1,invalid",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			setupAccountHealthAccountRouter(&accountHealthAccountAdminServiceStub{}).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}

	svc := &accountHealthAccountAdminServiceStub{groups: map[int64]*service.Group{
		7: {ID: 7, Name: "Claude", Platform: service.PlatformAnthropic},
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/account-health-candidates?group_ids=7", nil)
	setupAccountHealthAccountRouter(svc).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestDetectAccountHealthValidatesSelectedGroupBinding(t *testing.T) {
	detection := &service.GroupAccountHealthDetection{
		GroupID: 7, GroupName: "OpenAI", AccountEmail: "mailbox@example.com",
		Status: "no_evidence", Evidence: []string{}, LifespanStatus: "unavailable",
	}
	svc := &accountHealthAccountAdminServiceStub{
		accounts: map[int64]*service.Account{
			42: {ID: 42, Name: "existing@example.com", Platform: service.PlatformOpenAI, GroupIDs: []int64{7}},
		},
		detection: detection,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/account-health-detection", strings.NewReader(`{"group_id":7}`))
	request.Header.Set("Content-Type", "application/json")

	setupAccountHealthAccountRouter(svc).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{7}, svc.detectionCalls)
	require.Contains(t, recorder.Body.String(), `"account_id":42`)
	require.Contains(t, recorder.Body.String(), `"account_name":"existing@example.com"`)
	require.NotContains(t, recorder.Body.String(), "description")
	require.NotContains(t, recorder.Body.String(), "mailbox_url")
}

func TestDetectAccountHealthRejectsUnboundAndNonOpenAIAccounts(t *testing.T) {
	for name, account := range map[string]*service.Account{
		"unbound":  {ID: 42, Name: "OpenAI", Platform: service.PlatformOpenAI, GroupIDs: []int64{8}},
		"platform": {ID: 42, Name: "Claude", Platform: service.PlatformAnthropic, GroupIDs: []int64{7}},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &accountHealthAccountAdminServiceStub{accounts: map[int64]*service.Account{42: account}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/account-health-detection", strings.NewReader(`{"group_id":7}`))
			request.Header.Set("Content-Type", "application/json")

			setupAccountHealthAccountRouter(svc).ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, svc.detectionCalls)
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	return payload
}
