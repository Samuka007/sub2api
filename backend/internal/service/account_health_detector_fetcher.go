package service

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
)

const (
	accountHealthHTTPTimeout       = 45 * time.Second
	accountHealthMaxHeaderBytes    = 256 << 10
	accountHealthMaxRedirects      = 5
	accountHealthMaxFetchAttempts  = 2
	accountHealthRetryDelay        = 250 * time.Millisecond
	accountHealthNAT64CacheTTL     = time.Minute
	accountHealthNAT64WKN          = "ipv4only.arpa"
	accountHealthMaxResolvedIPs    = 16
	accountHealthDialFallbackDelay = 250 * time.Millisecond
)

var (
	errAccountHealthUnsafeMailboxURL = errors.New("account health mailbox URL is not allowed")
	errAccountHealthMailboxFetch     = errors.New("account health mailbox request failed")
	errAccountHealthNAT64Discovery   = errors.New("account health NAT64 prefix discovery failed")
)

type accountHealthDNSResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type accountHealthMailboxResponse struct {
	body        []byte
	contentType string
	visitedURLs []string
}

type accountHealthMailboxFetcher interface {
	Fetch(ctx context.Context, rawURL string) (accountHealthMailboxResponse, error)
}

type httpAccountHealthMailboxFetcher struct {
	client       *http.Client
	resolver     accountHealthDNSResolver
	maxBytes     int64
	maxRedirects int
	maxAttempts  int
	timeout      time.Duration
	wait         func(context.Context, time.Duration) error
	pref64Cache  *accountHealthNAT64PrefixCache
}

func newHTTPAccountHealthMailboxFetcher() *httpAccountHealthMailboxFetcher {
	resolver := net.DefaultResolver
	pref64Cache := &accountHealthNAT64PrefixCache{}
	safeDialer := &accountHealthSafeDialer{
		resolver:    resolver,
		pref64Cache: pref64Cache,
		dialer: &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	transport.DialContext = safeDialer.DialContext
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.MaxResponseHeaderBytes = accountHealthMaxHeaderBytes
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	return &httpAccountHealthMailboxFetcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   accountHealthHTTPTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		resolver:     resolver,
		maxBytes:     accountHealthMaxMailboxBytes,
		maxRedirects: accountHealthMaxRedirects,
		maxAttempts:  accountHealthMaxFetchAttempts,
		timeout:      accountHealthHTTPTimeout,
		wait:         waitForAccountHealthRetry,
		pref64Cache:  pref64Cache,
	}
}

type accountHealthSafeDialer struct {
	resolver      accountHealthDNSResolver
	dialer        *net.Dialer
	dialContext   func(context.Context, string, string) (net.Conn, error)
	pref64Cache   *accountHealthNAT64PrefixCache
	fallbackDelay time.Duration
}

type accountHealthDialResult struct {
	conn net.Conn
	err  error
}

