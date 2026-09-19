package usage

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NormalizeObservedModel accepts a bounded, printable model identifier. Invalid
// observations stay unknown instead of being truncated into a different model.
func NormalizeObservedModel(model string) string {
	if !utf8.ValidString(model) {
		return ""
	}
	for _, char := range model {
		if unicode.IsControl(char) {
			return ""
		}
	}
	model = strings.TrimSpace(model)
	if len(model) > 256 {
		return ""
	}
	return model
}

type requestedModelContextKey struct{}

// WithRequestedModel records the original client model without an alias or
// routing fallback. An empty value clears any observation inherited from ctx.
func WithRequestedModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestedModelContextKey{}, NormalizeObservedModel(model))
}

// RequestedModelFromContext returns only an explicitly observed client model.
func RequestedModelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	model, _ := ctx.Value(requestedModelContextKey{}).(string)
	return NormalizeObservedModel(model)
}
