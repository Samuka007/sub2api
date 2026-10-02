package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type oneClickAccountNotesRepositoryStub struct {
	targets    []OneClickAccountNoteTarget
	listFunc   func([]string) ([]OneClickAccountNoteTarget, error)
	listErr    error
	applyErr   error
	applyCalls int
	listNames  []string
	matchNames []string
	updates    []OneClickAccountNoteUpdate
}

func (r *oneClickAccountNotesRepositoryStub) ListOneClickAccountNoteTargets(_ context.Context, matchNames []string) ([]OneClickAccountNoteTarget, error) {
	r.listNames = append([]string(nil), matchNames...)
	if r.listFunc != nil {
		return r.listFunc(matchNames)
	}
	if r.listErr != nil {
		return nil, r.listErr
	}
	targets := make([]OneClickAccountNoteTarget, len(r.targets))
	for index, target := range r.targets {
		targets[index] = target
		targets[index].Notes = cloneOneClickAccountNotesString(target.Notes)
	}
	return targets, nil
}

func (r *oneClickAccountNotesRepositoryStub) ApplyOneClickAccountNotes(
	_ context.Context,
	matchNames []string,
	updates []OneClickAccountNoteUpdate,
) error {
	r.applyCalls++
	r.matchNames = append([]string(nil), matchNames...)
	r.updates = make([]OneClickAccountNoteUpdate, len(updates))
	for index, update := range updates {
		r.updates[index] = update
		r.updates[index].ExpectedNotes = cloneOneClickAccountNotesString(update.ExpectedNotes)
	}
	return r.applyErr
}

func TestPreviewOneClickAccountNotesCountsDuplicatesAndRedactsSensitiveInput(t *testing.T) {
	existing := "existing-note-canary"
	line := "  USER+tag@Example.COM---https://mail.example/open?token=url-secret  "
	content := []byte("\xef\xbb\xbf" + line + "\r\n" + line + "\n\nmissing@example.com----https://mail.example/missing\nnot-an-email url-secret-two")
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 9, Name: "user+tag@example.com", Notes: &existing},
		{ID: 3, Name: "USER+TAG@EXAMPLE.COM", Notes: &line},
		{ID: 12, Name: "user+tag@example.com ", Notes: nil},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.Len(t, preview.PreviewDigest, sha256HexLength)
	require.Equal(t, 4, preview.TotalLines)
	require.Equal(t, 3, preview.ValidLines)
	require.Equal(t, 1, preview.InvalidLines)
	require.Equal(t, 1, preview.DuplicateLines)
	require.Zero(t, preview.ConflictLines)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 1, preview.UnmatchedLines)
	require.Equal(t, 2, preview.MatchedAccounts)
	require.Equal(t, 1, preview.WillUpdateAccounts)
	require.Equal(t, 1, preview.UnchangedAccounts)
	require.True(t, preview.CanApply)
	require.Equal(t, []int{1, 2, 4, 5}, []int{
		preview.Entries[0].LineNumber,
		preview.Entries[1].LineNumber,
		preview.Entries[2].LineNumber,
		preview.Entries[3].LineNumber,
	})
	require.Equal(t, OneClickAccountNotesEntryStatusMatched, preview.Entries[0].Status)
	require.Equal(t, "u***g@example.com", preview.Entries[0].Email)
	require.Equal(t, 2, preview.Entries[0].MatchedAccounts)
	require.Equal(t, 1, preview.Entries[0].WillUpdateAccounts)
	require.Equal(t, OneClickAccountNotesEntryStatusDuplicate, preview.Entries[1].Status)
	require.Equal(t, OneClickAccountNotesReasonDuplicateIdenticalLine, preview.Entries[1].Reason)
	require.Equal(t, 1, preview.Entries[1].DuplicateOfLine)
	require.Equal(t, OneClickAccountNotesEntryStatusUnmatched, preview.Entries[2].Status)
	require.Equal(t, OneClickAccountNotesEntryStatusInvalid, preview.Entries[3].Status)
	require.Equal(t, OneClickAccountNotesReasonMissingEmail, preview.Entries[3].Reason)
	require.Empty(t, preview.Entries[3].Email)

	encoded, err := json.Marshal(preview)
	require.NoError(t, err)
	response := string(encoded)
	for _, secret := range []string{
		line,
		"user+tag@example.com",
		"USER+tag@Example.COM",
		"https://mail.example",
		"url-secret",
		"existing-note-canary",
	} {
		require.NotContains(t, response, secret)
	}
}

