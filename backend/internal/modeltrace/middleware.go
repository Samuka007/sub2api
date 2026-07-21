package modeltrace

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	rootSpanName       = "model.request"
	generationSpanName = "generation"
)

// Middleware starts a root model-request Trace for authenticated gateway
// requests and a Generation child span capturing client input/output. Install
// after APIKeyAuth so identity is available. Pure pass-through when disabled.
func (m *Manager) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if m == nil || m.provider == nil {
			c.Next()
			return
		}
		ctx, span := m.tracer.Start(c.Request.Context(), rootSpanName,
			trace.WithSpanKind(trace.SpanKindServer),
		)
		c.Request = c.Request.WithContext(ctx)
		defer span.End()

		limit := m.cfg.PromptMaxBytes
		var clientInput []byte
		if limit > 0 && c.Request.Body != nil {
			raw, err := io.ReadAll(io.LimitReader(c.Request.Body, int64(limit)))
			if err == nil {
				clientInput = raw
			}
			c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(clientInput), c.Request.Body))
		}

		recorder := &responseRecorder{ResponseWriter: c.Writer, limit: m.cfg.ResponseMaxBytes}
		c.Writer = recorder

		start := time.Now()
		c.Next()
		_ = start

		status := c.Writer.Status()
		attrs := []attribute.KeyValue{
			attribute.String("langfuse.trace.name", rootSpanName),
			attribute.String("langfuse.observation.input", string(clientInput)),
			attribute.String("langfuse.observation.output", string(recorder.bytes())),
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("url.path", c.Request.URL.Path),
			attribute.Int64("http.response.status_code", int64(status)),
		}
		if reqID := clientRequestID(c); reqID != "" {
			attrs = append(attrs, attribute.String("langfuse.trace.metadata.request_id", reqID))
		}
		if uid, ok := authSubjectID(c); ok {
			attrs = append(attrs, attribute.String("langfuse.user.id", strconv.FormatInt(uid, 10)))
		}
		if apiKey, ok := middleware.GetAPIKeyFromContext(c); ok {
			attrs = append(attrs, attribute.Int64("langfuse.trace.metadata.api_key_id", apiKey.ID))
			if apiKey.GroupID != nil {
				attrs = append(attrs, attribute.Int64("langfuse.trace.metadata.group_id", *apiKey.GroupID))
			}
		}
		if session := extractSession(clientInput, c); session != "" {
			attrs = append(attrs, attribute.String("langfuse.session.id", session))
		}
		span.SetAttributes(attrs...)
		if status >= 500 {
			span.SetStatus(codes.Error, httpStatusText(status))
		} else {
			span.SetStatus(codes.Ok, "")
		}

		// Generation child span (tracer bullet; upstream-attempt split is task 4.1).
		genAttrs := []attribute.KeyValue{
			attribute.String("langfuse.observation.type", "generation"),
			attribute.String("langfuse.observation.model.name", gjson.GetBytes(clientInput, "model").String()),
			attribute.String("langfuse.observation.input", string(clientInput)),
			attribute.String("langfuse.observation.output", string(recorder.bytes())),
			attribute.Int64("http.response.status_code", int64(status)),
		}
		if reqID := clientRequestID(c); reqID != "" {
			genAttrs = append(genAttrs, attribute.String("langfuse.observation.metadata.request_id", reqID))
		}
		if apiKey, ok := middleware.GetAPIKeyFromContext(c); ok {
			genAttrs = append(genAttrs, attribute.Int64("langfuse.observation.metadata.api_key_id", apiKey.ID))
			if apiKey.GroupID != nil {
				genAttrs = append(genAttrs, attribute.Int64("langfuse.observation.metadata.group_id", *apiKey.GroupID))
			}
		}
		_, genSpan := m.tracer.Start(ctx, generationSpanName,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(genAttrs...),
		)
		genSpan.End()
	}
}

type responseRecorder struct {
	gin.ResponseWriter
	limit int
	buf   bytes.Buffer
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	if r.limit > 0 && r.buf.Len() < r.limit {
		remaining := r.limit - r.buf.Len()
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = r.buf.Write(p[:remaining])
	}
	return n, err
}

func (r *responseRecorder) bytes() []byte { return r.buf.Bytes() }

func clientRequestID(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("X-Client-Request-ID")); v != "" {
		return v
	}
	return ""
}

func authSubjectID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		return 0, false
	}
	return subject.UserID, true
}

// extractSession implements design §12 allowlist: only explicit session_id,
// conversation_id, metadata.session_id and Grok x-grok-conv-id header.
func extractSession(body []byte, c *gin.Context) string {
	if len(body) > 0 {
		for _, key := range []string{"session_id", "conversation_id"} {
			if v := gjson.GetBytes(body, key); v.Exists() && v.Type == gjson.String {
				if s := strings.TrimSpace(v.String()); s != "" {
					return s
				}
			}
		}
		if v := gjson.GetBytes(body, "metadata.session_id"); v.Exists() && v.Type == gjson.String {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	if v := strings.TrimSpace(c.GetHeader("x-grok-conv-id")); v != "" {
		return v
	}
	return ""
}

func httpStatusText(code int) string {
	switch {
	case code >= 500:
		return "server_error"
	case code >= 400:
		return "client_error"
	default:
		return ""
	}
}
