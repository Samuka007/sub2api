package service

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type accountHealthResolverStub map[string][]net.IPAddr

func (s accountHealthResolverStub) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses, ok := s[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return addresses, nil
}

type accountHealthResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f accountHealthResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type accountHealthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f accountHealthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newAccountHealthFetcherForTest(resolver accountHealthDNSResolver, roundTrip http.RoundTripper) *httpAccountHealthMailboxFetcher {
	fetcher := newHTTPAccountHealthMailboxFetcher()
	fetcher.client.Transport = roundTrip
	fetcher.client.Timeout = 0
	fetcher.resolver = resolver
	fetcher.wait = func(context.Context, time.Duration) error {
		return nil
	}
	return fetcher
}

func TestParseAndValidateAccountHealthURLRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"private.example": {{IP: net.ParseIP("192.168.1.10")}},
		"mixed.example": {
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("127.0.0.1")},
		},
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
		"nat64.example":  {{IP: net.ParseIP("64:ff9b:1::a00:1")}},
	}
	tests := []string{
		"http://public.example/mail",
		"https://public.example/" + strings.Repeat("x", accountHealthMaxURLBytes),
		"https://127.0.0.1/mail",
		"https://169.254.169.254/latest/meta-data",
		"https://198.18.0.10/mail",
		"https://private.example/mail",
		"https://mixed.example/mail",
		"https://user:secret@public.example/mail",
		"https://[64:ff9b:1::a00:1]/mail",
		"https://nat64.example/mail",
		"https://[::7f00:1]/mail",
		"https://[::ffff:0:7f00:1]/mail",
		"https://[2002:7f00:1::]/mail",
		"https://[2001:0:4136:e378:8000:63bf:3fff:fdd2]/mail",
		"https://[2001:2::1]/mail",
		"https://[100:0:0:1::1]/mail",
		"https://[3ffe::1]/mail",
		"https://[3fff::1]/mail",
		"https://[5f00::1]/mail",
		"https://[fec0::1]/mail",
		"https://[2606:4700:1234:5678:0:5efe:7f00:1]/mail",
		"file:///tmp/mail",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := parseAndValidateAccountHealthURL(context.Background(), resolver, rawURL)
			if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
				t.Fatalf("expected unsafe URL error, got %v", err)
			}
		})
	}
}

func TestParseAndValidateAccountHealthURLRejectsPrivateRFC6052Mappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prefix    string
		discovery string
		private   string
	}{
		{
			name:      "/32",
			prefix:    "2606:4700::/32",
			discovery: "2606:4700:c000:aa::",
			private:   "2606:4700:a00:1::",
		},
		{
			name:      "/40",
			prefix:    "2606:4700:aa00::/40",
			discovery: "2606:4700:aac0:0:aa::",
			private:   "2606:4700:aa0a:0:1::",
		},
		{
			name:      "/48",
			prefix:    "2606:4700:aabb::/48",
			discovery: "2606:4700:aabb:c000:0:aa00::",
			private:   "2606:4700:aabb:a00:0:100::",
		},
		{
			name:      "/56",
			prefix:    "2606:4700:aabb:cc00::/56",
			discovery: "2606:4700:aabb:ccc0:0:aa::",
			private:   "2606:4700:aabb:cc0a:0:1::",
		},
		{
			name:      "/64",
			prefix:    "2606:4700:aabb:ccdd::/64",
			discovery: "2606:4700:aabb:ccdd:c0:0:aa00:0",
			private:   "2606:4700:aabb:ccdd:a:0:100:0",
		},
		{
			name:      "/96",
			prefix:    "2606:4700:aabb:ccdd:eeff:1100::/96",
			discovery: "2606:4700:aabb:ccdd:eeff:1100:c000:aa",
			private:   "2606:4700:aabb:ccdd:eeff:1100:a00:1",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolver := accountHealthResolverStub{
				accountHealthNAT64WKN: {{IP: net.ParseIP(test.discovery)}},
			}
			_, err := parseAndValidateAccountHealthURL(
				context.Background(),
				resolver,
				"https://["+test.private+"]/mail",
			)
			if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
				t.Fatalf("expected private IPv4 mapping through %s to be rejected, got %v", test.prefix, err)
			}
		})
	}
}

