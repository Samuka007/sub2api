package service

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	accountHealthGlobalConcurrency = 10
	accountHealthHostConcurrency   = 2
	accountHealthMailboxLimit      = 50
	accountHealthMailboxPageLimit  = 10
	accountHealthDetectionTimeout  = 60 * time.Second
	accountHealthCandidatePageSize = 500
)

const (
	accountHealthStatusFetchError  accountHealthDetectionStatus = "fetch_error"
	accountHealthStatusFormatError accountHealthDetectionStatus = "format_error"
)

var ErrAccountHealthUnsupportedGroup = infraerrors.BadRequest(
	"ACCOUNT_HEALTH_UNSUPPORTED_GROUP",
	"account health detection is only available for OpenAI groups",
)

var ErrAccountHealthUnsupportedAccount = infraerrors.BadRequest(
	"ACCOUNT_HEALTH_UNSUPPORTED_ACCOUNT",
	"account health detection is only available for OpenAI accounts",
)

var ErrAccountHealthGroupMismatch = infraerrors.BadRequest(
	"ACCOUNT_HEALTH_GROUP_MISMATCH",
	"account does not belong to the selected group",
)

type AccountHealthCandidate struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Platform   string   `json:"platform"`
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	GroupID    int64    `json:"group_id"`
	GroupName  string   `json:"group_name"`
	GroupIDs   []int64  `json:"group_ids"`
	GroupNames []string `json:"group_names"`
}

type GroupAccountHealthDetection struct {
	AccountID       int64                        `json:"account_id,omitempty"`
	AccountName     string                       `json:"account_name,omitempty"`
	GroupID         int64                        `json:"group_id"`
	GroupName       string                       `json:"group_name"`
	AccountEmail    string                       `json:"account_email"`
	Status          accountHealthDetectionStatus `json:"status"`
	FormatIssue     string                       `json:"format_issue,omitempty"`
	Score           int                          `json:"score"`
	Language        string                       `json:"language"`
	Evidence        []string                     `json:"evidence"`
	MessageDate     string                       `json:"message_date"`
	AssociatedEmail string                       `json:"associated_email"`
	MessagesScanned int                          `json:"messages_scanned"`
	PagesScanned    int                          `json:"pages_scanned"`
	PlusDetected    bool                         `json:"plus_detected"`
	PlusScore       int                          `json:"plus_score"`
	PlusLanguage    string                       `json:"plus_language"`
	PlusDate        string                       `json:"plus_date"`
	PaymentMethod   string                       `json:"payment_method"`
	LifespanStatus  string                       `json:"lifespan_status"`
	LifespanSeconds *int64                       `json:"lifespan_seconds"`
	BanDate         string                       `json:"ban_date"`
	ElapsedMS       int64                        `json:"elapsed_ms"`
	CheckedAt       string                       `json:"checked_at"`
}

type GroupAccountHealthDetector interface {
	DetectGroup(ctx context.Context, groupID int64) (*GroupAccountHealthDetection, error)
	DetectAccountNotes(ctx context.Context, groupID int64, notes string) (*GroupAccountHealthDetection, error)
}

type groupAccountHealthReader interface {
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
}

type groupAccountHealthDetector struct {
	groupRepo groupAccountHealthReader
	fetcher   accountHealthMailboxFetcher
	now       func() time.Time
	timeout   time.Duration
	global    chan struct{}
	hostMu    sync.Mutex
	hostGates map[string]*accountHealthHostGate
}

type accountHealthHostGate struct {
	slots chan struct{}
	refs  int
}

func NewGroupAccountHealthDetector(groupRepo groupAccountHealthReader) GroupAccountHealthDetector {
	return &groupAccountHealthDetector{
		groupRepo: groupRepo,
		fetcher:   newHTTPAccountHealthMailboxFetcher(),
		now:       time.Now,
		timeout:   accountHealthDetectionTimeout,
		global:    make(chan struct{}, accountHealthGlobalConcurrency),
		hostGates: make(map[string]*accountHealthHostGate),
	}
}

