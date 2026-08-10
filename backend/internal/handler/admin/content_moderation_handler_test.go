package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestContentModerationHandlerUpdateConfigPersistsTrustedAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newTestSettingRepo()
	svc := service.NewContentModerationService(repo, nil, nil, nil, nil, nil, nil, nil)
	handler := NewContentModerationHandler(svc)
	router := gin.New()
	router.PUT("/content-moderation/config", handler.UpdateConfig)

	expiresAt := "2026-08-01T12:00:00Z"
	body := []byte(`{
		"upstream_protocol": "anthropic_messages",
		"base_url": "https://api.anthropic.com",
		"model": "claude-test",
		"trusted_api_keys": [{
			"api_key_id": 42,
			"models": ["gpt-5.6-sol"],
			"endpoints": ["/v1/responses"],
			"expires_at": "` + expiresAt + `",
			"reason": "administrator maintenance"
		}]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/content-moderation/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	raw := repo.values[service.SettingKeyContentModerationConfig]
	require.NotEmpty(t, raw)
	var saved service.ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &saved))
	require.Equal(t, service.ContentModerationUpstreamProtocolAnthropicMessages, saved.UpstreamProtocol)
	require.Equal(t, "https://api.anthropic.com", saved.BaseURL)
	require.Equal(t, "claude-test", saved.Model)
	require.Len(t, saved.TrustedAPIKeys, 1)
	require.Equal(t, int64(42), saved.TrustedAPIKeys[0].APIKeyID)
	require.Equal(t, []string{"gpt-5.6-sol"}, saved.TrustedAPIKeys[0].Models)
	require.Equal(t, []string{"/v1/responses"}, saved.TrustedAPIKeys[0].Endpoints)
	require.Equal(t, "administrator maintenance", saved.TrustedAPIKeys[0].Reason)
	wantExpiry, err := time.Parse(time.RFC3339, expiresAt)
	require.NoError(t, err)
	require.Equal(t, wantExpiry, *saved.TrustedAPIKeys[0].ExpiresAt)
}

func TestContentModerationHandlerTestAPIKeysUsesAnthropicMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var requestedPath string
	var requestedAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedAPIKey = r.Header.Get("x-api-key")
		scores := make(map[string]float64)
		for _, category := range service.ContentModerationCategories() {
			scores[category] = 0.01
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{
				"type":  "tool_use",
				"name":  "submit_moderation",
				"input": map[string]any{"category_scores": scores},
			}},
		}))
	}))
	defer upstream.Close()

	svc := service.NewContentModerationService(newTestSettingRepo(), nil, nil, nil, nil, nil, nil, nil)
	handler := NewContentModerationHandler(svc)
	router := gin.New()
	router.POST("/content-moderation/test-api-keys", handler.TestAPIKeys)

	body, err := json.Marshal(map[string]any{
		"api_keys":          []string{"sk-ant-handler-test"},
		"upstream_protocol": service.ContentModerationUpstreamProtocolAnthropicMessages,
		"base_url":          upstream.URL,
		"model":             "claude-test",
		"prompt":            "classify this",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/content-moderation/test-api-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, "/v1/messages", requestedPath)
	require.Equal(t, "sk-ant-handler-test", requestedAPIKey)
}
