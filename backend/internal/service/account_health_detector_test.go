//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type accountHealthGroupRepoStub struct {
	GroupRepository
	group     *Group
	err       error
	calls     int
	fullCalls int
	groupID   int64
}

func (s *accountHealthGroupRepoStub) GetByID(_ context.Context, groupID int64) (*Group, error) {
	s.fullCalls++
	return s.group, s.err
}

func (s *accountHealthGroupRepoStub) GetByIDLite(_ context.Context, groupID int64) (*Group, error) {
	s.calls++
	s.groupID = groupID
	return s.group, s.err
}

type accountHealthMailboxFetcherStub struct {
	response  accountHealthMailboxResponse
	responses []accountHealthMailboxResponse
	err       error
	errs      []error
	fetch     func(context.Context, string) (accountHealthMailboxResponse, error)
	calls     int
	rawURL    string
	rawURLs   []string
}

type accountHealthConcurrentGroupRepoStub struct {
	AdminGroupRepository
	group  *Group
	groups map[int64]*Group
}

type accountHealthAdminAccountRepoStub struct {
	AccountRepository
	accounts        map[int64]*Account
	accountsByGroup map[int64][]Account
}

func (s *accountHealthAdminAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if account := s.accounts[id]; account != nil {
		return account, nil
	}
	return nil, ErrAccountNotFound
}

func (s *accountHealthAdminAccountRepoStub) ListWithFilters(
	_ context.Context,
	params pagination.PaginationParams,
	_, _, _, _ string,
	groupID int64,
	_ string,
) ([]Account, *pagination.PaginationResult, error) {
	accounts := s.accountsByGroup[groupID]
	start := (params.Page - 1) * params.PageSize
	if start >= len(accounts) {
		return []Account{}, &pagination.PaginationResult{Total: int64(len(accounts))}, nil
	}
	end := start + params.PageSize
	if end > len(accounts) {
		end = len(accounts)
	}
	return append([]Account(nil), accounts[start:end]...), &pagination.PaginationResult{Total: int64(len(accounts))}, nil
}

type accountHealthAdminDetectorStub struct {
	result       *GroupAccountHealthDetection
	err          error
	calls        []int64
	accountCalls []accountHealthAdminDetectorCall
}

type accountHealthAdminDetectorCall struct {
	groupID int64
	notes   string
}

func (s *accountHealthAdminDetectorStub) DetectGroup(_ context.Context, groupID int64) (*GroupAccountHealthDetection, error) {
	s.calls = append(s.calls, groupID)
	return s.result, s.err
}

func (s *accountHealthAdminDetectorStub) DetectAccountNotes(_ context.Context, groupID int64, notes string) (*GroupAccountHealthDetection, error) {
	s.accountCalls = append(s.accountCalls, accountHealthAdminDetectorCall{groupID: groupID, notes: notes})
	return s.result, s.err
}

func (s *accountHealthConcurrentGroupRepoStub) groupByID(groupID int64) *Group {
	if group, ok := s.groups[groupID]; ok {
		return group
	}
	return s.group
}

func (s *accountHealthConcurrentGroupRepoStub) GetByID(_ context.Context, groupID int64) (*Group, error) {
	return s.groupByID(groupID), nil
}

func (s *accountHealthConcurrentGroupRepoStub) GetByIDLite(_ context.Context, groupID int64) (*Group, error) {
	return s.groupByID(groupID), nil
}

