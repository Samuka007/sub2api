package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountNoteExportAdminService struct {
	service.AdminService

	accounts []*service.Account
	err      error
	calls    int
	ids      []int64
}

func (s *accountNoteExportAdminService) GetAccountsByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	s.calls++
	s.ids = append([]int64(nil), ids...)
	return s.accounts, s.err
}

func setupAccountNoteExportRouter(adminService service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/export-notes", handler.ExportNotes)
	return router
}

func performAccountNoteExportRequest(router *gin.Engine, body []byte) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/accounts/export-notes",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestAccountHandlerExportNotesPreservesSelectionOrderAndEmptyLines(t *testing.T) {
	firstNote := " first\r\naccount\t note "
	blankNote := " \r\n\t "
	thirdNote := " third\naccount note "
	adminService := &accountNoteExportAdminService{accounts: []*service.Account{
		{ID: 4, Platform: service.PlatformOpenAI},
		{ID: 2, Platform: service.PlatformOpenAI, Notes: &blankNote},
		{ID: 1, Platform: service.PlatformOpenAI, Notes: &firstNote},
		{ID: 3, Platform: service.PlatformOpenAI, Notes: &thirdNote},
	}}

	recorder := performAccountNoteExportRequest(
		setupAccountNoteExportRouter(adminService),
		[]byte(`{"account_ids":[3,1,3,2,4]}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{3, 1, 2, 4}, adminService.ids)
	require.Equal(t, 1, adminService.calls)
	require.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "4", recorder.Header().Get("X-Exported-Count"))
	require.Regexp(
		t,
		`^attachment; filename="sub2api-account-notes-\d{14}\.txt"$`,
		recorder.Header().Get("Content-Disposition"),
	)
	require.Equal(t, "\uFEFFthird account note\nfirst account note\n\n\n", recorder.Body.String())
}

func TestAccountHandlerExportNotesWritesOneLFPerUniqueAccount(t *testing.T) {
	noteA := " a "
	blankNote := " \r\n\t "
	multibyteNote := " 中文\n备注\t🙂 "
	tests := []struct {
		name       string
		accountIDs []int64
		accounts   []*service.Account
		wantBody   []byte
		wantCount  string
	}{
		{
			name:       "single empty note",
			accountIDs: []int64{1},
			accounts:   []*service.Account{{ID: 1, Platform: service.PlatformOpenAI}},
			wantBody:   []byte{0xef, 0xbb, 0xbf, '\n'},
			wantCount:  "1",
		},
		{
			name:       "non-empty then empty",
			accountIDs: []int64{1, 2},
			accounts: []*service.Account{
				{ID: 1, Platform: service.PlatformOpenAI, Notes: &noteA},
				{ID: 2, Platform: service.PlatformOpenAI},
			},
			wantBody:  []byte{0xef, 0xbb, 0xbf, 'a', '\n', '\n'},
			wantCount: "2",
		},
		{
			name:       "two empty notes",
			accountIDs: []int64{1, 2},
			accounts: []*service.Account{
				{ID: 1, Platform: service.PlatformOpenAI, Notes: &blankNote},
				{ID: 2, Platform: service.PlatformOpenAI},
			},
			wantBody:  []byte{0xef, 0xbb, 0xbf, '\n', '\n'},
			wantCount: "2",
		},
		{
			name:       "multibyte note",
			accountIDs: []int64{1},
			accounts: []*service.Account{
				{ID: 1, Platform: service.PlatformOpenAI, Notes: &multibyteNote},
			},
			wantBody:  append([]byte{0xef, 0xbb, 0xbf}, []byte("中文 备注 🙂\n")...),
			wantCount: "1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(accountNoteExportRequest{AccountIDs: test.accountIDs})
			require.NoError(t, err)
			adminService := &accountNoteExportAdminService{accounts: test.accounts}

			recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), body)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, test.wantCount, recorder.Header().Get("X-Exported-Count"))
			require.Equal(t, test.wantBody, recorder.Body.Bytes())
		})
	}
}

func TestAccountHandlerExportNotesRejectsMissingAccountWithoutPartialExport(t *testing.T) {
	note := "must not be exported"
	adminService := &accountNoteExportAdminService{accounts: []*service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Notes: &note},
	}}

	recorder := performAccountNoteExportRequest(
		setupAccountNoteExportRouter(adminService),
		[]byte(`{"account_ids":[1,99]}`),
	)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Equal(t, 1, adminService.calls)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
	require.Empty(t, recorder.Header().Get("X-Exported-Count"))
	require.NotContains(t, recorder.Body.String(), note)
	require.NotContains(t, recorder.Body.String(), "\uFEFF")
}

func TestAccountHandlerExportNotesRejectsNonOpenAIAccountWithoutPartialExport(t *testing.T) {
	openAINote := "must not be exported"
	otherNote := "other platform"
	adminService := &accountNoteExportAdminService{accounts: []*service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Notes: &openAINote},
		{ID: 2, Platform: service.PlatformAnthropic, Notes: &otherNote},
	}}

	recorder := performAccountNoteExportRequest(
		setupAccountNoteExportRouter(adminService),
		[]byte(`{"account_ids":[1,2]}`),
	)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, 1, adminService.calls)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
	require.Empty(t, recorder.Header().Get("X-Exported-Count"))
	require.NotContains(t, recorder.Body.String(), openAINote)
	require.NotContains(t, recorder.Body.String(), "\uFEFF")
}

func TestAccountHandlerExportNotesValidatesRequest(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing account_ids", body: []byte(`{}`)},
		{name: "null account_ids", body: []byte(`{"account_ids":null}`)},
		{name: "empty account_ids", body: []byte(`{"account_ids":[]}`)},
		{name: "zero account ID", body: []byte(`{"account_ids":[1,0]}`)},
		{name: "negative account ID", body: []byte(`{"account_ids":[-1]}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adminService := &accountNoteExportAdminService{}
			recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), test.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, adminService.calls)
			require.Empty(t, recorder.Header().Get("Content-Disposition"))
		})
	}
}

