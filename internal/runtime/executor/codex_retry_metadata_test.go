package executor

import (
	usage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"testing"
)

func TestCodexRetryMetadataSurvivesTerminalConversion(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.failed","headers":{"x-retry-metadata":"NO_MORE_RETRY"},"response":{"error":{"type":"server_error","message":"You can retry your request"}}}`,
		`{"type":"response.failed","response":{"error":{"type":"server_error","headers":{"X-Retry-Metadata":"NO_MORE_RETRY"},"message":"You can retry your request"}}}`,
	} {
		err, body, ok := codexTerminalFailureErr([]byte(event))
		if !ok || !err.SameAccountRetryBlocked() || !err.IsRequestStop() || isCodexOverloadBootstrapFailure(body) {
			t.Fatalf("metadata lost: %+v %s", err, body)
		}
	}
	capacity := newCodexStatusErr(429, []byte(`{"error":{"type":"usage_limit_reached","headers":{"x-retry-metadata":"NO_MORE_RETRY"},"resets_in_seconds":60}}`))
	if !capacity.SameAccountRetryBlocked() || capacity.IsRequestStop() || capacity.RetryAfter() == nil {
		t.Fatalf("capacity policy changed: %+v", capacity)
	}
	normal := newCodexStatusErr(502, []byte(`{"error":{"message":"NO_MORE_RETRY"}}`))
	if normal.SameAccountRetryBlocked() || normal.IsRequestStop() {
		t.Fatal("message text became policy")
	}
	ws, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":502,"headers":{"x-retry-metadata":"NO_MORE_RETRY"},"error":{"type":"server_error"}}`))
	if !ok || !ws.(statusErrWithHeaders).IsRequestStop() {
		t.Fatalf("ws metadata lost: %v", ws)
	}
}

func TestCodexIncompleteHealthEvidence(t *testing.T) {
	if usage.FailureCategory(newCodexIncompleteStreamError()) != "stream_incomplete" || usage.FailureCategory(newCodexEmptyIncompleteStreamError()) != "stream_incomplete" {
		t.Fatal("missing incomplete evidence")
	}
}
