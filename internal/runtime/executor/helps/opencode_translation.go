package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	codexgemini "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/gemini"
	codexinteractions "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/interactions"
	codexopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TranslateOpenCodeRequest converts source protocols to the OpenCode native Responses shape.
// The Codex request converters already produce the standard Responses input model and preserve
// tools, multimodal content, and reasoning fields without changing the shared translator registry.
func TranslateOpenCodeRequest(from sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	return TranslateOpenCodeRequestWithContext(context.Background(), from, model, payload, stream)
}

// TranslateOpenCodeRequestWithContext performs the native Responses conversion
// while preserving the shared summary and plugin-normalization pipeline.
func TranslateOpenCodeRequestWithContext(ctx context.Context, from sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	var translated []byte
	switch from {
	case sdktranslator.FormatOpenAI:
		translated = codexopenai.ConvertOpenAIRequestToCodex(model, payload, stream)
	case sdktranslator.FormatClaude:
		translated = codexclaude.ConvertClaudeRequestToCodex(model, payload, stream)
	case sdktranslator.FormatGemini:
		translated = codexgemini.ConvertGeminiRequestToCodex(model, payload, stream)
	case sdktranslator.FormatInteractions:
		translated = codexinteractions.ConvertInteractionsRequestToCodex(model, payload, stream)
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		translated = payload
	default:
		translated = sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAIResponse, model, payload, stream)
	}

	translated = restoreOpenCodeResponsesGenerationFields(payload, translated)
	// Native OpenCode conversion bypasses Registry.TranslateRequest, so apply
	// the same summary-before-normalizer ordering explicitly.
	summaryConfig := thinking.ExtractSummaryConfig(payload, from.String())
	translated = thinking.ApplySummaryConfigForModel(translated, sdktranslator.FormatCodex.String(), model, summaryConfig)
	translated = sdktranslator.NormalizeRequest(ctx, from, sdktranslator.FormatCodex, model, translated, stream)
	translated = SetBoolIfDifferent(translated, "stream", stream)
	return SetStringIfDifferent(translated, "model", model)
}

func restoreOpenCodeResponsesGenerationFields(source, translated []byte) []byte {
	root := gjson.ParseBytes(source)
	for _, mapping := range []struct {
		destination string
		sources     []string
	}{
		{destination: "max_output_tokens", sources: []string{"max_output_tokens", "max_completion_tokens", "max_tokens"}},
		{destination: "temperature", sources: []string{"temperature"}},
		{destination: "top_p", sources: []string{"top_p"}},
		{destination: "stop", sources: []string{"stop", "stop_sequences"}},
		{destination: "metadata", sources: []string{"metadata"}},
	} {
		for _, path := range mapping.sources {
			value := root.Get(path)
			if !value.Exists() {
				continue
			}
			if updated, err := sjson.SetRawBytes(translated, mapping.destination, []byte(value.Raw)); err == nil {
				translated = updated
			}
			break
		}
	}
	return translated
}

// TranslateOpenCodeStream converts Responses events emitted by OpenCode to the client's format.
// Codex is the canonical internal Responses format, while the legacy
// openai-response branch remains for compatibility with direct helper callers.
func TranslateOpenCodeStream(ctx context.Context, upstream, target sdktranslator.Format, model string, originalRequest, request, rawJSON []byte, param *any) [][]byte {
	if upstream == sdktranslator.FormatCodex {
		return sdktranslator.TranslateStream(ctx, upstream, target, model, originalRequest, request, rawJSON, param)
	}
	if upstream != sdktranslator.FormatOpenAIResponse {
		return sdktranslator.TranslateStream(ctx, upstream, target, model, originalRequest, request, rawJSON, param)
	}

	switch target {
	case sdktranslator.FormatOpenAI:
		return codexopenai.ConvertCodexResponseToOpenAI(ctx, model, originalRequest, request, rawJSON, param)
	case sdktranslator.FormatClaude:
		return codexclaude.ConvertCodexResponseToClaude(ctx, model, originalRequest, request, rawJSON, param)
	case sdktranslator.FormatGemini:
		return codexgemini.ConvertCodexResponseToGemini(ctx, model, originalRequest, request, rawJSON, param)
	case sdktranslator.FormatInteractions:
		return codexinteractions.ConvertCodexResponseToInteractions(ctx, model, originalRequest, request, rawJSON, param)
	default:
		return sdktranslator.TranslateStream(ctx, upstream, target, model, originalRequest, request, rawJSON, param)
	}
}

// TranslateOpenCodeNonStream converts a direct Responses object to the client's format.
func TranslateOpenCodeNonStream(ctx context.Context, upstream, target sdktranslator.Format, model string, originalRequest, request, rawJSON []byte, param *any) []byte {
	if upstream == sdktranslator.FormatCodex {
		if target == sdktranslator.FormatCodex || !sdktranslator.HasNonStreamResponseTransformer(target, upstream) {
			return rawJSON
		}
		rawJSON = NormalizeOpenCodeResponsesEnvelope(rawJSON)
		return sdktranslator.TranslateNonStream(ctx, upstream, target, model, originalRequest, request, rawJSON, param)
	}
	if upstream != sdktranslator.FormatOpenAIResponse || target == sdktranslator.FormatOpenAIResponse {
		return sdktranslator.TranslateNonStream(ctx, upstream, target, model, originalRequest, request, rawJSON, param)
	}

	envelope := NormalizeOpenCodeResponsesEnvelope(rawJSON)
	switch target {
	case sdktranslator.FormatOpenAI:
		return codexopenai.ConvertCodexResponseToOpenAINonStream(ctx, model, originalRequest, request, envelope, param)
	case sdktranslator.FormatClaude:
		return codexclaude.ConvertCodexResponseToClaudeNonStream(ctx, model, originalRequest, request, envelope, param)
	case sdktranslator.FormatGemini:
		return codexgemini.ConvertCodexResponseToGeminiNonStream(ctx, model, originalRequest, request, envelope, param)
	case sdktranslator.FormatInteractions:
		return codexinteractions.ConvertCodexResponseToInteractionsNonStream(ctx, model, originalRequest, request, envelope, param)
	default:
		return sdktranslator.TranslateNonStream(ctx, upstream, target, model, originalRequest, request, envelope, param)
	}
}

// NormalizeOpenCodeResponsesEnvelope wraps a direct Responses object in the terminal event
// envelope expected by the existing Codex response converters.
func NormalizeOpenCodeResponsesEnvelope(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return rawJSON
	}
	root := gjson.ParseBytes(trimmed)
	typeName := root.Get("type").String()
	if typeName == "response.completed" || typeName == "response.incomplete" || typeName == "response.failed" {
		return trimmed
	}
	if root.Get("response").Exists() {
		return trimmed
	}
	if root.Get("object").String() != "response" && !root.Get("output").IsArray() {
		return trimmed
	}

	status := strings.ToLower(strings.TrimSpace(root.Get("status").String()))
	switch status {
	case "failed", "error":
		typeName = "response.failed"
	case "incomplete":
		typeName = "response.incomplete"
	default:
		typeName = "response.completed"
	}
	envelope := []byte(`{"type":"","response":null}`)
	envelope, err := sjson.SetBytes(envelope, "type", typeName)
	if err != nil {
		return rawJSON
	}
	envelope, err = sjson.SetRawBytes(envelope, "response", trimmed)
	if err != nil {
		return rawJSON
	}
	return envelope
}