func TestParseAndValidateAccountHealthURLRejectsZonedIPv6Literals(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		accountHealthNAT64WKN: {{IP: net.ParseIP("2606:4700:aabb:c000:0:aa00::")}},
	}
	tests := []string{
		"https://[64:ff9b:1:a00:0:100::%25eth0]/mail",
		"https://[2002:7f00:1::%25eth0]/mail",
		"https://[2606:4700:aabb:a00:0:100::%25eth0]/mail",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := parseAndValidateAccountHealthURL(context.Background(), resolver, rawURL)
			if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
				t.Fatalf("expected zoned IPv6 URL to be rejected, got %v", err)
			}
		})
	}
}

func TestParseAndValidateAccountHealthURLAllowsPublicRFC6052Mapping(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		accountHealthNAT64WKN: {{IP: net.ParseIP("2606:4700:aabb:ccdd:eeff:1100:c000:aa")}},
	}
	parsed, err := parseAndValidateAccountHealthURL(
		context.Background(),
		resolver,
		"https://[2606:4700:aabb:ccdd:eeff:1100:808:808]/mail",
	)
	if err != nil {
		t.Fatalf("expected public IPv4 mapping to be allowed, got %v", err)
	}
	if parsed.Hostname() != "2606:4700:aabb:ccdd:eeff:1100:808:808" {
		t.Fatalf("unexpected parsed host %q", parsed.Hostname())
	}
}

func TestParseAndValidateAccountHealthURLValidatesWellKnownNAT64Literals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{
			name:   "public IPv4 mapping",
			rawURL: "https://[64:ff9b::808:808]/mail",
		},
		{
			name:    "private IPv4 mapping",
			rawURL:  "https://[64:ff9b::a00:1]/mail",
			wantErr: true,
		},
		{
			name:    "network-specific prefix remains blocked",
			rawURL:  "https://[64:ff9b:1:808:8:800::]/mail",
			wantErr: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseAndValidateAccountHealthURL(context.Background(), accountHealthResolverStub{}, test.rawURL)
			if test.wantErr {
				if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
					t.Fatalf("expected unsafe URL error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected public well-known NAT64 mapping to be allowed, got %v", err)
			}
		})
	}
}

func TestParseAndValidateAccountHealthURLValidatesWellKnownNAT64DNSResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    string
		address string
		wantErr bool
	}{
		{
			name:    "public IPv4 mapping",
			host:    "public-nat64.example",
			address: "64:ff9b::808:808",
		},
		{
			name:    "private IPv4 mapping",
			host:    "private-nat64.example",
			address: "64:ff9b::a00:1",
			wantErr: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := accountHealthResolverStub{
				test.host: {{IP: net.ParseIP(test.address)}},
			}
			_, err := parseAndValidateAccountHealthURL(context.Background(), resolver, "https://"+test.host+"/mail")
			if test.wantErr {
				if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
					t.Fatalf("expected unsafe URL error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected public well-known NAT64 DNS result to be allowed, got %v", err)
			}
		})
	}
}

func TestParseAndValidateAccountHealthURLDoesNotGuessUndiscoveredNAT64Prefixes(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		accountHealthNAT64WKN: {{IP: net.ParseIP("2606:4700:aabb:ccdd:eeff:1100:c000:aa")}},
	}
	_, err := parseAndValidateAccountHealthURL(
		context.Background(),
		resolver,
		"https://[2607:f8b0:1234:5678:9abc:def0:a00:1]/mail",
	)
	if err != nil {
		t.Fatalf("an unrelated public IPv6 ending in private-looking bits must remain allowed: %v", err)
	}
}

func TestParseAndValidateAccountHealthURLFailsClosedWhenNAT64DiscoveryFails(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == accountHealthNAT64WKN {
			return nil, errors.New("discovery unavailable")
		}
		return nil, errors.New("unexpected lookup")
	})
	_, err := parseAndValidateAccountHealthURL(
		context.Background(),
		resolver,
		"https://[2606:4700:1234:5678::1]/mail",
	)
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("IPv6 validation must fail closed when PREF64 discovery is unavailable: %v", err)
	}
}

