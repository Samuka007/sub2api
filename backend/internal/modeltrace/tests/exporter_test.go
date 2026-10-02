package modeltrace_test

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"go.opentelemetry.io/otel"
)

func TestManagerIdenticalSnapshotIsNoOpBeforeExporterBuild(t *testing.T) {
	// slog.SetDefault is process-global, so this test must not run in parallel.
	cfg := config.ModelTracingConfig{
		Enabled:   true,
		Endpoint:  "https://langfuse.example.test",
		PublicKey: "public",
		SecretKey: "secret",
	}
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	managerClosed := false
	defer func() {
		if managerClosed {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown manager: %v", err)
		}
	}()
	if err := manager.ApplySnapshot(context.Background(), modeltrace.ConfigSnapshot{
		Config: cfg, Source: modeltrace.ConfigSourceRuntime, ConfigVersion: 1,
	}); err != nil {
		t.Fatalf("apply initial runtime snapshot: %v", err)
	}

	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	loggerRestored := false
	defer func() {
		if !loggerRestored {
			slog.SetDefault(previousLogger)
		}
	}()

	if err := manager.ApplySnapshot(context.Background(), modeltrace.ConfigSnapshot{
		Config: cfg, Source: modeltrace.ConfigSourceRuntime, ConfigVersion: 1,
	}); err != nil {
		t.Fatalf("reapply identical runtime snapshot: %v", err)
	}
	if strings.Contains(logs.String(), "result=shutdown") {
		t.Fatalf("identical snapshot built and closed an unpublished exporter generation:\n%s", logs.String())
	}

	slog.SetDefault(previousLogger)
	loggerRestored = true
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown manager: %v", err)
	}
	managerClosed = true
}

func TestManagerFailedSnapshotBuildKeepsActiveGeneration(t *testing.T) {
	cfg := config.ModelTracingConfig{
		Enabled:   true,
		Endpoint:  "https://langfuse.example.test",
		PublicKey: "public",
		SecretKey: "secret",
	}
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	managerClosed := false
	defer func() {
		if managerClosed {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown manager: %v", err)
		}
	}()
	if err := manager.ApplySnapshot(context.Background(), modeltrace.ConfigSnapshot{
		Config: cfg, Source: modeltrace.ConfigSourceRuntime, ConfigVersion: 1,
	}); err != nil {
		t.Fatalf("apply initial runtime snapshot: %v", err)
	}

	before := manager.Acquire()
	beforeFingerprint := before.Fingerprint()
	beforeConfig := before.Config()
	before.Release()

	err = manager.ApplySnapshot(context.Background(), modeltrace.ConfigSnapshot{
		Config: config.ModelTracingConfig{
			Enabled: true, Destination: "unsupported", Endpoint: "https://collector.example.test",
		},
		Source: modeltrace.ConfigSourceRuntime, ConfigVersion: 2,
	})
	if err == nil {
		t.Fatal("apply invalid runtime snapshot succeeded")
	}

	after := manager.Acquire()
	if after.Fingerprint() != beforeFingerprint || after.Source() != modeltrace.ConfigSourceRuntime || after.Version() != 1 {
		t.Fatalf("failed build changed active generation: fingerprint=%q source=%q version=%d", after.Fingerprint(), after.Source(), after.Version())
	}
	if !reflect.DeepEqual(after.Config(), beforeConfig) {
		t.Fatalf("failed build changed active config:\n got: %#v\nwant: %#v", after.Config(), beforeConfig)
	}
	after.Release()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown manager: %v", err)
	}
	managerClosed = true
}

