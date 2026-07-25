package modeltrace_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type blockingConversationReadbackServer struct {
	server       *httptest.Server
	readStarted  chan struct{}
	readFinished chan struct{}
	release      chan struct{}
	releaseOnce  sync.Once
}

func newBlockingConversationReadbackServer(t *testing.T) *blockingConversationReadbackServer {
	t.Helper()
	blocker := &blockingConversationReadbackServer{
		readStarted:  make(chan struct{}, 1),
		readFinished: make(chan struct{}, 1),
		release:      make(chan struct{}),
	}
	blocker.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/public/traces" {
			select {
			case blocker.readStarted <- struct{}{}:
			default:
			}
			select {
			case <-blocker.release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[],"meta":{"totalPages":1}}`))
			select {
			case blocker.readFinished <- struct{}{}:
			default:
			}
			return
		}

		// The trace exporter only needs a successful OTLP response; an empty
		// ExportTraceServiceResponse is valid protobuf.
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		blocker.unblock()
		blocker.server.Close()
	})
	return blocker
}

func (b *blockingConversationReadbackServer) unblock() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockingConversationReadbackServer) waitForRead(t *testing.T) {
	t.Helper()
	select {
	case <-b.readStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("blocking Langfuse read-back did not start")
	}
}

func (b *blockingConversationReadbackServer) waitForReadFinish(t *testing.T) {
	t.Helper()
	select {
	case <-b.readFinished:
	case <-time.After(time.Second):
		t.Fatal("released Langfuse read-back did not finish")
	}
}

func newConversationReadbackTestManager(t *testing.T, endpoint string) *modeltrace.Manager {
	t.Helper()
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: endpoint + "/api/public/otel", PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
	})
	require.NoError(t, err)
	return manager
}

func TestModelTraceHTTPFinishDoesNotWaitForBlockingConversationReadback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	blocker := newBlockingConversationReadbackServer(t)
	manager := newConversationReadbackTestManager(t, blocker.server.URL)
	router := gin.New()
	router.POST("/v1/responses", manager.CandidateMiddleware(), installUsageTestIdentity(), func(c *gin.Context) {
		_, err := c.GetRawData()
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{"id": "resp_async_readback"})
	})

	finished := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-test","session_id":"http-blocking-session"}`))
		router.ServeHTTP(httptest.NewRecorder(), request)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("HTTP response waited for blocking Langfuse read-back")
	}

	blocker.waitForRead(t)
	blocker.unblock()
	blocker.waitForReadFinish(t)
	shutdownUsageTestManager(t, manager)
}

func TestModelTraceResponsesWSTurnEndDoesNotWaitForBlockingConversationReadback(t *testing.T) {
	blocker := newBlockingConversationReadbackServer(t)
	manager := newConversationReadbackTestManager(t, blocker.server.URL)
	turn := manager.StartResponsesWSTurn(context.Background(), modeltrace.ResponsesWSTurnMetadata{
		SessionID: "ws-blocking-session", TurnRequestID: "ws-turn", Path: "/v1/responses", Model: "gpt-test",
	}, []byte(`{"type":"response.create","model":"gpt-test"}`))
	require.NotNil(t, turn)
	turn.ObserveClientWrite([]byte(`{"type":"response.completed","response":{"id":"resp_ws"}}`), nil)

	finished := make(chan struct{})
	go func() {
		turn.End(modeltrace.TestingStreamStatusCompleted, "", nil)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Responses WebSocket turn waited for blocking Langfuse read-back")
	}

	blocker.waitForRead(t)
	blocker.unblock()
	blocker.waitForReadFinish(t)
	shutdownUsageTestManager(t, manager)
}