func TestAccountHealthNAT64PrefixCacheAvoidsRepeatedDiscovery(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32
	resolver := accountHealthResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != accountHealthNAT64WKN {
			return nil, errors.New("unexpected lookup")
		}
		lookups.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("2606:4700:aabb:ccdd:eeff:1100:c000:aa")}}, nil
	})
	cache := &accountHealthNAT64PrefixCache{}
	for _, rawURL := range []string{
		"https://[2606:4700:aabb:ccdd:eeff:1100:808:808]/mail",
		"https://[2606:4700:1234:5678::1]/mail",
	} {
		if _, err := parseAndValidateAccountHealthURLWithCache(
			context.Background(),
			resolver,
			cache,
			rawURL,
		); err != nil {
			t.Fatalf("parse %q: %v", rawURL, err)
		}
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("expected one cached PREF64 lookup, got %d", got)
	}
}

func TestAccountHealthNAT64PrefixCacheDoesNotCacheDiscoveryFailure(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32
	resolver := accountHealthResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != accountHealthNAT64WKN {
			return nil, errors.New("unexpected lookup")
		}
		if lookups.Add(1) == 1 {
			return nil, errors.New("temporary discovery failure")
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.0.170")}}, nil
	})
	cache := &accountHealthNAT64PrefixCache{}
	if _, err := cache.get(context.Background(), resolver); err == nil {
		t.Fatal("expected the first PREF64 discovery to fail")
	}
	prefixes, err := cache.get(context.Background(), resolver)
	if err != nil || len(prefixes) != 0 {
		t.Fatalf("expected a fresh successful non-DNS64 lookup, got %v, %v", prefixes, err)
	}
	if lookups.Load() != 2 {
		t.Fatalf("expected failed discovery not to be cached, got %d lookups", lookups.Load())
	}
}

func TestDiscoverAccountHealthNAT64PrefixesRejectsOnlyAmbiguousIPv6Answers(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		accountHealthNAT64WKN: {{IP: net.ParseIP("2606:4700:c000:aa::c000:aa")}},
	}
	_, err := discoverAccountHealthNAT64Prefixes(context.Background(), resolver)
	if !errors.Is(err, errAccountHealthNAT64Discovery) {
		t.Fatalf("expected ambiguous PREF64 discovery to fail closed, got %v", err)
	}
}

func TestDiscoverAccountHealthNAT64PrefixesIgnoresAmbiguousAndNonIPv6Answers(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		accountHealthNAT64WKN: {
			{IP: net.ParseIP("192.0.0.170")},
			{IP: net.ParseIP("2606:4700:c000:aa::c000:aa")},
			{IP: net.ParseIP("2606:4700:aabb:ccdd:eeff:1100:c000:aa")},
		},
	}
	prefixes, err := discoverAccountHealthNAT64Prefixes(context.Background(), resolver)
	if err != nil {
		t.Fatalf("discover PREF64: %v", err)
	}
	want := netip.MustParsePrefix("2606:4700:aabb:ccdd:eeff:1100::/96")
	if len(prefixes) != 1 || prefixes[0] != want {
		t.Fatalf("unexpected discovered prefixes: %#v", prefixes)
	}
}

func TestAccountHealthSafeDialerPinsValidatedAddress(t *testing.T) {
	t.Parallel()

	var dialed string
	dialer := &accountHealthSafeDialer{
		resolver: accountHealthResolverStub{
			"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
		},
		dialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = address
			return nil, errors.New("test stop")
		},
	}
	_, err := dialer.DialContext(context.Background(), "tcp", "mail.example:443")
	if !errors.Is(err, errAccountHealthMailboxFetch) {
		t.Fatalf("expected stable fetch error, got %v", err)
	}
	if dialed != "93.184.216.34:443" {
		t.Fatalf("expected validated IP to be dialed, got %q", dialed)
	}
}

func TestAccountHealthSafeDialerFallsBackWhileFirstAddressIsBlocked(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	dialer := &accountHealthSafeDialer{
		resolver: accountHealthResolverStub{
			"mail.example": {
				{IP: net.ParseIP("93.184.216.34")},
				{IP: net.ParseIP("93.184.216.35")},
			},
		},
		fallbackDelay: 10 * time.Millisecond,
		dialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			switch address {
			case "93.184.216.34:443":
				close(firstStarted)
				<-ctx.Done()
				close(firstCanceled)
				return nil, ctx.Err()
			case "93.184.216.35:443":
				return clientConn, nil
			default:
				return nil, fmt.Errorf("unexpected dial target %s", address)
			}
		},
	}

	startedAt := time.Now()
	conn, err := dialer.DialContext(context.Background(), "tcp", "mail.example:443")
	if err != nil {
		t.Fatalf("dial fallback failed: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("fallback waited too long: %s", elapsed)
	}
	_ = conn.Close()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first validated address was not attempted")
	}
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("losing dial was not canceled")
	}
}

