package modeltrace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Tests for model_tracing.sanitization_enabled=false policy gating.

func TestCapturePolicyDisabledSanitizationKeepsAuthorizationHeaderRaw(t *testing.T) {
	body := []byte(`{"model":"gpt-4","headers":{"Authorization":"Bearer super-secret-token","x-api-key":"another-secret"}}`)
	disabled := capturePolicy{sanitizationDisabled: true}
	captured := sanitizeStructuredContent(body, disabled)
	if string(captured) != string(body) {
		t.Fatalf("disabled sanitization must pass structured content through verbatim, got %s", captured)
	}

	// The same body through the common capture entry keeps the secret too.
	captured = []byte(captureModelContent(body, len(body), 4096, disabled))
	if string(captured) != string(body) {
		t.Fatalf("disabled sanitization must keep captureModelContent verbatim, got %s", captured)
	}

	// Unstructured text with a colon-delimited header is also untouched.
	text := "POST /v1/messages\nauthorization: Bearer plain-secret-42"
	if got := sanitizeUnstructuredText(text, disabled); got != text {
		t.Fatalf("disabled sanitization must pass unstructured text through verbatim, got %s", got)
	}
}

func TestCapturePolicyDisabledSanitizationKeepsURLCredentialsRaw(t *testing.T) {
	value := `https://user:pass@host.example/path?token=query-secret#fragment`
	disabled := capturePolicy{sanitizationDisabled: true}
	if got := scrubURLsInString(value, disabled); got != value {
		t.Fatalf("disabled sanitization must keep URL credentials verbatim, got %s", got)
	}

	body := []byte(`{"download_url":"https://user:pass@cdn.example/file?sig=s3cret"}`)
	captured := sanitizeStructuredContent(body, disabled)
	if string(captured) != string(body) {
		t.Fatalf("disabled sanitization must keep URL-bearing JSON verbatim, got %s", captured)
	}

	// Form bodies with sanitization off are only bounded, not redacted: the
	// re-encoded body still carries the URL credentials verbatim.
	form := "url=https%3A%2F%2Fuser%3Apass%40host.example%2Fpath%3Ftoken%3Dform-secret&model=gpt-4"
	got := sanitizeFormURLEncoded([]byte(form), len(form), 4096, disabled)
	if !strings.Contains(got, "user%3Apass%40host.example") || !strings.Contains(got, "token%3Dform-secret") {
		t.Fatalf("disabled sanitization must keep form URL credentials, got %s", got)
	}
	if strings.Contains(got, redactedValue) {
		t.Fatalf("disabled sanitization must not redact form secrets, got %s", got)
	}

	errMsg := "Get https://user:err-secret@upstream/?api_key=err-canary: failed"
	if got := (capturePolicy{sanitizationDisabled: true}).traceErrorMessage(errMsg); got != errMsg {
		t.Fatalf("disabled sanitization must keep error verbatim, got %s", got)
	}
}

func TestCapturePolicyDisabledSanitizationKeepsFingerprintAndTruncation(t *testing.T) {
	mediaPayload := "data:image/png;base64,AAAA"
	body := []byte(`{"type":"image_url","image_url":{"url":"` + mediaPayload + `"}}`)
	disabled := capturePolicy{sanitizationDisabled: true, mediaMaxBytes: 1024, captureMediaContent: false}
	captured := captureModelContent(body, len(body), 4096, disabled)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(captured), &parsed); err != nil {
		t.Fatalf("media summary must stay valid JSON: %v", err)
	}
	// The media payload key is still summarized to a sha256 fingerprint —
	// disabling sanitization does not disable capture.
	nested, ok := parsed["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("expected image_url summary, got %s", captured)
	}
	fp, _ := nested["fingerprint"].(string)
	if len(fp) != len("sha256:")+64 || !strings.HasPrefix(fp, "sha256:") {
		t.Fatalf("expected sha256 fingerprint to survive, got %v", nested)
	}
	if _, leaked := nested["url"]; leaked {
		t.Fatalf("media summarization (capture) must still apply, got %s", captured)
	}

	// Secret-keyed values are no longer redacted when sanitization is off,
	// but capture-bound summarization of media keys still applies.
	secretBody := []byte(`{"api_key":"super-secret-value","type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`)
	secretCaptured := captureModelContent(secretBody, len(secretBody), 4096, disabled)
	if !strings.Contains(secretCaptured, "super-secret-value") {
		t.Fatalf("disabled sanitization must keep secret-keyed values, got %s", secretCaptured)
	}

	// Truncation logic is unaffected by the sanitization flag.
	raw := []byte(`{"k":"` + strings.Repeat("x", 8192) + `"}`)
	truncated := captureModelContent(raw, len(raw), 1024, disabled)
	if len(truncated) > 1024+128 {
		t.Fatalf("capture bound must still apply: %d bytes", len(truncated))
	}
	if !strings.Contains(truncated, "[truncated:original_bytes=") {
		t.Fatalf("expected truncation marker, got tail: %s", truncated[len(truncated)-96:])
	}
}