func (d *accountHealthSafeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	addresses, err := resolveAccountHealthHostWithCache(ctx, d.resolver, d.pref64Cache, host)
	if err != nil {
		return nil, err
	}

	if len(addresses) == 0 {
		return nil, errAccountHealthUnsafeMailboxURL
	}

	dial := d.dialContext
	if dial == nil {
		dialer := d.dialer
		if dialer == nil {
			dialer = &net.Dialer{Timeout: 10 * time.Second}
		}
		dial = dialer.DialContext
	}
	fallbackDelay := d.fallbackDelay
	if fallbackDelay <= 0 {
		fallbackDelay = accountHealthDialFallbackDelay
	}

	dialCtx, cancel := context.WithCancel(ctx)
	results := make(chan accountHealthDialResult, len(addresses))
	startDial := func(ip netip.Addr) {
		go func() {
			conn, dialErr := dial(dialCtx, network, net.JoinHostPort(ip.String(), port))
			results <- accountHealthDialResult{conn: conn, err: dialErr}
		}()
	}
	closeLateConnections := func(pending int) {
		go func() {
			for range pending {
				result := <-results
				if result.conn != nil {
					_ = result.conn.Close()
				}
			}
		}()
	}

	started := 1
	pending := 1
	startDial(addresses[0])
	var fallbackTimer *time.Timer
	var fallbackC <-chan time.Time
	scheduleFallback := func() {
		if started >= len(addresses) || fallbackC != nil {
			return
		}
		fallbackTimer = time.NewTimer(fallbackDelay)
		fallbackC = fallbackTimer.C
	}
	stopFallback := func() {
		if fallbackTimer != nil && !fallbackTimer.Stop() {
			select {
			case <-fallbackTimer.C:
			default:
			}
		}
		fallbackC = nil
	}
	scheduleFallback()
	defer stopFallback()

	for pending > 0 || started < len(addresses) {
		select {
		case <-ctx.Done():
			cancel()
			closeLateConnections(pending)
			return nil, ctx.Err()
		case result := <-results:
			pending--
			if result.err == nil && result.conn != nil {
				if err := ctx.Err(); err != nil {
					_ = result.conn.Close()
					cancel()
					closeLateConnections(pending)
					return nil, err
				}
				cancel()
				closeLateConnections(pending)
				return result.conn, nil
			}
			if pending == 0 && started < len(addresses) {
				stopFallback()
				startDial(addresses[started])
				started++
				pending++
				scheduleFallback()
			}
		case <-fallbackC:
			fallbackC = nil
			startDial(addresses[started])
			started++
			pending++
			scheduleFallback()
		}
	}
	cancel()
	return nil, errAccountHealthMailboxFetch
}

func (f *httpAccountHealthMailboxFetcher) Fetch(ctx context.Context, rawURL string) (accountHealthMailboxResponse, error) {
	timeout := f.timeout
	if timeout <= 0 {
		timeout = accountHealthHTTPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	current, err := parseAndValidateAccountHealthURLWithCache(ctx, f.resolver, f.pref64Cache, rawURL)
	if err != nil {
		return accountHealthMailboxResponse{}, err
	}
	originHost, ok := accountHealthMailboxHTTPSHost(current)
	if !ok {
		return accountHealthMailboxResponse{}, errAccountHealthUnsafeMailboxURL
	}
	visitedURLs := make([]string, 0, accountHealthMaxRedirects+1)

	maxRedirects := f.maxRedirects
	if maxRedirects <= 0 {
		maxRedirects = accountHealthMaxRedirects
	}
	for redirects := 0; ; redirects++ {
		visitedURLs = append(visitedURLs, current.String())
		resp, err := f.fetchURL(ctx, current)
		if err != nil {
			return accountHealthMailboxResponse{}, err
		}

		if !isAccountHealthRedirect(resp.StatusCode) {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
			}
			mailbox, readErr := f.readResponse(resp)
			mailbox.visitedURLs = visitedURLs
			return mailbox, readErr
		}

		if redirects >= maxRedirects {
			_ = resp.Body.Close()
			return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
		}
		next, locationErr := resp.Location()
		_ = resp.Body.Close()
		if locationErr != nil {
			return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
		}
		next, err = parseAndValidateAccountHealthURLWithCache(ctx, f.resolver, f.pref64Cache, next.String())
		if err != nil {
			return accountHealthMailboxResponse{}, err
		}
		nextHost, ok := accountHealthMailboxHTTPSHost(next)
		if !ok || nextHost != originHost {
			return accountHealthMailboxResponse{}, errAccountHealthUnsafeMailboxURL
		}
		current = next
	}
}

