package routes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelTraceRouteMatrix(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "chat completions", method: http.MethodPost, path: "/v1/chat/completions", want: true},
		{name: "messages", method: http.MethodPost, path: "/v1/messages", want: true},
		{name: "responses", method: http.MethodPost, path: "/v1/responses", want: true},
		{name: "embeddings", method: http.MethodPost, path: "/v1/embeddings", want: true},
		{name: "Gemini generation", method: http.MethodPost, path: "/v1beta/models/gemini-2.5-pro:generateContent", want: true},
		{name: "async image submission", method: http.MethodPost, path: "/v1/images/generations/async", want: true},
		{name: "model list", method: http.MethodGet, path: "/v1/models"},
		{name: "usage", method: http.MethodGet, path: "/v1/usage"},
		{name: "response websocket handshake", method: http.MethodGet, path: "/v1/responses"},
		{name: "async image task polling", method: http.MethodGet, path: "/v1/images/tasks/task-1"},
		{name: "batch download", method: http.MethodGet, path: "/v1/images/batches/1/download"},
		{name: "batch cancellation", method: http.MethodPost, path: "/v1/images/batches/1/cancel"},
		{name: "video status", method: http.MethodGet, path: "/v1/videos/request-1"},
		{name: "response cancellation", method: http.MethodPost, path: "/v1/responses/resp-1/cancel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isModelTraceCandidate(tt.method, tt.path))
		})
	}
}
