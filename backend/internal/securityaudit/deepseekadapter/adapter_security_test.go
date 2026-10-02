package deepseekadapter

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	securityaudit "github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/stretchr/testify/require"
)

func auditRequestBody() io.Reader {
	return strings.NewReader(`{"messages":[{"role":"user","content":"test"}]}`)
}

func TestAdapterTokenCannotBeEmptyOrReuseAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	for _, value := range []string{"", "\n", "   \n"} {
		t.Run("empty-file-"+strings.ReplaceAll(value, "\n", "newline"), func(t *testing.T) {
			dir := t.TempDir()
			apiKeyFile := filepath.Join(dir, "api-key")
			tokenFile := filepath.Join(dir, "adapter-token")
			require.NoError(t, os.WriteFile(apiKeyFile, []byte("api-key-value"), 0o600))
			require.NoError(t, os.WriteFile(tokenFile, []byte(value), 0o600))
			config := testConfig(upstream.URL)
			config.APIKey, config.AdapterToken = "", ""
			config.APIKeyFile, config.AdapterTokenFile = apiKeyFile, tokenFile
			_, _, err := NewHandler(config)
			require.ErrorContains(t, err, "load adapter token")
		})
	}

	direct := testConfig(upstream.URL)
	direct.AdapterToken = direct.APIKey
	_, _, err := NewHandler(direct)
	require.Error(t, err)
	require.NotContains(t, err.Error(), direct.APIKey)

	dir := t.TempDir()
	apiKeyFile := filepath.Join(dir, "api-key")
	tokenFile := filepath.Join(dir, "adapter-token")
	require.NoError(t, os.WriteFile(apiKeyFile, []byte("same-secret\n"), 0o600))
	require.NoError(t, os.WriteFile(tokenFile, []byte("same-secret"), 0o600))
	files := testConfig(upstream.URL)
	files.APIKey, files.AdapterToken = "", ""
	files.APIKeyFile, files.AdapterTokenFile = apiKeyFile, tokenFile
	_, _, err = NewHandler(files)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "same-secret")
}

func TestNativeTransportRejectsRedirectWithoutReplayingPrompt(t *testing.T) {
	var sinkHits atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sinkHits.Add(1)
	}))
	defer sink.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Zero(t, sinkHits.Load())
}

func TestAdapterMapsPermanentAndRetryableUpstreamFailures(t *testing.T) {
	cases := []struct {
		name           string
		upstreamStatus int
		wantStatus     int
	}{
		{"bad-request", http.StatusBadRequest, http.StatusUnprocessableEntity},
		{"unauthorized", http.StatusUnauthorized, http.StatusUnprocessableEntity},
		{"forbidden", http.StatusForbidden, http.StatusUnprocessableEntity},
		{"rate-limit", http.StatusTooManyRequests, http.StatusTooManyRequests},
		{"server-error", http.StatusInternalServerError, http.StatusBadGateway},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.upstreamStatus)
			}))
			defer upstream.Close()
			handler, cleanup, err := NewHandler(testConfig(upstream.URL))
			require.NoError(t, err)
			defer cleanup()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
			require.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}

func TestNativeResponseHeaderTimeoutIsBounded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
			writeJSON(w, http.StatusOK, map[string]any{})
		}
	}))
	defer upstream.Close()
	config := testConfig(upstream.URL)
	config.Timeout = time.Second
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()
	started := time.Now()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestNativeTransportNegotiatesHTTP1(t *testing.T) {
	var protoMajor atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoMajor.Store(int32(r.ProtoMajor))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := newHTTP1Client(3 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	transport.TLSClientConfig.RootCAs = pool
	s := &server{config: testConfig(upstream.URL), apiKey: "test-key", client: client}
	_, err := s.requestNative(context.Background(), []byte(`{"test":true}`))
	require.NoError(t, err)
	require.Equal(t, int32(1), protoMajor.Load())
}

func TestCurlTransportEndToEndAndFailures(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	t.Run("success", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer deepseek-test-key", r.Header.Get("Authorization"))
			require.Equal(t, 1, r.ProtoMajor)
			writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": `{"safety":"Safe","categories":[]}`}}}})
		}))
		defer upstream.Close()
		config := testConfig(upstream.URL)
		config.Transport = "curl"
		handler, cleanup, err := NewHandler(config)
		require.NoError(t, err)
		defer cleanup()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), `Safety: Safe\nCategories: None`)
	})

	for _, tt := range []struct{ status, want int }{
		{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		{http.StatusTooManyRequests, http.StatusTooManyRequests},
		{http.StatusServiceUnavailable, http.StatusBadGateway},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tt.status) }))
			defer upstream.Close()
			config := testConfig(upstream.URL)
			config.Transport = "curl"
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
			require.Equal(t, tt.want, recorder.Code)
		})
	}

	t.Run("timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(1500 * time.Millisecond):
			}
		}))
		defer upstream.Close()
		config := testConfig(upstream.URL)
		config.Transport = "curl"
		config.Timeout = time.Second
		handler, cleanup, err := NewHandler(config)
		require.NoError(t, err)
		defer cleanup()
		started := time.Now()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Less(t, time.Since(started), 2*time.Second)
	})

	for _, tt := range []struct {
		upstreamStatus int
		wantStatus     int
	}{
		{http.StatusOK, http.StatusUnprocessableEntity},
		{http.StatusTooManyRequests, http.StatusTooManyRequests},
		{http.StatusServiceUnavailable, http.StatusBadGateway},
	} {
		t.Run("oversized-"+http.StatusText(tt.upstreamStatus), func(t *testing.T) {
			cancelled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(cancelled)
				w.WriteHeader(tt.upstreamStatus)
				chunk := strings.Repeat("x", 32<<10)
				for {
					select {
					case <-r.Context().Done():
						return
					default:
					}
					if _, err := io.WriteString(w, chunk); err != nil {
						return
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}))
			defer upstream.Close()
			config := testConfig(upstream.URL)
			config.Transport = "curl"
			config.Timeout = 5 * time.Second
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()
			started := time.Now()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
			require.Equal(t, tt.wantStatus, recorder.Code)
			require.Less(t, time.Since(started), 2*time.Second)
			require.Eventually(t, func() bool {
				select {
				case <-cancelled:
					return true
				default:
					return false
				}
			}, time.Second, 10*time.Millisecond)
		})
	}
}

func TestCurlCredentialCleanupCallbackIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	t.Setenv("TMPDIR", t.TempDir())
	config := testConfig("http://127.0.0.1:1")
	config.Transport = "curl"
	_, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(os.TempDir(), "sub2api-deepseek-curl-*.conf"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	cleanup()
	cleanup()
	files, err = filepath.Glob(filepath.Join(os.TempDir(), "sub2api-deepseek-curl-*.conf"))
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestTransportBodyTimeoutAndCallerCancellation(t *testing.T) {
	transports := []string{"native"}
	if _, err := exec.LookPath("curl"); err == nil {
		transports = append(transports, "curl")
	}
	for _, transportName := range transports {
		t.Run(transportName+"-body-timeout", func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"choices":[`)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(1500 * time.Millisecond):
				}
			}))
			defer upstream.Close()
			config := testConfig(upstream.URL)
			config.Transport = transportName
			config.Timeout = time.Second
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()
			started := time.Now()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()))
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Equal(t, "upstream classification unavailable\n", recorder.Body.String())
			require.Less(t, time.Since(started), 2*time.Second)
		})

		t.Run(transportName+"-caller-cancel", func(t *testing.T) {
			startedUpstream := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(startedUpstream)
				select {
				case <-r.Context().Done():
				case <-time.After(1500 * time.Millisecond):
				}
			}))
			defer upstream.Close()
			config := testConfig(upstream.URL)
			config.Transport = transportName
			config.Timeout = 5 * time.Second
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			request := testRequest(http.MethodPost, "/v1/chat/completions", auditRequestBody()).WithContext(ctx)
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				handler.ServeHTTP(recorder, request)
				close(done)
			}()
			require.Eventually(t, func() bool {
				select {
				case <-startedUpstream:
					return true
				default:
					return false
				}
			}, time.Second, 10*time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("adapter did not stop after caller cancellation")
			}
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Equal(t, "upstream classification unavailable\n", recorder.Body.String())
		})
	}
}

func TestAdapterContractDrivesRealScannerAndRetryability(t *testing.T) {
	newStack := func(t *testing.T, upstreamHandler http.HandlerFunc) (*securityaudit.OpenAICompatibleScanner, securityaudit.ActiveEndpoint, func()) {
		t.Helper()
		upstream := httptest.NewServer(upstreamHandler)
		config := testConfig(upstream.URL)
		handler, cleanupHandler, err := NewHandler(config)
		require.NoError(t, err)
		adapter := httptest.NewServer(handler)
		cleanup := func() { adapter.Close(); cleanupHandler(); upstream.Close() }
		endpoint := securityaudit.ActiveEndpoint{ID: "deepseek-stack", BaseURL: adapter.URL, Model: defaultModel, Token: "adapter-test-token", TimeoutMS: 3000, InputLimit: 4000, Enabled: true}
		return securityaudit.NewOpenAICompatibleScanner(), endpoint, cleanup
	}

	t.Run("safe-and-unsafe", func(t *testing.T) {
		scanner, endpoint, cleanup := newStack(t, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			content := `{"safety":"Safe","categories":[]}`
			if strings.Contains(request.Messages[len(request.Messages)-1].Content, "JAILBREAK_MARK") {
				content = `{"safety":"Unsafe","categories":["Jailbreak"]}`
			}
			writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}})
		})
		defer cleanup()
		safe, err := scanner.Scan(context.Background(), endpoint, "benign", securityaudit.AllScannerIDs)
		require.NoError(t, err)
		require.Equal(t, securityaudit.EventPass, safe.Decision)
		require.Equal(t, securityaudit.ActionAllow, safe.Action)
		risk, err := scanner.Scan(context.Background(), endpoint, "JAILBREAK_MARK", securityaudit.AllScannerIDs)
		require.NoError(t, err)
		require.Equal(t, securityaudit.EventCritical, risk.Decision)
		require.Equal(t, securityaudit.ActionBlock, risk.Action)
		require.Contains(t, risk.Categories, "jailbreak")
	})

	for _, tt := range []struct {
		upstreamStatus int
		retryable      bool
	}{
		{http.StatusUnauthorized, false},
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
	} {
		t.Run("status-"+http.StatusText(tt.upstreamStatus), func(t *testing.T) {
			scanner, endpoint, cleanup := newStack(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tt.upstreamStatus) })
			defer cleanup()
			_, err := scanner.Scan(context.Background(), endpoint, "test", securityaudit.AllScannerIDs)
			var guardErr *securityaudit.GuardError
			require.ErrorAs(t, err, &guardErr)
			require.Equal(t, tt.retryable, guardErr.Retryable)
		})
	}
}