func TestPreviewOneClickAccountNotesMarksEveryDifferentLineForSameEmailAsConflict(t *testing.T) {
	content := []byte(strings.Join([]string{
		"owner@example.com---https://mail.example/first",
		"OWNER@example.com---https://mail.example/second",
	}, "\n"))
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{{ID: 1, Name: "owner@example.com"}}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.Equal(t, 2, preview.ValidLines)
	require.Equal(t, 2, preview.ConflictLines)
	require.Zero(t, preview.MatchedLines)
	require.Zero(t, preview.MatchedAccounts)
	require.False(t, preview.CanApply)
	for _, entry := range preview.Entries {
		require.Equal(t, OneClickAccountNotesEntryStatusConflict, entry.Status)
		require.Equal(t, OneClickAccountNotesReasonDuplicateEmailConflict, entry.Reason)
		require.Equal(t, "o***r@example.com", entry.Email)
	}

	_, err = svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.ErrorIs(t, err, ErrOneClickAccountNotesNotApplicable)
	require.Zero(t, repo.applyCalls)
}

func TestPreviewOneClickAccountNotesSkipsOversizedConflictingGroup(t *testing.T) {
	conflictFirst := "conflict@example.com---first"
	conflictSecond := "CONFLICT@example.com---second"
	validLine := "valid@example.com---valid note"
	repo := &oneClickAccountNotesRepositoryStub{
		listFunc: func(matchNames []string) ([]OneClickAccountNoteTarget, error) {
			for _, name := range matchNames {
				if name == "conflict@example.com" {
					return nil, ErrOneClickAccountNotesPlanTooLarge
				}
			}
			return []OneClickAccountNoteTarget{{ID: 7, Name: "valid@example.com"}}, nil
		},
	}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	content := []byte(strings.Join([]string{conflictFirst, conflictSecond, validLine}, "\n"))

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.Equal(t, []string{"valid@example.com"}, repo.listNames)
	require.Equal(t, 2, preview.ConflictLines)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 1, preview.MatchedAccounts)
	require.True(t, preview.CanApply)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, []string{"valid@example.com"}, repo.matchNames)
	require.Equal(t, validLine, repo.updates[0].Notes)
	require.Equal(t, 1, result.UpdatedAccounts)
}

func TestApplyOneClickAccountNotesPreservesExactLinesAndReportsCounts(t *testing.T) {
	unchangedLine := "owner@example.com---https://mail.example/existing?token=keep-this"
	changedLine := "  second@example.com----https://mail.example/new?token=new-secret  "
	oldNotes := "old-notes"
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 20, Name: "SECOND@EXAMPLE.COM", Notes: &oldNotes},
		{ID: 10, Name: "owner@example.com", Notes: &unchangedLine},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	content := []byte(unchangedLine + "\r\n" + changedLine + "\nno-account@example.com---https://mail.example/missing")

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 2, preview.MatchedLines)
	require.Equal(t, 1, preview.UnmatchedLines)
	require.Equal(t, 2, preview.MatchedAccounts)
	require.Equal(t, 1, preview.WillUpdateAccounts)
	require.Equal(t, 1, preview.UnchangedAccounts)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 1, repo.applyCalls)
	require.Equal(t, []string{"no-account@example.com", "owner@example.com", "second@example.com"}, repo.listNames)
	require.Equal(t, []string{"no-account@example.com", "owner@example.com", "second@example.com"}, repo.matchNames)
	require.Len(t, repo.updates, 2)
	require.Equal(t, int64(10), repo.updates[0].AccountID)
	require.Equal(t, unchangedLine, repo.updates[0].Notes)
	require.Equal(t, int64(20), repo.updates[1].AccountID)
	require.Equal(t, changedLine, repo.updates[1].Notes)
	require.Equal(t, oldNotes, *repo.updates[1].ExpectedNotes)
	require.Equal(t, &OneClickAccountNotesApplyResult{
		MatchedLines:      2,
		MatchedAccounts:   2,
		UpdatedAccounts:   1,
		UnchangedAccounts: 1,
		UnmatchedLines:    1,
		InvalidLines:      0,
		ConflictLines:     0,
	}, result)
}

