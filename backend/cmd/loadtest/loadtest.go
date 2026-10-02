package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// loadTestConfig configures a single load-test run.
type loadTestConfig struct {
	targetURL   string
	method      string
	body        string
	headers     []header
	concurrency int
	duration    time.Duration
	targetRPS   int
	httpTimeout time.Duration
}

type header struct {
	key   string
	value string
}

// loadTestReport captures the aggregated result of a run.
type loadTestReport struct {
	Concurrency     int
	Duration        time.Duration
	TargetRPS       int
	TotalRequests   int64
	SuccessCount    int64
	ErrorCount      int64
	AchievedRPS     float64
	SuccessRatio    float64
	LatencyMin      time.Duration
	LatencyP50      time.Duration
	LatencyP95      time.Duration
	LatencyP99      time.Duration
	LatencyMax      time.Duration
	StatusHistogram map[int]int64
	ErrorHistogram  map[string]int64
	BytesReceived   int64
	Non2xxCount     int64
}

const (
	latencySubBuckets    = 16
	latencyBucketCount   = 64 * latencySubBuckets
	maxLatencyHistShards = 64
)

type latencyHistogramShard struct {
	buckets [latencyBucketCount]atomic.Uint64
}

type latencyHistogram struct {
	shards []latencyHistogramShard
	min    atomic.Int64
	max    atomic.Int64
}

func newLatencyHistogram(concurrency int) *latencyHistogram {
	shardCount := concurrency
	if shardCount > maxLatencyHistShards {
		shardCount = maxLatencyHistShards
	}
	return &latencyHistogram{shards: make([]latencyHistogramShard, shardCount)}
}

func (h *latencyHistogram) Record(workerID int, latency time.Duration) {
	nanos := latency.Nanoseconds()
	if nanos < 1 {
		nanos = 1
	}
	updateAtomicMin(&h.min, nanos)
	updateAtomicMax(&h.max, nanos)
	bucket := latencyBucketIndex(uint64(nanos))
	h.shards[workerID%len(h.shards)].buckets[bucket].Add(1)
}

