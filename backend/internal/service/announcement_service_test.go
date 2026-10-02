package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type announcementRepoStub struct {
	item          *Announcement
	activeItems   []Announcement
	listActiveErr error
}

func (s *announcementRepoStub) Create(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (s *announcementRepoStub) GetByID(_ context.Context, _ int64) (*Announcement, error) {
	if s.item == nil {
		return nil, ErrAnnouncementNotFound
	}
	return s.item, nil
}

func (s *announcementRepoStub) Update(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (*announcementRepoStub) Delete(context.Context, int64) error {
	return nil
}

func (*announcementRepoStub) List(context.Context, pagination.PaginationParams, AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (s *announcementRepoStub) ListActive(context.Context, time.Time) ([]Announcement, error) {
	return s.activeItems, s.listActiveErr
}

func TestAnnouncementServiceListPublicFiltersAndLimitsResults(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	repo := &announcementRepoStub{activeItems: []Announcement{
		{ID: 1, Status: AnnouncementStatusActive, NotifyMode: AnnouncementNotifyModeSilent},
		{ID: 2, Status: AnnouncementStatusActive, NotifyMode: AnnouncementNotifyModePopup, Targeting: AnnouncementTargeting{AnyOf: []AnnouncementConditionGroup{{AllOf: []AnnouncementCondition{{Type: AnnouncementConditionTypeBalance, Operator: AnnouncementOperatorGTE, Value: 1}}}}}},
		{ID: 3, Status: AnnouncementStatusDraft, NotifyMode: AnnouncementNotifyModePopup},
		{ID: 4, Status: AnnouncementStatusActive, NotifyMode: AnnouncementNotifyModePopup, StartsAt: &future},
		{ID: 5, Title: "Public", Content: "Visible", Status: AnnouncementStatusActive, NotifyMode: AnnouncementNotifyModePopup},
		{ID: 6, Status: AnnouncementStatusActive, NotifyMode: AnnouncementNotifyModePopup},
	}}
	svc := NewAnnouncementService(repo, nil, nil, nil)

	items, err := svc.ListPublic(context.Background())

	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(5), items[0].ID)
}

func TestAnnouncementServiceListPublicPropagatesRepositoryError(t *testing.T) {
	repoErr := errors.New("database unavailable")
	svc := NewAnnouncementService(&announcementRepoStub{listActiveErr: repoErr}, nil, nil, nil)

	_, err := svc.ListPublic(context.Background())

	require.ErrorIs(t, err, repoErr)
}

func TestAnnouncementServiceCreateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModePopup,
		StartsAt:   &now,
		EndsAt:     &now,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

func TestAnnouncementServiceUpdateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{
		item: &Announcement{
			ID:         1,
			Title:      "公告",
			Content:    "内容",
			Status:     AnnouncementStatusActive,
			NotifyMode: AnnouncementNotifyModePopup,
		},
	}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)
	startsAt := &now
	endsAt := &now

	_, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{
		StartsAt: &startsAt,
		EndsAt:   &endsAt,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}
