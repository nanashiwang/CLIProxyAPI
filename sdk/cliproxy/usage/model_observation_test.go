package usage

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeObservedModel(t *testing.T) {
	for _, tt := range []struct{ value, want string }{
		{" gpt-5.4 ", "gpt-5.4"},
		{strings.Repeat("x", 256), strings.Repeat("x", 256)},
		{strings.Repeat("x", 257), ""},
		{strings.Repeat("模", 86), ""},
		{"gpt\nmodel", ""}, {"\tgpt", ""}, {"gpt\u007f", ""},
		{"bad\xff", ""}, {"", ""},
	} {
		if got := NormalizeObservedModel(tt.value); got != tt.want {
			t.Errorf("NormalizeObservedModel(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestRequestedModelNeverFallsBackAndCanBeCleared(t *testing.T) {
	ctx := WithRequestedModelAlias(context.Background(), "routing-alias")
	if got := RequestedModelFromContext(ctx); got != "" {
		t.Fatalf("alias leaked into original model: %q", got)
	}
	ctx = WithRequestedModel(ctx, " original ")
	if got := RequestedModelFromContext(ctx); got != "original" {
		t.Fatalf("model = %q", got)
	}
	if got := RequestedModelFromContext(WithRequestedModel(ctx, "")); got != "" {
		t.Fatalf("inherited model was not cleared: %q", got)
	}
}