// ListAccountHealthCandidates returns existing OpenAI accounts in the selected
// groups without exposing credentials, account extras, account notes, or group descriptions.
func (s *adminServiceImpl) ListAccountHealthCandidates(ctx context.Context, groupIDs []int64) ([]AccountHealthCandidate, error) {
	candidates := make([]AccountHealthCandidate, 0)
	candidateIndex := make(map[int64]int)
	for _, groupID := range groupIDs {
		group, err := s.groupRepo.GetByIDLite(ctx, groupID)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(group.Platform), PlatformOpenAI) {
			return nil, ErrAccountHealthUnsupportedGroup
		}

		for page := 1; ; page++ {
			accounts, total, err := s.ListAccounts(
				ctx, page, accountHealthCandidatePageSize,
				PlatformOpenAI, "", "", "", groupID, "", "name", "asc",
			)
			if err != nil {
				return nil, err
			}
			for i := range accounts {
				account := &accounts[i]
				if index, exists := candidateIndex[account.ID]; exists {
					candidates[index].GroupIDs = append(candidates[index].GroupIDs, groupID)
					candidates[index].GroupNames = append(candidates[index].GroupNames, group.Name)
					continue
				}
				candidateIndex[account.ID] = len(candidates)
				candidates = append(candidates, AccountHealthCandidate{
					ID:         account.ID,
					Name:       account.Name,
					Platform:   account.Platform,
					Type:       account.Type,
					Status:     account.Status,
					GroupID:    groupID,
					GroupName:  group.Name,
					GroupIDs:   []int64{groupID},
					GroupNames: []string{group.Name},
				})
			}
			if int64(page*accountHealthCandidatePageSize) >= total || len(accounts) == 0 {
				break
			}
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := strings.ToLower(candidates[i].Name)
		right := strings.ToLower(candidates[j].Name)
		if left == right {
			return candidates[i].ID < candidates[j].ID
		}
		return left < right
	})
	return candidates, nil
}

// DetectAccountHealth revalidates the account and selected group binding, then
// runs the detector with the account's server-side notes.
func (s *adminServiceImpl) DetectAccountHealth(ctx context.Context, accountID, groupID int64) (*GroupAccountHealthDetection, error) {
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(account.Platform), PlatformOpenAI) {
		return nil, ErrAccountHealthUnsupportedAccount
	}
	if !accountHealthAccountBelongsToGroup(account, groupID) {
		return nil, ErrAccountHealthGroupMismatch
	}

	notes := ""
	if account.Notes != nil {
		notes = *account.Notes
	}
	detector := s.accountHealthDetector
	if detector == nil {
		detector = NewGroupAccountHealthDetector(s.groupRepo)
	}
	result, err := detector.DetectAccountNotes(ctx, groupID, notes)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("account health detector returned no result")
	}
	responseResult := *result
	responseResult.Evidence = append([]string(nil), result.Evidence...)
	responseResult.AccountID = account.ID
	responseResult.AccountName = account.Name
	return &responseResult, nil
}

func accountHealthAccountBelongsToGroup(account *Account, groupID int64) bool {
	if account == nil || groupID <= 0 {
		return false
	}
	for _, candidate := range account.GroupIDs {
		if candidate == groupID {
			return true
		}
	}
	for _, binding := range account.AccountGroups {
		if binding.GroupID == groupID {
			return true
		}
	}
	for _, group := range account.Groups {
		if group != nil && group.ID == groupID {
			return true
		}
	}
	return false
}

func (d *groupAccountHealthDetector) DetectGroup(ctx context.Context, groupID int64) (*GroupAccountHealthDetection, error) {
	return d.detect(ctx, groupID, nil)
}

func (d *groupAccountHealthDetector) DetectAccountNotes(ctx context.Context, groupID int64, notes string) (*GroupAccountHealthDetection, error) {
	return d.detect(ctx, groupID, &notes)
}

