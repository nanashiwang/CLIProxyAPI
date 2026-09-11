package registry

import (
	"encoding/json"
	"testing"
)

func TestSecondBatchModelsPreserveExistingRoutes(t *testing.T) {
	for name, tc := range map[string]struct {
		models []*ModelInfo
		ids    []string
	}{
		"claude":      {GetClaudeModels(), []string{"claude-fable-5-1"}},
		"gemini":      {GetGeminiModels(), []string{"gemini-3.7-flash", "gemini-3.8-flash"}},
		"vertex":      {GetGeminiVertexModels(), []string{"gemini-3.7-flash", "gemini-3.8-flash"}},
		"aistudio":    {GetAIStudioModels(), []string{"gemini-3.7-flash", "gemini-3.8-flash"}},
		"codex-plus":  {GetCodexPlusModels(), []string{"gpt-6-astra", "gpt-5.4", "gpt-5.4-mini"}},
		"codex-pro":   {GetCodexProModels(), []string{"gpt-6-astra", "gpt-5.4", "gpt-5.4-mini"}},
		"codex-free":  {GetCodexFreeModels(), []string{"gpt-5.4-mini"}},
		"antigravity": {GetAntigravityModels(), []string{"gemini-3.8-flash-high", "gemini-3-flash-agent", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low"}},
	} {
		t.Run(name, func(t *testing.T) {
			ids := make(map[string]bool)
			for _, m := range tc.models {
				if m != nil {
					if ids[m.ID] {
						t.Fatalf("duplicate model %s", m.ID)
					}
					ids[m.ID] = true
				}
			}
			for _, id := range tc.ids {
				if !ids[id] {
					t.Errorf("missing model %s", id)
				}
			}
		})
	}
}

func TestCompatibilityCatalogKeepsOnlyExplicitLegacyRoutes(t *testing.T) {
	current := &staticModelsJSON{CodexPlus: []*ModelInfo{{ID: "gpt-5.4", DisplayName: "old"}, {ID: "removed-other"}}, Antigravity: []*ModelInfo{{ID: "gemini-3-flash-agent"}}}
	incoming := &staticModelsJSON{CodexPlus: []*ModelInfo{{ID: "gpt-6-astra"}}}
	retainCompatibilityCatalog(incoming, current)
	if len(incoming.CodexPlus) != 2 || incoming.CodexPlus[1].ID != "gpt-5.4" || len(incoming.Antigravity) != 1 {
		t.Fatal("compatibility routes not retained")
	}
	incoming.CodexPlus[1].DisplayName = "copy"
	if current.CodexPlus[0].DisplayName != "old" {
		t.Fatal("retained model aliases source")
	}
	retainCompatibilityCatalog(incoming, current)
	if len(incoming.CodexPlus) != 2 {
		t.Fatal("duplicate compatibility route")
	}
	replacement := &ModelInfo{ID: "gpt-5.4", DisplayName: "remote"}
	if got := retainCompatibilityModels([]*ModelInfo{replacement}, current.CodexPlus); len(got) != 1 || got[0].DisplayName != "remote" {
		t.Fatal("remote definition must win")
	}
}

func TestCompatibilityClientModelsRetainOldSlugsWithoutDuplicates(t *testing.T) {
	current := []byte(`{"models":[{"slug":"gpt-5.4","display_name":"old"},{"slug":"removed-other"}]}`)
	remote := []byte(`{"models":[{"slug":"gpt-6-astra"}]}`)
	got := retainCompatibilityClientModels(remote, current)
	var payload codexClientModelsPayload
	if json.Unmarshal(got, &payload) != nil || len(payload.Models) != 2 || payload.Models[1]["slug"] != "gpt-5.4" {
		t.Fatal("old slug was not retained")
	}
	if again := retainCompatibilityClientModels(got, current); string(again) != string(got) {
		t.Fatal("compatibility merge was not idempotent")
	}
}