func TestApplyOneClickAccountNotesSkipsInvalidAndConflictingLines(t *testing.T) {
	validLine := "valid@example.com---https://mail.example/valid"
	invalidLine := "this line has no email"
	conflictFirst := "conflict@example.com---https://mail.example/first"
	conflictSecond := "CONFLICT@example.com---https://mail.example/second"
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 7, Name: "VALID@EXAMPLE.COM"},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	content := []byte(strings.Join([]string{validLine, invalidLine, conflictFirst, conflictSecond}, "\n"))

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 1, preview.MatchedAccounts)
	require.Equal(t, 1, preview.WillUpdateAccounts)
	require.Equal(t, 1, preview.InvalidLines)
	require.Equal(t, 2, preview.ConflictLines)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 1, repo.applyCalls)
	require.Equal(t, []string{"valid@example.com"}, repo.listNames)
	require.Equal(t, []string{"valid@example.com"}, repo.matchNames)
	require.Len(t, repo.updates, 1)
	require.Equal(t, validLine, repo.updates[0].Notes)
	require.Equal(t, &OneClickAccountNotesApplyResult{
		MatchedLines:      1,
		MatchedAccounts:   1,
		UpdatedAccounts:   1,
		UnchangedAccounts: 0,
		UnmatchedLines:    0,
		InvalidLines:      1,
		ConflictLines:     2,
	}, result)
}

func TestApplyOneClickAccountNotesRejectsChangedFileOrTargetState(t *testing.T) {
	line := "owner@example.com---https://mail.example/open?token=secret"
	oldNotes := "old"
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{{ID: 1, Name: "owner@example.com", Notes: &oldNotes}}}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
	require.NoError(t, err)

	_, err = svc.ApplyOneClickAccountNotes(context.Background(), []byte(line+" "), preview.PreviewDigest)
	require.ErrorIs(t, err, ErrOneClickAccountNotesPreviewStale)
	require.Zero(t, repo.applyCalls)

	changedNotes := line
	repo.targets[0].Notes = &changedNotes
	_, err = svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
	require.ErrorIs(t, err, ErrOneClickAccountNotesPreviewStale)
	require.Zero(t, repo.applyCalls)

	repo.targets[0].Notes = &oldNotes
	repo.targets = append(repo.targets, OneClickAccountNoteTarget{ID: 2, Name: "OWNER@EXAMPLE.COM"})
	_, err = svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
	require.ErrorIs(t, err, ErrOneClickAccountNotesPreviewStale)
	require.Zero(t, repo.applyCalls)
}

func TestOneClickAccountNotesRequiresAtLeastOneActualUpdate(t *testing.T) {
	line := "owner@example.com---https://mail.example/open"
	tests := []struct {
		name    string
		targets []OneClickAccountNoteTarget
	}{
		{name: "all unmatched"},
		{name: "all unchanged", targets: []OneClickAccountNoteTarget{{ID: 1, Name: "owner@example.com", Notes: &line}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &oneClickAccountNotesRepositoryStub{targets: test.targets}
			svc := &adminServiceImpl{accountNoteRepo: repo}
			preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
			require.NoError(t, err)
			require.Zero(t, preview.WillUpdateAccounts)
			require.False(t, preview.CanApply)

			result, err := svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrOneClickAccountNotesNotApplicable)
			require.Zero(t, repo.applyCalls)
		})
	}
}