func (d *groupAccountHealthDetector) detect(ctx context.Context, groupID int64, accountNotes *string) (*GroupAccountHealthDetection, error) {
	callerCtx := ctx
	timeout := d.timeout
	if timeout <= 0 {
		timeout = accountHealthDetectionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	group, err := d.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(group.Platform), PlatformOpenAI) {
		return nil, ErrAccountHealthUnsupportedGroup
	}

	result := &GroupAccountHealthDetection{
		GroupID:        group.ID,
		GroupName:      group.Name,
		Evidence:       []string{},
		LifespanStatus: string(accountHealthLifespanUnavailable),
	}
	finish := func() *GroupAccountHealthDetection {
		result.ElapsedMS = time.Since(startedAt).Milliseconds()
		result.CheckedAt = d.currentTime().UTC().Format(time.RFC3339)
		return result
	}
	finishContextError := func(contextErr error, messagesScanned, pagesScanned int) (*GroupAccountHealthDetection, error) {
		if callerErr := callerCtx.Err(); callerErr != nil {
			return nil, callerErr
		}
		if errors.Is(contextErr, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			clearGroupAccountHealthDetectionEvidence(result)
			result.Status = accountHealthStatusFetchError
			result.MessagesScanned = messagesScanned
			result.PagesScanned = pagesScanned
			return finish(), nil
		}
		return nil, contextErr
	}

	detectionSource := group.Description
	if accountNotes != nil {
		detectionSource = *accountNotes
	}
	target, err := parseAccountHealthMailboxTarget(detectionSource)
	if err != nil {
		result.Status = accountHealthStatusFormatError
		result.FormatIssue = accountHealthFormatIssue(err)
		return finish(), nil
	}
	result.AccountEmail = target.email

	mailboxURL := accountHealthMailboxURLWithLimit(target.mailboxURL, accountHealthMailboxLimit)
	host := accountHealthMailboxHost(mailboxURL)
	release, err := d.acquire(ctx, host)
	if err != nil {
		return finishContextError(err, 0, 0)
	}
	defer release()

	fetcher := d.fetcher
	if fetcher == nil {
		fetcher = newHTTPAccountHealthMailboxFetcher()
	}

	blocks := make([]string, 0, accountHealthMailboxLimit)
	blockFingerprints := make(map[string]struct{}, accountHealthMailboxLimit)
	visited := make(map[string]struct{}, accountHealthMailboxPageLimit)
	fetchedURLs := make([]string, 0, accountHealthMailboxPageLimit+1)
	fetchedURLs = append(fetchedURLs, target.mailboxURL)
	currentURL := mailboxURL
	pages := 0
	structuredPages := 0
	dynamicHint := false
	for currentURL != "" && pages < accountHealthMailboxPageLimit && len(blocks) < accountHealthMailboxLimit {
		if err := ctx.Err(); err != nil {
			return finishContextError(err, len(blocks), pages)
		}
		if _, complete := accountHealthRedactionSecrets(currentURL, target.secrets...); !complete {
			result.Status = accountHealthStatusParseError
			result.MessagesScanned = len(blocks)
			result.PagesScanned = pages
			return finish(), nil
		}

		currentKey := accountHealthCanonicalMailboxURL(currentURL)
		if _, seen := visited[currentKey]; seen {
			break
		}
		visited[currentKey] = struct{}{}
		fetchedURLs = append(fetchedURLs, currentURL)

		mailbox, fetchErr := fetcher.Fetch(ctx, currentURL)
		if fetchErr != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return finishContextError(contextErr, len(blocks), pages)
			}
			result.Status = accountHealthStatusFetchError
			result.MessagesScanned = len(blocks)
			result.PagesScanned = pages
			return finish(), nil
		}
		if err := ctx.Err(); err != nil {
			return finishContextError(err, len(blocks), pages)
		}
		for _, visitedURL := range mailbox.visitedURLs {
			if _, complete := accountHealthRedactionSecrets(visitedURL, target.secrets...); !complete {
				result.Status = accountHealthStatusParseError
				result.MessagesScanned = len(blocks)
				result.PagesScanned = pages + 1
				return finish(), nil
			}
		}
		fetchedURLs = append(fetchedURLs, mailbox.visitedURLs...)
		page, parseErr := parseAccountHealthMailboxPage(mailbox.body, mailbox.contentType, accountHealthMailboxLimit)
		if parseErr != nil {
			result.Status = accountHealthStatusParseError
			result.MessagesScanned = len(blocks)
			result.PagesScanned = pages + 1
			return finish(), nil
		}
		if err := ctx.Err(); err != nil {
			return finishContextError(err, len(blocks), pages)
		}

		pages++
		if page.structured {
			structuredPages++
		}
		dynamicHint = dynamicHint || page.dynamicHint
		for _, block := range page.blocks {
			blocks = appendUniqueAccountHealthBlock(blocks, blockFingerprints, block)
			if len(blocks) >= accountHealthMailboxLimit {
				break
			}
		}
		if len(blocks) >= accountHealthMailboxLimit {
			break
		}
		paginationBaseURL := currentURL
		if len(mailbox.visitedURLs) > 0 {
			paginationBaseURL = mailbox.visitedURLs[len(mailbox.visitedURLs)-1]
		}
		currentURL = accountHealthNextMailboxURL(paginationBaseURL, mailboxURL, page.nextLinks, visited)
	}

	if err := ctx.Err(); err != nil {
		return finishContextError(err, len(blocks), pages)
	}
	now := d.currentTime()
	var detection accountHealthDetection
	if len(blocks) == 0 && pages > 0 && structuredPages == pages {
		detection = attachAccountHealthPlus(accountHealthDetection{
			Status:          accountHealthStatusNotFound,
			MessagesScanned: 0,
			PagesScanned:    pages,
		}, accountHealthPlusEvidence{}, "", now)
	} else {
		detection = classifyAccountHealthMessages(blocks, target.email, pages, dynamicHint, now)
	}
	populateGroupAccountHealthDetection(result, detection)
	for _, fetchedURL := range fetchedURLs {
		if err := ctx.Err(); err != nil {
			return finishContextError(err, len(blocks), pages)
		}
		if !redactGroupAccountHealthDetection(result, fetchedURL, target.secrets...) {
			result.Status = accountHealthStatusParseError
			return finish(), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return finishContextError(err, len(blocks), pages)
	}
	return finish(), nil
}

