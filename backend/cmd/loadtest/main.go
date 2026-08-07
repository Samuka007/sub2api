// Command loadtest is a standalone high-concurrency harness for the sub2api
// gateway. It starts a mock LLM upstream and requires a marker preflight before
// it sends concurrent traffic. Operators must also isolate the target API key in
// a group that contains only the mock account. Results are synthetic and support
// relative performance comparison and capacity probing, not real upstream sizing.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Default flag values are intentionally CI-safe (modest concurrency/duration) while
// still demonstrating the harness; production tuning is documented in the report.
const (
	defaultConcurrency = 50
	defaultDuration    = 5 * time.Second
	defaultRPS         = 0 // 0 = unthrottled, drive pure concurrency
	defaultBody        = `{"model":"loadtest-mock","messages":[{"role":"user","content":"ping"}],"stream":false}`
	mockUpstreamPath   = "/v1/chat/completions"
	mockResponsesPath  = "/v1/responses"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "loadtest: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	targetURL := fs.String("target", "", "Gateway URL to load test (required, e.g. http://127.0.0.1:8080/v1/chat/completions)")
	concurrency := fs.Int("concurrency", defaultConcurrency, "Number of concurrent workers driving requests")
	duration := fs.Duration("duration", defaultDuration, "Duration of the load test (e.g. 30s, 2m)")
	rps := fs.Int("rps", defaultRPS, "Target requests per second limit (0 = unthrottled, drive pure concurrency)")
	method := fs.String("method", "POST", "HTTP method to use against the target")
	body := fs.String("body", defaultBody, "Request body sent to the target gateway")
	headersRaw := fs.String("headers", "", `Headers to send, as "Key: Value" pairs separated by newlines or semicolons`)
	mockAddr := fs.String("mock-upstream-addr", "127.0.0.1:0", "Address for the in-process mock LLM upstream (host:port); :0 picks a free port")
	mockGain := fs.Duration("mock-latency", 0, "Artificial latency the mock upstream adds to each response")
	mockFailRate := fs.Float64("mock-fail-rate", 0, "Fraction [0,1] of mock upstream responses that return HTTP 500 (canned errors)")
	timeout := fs.Duration("http-timeout", 10*time.Second, "Per-request HTTP client timeout")
	printVersion := fs.Bool("version", false, "Print version information and exit")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *printVersion {
		fmt.Fprintf(stdout, "loadtest %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}

	if *targetURL == "" {
		return fmt.Errorf("-target is required (provide the gateway URL to load test)")
	}
	if *concurrency < 1 {
		return fmt.Errorf("-concurrency must be >= 1, got %d", *concurrency)
	}
	if *duration <= 0 {
		return fmt.Errorf("-duration must be positive, got %s", *duration)
	}
	if *rps < 0 {
		return fmt.Errorf("-rps must be >= 0, got %d", *rps)
	}
	if *mockFailRate < 0 || *mockFailRate > 1 {
		return fmt.Errorf("-mock-fail-rate must be in [0,1], got %f", *mockFailRate)
	}
	if *timeout <= 0 {
		return fmt.Errorf("-http-timeout must be positive, got %s", *timeout)
	}

	headers, err := parseHeaders(*headersRaw)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	marker, err := newMockResponseMarker()
	if err != nil {
		return err
	}

	mock, err := startMockUpstream(ctx, mockUpstreamConfig{
		addr:           *mockAddr,
		latency:        *mockGain,
		fail:           0,
		responseMarker: marker,
	})
	if err != nil {
		return fmt.Errorf("start mock upstream: %w", err)
	}
	defer func() {
		_ = mock.Close()
	}()

	fmt.Fprintf(stdout, "loadtest: mock LLM upstream listening on %s; verifying the gateway route before load\n", mock.Addr())

	cfg := loadTestConfig{
		targetURL:   *targetURL,
		method:      *method,
		body:        *body,
		headers:     headers,
		concurrency: *concurrency,
		duration:    *duration,
		targetRPS:   *rps,
		httpTimeout: *timeout,
	}

	preflightClient := &http.Client{Timeout: cfg.httpTimeout}
	if err := verifyMockRoute(ctx, preflightClient, cfg, marker, mock); err != nil {
		return fmt.Errorf("verify gateway mock route: %w", err)
	}
	mock.SetFailRate(*mockFailRate)

	report, err := runLoadTest(ctx, cfg, stdout)
	if err != nil {
		return err
	}
	printReport(stdout, report)
	return nil
}

// ldflags-injected build metadata (kept simple; defaults are benign).
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)
