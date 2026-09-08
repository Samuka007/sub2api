package modeltrace

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden-master harness for the modeltrace quick wins (QW-1 media prescan,
// QW-2 UTF-8 fast path). The red lines being enforced:
//   - sanitization ON: output must be byte-identical to the pre-QW code.
//   - sanitization OFF + media present: output must be byte-identical.
//   - sanitization OFF + no media: output equals raw (the QW-1 skip).
//
// Baselines are recorded with `go test ./internal/modeltrace/ -run
// TestGoldenMasterSanitize -golden-write` on the pre-QW code; subsequent runs
// diff byte-exactly against the recorded file.

var goldenWrite = flag.Bool("golden-write", false, "write golden master file instead of comparing")

type goldenFixture struct {
	name  string
	body  []byte
	limit int
}

type goldenPolicyVariant struct {
	name   string
	policy capturePolicy
}

func goldenFixtures() []goldenFixture {
	big := strings.Repeat("x", 4096)
	return []goldenFixture{
		{
			name:  "media_free_root_json_with_media_words",
			body:  []byte(`{"model":"gpt-4","metadata":{"user":"metadata"},"text":"the file with image and data words","other":"source of data"}`),
			limit: 1 << 20,
		},
		{
			name:  "media_free_root_json_pure",
			body:  []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello world"}]}`),
			limit: 1 << 20,
		},
		{
			name:  "anthropic_source_base64_block",
			body:  []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`),
			limit: 1 << 20,
		},
		{
			name:  "responses_input_image",
			body:  []byte(`{"model":"gpt-4o","input":[{"type":"input_image","image_url":"https://user:pass@host/p?sig=x"}]}`),
			limit: 1 << 20,
		},
		{
			name:  "escaped_key_image_url",
			body:  []byte(`{"model":"gpt-4","\u0069mage_url":"https://host/x.png"}`),
			limit: 1 << 20,
		},
		{
			name:  "escaped_value_data_prefix",
			body:  []byte(`{"c":"\u0064ata:text/plain;base64,AAAA"}`),
			limit: 1 << 20,
		},
		{
			name:  "escaped_quote_then_media_key",
			body:  []byte(`{"t":"say \"hi\"","image_url":"https://host/x.png"}`),
			limit: 1 << 20,
		},
		{
			name:  "uppercase_image_url_key",
			body:  []byte(`{"IMAGE_URL":"https://host/x.png"}`),
			limit: 1 << 20,
		},
		{
			name:  "value_exactly_data",
			body:  []byte(`{"t":"data","other":"file"}`),
			limit: 1 << 20,
		},
		{
			name:  "sse_nonjson_with_data_lines",
			body:  []byte("data: {\"delta\":\"hello\"}\n\ndata: [DONE]\n"),
			limit: 1 << 20,
		},
		{
			name:  "invalid_utf8_with_media",
			body:  []byte{'{', '"', 'i', 'm', 'a', 'g', 'e', '_', 'u', 'r', 'l', '"', ':', '"', 0xff, 0xfe, '"', '}'},
			limit: 1 << 20,
		},
		{
			name:  "invalid_utf8_media_free",
			body:  []byte{'{', '"', 'k', '"', ':', '"', 0xff, 0xfe, '"', '}'},
			limit: 1 << 20,
		},
		{
			name:  "truncated_json_over_limit",
			body:  []byte(`{"model":"gpt-4","payload":"` + big),
			limit: 4096,
		},
		{
			name:  "valid_json_then_garbage",
			body:  []byte(`{"model":"gpt-4"} trailing garbage`),
			limit: 1 << 20,
		},
		{
			name:  "bom_prefixed_json",
			body:  append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"model":"gpt-4"}`)...),
			limit: 1 << 20,
		},
		{
			name:  "empty_body",
			body:  []byte{},
			limit: 1 << 20,
		},
		{
			name:  "secret_key_and_url",
			body:  []byte(`{"Authorization":"Bearer super-secret-token","download_url":"https://user:pass@host/p?token=s3cret"}`),
			limit: 1 << 20,
		},
		{
			name:  "media_url_credentials_non_media_key",
			body:  []byte(`{"download_url":"https://user:pass@host/file?sig=s3cret"}`),
			limit: 1 << 20,
		},
	}
}

func goldenPolicyVariants() []goldenPolicyVariant {
	return []goldenPolicyVariant{
		{name: "sanitization_on", policy: capturePolicy{}},
		{name: "sanitization_off", policy: capturePolicy{sanitizationDisabled: true}},
	}
}

type goldenRecord struct {
	Fixture string `json:"fixture"`
	Policy  string `json:"policy"`
	Output  string `json:"output"`
}

func goldenRecords() []goldenRecord {
	records := make([]goldenRecord, 0, len(goldenFixtures())*2)
	for _, fx := range goldenFixtures() {
		for _, pv := range goldenPolicyVariants() {
			got := captureModelContent(fx.body, len(fx.body), fx.limit, pv.policy)
			records = append(records, goldenRecord{Fixture: fx.name, Policy: pv.name, Output: got})
		}
	}
	return records
}

const goldenPath = "testdata/capture_quickwins_golden.json"

func TestGoldenMasterSanitizeStructuredContent(t *testing.T) {
	records := goldenRecords()
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	if *goldenWrite {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, encoded, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden written: %s (%d records)", goldenPath, len(records))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (record it on the baseline code with -golden-write): %v", err)
	}
	var baseline []goldenRecord
	if err := json.Unmarshal(want, &baseline); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(baseline) != len(records) {
		t.Fatalf("golden record count drift: %d baseline vs %d current", len(baseline), len(records))
	}
	failures := 0
	for i, rec := range baseline {
		cur := records[i]
		if rec.Fixture != cur.Fixture || rec.Policy != cur.Policy {
			t.Fatalf("record %d ordering drift: %s/%s vs %s/%s", i, rec.Fixture, rec.Policy, cur.Fixture, cur.Policy)
		}
		if !bytes.Equal([]byte(rec.Output), []byte(cur.Output)) {
			failures++
			t.Errorf("BYTE MISMATCH %s/%s:\n  want(%d): %q\n  got (%d): %q",
				rec.Fixture, rec.Policy, len(rec.Output), truncateForLog(rec.Output), len(cur.Output), truncateForLog(cur.Output))
		}
	}
	if failures > 0 {
		t.Fatalf("%d golden-master byte mismatches", failures)
	}
}

func truncateForLog(s string) string {
	if len(s) > 240 {
		return s[:120] + "..." + s[len(s)-120:]
	}
	return s
}