func TestModelTraceEndpointTransport(t *testing.T) {
	t.Run("accepts complete HTTP or HTTPS endpoints", func(t *testing.T) {
		tests := []struct {
			name      string
			endpoint  string
			wantError bool
		}{
			{name: "remote HTTPS", endpoint: "https://langfuse.example.com/api/public/otel/v1/traces"},
			{name: "localhost HTTP", endpoint: "http://localhost:3000/custom/traces"},
			{name: "IPv4 loopback HTTP", endpoint: "http://127.0.0.1:3000/custom/traces"},
			{name: "IPv6 loopback HTTP", endpoint: "http://[::1]:3000/custom/traces"},
			{name: "remote domain HTTP", endpoint: "http://collector.example.com:4318/v1/traces"},
			{name: "remote IP HTTP", endpoint: "http://192.0.2.10:4318/custom/traces"},
			{name: "userinfo", endpoint: "https://user:credential-canary@langfuse.example.com/api/public/otel", wantError: true},
			{name: "query", endpoint: "https://langfuse.example.com/api/public/otel?token=query-canary", wantError: true},
			{name: "fragment", endpoint: "https://langfuse.example.com/api/public/otel#fragment-canary", wantError: true},
			{name: "unsupported scheme", endpoint: "ftp://localhost/traces", wantError: true},
			{name: "empty endpoint", endpoint: "", wantError: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := config.ValidateModelTracingEndpoint(tt.endpoint)
				if tt.wantError && err == nil {
					t.Fatalf("config.ValidateModelTracingEndpoint(%q) succeeded, want rejection", tt.endpoint)
				}
				if !tt.wantError && err != nil {
					t.Fatalf("config.ValidateModelTracingEndpoint(%q) rejected a safe transport: %v", tt.endpoint, err)
				}
			})
		}
	})

	t.Run("exports to the configured HTTP path without rewriting it", func(t *testing.T) {
		const configuredPath = "/custom/collector/traces/"
		paths := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths <- r.URL.Path
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
			Enabled:   true,
			Endpoint:  server.URL + configuredPath,
			PublicKey: "public",
			SecretKey: "secret",
		})
		if err != nil {
			t.Fatalf("modeltrace.NewManager rejected HTTP endpoint: %v", err)
		}
		_, span := manager.Tracer().Start(context.Background(), "exact-endpoint")
		span.End()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown exporter: %v", err)
		}

		select {
		case got := <-paths:
			if got != configuredPath {
				t.Fatalf("export path = %q, want configured path %q", got, configuredPath)
			}
		default:
			t.Fatal("configured HTTP endpoint received no OTLP request")
		}
	})

	t.Run("Langfuse base endpoint uses the standard trace path", func(t *testing.T) {
		paths := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths <- r.URL.Path
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
			Enabled: true, Endpoint: server.URL, PublicKey: "public", SecretKey: "secret",
		})
		if err != nil {
			t.Fatalf("modeltrace.NewManager rejected Langfuse base endpoint: %v", err)
		}
		_, span := manager.Tracer().Start(context.Background(), "langfuse-default-path")
		span.End()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown exporter: %v", err)
		}

		select {
		case got := <-paths:
			if got != "/api/public/otel/v1/traces" {
				t.Fatalf("Langfuse export path = %q, want standard path", got)
			}
		default:
			t.Fatal("Langfuse endpoint received no OTLP request")
		}
	})

	t.Run("collector exports without Langfuse authentication headers", func(t *testing.T) {
		type receivedRequest struct {
			path          string
			authorization string
			ingestion     string
		}
		requests := make(chan receivedRequest, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- receivedRequest{
				path:          r.URL.Path,
				authorization: r.Header.Get("Authorization"),
				ingestion:     r.Header.Get("x-langfuse-ingestion-version"),
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
			Enabled:     true,
			Destination: config.ModelTracingDestinationOTLPCollector,
			Endpoint:    server.URL,
		})
		if err != nil {
			t.Fatalf("modeltrace.NewManager rejected collector without Langfuse credentials: %v", err)
		}
		_, span := manager.Tracer().Start(context.Background(), "collector-no-auth")
		span.End()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown exporter: %v", err)
		}

		select {
		case got := <-requests:
			if got.path != "/v1/traces" {
				t.Fatalf("collector export path = %q, want standard path", got.path)
			}
			if got.authorization != "" || got.ingestion != "" {
				t.Fatalf("collector received Langfuse headers: Authorization=%q ingestion=%q", got.authorization, got.ingestion)
			}
		default:
			t.Fatal("collector received no OTLP request")
		}
	})

	t.Run("HTTPS keeps standard certificate verification", func(t *testing.T) {
		previousErrorHandler := otel.GetErrorHandler()
		exportErrors := make(chan error, 1)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			select {
			case exportErrors <- err:
			default:
			}
		}))
		defer otel.SetErrorHandler(previousErrorHandler)
		var requests atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
			Enabled:   true,
			Endpoint:  server.URL,
			PublicKey: "public",
			SecretKey: "secret",
		})
		if err != nil {
			t.Fatalf("modeltrace.NewManager rejected syntactically valid HTTPS endpoint: %v", err)
		}
		_, span := manager.Tracer().Start(context.Background(), "certificate-check")
		span.End()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
		select {
		case err := <-exportErrors:
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "certificate") && !strings.Contains(message, "x509") {
				t.Fatalf("HTTPS export failed for an unexpected reason: %v", err)
			}
		default:
			t.Fatal("export through an untrusted TLS certificate reported no verification failure")
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("untrusted TLS server received %d HTTP requests, want 0", got)
		}
	})

	t.Run("configuration exposes no certificate verification bypass", func(t *testing.T) {
		configType := reflect.TypeOf(config.ModelTracingConfig{})
		for i := 0; i < configType.NumField(); i++ {
			field := configType.Field(i)
			surface := strings.ToLower(field.Name + " " + field.Tag.Get("mapstructure") + " " + field.Tag.Get("json") + " " + field.Tag.Get("yaml"))
			if strings.Contains(surface, "skip") && strings.Contains(surface, "verify") {
				t.Fatalf("ModelTracingConfig exposes certificate verification bypass through %s", field.Name)
			}
		}
	})
}

func TestManagerConcurrentShutdownWaitsForEveryPublishedGeneration(t *testing.T) {
	initialClosed := make(chan struct{})
	nextClosed := make(chan struct{})
	initial := modeltrace.TestingNewGeneration(modeltrace.TestingGenerationConfig{
		Source: modeltrace.ConfigSourceDeployment, Fingerprint: "initial",
		Shutdown: func(context.Context) error {
			close(initialClosed)
			return nil
		},
	})
	manager := modeltrace.TestingNewManagerWithActive(initial)
	initialHeld := manager.Acquire()
	next := modeltrace.TestingNewGeneration(modeltrace.TestingGenerationConfig{
		Source: modeltrace.ConfigSourceRuntime, Version: 1, Fingerprint: "next",
		Shutdown: func(context.Context) error {
			close(nextClosed)
			return nil
		},
	})
	if err := manager.TestingInstallGeneration(next); err != nil {
		t.Fatalf("install generation: %v", err)
	}
	nextHeld := manager.Acquire()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- manager.Shutdown(shutdownCtx) }()
	go func() { results <- manager.Shutdown(shutdownCtx) }()

	select {
	case err := <-results:
		t.Fatalf("Shutdown returned while both generations were retained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	nextHeld.Release()
	select {
	case <-nextClosed:
	case <-time.After(time.Second):
		t.Fatal("active generation was not shut down after release")
	}
	select {
	case err := <-results:
		t.Fatalf("Shutdown returned before the retired generation was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	initialHeld.Release()
	select {
	case <-initialClosed:
	case <-time.After(time.Second):
		t.Fatal("retired generation was not shut down after release")
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Shutdown returned an error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Shutdown did not wait for all generations")
		}
	}
}