func accountHealthNextMailboxURL(currentRawURL, originRawURL string, nextLinks []string, visited map[string]struct{}) string {
	current, err := url.Parse(currentRawURL)
	if err != nil {
		return ""
	}
	origin, err := url.Parse(originRawURL)
	if err != nil {
		return ""
	}
	originHost, ok := accountHealthMailboxHTTPSHost(origin)
	if !ok {
		return ""
	}

	for _, rawLink := range nextLinks {
		if len(rawLink) > accountHealthMaxURLBytes {
			continue
		}
		reference, parseErr := url.Parse(strings.TrimSpace(rawLink))
		if parseErr != nil {
			continue
		}
		referenceQuery, queryErr := url.ParseQuery(reference.RawQuery)
		if queryErr != nil {
			continue
		}
		candidate := current.ResolveReference(reference)
		candidate.Fragment = ""
		if len(candidate.String()) > accountHealthMaxURLBytes {
			continue
		}
		candidateHost, valid := accountHealthMailboxHTTPSHost(candidate)
		if !valid || candidateHost != originHost {
			continue
		}
		if err := validateAccountHealthPort(candidate); err != nil {
			continue
		}
		if reference.RawQuery != "" {
			retainedQuery := url.Values(nil)
			if candidate.EscapedPath() == current.EscapedPath() &&
				accountHealthSameHTTPSOrigin(current, candidate) {
				retainedQuery = current.Query()
			}
			if !accountHealthIsSafePaginationQuery(referenceQuery, retainedQuery) {
				continue
			}
		}
		if reference.RawQuery != "" && retainedAccountHealthPaginationQuery(current, candidate) {
			query := candidate.Query()
			for key, values := range current.Query() {
				if _, exists := query[key]; exists {
					continue
				}
				for _, value := range values {
					query.Add(key, value)
				}
			}
			candidate.RawQuery = query.Encode()
		}
		candidate.Scheme = "https"
		candidateKey := accountHealthCanonicalParsedMailboxURL(candidate)
		if _, seen := visited[candidateKey]; seen {
			continue
		}
		return candidate.String()
	}
	return ""
}

