package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

const mockResponseMarker = "mock-loadtest"

// mockUpstreamConfig configures the in-process mock LLM upstream HTTP server.
type mockUpstreamConfig struct {
	addr           string        // listen address; :0 picks a free port
	latency        time.Duration // artificial per-response delay
	fail           float64       // initial fraction [0,1] of responses returning HTTP 500
	responseMarker string        // per-run marker returned only by this mock
}

// mockUpstream serves canned Chat Completions and Responses payloads and never contacts a real upstream.
type mockUpstream struct {
	server   *http.Server
	listener net.Listener
	cfg      mockUpstreamConfig
	failBits atomic.Uint64

	// calls counts the number of completion/response requests the mock has served.
	calls atomic.Int64
}

// startMockUpstream starts the mock upstream server in a background goroutine and
// blocks until it is listening. The returned mock's Close shuts the server down.
func startMockUpstream(ctx context.Context, cfg mockUpstreamConfig) (*mockUpstream, error) {
	if cfg.responseMarker == "" {
		cfg.responseMarker = mockResponseMarker
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.addr, err)
	}
	m := &mockUpstream{listener: ln, cfg: cfg}
	m.SetFailRate(cfg.fail)
	mux := http.NewServeMux()
	mux.HandleFunc(mockUpstreamPath, m.handleChatCompletion)
	mux.HandleFunc(mockResponsesPath, m.handleResponses)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	m.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = m.server.Serve(ln)
	}()
	return m, nil
}

// Addr returns the actual address the mock is listening on.
func (m *mockUpstream) Addr() string {
	return m.listener.Addr().String()
}

// Calls returns the number of completion/response requests served by this mock.
func (m *mockUpstream) Calls() int64 {
	return m.calls.Load()
}

// SetFailRate atomically changes the failure injection rate after preflight.
func (m *mockUpstream) SetFailRate(rate float64) {
	m.failBits.Store(math.Float64bits(rate))
}

// Close shuts the mock upstream server down.
func (m *mockUpstream) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return m.server.Shutdown(ctx)
}

func (m *mockUpstream) handleChatCompletion(w http.ResponseWriter, r *http.Request) {
	if !m.beginResponse(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, mockCompletion{
		ID:      m.cfg.responseMarker,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "loadtest-mock",
		Choices: []mockChoice{{
			Index: 0,
			Message: mockMessage{
				Role:    "assistant",
				Content: "pong (canned mock response): " + m.cfg.responseMarker,
			},
			FinishReason: "stop",
		}},
		Usage: mockUsage{PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11},
	})
}

func (m *mockUpstream) handleResponses(w http.ResponseWriter, r *http.Request) {
	if !m.beginResponse(w, r) {
		return
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, mockError{
			Error: mockErrBody{Message: "invalid canned Responses request", Type: "invalid_request_error"},
		})
		return
	}
	response := m.newResponse()
	if !request.Stream {
		writeJSON(w, http.StatusOK, response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: ")
	_ = json.NewEncoder(w).Encode(struct {
		Type     string       `json:"type"`
		Response mockResponse `json:"response"`
	}{Type: "response.completed", Response: response})
	_, _ = io.WriteString(w, "\ndata: [DONE]\n\n")
}

func (m *mockUpstream) newResponse() mockResponse {
	return mockResponse{
		ID:        m.cfg.responseMarker,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Status:    "completed",
		Model:     "loadtest-mock",
		Output: []mockResponseOutput{{
			ID:     "msg_" + m.cfg.responseMarker,
			Type:   "message",
			Status: "completed",
			Role:   "assistant",
			Content: []mockResponseContent{{
				Type: "output_text",
				Text: "pong (canned mock response): " + m.cfg.responseMarker,
			}},
		}},
		Usage: mockResponseUsage{InputTokens: 5, OutputTokens: 6, TotalTokens: 11},
	}
}

func (m *mockUpstream) beginResponse(w http.ResponseWriter, r *http.Request) bool {
	m.calls.Add(1)
	if m.cfg.latency > 0 {
		timer := time.NewTimer(m.cfg.latency)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return false
		}
	}
	if rate := math.Float64frombits(m.failBits.Load()); rate > 0 && mathrand.Float64() < rate {
		writeJSON(w, http.StatusInternalServerError, mockError{
			Error: mockErrBody{Message: "canned mock upstream error (500)", Type: "mock_error"},
		})
		return false
	}
	return true
}

func newMockResponseMarker() (string, error) {
	var nonce [16]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate mock response marker: %w", err)
	}
	return mockResponseMarker + "-" + hex.EncodeToString(nonce[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Types deliberately mirror the OpenAI chat-completion shape so the mock is a
// realistic stand-in for the LLM upstream the gateway normally proxies to.

type mockCompletion struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []mockChoice `json:"choices"`
	Usage   mockUsage    `json:"usage"`
}

type mockChoice struct {
	Index        int         `json:"index"`
	Message      mockMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type mockMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type mockUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type mockResponse struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	CreatedAt int64                `json:"created_at"`
	Status    string               `json:"status"`
	Model     string               `json:"model"`
	Output    []mockResponseOutput `json:"output"`
	Usage     mockResponseUsage    `json:"usage"`
}

type mockResponseOutput struct {
	ID      string                `json:"id"`
	Type    string                `json:"type"`
	Status  string                `json:"status"`
	Role    string                `json:"role"`
	Content []mockResponseContent `json:"content"`
}

type mockResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mockResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type mockError struct {
	Error mockErrBody `json:"error"`
}

type mockErrBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}
