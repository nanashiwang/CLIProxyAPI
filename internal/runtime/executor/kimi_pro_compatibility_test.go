package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiProCompatibilityAcrossExecutors(t *testing.T) {
	for _, provider := range []string{"kimi", "opencode"} {
		for _, protocol := range []struct {
			name              string
			format            sdktranslator.Format
			payload, response string
		}{
			{"chat", sdktranslator.FormatOpenAI, `"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}`, `{"id":"chat_1","object":"chat.completion","model":"k3.1-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`},
			{"anthropic", sdktranslator.FormatClaude, `"max_tokens":64,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"tool","name":"lookup"}`, `{"id":"msg_1","type":"message","role":"assistant","model":"k3.1-pro","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`},
			{"responses", sdktranslator.FormatOpenAIResponse, `"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"lookup"}`, `{"id":"resp_1","object":"response","status":"completed","model":"k3.1-pro","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`},
		} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", provider, protocol.name, stream), func(t *testing.T) {
					var sent []byte
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
						var err error
						sent, err = io.ReadAll(req.Body)
						if err != nil {
							return nil, err
						}
						body, contentType := protocol.response, "application/json"
						if stream {
							contentType = "text/event-stream"
							switch protocol.name {
							case "responses":
								body = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + protocol.response + "}\n\n"
							case "anthropic":
								body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + protocol.response + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
							default:
								body = "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
							}
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					}))
					// Final wire constraints must win even when payload rules override
					// both the caller and the canonical thinking pipeline.
					cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"temperature": 0.4, "thinking.type": "enabled", "reasoning_effort": "high", "reasoning.effort": "high", "output_config.effort": "high"}}}}}
					model := "k3.1-pro(high)"
					var executor interface {
						Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
						ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
					}
					var auth *cliproxyauth.Auth
					if provider == "kimi" {
						executor = NewKimiExecutor(cfg)
						model = "kimi-" + model
						auth = &cliproxyauth.Auth{Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "test-kimi-key"}}
					} else {
						executor = NewOpenCodeExecutor(cfg)
						auth = openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, protocol.name)
					}
					payload := []byte(`{"model":"` + model + `","temperature":0.2,` + protocol.payload + `}`)
					request := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: protocol.format, OriginalRequest: payload, Stream: stream}
					if stream {
						result, err := executor.ExecuteStream(ctx, auth, request, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else {
						if _, err := executor.Execute(ctx, auth, request, opts); err != nil {
							t.Fatal(err)
						}
					}
					if len(sent) == 0 {
						t.Fatal("request never reached upstream")
					}
					if temp := gjson.GetBytes(sent, "temperature"); temp.Exists() {
						t.Fatalf("invalid temperature: %s", sent)
					}
					if protocol.name == "responses" {
						if gjson.GetBytes(sent, "reasoning.effort").String() != "none" || gjson.GetBytes(sent, "thinking").Exists() {
							t.Fatalf("Responses reasoning not disabled: %s", sent)
						}
					} else if gjson.GetBytes(sent, "thinking.type").String() != "disabled" {
						t.Fatalf("thinking not explicitly disabled: %s", sent)
					}
					choice := gjson.GetBytes(sent, "tool_choice")
					if choice.Get("name").String() != "lookup" && choice.Get("function.name").String() != "lookup" {
						t.Fatalf("forced tool lost: %s", sent)
					}
					if gjson.GetBytes(sent, "reasoning_effort").Exists() || gjson.GetBytes(sent, "output_config.effort").Exists() {
						t.Fatalf("conflicting effort remains: %s", sent)
					}
				})
			}
		}
	}
}
