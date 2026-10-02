//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountHealthAdminServiceStub struct {
	service.AdminService
	listGroups      []service.Group
	listTotal       int64
	listErr         error
	listCalls       int
	listPage        int
	listPageSize    int
	listPlatform    string
	listStatus      string
	listSearch      string
	listIsExclusive *bool
	listSortBy      string
	listSortOrder   string
}

func (s *accountHealthAdminServiceStub) ListGroups(
	_ context.Context,
	page, pageSize int,
	platform, status, search string,
	isExclusive *bool,
	sortBy, sortOrder string,
) ([]service.Group, int64, error) {
	s.listCalls++
	s.listPage = page
	s.listPageSize = pageSize
	s.listPlatform = platform
	s.listStatus = status
	s.listSearch = search
	s.listIsExclusive = isExclusive
	s.listSortBy = sortBy
	s.listSortOrder = sortOrder
	return s.listGroups, s.listTotal, s.listErr
}

func setupAccountHealthGroupRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewGroupHandler(svc, nil, nil)
	router.GET("/api/v1/admin/groups/account-health-candidates", handler.ListAccountHealthCandidates)
	return router
}

func requireExactJSONKeys(t *testing.T, value map[string]json.RawMessage, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	require.ElementsMatch(t, expected, actual)
}

func TestGroupAccountHealthCandidatesReturnOnlyNonSensitiveOpenAIFields(t *testing.T) {
	svc := &accountHealthAdminServiceStub{
		listGroups: []service.Group{
			{
				ID:          11,
				Name:        "OpenAI active",
				Platform:    service.PlatformOpenAI,
				Status:      service.StatusActive,
				Description: "user@example.com---password---token---https://mail.example/?pwd=secret",
			},
			{
				ID:          12,
				Name:        "OpenAI inactive",
				Platform:    service.PlatformOpenAI,
				Status:      service.StatusDisabled,
				Description: "other@example.com---password---token---https://mail.example/?pwd=secret-2",
			},
		},
		listTotal: 2,
	}
	router := setupAccountHealthGroupRouter(svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/groups/account-health-candidates?page=1&page_size=2",
		nil,
	)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, svc.listCalls)
	require.Equal(t, 1, svc.listPage)
	require.Equal(t, 2, svc.listPageSize)
	require.Equal(t, service.PlatformOpenAI, svc.listPlatform)
	require.Empty(t, svc.listStatus)
	require.Empty(t, svc.listSearch)
	require.Nil(t, svc.listIsExclusive)
	require.Equal(t, "sort_order", svc.listSortBy)
	require.Equal(t, "asc", svc.listSortOrder)
	require.Contains(t, recorder.Body.String(), `"name":"OpenAI active"`)
	require.Contains(t, recorder.Body.String(), `"name":"OpenAI inactive"`)
	require.Contains(t, recorder.Body.String(), `"status":"active"`)
	require.Contains(t, recorder.Body.String(), `"status":"inactive"`)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	requireExactJSONKeys(t, envelope, "code", "message", "data")
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope["data"], &data))
	requireExactJSONKeys(t, data, "items", "total", "page", "page_size", "pages")
	var items []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data["items"], &items))
	require.Len(t, items, 2)
	for _, item := range items {
		requireExactJSONKeys(t, item, "id", "name", "platform", "status")
	}
	var decoded struct {
		Items    []accountHealthGroupCandidate `json:"items"`
		Total    int64                         `json:"total"`
		Page     int                           `json:"page"`
		PageSize int                           `json:"page_size"`
		Pages    int                           `json:"pages"`
	}
	require.NoError(t, json.Unmarshal(envelope["data"], &decoded))
	require.Equal(t, []accountHealthGroupCandidate{
		{ID: 11, Name: "OpenAI active", Platform: service.PlatformOpenAI, Status: "active"},
		{ID: 12, Name: "OpenAI inactive", Platform: service.PlatformOpenAI, Status: "inactive"},
	}, decoded.Items)
	require.Equal(t, int64(2), decoded.Total)
	require.Equal(t, 1, decoded.Page)
	require.Equal(t, 2, decoded.PageSize)
	require.Equal(t, 1, decoded.Pages)
	require.NotContains(t, recorder.Body.String(), "description")
	require.NotContains(t, recorder.Body.String(), "password")
	require.NotContains(t, recorder.Body.String(), "token")
	require.NotContains(t, recorder.Body.String(), "pwd")
}
