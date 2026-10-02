package modeltrace

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// otlpString normalizes every dynamic string before it reaches the OTel SDK.
// The SDK may otherwise drop attributes containing invalid UTF-8, making the
// exported trace incomplete even when the surrounding OTLP batch succeeds.
func otlpString(key, value string) attribute.KeyValue {
	return attribute.String(key, strings.ToValidUTF8(value, "\uFFFD"))
}

func otlpStringSlice(key string, values []string) attribute.KeyValue {
	normalized := make([]string, len(values))
	for i, value := range values {
		normalized[i] = strings.ToValidUTF8(value, "\uFFFD")
	}
	return attribute.StringSlice(key, normalized)
}