type accountHealthBlockingFetcher struct {
	started chan struct{}
	release <-chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (f *accountHealthBlockingFetcher) Fetch(ctx context.Context, _ string) (accountHealthMailboxResponse, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		peak := f.peak.Load()
		if active <= peak || f.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	f.started <- struct{}{}
	select {
	case <-ctx.Done():
		return accountHealthMailboxResponse{}, ctx.Err()
	case <-f.release:
		return accountHealthMailboxPageForTest([]string{"OpenAI verification code"}, ""), nil
	}
}

func (s *accountHealthMailboxFetcherStub) Fetch(ctx context.Context, rawURL string) (accountHealthMailboxResponse, error) {
	call := s.calls
	s.calls++
	s.rawURL = rawURL
	s.rawURLs = append(s.rawURLs, rawURL)
	if s.fetch != nil {
		return s.fetch(ctx, rawURL)
	}
	if call < len(s.errs) && s.errs[call] != nil {
		return accountHealthMailboxResponse{}, s.errs[call]
	}
	if call < len(s.responses) {
		return s.responses[call], nil
	}
	return s.response, s.err
}

func accountHealthMailboxPageForTest(blocks []string, nextLink string) accountHealthMailboxResponse {
	var body strings.Builder
	body.WriteString("<html><body>")
	for _, block := range blocks {
		body.WriteString("<article>")
		body.WriteString(stdhtml.EscapeString(block))
		body.WriteString("</article>")
	}
	if nextLink != "" {
		body.WriteString(`<a rel="next" href="`)
		body.WriteString(stdhtml.EscapeString(nextLink))
		body.WriteString(`">Next</a>`)
	}
	body.WriteString("</body></html>")
	return accountHealthMailboxResponse{body: []byte(body.String()), contentType: "text/html; charset=utf-8"}
}

func newAccountHealthDetectorForTest(group *Group, fetcher accountHealthMailboxFetcher) *groupAccountHealthDetector {
	return &groupAccountHealthDetector{
		groupRepo: &accountHealthGroupRepoStub{group: group},
		fetcher:   fetcher,
		now: func() time.Time {
			return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
		},
		timeout:   accountHealthDetectionTimeout,
		global:    make(chan struct{}, accountHealthGlobalConcurrency),
		hostGates: make(map[string]*accountHealthHostGate),
	}
}

func TestAdminServiceListAccountHealthCandidatesDeduplicatesAndOmitsSecrets(t *testing.T) {
	accountNotes := "account-note-secret"
	groupRepo := &accountHealthConcurrentGroupRepoStub{groups: map[int64]*Group{
		1: {ID: 1, Name: "Primary", Platform: PlatformOpenAI, Description: "group-secret"},
		2: {ID: 2, Name: "Backup", Platform: PlatformOpenAI, Description: "backup-secret"},
	}}
	accountRepo := &accountHealthAdminAccountRepoStub{accountsByGroup: map[int64][]Account{
		1: {
			{ID: 20, Name: "beta", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled},
			{ID: 10, Name: "Alpha", Notes: &accountNotes, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "token-secret"}},
		},
		2: {
			{ID: 10, Name: "Alpha", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accountRepo}

	candidates, err := svc.ListAccountHealthCandidates(context.Background(), []int64{1, 2})

	require.NoError(t, err)
	require.Len(t, candidates, 2)
	require.Equal(t, int64(10), candidates[0].ID)
	require.Equal(t, []int64{1, 2}, candidates[0].GroupIDs)
	require.Equal(t, []string{"Primary", "Backup"}, candidates[0].GroupNames)
	payload, err := json.Marshal(candidates)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "group-secret")
	require.NotContains(t, string(payload), "backup-secret")
	require.NotContains(t, string(payload), "token-secret")
	require.NotContains(t, string(payload), "account-note-secret")
}

func TestAdminServiceListAccountHealthCandidatesRejectsUnsupportedGroup(t *testing.T) {
	svc := &adminServiceImpl{
		groupRepo: &accountHealthConcurrentGroupRepoStub{groups: map[int64]*Group{
			7: {ID: 7, Name: "Claude", Platform: PlatformAnthropic},
		}},
		accountRepo: &accountHealthAdminAccountRepoStub{},
	}

	_, err := svc.ListAccountHealthCandidates(context.Background(), []int64{7})

	require.ErrorIs(t, err, ErrAccountHealthUnsupportedGroup)
}

func TestAdminServiceDetectAccountHealthValidatesBindingAndCopiesResult(t *testing.T) {
	notes := "existing@example.com---https://mail.example.test/messages?user=existing@example.com"
	for name, account := range map[string]*Account{
		"group_ids":      {ID: 42, Name: "existing", Notes: &notes, Platform: PlatformOpenAI, GroupIDs: []int64{7}},
		"account_groups": {ID: 42, Name: "existing", Notes: &notes, Platform: PlatformOpenAI, AccountGroups: []AccountGroup{{GroupID: 7}}},
		"groups":         {ID: 42, Name: "existing", Notes: &notes, Platform: PlatformOpenAI, Groups: []*Group{{ID: 7}}},
	} {
		t.Run(name, func(t *testing.T) {
			detectorResult := &GroupAccountHealthDetection{GroupID: 7, Evidence: []string{"openai"}}
			detector := &accountHealthAdminDetectorStub{result: detectorResult}
			svc := &adminServiceImpl{
				accountRepo:           &accountHealthAdminAccountRepoStub{accounts: map[int64]*Account{42: account}},
				accountHealthDetector: detector,
			}

			result, err := svc.DetectAccountHealth(context.Background(), 42, 7)

			require.NoError(t, err)
			require.Empty(t, detector.calls)
			require.Equal(t, []accountHealthAdminDetectorCall{{groupID: 7, notes: notes}}, detector.accountCalls)
			require.Equal(t, int64(42), result.AccountID)
			require.Equal(t, "existing", result.AccountName)
			require.Zero(t, detectorResult.AccountID)
			result.Evidence[0] = "changed"
			require.Equal(t, []string{"openai"}, detectorResult.Evidence)
		})
	}
}

func TestAdminServiceDetectAccountHealthRejectsInvalidAccountScope(t *testing.T) {
	for name, testCase := range map[string]struct {
		account  *Account
		expected error
	}{
		"unsupported": {account: &Account{ID: 42, Platform: PlatformAnthropic, GroupIDs: []int64{7}}, expected: ErrAccountHealthUnsupportedAccount},
		"unbound":     {account: &Account{ID: 42, Platform: PlatformOpenAI, GroupIDs: []int64{8}}, expected: ErrAccountHealthGroupMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			detector := &accountHealthAdminDetectorStub{result: &GroupAccountHealthDetection{}}
			svc := &adminServiceImpl{
				accountRepo:           &accountHealthAdminAccountRepoStub{accounts: map[int64]*Account{42: testCase.account}},
				accountHealthDetector: detector,
			}

			_, err := svc.DetectAccountHealth(context.Background(), 42, 7)

			require.ErrorIs(t, err, testCase.expected)
			require.Empty(t, detector.calls)
			require.Empty(t, detector.accountCalls)
		})
	}
}

func TestGroupAccountHealthDetectorPassesRequestedIDToRepository(t *testing.T) {
	repo := &accountHealthGroupRepoStub{group: &Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "",
	}}
	detector := &groupAccountHealthDetector{
		groupRepo: repo,
		fetcher:   &accountHealthMailboxFetcherStub{},
		now:       time.Now,
		timeout:   accountHealthDetectionTimeout,
		global:    make(chan struct{}, accountHealthGlobalConcurrency),
		hostGates: make(map[string]*accountHealthHostGate),
	}

	result, err := detector.DetectGroup(context.Background(), 77)

	require.NoError(t, err)
	require.Equal(t, int64(77), repo.groupID)
	require.Equal(t, 1, repo.calls)
	require.Zero(t, repo.fullCalls)
	require.Equal(t, int64(42), result.GroupID)
}

func TestGroupAccountHealthDetectorUsesAccountNotesInsteadOfGroupDescription(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{
		response: accountHealthMailboxPageForTest([]string{"OpenAI account notice"}, ""),
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "group@example.com---https://mail.example.test/group?user=group@example.com",
	}, fetcher)

	result, err := detector.DetectAccountNotes(
		context.Background(),
		42,
		"account@example.com---https://mail.example.test/account?user=account@example.com",
	)

	require.NoError(t, err)
	require.Equal(t, "account@example.com", result.AccountEmail)
	require.Contains(t, fetcher.rawURL, "/account")
	require.NotContains(t, fetcher.rawURL, "/group")
}

func TestGroupAccountHealthDetectorUsesMailURLAndIgnoresTrailingFields(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
		[]string{"An ordinary mailbox message with no account health evidence."},
		"",
	)}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "OpenAI primary",
		Platform: PlatformOpenAI,
	}, fetcher)

	result, err := detector.DetectAccountNotes(
		context.Background(),
		42,
		"account@example.com---https://mail.example.test/account?user=account@example.com"+
			"---not-a-phone---http://sms.example.test/api/messages/synthetic-sms-token---anything",
	)

	require.NoError(t, err)
	require.Equal(t, "account@example.com", result.AccountEmail)
	require.Equal(t, 1, fetcher.calls)
	require.Contains(t, fetcher.rawURL, "mail.example.test/account")
	require.NotContains(t, fetcher.rawURL, "sms.example.test")
}

func TestGroupAccountHealthDetectorDoesNotFallbackToGroupDescriptionForEmptyAccountNotes(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "group@example.com---https://mail.example.test/group?user=group@example.com",
	}, fetcher)

	result, err := detector.DetectAccountNotes(context.Background(), 42, "")

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "empty", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorReturnsFormatErrorWithoutFetching(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "empty", result.FormatIssue)
	require.Equal(t, int64(42), result.GroupID)
	require.Equal(t, "OpenAI primary", result.GroupName)
	require.Empty(t, result.AccountEmail)
	require.Zero(t, fetcher.calls)
	require.NotEmpty(t, result.CheckedAt)
}