func TestAccountHealthSafeDialerRejectsDNSRebindingBeforeDial(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32
	resolver := accountHealthResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "mail.example" {
			return nil, errors.New("unexpected lookup")
		}
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	cache := &accountHealthNAT64PrefixCache{}
	_, err := parseAndValidateAccountHealthURLWithCache(
		context.Background(), resolver, cache, "https://mail.example/inbox",
	)
	if err != nil {
		t.Fatalf("initial public validation failed: %v", err)
	}

	var dialCalls atomic.Int32
	dialer := &accountHealthSafeDialer{
		resolver:    resolver,
		pref64Cache: cache,
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, errors.New("must not dial")
		},
	}
	_, err = dialer.DialContext(context.Background(), "tcp", "mail.example:443")
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected rebound private address to be rejected, got %v", err)
	}
	if dialCalls.Load() != 0 {
		t.Fatalf("private rebound reached the network dialer %d times", dialCalls.Load())
	}
}

func TestAccountHealthSafeDialerRejectsZonedIPv6BeforeDial(t *testing.T) {
	t.Parallel()

	var dialCalls atomic.Int32
	dialer := &accountHealthSafeDialer{
		resolver: accountHealthResolverStub{
			accountHealthNAT64WKN: {{IP: net.ParseIP("2606:4700:aabb:c000:0:aa00::")}},
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, errors.New("must not dial")
		},
	}

	_, err := dialer.DialContext(
		context.Background(),
		"tcp",
		"[64:ff9b:1:a00:0:100::%eth0]:443",
	)
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected zoned IPv6 address to be rejected, got %v", err)
	}
	if dialCalls.Load() != 0 {
		t.Fatalf("zoned IPv6 address reached the network dialer %d times", dialCalls.Load())
	}
}

func TestResolveAccountHealthHostDeduplicatesAndCapsAddresses(t *testing.T) {
	t.Parallel()

	resolved := make([]net.IPAddr, 0, accountHealthMaxResolvedIPs+2)
	for index := 1; index <= accountHealthMaxResolvedIPs+1; index++ {
		resolved = append(resolved, net.IPAddr{IP: net.ParseIP(fmt.Sprintf("93.184.216.%d", index))})
	}
	resolver := accountHealthResolverStub{"mail.example": resolved}
	_, err := resolveAccountHealthHostWithCache(
		context.Background(), resolver, &accountHealthNAT64PrefixCache{}, "mail.example",
	)
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected excessive unique DNS answers to be rejected, got %v", err)
	}

	resolver["mail.example"] = []net.IPAddr{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("93.184.216.34")},
	}
	addresses, err := resolveAccountHealthHostWithCache(
		context.Background(), resolver, &accountHealthNAT64PrefixCache{}, "mail.example",
	)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("expected duplicate DNS answers to collapse to one address, got %v, %v", addresses, err)
	}
}

func TestAccountHealthFetcherValidatesSameHostRedirectAndClearsReferer(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"first.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"/inbox"}},
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Request:    req,
			}, nil
		}
		if got := req.Header.Get("Referer"); got != "" {
			t.Fatalf("redirect leaked Referer: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("mailbox")),
			Request:    req,
		}, nil
	}))

	result, err := fetcher.Fetch(context.Background(), "https://first.example/inbox?pwd=secret&token=abc")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(result.body) != "mailbox" || requests != 2 {
		t.Fatalf("unexpected fetch result: body=%q requests=%d", result.body, requests)
	}
	if len(result.visitedURLs) != 2 {
		t.Fatalf("expected redirect URL chain, got %#v", result.visitedURLs)
	}
	if result.visitedURLs[0] != "https://first.example/inbox?pwd=secret&token=abc" ||
		result.visitedURLs[1] != "https://first.example/inbox" {
		t.Fatalf("unexpected redirect URL chain: %#v", result.visitedURLs)
	}
}

