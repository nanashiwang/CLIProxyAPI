package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeFableFromOpenAIUsesCompatibleDefaultFingerprint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			var headers http.Header
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				body, _ = io.ReadAll(r.Body)
				if headers.Get("User-Agent") != "claude-cli/2.1.258 (external, cli)" {
					http.Error(w, "Claude Code version too old", 400)
					return
				}
				if gjson.GetBytes(body, "stream").Bool() {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fable\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-fable-5-1\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":"msg_fable","type":"message","role":"assistant","model":"claude-fable-5-1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				}
			}))
			defer server.Close()
			e := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "fable-test", Provider: "claude", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{"api_key": "sk-ant-oat-fable-test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "claude-fable-5-1", Payload: []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream}
			if stream {
				result, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else {
				if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if headers.Get("X-Stainless-Package-Version") != "0.112.1" {
				t.Fatal("SDK baseline not upgraded")
			}
			if gjson.GetBytes(body, "model").String() != "claude-fable-5-1" {
				t.Fatal("Fable model changed")
			}
			if !strings.Contains(string(body), "cc_version=2.1.258.") {
				t.Fatal("billing fingerprint version does not match User-Agent")
			}
		})
	}
}