func TestGroupAccountHealthDetectorRejectsRedactionBudgetOverflowWithoutFetching(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---" + accountHealthOverflowURLForTest("reflected-path-token"),
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "invalid_url", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorRejectsInvalidEmailSyntaxWithoutFetching(t *testing.T) {
	t.Parallel()

	for _, description := range []string{
		"user@example..com---https://mail.example/inbox---+19045550123---https://sms.example/messages",
		"user@.example.com---https://mail.example/inbox---+19045550123---https://sms.example/messages",
		"user@-example.com---https://mail.example/inbox---+19045550123---https://sms.example/messages",
		"user@example-.com---https://mail.example/inbox---+19045550123---https://sms.example/messages",
	} {
		fetcher := &accountHealthMailboxFetcherStub{}
		detector := newAccountHealthDetectorForTest(&Group{
			ID:          42,
			Name:        "OpenAI primary",
			Platform:    PlatformOpenAI,
			Description: description,
		}, fetcher)

		result, err := detector.DetectGroup(context.Background(), 42)

		require.NoError(t, err)
		require.Equal(t, accountHealthStatusFormatError, result.Status)
		require.Equal(t, "missing_email", result.FormatIssue)
		require.Zero(t, fetcher.calls)
	}
}

func TestGroupAccountHealthDetectorRejectsInsecureStructuredMailboxBeforeAuxiliaryURL(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---http://mail.example/inbox---+19045550123---https://sms.example/messages",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "invalid_url", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorRejectsInsecureUnstructuredMailboxBeforeAuxiliaryURL(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---http://mail.example/inbox---https://docs.example/help",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "invalid_url", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorUsesFirstMailboxURLWhenAuxiliaryCarriesEmail(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
		[]string{"Ordinary mailbox message with enough content to be parsed."},
		"",
	)}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "reader@example.com---https://mail.example/inbox---https://sms.example/messages?mail=reader%40example.com",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, fetcher.calls)
	require.Contains(t, fetcher.rawURL, "mail.example")
	require.NotContains(t, fetcher.rawURL, "sms.example")
}

func TestGroupAccountHealthDetectorRejectsEmptyStructuredMailboxBeforeAuxiliaryURL(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com--- ---+19045550123---https://sms.example/messages",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "missing_url", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorRejectsAlternateRecordSeparatorsWithoutFetching(t *testing.T) {
	for _, separator := range []string{"\r", "\f", "\v", "\u0085", "\u2028", "\u2029", "&#12;"} {
		separator := separator
		t.Run(fmt.Sprintf("separator_%x", []byte(separator)), func(t *testing.T) {
			t.Parallel()
			fetcher := &accountHealthMailboxFetcherStub{}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:       42,
				Name:     "OpenAI primary",
				Platform: PlatformOpenAI,
				Description: "one@example.com---https://mail.example/one?mail=one%40example.com" + separator +
					"two@example.com---https://mail.example/two?mail=two%40example.com",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)
			require.NoError(t, err)
			require.Equal(t, accountHealthStatusFormatError, result.Status)
			require.Zero(t, fetcher.calls)
		})
	}
}

func TestGroupAccountHealthDetectorDoesNotFallbackToTrailingURL(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "OpenAI primary",
		Platform: PlatformOpenAI,
		Description: "one@example.com---password---token---https://mail.example/one?mail=one%40example.com---" +
			"two@example.com---password---token---https://mail.example/two?mail=two%40example.com",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "missing_url", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorIgnoresAdditionalRecordOnSameLine(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
		[]string{"An ordinary mailbox message with no account health evidence."},
		"",
	)}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "OpenAI primary",
		Platform: PlatformOpenAI,
		Description: "one@example.com---https://mail.example/one?mail=one%40example.com---" +
			"two@example.com---https://mail.example/two?mail=two%40example.com",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, fetcher.calls)
	require.Contains(t, fetcher.rawURL, "mail.example/one")
	require.NotContains(t, fetcher.rawURL, "mail.example/two")
}

func TestGroupAccountHealthDetectorRejectsMultipleURLOnlyRecordsWithoutFetching(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "https://mail.example/one?mail=one%40example.com---https://mail.example/two?mail=two%40example.com",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFormatError, result.Status)
	require.Equal(t, "missing_email", result.FormatIssue)
	require.Zero(t, fetcher.calls)
}

func TestGroupAccountHealthDetectorDetectsDeactivatedMailbox(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxResponse{
		contentType: "application/json; charset=utf-8",
		body: []byte(`[
          "Subject: OpenAI - Access Deactivated\nYour OpenAI account has been deactivated and can no longer be used. Associated with user@example.com. 2026-07-21"
        ]`),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "OpenAI primary",
		Platform: PlatformOpenAI,
		Description: "user@example.com---" +
			"https://mail.example/inbox?mail=user%40example.com&pwd=secret&limit=5",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusDeactivated, result.Status)
	require.Equal(t, "user@example.com", result.AccountEmail)
	require.Equal(t, 1, result.MessagesScanned)
	require.Equal(t, 1, result.PagesScanned)
	require.NotEmpty(t, result.Evidence)
	require.Contains(t, fetcher.rawURL, "limit=50")
}

