package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeReviewProtocolCapabilitiesDoNotDependOnFingerprint(t *testing.T) {
	const review = "dangerous-tool-use-2026-09-03"
	const future = "future-review-protocol-2099-01-01"
	for _, authKind := range []string{"oauth", "api_key", "profile_api_key"} {
		for _, confirmed := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				for _, endpoint := range []string{"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/messages/count_tokens", "https://gateway.example/v1/messages"} {
					t.Run(authKind+"/"+endpoint+"/confirmed="+boolName(confirmed)+"/stream="+boolName(stream), func(t *testing.T) {
						key := "audit-upstream-key"
						auth := &cliproxyauth.Auth{ID: "protocol-test", Attributes: map[string]string{"api_key": key}, Metadata: claudeOAuthTestMetadata()}
						if authKind == "oauth" {
							key = "sk-ant-oat01-audit-only"
							auth.Attributes["api_key"] = key
						}
						if authKind == "profile_api_key" {
							auth.Attributes["fingerprint_profile"] = "claude-code-cli"
						}
						incoming := make(http.Header)
						incoming.Add("Anthropic-Beta", "claude-code-20250219,"+review)
						incoming.Add("Anthropic-Beta", " "+future+",,"+review)
						incoming.Set("User-Agent", "claude-cli/2.1.301 (external, sdk-cli)")
						incoming.Set("X-App", "cli")
						incoming.Set("Authorization", "Bearer caller-secret")
						incoming.Set("X-Api-Key", "caller-secret")
						incoming.Set("Cookie", "session=caller-secret")
						req := httptest.NewRequest(http.MethodPost, endpoint, nil)
						body := []byte(`{"model":"claude-opus-5-5","safeguards":[{"type":"dangerous_tool_use"}]}`)
						err := applyClaudeHeaders(req, auth, key, stream, []string{future, "per-turn-control-2026-07-01"}, body, &config.Config{}, incoming, confirmed)
						if err != nil {
							t.Fatal(err)
						}
						got := strings.Split(req.Header.Get("Anthropic-Beta"), ",")
						for _, want := range []string{review, future, "per-turn-control-2026-07-01"} {
							count := 0
							for _, v := range got {
								if v == want {
									count++
								}
							}
							if count != 1 {
								t.Fatalf("capability %q count=%d in %q", want, count, got)
							}
						}
						for _, header := range []string{"Authorization", "X-Api-Key", "Cookie"} {
							if strings.Contains(req.Header.Get(header), "caller-secret") {
								t.Fatalf("caller credential leaked via %s", header)
							}
						}
					})
				}
			}
		}
	}
}