func TestAccountHealthFetcherStopsAtRedirectLimit(t *testing.T) {
	t.Parallel()

	requests := 0
	fetcher := newAccountHealthFetcherForTest(
		accountHealthResolverStub{"public.example": {{IP: net.ParseIP("93.184.216.34")}}},
		accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {fmt.Sprintf("/step-%d", requests)}},
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Request:    req,
			}, nil
		}),
	)
	fetcher.maxRedirects = 2

	_, err := fetcher.Fetch(context.Background(), "https://public.example/start")
	if !errors.Is(err, errAccountHealthMailboxFetch) {
		t.Fatalf("expected redirect limit error, got %v", err)
	}
	if requests != 3 {
		t.Fatalf("expected initial request plus two redirects, got %d requests", requests)
	}
}

func TestAccountHealthFetcherRejectsCrossHostRedirect(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"first.example":  {{IP: net.ParseIP("93.184.216.34")}},
		"second.example": {{IP: net.ParseIP("93.184.216.35")}},
	}
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://second.example/inbox"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    req,
		}, nil
	}))

	_, err := fetcher.Fetch(context.Background(), "https://first.example/inbox?pwd=secret")
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected cross-host redirect to be rejected, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("cross-host redirect must not be requested, requests=%d", requests)
	}
}

func TestAccountHealthFetcherAllowsSameHostCrossPortRedirect(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests > 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader("mailbox")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://mail.example:8443/inbox"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    req,
		}, nil
	}))

	result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox?pwd=secret")
	if err != nil {
		t.Fatalf("expected same-host cross-port redirect to succeed, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("same-host cross-port redirect must be requested, requests=%d", requests)
	}
	if len(result.visitedURLs) != 2 || result.visitedURLs[1] != "https://mail.example:8443/inbox" {
		t.Fatalf("unexpected redirect URL chain: %#v", result.visitedURLs)
	}
}

func TestAccountHealthFetcherRejectsHTTPRedirect(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://mail.example/inbox?pwd=do-not-leak"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    req,
		}, nil
	}))

	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox?pwd=secret")
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected HTTP redirect to be rejected, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("HTTP redirect must not be requested, requests=%d", requests)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error exposed mailbox credentials: %q", err)
	}
}

func TestAccountHealthFetcherBlocksPrivateRedirect(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"first.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://127.0.0.1/internal?pwd=do-not-leak"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    req,
		}, nil
	}))

	_, err := fetcher.Fetch(context.Background(), "https://first.example/inbox?pwd=secret")
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("expected unsafe redirect error, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("private redirect must not be requested, requests=%d", requests)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error exposed mailbox credentials: %q", err)
	}
}

func TestAccountHealthFetcherRejectsOversizedGzipBody(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), accountHealthMaxMailboxBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"text/html; charset=utf-8"},
				"Content-Encoding": []string{"gzip"},
			},
			Body:    io.NopCloser(bytes.NewReader(compressed.Bytes())),
			Request: req,
		}, nil
	}))

	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if !errors.Is(err, errAccountHealthMailboxResponseTooLarge) {
		t.Fatalf("expected response size error, got %v", err)
	}
}

func TestAccountHealthFetcherRejectsOversizedEncodedInput(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	for index := 0; index < 4; index++ {
		writer := gzip.NewWriter(&compressed)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Encoding": []string{"gzip"}},
			Body:          io.NopCloser(bytes.NewReader(compressed.Bytes())),
			ContentLength: -1,
			Request:       req,
		}, nil
	}))
	fetcher.maxBytes = 32

	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if !errors.Is(err, errAccountHealthMailboxResponseTooLarge) {
		t.Fatalf("expected encoded response size error, got %v", err)
	}
}

func TestAccountHealthFetcherRejectsOversizedDecodedInputBeforeCharsetConversion(t *testing.T) {
	t.Parallel()

	encodings := []struct {
		name      string
		newWriter func(io.Writer) (io.WriteCloser, error)
	}{
		{
			name: "gzip",
			newWriter: func(writer io.Writer) (io.WriteCloser, error) {
				return gzip.NewWriter(writer), nil
			},
		},
		{
			name: "deflate",
			newWriter: func(writer io.Writer) (io.WriteCloser, error) {
				return zlib.NewWriter(writer), nil
			},
		},
	}
	for _, encoding := range encodings {
		encoding := encoding
		t.Run(encoding.name, func(t *testing.T) {
			t.Parallel()

			var compressed bytes.Buffer
			writer, err := encoding.newWriter(&compressed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(bytes.Repeat([]byte{0x1b, '(', 'B'}, 64)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if compressed.Len() > 64 {
				t.Fatalf("test input exceeded wire budget: %d", compressed.Len())
			}

			resolver := accountHealthResolverStub{
				"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
			}
			fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":     []string{"text/plain; charset=iso-2022-jp"},
						"Content-Encoding": []string{encoding.name},
					},
					Body:          io.NopCloser(bytes.NewReader(compressed.Bytes())),
					ContentLength: -1,
					Request:       req,
				}, nil
			}))
			fetcher.maxBytes = 64

			_, err = fetcher.Fetch(context.Background(), "https://mail.example/inbox")
			if !errors.Is(err, errAccountHealthMailboxResponseTooLarge) {
				t.Fatalf("expected decoded response size error, got %v", err)
			}
		})
	}
}