func TestGroupAccountHealthDetectorTreatsStructuredEmptyMailboxAsNoEvidence(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "top-level array", body: `[]`},
		{name: "messages collection", body: `{"messages":[],"total":0}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxResponse{
				contentType: "application/json; charset=utf-8",
				body:        []byte(test.body),
			}}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, accountHealthStatusNotFound, result.Status)
			require.Zero(t, result.MessagesScanned)
			require.Equal(t, 1, result.PagesScanned)
			require.Empty(t, result.Evidence)
			require.Equal(t, string(accountHealthLifespanUnavailable), result.LifespanStatus)
			require.Equal(t, 1, fetcher.calls)
		})
	}
}

func TestGroupAccountHealthDetectorRedactsMailboxURLCredentialsFromResult(t *testing.T) {
	t.Parallel()

	const mailboxPassword = "NOTE_MAIL_PASSWORD_SYNTHETIC"
	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest([]string{
		"Subject: OpenAI - Access Deactivated " + mailboxPassword + "\n" +
			"Your OpenAI account has been deactivated and can no longer be used. " +
			"Associated with user@example.com. 2026-07-21",
		"You've successfully subscribed to ChatGPT Plus.\n" +
			"Order number: sub_RedactionSynthetic\nOrder date: Jul 16, 2026\n" +
			"Payment method: " + mailboxPassword,
	}, "")}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "OpenAI primary",
		Platform: PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&limit=5&pwd=" +
			mailboxPassword + "---ignored-tail",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 1, fetcher.calls)
	require.Contains(t, fetcher.rawURL, "mail.example")
	require.NotContains(t, fetcher.rawURL, "sms.example")
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), mailboxPassword)
	require.Empty(t, result.PaymentMethod)
}

func TestGroupAccountHealthDetectorRedactsLowercasedEmailSubstringsFromCredentials(t *testing.T) {
	const reflectedEmail = "Password@Example.net"
	tests := []struct {
		name                string
		description         string
		reflectedCredential string
	}{
		{
			name:                "mailbox query credential",
			description:         "owner@example.com---https://mail.example/inbox?pwd=Password%40Example.net",
			reflectedCredential: reflectedEmail,
		},
		{
			name:                "wrapped mailbox query credential",
			description:         "owner@example.com---https://mail.example/inbox?pwd=Password%40Example.net%21",
			reflectedCredential: reflectedEmail + "!",
		},
		{
			name:                "wrapped mailbox path credential",
			description:         "owner@example.com---https://mail.example/Password@Example.net!/inbox",
			reflectedCredential: reflectedEmail + "!",
		},
		{
			name:                "trailing hyphen mailbox query credential",
			description:         "owner@example.com---https://mail.example/inbox?pwd=Password%40Example.net-",
			reflectedCredential: reflectedEmail + "-",
		},
		{
			name:                "trailing dot mailbox path credential",
			description:         "owner@example.com---https://mail.example/Password@Example.net./inbox",
			reflectedCredential: reflectedEmail + ".",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest([]string{
				"Subject: OpenAI - Access Deactivated\n" +
					"Your OpenAI account has been deactivated and can no longer be used. " +
					"Associated with " + test.reflectedCredential + " 2026-07-21",
			}, "")}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: test.description,
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			serialized, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), reflectedEmail)
			require.NotContains(t, string(serialized), strings.ToLower(reflectedEmail))
		})
	}
}

func TestGroupAccountHealthDetectorSerializesEmptyEvidenceAsArray(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxResponse{
		contentType: "application/json",
		body:        []byte(`["An ordinary mailbox message without account health evidence."]`),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, result.Evidence)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(serialized), `"evidence":[]`)
}

func TestGroupAccountHealthDetectorSharesOneDeadlineAcrossMailboxPages(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{}
	deadlines := make([]time.Time, 0, 2)
	fetcher.fetch = func(ctx context.Context, _ string) (accountHealthMailboxResponse, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		deadlines = append(deadlines, deadline)
		if fetcher.calls == 1 {
			return accountHealthMailboxPageForTest(
				[]string{"First ordinary mailbox message with enough content to be classified safely."},
				"?page=2",
			), nil
		}
		return accountHealthMailboxPageForTest(
			[]string{"Second ordinary mailbox message with enough content to be classified safely."},
			"",
		), nil
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)
	detector.timeout = time.Second

	result, err := detector.DetectGroup(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 2, result.PagesScanned)
	require.Len(t, deadlines, 2)
	require.Equal(t, deadlines[0], deadlines[1])
}

func TestGroupAccountHealthDetectorReturnsStructuredFetchErrorOnInternalTimeout(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{}
	fetcher.fetch = func(ctx context.Context, _ string) (accountHealthMailboxResponse, error) {
		if fetcher.calls == 1 {
			return accountHealthMailboxPageForTest(
				[]string{"First ordinary mailbox message with enough content to be classified safely."},
				"?page=2",
			), nil
		}
		<-ctx.Done()
		return accountHealthMailboxResponse{}, ctx.Err()
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)
	detector.timeout = 20 * time.Millisecond

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, accountHealthStatusFetchError, result.Status)
	require.Equal(t, 1, result.MessagesScanned)
	require.Equal(t, 1, result.PagesScanned)
	require.Equal(t, 2, fetcher.calls)
	require.NotEmpty(t, result.CheckedAt)
	require.Empty(t, result.Evidence)
}

func TestGroupAccountHealthDetectorReturnsStructuredFetchErrorWhileWaitingForConcurrencyGates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *groupAccountHealthDetector) func()
	}{
		{
			name: "per-host gate",
			setup: func(t *testing.T, detector *groupAccountHealthDetector) func() {
				releaseFirst, err := detector.acquire(context.Background(), "mail.example")
				require.NoError(t, err)
				releaseSecond, err := detector.acquire(context.Background(), "mail.example")
				require.NoError(t, err)
				return func() {
					releaseFirst()
					releaseSecond()
				}
			},
		},
		{
			name: "global gate",
			setup: func(t *testing.T, detector *groupAccountHealthDetector) func() {
				detector.global = make(chan struct{}, 1)
				release, err := detector.acquire(context.Background(), "occupied.example")
				require.NoError(t, err)
				return release
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
				[]string{"An ordinary mailbox message without account health evidence."},
				"",
			)}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com",
			}, fetcher)
			releaseOccupied := test.setup(t, detector)
			t.Cleanup(releaseOccupied)
			detector.timeout = 50 * time.Millisecond

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, accountHealthStatusFetchError, result.Status)
			require.Zero(t, result.MessagesScanned)
			require.Zero(t, result.PagesScanned)
			require.NotEmpty(t, result.CheckedAt)
			require.Empty(t, result.Evidence)
			require.Zero(t, fetcher.calls)

			releaseOccupied()
			detector.timeout = time.Second
			result, err = detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, accountHealthStatusNotFound, result.Status)
			require.Equal(t, 1, fetcher.calls)
			require.Empty(t, detector.global)
			detector.hostMu.Lock()
			hostGateCount := len(detector.hostGates)
			detector.hostMu.Unlock()
			require.Zero(t, hostGateCount)
		})
	}
}

func TestGroupAccountHealthDetectorFindsEvidenceOnSecondPage(t *testing.T) {
	tests := []struct {
		name       string
		secondPage string
		nextLink   string
		verify     func(*testing.T, *GroupAccountHealthDetection)
	}{
		{
			name:       "deactivation",
			secondPage: accountHealthTestBan + "\n2026-07-21 12:00:00",
			nextLink:   "?page=2",
			verify: func(t *testing.T, result *GroupAccountHealthDetection) {
				require.Equal(t, accountHealthStatusDeactivated, result.Status)
				require.Equal(t, "2026-07-21 12:00:00", result.BanDate)
			},
		},
		{
			name:       "plus subscription",
			secondPage: accountHealthTestPlus,
			nextLink:   "https://mail.example/inbox?page=2",
			verify: func(t *testing.T, result *GroupAccountHealthDetection) {
				require.Equal(t, accountHealthStatusNotFound, result.Status)
				require.True(t, result.PlusDetected)
				require.Equal(t, "UPI", result.PaymentMethod)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
				accountHealthMailboxPageForTest(
					[]string{"OpenAI verification code. This is an ordinary mailbox message with no health evidence."},
					test.nextLink,
				),
				accountHealthMailboxPageForTest([]string{test.secondPage}, ""),
			}}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:       42,
				Name:     "OpenAI primary",
				Platform: PlatformOpenAI,
				Description: "user@example.com---" +
					"https://mail.example/inbox?mail=user%40example.com&pwd=secret&limit=5",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, 2, fetcher.calls)
			require.Equal(t, 2, result.PagesScanned)
			require.Equal(t, 2, result.MessagesScanned)
			test.verify(t, result)

			secondURL, parseErr := url.Parse(fetcher.rawURLs[1])
			require.NoError(t, parseErr)
			require.Equal(t, "mail.example", secondURL.Hostname())
			require.Equal(t, "2", secondURL.Query().Get("page"))
			require.Equal(t, "user@example.com", secondURL.Query().Get("mail"))
			require.Equal(t, "secret", secondURL.Query().Get("pwd"))
			require.Equal(t, "50", secondURL.Query().Get("limit"))
		})
	}
}

func TestGroupAccountHealthDetectorIgnoresPaginationLinksInsideMessageContent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "article descendant",
			body: `<html><body><article>Ordinary mailbox message with enough content to be classified safely.
<a rel="next" href="?page=2">Next</a></article></body></html>`,
		},
		{
			name: "message container descendant",
			body: `<html><body><div class="mail-item-row">Ordinary mailbox message with enough content to be classified safely.
<a rel="next" href="?page=2">Next</a></div></body></html>`,
		},
		{
			name: "link wrapping article",
			body: `<html><body><a rel="next" href="?page=2"><article>
Ordinary mailbox message with enough content to be classified safely.</article>Next</a></body></html>`,
		},
		{
			name: "heading fallback",
			body: `<html><body><section><h2>Ordinary mailbox message</h2><p>
This body contains enough ordinary content for heading-based message extraction.
<a rel="next" href="?page=2">Next</a></p></section></body></html>`,
		},
		{
			name: "whole page fallback",
			body: `<html><body><div>Ordinary mailbox message with enough unstructured content to be classified safely.
<a rel="next" href="?page=2">Next</a></div></body></html>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxResponse{
				body:        []byte(test.body),
				contentType: "text/html; charset=utf-8",
			}}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=synthetic-password",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, 1, fetcher.calls)
			require.Equal(t, 1, result.PagesScanned)
			require.NotContains(t, strings.Join(fetcher.rawURLs, "\n"), "page=2")
		})
	}
}

