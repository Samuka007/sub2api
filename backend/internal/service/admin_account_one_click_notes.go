package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	oneClickAccountNotesMaxFileBytes = 1 << 20
	oneClickAccountNotesMaxLines     = 5000
	oneClickAccountNotesMaxLineBytes = 64 << 10

	// These bounds limit server-side amplification after a small uploaded file
	// fans out to many same-name accounts. Repository implementations enforce
	// the same limits before taking the accounts table lock.
	OneClickAccountNotesMaxMatchedAccounts      = 10_000
	OneClickAccountNotesMaxTargetStateBytes     = 8 << 20
	OneClickAccountNotesMaxMutationBytes        = 2 << 20
	OneClickAccountNotesMaxEncodedMutationBytes = 16 << 20

	OneClickAccountNotesEntryStatusMatched   = "matched"
	OneClickAccountNotesEntryStatusUnmatched = "unmatched"
	OneClickAccountNotesEntryStatusInvalid   = "invalid"
	OneClickAccountNotesEntryStatusDuplicate = "duplicate"
	OneClickAccountNotesEntryStatusConflict  = "conflict"

	OneClickAccountNotesReasonMissingEmail           = "missing_email"
	OneClickAccountNotesReasonDuplicateIdenticalLine = "duplicate_identical_line"
	OneClickAccountNotesReasonDuplicateEmailConflict = "duplicate_email_conflict"
)

var (
	ErrOneClickAccountNotesBusy = infraerrors.New(
		http.StatusTooManyRequests,
		"ACCOUNT_NOTE_IMPORT_BUSY",
		"another account note import is waiting for account writes",
	).WithMetadata(map[string]string{"retry_after": "1"})
	ErrOneClickAccountNotesPreviewStale = infraerrors.Conflict(
		"ACCOUNT_NOTE_IMPORT_PREVIEW_STALE",
		"account note import preview is stale; preview the file again",
	)
	ErrOneClickAccountNotesNotApplicable = infraerrors.BadRequest(
		"ACCOUNT_NOTE_IMPORT_NOT_APPLICABLE",
		"account note import cannot be applied",
	)
	ErrOneClickAccountNotesRepositoryUnavailable = infraerrors.InternalServer(
		"ACCOUNT_NOTE_IMPORT_UNAVAILABLE",
		"account note import is unavailable",
	)
	ErrOneClickAccountNotesPlanInvalid = infraerrors.InternalServer(
		"ACCOUNT_NOTE_IMPORT_PLAN_INVALID",
		"account note import plan is invalid",
	)
	ErrOneClickAccountNotesPlanTooLarge = infraerrors.New(
		http.StatusRequestEntityTooLarge,
		"ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE",
		"matched account set is too large",
	)
)

// OneClickAccountNotesRepository is deliberately narrower than AccountRepository.
// It projects only fields needed by the import and applies the resulting notes
// with an optimistic, atomic repository operation.
type OneClickAccountNotesRepository interface {
	ListOneClickAccountNoteTargets(ctx context.Context, matchNames []string) ([]OneClickAccountNoteTarget, error)
	ApplyOneClickAccountNotes(ctx context.Context, matchNames []string, updates []OneClickAccountNoteUpdate) error
}

type OneClickAccountNoteTarget struct {
	ID    int64
	Name  string
	Notes *string
}

// OneClickAccountNoteUpdate also represents unchanged matched accounts. The
// repository guards every target's exact name and current notes, while writing
// only entries whose Notes differ from ExpectedNotes.
type OneClickAccountNoteUpdate struct {
	AccountID     int64
	ExpectedName  string
	ExpectedNotes *string
	Notes         string
}