func TestOneClickAccountNotesDigestIsStableAcrossRepositoryOrderAndChangesOnlyForRelevantTargets(t *testing.T) {
	line := []byte("owner@example.com---https://mail.example/open")
	one := "one"
	two := "two"
	targets := []OneClickAccountNoteTarget{
		{ID: 2, Name: "OWNER@example.com", Notes: &two},
		{ID: 1, Name: "owner@example.com", Notes: &one},
		{ID: 3, Name: "irrelevant@example.net", Notes: &one},
	}
	repo := &oneClickAccountNotesRepositoryStub{targets: targets}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	first, err := svc.PreviewOneClickAccountNotes(context.Background(), line)
	require.NoError(t, err)

	repo.targets[0], repo.targets[2] = repo.targets[2], repo.targets[0]
	second, err := svc.PreviewOneClickAccountNotes(context.Background(), line)
	require.NoError(t, err)
	require.Equal(t, first.PreviewDigest, second.PreviewDigest)

	irrelevantChanged := "irrelevant changed"
	repo.targets[0].Notes = &irrelevantChanged
	third, err := svc.PreviewOneClickAccountNotes(context.Background(), line)
	require.NoError(t, err)
	require.Equal(t, first.PreviewDigest, third.PreviewDigest)

	for index := range repo.targets {
		if repo.targets[index].ID == 1 {
			repo.targets[index].Name = "Owner@example.com"
		}
	}
	fourth, err := svc.PreviewOneClickAccountNotes(context.Background(), line)
	require.NoError(t, err)
	require.NotEqual(t, first.PreviewDigest, fourth.PreviewDigest)
}

func TestPreviewOneClickAccountNotesUsesFirstBoundaryCompleteEmailAndExactASCIINameMatch(t *testing.T) {
	content := []byte("bad..candidate@example.com ; Owner@Example.COM---opaque suffix")
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 1, Name: "owner@example.com"},
		{ID: 2, Name: " owner@example.com"},
		{ID: 3, Name: "owner@example.com-suffix"},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 1, preview.MatchedAccounts)
	require.Equal(t, "o***r@example.com", preview.Entries[0].Email)
}

func TestFindOneClickAccountNotesEmailAcceptsCompleteEmailBeforeNonDomainDelimiters(t *testing.T) {
	for _, delimiter := range []string{"&token=secret", "/path", "+suffix", "'suffix"} {
		line := "owner@example.com" + delimiter + " ; second@example.net---note"
		require.Equal(t, "owner@example.com", findOneClickAccountNotesEmail(line), delimiter)
	}
}

func TestFindOneClickAccountNotesEmailRejectsDomainSubstringBeforeLaterEmail(t *testing.T) {
	for _, continuation := range []string{"-suffix", ".x", "@invalid.example"} {
		line := "owner@example.com" + continuation + " ; second@example.net---note"
		require.Equal(t, "second@example.net", findOneClickAccountNotesEmail(line), continuation)
	}
	require.Equal(
		t,
		"owner@example.com",
		findOneClickAccountNotesEmail("owner@example.com---fixed-format note"),
	)
}

func TestOneClickAccountNotesUsesFirstBoundaryCompleteEmailWhenOnlySecondMatches(t *testing.T) {
	line := "first.unmatched@example.com ; second.matched@example.net---opaque suffix"
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 2, Name: "second.matched@example.net"},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
	require.NoError(t, err)
	require.False(t, preview.CanApply)
	require.Zero(t, preview.MatchedLines)
	require.Zero(t, preview.MatchedAccounts)
	require.Equal(t, 1, preview.UnmatchedLines)
	require.Equal(t, OneClickAccountNotesEntryStatusUnmatched, preview.Entries[0].Status)
	require.Equal(t, []string{"first.unmatched@example.com"}, repo.listNames)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrOneClickAccountNotesNotApplicable)
	require.Zero(t, repo.applyCalls)
}

func TestOneClickAccountNotesUpdatesOnlyFirstBoundaryCompleteEmailWhenBothMatch(t *testing.T) {
	line := "First.Match@Example.com ; second.match@example.net---opaque suffix"
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 20, Name: "first.match@example.com"},
		{ID: 10, Name: "second.match@example.net"},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 1, preview.MatchedLines)
	require.Equal(t, 1, preview.MatchedAccounts)
	require.Equal(t, 1, preview.WillUpdateAccounts)
	require.Equal(t, []string{"first.match@example.com"}, repo.listNames)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedAccounts)
	require.Equal(t, 1, repo.applyCalls)
	require.Equal(t, []string{"first.match@example.com"}, repo.matchNames)
	require.Equal(t, []OneClickAccountNoteUpdate{{
		AccountID:    20,
		ExpectedName: "first.match@example.com",
		Notes:        line,
	}}, repo.updates)
}