func TestGroupAccountHealthDetectorResolvesRelativeNextLinkAgainstCurrentPage(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		accountHealthMailboxPageForTest(
			[]string{"First ordinary mailbox message with enough content to be classified safely."},
			"archive/page-2",
		),
		accountHealthMailboxPageForTest(
			[]string{"Second ordinary mailbox message with enough content to be classified safely."},
			"",
		),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/mail/inbox",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 2, result.PagesScanned)
	require.Len(t, fetcher.rawURLs, 2)
	secondURL, parseErr := url.Parse(fetcher.rawURLs[1])
	require.NoError(t, parseErr)
	require.Equal(t, "/mail/archive/page-2", secondURL.Path)
}

func TestGroupAccountHealthDetectorResolvesRelativeNextLinkAgainstRedirectedPage(t *testing.T) {
	firstPage := accountHealthMailboxPageForTest(
		[]string{"First ordinary mailbox message with enough content to be classified safely."},
		"?page=2",
	)
	firstPage.visitedURLs = []string{
		"https://mail.example/inbox?mail=user%40example.com&pwd=secret&limit=50",
		"https://mail.example/mailboxes/user/inbox?mail=user%40example.com&pwd=secret&limit=50",
	}
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		firstPage,
		accountHealthMailboxPageForTest(
			[]string{"Second ordinary mailbox message with enough content to be classified safely."},
			"",
		),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 2, result.PagesScanned)
	require.Len(t, fetcher.rawURLs, 2)
	secondURL, parseErr := url.Parse(fetcher.rawURLs[1])
	require.NoError(t, parseErr)
	require.Equal(t, "/mailboxes/user/inbox", secondURL.Path)
	require.Equal(t, "2", secondURL.Query().Get("page"))
	require.Equal(t, "user@example.com", secondURL.Query().Get("mail"))
	require.Equal(t, "secret", secondURL.Query().Get("pwd"))
	require.Equal(t, "50", secondURL.Query().Get("limit"))
}

func TestGroupAccountHealthDetectorStopsAtVisitedNextLink(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		accountHealthMailboxPageForTest(
			[]string{"First ordinary mailbox message with enough content to be classified safely."},
			"?page=2",
		),
		accountHealthMailboxPageForTest(
			[]string{"Second ordinary mailbox message with enough content to be classified safely."},
			"https://MAIL.EXAMPLE:443/inbox?pwd=secret&page=2&mail=user%40example.com",
		),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 2, fetcher.calls)
	require.Equal(t, 2, result.PagesScanned)
	require.Equal(t, 2, result.MessagesScanned)
}

func TestGroupAccountHealthDetectorFollowsSameHostNextPageAcrossHTTPSPort(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		accountHealthMailboxPageForTest(
			[]string{"First ordinary mailbox message with enough content to be classified safely."},
			"https://mail.example:444/inbox?page=2",
		),
		accountHealthMailboxPageForTest(
			[]string{"Second ordinary mailbox message with enough content to be classified safely."},
			"",
		),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 2, result.PagesScanned)
	require.Len(t, fetcher.rawURLs, 2)
	secondURL, parseErr := url.Parse(fetcher.rawURLs[1])
	require.NoError(t, parseErr)
	require.Equal(t, "mail.example", secondURL.Hostname())
	require.Equal(t, "444", secondURL.Port())
	require.Equal(t, "2", secondURL.Query().Get("page"))
	require.Empty(t, secondURL.Query().Get("mail"))
	require.Empty(t, secondURL.Query().Get("pwd"))
}

func TestGroupAccountHealthDetectorRejectsUnsafeNextPageHosts(t *testing.T) {
	tests := []struct {
		name     string
		nextLink string
	}{
		{name: "cross host", nextLink: "https://other.example/inbox?page=2&pwd=do-not-send"},
		{name: "HTTP downgrade", nextLink: "http://mail.example/inbox?page=2"},
		{name: "zero port", nextLink: "https://mail.example:0/inbox?page=2"},
		{name: "oversized port", nextLink: "https://mail.example:65536/inbox?page=2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
				[]string{"An ordinary mailbox message with enough content to be classified safely."},
				test.nextLink,
			)}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, 1, fetcher.calls)
			require.Equal(t, 1, result.PagesScanned)
			require.NotContains(t, strings.Join(fetcher.rawURLs, ""), "other.example")
			require.NotContains(t, strings.Join(fetcher.rawURLs, ""), "do-not-send")
		})
	}
}

func TestGroupAccountHealthDetectorRejectsNonPaginationNextPageQueries(t *testing.T) {
	tests := []struct {
		name     string
		nextLink string
	}{
		{name: "operation only", nextLink: "?action=delete"},
		{name: "operation with page", nextLink: "?page=2&action=delete"},
		{name: "changed credential with page", nextLink: "?page=2&pwd=attacker-value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
				[]string{"An ordinary mailbox message with enough content to be classified safely."},
				test.nextLink,
			)}
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=synthetic-password",
			}, fetcher)

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.Equal(t, 1, fetcher.calls)
			require.Equal(t, 1, result.PagesScanned)
			require.NotContains(t, strings.Join(fetcher.rawURLs, "\n"), "action=delete")
			require.NotContains(t, strings.Join(fetcher.rawURLs, "\n"), "attacker-value")
		})
	}
}

func TestGroupAccountHealthDetectorDeduplicatesAndCapsMessagesAcrossPages(t *testing.T) {
	firstPage := make([]string, 0, 30)
	secondPage := make([]string, 0, 31)
	for index := 0; index < 30; index++ {
		firstPage = append(firstPage, fmt.Sprintf("Ordinary mailbox message %02d from the first page with no health evidence.", index))
	}
	secondPage = append(secondPage, firstPage[0])
	for index := 30; index < 60; index++ {
		secondPage = append(secondPage, fmt.Sprintf("Ordinary mailbox message %02d from the second page with no health evidence.", index))
	}
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		accountHealthMailboxPageForTest(firstPage, "?page=2"),
		accountHealthMailboxPageForTest(secondPage, ""),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 2, result.PagesScanned)
	require.Equal(t, accountHealthMailboxLimit, result.MessagesScanned)
}

func TestGroupAccountHealthDetectorAccumulatesDynamicHintAcrossPages(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
		{
			body:        []byte(`<html><body><script>loadMailbox()</script><article>short first mail</article><a rel="next" href="?page=2">Next</a></body></html>`),
			contentType: "text/html; charset=utf-8",
		},
		accountHealthMailboxPageForTest([]string{"short second mail"}, ""),
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusParseError, result.Status)
	require.Equal(t, 2, result.PagesScanned)
	require.Equal(t, 2, result.MessagesScanned)
}

