package helps

import (
	"context"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"net/http"
	"testing"
)

func TestCodexAccountScopePreservesNativeAndIsolatesSwitch(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"unchanged"}],"client_metadata":{"parent_response_id":"old-account-response","x-codex-turn-state":"old-state","x-codex-turn-metadata":"{\"parent_response_id\":\"client-correlation\",\"account_id\":\"old\",\"future\":42}","future":true}}`)
	if got := ScopeCodexAccountBody(context.Background(), body); string(got) != string(body) {
		t.Fatal("same/unknown account changed native bytes")
	}
	ctx := core.WithAccountSwitched(context.Background(), true)
	got := ScopeCodexAccountBody(ctx, body)
	for _, key := range []string{"client_metadata.parent_response_id", "client_metadata.x-codex-turn-state"} {
		if gjson.GetBytes(got, key).Exists() {
			t.Fatalf("retained %s", key)
		}
	}
	metadata := gjson.GetBytes(got, "client_metadata.x-codex-turn-metadata").String()
	if gjson.Get(metadata, "account_id").Exists() || gjson.Get(metadata, "parent_response_id").String() != "client-correlation" || gjson.Get(metadata, "future").Int() != 42 {
		t.Fatalf("incorrect nested scope: %s", metadata)
	}
	if !gjson.GetBytes(got, "client_metadata.future").Bool() || gjson.GetBytes(got, "input.0.content").String() != "unchanged" {
		t.Fatal("unrelated payload changed")
	}
	h := http.Header{"X-Codex-Turn-State": {"old"}, "ChatGPT-Account-ID": {"selected"}, "Future": {"ok"}}
	ScopeCodexAccountHeaders(ctx, h)
	if h.Get("X-Codex-Turn-State") != "" || h["ChatGPT-Account-ID"][0] != "selected" || h.Get("Future") != "ok" {
		t.Fatalf("header isolation: %v", h)
	}
}
func TestCodexOversizedContinuationChecksOriginalAndTransport(t *testing.T) {
	for _, p := range []string{`{"previous_response_id":"r"}`, `{"type":"response.append"}`, `{"generate":false}`} {
		if !CodexOversizedContinuation(context.Background(), []byte(`{}`), []byte(p)) {
			t.Fatalf("unsafe fallback for %s", p)
		}
	}
	if !CodexOversizedContinuation(core.WithRequiredUpstreamWebsocket(context.Background()), []byte(`{}`)) {
		t.Fatal("transport requirement ignored")
	}
	if CodexOversizedContinuation(context.Background(), []byte(`{"input":[],"generate":true}`)) {
		t.Fatal("new request incorrectly blocked")
	}
}