func retainedAccountHealthPaginationQuery(current, candidate *url.URL) bool {
	return candidate.EscapedPath() == current.EscapedPath() && accountHealthSameHTTPSOrigin(current, candidate)
}

func accountHealthIsSafePaginationQuery(reference, retained url.Values) bool {
	hasPosition := false
	for key, values := range reference {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "page", "p", "offset", "cursor":
			hasPosition = true
		case "limit", "per_page", "page_size", "pagesize", "size", "count":
			continue
		default:
			retainedValues, ok := retained[key]
			if !ok || !accountHealthQueryValuesEqual(values, retainedValues) {
				return false
			}
		}
	}
	return hasPosition
}

func accountHealthQueryValuesEqual(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	counts := make(map[string]int, len(first))
	for _, value := range first {
		counts[value]++
	}
	for _, value := range second {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}

func accountHealthSameHTTPSOrigin(first, second *url.URL) bool {
	firstHost, firstValid := accountHealthMailboxHTTPSHost(first)
	secondHost, secondValid := accountHealthMailboxHTTPSHost(second)
	if !firstValid || !secondValid || firstHost != secondHost {
		return false
	}
	return accountHealthEffectiveHTTPSPort(first) == accountHealthEffectiveHTTPSPort(second)
}

func accountHealthEffectiveHTTPSPort(parsed *url.URL) string {
	if parsed == nil || parsed.Port() == "" {
		return "443"
	}
	return parsed.Port()
}

func accountHealthMailboxHTTPSHost(parsed *url.URL) (string, bool) {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", false
	}
	return host, true
}

func accountHealthCanonicalMailboxURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return accountHealthCanonicalParsedMailboxURL(parsed)
}

func accountHealthCanonicalParsedMailboxURL(parsed *url.URL) string {
	canonical := *parsed
	canonical.Scheme = strings.ToLower(canonical.Scheme)
	host := strings.ToLower(strings.TrimSuffix(canonical.Hostname(), "."))
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := canonical.Port(); port != "" && port != "443" {
		host += ":" + port
	}
	canonical.Host = host
	canonical.Fragment = ""
	canonical.RawQuery = canonical.Query().Encode()
	if canonical.RawQuery == "" {
		canonical.ForceQuery = false
	}
	return canonical.String()
}

func (d *groupAccountHealthDetector) currentTime() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *groupAccountHealthDetector) acquire(ctx context.Context, host string) (func(), error) {
	d.hostMu.Lock()
	if d.hostGates == nil {
		d.hostGates = make(map[string]*accountHealthHostGate)
	}
	gate := d.hostGates[host]
	if gate == nil {
		gate = &accountHealthHostGate{slots: make(chan struct{}, accountHealthHostConcurrency)}
		d.hostGates[host] = gate
	}
	gate.refs++
	d.hostMu.Unlock()

	select {
	case gate.slots <- struct{}{}:
	case <-ctx.Done():
		d.releaseHostReference(host, gate)
		return nil, ctx.Err()
	}

	global := d.global
	if global == nil {
		global = make(chan struct{}, accountHealthGlobalConcurrency)
		d.global = global
	}
	select {
	case global <- struct{}{}:
	case <-ctx.Done():
		<-gate.slots
		d.releaseHostReference(host, gate)
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-global
			<-gate.slots
			d.releaseHostReference(host, gate)
		})
	}, nil
}

