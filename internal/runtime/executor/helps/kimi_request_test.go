package helps

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeKimiProRequestForcedTools(t *testing.T) {
	for _, tc := range []struct {
		name, choice string
		format       sdktranslator.Format
	}{
		{"chat required", `"required"`, sdktranslator.FormatOpenAI},
		{"chat named", `{"type":"function","function":{"name":"lookup"}}`, sdktranslator.FormatOpenAI},
		{"messages any", `{"type":"any"}`, sdktranslator.FormatClaude},
		{"messages named", `{"type":"tool","name":"lookup"}`, sdktranslator.FormatClaude},
		{"responses named", `{"type":"function","name":"lookup"}`, sdktranslator.FormatOpenAIResponse},
		{"codex required", `"required"`, sdktranslator.FormatCodex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, initialThinking := range []string{"", `,"thinking":{"type":"enabled","budget_tokens":2048}`} {
				body := []byte(`{"temperature":0.2,"tool_choice":` + tc.choice + `,"reasoning_effort":"high","reasoning":{"effort":"high","summary":"auto"},"output_config":{"effort":"high","format":{"type":"json_schema"}},"tools":[{"name":"lookup"}],"messages":[{"role":"user","content":"hello"}]` + initialThinking + `}`)
				got := NormalizeKimiProRequest(body, "kimi-k3.1-pro[1m](high)", tc.format)
				if gjson.GetBytes(got, "temperature").Exists() || gjson.GetBytes(got, "tool_choice").Raw != tc.choice {
					t.Fatalf("sampling or tool selection changed incorrectly: %s", got)
				}
				if tc.format == sdktranslator.FormatCodex || tc.format == sdktranslator.FormatOpenAIResponse {
					if gjson.GetBytes(got, "reasoning.effort").String() != "none" || gjson.GetBytes(got, "thinking").Exists() {
						t.Fatalf("Responses reasoning not disabled: %s", got)
					}
				} else if gjson.GetBytes(got, "thinking").Raw != `{"type":"disabled"}` || gjson.GetBytes(got, "reasoning.effort").Exists() {
					t.Fatalf("thinking not disabled: %s", got)
				}
				for _, path := range []string{"tools", "messages", "reasoning.summary", "output_config.format"} {
					if gjson.GetBytes(got, path).Raw != gjson.GetBytes(body, path).Raw {
						t.Fatalf("%s not preserved: %s", path, got)
					}
				}
				for _, path := range []string{"reasoning_effort", "output_config.effort"} {
					if gjson.GetBytes(got, path).Exists() {
						t.Fatalf("stale %s: %s", path, got)
					}
				}
				if next := NormalizeKimiProRequest(got, "k3.1-pro", tc.format); string(next) != string(got) {
					t.Fatalf("not idempotent: %s", next)
				}
			}
		})
	}
}

func TestNormalizeKimiProRequestPreservesUnrelatedRequests(t *testing.T) {
	for _, temperature := range []string{"0", "0.6", "1", "2"} {
		got := NormalizeKimiProRequest([]byte(`{"temperature":`+temperature+`,"tool_choice":"auto","thinking":{"type":"enabled","effort":"high"}}`), "k3.1-pro", sdktranslator.FormatOpenAI)
		if gjson.GetBytes(got, "temperature").Exists() || gjson.GetBytes(got, "thinking.effort").String() != "high" || gjson.GetBytes(got, "thinking.type").String() != "enabled" {
			t.Errorf("automatic tool choice must retain thinking and use default sampling: %s", got)
		}
	}
	for _, model := range []string{"k3.1-pro", "kimi-k3.1-pro", "k3.1-pro-highspeed", "kimi-k3.1-pro-highspeed(high)"} {
		got := NormalizeKimiProRequest([]byte(`{"temperature":0,"tool_choice":"required"}`), model, sdktranslator.FormatOpenAI)
		if gjson.GetBytes(got, "temperature").Exists() || gjson.GetBytes(got, "thinking.type").String() != "disabled" {
			t.Errorf("model alias %s missed compatibility: %s", model, got)
		}
	}
	for _, model := range []string{"k3", "kimi-k2.7-code", "claude-sonnet-4-5", "k3.1-pro-preview", "my-k3.1-pro"} {
		body := []byte(`{"temperature":0.2,"tool_choice":"required","thinking":{"type":"enabled"}}`)
		if got := NormalizeKimiProRequest(body, model, sdktranslator.FormatOpenAI); string(got) != string(body) {
			t.Errorf("changed unrelated model %s: %s", model, got)
		}
	}
	for _, choice := range []string{`"auto"`, `"none"`, `{"type":"auto"}`, `{"type":"none"}`, `null`} {
		body := []byte(`{"tool_choice":` + choice + `,"thinking":{"type":"enabled","effort":"high"}}`)
		if got := NormalizeKimiProRequest(body, "k3.1-pro", sdktranslator.FormatOpenAI); string(got) != string(body) {
			t.Errorf("changed ordinary tool choice: %s", got)
		}
	}
	for _, body := range []string{`{}`, `{"temperature":null}`, `{"temperature":"bad"}`, `not json`} {
		if got := NormalizeKimiProRequest([]byte(body), "k3.1-pro", sdktranslator.FormatOpenAI); string(got) != body {
			t.Errorf("changed omitted/invalid temperature: %s", got)
		}
	}
}