func TestAccountHealthFetcherRejectsKnownCompressedContentLength(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Encoding": []string{"gzip"}},
			Body:          io.NopCloser(strings.NewReader("not read")),
			ContentLength: 33,
			Request:       req,
		}, nil
	}))
	fetcher.maxBytes = 32

	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if !errors.Is(err, errAccountHealthMailboxResponseTooLarge) {
		t.Fatalf("expected Content-Length response size error, got %v", err)
	}
}

func TestAccountHealthFetcherPreservesUTF8JSONWithoutCharset(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{"application/json", "application/problem+json"} {
		contentType := contentType
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()
			body := `{"padding":"` + strings.Repeat("a", 1100) + `","evidence":"账号恢复提醒"}`
			resolver := accountHealthResolverStub{
				"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
			}
			fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{contentType}},
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			}))

			result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
			if err != nil {
				t.Fatalf("Fetch returned error: %v", err)
			}
			if string(result.body) != body {
				t.Fatalf("UTF-8 JSON was changed during fetch: got %q", result.body)
			}
		})
	}
}

func TestAccountHealthFetcherRetriesOnlyContractStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		status           int
		expectedRequests int
		expectSuccess    bool
	}{
		{name: "request timeout", status: http.StatusRequestTimeout, expectedRequests: 2, expectSuccess: true},
		{name: "too many requests", status: http.StatusTooManyRequests, expectedRequests: 2, expectSuccess: true},
		{name: "internal server error", status: http.StatusInternalServerError, expectedRequests: 2, expectSuccess: true},
		{name: "bad gateway", status: http.StatusBadGateway, expectedRequests: 2, expectSuccess: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, expectedRequests: 2, expectSuccess: true},
		{name: "gateway timeout", status: http.StatusGatewayTimeout, expectedRequests: 2, expectSuccess: true},
		{name: "bad request", status: http.StatusBadRequest, expectedRequests: 1},
		{name: "not found", status: http.StatusNotFound, expectedRequests: 1},
		{name: "not implemented", status: http.StatusNotImplemented, expectedRequests: 1},
		{name: "http version not supported", status: http.StatusHTTPVersionNotSupported, expectedRequests: 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			fetcher := newAccountHealthFetcherForTest(
				accountHealthResolverStub{"mail.example": {{IP: net.ParseIP("93.184.216.34")}}},
				accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests++
					status := test.status
					body := "retry"
					if requests == 2 {
						status = http.StatusOK
						body = "ok"
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    req,
					}, nil
				}),
			)
			fetcher.wait = func(_ context.Context, _ time.Duration) error {
				return nil
			}

			result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
			if test.expectSuccess {
				if err != nil {
					t.Fatalf("Fetch returned error: %v", err)
				}
				if string(result.body) != "ok" {
					t.Fatalf("unexpected retry body: %q", result.body)
				}
			} else if !errors.Is(err, errAccountHealthMailboxFetch) {
				t.Fatalf("expected stable fetch error, got %v", err)
			}
			if requests != test.expectedRequests {
				t.Fatalf("expected %d requests, got %d", test.expectedRequests, requests)
			}
		})
	}
}

