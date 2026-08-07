//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func TestRunRejectsTargetThatDoesNotUseMockUpstream(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"unexpected-real-upstream"}`)
	}))
	t.Cleanup(target.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-target", target.URL,
		"-concurrency", "1",
		"-duration", "10ms",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "in-process mock") {
		t.Fatalf("expected mock route verification error, got %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected exactly one preflight request and no load, got %d", got)
	}
}

func TestRunRejectsNonPositiveHTTPTimeout(t *testing.T) {
	for _, timeout := range []string{"0", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			err := run([]string{
				"-target", "http://127.0.0.1:1/v1/chat/completions",
				"-http-timeout", timeout,
			}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "-http-timeout must be positive") {
				t.Fatalf("expected positive timeout validation error, got %v", err)
			}
		})
	}
}

func TestVerifyMockRouteRejectsForgedMarkerWithoutMockCall(t *testing.T) {
	mock, err := startMockUpstream(context.Background(), mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+mockResponseMarker+`"}`)
	}))
	t.Cleanup(target.Close)

	cfg := loadTestConfig{
		targetURL: target.URL,
		method:    http.MethodPost,
		body:      defaultBody,
	}
	err = verifyMockRoute(context.Background(), target.Client(), cfg, mockResponseMarker, mock)
	if err == nil {
		t.Fatal("expected forged marker to fail without a request reaching the in-process mock")
	}
}

func TestVerifyMockRouteRejectsDirectMockTarget(t *testing.T) {
	mock, err := startMockUpstream(context.Background(), mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	cfg := loadTestConfig{
		targetURL: "http://" + mock.Addr() + mockUpstreamPath,
		method:    http.MethodPost,
		body:      defaultBody,
	}
	err = verifyMockRoute(context.Background(), &http.Client{Timeout: time.Second}, cfg, mockResponseMarker, mock)
	if err == nil {
		t.Fatal("expected direct mock target to be rejected because it bypasses the gateway")
	}
}

func TestRunVerifiesMockRouteAndPrintsReport(t *testing.T) {
	mockAddr := reserveTestAddress(t)
	target := startTestGatewayProxy(t, mockAddr)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-target", target.URL + mockUpstreamPath,
		"-mock-upstream-addr", mockAddr,
		"-concurrency", "4",
		"-duration", "30ms",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "loadtest report") || !strings.Contains(stdout.String(), "Successful (2xx)") {
		t.Fatalf("expected loadtest report, got:\n%s", stdout.String())
	}
}

func TestRunAppliesMockFailRateOnlyAfterPreflight(t *testing.T) {
	mockAddr := reserveTestAddress(t)
	target := startTestGatewayProxy(t, mockAddr)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-target", target.URL + mockUpstreamPath,
		"-mock-upstream-addr", mockAddr,
		"-mock-fail-rate", "1",
		"-concurrency", "16",
		"-duration", "30ms",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Successful (2xx)  : 0") ||
		!strings.Contains(stdout.String(), "Non-2xx responses : ") ||
		!strings.Contains(stdout.String(), "  500 : ") {
		t.Fatalf("expected preflight success followed by injected 500 load, got:\n%s", stdout.String())
	}
}

func reserveTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve mock address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release mock address: %v", err)
	}
	return addr
}

