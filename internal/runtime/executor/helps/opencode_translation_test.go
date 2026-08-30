package helps

import (
	"context"
	"encoding/json"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenCodeResponsesEnvelope(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
	got := NormalizeOpenCodeResponsesEnvelope(raw)
	if gjson.GetBytes(got, "type").String() != "response.completed" {
		t.Fatalf("type = %q", gjson.GetBytes(got, "type").String())
	}
	if gjson.GetBytes(got, "response.id").String() != "resp_1" {
		t.Fatalf("response.id = %q", gjson.GetBytes(got, "response.id").String())
	}

	wrapped := []byte(`{"type":"response.incomplete","response":{"status":"incomplete"}}`)
	if got := NormalizeOpenCodeResponsesEnvelope(wrapped); string(got) != string(wrapped) {
		t.Fatalf("already wrapped response changed: %s", got)
	}
}

func TestTranslateOpenCodeRequestClaudeToResponses(t *testing.T) {
	payload := []byte(`{"model":"source","max_tokens":123,"temperature":0.2,"top_p":0.8,"system":"be concise","messages":[{"role":"user","content":"hello"}]}`)
	got := TranslateOpenCodeRequest(sdktranslator.FormatClaude, "target", payload, true)
	if !json.Valid(got) {
		t.Fatalf("invalid JSON: %s", got)
	}
	if gjson.GetBytes(got, "model").String() != "target" || !gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("model/stream not normalized: %s", got)
	}
	if gjson.GetBytes(got, "input.0.type").String() != "message" {
		t.Fatalf("missing converted input: %s", got)
	}
	if gjson.GetBytes(got, "max_output_tokens").Int() != 123 || gjson.GetBytes(got, "temperature").Float() != 0.2 || gjson.GetBytes(got, "top_p").Float() != 0.8 {
		t.Fatalf("generation fields were lost: %s", got)
	}
}

func TestTranslateOpenCodeNonStreamResponsesToOpenAI(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"m","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	got := TranslateOpenCodeNonStream(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "m", nil, nil, body, nil)
	if gjson.GetBytes(got, "object").String() != "chat.completion" {
		t.Fatalf("object = %q, body=%s", gjson.GetBytes(got, "object").String(), got)
	}
	if gjson.GetBytes(got, "choices.0.message.content").String() != "ok" {
		t.Fatalf("content = %q, body=%s", gjson.GetBytes(got, "choices.0.message.content").String(), got)
	}
}

type openCodeTranslationHooks struct {
	called bool
}

func (h *openCodeTranslationHooks) NormalizeRequest(_ context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) []byte {
	h.called = true
	if from != sdktranslator.FormatOpenAI || to != sdktranslator.FormatCodex || model != "target" || !stream {
		return body
	}
	return SetStringIfDifferent(body, "plugin_marker", "normalized")
}

func (*openCodeTranslationHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}
func (*openCodeTranslationHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}
func (*openCodeTranslationHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}
func (*openCodeTranslationHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestTranslateOpenCodeRequestHonorsPluginNormalizer(t *testing.T) {
	hooks := &openCodeTranslationHooks{}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })

	got := TranslateOpenCodeRequestWithContext(context.Background(), sdktranslator.FormatOpenAI, "target", []byte(`{"model":"source","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`), true)
	if !hooks.called {
		t.Fatal("OpenCode request did not invoke the plugin normalizer")
	}
	if gjson.GetBytes(got, "plugin_marker").String() != "normalized" {
		t.Fatalf("plugin marker missing: %s", got)
	}
	if gjson.GetBytes(got, "model").String() != "target" || !gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("final model/stream normalization missing: %s", got)
	}
}

func TestTranslateOpenCodeNonStreamCodexToOpenAI(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
	got := TranslateOpenCodeNonStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatOpenAI, "m", nil, nil, body, nil)
	if gjson.GetBytes(got, "object").String() != "chat.completion" {
		t.Fatalf("object = %q, body=%s", gjson.GetBytes(got, "object").String(), got)
	}
	if gjson.GetBytes(got, "choices.0.message.content").String() != "ok" {
		t.Fatalf("content = %q, body=%s", gjson.GetBytes(got, "choices.0.message.content").String(), got)
	}
}
