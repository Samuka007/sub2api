package modeltrace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelTraceSettingsStore struct {
	value string
	err   error
}

func (s *modelTraceSettingsStore) GetValue(context.Context, string) (string, error) {
	return s.value, s.err
}

func (s *modelTraceSettingsStore) Set(_ context.Context, _ string, value string) error {
	if s.err != nil {
		return s.err
	}
	s.value = value
	return nil
}

type modelTracePrefixEncryptor struct{}

func (modelTracePrefixEncryptor) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (modelTracePrefixEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "enc:") {
		return "", errors.New("invalid ciphertext")
	}
	return strings.TrimPrefix(value, "enc:"), nil
}


func decodeModelTraceRuntime(t *testing.T, store *modelTraceSettingsStore) RuntimeConfig {
	t.Helper()
	var stored RuntimeConfig
	require.NoError(t, json.Unmarshal([]byte(store.value), &stored))
	return stored
}

func TestModelTraceConfigUsesDeploymentOnly(t *testing.T) {
	deployment := config.ModelTracingConfig{
		Enabled:             true,
		Endpoint:            "https://langfuse.example.test/api/public/otel",
		PublicKey:           "deployment-public",
		SecretKey:           "deployment-secret",
		PromptMaxBytes:      101,
		ResponseMaxBytes:    202,
		MediaMaxBytes:       303,
		CaptureMediaContent: true,
	}

	manager := NewConfigManager(deployment, nil, nil, true)
	got := manager.Resolve(context.Background())

	require.Equal(t, ConfigSourceDeployment, got.Source)
	require.Equal(t, deployment, got.Config)
	require.Zero(t, got.ConfigVersion)
}

func TestModelTraceConfigRuntimeOverridesDeployment(t *testing.T) {
	runtime := RuntimeConfig{
		Configured:          true,
		Enabled:             true,
		Endpoint:            "https://runtime-langfuse.example.test/api/public/otel",
		PublicKey:           "runtime-public",
		SecretKeyEncrypted:  "enc:runtime-secret",
		PromptMaxBytes:      404,
		ResponseMaxBytes:    505,
		MediaMaxBytes:       606,
		CaptureMediaContent: true,
		ConfigVersion:       7,
	}
	raw, err := json.Marshal(runtime)
	require.NoError(t, err)
	store := &modelTraceSettingsStore{value: string(raw)}
	manager := NewConfigManager(config.ModelTracingConfig{
		Enabled: true, Endpoint: "https://deployment.example.test/api/public/otel",
		PublicKey: "deployment-public", SecretKey: "deployment-secret",
	}, store, modelTracePrefixEncryptor{}, true)

	got := manager.Resolve(context.Background())

	require.Equal(t, ConfigSourceRuntime, got.Source)
	require.Equal(t, int64(7), got.ConfigVersion)
	require.Equal(t, "runtime-public", got.Config.PublicKey)
	require.Equal(t, "runtime-secret", got.Config.SecretKey)
	require.Equal(t, 404, got.Config.PromptMaxBytes)
}

func TestModelTraceConfigRuntimeCanDisableDeployment(t *testing.T) {
	raw, err := json.Marshal(RuntimeConfig{Configured: true, Enabled: false, ConfigVersion: 8})
	require.NoError(t, err)
	manager := NewConfigManager(config.ModelTracingConfig{
		Enabled: true, Endpoint: "https://deployment.example.test/api/public/otel",
		PublicKey: "deployment-public", SecretKey: "deployment-secret",
	}, &modelTraceSettingsStore{value: string(raw)}, modelTracePrefixEncryptor{}, true)

	got := manager.Resolve(context.Background())

	require.Equal(t, ConfigSourceRuntime, got.Source)
	require.False(t, got.Config.Enabled)
	require.Equal(t, int64(8), got.ConfigVersion)
}