func startTestGatewayProxy(t *testing.T, upstreamAddr string) *httptest.Server {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+upstreamAddr+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		upstreamReq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(upstreamReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(target.Close)
	return target
}

func TestStartMockUpstreamServesOpenAICompatibleRoutes(t *testing.T) {
	mock, err := startMockUpstream(context.Background(), mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	for _, tc := range []struct {
		path   string
		object string
	}{
		{path: mockUpstreamPath, object: `"object":"chat.completion"`},
		{path: mockResponsesPath, object: `"object":"response"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Post("http://"+mock.Addr()+tc.path, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("read response: %v", readErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(mockResponseMarker)) ||
				!bytes.Contains(body, []byte("pong")) || !bytes.Contains(body, []byte(tc.object)) {
				t.Fatalf("unexpected canned body: %s", body)
			}
		})
	}
	if got := mock.Calls(); got != 2 {
		t.Fatalf("expected one request per OpenAI route, got %d", got)
	}
}

func TestMockResponsesRouteStreamsTerminalEventWhenRequested(t *testing.T) {
	mock, err := startMockUpstream(context.Background(), mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	resp, err := http.Post(
		"http://"+mock.Addr()+mockResponsesPath,
		"application/json",
		strings.NewReader(`{"stream":true}`),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", contentType)
	}
	if !bytes.Contains(body, []byte(`"type":"response.completed"`)) ||
		!bytes.Contains(body, []byte(mockResponseMarker)) || !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Fatalf("unexpected SSE body: %s", body)
	}
	var event struct {
		Type     string                      `json:"type"`
		Response apicompat.ResponsesResponse `json:"response"`
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "data: {") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("decode terminal event: %v", err)
			}
			break
		}
	}
	if event.Type != "response.completed" {
		t.Fatalf("expected response.completed event, got %q", event.Type)
	}
	chatBody, err := json.Marshal(apicompat.ResponsesToChatCompletions(&event.Response, "loadtest-mock"))
	if err != nil {
		t.Fatalf("marshal converted chat response: %v", err)
	}
	if !bytes.Contains(chatBody, []byte(mockResponseMarker)) {
		t.Fatalf("expected unique marker to survive gateway conversion, got %s", chatBody)
	}
}

func TestMockUpstreamFailRate(t *testing.T) {
	ctx := context.Background()
	mock, err := startMockUpstream(ctx, mockUpstreamConfig{
		addr: "127.0.0.1:0",
		fail: 1, // always fail
	})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	url := "http://" + mock.Addr() + mockUpstreamPath
	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 with fail=1, got %d", resp.StatusCode)
	}
}

func TestMockUpstreamFailRateCanChangeDuringRequests(t *testing.T) {
	mock, err := startMockUpstream(context.Background(), mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	stopUpdates := make(chan struct{})
	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		for i := 0; ; i++ {
			select {
			case <-stopUpdates:
				return
			default:
				mock.SetFailRate(float64(i % 2))
			}
		}
	}()

	errs := make(chan error, 128)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 16 {
				resp, err := http.Post("http://"+mock.Addr()+mockUpstreamPath, "application/json", strings.NewReader(`{}`))
				if err != nil {
					errs <- err
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusInternalServerError {
					errs <- fmt.Errorf("unexpected status %d", resp.StatusCode)
				}
			}
		}()
	}
	wg.Wait()
	close(stopUpdates)
	<-updatesDone
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestLatencyHistogramAggregatesKnownDistributionAcross5000Workers(t *testing.T) {
	const workers = 5000
	histogram := newLatencyHistogram(workers)
	if got := len(histogram.shards); got != maxLatencyHistShards {
		t.Fatalf("expected %d bounded shards, got %d", maxLatencyHistShards, got)
	}

	var wg sync.WaitGroup
	for workerID := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			histogram.Record(workerID, time.Duration(workerID+1)*time.Microsecond)
		}()
	}
	wg.Wait()

	report := buildHistogramReport(
		loadTestConfig{concurrency: workers},
		histogram,
		map[int]int64{http.StatusOK: workers},
		map[string]int64{},
		workers*100,
		workers,
		workers,
		0,
		0,
		time.Second,
	)
	if report.TotalRequests != workers || report.SuccessCount != workers || report.SuccessRatio != 1 {
		t.Fatalf("unexpected aggregate counts: %+v", report)
	}
	if report.LatencyMin != time.Microsecond || report.LatencyMax != workers*time.Microsecond {
		t.Fatalf("unexpected exact bounds: min=%s max=%s", report.LatencyMin, report.LatencyMax)
	}
	for name, tc := range map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		"p50": {got: report.LatencyP50, want: 2500 * time.Microsecond},
		"p95": {got: report.LatencyP95, want: 4750 * time.Microsecond},
		"p99": {got: report.LatencyP99, want: 4950 * time.Microsecond},
	} {
		if tc.got < tc.want || tc.got > tc.want+tc.want*7/100 {
			t.Errorf("%s bucket bound %s outside [%s,%s]", name, tc.got, tc.want, tc.want+tc.want*7/100)
		}
	}
}

func BenchmarkLatencyHistogramRecordWith5000WorkerShards(b *testing.B) {
	histogram := newLatencyHistogram(5000)
	var nextWorker atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		workerID := int(nextWorker.Add(1)-1) % 5000
		for pb.Next() {
			histogram.Record(workerID, time.Millisecond)
		}
	})
}

func TestRunLoadTestAgainstMock(t *testing.T) {
	ctx := context.Background()
	mock, err := startMockUpstream(ctx, mockUpstreamConfig{addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	cfg := loadTestConfig{
		targetURL:   "http://" + mock.Addr() + mockUpstreamPath,
		method:      "POST",
		body:        defaultBody,
		concurrency: 5,
		duration:    200 * time.Millisecond,
		targetRPS:   0,
		httpTimeout: 2 * time.Second,
	}

	var prog bytes.Buffer
	report, err := runLoadTest(ctx, cfg, &prog)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}

	if report.TotalRequests == 0 {
		t.Fatal("expected at least one request")
	}
	// A tiny number of in-flight requests are cancelled when the run deadline
	// fires and are counted as transport errors; require overwhelming success.
	if report.SuccessCount <= 0 {
		t.Fatalf("expected successful requests, got success=%d total=%d", report.SuccessCount, report.TotalRequests)
	}
	if report.ErrorCount > 0 && report.SuccessRatio < 0.99 {
		t.Fatalf("expected >=99%% success under mock, got ratio=%v (success=%d total=%d)",
			report.SuccessRatio, report.SuccessCount, report.TotalRequests)
	}
	if report.LatencyMax <= 0 {
		t.Fatalf("expected non-zero latency, got %v", report.LatencyMax)
	}
	if report.StatusHistogram[http.StatusOK] == 0 {
		t.Fatal("expected 200 in status histogram")
	}
}

func TestRunLoadTestMeasuresFullResponseBodyLatency(t *testing.T) {
	const bodyDelay = 75 * time.Millisecond
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(bodyDelay)
		_, _ = io.WriteString(w, `{"id":"mock-loadtest"}`)
	}))
	t.Cleanup(target.Close)

	report, err := runLoadTest(context.Background(), loadTestConfig{
		targetURL:   target.URL,
		method:      http.MethodPost,
		body:        defaultBody,
		concurrency: 1,
		duration:    100 * time.Millisecond,
		httpTimeout: time.Second,
	}, io.Discard)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}
	if report.LatencyMax < bodyDelay {
		t.Fatalf("expected full-body latency >= %s, got %s", bodyDelay, report.LatencyMax)
	}
}

func TestRunLoadTestCountsTruncatedBodiesAsErrors(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "128")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"mock-loadtest"}`)
	}))
	t.Cleanup(target.Close)

	report, err := runLoadTest(context.Background(), loadTestConfig{
		targetURL:   target.URL,
		method:      http.MethodPost,
		body:        defaultBody,
		concurrency: 1,
		duration:    30 * time.Millisecond,
		httpTimeout: time.Second,
	}, io.Discard)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}
	if report.SuccessCount != 0 || report.ErrorCount == 0 {
		t.Fatalf("expected truncated bodies to be errors, got success=%d errors=%d", report.SuccessCount, report.ErrorCount)
	}
	if report.ErrorHistogram["eof"] == 0 {
		t.Fatalf("expected eof error histogram entry, got %+v", report.ErrorHistogram)
	}
}

