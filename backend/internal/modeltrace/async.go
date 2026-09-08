package modeltrace

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/modeltrace/recording"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// AsyncExecutionMetadata identifies one background task or batch item. TaskID
// and ItemID are correlation identifiers, never inferred Langfuse Session IDs.
type AsyncExecutionMetadata struct {
	Identity    servermiddleware.ResolvedIdentity
	AccountID   int64
	TaskID      string
	ItemID      string
	Model       string
	Operation   string
	ContentType string
}

// AsyncExecution owns one background root/continuation and its generation pin.
type AsyncExecution struct {
	generation *GenerationSnapshot
	span       trace.Span
	recorder   *traceRecorder
	metadata   AsyncExecutionMetadata
	policy     capturePolicy
	endOnce    sync.Once
}

// StartAsyncExecution applies the persisted-continuation safety rule:
// fingerprint match continues the original trace; mismatch starts a new root
// linked to the submission; disabled tracing remains a no-op.
func (m *Manager) StartAsyncExecution(parent context.Context, continuation recording.TraceContinuation, metadata AsyncExecutionMetadata, input []byte) *AsyncExecution {
	generation := m.Acquire()
	if !generation.Enabled() {
		generation.Release()
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	cfg := generation.Config()
	tracer := generation.Tracer()
	spanContext, validParent := spanContextFromContinuation(continuation)
	matches := validParent && constantTimeFingerprintEqual(generation.Fingerprint(), continuation.GenerationFingerprint)

	options := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}
	if matches {
		parent = trace.ContextWithRemoteSpanContext(parent, spanContext)
	} else {
		options = append(options, trace.WithNewRoot())
		if validParent {
			options = append(options, trace.WithLinks(trace.Link{SpanContext: spanContext}))
		}
	}
	ctx, span := tracer.Start(parent, "model.async.execution", options...)
	policy := newCapturePolicy(cfg)
	recorder := newTraceRecorder(ctx, tracer, metadata.Identity, cfg.PromptMaxBytes, cfg.ResponseMaxBytes, policy, generation)
	recorder.ctx = recording.WithRecorder(ctx, recorder)

	attrs := []attribute.KeyValue{
		otlpString("langfuse.trace.name", "model.async.execution"),
		otlpString("langfuse.observation.input", captureModelContentWithType(input, len(input), cfg.PromptMaxBytes, metadata.ContentType, policy)),
		attribute.Bool("modeltrace.async.continuation_matched", matches),
	}
	traceCorrelation := map[string]string{}
	observationCorrelation := map[string]any{}
	if metadata.TaskID != "" {
		taskID := scrubURLsInString(metadata.TaskID, policy)
		attrs = append(attrs, otlpString("langfuse.trace.metadata.task_id", taskID))
		traceCorrelation["task_id"] = taskID
		observationCorrelation["task_id"] = taskID
	}
	if metadata.ItemID != "" {
		observationCorrelation["item_id"] = scrubURLsInString(metadata.ItemID, policy)
	}
	if metadata.AccountID > 0 {
		attrs = append(attrs, attribute.Int64("modeltrace.account.id", metadata.AccountID))
		observationCorrelation["account_id"] = metadata.AccountID
	}
	if metadata.Identity.UserID > 0 {
		attrs = append(attrs, otlpString("langfuse.user.id", strconv.FormatInt(metadata.Identity.UserID, 10)))
		observationCorrelation["user_id"] = metadata.Identity.UserID
	}
	if metadata.Identity.APIKeyID > 0 {
		attrs = append(attrs, attribute.Int64("langfuse.trace.metadata.api_key_id", metadata.Identity.APIKeyID))
		observationCorrelation["api_key_id"] = metadata.Identity.APIKeyID
	}
	if metadata.Identity.GroupID > 0 {
		attrs = append(attrs, attribute.Int64("langfuse.trace.metadata.group_id", metadata.Identity.GroupID))
		observationCorrelation["group_id"] = metadata.Identity.GroupID
	}
	if !matches && validParent {
		submissionTraceID := spanContext.TraceID().String()
		attrs = append(attrs, otlpString("langfuse.trace.metadata.submission_trace_id", submissionTraceID))
		traceCorrelation["submission_trace_id"] = submissionTraceID
		observationCorrelation["submission_trace_id"] = submissionTraceID
	}
	if metadata.Model != "" {
		attrs = append(attrs, otlpString("gen_ai.request.model", scrubURLsInString(metadata.Model, policy)))
	}
	if metadata.Operation != "" {
		attrs = append(attrs, otlpString("gen_ai.operation.name", metadata.Operation))
	}
	if len(traceCorrelation) > 0 {
		encoded, _ := json.Marshal(traceCorrelation)
		attrs = append(attrs, otlpString("langfuse.trace.metadata", string(encoded)))
	}
	if len(observationCorrelation) > 0 {
		encoded, _ := json.Marshal(observationCorrelation)
		attrs = append(attrs, otlpString("langfuse.observation.metadata", string(encoded)))
	}
	span.SetAttributes(attrs...)
	return &AsyncExecution{
		generation: generation,
		span:       span,
		recorder:   recorder,
		metadata:   metadata,
		policy:     policy,
	}
}

func (e *AsyncExecution) Context() context.Context {
	if e == nil || e.recorder == nil {
		return context.Background()
	}
	return e.recorder.ctx
}

func (e *AsyncExecution) End(status string, output []byte, err error) {
	if e == nil {
		return
	}
	e.endOnce.Do(func() {
		defer e.generation.Release()
		cfg := e.generation.Config()
		e.span.SetAttributes(
			otlpString("langfuse.observation.output", captureModelContent(output, len(output), cfg.ResponseMaxBytes, e.policy)),
			otlpString("modeltrace.async.status", status),
		)
		if err != nil {
			sanitized := e.policy.traceErrorMessage(err.Error())
			e.span.RecordError(errors.New(sanitized))
			e.span.SetStatus(codes.Error, sanitized)
		} else if status == "cancelled" {
			e.span.SetStatus(codes.Error, "async task cancelled")
		} else {
			e.span.SetStatus(codes.Ok, "")
		}
		e.recorder.FinishRequest()
		e.span.End()
	})
}

func spanContextFromContinuation(continuation recording.TraceContinuation) (trace.SpanContext, bool) {
	if !continuation.Valid() {
		return trace.SpanContext{}, false
	}
	traceID, err := trace.TraceIDFromHex(continuation.TraceID)
	if err != nil {
		return trace.SpanContext{}, false
	}
	spanID, err := trace.SpanIDFromHex(continuation.SpanID)
	if err != nil {
		return trace.SpanContext{}, false
	}
	state, err := trace.ParseTraceState(continuation.TraceState)
	if err != nil {
		return trace.SpanContext{}, false
	}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.TraceFlags(continuation.TraceFlags),
		TraceState: state,
		Remote:     true,
	})
	return spanContext, spanContext.IsValid()
}

func constantTimeFingerprintEqual(left, right string) bool {
	if len(left) != 64 || len(right) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