func TestOneClickAccountNotesTreatsSentencePeriodAsEmailBoundary(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "terminal period", line: "first@example.com."},
		{name: "period before later email", line: "first@example.com. second@example.net---opaque note"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
				{ID: 20, Name: "first@example.com"},
				{ID: 10, Name: "second@example.net"},
			}}
			svc := &adminServiceImpl{accountNoteRepo: repo}

			preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(test.line))
			require.NoError(t, err)
			require.True(t, preview.CanApply)
			require.Equal(t, 1, preview.MatchedLines)
			require.Equal(t, 1, preview.MatchedAccounts)
			require.Equal(t, []string{"first@example.com"}, repo.listNames)

			result, err := svc.ApplyOneClickAccountNotes(context.Background(), []byte(test.line), preview.PreviewDigest)
			require.NoError(t, err)
			require.Equal(t, 1, result.UpdatedAccounts)
			require.Equal(t, []string{"first@example.com"}, repo.matchNames)
			require.Equal(t, []OneClickAccountNoteUpdate{{
				AccountID:    20,
				ExpectedName: "first@example.com",
				Notes:        test.line,
			}}, repo.updates)
		})
	}
}

func TestOneClickAccountNotesMatchesExtendedDotAtomEmailsWithoutSuffixWrites(t *testing.T) {
	content := []byte(strings.Join([]string{
		"o'connor@example.com---first note",
		"foo!bar@example.com---second note",
	}, "\n"))
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
		{ID: 1, Name: "o'connor@example.com"},
		{ID: 2, Name: "connor@example.com"},
		{ID: 3, Name: "foo!bar@example.com"},
		{ID: 4, Name: "bar@example.com"},
	}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 2, preview.MatchedAccounts)
	require.Equal(t, []string{"foo!bar@example.com", "o'connor@example.com"}, repo.listNames)

	_, err = svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 3}, []int64{repo.updates[0].AccountID, repo.updates[1].AccountID})
}

func TestApplyOneClickAccountNotesCollapsesIdenticalDuplicateLines(t *testing.T) {
	line := "owner@example.com---same note"
	content := []byte(line + "\n" + line)
	repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{{ID: 1, Name: "owner@example.com"}}}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), content)
	require.NoError(t, err)
	require.True(t, preview.CanApply)
	require.Equal(t, 1, preview.DuplicateLines)
	require.Equal(t, OneClickAccountNotesEntryStatusDuplicate, preview.Entries[1].Status)
	require.Equal(t, 1, preview.Entries[1].DuplicateOfLine)

	_, err = svc.ApplyOneClickAccountNotes(context.Background(), content, preview.PreviewDigest)
	require.NoError(t, err)
	require.Equal(t, 1, repo.applyCalls)
	require.Len(t, repo.updates, 1)
	require.Equal(t, line, repo.updates[0].Notes)
}