func TestRunLoadTestLowRateStartsImmediatelyWithoutBurst(t *testing.T) {
	var mu sync.Mutex
	requestStarts := make([]time.Time, 0, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestStarts = append(requestStarts, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"mock-loadtest"}`)
	}))
	t.Cleanup(target.Close)

	runStarted := time.Now()
	report, err := runLoadTest(context.Background(), loadTestConfig{
		targetURL:   target.URL,
		method:      http.MethodPost,
		body:        defaultBody,
		concurrency: 100,
		duration:    350 * time.Millisecond,
		targetRPS:   10,
		httpTimeout: time.Second,
	}, io.Discard)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}
	mu.Lock()
	starts := append([]time.Time(nil), requestStarts...)
	mu.Unlock()
	if report.TotalRequests < 3 || report.TotalRequests > 4 || int64(len(starts)) != report.TotalRequests {
		t.Fatalf("expected 3-4 evenly paced requests at 10 RPS, got report=%d starts=%d", report.TotalRequests, len(starts))
	}
	if delay := starts[0].Sub(runStarted); delay >= 80*time.Millisecond {
		t.Fatalf("expected first request immediately, started after %s", delay)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 50*time.Millisecond {
			t.Fatalf("requests %d and %d burst within %s; starts=%v", i-1, i, gap, starts)
		}
	}
}

func TestRunLoadTestDrainsInFlightRequestsAtDurationBoundary(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"mock-loadtest"}`)
	}))
	t.Cleanup(target.Close)

	report, err := runLoadTest(context.Background(), loadTestConfig{
		targetURL:   target.URL,
		method:      http.MethodPost,
		body:        defaultBody,
		concurrency: 20,
		duration:    100 * time.Millisecond,
		httpTimeout: time.Second,
	}, io.Discard)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}
	if report.SuccessCount != 20 || report.ErrorCount != 0 {
		t.Fatalf("expected 20 drained successes and no synthetic cancellation errors, got success=%d errors=%d", report.SuccessCount, report.ErrorCount)
	}
}

