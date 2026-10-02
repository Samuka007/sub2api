//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelIQReaderStub struct {
	view *service.ModelIQView
	err  error
}

func (s *modelIQReaderStub) Get(context.Context) (*service.ModelIQView, error) {
	return s.view, s.err
}

func (s *modelIQReaderStub) Refresh(context.Context) (*service.ModelIQView, error) {
	return s.view, s.err
}

func TestModelIQHandlerGetSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &ModelIQHandler{modelIQService: &modelIQReaderStub{
		view: &service.ModelIQView{
			MonitoredAt: "2026-07-15T12:00:00+08:00",
			Status:      "community_confirmed",
			ModelIQ: service.ModelIQData{
				Comparisons: map[string]service.ModelIQComparison{
					"gpt_test": {Label: "GPT Test", Model: "gpt-test"},
				},
			},
			FetchedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		},
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-iq", nil)

	handler.Get(c)

	require.Equal(t, http.StatusOK, w.Code)
	var envelope response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Equal(t, 0, envelope.Code)
	rawData, err := json.Marshal(envelope.Data)
	require.NoError(t, err)
	require.Contains(t, string(rawData), `"model_iq"`)
	require.Contains(t, string(rawData), `"gpt_test"`)
	require.Contains(t, string(rawData), `"monitored_at"`)
	require.Contains(t, string(rawData), `"community_confirmed"`)
}

func TestModelIQHandlerGetMapsControlledUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &ModelIQHandler{modelIQService: &modelIQReaderStub{
		err: service.ErrModelIQUpstreamUnavailable.WithCause(errors.New("sensitive upstream body")),
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-iq", nil)

	handler.Get(c)

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "MODEL_IQ_UPSTREAM_UNAVAILABLE")
	require.Contains(t, w.Body.String(), "model IQ ranking is temporarily unavailable")
	require.NotContains(t, w.Body.String(), "sensitive upstream body")
}

func TestModelIQHandlerGetUnavailableWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &ModelIQHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-iq", nil)

	handler.Get(c)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "token")
}

func TestModelIQHandlerRefreshSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &ModelIQHandler{modelIQService: &modelIQReaderStub{
		view: &service.ModelIQView{
			ModelIQ: service.ModelIQData{
				Comparisons: map[string]service.ModelIQComparison{
					"gpt_test": {Label: "GPT Test", Model: "gpt-test"},
				},
			},
		},
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/model-iq/refresh", nil)

	handler.Refresh(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "gpt_test")
}
