package modeltrace

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkSanitizeStructuredContent compares the sanitizeStructuredContent
// cost for a large media-free Claude-Code-style body with sanitization on vs
// off. The off variant exercises the QW-1 lexer skip; the on variant pays the
// decode + walk + DeepEqual + re-encode. Size chosen to mirror a big
// tool-schema system prompt (~264KB).
func benchmarkMediaFreeBody() []byte {
	message := `{"type":"text","text":"You are a coding agent. Read the file and metadata before acting; the data source of truth is the repository. Avoid image generation unless asked."}`
	block := `{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("analyze the file metadata and data pipeline output. ", 300) + `"}]}`
	body := `{"model":"claude-sonnet","max_tokens":1024,"metadata":{"user_id":"u-1"},"system":[{"type":"text","text":"` + strings.Repeat("system prompt guidance with file/image/data words. ", 400) + `"}],"messages":[`
	for i := range 20 {
		if i > 0 {
			body += ","
		}
		body += block
		body += `,{"role":"assistant","content":` + message + `}`
	}
	body += `]}`
	return []byte(body)
}

var benchMediaFreeBody = benchmarkMediaFreeBody()

func BenchmarkSanitizeStructuredContent(b *testing.B) {
	b.Run("sanitization_on", func(b *testing.B) {
		b.SetBytes(int64(len(benchMediaFreeBody)))
		b.ReportAllocs()
		for range b.N {
			_ = sanitizeStructuredContent(benchMediaFreeBody, capturePolicy{})
		}
	})
	b.Run("sanitization_off", func(b *testing.B) {
		b.SetBytes(int64(len(benchMediaFreeBody)))
		b.ReportAllocs()
		for range b.N {
			_ = sanitizeStructuredContent(benchMediaFreeBody, capturePolicy{sanitizationDisabled: true})
		}
	})
}

// BenchmarkMayContainMedia isolates the QW-1 lexer cost.
func BenchmarkMayContainMedia(b *testing.B) {
	b.SetBytes(int64(len(benchMediaFreeBody)))
	b.ReportAllocs()
	for range b.N {
		if !mayContainMedia(benchMediaFreeBody) {
			continue
		}
		b.Fatal("expected media-free body to skip")
	}
}

var _ = fmt.Sprintf
