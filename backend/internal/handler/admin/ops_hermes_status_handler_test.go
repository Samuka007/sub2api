package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type hermesStatusResponseEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func TestGetHermesStatusReturnsServiceUnavailableWhenNotWired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/hermes/status", nil)

	h.GetHermesStatus(ctx)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	var envelope hermesStatusResponseEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, http.StatusServiceUnavailable, envelope.Code)
}

func TestGetHermesStatusReturnsDisabledSnapshotWithoutRunningHermes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewQuotaRecoveryService(nil, nil, nil, &config.Config{
		QuotaRecovery: config.QuotaRecoveryConfig{
			Enabled:         false,
			IntervalSeconds: 86400,
			BatchSize:       50,
			Concurrency:     3,
			TimeoutSeconds:  75,
			JitterSeconds:   10,
		},
	})
	h := ProvideOpsHandler(nil, svc)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/hermes/status", nil)

	h.GetHermesStatus(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope hermesStatusResponseEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, 0, envelope.Code)
	var status service.QuotaRecoveryStatus
	require.NoError(t, json.Unmarshal(envelope.Data, &status))
	require.False(t, status.Enabled)
	require.Equal(t, "disabled", status.Status)
	require.False(t, status.LeaseHeld)
	require.Nil(t, status.CurrentRun)
	require.Nil(t, status.LastRun)
}
