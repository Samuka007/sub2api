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
	svc := service.NewContentModerationService(repo, nil, nil, nil, nil, nil, nil)
	handler := NewContentModerationHandler(svc)
	router := gin.New()
	router.PUT("/content-moderation/config", handler.UpdateConfig)

	expiresAt := "2026-08-01T12:00:00Z"
	body := []byte(`{
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
	require.Len(t, saved.TrustedAPIKeys, 1)
	require.Equal(t, int64(42), saved.TrustedAPIKeys[0].APIKeyID)
	require.Equal(t, []string{"gpt-5.6-sol"}, saved.TrustedAPIKeys[0].Models)
	require.Equal(t, []string{"/v1/responses"}, saved.TrustedAPIKeys[0].Endpoints)
	require.Equal(t, "administrator maintenance", saved.TrustedAPIKeys[0].Reason)
	wantExpiry, err := time.Parse(time.RFC3339, expiresAt)
	require.NoError(t, err)
	require.Equal(t, wantExpiry, *saved.TrustedAPIKeys[0].ExpiresAt)
}
