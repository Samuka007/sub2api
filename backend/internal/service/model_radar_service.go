package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	modelRadarSettingKey            = "model_radar_snapshot"
	modelRadarSourceName            = "Codex Radar"
	modelRadarSourceURL             = "https://codexradar.com/"
	modelRadarUserAgent             = "STEM-Model-Radar/1.0 (+https://sub2api.local)"
	modelRadarRefreshInterval       = 12 * time.Hour
	modelRadarManualRefreshCooldown = 30 * time.Minute
	modelRadarRequestTimeout        = 15 * time.Second
	modelRadarMaxResponseBytes      = 2 * 1024 * 1024
)

var (
	// ErrModelRadarRefreshTooSoon protects the public source from repeated manual refreshes.
	ErrModelRadarRefreshTooSoon = errors.New("model radar was refreshed recently")

	modelRadarUpdatedAtPattern = regexp.MustCompile(`\d{1,2}月\d{1,2}日\d{1,2}:\d{2}更新`)
	modelRadarNumberPattern    = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	modelRadarCostPattern      = regexp.MustCompile(`^\$[\d,.]+$`)
	modelRadarDurationPattern  = regexp.MustCompile(`^\d+(?:\.\d+)?h$`)
)

// ModelRadarSnapshot is the locally cached, structured subset of the public radar page.
// Raw third-party HTML is deliberately not stored.
type ModelRadarSnapshot struct {
	SourceName string            `json:"source_name"`
	SourceURL  string            `json:"source_url"`
	FetchedAt  time.Time         `json:"fetched_at"`
	Quota      ModelRadarSection `json:"quota"`
	Fast       ModelRadarSection `json:"fast"`
	Quality    ModelRadarSection `json:"quality"`
}

// ModelRadarView adds runtime state that is not persisted with a snapshot.
type ModelRadarView struct {
	ModelRadarSnapshot
	Stale            bool      `json:"stale"`
	RefreshAllowedAt time.Time `json:"refresh_allowed_at"`
}

type ModelRadarSection struct {
	Title           string                  `json:"title"`
	SourceUpdatedAt string                  `json:"source_updated_at,omitempty"`
	Summary         []string                `json:"summary,omitempty"`
	Highlights      []string                `json:"highlights,omitempty"`
	Table           *ModelRadarTable        `json:"table,omitempty"`
	Cards           []ModelRadarQualityCard `json:"cards,omitempty"`
}

type ModelRadarTable struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

type ModelRadarQualityCard struct {
	Model    string `json:"model"`
	Score    string `json:"score"`
	Cost     string `json:"cost"`
	Duration string `json:"duration"`
}

// ModelRadarService fetches a small public snapshot at a bounded cadence and keeps
// the last known-good result in the settings store.
type ModelRadarService struct {
	settingRepo SettingRepository
	httpClient  *http.Client
	now         func() time.Time
	refreshMu   sync.Mutex
}