func TestPreviewOneClickAccountNotesRejectsAmplifiedPlans(t *testing.T) {
	t.Run("matched accounts", func(t *testing.T) {
		targets := make([]OneClickAccountNoteTarget, OneClickAccountNotesMaxMatchedAccounts+1)
		for index := range targets {
			targets[index] = OneClickAccountNoteTarget{ID: int64(index + 1), Name: "owner@example.com"}
		}
		svc := &adminServiceImpl{accountNoteRepo: &oneClickAccountNotesRepositoryStub{targets: targets}}

		_, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte("owner@example.com---note"))
		require.ErrorIs(t, err, ErrOneClickAccountNotesPlanTooLarge)
		require.Equal(t, http.StatusRequestEntityTooLarge, infraerrors.Code(err))
	})

	t.Run("projected mutation bytes", func(t *testing.T) {
		prefix := "owner@example.com---"
		line := prefix + strings.Repeat("x", oneClickAccountNotesMaxLineBytes-len(prefix))
		matchCount := OneClickAccountNotesMaxMutationBytes/(len(line)+32) + 1
		targets := make([]OneClickAccountNoteTarget, matchCount)
		for index := range targets {
			targets[index] = OneClickAccountNoteTarget{ID: int64(index + 1), Name: "owner@example.com"}
		}
		svc := &adminServiceImpl{accountNoteRepo: &oneClickAccountNotesRepositoryStub{targets: targets}}

		_, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
		require.ErrorIs(t, err, ErrOneClickAccountNotesPlanTooLarge)
	})

	t.Run("existing target state bytes", func(t *testing.T) {
		hugeNotes := strings.Repeat("x", OneClickAccountNotesMaxTargetStateBytes+1)
		repo := &oneClickAccountNotesRepositoryStub{targets: []OneClickAccountNoteTarget{
			{ID: 1, Name: "owner@example.com", Notes: &hugeNotes},
		}}
		svc := &adminServiceImpl{accountNoteRepo: repo}

		_, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte("owner@example.com---note"))
		require.ErrorIs(t, err, ErrOneClickAccountNotesPlanTooLarge)
	})
}

func TestParseOneClickAccountNotesFileValidation(t *testing.T) {
	validLine := "owner@example.com---https://mail.example/open"
	tests := []struct {
		name       string
		content    []byte
		wantReason string
		wantCode   int
	}{
		{name: "empty", content: nil, wantReason: "ACCOUNT_NOTE_IMPORT_FILE_EMPTY", wantCode: http.StatusBadRequest},
		{name: "too large", content: []byte(strings.Repeat("x", oneClickAccountNotesMaxFileBytes+1)), wantReason: "ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE", wantCode: http.StatusRequestEntityTooLarge},
		{name: "invalid UTF-8", content: []byte{0xff}, wantReason: "ACCOUNT_NOTE_IMPORT_INVALID_UTF8", wantCode: http.StatusBadRequest},
		{name: "NUL", content: []byte(validLine + "\x00"), wantReason: "ACCOUNT_NOTE_IMPORT_NUL_BYTE", wantCode: http.StatusBadRequest},
		{name: "double BOM", content: []byte("\xef\xbb\xbf\xef\xbb\xbf" + validLine), wantReason: "ACCOUNT_NOTE_IMPORT_INVALID_BOM", wantCode: http.StatusBadRequest},
		{name: "embedded BOM", content: []byte(validLine + "\xef\xbb\xbf"), wantReason: "ACCOUNT_NOTE_IMPORT_INVALID_BOM", wantCode: http.StatusBadRequest},
		{name: "bare carriage return", content: []byte(validLine + "\r" + validLine), wantReason: "ACCOUNT_NOTE_IMPORT_INVALID_LINE_ENDING", wantCode: http.StatusBadRequest},
		{name: "line too long", content: []byte(validLine + strings.Repeat("x", oneClickAccountNotesMaxLineBytes-len(validLine)+1)), wantReason: "ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG", wantCode: http.StatusBadRequest},
		{name: "whitespace line still enforces size limit", content: []byte(strings.Repeat(" ", oneClickAccountNotesMaxLineBytes+1)), wantReason: "ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG", wantCode: http.StatusBadRequest},
		{name: "too many lines", content: []byte(strings.Repeat(validLine+"\n", oneClickAccountNotesMaxLines+1)), wantReason: "ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES", wantCode: http.StatusBadRequest},
		{name: "no records", content: []byte("\n \t\r\n"), wantReason: "ACCOUNT_NOTE_IMPORT_NO_RECORDS", wantCode: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOneClickAccountNotesFile(test.content)
			require.Error(t, err)
			require.Equal(t, test.wantReason, infraerrors.Reason(err))
			require.Equal(t, test.wantCode, infraerrors.Code(err))
			require.NotContains(t, err.Error(), "mail.example")
			require.NotContains(t, err.Error(), "owner@example.com")
		})
	}
}