func (d *groupAccountHealthDetector) releaseHostReference(host string, gate *accountHealthHostGate) {
	d.hostMu.Lock()
	defer d.hostMu.Unlock()
	gate.refs--
	if gate.refs == 0 && d.hostGates[host] == gate {
		delete(d.hostGates, host)
	}
}

func accountHealthMailboxHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "invalid"
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
}

func accountHealthFormatIssue(err error) string {
	var formatErr *accountHealthDescriptionFormatError
	if errors.As(err, &formatErr) {
		return string(formatErr.kind)
	}
	return string(accountHealthDescriptionInvalidURL)
}

func populateGroupAccountHealthDetection(result *GroupAccountHealthDetection, detection accountHealthDetection) {
	result.Status = detection.Status
	result.Score = detection.Score
	result.Language = detection.Language
	result.Evidence = append([]string{}, detection.Evidence...)
	for _, item := range detection.PlusEvidence {
		if !containsAccountHealthEvidence(result.Evidence, item) {
			result.Evidence = append(result.Evidence, item)
		}
	}
	result.MessageDate = detection.MessageDate
	result.AssociatedEmail = detection.AssociatedEmail
	result.MessagesScanned = detection.MessagesScanned
	result.PagesScanned = detection.PagesScanned
	result.PlusDetected = detection.PlusDetected
	result.PlusScore = detection.PlusScore
	result.PlusLanguage = detection.PlusLanguage
	result.PlusDate = detection.PlusDate
	result.PaymentMethod = detection.PaymentMethod
	result.LifespanStatus = string(detection.LifespanStatus)
	result.LifespanSeconds = detection.LifespanSeconds
	result.BanDate = detection.BanDate
}

func containsAccountHealthEvidence(items []string, candidate string) bool {
	for _, item := range items {
		if item == candidate {
			return true
		}
	}
	return false
}

var accountHealthAuthorizationPattern = regexp.MustCompile(
	`(?i)\b(authorization)\b(\s*[:=]\s*)[^\r\n]*`,
)

var accountHealthCredentialSchemePattern = regexp.MustCompile(
	`(?i)\b(bearer|basic)(\s+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[A-Za-z0-9._~+/=-]+)`,
)

var accountHealthSensitiveAssignmentPattern = regexp.MustCompile(
	`(?i)\b(password|passwd|pwd|token|secret|api[_-]?key|key|code)\b(\s*[:=]\s*)[^&;\r\n]*`,
)

func redactGroupAccountHealthDetection(result *GroupAccountHealthDetection, mailboxURL string, additionalSecrets ...string) bool {
	if result == nil {
		return true
	}
	secrets, complete := accountHealthRedactionSecrets(mailboxURL, additionalSecrets...)
	if !complete {
		clearGroupAccountHealthDetectionEvidence(result)
		return false
	}
	percentEncodedPatterns := accountHealthPercentEncodedSecretPatterns(secrets)
	redact := func(value string) string {
		for _, secret := range secrets {
			value = redactAccountHealthSecret(value, secret)
			value = redactAccountHealthSecret(value, url.QueryEscape(secret))
			value = redactAccountHealthSecret(value, url.PathEscape(secret))
		}
		for _, pattern := range percentEncodedPatterns {
			value = pattern.ReplaceAllString(value, "***")
		}
		value = accountHealthAuthorizationPattern.ReplaceAllString(value, "$1$2***")
		value = accountHealthCredentialSchemePattern.ReplaceAllString(value, "$1$2***")
		value = accountHealthSensitiveAssignmentPattern.ReplaceAllString(value, "$1$2***")
		value = accountHealthURLPattern.ReplaceAllString(value, "[redacted URL]")
		return value
	}
	result.Language = redact(result.Language)
	result.MessageDate = redact(result.MessageDate)
	result.AssociatedEmail = redact(result.AssociatedEmail)
	result.PlusLanguage = redact(result.PlusLanguage)
	result.PlusDate = redact(result.PlusDate)
	result.PaymentMethod = normalizeAccountHealthPaymentMethod(redact(result.PaymentMethod))
	result.BanDate = redact(result.BanDate)
	for i := range result.Evidence {
		if isKnownAccountHealthEvidence(result.Evidence[i]) {
			continue
		}
		result.Evidence[i] = redact(result.Evidence[i])
	}
	return true
}

