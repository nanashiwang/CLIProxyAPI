package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestContextWithRequestedModelAliasIncludesReasoningEffort(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
			cliproxyexecutor.ReasoningEffortMetadataKey: "medium",
			cliproxyexecutor.ServiceTierMetadataKey:     "auto",
			cliproxyexecutor.GenerateMetadataKey:        false,
		},
	}, "fallback-model")

	if got := coreusage.RequestedModelAliasFromContext(ctx); got != "client-model" {
		t.Fatalf("requested model alias = %q, want %q", got, "client-model")
	}
	if got := coreusage.ReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", got, "medium")
	}
	gotServiceTier := coreusage.ServiceTierFromContext(ctx)
	if gotServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", gotServiceTier, "auto")
	}
	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}

func TestContextWithRequestedModelAliasDefaultsGenerateTrue(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); !got {
		t.Fatalf("generate = %v, want true", got)
	}
}

func TestContextWithRequestedModelAliasPreservesExistingGenerateFalse(t *testing.T) {
	ctx := coreusage.WithGenerate(context.Background(), false)
	ctx = contextWithRequestedModelAlias(ctx, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}

func TestRequestedModelObservationNeverUsesRoutingFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts cliproxyexecutor.Options
		want string
	}{
		{"metadata before translation", cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "client-alias"}, OriginalRequest: []byte(`{"model":"other"}`)}, "client-alias"},
		{"original payload", cliproxyexecutor.Options{OriginalRequest: []byte(`{"model":"client-original"}`)}, "client-original"},
		{"unknown SDK request", cliproxyexecutor.Options{}, ""},
		{"nested tool model is not request model", cliproxyexecutor.Options{OriginalRequest: []byte(`{"tools":[{"model":"not-the-request"}]}`)}, ""},
		{"invalid type", cliproxyexecutor.Options{OriginalRequest: []byte(`{"model":123}`)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for attempt := 0; attempt < 3; attempt++ {
				opts := tc.opts
				for pass := 0; pass < attempt; pass++ {
					opts = ensureRequestedModelMetadata(opts, "upstream-route-model")
				}
				ctx := contextWithRequestedModelAlias(context.Background(), opts, "upstream-route-model")
				if got := coreusage.RequestedModelFromContext(ctx); got != tc.want {
					t.Fatalf("after %d fallback passes: requested model = %q, want %q", attempt, got, tc.want)
				}
				if tc.want == "" && coreusage.RequestedModelAliasFromContext(ctx) != "upstream-route-model" {
					t.Fatal("legacy alias fallback changed")
				}
			}
		})
	}
}