func (f *httpAccountHealthMailboxFetcher) fetchURL(ctx context.Context, target *url.URL) (*http.Response, error) {
	maxAttempts := f.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = accountHealthMaxFetchAttempts
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, errAccountHealthMailboxFetch
		}
		req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.5")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		req.Header.Set("User-Agent", "Sub2API-Account-Health-Detector/1.0")

		resp, requestErr := f.client.Do(req)
		if requestErr == nil && !shouldRetryAccountHealthStatus(resp.StatusCode) {
			return resp, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt+1 >= maxAttempts {
			return nil, errAccountHealthMailboxFetch
		}

		delay := accountHealthRetryDelay
		if requestErr == nil {
			delay = accountHealthRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		wait := f.wait
		if wait == nil {
			wait = waitForAccountHealthRetry
		}
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, errAccountHealthMailboxFetch
}

func (f *httpAccountHealthMailboxFetcher) readResponse(resp *http.Response) (accountHealthMailboxResponse, error) {
	maxBytes := f.maxBytes
	if maxBytes <= 0 {
		maxBytes = accountHealthMaxMailboxBytes
	}
	if resp.ContentLength > maxBytes {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxResponseTooLarge
	}

	wireReader := &io.LimitedReader{R: resp.Body, N: maxBytes + 1}
	reader, closer, err := accountHealthDecodedReader(wireReader, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	decodedReader := &io.LimitedReader{R: reader, N: maxBytes + 1}
	contentType := resp.Header.Get("Content-Type")
	reader, err = accountHealthCharsetReader(decodedReader, contentType)
	if err != nil {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if wireReader.N <= 0 || decodedReader.N <= 0 {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxResponseTooLarge
	}
	if err != nil {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxFetch
	}
	if int64(len(body)) > maxBytes {
		return accountHealthMailboxResponse{}, errAccountHealthMailboxResponseTooLarge
	}
	return accountHealthMailboxResponse{body: body, contentType: contentType}, nil
}

func accountHealthCharsetReader(reader io.Reader, contentType string) (io.Reader, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err == nil {
		_, hasCharset := params["charset"]
		if !hasCharset && isAccountHealthJSONMediaType(mediaType) {
			return reader, nil
		}
	}
	return charset.NewReader(reader, contentType)
}

func isAccountHealthJSONMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "application/json" {
		return true
	}
	if !strings.HasPrefix(mediaType, "application/") {
		return false
	}
	subtype := strings.TrimPrefix(mediaType, "application/")
	return len(subtype) > len("+json") && strings.HasSuffix(subtype, "+json")
}

func accountHealthDecodedReader(reader io.Reader, contentEncoding string) (io.Reader, io.Closer, error) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return reader, nil, nil
	case "gzip", "x-gzip":
		decoded, err := gzip.NewReader(reader)
		return decoded, decoded, err
	case "deflate":
		decoded, err := zlib.NewReader(reader)
		return decoded, decoded, err
	default:
		return nil, nil, errAccountHealthMailboxFetch
	}
}

func parseAndValidateAccountHealthURL(ctx context.Context, resolver accountHealthDNSResolver, rawURL string) (*url.URL, error) {
	return parseAndValidateAccountHealthURLWithCache(ctx, resolver, &accountHealthNAT64PrefixCache{}, rawURL)
}

func parseAndValidateAccountHealthURLWithCache(
	ctx context.Context,
	resolver accountHealthDNSResolver,
	pref64Cache *accountHealthNAT64PrefixCache,
	rawURL string,
) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) == 0 || len(rawURL) > accountHealthMaxURLBytes {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	if parsed.Scheme != "https" {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	if err := validateAccountHealthPort(parsed); err != nil {
		return nil, err
	}
	if _, err := resolveAccountHealthHostWithCache(ctx, resolver, pref64Cache, parsed.Hostname()); err != nil {
		return nil, err
	}
	parsed.Fragment = ""
	return parsed, nil
}

func validateAccountHealthPort(parsed *url.URL) error {
	port := parsed.Port()
	if port == "" {
		return nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return errAccountHealthUnsafeMailboxURL
	}
	return nil
}

func resolveAccountHealthHostWithCache(
	ctx context.Context,
	resolver accountHealthDNSResolver,
	pref64Cache *accountHealthNAT64PrefixCache,
	host string,
) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" || isAccountHealthLocalHostname(host) {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	if parsed, err := netip.ParseAddr(host); err == nil {
		if parsed.Zone() != "" {
			return nil, errAccountHealthUnsafeMailboxURL
		}
		parsed = parsed.Unmap()
		if err := validatePublicAccountHealthIP(ctx, resolver, pref64Cache, parsed); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, errAccountHealthUnsafeMailboxURL
		}
		return []netip.Addr{parsed}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errAccountHealthUnsafeMailboxURL
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, errAccountHealthUnsafeMailboxURL
	}
	addresses := make([]netip.Addr, 0, min(len(resolved), accountHealthMaxResolvedIPs))
	seen := make(map[netip.Addr]struct{}, min(len(resolved), accountHealthMaxResolvedIPs))
	for _, item := range resolved {
		parsed, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			return nil, errAccountHealthUnsafeMailboxURL
		}
		parsed = parsed.Unmap()
		if _, exists := seen[parsed]; exists {
			continue
		}
		if len(addresses) >= accountHealthMaxResolvedIPs {
			return nil, errAccountHealthUnsafeMailboxURL
		}
		seen[parsed] = struct{}{}
		if err := validatePublicAccountHealthIP(ctx, resolver, pref64Cache, parsed); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, errAccountHealthUnsafeMailboxURL
		}
		addresses = append(addresses, parsed)
	}
	return addresses, nil
}