func isKnownAccountHealthEvidence(value string) bool {
	switch value {
	case accountHealthEvidenceAccountMismatch,
		accountHealthEvidencePlusAfterBan,
		accountHealthEvidenceDynamicMailbox,
		accountHealthEvidenceReactivated,
		accountHealthEvidenceDeactivated,
		accountHealthEvidenceOpenAI,
		accountHealthEvidenceAccountUnavailable,
		accountHealthEvidencePolicyViolation,
		accountHealthEvidenceAppeal,
		accountHealthEvidenceDeactivationSubject,
		accountHealthEvidenceWarning,
		accountHealthEvidencePlus,
		accountHealthEvidenceSubscriptionSuccess,
		accountHealthEvidenceOrderNumber,
		accountHealthEvidencePaymentMethod,
		accountHealthEvidenceOrderDate,
		accountHealthEvidenceSubscriptionManagement:
		return true
	default:
		return false
	}
}

func clearGroupAccountHealthDetectionEvidence(result *GroupAccountHealthDetection) {
	result.Score = 0
	result.Language = ""
	result.MessageDate = ""
	result.AssociatedEmail = ""
	result.PlusDetected = false
	result.PlusScore = 0
	result.PlusLanguage = ""
	result.PlusDate = ""
	result.PaymentMethod = ""
	result.LifespanStatus = string(accountHealthLifespanUnavailable)
	result.LifespanSeconds = nil
	result.BanDate = ""
	result.Evidence = []string{}
}

func accountHealthPercentEncodedSecretPatterns(secrets []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		for _, variant := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			pattern, ok := accountHealthPercentEncodedSecretPattern(variant)
			if !ok {
				continue
			}
			key := pattern.String()
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func accountHealthPercentEncodedSecretPattern(secret string) (*regexp.Regexp, bool) {
	var expression strings.Builder
	last := 0
	found := false
	for index := 0; index+2 < len(secret); {
		if secret[index] != '%' || !isAccountHealthHexDigit(secret[index+1]) || !isAccountHealthHexDigit(secret[index+2]) {
			index++
			continue
		}
		_, _ = expression.WriteString(regexp.QuoteMeta(secret[last:index]))
		_ = expression.WriteByte('%')
		writeAccountHealthHexDigitPattern(&expression, secret[index+1])
		writeAccountHealthHexDigitPattern(&expression, secret[index+2])
		index += 3
		last = index
		found = true
	}
	if !found {
		return nil, false
	}
	_, _ = expression.WriteString(regexp.QuoteMeta(secret[last:]))
	pattern, err := regexp.Compile(expression.String())
	return pattern, err == nil
}

func isAccountHealthHexDigit(value byte) bool {
	return (value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'f') ||
		(value >= 'A' && value <= 'F')
}

func writeAccountHealthHexDigitPattern(expression *strings.Builder, value byte) {
	if value >= 'a' && value <= 'f' {
		_ = expression.WriteByte('[')
		_ = expression.WriteByte(value)
		_ = expression.WriteByte(value - ('a' - 'A'))
		_ = expression.WriteByte(']')
		return
	}
	if value >= 'A' && value <= 'F' {
		_ = expression.WriteByte('[')
		_ = expression.WriteByte(value + ('a' - 'A'))
		_ = expression.WriteByte(value)
		_ = expression.WriteByte(']')
		return
	}
	_ = expression.WriteByte(value)
}

func redactAccountHealthSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	if len([]rune(secret)) >= 3 {
		return strings.ReplaceAll(value, secret, "***")
	}

	var redacted strings.Builder
	redacted.Grow(len(value))
	searchFrom := 0
	last := 0
	for searchFrom <= len(value)-len(secret) {
		relative := strings.Index(value[searchFrom:], secret)
		if relative < 0 {
			break
		}
		start := searchFrom + relative
		end := start + len(secret)
		searchFrom = end
		if !accountHealthSecretBoundary(value, start-1) || !accountHealthSecretBoundary(value, end) {
			continue
		}
		_, _ = redacted.WriteString(value[last:start])
		_, _ = redacted.WriteString("***")
		last = end
	}
	if last == 0 {
		return value
	}
	_, _ = redacted.WriteString(value[last:])
	return redacted.String()
}

