package modeltrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const defaultLangfuseReadTimeout = 5 * time.Second

// LangfuseReader loads conversation message IDs from the Langfuse Public API.
type LangfuseReader struct {
	BaseURL    string
	PublicKey  string
	SecretKey  string
	HTTPClient *http.Client
}

func langfuseReaderForTracing(endpoint, publicKey, secretKey string) *LangfuseReader {
	base := langfusePublicBaseURL(endpoint)
	if base == "" || strings.TrimSpace(publicKey) == "" || strings.TrimSpace(secretKey) == "" {
		return nil
	}
	return &LangfuseReader{
		BaseURL:   base,
		PublicKey: strings.TrimSpace(publicKey),
		SecretKey: strings.TrimSpace(secretKey),
		HTTPClient: &http.Client{
			Timeout: defaultLangfuseReadTimeout,
		},
	}
}

func langfusePublicBaseURL(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

// ListChatMessageIDs returns message_id -> observation name for chat.* in a session.
func (r *LangfuseReader) ListChatMessageIDs(ctx context.Context, sessionID string) (map[string]string, error) {
	ids := make(map[string]string)
	if r == nil {
		return nil, fmt.Errorf("langfuse reader is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ids, nil
	}
	traceIDs, err := r.listTraceIDs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, traceID := range traceIDs {
		obs, err := r.listObservations(ctx, traceID)
		if err != nil {
			return nil, err
		}
		for _, item := range obs {
			if !strings.HasPrefix(item.Name, "chat.") {
				continue
			}
			messageID := strings.TrimSpace(item.MessageID)
			if messageID == "" {
				continue
			}
			ids[messageID] = strings.TrimSpace(item.Name)
		}
	}
	return ids, nil
}

func (r *LangfuseReader) listTraceIDs(ctx context.Context, sessionID string) ([]string, error) {
	page := 1
	var out []string
	for {
		q := url.Values{}
		q.Set("sessionId", sessionID)
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("limit", "100")
		body, err := r.getJSON(ctx, "/api/public/traces?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Meta struct {
				TotalPages int `json:"totalPages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data {
			if id := strings.TrimSpace(item.ID); id != "" {
				out = append(out, id)
			}
		}
		if page >= resp.Meta.TotalPages || len(resp.Data) == 0 {
			break
		}
		page++
	}
	return out, nil
}

type langfuseObservation struct {
	Name      string
	MessageID string
}

func (r *LangfuseReader) listObservations(ctx context.Context, traceID string) ([]langfuseObservation, error) {
	page := 1
	var out []langfuseObservation
	for {
		q := url.Values{}
		q.Set("traceId", traceID)
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("limit", "100")
		body, err := r.getJSON(ctx, "/api/public/observations?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data []struct {
				Name     string          `json:"name"`
				Metadata json.RawMessage `json:"metadata"`
			} `json:"data"`
			Meta struct {
				TotalPages int `json:"totalPages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data {
			out = append(out, langfuseObservation{
				Name:      strings.TrimSpace(item.Name),
				MessageID: messageIDFromMetadata(item.Metadata),
			})
		}
		if page >= resp.Meta.TotalPages || len(resp.Data) == 0 {
			break
		}
		page++
	}
	return out, nil
}

func messageIDFromMetadata(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	value := gjson.ParseBytes(raw)
	if value.IsObject() {
		return strings.TrimSpace(value.Get("message_id").String())
	}
	if value.Type == gjson.String {
		inner := strings.TrimSpace(value.String())
		if gjson.Valid(inner) {
			return strings.TrimSpace(gjson.Get(inner, "message_id").String())
		}
	}
	return ""
}

func (r *LangfuseReader) getJSON(ctx context.Context, pathQuery string) ([]byte, error) {
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultLangfuseReadTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.BaseURL, "/")+pathQuery, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(r.PublicKey, r.SecretKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("langfuse public api %s: status %d", pathQuery, resp.StatusCode)
	}
	return body, nil
}