func TestModelTraceConfigBrokenRuntimeFallsBackToDeployment(t *testing.T) {
	deployment := config.ModelTracingConfig{
		Enabled: true, Endpoint: "https://deployment.example.test/api/public/otel",
		PublicKey: "deployment-public", SecretKey: "deployment-secret",
	}

	t.Run("malformed JSON", func(t *testing.T) {
		manager := NewConfigManager(deployment, &modelTraceSettingsStore{value: "not-json"}, modelTracePrefixEncryptor{}, true)
		got := manager.Resolve(context.Background())
		require.Equal(t, ConfigSourceDeployment, got.Source)
		require.Equal(t, "deployment-public", got.Config.PublicKey)
	})

	t.Run("secret cannot decrypt", func(t *testing.T) {
		raw, err := json.Marshal(RuntimeConfig{
			Configured: true, Enabled: true, Endpoint: "https://runtime.example.test/api/public/otel",
			PublicKey: "runtime-public", SecretKeyEncrypted: "broken", ConfigVersion: 9,
		})
		require.NoError(t, err)
		manager := NewConfigManager(deployment, &modelTraceSettingsStore{value: string(raw)}, modelTracePrefixEncryptor{}, true)
		got := manager.Resolve(context.Background())
		require.Equal(t, ConfigSourceDeployment, got.Source)
		require.Equal(t, "deployment-public", got.Config.PublicKey)
	})
}

func TestModelTraceConfigSecretTriStateAndCAS(t *testing.T) {
	raw, err := json.Marshal(RuntimeConfig{
		Configured: true, Enabled: true, Endpoint: "https://old.example.test/api/public/otel",
		PublicKey: "old-public", SecretKeyEncrypted: "enc:old-secret",
		PromptMaxBytes: 100, ResponseMaxBytes: 200, MediaMaxBytes: 300, ConfigVersion: 3,
	})
	require.NoError(t, err)
	store := &modelTraceSettingsStore{value: string(raw)}
	manager := NewConfigManager(config.ModelTracingConfig{}, store, modelTracePrefixEncryptor{}, true)

	preserved, err := manager.Save(context.Background(), UpdateConfigRequest{
		ExpectedConfigVersion: 3, Enabled: true,
		Endpoint: "https://preserved.example.test/api/public/otel", PublicKey: "preserved-public",
		PromptMaxBytes: 101, ResponseMaxBytes: 201, MediaMaxBytes: 301,
	}, 42)
	require.NoError(t, err)
	require.True(t, preserved.HasSecret)
	require.Equal(t, int64(4), preserved.ConfigVersion)
	stored := decodeModelTraceRuntime(t, store)
	require.Equal(t, "enc:old-secret", stored.SecretKeyEncrypted)
	require.Equal(t, int64(42), stored.UpdatedBy)

	replaced, err := manager.Save(context.Background(), UpdateConfigRequest{
		ExpectedConfigVersion: 4, Enabled: true,
		Endpoint: "https://replaced.example.test/api/public/otel", PublicKey: "replaced-public",
		SecretKey: new("new-secret"),
		PromptMaxBytes: 102, ResponseMaxBytes: 202, MediaMaxBytes: 302,
	}, 43)
	require.NoError(t, err)
	require.True(t, replaced.HasSecret)
	require.Equal(t, int64(5), replaced.ConfigVersion)
	stored = decodeModelTraceRuntime(t, store)
	require.Equal(t, "enc:new-secret", stored.SecretKeyEncrypted)

	cleared, err := manager.Save(context.Background(), UpdateConfigRequest{
		ExpectedConfigVersion: 5, Enabled: false,
		Endpoint: "", PublicKey: "", SecretKey: new(""),
		PromptMaxBytes: 103, ResponseMaxBytes: 203, MediaMaxBytes: 303,
	}, 44)
	require.NoError(t, err)
	require.False(t, cleared.HasSecret)
	require.Equal(t, int64(6), cleared.ConfigVersion)
	stored = decodeModelTraceRuntime(t, store)
	require.Empty(t, stored.SecretKeyEncrypted)

	beforeConflict := store.value
	_, err = manager.Save(context.Background(), UpdateConfigRequest{
		ExpectedConfigVersion: 5, Enabled: false,
		PromptMaxBytes: 104, ResponseMaxBytes: 204, MediaMaxBytes: 304,
	}, 45)
	require.Error(t, err)
	require.Equal(t, beforeConflict, store.value)
}