func (h *latencyHistogram) Percentile(p float64) time.Duration {
	var total uint64
	for bucket := range latencyBucketCount {
		for shard := range h.shards {
			total += h.shards[shard].buckets[bucket].Load()
		}
	}
	if total == 0 {
		return 0
	}
	rank := uint64(math.Ceil(p * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for bucket := range latencyBucketCount {
		for shard := range h.shards {
			seen += h.shards[shard].buckets[bucket].Load()
		}
		if seen >= rank {
			return latencyBucketUpperBound(bucket)
		}
	}
	return time.Duration(h.max.Load())
}

func (h *latencyHistogram) Min() time.Duration { return time.Duration(h.min.Load()) }
func (h *latencyHistogram) Max() time.Duration { return time.Duration(h.max.Load()) }

func latencyBucketIndex(nanos uint64) int {
	exponent := bits.Len64(nanos) - 1
	base := uint64(1) << exponent
	offset := nanos - base
	var subBucket int
	if exponent >= 4 {
		subBucket = int(offset >> (exponent - 4))
	} else {
		subBucket = int(offset << (4 - exponent))
	}
	if subBucket >= latencySubBuckets {
		subBucket = latencySubBuckets - 1
	}
	return exponent*latencySubBuckets + subBucket
}

func latencyBucketUpperBound(bucket int) time.Duration {
	exponent := bucket / latencySubBuckets
	subBucket := bucket % latencySubBuckets
	base := float64(uint64(1) << exponent)
	upper := base * (1 + float64(subBucket+1)/latencySubBuckets)
	const maxDuration = time.Duration(1<<63 - 1)
	if upper >= float64(maxDuration) {
		return maxDuration
	}
	return time.Duration(upper)
}

func updateAtomicMin(value *atomic.Int64, candidate int64) {
	for {
		current := value.Load()
		if current != 0 && current <= candidate {
			return
		}
		if value.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func updateAtomicMax(value *atomic.Int64, candidate int64) {
	for {
		current := value.Load()
		if current >= candidate {
			return
		}
		if value.CompareAndSwap(current, candidate) {
			return
		}
	}
}

type workerLoadStats struct {
	total        int64
	success      int64
	errors       int64
	non2xx       int64
	bytes        int64
	statusCounts map[int]int64
	errorCounts  map[string]int64
}

// runLoadTest executes the load test against the configured gateway URL and returns
// the aggregated report. It stops launching requests at the run deadline, then drains
// in-flight requests unless the parent context is cancelled.
func runLoadTest(ctx context.Context, cfg loadTestConfig, progress io.Writer) (loadTestReport, error) {
	if cfg.concurrency < 1 {
		return loadTestReport{}, fmt.Errorf("concurrency must be at least 1")
	}
	if cfg.duration <= 0 {
		return loadTestReport{}, fmt.Errorf("duration must be positive")
	}
	if cfg.httpTimeout <= 0 {
		return loadTestReport{}, fmt.Errorf("HTTP timeout must be positive")
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return loadTestReport{}, fmt.Errorf("default HTTP transport has type %T, want *http.Transport", http.DefaultTransport)
	}
	transport := baseTransport.Clone()
	transport.MaxIdleConns = cfg.concurrency
	transport.MaxIdleConnsPerHost = cfg.concurrency
	transport.MaxConnsPerHost = cfg.concurrency
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: cfg.httpTimeout}

	deadline := time.Now().Add(cfg.duration)
	start := time.Now()
	launchCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	pacer := newRequestPacer(launchCtx, cfg.targetRPS)
	defer pacer.Stop()
	histogram := newLatencyHistogram(cfg.concurrency)
	workerStats := make([]workerLoadStats, cfg.concurrency)

	var wg sync.WaitGroup
	for i := range cfg.concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			stats := &workerStats[workerID]
			for {
				select {
				case <-launchCtx.Done():
					return
				default:
				}
				if !pacer.Wait(launchCtx) {
					return
				}
				if !time.Now().Before(deadline) {
					return
				}
				latency, status, errKind, bytesRead, responded := fireRequest(ctx, client, cfg)
				stats.total++
				stats.bytes += int64(bytesRead)
				histogram.Record(workerID, latency)
				switch {
				case responded && errKind == "" && status >= 200 && status < 300:
					stats.success++
				case responded && errKind == "":
					stats.non2xx++
					stats.errors++
				default:
					stats.errors++
				}
				if status != 0 {
					if stats.statusCounts == nil {
						stats.statusCounts = make(map[int]int64)
					}
					stats.statusCounts[status]++
				}
				if errKind != "" {
					if stats.errorCounts == nil {
						stats.errorCounts = make(map[string]int64)
					}
					stats.errorCounts[errKind]++
				}
			}
		}(i)
	}

	if progress != nil {
		fmt.Fprintf(progress, "loadtest: driving %d concurrent workers for %s (target %d RPS)...\n",
			cfg.concurrency, cfg.duration, cfg.targetRPS)
	}

	wg.Wait()
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = cfg.duration
	}

	statusCounts := make(map[int]int64)
	errorCounts := make(map[string]int64)
	var total, success, errorCount, non2xx, totalBytes int64
	for i := range workerStats {
		stats := &workerStats[i]
		total += stats.total
		success += stats.success
		errorCount += stats.errors
		non2xx += stats.non2xx
		totalBytes += stats.bytes
		for status, count := range stats.statusCounts {
			statusCounts[status] += count
		}
		for kind, count := range stats.errorCounts {
			errorCounts[kind] += count
		}
	}
	return buildHistogramReport(cfg, histogram, statusCounts, errorCounts, totalBytes,
		total, success, errorCount, non2xx, elapsed), nil
}

// fireRequest issues a single request to the target and returns its latency, HTTP
// status (0 on transport error), a non-empty errorKind when the request failed at the
// transport layer, the number of response bytes read, and whether an HTTP response was
// obtained at all.
func fireRequest(ctx context.Context, client *http.Client, cfg loadTestConfig) (
	latency time.Duration, status int, errKind string, bytesRead int, responded bool,
) {
	var body io.Reader
	if cfg.body != "" {
		// Build a fresh reader per request: a shared *bytes.Reader is not safe for
		// concurrent use and would send empty bodies to all but the first worker.
		body = strings.NewReader(cfg.body)
	}
	req, err := http.NewRequestWithContext(ctx, cfg.method, cfg.targetURL, body)
	if err != nil {
		return 0, 0, "build_request", 0, false
	}
	for _, h := range cfg.headers {
		req.Header.Set(h.key, h.value)
	}
	if cfg.body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return time.Since(start), 0, classifyError(err), 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	n, readErr := io.Copy(io.Discard, resp.Body)
	latency = time.Since(start)
	if readErr != nil {
		return latency, resp.StatusCode, classifyError(readErr), int(n), true
	}
	return latency, resp.StatusCode, "", int(n), true
}

