package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeKimiProRequest reconciles model constraints after thinking and payload
// rules. Forced tool selection takes precedence over reasoning for this turn.
func NormalizeKimiProRequest(body []byte, model string, format sdktranslator.Format) []byte {
	base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	base = strings.TrimPrefix(strings.TrimSuffix(base, "[1m]"), "kimi-")
	if (base != "k3.1-pro" && base != "k3.1-pro-highspeed") || !gjson.ValidBytes(body) {
		return body
	}
	if temperature := gjson.GetBytes(body, "temperature"); temperature.Type == gjson.Number {
		// The provider selects 1 for thinking and 0.6 for non-thinking.
		// Omission also handles provider-specific defaults on Responses.
		body, _ = sjson.DeleteBytes(body, "temperature")
	}
	choice := gjson.GetBytes(body, "tool_choice")
	forced := choice.Type == gjson.String && choice.String() == "required"
	switch format {
	case sdktranslator.FormatClaude:
		forced = choice.Get("type").String() == "any" || choice.Get("type").String() == "tool"
	case sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		forced = forced || choice.Get("type").String() == "function"
	default:
		return body
	}
	if !forced {
		return body
	}
	// Remove stale cross-protocol effort fields without discarding output format
	// or reasoning-summary preferences owned by the caller.
	body, _ = sjson.DeleteBytes(body, "reasoning_effort")
	body, _ = sjson.DeleteBytes(body, "output_config.effort")
	if output := gjson.GetBytes(body, "output_config"); output.IsObject() && len(output.Map()) == 0 {
		body, _ = sjson.DeleteBytes(body, "output_config")
	}
	if format == sdktranslator.FormatOpenAIResponse || format == sdktranslator.FormatCodex {
		body, _ = sjson.DeleteBytes(body, "thinking")
		body, _ = sjson.SetBytes(body, "reasoning.effort", "none")
	} else {
		body, _ = sjson.DeleteBytes(body, "reasoning.effort")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
		body, _ = sjson.SetRawBytes(body, "thinking", []byte(`{"type":"disabled"}`))
	}
	return body
}