func validatePublicAccountHealthIP(
	ctx context.Context,
	resolver accountHealthDNSResolver,
	pref64Cache *accountHealthNAT64PrefixCache,
	ip netip.Addr,
) error {
	if accountHealthNAT64WellKnownPrefix.Contains(ip) {
		embedded, ok := extractAccountHealthRFC6052IPv4(ip, accountHealthNAT64WellKnownPrefix.Bits())
		if !ok || !isPublicAccountHealthIP(embedded) {
			return errAccountHealthUnsafeMailboxURL
		}
		return nil
	}
	if !isPublicAccountHealthIP(ip) {
		return errAccountHealthUnsafeMailboxURL
	}
	if !ip.Is6() {
		return nil
	}
	prefixes, err := pref64Cache.get(ctx, resolver)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errAccountHealthNAT64Discovery
	}
	for _, prefix := range prefixes {
		if !prefix.Contains(ip) {
			continue
		}
		embedded, ok := extractAccountHealthRFC6052IPv4(ip, prefix.Bits())
		if ok && !isPublicAccountHealthIP(embedded) {
			return errAccountHealthUnsafeMailboxURL
		}
	}
	return nil
}

type accountHealthNAT64PrefixCache struct {
	mu          sync.Mutex
	prefixes    []netip.Prefix
	expiresAt   time.Time
	refreshDone chan struct{}
}

func (c *accountHealthNAT64PrefixCache) get(
	ctx context.Context,
	resolver accountHealthDNSResolver,
) ([]netip.Prefix, error) {
	if c == nil {
		return discoverAccountHealthNAT64Prefixes(ctx, resolver)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		c.mu.Lock()
		if !c.expiresAt.IsZero() && time.Now().Before(c.expiresAt) {
			prefixes := cloneAccountHealthPrefixes(c.prefixes)
			c.mu.Unlock()
			return prefixes, nil
		}
		if c.refreshDone != nil {
			done := c.refreshDone
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		c.refreshDone = make(chan struct{})
		c.mu.Unlock()

		prefixes, err := discoverAccountHealthNAT64Prefixes(ctx, resolver)

		c.mu.Lock()
		if err == nil {
			c.prefixes = cloneAccountHealthPrefixes(prefixes)
			c.expiresAt = time.Now().Add(accountHealthNAT64CacheTTL)
		}
		done := c.refreshDone
		c.refreshDone = nil
		close(done)
		c.mu.Unlock()
		return prefixes, err
	}
}

func cloneAccountHealthPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	if len(prefixes) == 0 {
		return nil
	}
	cloned := make([]netip.Prefix, len(prefixes))
	copy(cloned, prefixes)
	return cloned
}

var accountHealthRFC6052PrefixLengths = []int{32, 40, 48, 56, 64, 96}

var accountHealthNAT64WellKnownPrefix = netip.MustParsePrefix("64:ff9b::/96")

var accountHealthNAT64DiscoveryIPv4 = []netip.Addr{
	netip.MustParseAddr("192.0.0.170"),
	netip.MustParseAddr("192.0.0.171"),
}