const maxPreflightResponseBytes = 1 << 20

func verifyMockRoute(
	ctx context.Context,
	client *http.Client,
	cfg loadTestConfig,
	marker string,
	mock *mockUpstream,
) error {
	if mock == nil {
		return fmt.Errorf("expected in-process mock is required")
	}
	if sameEndpoint(cfg.targetURL, mock.Addr()) {
		return fmt.Errorf("target %q points directly to the mock; provide the gateway URL instead", cfg.targetURL)
	}
	beforeCalls := mock.Calls()

	var body io.Reader
	if cfg.body != "" {
		body = strings.NewReader(cfg.body)
	}
	req, err := http.NewRequestWithContext(ctx, cfg.method, cfg.targetURL, body)
	if err != nil {
		return fmt.Errorf("build preflight request: %w", err)
	}
	for _, h := range cfg.headers {
		req.Header.Set(h.key, h.value)
	}
	if cfg.body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send preflight request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxPreflightResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read preflight response: %w", err)
	}
	if got := mock.Calls() - beforeCalls; got != 1 {
		return fmt.Errorf("preflight reached the in-process mock %d times; want exactly once", got)
	}
	if len(responseBody) > maxPreflightResponseBytes {
		return fmt.Errorf("preflight response exceeds %d bytes", maxPreflightResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("preflight returned HTTP %d", resp.StatusCode)
	}
	if !bytes.Contains(responseBody, []byte(marker)) {
		return fmt.Errorf("unique mock response marker not found; configure the target gateway to use only the loadtest mock account")
	}
	return nil
}

func sameEndpoint(targetURL, mockAddr string) bool {
	target, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	mockHost, mockPort, err := net.SplitHostPort(mockAddr)
	if err != nil || target.Port() != mockPort {
		return false
	}
	targetHost := target.Hostname()
	if strings.EqualFold(targetHost, mockHost) {
		return true
	}
	targetIP := net.ParseIP(targetHost)
	mockIP := net.ParseIP(mockHost)
	if targetIP != nil && mockIP != nil {
		return targetIP.Equal(mockIP) || targetIP.IsLoopback() && mockIP.IsLoopback()
	}
	return strings.EqualFold(targetHost, "localhost") && mockIP != nil && mockIP.IsLoopback()
}

func classifyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "timeout"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "EOF"):
		return "eof"
	case strings.Contains(msg, "reset"):
		return "connection_reset"
	default:
		return "transport_error"
	}
}

func buildHistogramReport(
	cfg loadTestConfig,
	histogram *latencyHistogram,
	statusCounts map[int]int64,
	errorCounts map[string]int64,
	totalBytes int64,
	total, success, errCount, non2xx int64,
	elapsed time.Duration,
) loadTestReport {
	report := loadTestReport{
		Concurrency:     cfg.concurrency,
		Duration:        elapsed,
		TargetRPS:       cfg.targetRPS,
		TotalRequests:   total,
		SuccessCount:    success,
		ErrorCount:      errCount,
		Non2xxCount:     non2xx,
		StatusHistogram: statusCounts,
		ErrorHistogram:  errorCounts,
		BytesReceived:   totalBytes,
		LatencyMin:      histogram.Min(),
		LatencyP50:      histogram.Percentile(0.50),
		LatencyP95:      histogram.Percentile(0.95),
		LatencyP99:      histogram.Percentile(0.99),
		LatencyMax:      histogram.Max(),
	}
	if total > 0 {
		report.SuccessRatio = float64(success) / float64(total)
		if seconds := elapsed.Seconds(); seconds > 0 {
			report.AchievedRPS = float64(total) / seconds
		}
	}
	return report
}

// requestPacer applies one global rate limit across all workers. A single buffered
// token allows the run to start immediately without accumulating a later burst.
type requestPacer struct {
	unlimited bool
	tokens    <-chan struct{}
	cancel    context.CancelFunc
	done      <-chan struct{}
}