func TestAccountHealthFetcherRetriesTransportErrors(t *testing.T) {
	t.Parallel()

	t.Run("recovers after a transient transport error", func(t *testing.T) {
		t.Parallel()
		requests := 0
		fetcher := newAccountHealthFetcherForTest(
			accountHealthResolverStub{"mail.example": {{IP: net.ParseIP("93.184.216.34")}}},
			accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					return nil, errors.New("temporary transport failure")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader("ok")),
					Request:    req,
				}, nil
			}),
		)

		result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")

		if err != nil {
			t.Fatalf("Fetch returned error: %v", err)
		}
		if string(result.body) != "ok" || requests != 2 {
			t.Fatalf("unexpected retry result: body=%q requests=%d", result.body, requests)
		}
	})

	t.Run("returns the stable error after transport retries are exhausted", func(t *testing.T) {
		t.Parallel()
		requests := 0
		waits := 0
		fetcher := newAccountHealthFetcherForTest(
			accountHealthResolverStub{"mail.example": {{IP: net.ParseIP("93.184.216.34")}}},
			accountHealthRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("persistent transport failure")
			}),
		)
		fetcher.maxAttempts = 3
		fetcher.wait = func(context.Context, time.Duration) error {
			waits++
			return nil
		}

		_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")

		if !errors.Is(err, errAccountHealthMailboxFetch) {
			t.Fatalf("expected stable fetch error, got %v", err)
		}
		if requests != 3 || waits != 2 {
			t.Fatalf("expected 3 requests and 2 waits, got requests=%d waits=%d", requests, waits)
		}
	})
}

func TestAccountHealthFetcherHonorsRetryAfterWithoutWaitingInTest(t *testing.T) {
	t.Parallel()

	requests := 0
	fetcher := newAccountHealthFetcherForTest(
		accountHealthResolverStub{"mail.example": {{IP: net.ParseIP("93.184.216.34")}}},
		accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			status := http.StatusTooManyRequests
			header := http.Header{"Retry-After": []string{"10"}}
			body := "retry"
			if requests == 2 {
				status = http.StatusOK
				header = http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}
				body = "ok"
			}
			return &http.Response{
				StatusCode: status,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}),
	)
	var waited time.Duration
	fetcher.wait = func(_ context.Context, delay time.Duration) error {
		waited = delay
		return nil
	}

	result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(result.body) != "ok" || requests != 2 {
		t.Fatalf("unexpected retry result: body=%q requests=%d", result.body, requests)
	}
	if waited != 10*time.Second {
		t.Fatalf("expected Retry-After delay of 10s, got %s", waited)
	}
}

func TestAccountHealthFetcherCancellationInterruptsProductionRetryWait(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	fetcher := newAccountHealthFetcherForTest(
		accountHealthResolverStub{"mail.example": {{IP: net.ParseIP("93.184.216.34")}}},
		accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"3600"}},
				Body:       io.NopCloser(strings.NewReader("retry")),
				Request:    req,
			}, nil
		}),
	)
	backoffStarted := make(chan time.Duration, 1)
	fetcher.wait = func(ctx context.Context, delay time.Duration) error {
		backoffStarted <- delay
		return waitForAccountHealthRetry(ctx, delay)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := fetcher.Fetch(ctx, "https://mail.example/inbox")
		result <- err
	}()

	select {
	case delay := <-backoffStarted:
		if delay != time.Hour {
			t.Fatalf("expected one-hour retry delay, got %s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("fetch did not enter retry backoff")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fetch did not return promptly after cancellation")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected no second request after cancellation, got %d requests", got)
	}
}

func TestAccountHealthRetryAfterSaturatesWithoutOverflow(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(1<<63 - 1)
	if delay := accountHealthRetryAfter("18446744073709551615", time.Now()); delay != maxDuration {
		t.Fatalf("expected saturated Retry-After delay, got %s", delay)
	}
}

func TestAccountHealthFetcherStopsAfterRetryAttemptsAreExhausted(t *testing.T) {
	t.Parallel()

	requests := 0
	fetcher := newAccountHealthFetcherForTest(
		accountHealthResolverStub{"public.example": {{IP: net.ParseIP("93.184.216.34")}}},
		accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("retry")),
				Request:    req,
			}, nil
		}),
	)
	fetcher.maxAttempts = 2

	_, err := fetcher.Fetch(context.Background(), "https://public.example/inbox")
	if !errors.Is(err, errAccountHealthMailboxFetch) {
		t.Fatalf("expected retry exhaustion error, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected exactly two attempts, got %d", requests)
	}
}