func TestGroupAccountHealthDetectorReportsMalformedJSONAsParseError(t *testing.T) {
	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxResponse{
		body:        []byte(`{"messages":["OpenAI account has been deactivated"`),
		contentType: "application/json",
	}}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusParseError, result.Status)
	require.Empty(t, result.Evidence)
}

func TestGroupAccountHealthDetectorCapsPagination(t *testing.T) {
	responses := make([]accountHealthMailboxResponse, 0, accountHealthMailboxPageLimit)
	for page := 1; page <= accountHealthMailboxPageLimit; page++ {
		responses = append(responses, accountHealthMailboxPageForTest(
			[]string{fmt.Sprintf("Ordinary mailbox message from page %02d with no health evidence.", page)},
			fmt.Sprintf("?page=%d", page+1),
		))
	}
	fetcher := &accountHealthMailboxFetcherStub{responses: responses}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthMailboxPageLimit, fetcher.calls)
	require.Equal(t, accountHealthMailboxPageLimit, result.PagesScanned)
	require.Equal(t, accountHealthMailboxPageLimit, result.MessagesScanned)
}

func TestGroupAccountHealthDetectorReturnsStableFailureForLaterPage(t *testing.T) {
	t.Run("fetch error", func(t *testing.T) {
		fetcher := &accountHealthMailboxFetcherStub{
			responses: []accountHealthMailboxResponse{
				accountHealthMailboxPageForTest(
					[]string{"First ordinary mailbox message with enough content to be classified safely."},
					"?page=2",
				),
			},
			errs: []error{nil, errors.New("GET https://mail.example/inbox?pwd=do-not-leak failed")},
		}
		detector := newAccountHealthDetectorForTest(&Group{
			ID:          42,
			Name:        "OpenAI primary",
			Platform:    PlatformOpenAI,
			Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
		}, fetcher)

		result, err := detector.DetectGroup(context.Background(), 42)

		require.NoError(t, err)
		require.Equal(t, accountHealthStatusFetchError, result.Status)
		require.Equal(t, 1, result.PagesScanned)
		require.Equal(t, 1, result.MessagesScanned)
		serialized, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(serialized), "do-not-leak")
	})

	t.Run("parse error", func(t *testing.T) {
		fetcher := &accountHealthMailboxFetcherStub{responses: []accountHealthMailboxResponse{
			accountHealthMailboxPageForTest(
				[]string{"First ordinary mailbox message with enough content to be classified safely."},
				"?page=2",
			),
			{body: make([]byte, accountHealthMaxMailboxBytes+1), contentType: "text/html"},
		}}
		detector := newAccountHealthDetectorForTest(&Group{
			ID:          42,
			Name:        "OpenAI primary",
			Platform:    PlatformOpenAI,
			Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
		}, fetcher)

		result, err := detector.DetectGroup(context.Background(), 42)

		require.NoError(t, err)
		require.Equal(t, accountHealthStatusParseError, result.Status)
		require.Equal(t, 2, result.PagesScanned)
		require.Equal(t, 1, result.MessagesScanned)
		require.Empty(t, result.Evidence)
	})
}

func TestGroupAccountHealthDetectorPropagatesCancellationBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &accountHealthMailboxFetcherStub{}
	fetcher.fetch = func(context.Context, string) (accountHealthMailboxResponse, error) {
		cancel()
		return accountHealthMailboxPageForTest(
			[]string{"First ordinary mailbox message with enough content to be classified safely."},
			"?page=2",
		), nil
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(ctx, 42)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, fetcher.calls)
}

func TestGroupAccountHealthDetectorPropagatesCancellationAfterFinalFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &accountHealthMailboxFetcherStub{}
	fetcher.fetch = func(context.Context, string) (accountHealthMailboxResponse, error) {
		cancel()
		return accountHealthMailboxPageForTest(
			[]string{"Final ordinary mailbox message with enough content to be classified safely."},
			"",
		), nil
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(ctx, 42)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, fetcher.calls)
}

func TestGroupAccountHealthDetectorReturnsStableFetchError(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{err: errors.New("GET https://mail.example/?pwd=secret failed")}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusFetchError, result.Status)
	require.Empty(t, result.Evidence)
}

func TestGroupAccountHealthDetectorRedactsRedirectPathSecrets(t *testing.T) {
	t.Parallel()

	mailbox := accountHealthMailboxPageForTest([]string{
		"OpenAI account redirectToken123456 and redirect%2Ftoken%3a123456 access deactivated\nYour account has been deactivated and can no longer be used.",
		"You've successfully subscribed to ChatGPT Plus.\nPayment method: redirectToken123456 redirect%2Ftoken%3a123456",
	}, "")
	mailbox.visitedURLs = []string{
		"https://mail.example/inbox?mail=user%40example.com&pwd=secret",
		"https://mail.example/redirectToken123456/inbox",
		"https://mail.example/redirect%2ftoken%3A123456/inbox",
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, &accountHealthMailboxFetcherStub{response: mailbox})

	result, err := detector.DetectGroup(context.Background(), 42)
	require.NoError(t, err)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "redirectToken123456")
	require.NotContains(t, string(serialized), "redirect%2Ftoken%3a123456")
}

func TestGroupAccountHealthDetectorRejectsRedirectRedactionBudgetOverflow(t *testing.T) {
	t.Parallel()

	mailbox := accountHealthMailboxPageForTest([]string{
		"OpenAI reflected-path-token access deactivated\nYour account has been deactivated and can no longer be used.",
	}, "")
	mailbox.visitedURLs = []string{accountHealthOverflowURLForTest("reflected-path-token")}
	fetcher := &accountHealthMailboxFetcherStub{response: mailbox}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=secret",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusParseError, result.Status)
	require.Equal(t, 1, fetcher.calls)
	require.Empty(t, result.AssociatedEmail)
	require.Empty(t, result.Evidence)
}

func TestGroupAccountHealthDetectorRedactsShortAndNormalizedPathSecrets(t *testing.T) {
	t.Parallel()

	mailbox := accountHealthMailboxPageForTest([]string{
		"OpenAI account s3cr3t and ＡＢＣ１２３ access deactivated\nYour account has been deactivated and can no longer be used.",
		"You've successfully subscribed to ChatGPT Plus.\nPayment method: s3cr3t ＡＢＣ１２３",
	}, "")
	mailbox.visitedURLs = []string{
		"https://mail.example/s3cr3t/%EF%BC%A1%EF%BC%A2%EF%BC%A3%EF%BC%91%EF%BC%92%EF%BC%93/inbox?mail=user%40example.com",
	}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com",
	}, &accountHealthMailboxFetcherStub{response: mailbox})

	result, err := detector.DetectGroup(context.Background(), 42)
	require.NoError(t, err)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "s3cr3t")
	require.NotContains(t, string(serialized), "ABC123")
}