func boolName(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func TestClaudeReviewProtocolBodyBetaSurvivesWithoutHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	auth := &cliproxyauth.Auth{ID: "body-beta", Metadata: claudeOAuthTestMetadata()}
	body := []byte(`{"model":"claude-opus-5-5","betas":["dangerous-tool-use-2026-09-03","future-review-2099-01-01"],"safeguards":[{"type":"dangerous_tool_use"}]}`)
	extra, body := extractAndRemoveBetas(body)
	if err := applyClaudeHeaders(req, auth, "sk-ant-oat01-audit-only", false, extra, body, &config.Config{}, nil, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range extra {
		if !strings.Contains(req.Header.Get("Anthropic-Beta"), want) {
			t.Fatalf("lost %s", want)
		}
	}
}

func TestClaudeReviewResultsSurviveToolNameRestoration(t *testing.T) {
	for _, outcome := range []string{"not_flagged", "flagged"} {
		t.Run(outcome, func(t *testing.T) {
			result := `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_review":{"type":"evaluated","outcome":"` + outcome + `"}}}}]`
			raw := []byte(`{"type":"message","content":[{"type":"tool_use","id":"toolu_review","name":"mcp__audit__bash","input":{}}],"safeguard_results":` + result + `}`)
			restored, err := restoreClaudeOAuthToolNamesFromResponse(raw, map[string]string{"mcp__audit__bash": "Bash"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(restored), `"id":"toolu_review"`) || !strings.Contains(string(restored), `"safeguard_results":`+result) {
				t.Fatalf("review/tool link changed: %s", restored)
			}
			event := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"safeguard_results":` + result + `}`)
			streamed, err := restoreClaudeOAuthToolNamesFromStreamLine(event, map[string]string{"mcp__audit__bash": "Bash"})
			if err != nil {
				t.Fatal(err)
			}
			if string(streamed) != string(event) {
				t.Fatalf("review event changed: %s", streamed)
			}
		})
	}
}

func TestClaudeReviewExecutorHTTPRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, outcome := range []string{"not_flagged", "flagged"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, outcome), func(t *testing.T) {
				verdict := `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"toolu_review":{"type":"evaluated","outcome":"` + outcome + `"}}}}]`
				incomingSeen := make(chan []byte, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					incomingSeen <- body
					if !strings.Contains(r.Header.Get("Anthropic-Beta"), "dangerous-tool-use-2026-09-03") || !strings.Contains(r.Header.Get("Anthropic-Beta"), "per-turn-control-2026-07-01") {
						t.Error("capability header lost")
						w.WriteHeader(400)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_review","model":"claude-opus-5-5","usage":{"input_tokens":2,"output_tokens":0}}}`+"\n\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_review","name":"Bash","input":{}}}`+"\n\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9},"safeguard_results":`+verdict+`}`+"\n\n"+`data: {"type":"message_stop"}`+"\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, `{"id":"msg_review","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"toolu_review","name":"Bash","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":9},"safeguard_results":`+verdict+`}`)
					}
				}))
				t.Cleanup(upstream.Close)
				body := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"v":1}}],"messages":[{"role":"user","content":"Synthetic audit"},{"role":"system","content":[],"output_config":{"effort":"high"}}]}`)
				incoming := make(http.Header)
				incoming.Set("User-Agent", "claude-cli/2.1.301 (external, sdk-cli)")
				incoming.Set("X-App", "cli")
				incoming.Set("Anthropic-Beta", "dangerous-tool-use-2026-09-03,per-turn-control-2026-07-01")
				auth := &cliproxyauth.Auth{ID: "roundtrip", Attributes: map[string]string{"api_key": "sk-ant-oat01-synthetic", "base_url": upstream.URL}, Metadata: claudeOAuthTestMetadata()}
				e := NewClaudeExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: body}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: body, Headers: incoming}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var wire []byte
				if stream {
					result, err := e.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						wire = append(wire, chunk.Payload...)
					}
				} else {
					result, err := e.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					wire = result.Payload
				}
				sent := <-incomingSeen
				if gjson.GetBytes(sent, "safeguards.0.type").String() != "dangerous_tool_use" {
					t.Fatal("safeguards lost")
				}
				if !strings.Contains(string(sent), `"output_config":{"effort":"high"}`) {
					t.Fatalf("message capability lost: %s", sent)
				}
				if !strings.Contains(string(wire), `"safeguard_results":`+verdict) || !strings.Contains(string(wire), `"id":"toolu_review"`) {
					t.Fatalf("verdict or tool identity lost: %s", wire)
				}
			})
		}
	}
}

func TestClaudeReviewUnsupportedCapabilityIsNotSilentlyRetried(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.Contains(r.Header.Get("Anthropic-Beta"), "future-review-2099-01-01") {
			t.Error("unknown beta removed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unsupported review capability"}}`)
	}))
	t.Cleanup(upstream.Close)
	e := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "audit-key", "base_url": upstream.URL}}
	payload := []byte(`{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":"synthetic"}],"safeguards":[{"type":"future_review"}]}`)
	headers := make(http.Header)
	headers.Set("Anthropic-Beta", "future-review-2099-01-01")
	_, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers})
	if err == nil || !strings.Contains(err.Error(), "unsupported review capability") {
		t.Fatalf("expected original capability error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("unexpected silent retry: %d", calls)
	}
}
