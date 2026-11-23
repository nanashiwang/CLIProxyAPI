package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CodexOversizedContinuation never treats a delta as a complete HTTP request.
func CodexOversizedContinuation(ctx context.Context, payloads ...[]byte) bool {
	if core.RequiredUpstreamWebsocket(ctx) || CodexHasIncrementalInput(payloads...) {
		return true
	}
	for _, payload := range payloads {
		if generate := ojson.GetBytesNoCopy(payload, "generate"); generate.Exists() && !generate.Bool() {
			return true
		}
	}
	return false
}

// CodexHasIncrementalInput also checks the original input before translation or payload rules.
func CodexHasIncrementalInput(payloads ...[]byte) bool {
	for _, payload := range payloads {
		if ojson.GetBytesNoCopy(payload, "previous_response_id").String() != "" || ojson.GetBytesNoCopy(payload, "type").String() == "response.append" {
			return true
		}
	}
	return false
}

// ScopeCodexAccountBody removes account-owned state only after a proven switch.
// Same-account and unknown provenance retain CPA's native passthrough contract.
func ScopeCodexAccountBody(ctx context.Context, body []byte) []byte {
	if !core.AccountSwitched(ctx) {
		return body
	}
	for _, root := range []string{"", "client_metadata."} {
		for _, key := range []string{"previous_response_id", "previousResponseId", "response_id", "responseId", "conversation", "turn_state", "turnState", "x-codex-turn-state", "account_id", "accountId", "chatgpt-account-id", "chatgpt_account_id", "access_token", "refresh_token", "id_token", "authorization", "cookie"} {
			body, _ = sjson.DeleteBytes(body, root+key)
		}
	}
	body, _ = sjson.DeleteBytes(body, "client_metadata.parent_response_id")
	for _, path := range []string{"client_metadata.x-codex-turn-metadata", "client_metadata.turn_metadata", "client_metadata.turnMetadata", "turn_metadata", "turnMetadata"} {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.String || !gjson.Valid(value.String()) {
			body, _ = sjson.DeleteBytes(body, path)
			continue
		}
		clean := []byte(value.String())
		for _, key := range []string{"account_id", "user_id", "authorization", "cookie", "access_token", "refresh_token", "turn_state", "turnState", "x-codex-turn-state", "previous_response_id", "response_id", "conversation"} {
			clean, _ = sjson.DeleteBytes(clean, key)
		}
		body, _ = sjson.SetBytes(body, path, string(clean))
	}
	return body
}

// ScopeCodexAccountHeaders leaves account-configured identity headers alone;
// only inherited turn state becomes invalid after a credential switch.
func ScopeCodexAccountHeaders(ctx context.Context, headers http.Header) {
	if !core.AccountSwitched(ctx) {
		return
	}
	for key := range headers {
		switch strings.ToLower(key) {
		case "x-codex-turn-state", "x-codex-turn-metadata":
			delete(headers, key)
		}
	}
}