type OneClickAccountNotesEntry struct {
	LineNumber         int    `json:"line_number"`
	Email              string `json:"email,omitempty"`
	Status             string `json:"status"`
	MatchedAccounts    int    `json:"matched_accounts"`
	WillUpdateAccounts int    `json:"will_update_accounts"`
	DuplicateOfLine    int    `json:"duplicate_of_line,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type OneClickAccountNotesPreview struct {
	PreviewDigest      string                      `json:"preview_digest"`
	TotalLines         int                         `json:"total_lines"`
	ValidLines         int                         `json:"valid_lines"`
	InvalidLines       int                         `json:"invalid_lines"`
	DuplicateLines     int                         `json:"duplicate_lines"`
	ConflictLines      int                         `json:"conflict_lines"`
	MatchedLines       int                         `json:"matched_lines"`
	UnmatchedLines     int                         `json:"unmatched_lines"`
	MatchedAccounts    int                         `json:"matched_accounts"`
	WillUpdateAccounts int                         `json:"will_update_accounts"`
	UnchangedAccounts  int                         `json:"unchanged_accounts"`
	CanApply           bool                        `json:"can_apply"`
	Entries            []OneClickAccountNotesEntry `json:"entries"`
}

type OneClickAccountNotesApplyResult struct {
	MatchedLines      int `json:"matched_lines"`
	MatchedAccounts   int `json:"matched_accounts"`
	UpdatedAccounts   int `json:"updated_accounts"`
	UnchangedAccounts int `json:"unchanged_accounts"`
	UnmatchedLines    int `json:"unmatched_lines"`
	InvalidLines      int `json:"invalid_lines"`
	ConflictLines     int `json:"conflict_lines"`
}

type oneClickAccountNoteLine struct {
	lineNumber int
	raw        string
	email      string
}

type oneClickAccountNotesPlan struct {
	preview    *OneClickAccountNotesPreview
	matchNames []string
	updates    []OneClickAccountNoteUpdate
}

// PreviewOneClickAccountNotes validates and plans an import without returning
// source lines, complete emails, existing notes, or account secrets.
func (s *adminServiceImpl) PreviewOneClickAccountNotes(ctx context.Context, content []byte) (*OneClickAccountNotesPreview, error) {
	plan, err := s.buildOneClickAccountNotesPlan(ctx, content)
	if err != nil {
		return nil, err
	}
	return plan.preview, nil
}

// ApplyOneClickAccountNotes rebuilds the plan, verifies the preview digest, and
// delegates one optimistic transaction to the narrow repository capability.
func (s *adminServiceImpl) ApplyOneClickAccountNotes(
	ctx context.Context,
	content []byte,
	previewDigest string,
) (*OneClickAccountNotesApplyResult, error) {
	plan, err := s.buildOneClickAccountNotesPlan(ctx, content)
	if err != nil {
		return nil, err
	}
	providedDigest := strings.TrimSpace(previewDigest)
	if subtle.ConstantTimeCompare([]byte(providedDigest), []byte(plan.preview.PreviewDigest)) != 1 {
		return nil, ErrOneClickAccountNotesPreviewStale
	}
	if !plan.preview.CanApply {
		return nil, ErrOneClickAccountNotesNotApplicable
	}
	if s.accountNoteRepo == nil {
		return nil, ErrOneClickAccountNotesRepositoryUnavailable
	}
	if err := s.accountNoteRepo.ApplyOneClickAccountNotes(ctx, plan.matchNames, plan.updates); err != nil {
		return nil, err
	}

	return &OneClickAccountNotesApplyResult{
		MatchedLines:      plan.preview.MatchedLines,
		MatchedAccounts:   plan.preview.MatchedAccounts,
		UpdatedAccounts:   plan.preview.WillUpdateAccounts,
		UnchangedAccounts: plan.preview.UnchangedAccounts,
		UnmatchedLines:    plan.preview.UnmatchedLines,
		InvalidLines:      plan.preview.InvalidLines,
		ConflictLines:     plan.preview.ConflictLines,
	}, nil
}

func (s *adminServiceImpl) buildOneClickAccountNotesPlan(
	ctx context.Context,
	content []byte,
) (*oneClickAccountNotesPlan, error) {
	lines, err := parseOneClickAccountNotesFile(content)
	if err != nil {
		return nil, err
	}
	if s.accountNoteRepo == nil {
		return nil, ErrOneClickAccountNotesRepositoryUnavailable
	}
	preview := &OneClickAccountNotesPreview{
		TotalLines: len(lines),
		Entries:    make([]OneClickAccountNotesEntry, len(lines)),
	}
	groups := make(map[string][]int, len(lines))
	conflictingNames := make(map[string]struct{})
	for index, line := range lines {
		entry := OneClickAccountNotesEntry{LineNumber: line.lineNumber}
		if line.email == "" {
			entry.Status = OneClickAccountNotesEntryStatusInvalid
			entry.Reason = OneClickAccountNotesReasonMissingEmail
			preview.InvalidLines++
		} else {
			entry.Email = maskEmailIdentity(line.email)
			preview.ValidLines++
			groups[line.email] = append(groups[line.email], index)
		}
		preview.Entries[index] = entry
	}

	// Resolve duplicate semantics before querying the repository. Conflicting
	// groups are intentionally skipped, so their account fan-out and existing
	// note state must not be allowed to make an otherwise valid import too large.
	lookupNameSet := make(map[string]struct{}, len(groups))
	for email, indexes := range groups {
		firstLine := lines[indexes[0]].raw
		conflict := false
		for _, index := range indexes[1:] {
			if lines[index].raw != firstLine {
				conflict = true
				break
			}
		}
		if conflict {
			conflictingNames[email] = struct{}{}
			for _, index := range indexes {
				preview.Entries[index].Status = OneClickAccountNotesEntryStatusConflict
				preview.Entries[index].Reason = OneClickAccountNotesReasonDuplicateEmailConflict
				preview.ConflictLines++
			}
			continue
		}
		lookupNameSet[email] = struct{}{}
	}
	lookupNames := make([]string, 0, len(lookupNameSet))
	for name := range lookupNameSet {
		lookupNames = append(lookupNames, name)
	}
	sort.Strings(lookupNames)
	targets, err := s.accountNoteRepo.ListOneClickAccountNoteTargets(ctx, lookupNames)
	if err != nil {
		return nil, err
	}
	if len(targets) > OneClickAccountNotesMaxMatchedAccounts {
		return nil, ErrOneClickAccountNotesPlanTooLarge
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	targetsByName := make(map[string][]OneClickAccountNoteTarget, len(targets))
	targetStateBytes := 0
	for _, target := range targets {
		if target.Notes != nil {
			if len(*target.Notes) > OneClickAccountNotesMaxTargetStateBytes-targetStateBytes {
				return nil, ErrOneClickAccountNotesPlanTooLarge
			}
			targetStateBytes += len(*target.Notes)
		}
		key := foldOneClickAccountNotesASCII(target.Name)
		targetsByName[key] = append(targetsByName[key], target)
	}

	matchNames := make([]string, 0, len(groups))
	updates := make([]OneClickAccountNoteUpdate, 0)
	projectedMutationBytes := 0
	for email, indexes := range groups {
		if _, conflict := conflictingNames[email]; conflict {
			continue
		}
		first := indexes[0]
		firstLine := lines[first].raw

		matchNames = append(matchNames, email)
		matches := targetsByName[email]
		if len(matches) == 0 {
			preview.Entries[first].Status = OneClickAccountNotesEntryStatusUnmatched
			preview.UnmatchedLines++
		} else {
			if len(matches) > OneClickAccountNotesMaxMatchedAccounts-preview.MatchedAccounts {
				return nil, ErrOneClickAccountNotesPlanTooLarge
			}
			preview.Entries[first].Status = OneClickAccountNotesEntryStatusMatched
			preview.Entries[first].MatchedAccounts = len(matches)
			preview.MatchedLines++
			preview.MatchedAccounts += len(matches)
			for _, target := range matches {
				willUpdate := target.Notes == nil || *target.Notes != firstLine
				if willUpdate {
					projectedBytes := len(firstLine) + 32
					if projectedBytes > OneClickAccountNotesMaxMutationBytes-projectedMutationBytes {
						return nil, ErrOneClickAccountNotesPlanTooLarge
					}
					projectedMutationBytes += projectedBytes
					preview.Entries[first].WillUpdateAccounts++
					preview.WillUpdateAccounts++
				} else {
					preview.UnchangedAccounts++
				}
				updates = append(updates, OneClickAccountNoteUpdate{
					AccountID:     target.ID,
					ExpectedName:  target.Name,
					ExpectedNotes: cloneOneClickAccountNotesString(target.Notes),
					Notes:         firstLine,
				})
			}
		}

		for _, index := range indexes[1:] {
			preview.Entries[index].Status = OneClickAccountNotesEntryStatusDuplicate
			preview.Entries[index].Reason = OneClickAccountNotesReasonDuplicateIdenticalLine
			preview.Entries[index].DuplicateOfLine = lines[first].lineNumber
			preview.DuplicateLines++
		}
	}

	sort.Strings(matchNames)
	sort.Slice(updates, func(i, j int) bool { return updates[i].AccountID < updates[j].AccountID })
	// Invalid rows and conflicting email groups are skipped; deterministic
	// matched updates can still be applied as a partial result.
	preview.CanApply = preview.WillUpdateAccounts > 0
	preview.PreviewDigest = digestOneClickAccountNotesPlan(content, matchNames, updates)
	return &oneClickAccountNotesPlan{preview: preview, matchNames: matchNames, updates: updates}, nil
}

func parseOneClickAccountNotesFile(content []byte) ([]oneClickAccountNoteLine, error) {
	if len(content) == 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_FILE_EMPTY", "file must not be empty")
	}
	if len(content) > oneClickAccountNotesMaxFileBytes {
		return nil, infraerrors.New(http.StatusRequestEntityTooLarge, "ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE", "file exceeds the 1 MiB limit")
	}
	if !utf8.Valid(content) {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_INVALID_UTF8", "file must contain valid UTF-8 text")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_NUL_BYTE", "file must not contain NUL bytes")
	}

	utf8BOM := []byte{0xef, 0xbb, 0xbf}
	data := bytes.TrimPrefix(content, utf8BOM)
	if bytes.Contains(data, utf8BOM) {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_INVALID_BOM", "file may contain one UTF-8 BOM at the beginning only")
	}

	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if bytes.IndexByte(normalized, '\r') >= 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_INVALID_LINE_ENDING", "file must use LF or CRLF line endings")
	}

	lines := make([]oneClickAccountNoteLine, 0, oneClickAccountNotesMaxLines)
	lineNumber := 1
	for start := 0; ; lineNumber++ {
		end := bytes.IndexByte(normalized[start:], '\n')
		hasMore := end >= 0
		if hasMore {
			end += start
		} else {
			end = len(normalized)
		}
		part := normalized[start:end]
		if len(part) > oneClickAccountNotesMaxLineBytes {
			return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG", "file contains a line longer than 64 KiB")
		}
		if len(bytes.TrimSpace(part)) > 0 {
			if len(lines) >= oneClickAccountNotesMaxLines {
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES", "file contains more than 5000 non-empty lines")
			}
			raw := string(part)
			lines = append(lines, oneClickAccountNoteLine{
				lineNumber: lineNumber,
				raw:        raw,
				email:      findOneClickAccountNotesEmail(raw),
			})
		}
		if !hasMore {
			break
		}
		start = end + 1
	}
	if len(lines) == 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_NO_RECORDS", "file contains no non-empty lines")
	}
	return lines, nil
}

const oneClickAccountNotesEmailExpression = "[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(?:\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*@(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\\.)+[A-Za-z]{2,63}"

var oneClickAccountNotesEmailPattern = regexp.MustCompile(oneClickAccountNotesEmailExpression)

func findOneClickAccountNotesEmail(line string) string {
	for _, indexes := range oneClickAccountNotesEmailPattern.FindAllStringIndex(line, -1) {
		if indexes[0] > 0 && isOneClickAccountNotesEmailTokenBefore(line, indexes[0]) {
			continue
		}
		if indexes[1] < len(line) && isOneClickAccountNotesEmailDomainContinuationAfter(line, indexes[1]) &&
			!strings.HasPrefix(line[indexes[1]:], "---") {
			continue
		}
		candidate := line[indexes[0]:indexes[1]]
		return foldOneClickAccountNotesASCII(candidate)
	}
	return ""
}

func isOneClickAccountNotesEmailTokenBefore(line string, index int) bool {
	value, _ := utf8.DecodeLastRuneInString(line[:index])
	if value <= unicode.MaxASCII {
		return isOneClickAccountNotesEmailTokenByte(byte(value))
	}
	return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value)
}

func isOneClickAccountNotesEmailDomainContinuationAfter(line string, index int) bool {
	value, size := utf8.DecodeRuneInString(line[index:])
	if value == '.' {
		nextIndex := index + size
		if nextIndex >= len(line) {
			return false
		}
		next, _ := utf8.DecodeRuneInString(line[nextIndex:])
		if next <= unicode.MaxASCII {
			return next >= 'a' && next <= 'z' ||
				next >= 'A' && next <= 'Z' ||
				next >= '0' && next <= '9'
		}
		return unicode.IsLetter(next) || unicode.IsNumber(next) || unicode.IsMark(next)
	}
	if value <= unicode.MaxASCII {
		return isOneClickAccountNotesEmailDomainContinuationByte(byte(value))
	}
	return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value)
}

func isOneClickAccountNotesEmailTokenByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-@", rune(value))
}

func isOneClickAccountNotesEmailDomainContinuationByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '.' || value == '-' || value == '@'
}

func foldOneClickAccountNotesASCII(value string) string {
	buffer := []byte(value)
	for index, char := range buffer {
		if char >= 'A' && char <= 'Z' {
			buffer[index] = char + ('a' - 'A')
		}
	}
	return string(buffer)
}

func digestOneClickAccountNotesPlan(
	content []byte,
	matchNames []string,
	updates []OneClickAccountNoteUpdate,
) string {
	digest := sha256.New()
	writeOneClickAccountNotesDigestString(digest, "sub2api-one-click-account-notes-v1")
	writeOneClickAccountNotesDigestBytes(digest, content)
	writeOneClickAccountNotesDigestUint64(digest, uint64(len(matchNames)))
	for _, name := range matchNames {
		writeOneClickAccountNotesDigestString(digest, name)
	}
	writeOneClickAccountNotesDigestUint64(digest, uint64(len(updates)))
	for _, update := range updates {
		writeOneClickAccountNotesDigestUint64(digest, uint64(update.AccountID))
		writeOneClickAccountNotesDigestString(digest, update.ExpectedName)
		if update.ExpectedNotes == nil {
			_, _ = digest.Write([]byte{0})
		} else {
			_, _ = digest.Write([]byte{1})
			writeOneClickAccountNotesDigestString(digest, *update.ExpectedNotes)
		}
		writeOneClickAccountNotesDigestString(digest, update.Notes)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeOneClickAccountNotesDigestString(digest hash.Hash, value string) {
	writeOneClickAccountNotesDigestBytes(digest, []byte(value))
}

func writeOneClickAccountNotesDigestBytes(digest hash.Hash, value []byte) {
	writeOneClickAccountNotesDigestUint64(digest, uint64(len(value)))
	if len(value) > 0 {
		_, _ = digest.Write(value)
	}
}

func writeOneClickAccountNotesDigestUint64(digest hash.Hash, value uint64) {
	var buffer [8]byte
	binary.BigEndian.PutUint64(buffer[:], value)
	_, _ = digest.Write(buffer[:])
}

func cloneOneClickAccountNotesString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