func TestGroupAccountHealthDetectorRedactsOriginalPercentEncodedQuerySecrets(t *testing.T) {
	t.Parallel()

	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "OpenAI primary",
		Platform:    PlatformOpenAI,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com&pwd=abc%2fdef%3Aghi&limit=5",
	}, &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest([]string{
		"OpenAI account abc%2fdef%3Aghi, abc%2Fdef%3Aghi, and abc%2Fdef%3aghi access deactivated\nYour account has been deactivated and can no longer be used.",
		"You've successfully subscribed to ChatGPT Plus.\nPayment method: abc%2fdef%3Aghi abc%2Fdef%3aghi",
	}, "")})

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "abc%2fdef%3Aghi")
	require.NotContains(t, string(serialized), "abc%2Fdef%3Aghi")
	require.NotContains(t, string(serialized), "abc%2Fdef%3aghi")
}

func TestGroupAccountHealthDetectorRedactsBareQuerySecrets(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		rawQuery    string
		reflections []string
	}{
		{name: "plain", rawQuery: "opaqueToken123456", reflections: []string{"opaqueToken123456"}},
		{name: "percent encoded", rawQuery: "opaque%54oken123456", reflections: []string{"opaque%54oken123456", "opaqueToken123456"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			paymentMethod := strings.Join(test.reflections, " ")
			detector := newAccountHealthDetectorForTest(&Group{
				ID:          42,
				Name:        "OpenAI primary",
				Platform:    PlatformOpenAI,
				Description: "user@example.com---https://mail.example/inbox?" + test.rawQuery,
			}, &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest([]string{
				"You've successfully subscribed to ChatGPT Plus.\nPayment method: " + paymentMethod,
			}, "")})

			result, err := detector.DetectGroup(context.Background(), 42)

			require.NoError(t, err)
			require.True(t, result.PlusDetected)
			serialized, err := json.Marshal(result)
			require.NoError(t, err)
			for _, reflection := range test.reflections {
				require.NotContains(t, string(serialized), reflection)
			}
		})
	}
}

func TestGroupAccountHealthDetectorRejectsNonOpenAIGroup(t *testing.T) {
	t.Parallel()

	detector := newAccountHealthDetectorForTest(&Group{
		ID:       42,
		Name:     "Anthropic",
		Platform: PlatformAnthropic,
	}, &accountHealthMailboxFetcherStub{})

	result, err := detector.DetectGroup(context.Background(), 42)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrAccountHealthUnsupportedGroup)
}

func TestGroupAccountHealthDetectorAllowsDisabledOpenAIGroup(t *testing.T) {
	t.Parallel()

	fetcher := &accountHealthMailboxFetcherStub{response: accountHealthMailboxPageForTest(
		[]string{"An ordinary mailbox message without account health evidence."},
		"",
	)}
	detector := newAccountHealthDetectorForTest(&Group{
		ID:          42,
		Name:        "Disabled OpenAI group",
		Platform:    PlatformOpenAI,
		Status:      StatusDisabled,
		Description: "user@example.com---https://mail.example/inbox?mail=user%40example.com",
	}, fetcher)

	result, err := detector.DetectGroup(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, accountHealthStatusNotFound, result.Status)
	require.Equal(t, 1, fetcher.calls)
}

func TestAccountHealthAcquireDoesNotLetOneHostConsumeWaitingGlobalSlots(t *testing.T) {
	detector := &groupAccountHealthDetector{
		global:    make(chan struct{}, 3),
		hostGates: make(map[string]*accountHealthHostGate),
	}

	releaseFirst, err := detector.acquire(context.Background(), "mail-a.example")
	require.NoError(t, err)
	defer releaseFirst()
	releaseSecond, err := detector.acquire(context.Background(), "mail-a.example")
	require.NoError(t, err)
	defer releaseSecond()

	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	waitingResult := make(chan error, 1)
	go func() {
		release, acquireErr := detector.acquire(waitingCtx, "mail-a.example")
		if release != nil {
			release()
		}
		waitingResult <- acquireErr
	}()
	require.Eventually(t, func() bool {
		detector.hostMu.Lock()
		defer detector.hostMu.Unlock()
		gate := detector.hostGates["mail-a.example"]
		return gate != nil && gate.refs == 3
	}, time.Second, 10*time.Millisecond)

	otherCtx, cancelOther := context.WithTimeout(context.Background(), time.Second)
	defer cancelOther()
	releaseOther, err := detector.acquire(otherCtx, "mail-b.example")
	require.NoError(t, err)
	releaseOther()

	cancelWaiting()
	require.ErrorIs(t, <-waitingResult, context.Canceled)
}

func TestNewAdminServiceSharesHostLimitAcrossDistinctGroupRequests(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	fetcher := &accountHealthBlockingFetcher{
		started: make(chan struct{}, 3),
		release: release,
	}
	groupRepo := &accountHealthConcurrentGroupRepoStub{groups: map[int64]*Group{
		1: {
			ID:          1,
			Name:        "OpenAI one",
			Platform:    PlatformOpenAI,
			Description: "one@example.com---https://mail.example/inbox?mail=one%40example.com",
		},
		2: {
			ID:          2,
			Name:        "OpenAI two",
			Platform:    PlatformOpenAI,
			Description: "two@example.com---https://mail.example/inbox?mail=two%40example.com",
		},
		3: {
			ID:          3,
			Name:        "OpenAI three",
			Platform:    PlatformOpenAI,
			Description: "three@example.com---https://mail.example/inbox?mail=three%40example.com",
		},
	}}
	adminService := NewAdminService(
		nil, groupRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	implementation, ok := adminService.(*adminServiceImpl)
	require.True(t, ok)
	detector, ok := implementation.accountHealthDetector.(*groupAccountHealthDetector)
	require.True(t, ok)
	detector.fetcher = fetcher

	results := make(chan error, 3)
	for groupID := int64(1); groupID <= 3; groupID++ {
		go func(id int64) {
			_, err := detector.DetectGroup(context.Background(), id)
			results <- err
		}(groupID)
	}
	for range 2 {
		select {
		case <-fetcher.started:
		case <-time.After(time.Second):
			t.Fatal("two same-host detections did not reach the fetch boundary")
		}
	}
	select {
	case <-fetcher.started:
		t.Fatal("third same-host detection bypassed the per-host gate")
	case <-time.After(75 * time.Millisecond):
	}

	close(release)
	released = true
	for range 3 {
		require.NoError(t, <-results)
	}
	require.Equal(t, int32(accountHealthHostConcurrency), fetcher.peak.Load())
}

func TestAccountHealthAcquireEnforcesGlobalLimitAndCleansCanceledWaiter(t *testing.T) {
	detector := &groupAccountHealthDetector{
		global:    make(chan struct{}, 2),
		hostGates: make(map[string]*accountHealthHostGate),
	}

	releaseFirst, err := detector.acquire(context.Background(), "mail-a.example")
	require.NoError(t, err)
	defer releaseFirst()
	releaseSecond, err := detector.acquire(context.Background(), "mail-b.example")
	require.NoError(t, err)
	defer releaseSecond()

	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	waitingResult := make(chan error, 1)
	go func() {
		release, acquireErr := detector.acquire(waitingCtx, "mail-c.example")
		if release != nil {
			release()
		}
		waitingResult <- acquireErr
	}()
	require.Eventually(t, func() bool {
		detector.hostMu.Lock()
		defer detector.hostMu.Unlock()
		gate := detector.hostGates["mail-c.example"]
		return gate != nil && gate.refs == 1
	}, time.Second, 10*time.Millisecond)
	select {
	case acquireErr := <-waitingResult:
		require.Failf(t, "global limit was bypassed", "acquire returned early: %v", acquireErr)
	case <-time.After(50 * time.Millisecond):
	}

	cancelWaiting()
	require.ErrorIs(t, <-waitingResult, context.Canceled)
	require.Len(t, detector.global, 2)
	detector.hostMu.Lock()
	_, canceledGateStillPresent := detector.hostGates["mail-c.example"]
	detector.hostMu.Unlock()
	require.False(t, canceledGateStillPresent)

	releaseFirst()
	releaseSecond()
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	releaseRetry, err := detector.acquire(retryCtx, "mail-c.example")
	require.NoError(t, err)
	releaseRetry()
	require.Empty(t, detector.global)
	detector.hostMu.Lock()
	require.Empty(t, detector.hostGates)
	detector.hostMu.Unlock()
}

func TestAccountHealthMailboxHostNormalizesTrailingDot(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		accountHealthMailboxHost("https://MAIL.EXAMPLE/inbox"),
		accountHealthMailboxHost("https://mail.example./inbox"),
	)
}