func TestAccountHandlerExportNotesHidesJSONBindingDetails(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "fractional account ID", body: []byte(`{"account_ids":[1.5]}`)},
		{name: "string account ID", body: []byte(`{"account_ids":["1"]}`)},
		{name: "malformed JSON", body: []byte(`{"account_ids":[1]`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adminService := &accountNoteExportAdminService{}
			recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), test.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, adminService.calls)
			require.JSONEq(t, `{"code":400,"message":"Invalid request"}`, recorder.Body.String())
		})
	}
}

func TestAccountHandlerExportNotesRejectsOversizedBody(t *testing.T) {
	body := append([]byte(`{"account_ids":[1],"padding":"`), bytes.Repeat([]byte("a"), 256<<10)...)
	body = append(body, []byte(`"}`)...)
	adminService := &accountNoteExportAdminService{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/export-notes", bytes.NewReader(body))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")

	setupAccountNoteExportRouter(adminService).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Zero(t, adminService.calls)
	require.JSONEq(t, `{"code":413,"message":"Request body too large"}`, recorder.Body.String())
}

func TestAccountHandlerExportNotesRejectsTrailingRequestData(t *testing.T) {
	validBody := []byte(`{"account_ids":[1]}`)
	oversizedTrailingWhitespace := append([]byte(nil), validBody...)
	oversizedTrailingWhitespace = append(
		oversizedTrailingWhitespace,
		bytes.Repeat([]byte(" "), maxAccountNoteExportBodyBytes-len(validBody)+1)...,
	)
	tests := []struct {
		name       string
		body       []byte
		wantStatus int
		wantBody   string
	}{
		{
			name:       "trailing garbage",
			body:       append(append([]byte(nil), validBody...), []byte(`garbage`)...),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":400,"message":"Invalid request"}`,
		},
		{
			name:       "second JSON value",
			body:       append(append([]byte(nil), validBody...), []byte(` {"account_ids":[2]}`)...),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":400,"message":"Invalid request"}`,
		},
		{
			name:       "oversized trailing whitespace",
			body:       oversizedTrailingWhitespace,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"code":413,"message":"Request body too large"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adminService := &accountNoteExportAdminService{}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/admin/accounts/export-notes",
				bytes.NewReader(test.body),
			)
			request.ContentLength = -1
			request.Header.Set("Content-Type", "application/json")

			setupAccountNoteExportRouter(adminService).ServeHTTP(recorder, request)

			require.Equal(t, test.wantStatus, recorder.Code)
			require.Zero(t, adminService.calls)
			require.Empty(t, recorder.Header().Get("Content-Disposition"))
			require.Empty(t, recorder.Header().Get("X-Exported-Count"))
			require.JSONEq(t, test.wantBody, recorder.Body.String())
		})
	}
}

func TestAccountHandlerExportNotesRejects10001RawIDs(t *testing.T) {
	accountIDs := make([]int64, 10001)
	for i := range accountIDs {
		accountIDs[i] = 1
	}
	body, err := json.Marshal(accountNoteExportRequest{AccountIDs: accountIDs})
	require.NoError(t, err)
	adminService := &accountNoteExportAdminService{}

	recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), body)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, adminService.calls)
	require.JSONEq(t, `{"code":400,"message":"too many account_ids"}`, recorder.Body.String())
}

func TestAccountHandlerExportNotesAccepts5000UniqueAccountsAndDuplicate(t *testing.T) {
	uniqueIDs := make([]int64, 5000)
	accounts := make([]*service.Account, 5000)
	for i := range uniqueIDs {
		accountID := int64((i+137)%5000 + 1)
		uniqueIDs[i] = accountID
		accounts[i] = &service.Account{ID: accountID, Platform: service.PlatformOpenAI}
	}
	requestIDs := append([]int64(nil), uniqueIDs...)
	requestIDs = append(requestIDs, uniqueIDs[100])
	body, err := json.Marshal(accountNoteExportRequest{AccountIDs: requestIDs})
	require.NoError(t, err)
	adminService := &accountNoteExportAdminService{accounts: accounts}

	recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), body)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, adminService.calls)
	require.Equal(t, uniqueIDs, adminService.ids)
	require.Equal(t, "5000", recorder.Header().Get("X-Exported-Count"))
	require.Len(t, recorder.Body.Bytes(), 5003)
	require.Equal(t, []byte{0xef, 0xbb, 0xbf}, recorder.Body.Bytes()[:3])
	require.Equal(t, 5000, bytes.Count(recorder.Body.Bytes()[3:], []byte{'\n'}))
}

func TestAccountHandlerExportNotesRejects5001UniqueAccounts(t *testing.T) {
	accountIDs := make([]int64, 5001)
	for i := range accountIDs {
		accountIDs[i] = int64(i + 1)
	}
	body, err := json.Marshal(accountNoteExportRequest{AccountIDs: accountIDs})
	require.NoError(t, err)
	adminService := &accountNoteExportAdminService{}

	recorder := performAccountNoteExportRequest(setupAccountNoteExportRouter(adminService), body)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, adminService.calls)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
}

func TestAccountHandlerExportNotesReportsServiceErrors(t *testing.T) {
	adminService := &accountNoteExportAdminService{err: errors.New("database unavailable")}

	recorder := performAccountNoteExportRequest(
		setupAccountNoteExportRouter(adminService),
		[]byte(`{"account_ids":[1]}`),
	)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, 1, adminService.calls)
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
	require.NotContains(t, recorder.Body.String(), "database unavailable")
}