func TestRunLoadTestBoundsInFlightDrainByHTTPTimeout(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(target.Close)

	started := time.Now()
	report, err := runLoadTest(context.Background(), loadTestConfig{
		targetURL:   target.URL,
		method:      http.MethodPost,
		body:        defaultBody,
		concurrency: 1,
		duration:    10 * time.Millisecond,
		httpTimeout: 75 * time.Millisecond,
	}, io.Discard)
	if err != nil {
		t.Fatalf("runLoadTest: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("expected bounded drain near request timeout, took %s", elapsed)
	}
	if report.SuccessCount != 0 || report.ErrorHistogram["timeout"] != 1 {
		t.Fatalf("expected one bounded timeout, got success=%d errors=%+v", report.SuccessCount, report.ErrorHistogram)
	}
}

func TestParseHeaders(t *testing.T) {
	h, err := parseHeaders("Authorization: Bearer x;X-Trace: 1\n")
	if err != nil {
		t.Fatalf("parseHeaders: %v", err)
	}
	if len(h) != 2 || h[0].key != "Authorization" || h[0].value != "Bearer x" {
		t.Fatalf("unexpected headers: %+v", h)
	}
	if _, err := parseHeaders("badheader"); err == nil {
		t.Fatal("expected error for header without colon")
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[string]string{
		"dial tcp: connection refused":   "connection_refused",
		"context deadline exceeded":      "timeout",
		"read: connection reset by peer": "connection_reset",
		"unexpected EOF":                 "eof",
		"some other":                     "transport_error",
	}
	for msg, want := range cases {
		if got := classifyError(errString(msg)); got != want {
			t.Fatalf("classifyError(%q)=%q want %q", msg, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