func TestParseOneClickAccountNotesFileAcceptsLimitsAndPreservesSourceLine(t *testing.T) {
	prefix := "  OWNER@example.com---https://mail.example/open?value=&amp;  "
	exactLimitLine := prefix + strings.Repeat("x", oneClickAccountNotesMaxLineBytes-len(prefix))
	content := []byte("\xef\xbb\xbf" + exactLimitLine + "\r\n \t \r\nsecond@example.com----opaque\n")

	lines, err := parseOneClickAccountNotesFile(content)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	require.Equal(t, exactLimitLine, lines[0].raw)
	require.Equal(t, "owner@example.com", lines[0].email)
	require.Equal(t, 1, lines[0].lineNumber)
	require.Equal(t, "second@example.com----opaque", lines[1].raw)
	require.Equal(t, 3, lines[1].lineNumber)
}

func TestParseOneClickAccountNotesFileAcceptsExactlyFiveThousandNonEmptyLines(t *testing.T) {
	line := "owner@example.com---https://mail.example/open\n"
	lines, err := parseOneClickAccountNotesFile([]byte(strings.Repeat(line, oneClickAccountNotesMaxLines)))
	require.NoError(t, err)
	require.Len(t, lines, oneClickAccountNotesMaxLines)
}

func TestParseOneClickAccountNotesFileAcceptsExactlyOneMiB(t *testing.T) {
	prefix := "owner@example.com---"
	line := prefix + strings.Repeat("x", oneClickAccountNotesMaxLineBytes-1-len(prefix))
	content := []byte(strings.Repeat(line+"\n", oneClickAccountNotesMaxFileBytes/(len(line)+1)))
	require.Len(t, content, oneClickAccountNotesMaxFileBytes)

	lines, err := parseOneClickAccountNotesFile(content)
	require.NoError(t, err)
	require.Len(t, lines, oneClickAccountNotesMaxFileBytes/(len(line)+1))
	require.Equal(t, "owner@example.com", lines[0].email)
}

func TestParseOneClickAccountNotesFileBoundsAllocationsForManyEmptyLines(t *testing.T) {
	content := bytes.Repeat([]byte{'\n'}, oneClickAccountNotesMaxFileBytes)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	lines, err := parseOneClickAccountNotesFile(content)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(content)
	require.Nil(t, lines)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_NO_RECORDS", infraerrors.Reason(err))
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(8<<20))
}

func TestFindOneClickAccountNotesEmailRejectsUnicodeCaseFoldLookalikes(t *testing.T) {
	for _, line := range []string{
		"u\u212Aer@example.com---note",
		"user@exam\u212Ale.com---note",
		"user@example.co\u017Fm---note",
	} {
		require.Empty(t, findOneClickAccountNotesEmail(line), line)
	}
}

func TestApplyOneClickAccountNotesPassesThroughRepositoryStaleWithoutLeakingInput(t *testing.T) {
	line := "owner@example.com---https://mail.example/open?token=do-not-leak"
	repo := &oneClickAccountNotesRepositoryStub{
		targets:  []OneClickAccountNoteTarget{{ID: 1, Name: "owner@example.com"}},
		applyErr: ErrOneClickAccountNotesPreviewStale,
	}
	svc := &adminServiceImpl{accountNoteRepo: repo}
	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte(line))
	require.NoError(t, err)

	result, err := svc.ApplyOneClickAccountNotes(context.Background(), []byte(line), preview.PreviewDigest)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrOneClickAccountNotesPreviewStale)
	require.Equal(t, 1, repo.applyCalls)
	for _, secret := range []string{"owner@example.com", "mail.example", "do-not-leak"} {
		require.NotContains(t, err.Error(), secret)
	}
}

func TestPreviewOneClickAccountNotesDoesNotWrapRepositoryErrorWithSourceData(t *testing.T) {
	repoErr := errors.New("database unavailable")
	repo := &oneClickAccountNotesRepositoryStub{listErr: repoErr}
	svc := &adminServiceImpl{accountNoteRepo: repo}

	preview, err := svc.PreviewOneClickAccountNotes(context.Background(), []byte("owner@example.com---secret"))
	require.Nil(t, preview)
	require.ErrorIs(t, err, repoErr)
}

const sha256HexLength = 64