func newRequestPacer(ctx context.Context, targetRPS int) *requestPacer {
	if targetRPS <= 0 {
		return &requestPacer{unlimited: true}
	}
	interval := time.Second / time.Duration(targetRPS)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	pacerCtx, cancel := context.WithCancel(ctx)
	tokens := make(chan struct{}, 1)
	done := make(chan struct{})
	tokens <- struct{}{}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pacerCtx.Done():
				return
			case <-ticker.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			}
		}
	}()
	return &requestPacer{tokens: tokens, cancel: cancel, done: done}
}

func (p *requestPacer) Wait(ctx context.Context) bool {
	if p.unlimited {
		return true
	}
	select {
	case <-p.tokens:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *requestPacer) Stop() {
	if p.unlimited {
		return
	}
	p.cancel()
	<-p.done
}

// printReport writes a human-readable metrics report to w.
func printReport(w io.Writer, r loadTestReport) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "================ loadtest report ================")
	fmt.Fprintf(w, "Concurrency level : %d\n", r.Concurrency)
	fmt.Fprintf(w, "Duration          : %s\n", r.Duration)
	fmt.Fprintf(w, "Target RPS        : %d\n", r.TargetRPS)
	fmt.Fprintf(w, "Total requests    : %d\n", r.TotalRequests)
	fmt.Fprintf(w, "Successful (2xx)  : %d\n", r.SuccessCount)
	fmt.Fprintf(w, "Non-2xx responses : %d\n", r.Non2xxCount)
	fmt.Fprintf(w, "Errors            : %d\n", r.ErrorCount)
	fmt.Fprintf(w, "Success ratio     : %.2f%%\n", r.SuccessRatio*100)
	fmt.Fprintf(w, "Achieved RPS      : %.2f\n", r.AchievedRPS)
	fmt.Fprintf(w, "Bytes received    : %s\n", formatBytes(r.BytesReceived))
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Latency (synthetic, mock upstream):")
	fmt.Fprintf(w, "  min  : %s\n", r.LatencyMin)
	fmt.Fprintf(w, "  p50  : %s\n", r.LatencyP50)
	fmt.Fprintf(w, "  p95  : %s\n", r.LatencyP95)
	fmt.Fprintf(w, "  p99  : %s\n", r.LatencyP99)
	fmt.Fprintf(w, "  max  : %s\n", r.LatencyMax)
	printHistograms(w, r)
	fmt.Fprintln(w, "=================================================")
	fmt.Fprintln(w, "NOTE: results are synthetic (canned mock-upstream responses) and")
	fmt.Fprintln(w, "compare relative performance/capacity only, not real upstream behavior.")
	fmt.Fprintln(w, "For ~5000 concurrent production runs, raise -concurrency and tune OS")
	fmt.Fprintln(w, "ulimit -n / ephemeral port range on the load-driver host accordingly.")
}

func printHistograms(w io.Writer, r loadTestReport) {
	if len(r.StatusHistogram) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "HTTP status histogram:")
		for _, code := range sortedStatusCodes(r.StatusHistogram) {
			fmt.Fprintf(w, "  %d : %d\n", code, r.StatusHistogram[code])
		}
	}
	if len(r.ErrorHistogram) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Error histogram:")
		for _, kind := range sortedKeys(r.ErrorHistogram) {
			fmt.Fprintf(w, "  %-20s : %d\n", kind, r.ErrorHistogram[kind])
		}
	}
}

func sortedStatusCodes(m map[int]int64) []int {
	out := make([]int, 0, len(m))
	for code := range m {
		out = append(out, code)
	}
	sort.Ints(out)
	return out
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for p := n / unit; p >= unit; p /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// parseHeaders parses a header string of the form `Key: Value;Key2: Value2` (newline
// or semicolon separated) into structured headers. Empty input yields no headers.
func parseHeaders(raw string) ([]header, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' })
	headers := make([]header, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.Index(p, ":")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid header %q (expected 'Key: Value')", p)
		}
		headers = append(headers, header{
			key:   strings.TrimSpace(p[:idx]),
			value: strings.TrimSpace(p[idx+1:]),
		})
	}
	return headers, nil
}