func TestModelTraceConfigAdminGETPUTHidesSecretAndEnforcesCAS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw, err := json.Marshal(RuntimeConfig{
		Configured: true, Enabled: true, Endpoint: "https://admin.example.test/api/public/otel",
		PublicKey: "admin-public", SecretKeyEncrypted: "enc:admin-secret",
		PromptMaxBytes: 100, ResponseMaxBytes: 200, MediaMaxBytes: 300, ConfigVersion: 10,
	})
	require.NoError(t, err)
	store := &modelTraceSettingsStore{value: string(raw)}
	handler := NewAdminHandler(NewConfigManager(config.ModelTracingConfig{}, store, modelTracePrefixEncryptor{}, true))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99})
		c.Set(string(middleware.ContextKeyUserRole), "admin")
		c.Next()
	})
	router.GET("/api/v1/admin/model-tracing/config", handler.GetConfig)
	router.PUT("/api/v1/admin/model-tracing/config", handler.UpdateConfig)

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/model-tracing/config", nil))
	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.Contains(t, getRecorder.Body.String(), `"has_secret":true`)
	require.NotContains(t, getRecorder.Body.String(), "admin-secret")
	require.NotContains(t, getRecorder.Body.String(), "secret_key_encrypted")

	request := UpdateConfigRequest{
		ExpectedConfigVersion: 10, Enabled: true,
		Endpoint: "https://updated.example.test/api/public/otel", PublicKey: "updated-public",
		SecretKey: new("updated-secret"), PromptMaxBytes: 101, ResponseMaxBytes: 201, MediaMaxBytes: 301,
	}
	body, err := json.Marshal(request)
	require.NoError(t, err)
	putRecorder := httptest.NewRecorder()
	router.ServeHTTP(putRecorder, httptest.NewRequest(http.MethodPut, "/api/v1/admin/model-tracing/config", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, putRecorder.Code)
	require.NotContains(t, putRecorder.Body.String(), "updated-secret")
	require.Equal(t, "enc:updated-secret", decodeModelTraceRuntime(t, store).SecretKeyEncrypted)

	conflictRecorder := httptest.NewRecorder()
	router.ServeHTTP(conflictRecorder, httptest.NewRequest(http.MethodPut, "/api/v1/admin/model-tracing/config", bytes.NewReader(body)))
	require.Equal(t, http.StatusConflict, conflictRecorder.Code)
}

func TestModelTraceConfigAdminRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAdminHandler(NewConfigManager(config.ModelTracingConfig{}, &modelTraceSettingsStore{}, modelTracePrefixEncryptor{}, true))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})
		c.Set(string(middleware.ContextKeyUserRole), "user")
		c.Next()
	})
	router.GET("/api/v1/admin/model-tracing/config", handler.GetConfig)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/model-tracing/config", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestModelTraceConfigRejectsNewSecretWithoutDurableEncryptionKey(t *testing.T) {
	store := &modelTraceSettingsStore{}
	manager := NewConfigManager(config.ModelTracingConfig{}, store, modelTracePrefixEncryptor{}, false)

	_, err := manager.Save(context.Background(), UpdateConfigRequest{
		ExpectedConfigVersion: 0, Enabled: true,
		Endpoint: "https://runtime.example.test/api/public/otel", PublicKey: "runtime-public",
		SecretKey: new("must-not-persist"),
		PromptMaxBytes: 100, ResponseMaxBytes: 200, MediaMaxBytes: 300,
	}, 99)

	require.Error(t, err)
	require.Empty(t, store.value)
}