func TestAccountHealthFetcherAppliesOneDeadlineToEntireFetch(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverStub{
		"mail.example": {{IP: net.ParseIP("93.184.216.34")}},
	}
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))
	fetcher.timeout = 20 * time.Millisecond

	started := time.Now()
	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected fetch deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("fetch deadline was not applied to the whole chain: %s", elapsed)
	}
}

func TestAccountHealthFetcherDeadlineIncludesDNSResolution(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverFunc(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("request must not start before DNS validation completes")
		return nil, nil
	}))
	fetcher.timeout = 20 * time.Millisecond

	_, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DNS resolution to share the fetch deadline, got %v", err)
	}
}

func TestAccountHealthFetcherDeadlineIncludesNAT64Discovery(t *testing.T) {
	t.Parallel()

	resolver := accountHealthResolverFunc(func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host != accountHealthNAT64WKN {
			return nil, errors.New("unexpected lookup")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("request must not start before PREF64 discovery completes")
		return nil, nil
	}))
	fetcher.timeout = 20 * time.Millisecond

	_, err := fetcher.Fetch(context.Background(), "https://[2606:4700:1234:5678::1]/inbox")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected PREF64 discovery to share the fetch deadline, got %v", err)
	}
}

func TestAccountHealthFetcherReusesDeadlineAcrossRetriesAndRedirects(t *testing.T) {
	t.Parallel()

	var deadlines []time.Time
	recordDeadline := func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("fetch context is missing its total deadline")
		}
		deadlines = append(deadlines, deadline)
	}
	resolver := accountHealthResolverFunc(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		recordDeadline(ctx)
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	requests := 0
	fetcher := newAccountHealthFetcherForTest(resolver, accountHealthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		recordDeadline(req.Context())
		requests++
		status := http.StatusInternalServerError
		header := http.Header{}
		body := "retry"
		switch requests {
		case 2:
			status = http.StatusFound
			header.Set("Location", "/next")
			body = "redirect"
		case 3:
			status = http.StatusOK
			header.Set("Content-Type", "text/plain; charset=utf-8")
			body = "ok"
		}
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))
	fetcher.timeout = time.Minute
	fetcher.wait = func(ctx context.Context, _ time.Duration) error {
		recordDeadline(ctx)
		return nil
	}

	result, err := fetcher.Fetch(context.Background(), "https://mail.example/inbox")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(result.body) != "ok" || requests != 3 {
		t.Fatalf("unexpected fetch result: body=%q requests=%d", result.body, requests)
	}
	if len(deadlines) < 2 {
		t.Fatalf("expected multiple deadline observations, got %d", len(deadlines))
	}
	for i := 1; i < len(deadlines); i++ {
		if !deadlines[i].Equal(deadlines[0]) {
			t.Fatalf("fetch deadline was reset: first=%s observation[%d]=%s", deadlines[0], i, deadlines[i])
		}
	}
}

func TestAccountHealthFetcherLimitsResponseHeaders(t *testing.T) {
	t.Parallel()

	fetcher := newHTTPAccountHealthMailboxFetcher()
	transport, ok := fetcher.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", fetcher.client.Transport)
	}
	if transport.MaxResponseHeaderBytes != accountHealthMaxHeaderBytes {
		t.Fatalf("unexpected response header limit: %d", transport.MaxResponseHeaderBytes)
	}
	if transport.Proxy != nil {
		t.Fatal("account health transport must ignore environment proxies")
	}
	if transport.DialContext == nil {
		t.Fatal("account health transport must use the SSRF-safe dialer")
	}
	connection, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:443")
	if connection != nil {
		_ = connection.Close()
	}
	if !errors.Is(err, errAccountHealthUnsafeMailboxURL) {
		t.Fatalf("production transport dialer must reject loopback targets, got %v", err)
	}
}

func TestAccountHealthMailboxURLWithLimitOnlyUpdatesExistingParameter(t *testing.T) {
	t.Parallel()

	withLimit := accountHealthMailboxURLWithLimit("https://mail.example/inbox?mail=user%40example.com&limit=5&pwd=secret", 50)
	if !strings.Contains(withLimit, "limit=50") {
		t.Fatalf("expected existing limit to be updated, got %q", withLimit)
	}
	withoutLimit := accountHealthMailboxURLWithLimit("https://mail.example/inbox?mail=user%40example.com&pwd=secret", 50)
	if strings.Contains(withoutLimit, "limit=") {
		t.Fatalf("limit must not be added when absent, got %q", withoutLimit)
	}
}
