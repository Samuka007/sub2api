package deepseekadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/webp"
)

const (
	// Moderation requests may contain a single inline image. Keep the request
	// bound above the raw-image bound to account for base64 expansion and JSON.
	maxModerationRequestBytes       = 16 << 20
	maxModerationParts              = 32
	maxModerationImagesPerInput     = 1
	maxModerationImageBytes         = 8 << 20
	maxModerationImageDataURLBytes  = 12 << 20
	maxModerationImageURLBytes      = 8192
	maxModerationImageDimension     = 8192
	maxModerationImagePixels        = 16 << 20
	maxModerationGIFFrames          = 32
	maxModerationGIFTotalPixels     = 16 << 20
	maxConcurrentModerationImages   = 1
	maxConcurrentModerationRequests = 1
)

var moderationImageMIMEs = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"gif":  "image/gif",
	"webp": "image/webp",
}

type unsupportedModerationInputError struct{ cause error }

func (e *unsupportedModerationInputError) Error() string {
	if e == nil || e.cause == nil {
		return "unsupported moderation input"
	}
	return "unsupported moderation input: " + e.cause.Error()
}

func (e *unsupportedModerationInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// moderationImagePolicyError marks a resolver result that is deliberately
// rejected by the image address policy. It must not be treated as a transient
// network failure: the caller supplied an address that cannot be inspected.
type moderationImagePolicyError struct{ cause error }

func (e *moderationImagePolicyError) Error() string {
	if e == nil || e.cause == nil {
		return "image host does not resolve to a public address"
	}
	return e.cause.Error()
}

func (e *moderationImagePolicyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeModerationError(err error) string {
	var unsupportedErr *unsupportedModerationInputError
	if errors.As(err, &unsupportedErr) {
		return "unsupported moderation input"
	}
	var fetchErr *moderationImageFetchError
	if errors.As(err, &fetchErr) {
		return fetchErr.Error()
	}
	return safeError(err)
}

// moderationImageFetchError is deliberately source-free: its wrapped cause
// may contain a URL from net/http, but Error is used by the adapter log path.
// A fetch failure is distinct from an image that is syntactically invalid or
// cannot be decoded, because the caller may retry the former.
type moderationImageFetchError struct {
	statusCode int
	cause      error
}

func (e *moderationImageFetchError) Error() string {
	if e == nil {
		return "image fetch unavailable"
	}
	if e.statusCode != 0 {
		return fmt.Sprintf("image fetch returned transient status %d", e.statusCode)
	}
	return "image fetch unavailable"
}

func (e *moderationImageFetchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *moderationImageFetchError) HTTPStatus() int {
	if e != nil && e.statusCode == http.StatusTooManyRequests {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

type moderationImageInput struct {
	URL    string
	Detail string
}

type preparedModerationImage struct {
	URL    string
	Detail string
}

// newModerationImageClient deliberately does not use environment proxies.
// Image URLs are user-controlled input and must be fetched either from a
// public address or from a loopback test server explicitly enabled by tests.
func newModerationImageClient(timeout time.Duration, allowHTTPForTests bool) *http.Client {
	imageTimeout := timeout
	if imageTimeout <= 0 || imageTimeout > time.Minute {
		imageTimeout = time.Minute
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           moderationImageDialContext(dialer, allowHTTPForTests),
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: imageTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   imageTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func moderationImageDialContext(dialer *net.Dialer, allowHTTPForTests bool) func(context.Context, string, string) (net.Conn, error) {
	return moderationImageDialContextWithResolver(dialer, allowHTTPForTests, nil, nil)
}

func moderationImageDialContextWithResolver(
	dialer *net.Dialer,
	allowHTTPForTests bool,
	lookupIP func(context.Context, string) ([]net.IP, error),
	dialContext func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	if lookupIP == nil {
		lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	if dialContext == nil {
		dialContext = dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowHTTPForTests {
			return dialContext(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid image address: %w", err)
		}
		ips, err := lookupIP(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve image host: %w", err)
		}
		var lastErr error
		for _, ip := range ips {
			if !isPublicModerationImageIP(ip) {
				continue
			}
			conn, dialErr := dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, fmt.Errorf("connect to public image host: %w", lastErr)
		}
		return nil, &moderationImagePolicyError{cause: errors.New("image host does not resolve to a public address")}
	}
}

func isPublicModerationImageIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" ||
		!addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range moderationBlockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Keep this list aligned with the repository's account-health SSRF policy.
// IsGlobalUnicast alone also accepts CGNAT and several documentation,
// benchmarking, translation, and special-purpose ranges.
var moderationBlockedPrefixes = []netip.Prefix{
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
	netip.MustParsePrefix("64:ff9b::/96"),
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

func prepareModerationImage(ctx context.Context, client *http.Client, input moderationImageInput, allowHTTPForTests bool) (preparedModerationImage, error) {
	source := strings.TrimSpace(input.URL)
	if source == "" {
		return preparedModerationImage{}, errors.New("image URL is empty")
	}
	if strings.HasPrefix(strings.ToLower(source), "data:") {
		canonical, err := canonicalizeModerationDataURL(source)
		if err != nil {
			return preparedModerationImage{}, err
		}
		return preparedModerationImage{URL: canonical, Detail: input.Detail}, nil
	}
	if len(source) > maxModerationImageURLBytes {
		return preparedModerationImage{}, errors.New("image URL is too long")
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return preparedModerationImage{}, errors.New("image URL is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return preparedModerationImage{}, errors.New("image URL scheme is unsupported")
	}
	if scheme == "http" && !allowHTTPForTests {
		return preparedModerationImage{}, errors.New("image URL must use HTTPS")
	}
	if parsed.Port() != "" && !allowHTTPForTests {
		port, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
			return preparedModerationImage{}, errors.New("image URL port is unsupported")
		}
	}
	if !allowHTTPForTests && net.ParseIP(parsed.Hostname()) != nil && !isPublicModerationImageIP(net.ParseIP(parsed.Hostname())) {
		return preparedModerationImage{}, errors.New("image URL host is not public")
	}
	if client == nil {
		return preparedModerationImage{}, errors.New("image client is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return preparedModerationImage{}, errors.New("image URL is invalid")
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/gif,image/webp")
	resp, err := client.Do(req)
	if err != nil {
		var policyErr *moderationImagePolicyError
		if errors.As(err, &policyErr) {
			return preparedModerationImage{}, &unsupportedModerationInputError{cause: policyErr}
		}
		return preparedModerationImage{}, &moderationImageFetchError{cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return preparedModerationImage{}, &moderationImageFetchError{statusCode: resp.StatusCode}
		}
		return preparedModerationImage{}, fmt.Errorf("image fetch returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxModerationImageBytes {
		return preparedModerationImage{}, errors.New("image is too large")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModerationImageBytes+1))
	if err != nil {
		return preparedModerationImage{}, &moderationImageFetchError{cause: err}
	}
	canonical, err := canonicalizeModerationImageBytes(raw)
	if err != nil {
		return preparedModerationImage{}, err
	}
	return preparedModerationImage{URL: canonical, Detail: input.Detail}, nil
}

func canonicalizeModerationDataURL(value string) (string, error) {
	if len(value) > maxModerationImageDataURLBytes {
		return "", errors.New("image data URL is too large")
	}
	comma := strings.IndexByte(value, ',')
	if comma <= len("data:") || comma == len(value)-1 {
		return "", errors.New("image data URL is invalid")
	}
	header := value[:comma]
	if !strings.EqualFold(header[:len("data:")], "data:") {
		return "", errors.New("image data URL is invalid")
	}
	metadata := strings.Split(header[len("data:"):], ";")
	if len(metadata) != 2 || !strings.Contains(metadata[1], "base64") || !strings.EqualFold(strings.TrimSpace(metadata[1]), "base64") {
		return "", errors.New("image data URL must use base64")
	}
	declaredMIME := strings.ToLower(strings.TrimSpace(metadata[0]))
	if !isAllowedModerationImageMIME(declaredMIME) {
		return "", errors.New("image data URL MIME type is unsupported")
	}
	encoded := value[comma+1:]
	if strings.TrimSpace(encoded) != encoded {
		return "", errors.New("image data URL base64 is invalid")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return "", errors.New("image data URL base64 is invalid")
	}
	if len(raw) > maxModerationImageBytes {
		return "", errors.New("image is too large")
	}
	return canonicalizeModerationImageBytes(raw)
}

func canonicalizeModerationImageBytes(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > maxModerationImageBytes {
		return "", errors.New("image is empty or too large")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", errors.New("image format is invalid")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxModerationImageDimension || config.Height > maxModerationImageDimension ||
		int64(config.Width)*int64(config.Height) > maxModerationImagePixels {
		return "", errors.New("image dimensions are unsupported")
	}
	format = strings.ToLower(format)
	mimeType, ok := moderationImageMIMEs[format]
	if !ok {
		return "", errors.New("image format is unsupported")
	}
	var decodeErr error
	switch format {
	case "jpeg":
		_, decodeErr = jpeg.Decode(bytes.NewReader(raw))
	case "png":
		_, decodeErr = png.Decode(bytes.NewReader(raw))
	case "gif":
		if err := validateModerationGIFStructure(raw); err != nil {
			return "", err
		}
		_, decodeErr = gif.DecodeAll(bytes.NewReader(raw))
	case "webp":
		_, decodeErr = webp.Decode(bytes.NewReader(raw))
	default:
		decodeErr = errors.New("unsupported decoder")
	}
	if decodeErr != nil {
		return "", errors.New("image data is incomplete or invalid")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func validateModerationGIFStructure(raw []byte) error {
	if len(raw) < 13 || (string(raw[:6]) != "GIF87a" && string(raw[:6]) != "GIF89a") {
		return errors.New("image data is incomplete or invalid")
	}
	pos := 13
	if raw[10]&0x80 != 0 {
		pos += 3 * (1 << ((raw[10] & 0x07) + 1))
	}
	skipSubBlocks := func() bool {
		for {
			if pos >= len(raw) {
				return false
			}
			size := int(raw[pos])
			pos++
			if size == 0 {
				return true
			}
			if size > len(raw)-pos {
				return false
			}
			pos += size
		}
	}
	frames := 0
	var totalPixels int64
	for pos < len(raw) {
		block := raw[pos]
		pos++
		switch block {
		case 0x21:
			if pos >= len(raw) {
				return errors.New("image data is incomplete or invalid")
			}
			pos++
			if !skipSubBlocks() {
				return errors.New("image data is incomplete or invalid")
			}
		case 0x2c:
			if pos+9 > len(raw) {
				return errors.New("image data is incomplete or invalid")
			}
			width := int64(binary.LittleEndian.Uint16(raw[pos+4 : pos+6]))
			height := int64(binary.LittleEndian.Uint16(raw[pos+6 : pos+8]))
			packed := raw[pos+8]
			pos += 9
			if width <= 0 || height <= 0 {
				return errors.New("image data is incomplete or invalid")
			}
			frames++
			totalPixels += width * height
			if frames > maxModerationGIFFrames || totalPixels > maxModerationGIFTotalPixels {
				return errors.New("animated GIF exceeds moderation resource limits")
			}
			if packed&0x80 != 0 {
				pos += 3 * (1 << ((packed & 0x07) + 1))
				if pos > len(raw) {
					return errors.New("image data is incomplete or invalid")
				}
			}
			if pos >= len(raw) {
				return errors.New("image data is incomplete or invalid")
			}
			pos++
			if !skipSubBlocks() {
				return errors.New("image data is incomplete or invalid")
			}
		case 0x3b:
			if frames == 0 {
				return errors.New("image data is incomplete or invalid")
			}
			return nil
		default:
			return errors.New("image data is incomplete or invalid")
		}
	}
	return errors.New("image data is incomplete or invalid")
}

func isAllowedModerationImageMIME(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