func accountHealthSecretBoundary(value string, index int) bool {
	if index < 0 || index >= len(value) {
		return true
	}
	char := value[index]
	return (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9')
}

type accountHealthSecretSet struct {
	values   []string
	overflow bool
}

func (s *accountHealthSecretSet) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for _, secret := range s.values {
		if secret == value {
			return
		}
	}
	if len(s.values) >= accountHealthMaxURLSecrets {
		s.overflow = true
		return
	}
	s.values = append(s.values, value)
}

func accountHealthRedactionSecrets(rawURL string, additionalSecrets ...string) ([]string, bool) {
	set := accountHealthSecretSet{values: make([]string, 0, accountHealthMaxURLSecrets)}
	for _, secret := range additionalSecrets {
		set.add(secret)
	}
	mailboxSecrets, complete := accountHealthMailboxSecrets(rawURL)
	if !complete {
		return nil, false
	}
	for _, secret := range mailboxSecrets {
		set.add(secret)
	}
	for _, secret := range append([]string(nil), set.values...) {
		normalized := normalizeAccountHealthText(secret)
		set.add(normalized)
		for _, email := range accountHealthEmailPattern.FindAllString(normalized, -1) {
			set.add(strings.ToLower(email))
		}
	}
	return set.values, !set.overflow
}

func accountHealthMailboxSecrets(rawURL string) ([]string, bool) {
	if len(rawURL) > accountHealthMaxURLBytes {
		return nil, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}
	set := accountHealthSecretSet{values: make([]string, 0, accountHealthMaxURLSecrets)}
	for key, values := range parsed.Query() {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if isAccountHealthPublicQueryValue(key, value) {
				continue
			}
			set.add(value)
		}
	}
	for _, field := range strings.Split(parsed.RawQuery, "&") {
		rawKey, rawValue, found := strings.Cut(field, "=")
		if !found {
			decoded, decodeErr := url.QueryUnescape(field)
			if decodeErr != nil {
				decoded = field
			}
			set.add(field)
			set.add(decoded)
			continue
		}
		if rawValue == "" {
			key, keyErr := url.QueryUnescape(rawKey)
			if keyErr != nil {
				key = rawKey
			}
			set.add(rawKey)
			set.add(key)
			continue
		}
		key, keyErr := url.QueryUnescape(rawKey)
		if keyErr != nil {
			key = rawKey
		}
		value, valueErr := url.QueryUnescape(rawValue)
		if valueErr != nil {
			value = rawValue
		}
		if isAccountHealthPublicQueryValue(key, value) {
			continue
		}
		set.add(rawValue)
	}
	for _, escapedSegment := range strings.Split(parsed.EscapedPath(), "/") {
		segment, decodeErr := url.PathUnescape(escapedSegment)
		if decodeErr != nil || strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			continue
		}
		set.add(segment)
		set.add(escapedSegment)
	}
	return set.values, !set.overflow
}

func isAccountHealthPublicQueryValue(key, value string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "mail", "email", "address", "user", "login":
		return strings.EqualFold(findAccountHealthEmail(value), value)
	case "page", "p", "offset", "limit", "per_page", "page_size", "size", "count":
		_, err := strconv.Atoi(value)
		return err == nil
	default:
		return false
	}
}