func TestRedactGroupAccountHealthDetectionRemovesURLsAndCredentials(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		MessageDate:     "password=alpha;verysecret@example.com key=pathKey123456 code=mailCode123456 sessionTicketXYZ purealphabetic https://mail.example/inbox?pwd=secret&token=abc",
		PaymentMethod:   "q xy sessionTicketXYZ purealphabetic",
		Evidence:        []string{"secret=visible mailCode123456 q xy sessionTicketXYZ purealphabetic"},
		AssociatedEmail: "alpha;verysecret@example.com",
	}
	redactGroupAccountHealthDetection(
		result,
		"https://mail.example/pathKey123456/purealphabetic/inbox?mail=user%40example.com&pwd=alpha%3Bverysecret%40example.com&pwd=q&token=abc&code=mailCode123456&session_id=xy&ticket=sessionTicketXYZ",
	)

	serialized := result.MessageDate + result.PaymentMethod + result.AssociatedEmail + strings.Join(result.Evidence, "")
	require.NotContains(t, serialized, "https://")
	require.NotContains(t, serialized, "abc")
	require.NotContains(t, serialized, "alpha")
	require.NotContains(t, serialized, "visible")
	require.NotContains(t, serialized, "pathKey123456")
	require.NotContains(t, serialized, "mailCode123456")
	require.NotContains(t, serialized, "verysecret")
	require.NotContains(t, serialized, "sessionTicketXYZ")
	require.NotContains(t, serialized, "purealphabetic")
	require.NotContains(t, serialized, "xy")
	require.NotContains(t, serialized, "q")
}

func TestRedactGroupAccountHealthDetectionPreservesKnownEvidence(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		Evidence: []string{
			accountHealthEvidenceDynamicMailbox,
			accountHealthEvidenceAccountMismatch,
			accountHealthEvidenceAccountUnavailable,
		},
	}
	require.True(t, redactGroupAccountHealthDetection(
		result,
		"https://mail.example/mail/account?mail=user%40example.com&pwd=secret",
	))
	require.Equal(t, []string{
		accountHealthEvidenceDynamicMailbox,
		accountHealthEvidenceAccountMismatch,
		accountHealthEvidenceAccountUnavailable,
	}, result.Evidence)
}

func TestRedactGroupAccountHealthDetectionRemovesFullAuthorizationValue(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		MessageDate:   "OpenAI Authorization: Bearer reflected-token another-fragment",
		PaymentMethod: "Bearer payment-token.with-segments",
		BanDate:       "Basic dXNlcjpwYXNzd29yZA==",
		Evidence:      []string{"Authorization=Basic reflected-basic-value"},
	}
	redactGroupAccountHealthDetection(result, "https://mail.example/inbox?mail=user%40example.com")

	require.Equal(t, "OpenAI Authorization: ***", result.MessageDate)
	require.Empty(t, result.PaymentMethod)
	require.Equal(t, "Basic ***", result.BanDate)
	require.Equal(t, []string{"Authorization=***"}, result.Evidence)
}

func TestRedactGroupAccountHealthDetectionRemovesQuotedAndMultiwordCredentials(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		PaymentMethod: `password: "alpha beta"; Bearer "token value"`,
		BanDate:       "Basic 'dXNl cjpwYXNz'",
		Evidence:      []string{"token=part one two"},
	}
	redactGroupAccountHealthDetection(result, "https://mail.example/inbox?mail=user%40example.com")

	require.Empty(t, result.PaymentMethod)
	require.Equal(t, "Basic ***", result.BanDate)
	require.Equal(t, []string{"token=***"}, result.Evidence)
}

func TestRedactGroupAccountHealthDetectionRemovesUnknownShortQueryValues(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		MessageDate:   "x x yz yz ordinary subject",
		PaymentMethod: "x x yz yz ordinary payment",
		Evidence:      []string{"x x yz yz ordinary evidence"},
	}
	redactGroupAccountHealthDetection(
		result,
		"https://mail.example/inbox?mail=user%40example.com&limit=50&opaque=x&custom=yz",
	)

	require.Equal(t, "*** *** *** *** ordinary subject", result.MessageDate)
	require.Empty(t, result.PaymentMethod)
	require.Equal(t, []string{"*** *** *** *** ordinary evidence"}, result.Evidence)
}

func TestRedactAccountHealthSecretPreservesEmbeddedShortValues(t *testing.T) {
	t.Parallel()

	require.Equal(t, "prefixx *** x1 1x ***", redactAccountHealthSecret("prefixx x x1 1x x", "x"))
	require.Equal(t, "fuzzy *** yz2 2yz ***", redactAccountHealthSecret("fuzzy yz yz2 2yz yz", "yz"))
}

func TestRedactGroupAccountHealthDetectionFailsClosedOnSecretBudgetOverflow(t *testing.T) {
	t.Parallel()

	result := &GroupAccountHealthDetection{
		Language:        "reflected-path-token",
		MessageDate:     "reflected-path-token",
		AssociatedEmail: "reflected-path-token@example.com",
		PlusLanguage:    "reflected-path-token",
		PlusDate:        "reflected-path-token",
		PaymentMethod:   "reflected-path-token",
		BanDate:         "reflected-path-token",
		Evidence:        []string{"reflected-path-token"},
	}

	complete := redactGroupAccountHealthDetection(result, accountHealthOverflowURLForTest("reflected-path-token"))

	require.False(t, complete)
	require.Empty(t, result.Language)
	require.Empty(t, result.MessageDate)
	require.Empty(t, result.AssociatedEmail)
	require.Empty(t, result.PlusLanguage)
	require.Empty(t, result.PlusDate)
	require.Empty(t, result.PaymentMethod)
	require.Equal(t, string(accountHealthLifespanUnavailable), result.LifespanStatus)
	require.Nil(t, result.LifespanSeconds)
	require.Empty(t, result.BanDate)
	require.Empty(t, result.Evidence)
}
