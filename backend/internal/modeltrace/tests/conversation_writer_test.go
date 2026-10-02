package modeltrace_test

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"testing"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestWriteConversationEvents(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")

	ctx, parent := tracer.Start(context.Background(), "model.request")
	modeltrace.WriteConversationEvents(ctx, tracer, []modeltrace.ChatEvent{
		{Name: "chat.user", MessageID: "u1", SessionID: "sess", TurnID: "t1", Content: "hi", Seq: 0},
		{Name: "chat.assistant", MessageID: "a1", SessionID: "sess", TurnID: "t1", Content: "yo", ParentMessageID: "u1", Seq: 1},
	})
	parent.End()

	spans := exporter.GetSpans()
	require.GreaterOrEqual(t, len(spans), 3)
	var names []string
	for _, span := range spans {
		names = append(names, span.Name)
	}
	require.Contains(t, names, "chat.user")
	require.Contains(t, names, "chat.assistant")
}