func TestCapturePolicyDefaultKeepsSanitization(t *testing.T) {
	// Zero-value policy must behave exactly like the pre-change behavior:
	// every credential is scrubbed.
	zero := capturePolicy{}
	body := []byte(`{"Authorization":"Bearer super-secret-token","download_url":"https://user:pass@host.example/p?token=s3cret"}`)
	captured := string(sanitizeStructuredContent(body, zero))
	if strings.Contains(captured, "super-secret-token") || strings.Contains(captured, "pass@") || strings.Contains(captured, "s3cret") {
		t.Fatalf("zero-value policy must scrub credentials, got %s", captured)
	}
	if !strings.Contains(captured, redactedValue) || !strings.Contains(captured, "https://host.example/p") {
		t.Fatalf("zero-value policy must produce redacted URL, got %s", captured)
	}

	errMsg := "Get https://user:err-secret@upstream/: failed"
	if got := zero.traceErrorMessage(errMsg); strings.Contains(got, "err-secret") {
		t.Fatalf("zero-value policy must scrub error URLs, got %s", got)
	}
}

func TestNewCapturePolicyFromConfig(t *testing.T) {
	// nil pointer (zero-value config, legacy snapshots) keeps sanitization on.
	if p := newCapturePolicy(config.ModelTracingConfig{}); p.sanitizationDisabled {
		t.Fatalf("nil SanitizationEnabled must keep sanitization on")
	}
	explicitOn := true
	on := newCapturePolicy(config.ModelTracingConfig{SanitizationEnabled: &explicitOn})
	if on.sanitizationDisabled {
		t.Fatalf("explicit true must keep sanitization on")
	}
	explicitOff := false
	off := newCapturePolicy(config.ModelTracingConfig{MediaMaxBytes: 4096, CaptureMediaContent: true, SanitizationEnabled: &explicitOff})
	if !off.sanitizationDisabled {
		t.Fatalf("explicit false must disable sanitization")
	}
	if off.mediaMaxBytes != 4096 || !off.captureMediaContent {
		t.Fatalf("other policy fields must flow through unchanged: %+v", off)
	}
}

// Enforce the QW-1 contract directly: with sanitization disabled, a
// media-free root JSON body returns byte-identical raw, and a media-bearing
// body still goes through the walk (media data: payloads always change).
func TestQW1SkipContract(t *testing.T) {
	disabled := capturePolicy{sanitizationDisabled: true}
	for _, fx := range goldenFixtures() {
		if len(fx.body) == 0 {
			continue
		}
		skip := !mayContainMedia(fx.body)
		got := sanitizeStructuredContent(fx.body, disabled)
		if skip {
			if !bytes.Equal(got, fx.body) {
				t.Errorf("skip contract violated for %s: output differs from raw", fx.name)
			}
			continue
		}
		if bytes.Contains(fx.body, []byte("data:image")) && bytes.Equal(got, fx.body) {
			t.Errorf("media payload %s must be summarized even with sanitization off", fx.name)
		}
	}
}

func TestQW2ValidUTF8FastPath(t *testing.T) {
	valid := []byte(`{"k":"v"}`)
	if !utf8.Valid(valid) {
		t.Fatalf("test fixture must be valid utf8")
	}
	invalid := []byte{0xff, 0xfe}
	if utf8.Valid(invalid) {
		t.Fatalf("test fixture must be invalid utf8")
	}
	for _, policy := range []capturePolicy{{}, {sanitizationDisabled: true}} {
		gotValid := captureModelContent(valid, len(valid), 4096, policy)
		if gotValid != string(valid) {
			t.Errorf("valid utf8 media-free body must round-trip verbatim with policy %+v, got %q", policy, gotValid)
		}
	}
	gotInvalid := captureModelContent(invalid, len(invalid), 4096, capturePolicy{})
	if !strings.ContainsRune(gotInvalid, '\uFFFD') {
		t.Errorf("invalid utf8 must still be normalized, got %q", gotInvalid)
	}
}

// QW-1 lexer unit checks: conservative superset over the media branches.
func TestMayContainMediaLexer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "plain_object", in: `{"model":"gpt-4","content":"hi"}`, want: false},
		{name: "media_words_in_values", in: `{"text":"file image data source"}`, want: false},
		{name: "media_key", in: `{"image_url":"https://x/y.png"}`, want: true},
		{name: "uppercase_media_key", in: `{"IMAGE_URL":"x"}`, want: true},
		{name: "data_value", in: `{"source":"base64"}`, want: true},
		{name: "exact_data_value", in: `{"t":"data"}`, want: true},
		{name: "data_colon_value", in: `{"t":"data:image/png;base64,AA"}`, want: true},
		{name: "escaped_key", in: `{"\u0069mage_url":"x"}`, want: true},
		{name: "escaped_value", in: `{"c":"\u0064ata:text/plain;base64,AAAA"}`, want: true},
		{name: "escaped_quote_then_key", in: `{"t":"say \"hi\"","image_url":"x"}`, want: true},
		{name: "escaped_escape_not_unicode", in: `{"t":"backslash\\u0041","other":"x"}`, want: false},
		{name: "parent_type_words", in: `{"type":"image","data":"AAAA"}`, want: true},
		{name: "metadata_word_only", in: `{"metadata":"user"}`, want: false},
		{name: "file_in_path_value", in: `{"path":"/usr/local/file.txt"}`, want: false},
		{name: "sse_not_json", in: "data: {\"delta\":\"x\"}\ndata: [DONE]", want: false},
		{name: "empty", in: "", want: false},
		{name: "long_token_over_128", in: `{"` + strings.Repeat("a", 200) + `":"x"}`, want: false},
	}
	for _, tc := range cases {
		if got := mayContainMedia([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: mayContainMedia=%v want %v", tc.name, got, tc.want)
		}
	}
}