func NewModelRadarService(settingRepo SettingRepository) *ModelRadarService {
	return &ModelRadarService{
		settingRepo: settingRepo,
		httpClient: &http.Client{
			Timeout: modelRadarRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

// Get returns cached data when it is current. When the snapshot is overdue, it tries
// one refresh and falls back to the last successful snapshot if the source is unavailable.
func (s *ModelRadarService) Get(ctx context.Context) (*ModelRadarView, error) {
	return s.obtain(ctx, false)
}

// Refresh is an admin-triggered refresh with a shorter cooldown than the normal cadence.
func (s *ModelRadarService) Refresh(ctx context.Context) (*ModelRadarView, error) {
	return s.obtain(ctx, true)
}

func (s *ModelRadarService) obtain(ctx context.Context, manual bool) (*ModelRadarView, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("model radar service is not initialized")
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	cached, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if manual && cached != nil && s.now().Sub(cached.FetchedAt) < modelRadarManualRefreshCooldown {
		return s.view(cached, false), ErrModelRadarRefreshTooSoon
	}
	if !manual && cached != nil && s.isFresh(cached) {
		return s.view(cached, false), nil
	}

	fresh, fetchErr := s.fetchAndStore(ctx)
	if fetchErr == nil {
		return s.view(fresh, false), nil
	}
	if cached != nil && !manual {
		return s.view(cached, true), nil
	}
	return nil, fmt.Errorf("fetch model radar: %w", fetchErr)
}

func (s *ModelRadarService) fetchAndStore(ctx context.Context) (*ModelRadarSnapshot, error) {
	requestCtx, cancel := context.WithTimeout(ctx, modelRadarRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, modelRadarSourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", modelRadarUserAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && !strings.Contains(strings.ToLower(contentType), "text/html") {
		return nil, fmt.Errorf("unexpected source content type %q", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(modelRadarMaxResponseBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > modelRadarMaxResponseBytes {
		return nil, fmt.Errorf("source response exceeds %d bytes", modelRadarMaxResponseBytes)
	}

	snapshot, err := parseModelRadarHTML(body)
	if err != nil {
		return nil, err
	}
	snapshot.SourceName = modelRadarSourceName
	snapshot.SourceURL = modelRadarSourceURL
	snapshot.FetchedAt = s.now().UTC()

	if err := s.save(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *ModelRadarService) load(ctx context.Context) (*ModelRadarSnapshot, error) {
	raw, err := s.settingRepo.GetValue(ctx, modelRadarSettingKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var snapshot ModelRadarSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, nil
	}
	if snapshot.FetchedAt.IsZero() || snapshot.SourceURL == "" {
		return nil, nil
	}
	return &snapshot, nil
}

func (s *ModelRadarService) save(ctx context.Context, snapshot *ModelRadarSnapshot) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, modelRadarSettingKey, string(raw))
}

func (s *ModelRadarService) isFresh(snapshot *ModelRadarSnapshot) bool {
	return snapshot != nil && !snapshot.FetchedAt.IsZero() && s.now().Sub(snapshot.FetchedAt) < modelRadarRefreshInterval
}

func (s *ModelRadarService) view(snapshot *ModelRadarSnapshot, stale bool) *ModelRadarView {
	return &ModelRadarView{
		ModelRadarSnapshot: *snapshot,
		Stale:              stale,
		RefreshAllowedAt:   snapshot.FetchedAt.Add(modelRadarManualRefreshCooldown),
	}
}

func parseModelRadarHTML(body []byte) (*ModelRadarSnapshot, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	quota := parseModelRadarSection(doc, "quota-radar")
	fast := parseModelRadarSection(doc, "fast-radar")
	quality := parseModelRadarSection(doc, "model-iq")
	if quota == nil || fast == nil || quality == nil {
		return nil, errors.New("source page does not contain the expected radar sections")
	}

	quota.Table = extractFirstTable(findElementByClass(doc, "quota-radar"))
	fast.Table = extractFirstTable(findElementByClass(doc, "fast-radar"))
	quality.Cards = extractQualityCards(findElementByClass(doc, "model-iq"))
	if len(quality.Cards) == 0 {
		return nil, errors.New("source page does not contain model quality cards")
	}

	return &ModelRadarSnapshot{
		Quota:   *quota,
		Fast:    *fast,
		Quality: *quality,
	}, nil
}

func parseModelRadarSection(doc *html.Node, className string) *ModelRadarSection {
	section := findElementByClass(doc, className)
	if section == nil {
		return nil
	}

	heading := findFirstElement(section, "h2")
	if heading == nil {
		heading = findFirstElement(section, "h3")
	}
	title, sourceUpdatedAt := splitModelRadarHeading(nodeText(heading))
	if title == "" {
		return nil
	}

	return &ModelRadarSection{
		Title:           title,
		SourceUpdatedAt: sourceUpdatedAt,
		Summary:         extractParagraphs(section, 2),
		Highlights:      extractStrongTexts(section, 3),
	}
}

func splitModelRadarHeading(heading string) (string, string) {
	updatedAt := modelRadarUpdatedAtPattern.FindString(heading)
	if updatedAt == "" {
		return heading, ""
	}
	return strings.TrimSpace(strings.Replace(heading, updatedAt, "", 1)), updatedAt
}

func extractParagraphs(root *html.Node, limit int) []string {
	paragraphs := make([]string, 0, limit)
	for _, node := range findElements(root, "p") {
		text := truncateModelRadarText(nodeText(node), 360)
		if text == "" {
			continue
		}
		paragraphs = append(paragraphs, text)
		if len(paragraphs) == limit {
			break
		}
	}
	return paragraphs
}

func extractStrongTexts(root *html.Node, limit int) []string {
	values := make([]string, 0, limit)
	seen := make(map[string]struct{})
	for _, node := range findElements(root, "strong") {
		text := nodeText(node)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		values = append(values, text)
		if len(values) == limit {
			break
		}
	}
	return values
}

func extractFirstTable(root *html.Node) *ModelRadarTable {
	table := findFirstElement(root, "table")
	if table == nil {
		return nil
	}

	result := &ModelRadarTable{}
	for _, row := range findElements(table, "tr") {
		cells, hasHeader := extractTableRow(row)
		if len(cells) == 0 {
			continue
		}
		if hasHeader && len(result.Headers) == 0 {
			result.Headers = cells
			continue
		}
		result.Rows = append(result.Rows, cells)
	}
	if len(result.Headers) == 0 && len(result.Rows) > 1 {
		result.Headers = result.Rows[0]
		result.Rows = result.Rows[1:]
	}
	if len(result.Headers) == 0 && len(result.Rows) == 0 {
		return nil
	}
	return result
}

func extractTableRow(row *html.Node) ([]string, bool) {
	var cells []string
	hasHeader := false
	for child := row.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || (child.Data != "th" && child.Data != "td") {
			continue
		}
		text := nodeText(child)
		if text == "" {
			continue
		}
		cells = append(cells, text)
		hasHeader = hasHeader || child.Data == "th"
	}
	return cells, hasHeader
}

func extractQualityCards(root *html.Node) []ModelRadarQualityCard {
	if root == nil {
		return nil
	}
	result := make([]ModelRadarQualityCard, 0)
	seen := make(map[string]struct{})
	for _, button := range findElements(root, "button") {
		fields := strings.Fields(nodeText(button))
		for i := 1; i+2 < len(fields); i++ {
			if !modelRadarNumberPattern.MatchString(fields[i]) ||
				!modelRadarCostPattern.MatchString(fields[i+1]) ||
				!modelRadarDurationPattern.MatchString(fields[i+2]) {
				continue
			}
			model := strings.TrimSpace(strings.Join(fields[:i], " "))
			if model == "" {
				break
			}
			if _, exists := seen[model]; !exists {
				seen[model] = struct{}{}
				result = append(result, ModelRadarQualityCard{
					Model:    model,
					Score:    fields[i],
					Cost:     fields[i+1],
					Duration: fields[i+2],
				})
			}
			break
		}
	}
	return result
}

func findElementByClass(root *html.Node, className string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && hasClass(root, className) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findElementByClass(child, className); found != nil {
			return found
		}
	}
	return nil
}

func findFirstElement(root *html.Node, tag string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && root.Data == tag {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirstElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func findElements(root *html.Node, tag string) []*html.Node {
	if root == nil {
		return nil
	}
	result := make([]*html.Node, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			result = append(result, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func hasClass(node *html.Node, expected string) bool {
	for _, attr := range node.Attr {
		if attr.Key != "class" {
			continue
		}
		for _, className := range strings.Fields(attr.Val) {
			if className == expected {
				return true
			}
		}
	}
	return false
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	parts := make([]string, 0)
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text := strings.TrimSpace(current.Data)
			if text != "" {
				parts = append(parts, text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, " ")
}

func truncateModelRadarText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