func discoverAccountHealthNAT64Prefixes(
	ctx context.Context,
	resolver accountHealthDNSResolver,
) ([]netip.Prefix, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupIPAddr(ctx, accountHealthNAT64WKN)
	if err != nil {
		return nil, err
	}

	prefixes := make([]netip.Prefix, 0, len(resolved))
	seen := make(map[netip.Prefix]struct{}, len(resolved))
	sawIPv6 := false
	for _, item := range resolved {
		ip, ok := netip.AddrFromSlice(item.IP)
		if !ok || !ip.Is6() || ip.Is4In6() {
			continue
		}
		sawIPv6 = true

		var matched netip.Prefix
		matches := 0
		for _, prefixLength := range accountHealthRFC6052PrefixLengths {
			embedded, valid := extractAccountHealthRFC6052IPv4(ip, prefixLength)
			if !valid || !isAccountHealthNAT64DiscoveryIPv4(embedded) {
				continue
			}
			matched = netip.PrefixFrom(ip, prefixLength).Masked()
			matches++
		}
		if matches != 1 {
			continue
		}
		if _, exists := seen[matched]; exists {
			continue
		}
		seen[matched] = struct{}{}
		prefixes = append(prefixes, matched)
	}
	if sawIPv6 && len(prefixes) == 0 {
		return nil, errAccountHealthNAT64Discovery
	}
	return prefixes, nil
}

func isAccountHealthNAT64DiscoveryIPv4(ip netip.Addr) bool {
	for _, discoveryIP := range accountHealthNAT64DiscoveryIPv4 {
		if ip == discoveryIP {
			return true
		}
	}
	return false
}

func extractAccountHealthRFC6052IPv4(ip netip.Addr, prefixLength int) (netip.Addr, bool) {
	if !ip.Is6() || ip.Is4In6() {
		return netip.Addr{}, false
	}
	raw := ip.As16()
	var embedded [4]byte
	switch prefixLength {
	case 32:
		copy(embedded[:], raw[4:8])
	case 40:
		copy(embedded[:3], raw[5:8])
		embedded[3] = raw[9]
	case 48:
		copy(embedded[:2], raw[6:8])
		copy(embedded[2:], raw[9:11])
	case 56:
		embedded[0] = raw[7]
		copy(embedded[1:], raw[9:12])
	case 64:
		copy(embedded[:], raw[9:13])
	case 96:
		copy(embedded[:], raw[12:16])
	default:
		return netip.Addr{}, false
	}
	if prefixLength < 96 && raw[8] != 0 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(embedded), true
}

func isAccountHealthLocalHostname(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" ||
		host == "localhost.localdomain" ||
		host == "metadata" ||
		host == "metadata.google.internal" ||
		host == "instance-data" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal")
}

var accountHealthBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	accountHealthNAT64WellKnownPrefix,
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("3ffe::/16"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicAccountHealthIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if isAccountHealthISATAPAddress(ip) {
		return false
	}
	for _, prefix := range accountHealthBlockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func isAccountHealthISATAPAddress(ip netip.Addr) bool {
	if !ip.Is6() {
		return false
	}
	raw := ip.As16()
	return (raw[8] == 0x00 || raw[8] == 0x02) &&
		raw[9] == 0x00 &&
		raw[10] == 0x5e &&
		raw[11] == 0xfe
}

func isAccountHealthRedirect(status int) bool {
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect ||
		status == http.StatusPermanentRedirect
}

func shouldRetryAccountHealthStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func accountHealthRetryAfter(value string, now time.Time) time.Duration {
	delay := accountHealthRetryDelay
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
		const maxDuration = time.Duration(1<<63 - 1)
		if seconds > uint64(maxDuration/time.Second) {
			delay = maxDuration
		} else {
			delay = time.Duration(seconds) * time.Second
		}
	} else if retryAt, err := http.ParseTime(value); err == nil {
		delay = retryAt.Sub(now)
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func waitForAccountHealthRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func accountHealthMailboxURLWithLimit(rawURL string, limit int) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || limit <= 0 {
		return rawURL
	}
	query := parsed.Query()
	if _, exists := query["limit"]; !exists {
		return rawURL
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
