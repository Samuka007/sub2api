package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type publicAnnouncementRepoStub struct {
	items []service.Announcement
}

func (*publicAnnouncementRepoStub) Create(context.Context, *service.Announcement) error {
	return nil
}

func (*publicAnnouncementRepoStub) GetByID(context.Context, int64) (*service.Announcement, error) {
	return nil, service.ErrAnnouncementNotFound
}

func (*publicAnnouncementRepoStub) Update(context.Context, *service.Announcement) error {
	return nil
}

func (*publicAnnouncementRepoStub) Delete(context.Context, int64) error {
	return nil
}

func (*publicAnnouncementRepoStub) List(context.Context, pagination.PaginationParams, service.AnnouncementListFilters) ([]service.Announcement, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (s *publicAnnouncementRepoStub) ListActive(context.Context, time.Time) ([]service.Announcement, error) {
	return s.items, nil
}

func TestAnnouncementHandlerListPublicReturnsMinimalPayloadWithoutAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &publicAnnouncementRepoStub{items: []service.Announcement{{
		ID:         42,
		Title:      "Service update",
		Content:    "Maintenance window",
		Status:     service.AnnouncementStatusActive,
		NotifyMode: service.AnnouncementNotifyModePopup,
		CreatedBy:  new(int64),
	}}}
	h := NewAnnouncementHandler(service.NewAnnouncementService(repo, nil, nil, nil))
	router := gin.New()
	router.GET("/api/v1/public/announcements", h.ListPublic)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/public/announcements", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":[{"id":42,"title":"Service update","content":"Maintenance window"}]}`, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "notify_mode")
	require.NotContains(t, recorder.Body.String(), "targeting")
	require.NotContains(t, recorder.Body.String(), "created_by")
}
