package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiModelObservationPreservesWireAndRawResponse(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		format                       sdktranslator.Format
		stream, compressed, override bool
	}{
		{name: "claude compressed before alias rewrite", format: sdktranslator.FormatClaude, compressed: true},
		{name: "claude SSE", format: sdktranslator.FormatClaude, stream: true},
		{name: "openai final payload override", format: sdktranslator.FormatOpenAI, override: true},
		{name: "responses normalized model", format: sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := uuid.NewString()
			plugin := &captureKimiModelObservationUsage{authID: authID, records: make(chan usage.Record, 1)}
			usage.RegisterPlugin(plugin)
			var wireModel string
			ctx := usage.WithRequestedModel(context.Background(), "client-kimi")
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				wireModel = gjson.GetBytes(body, "model").String()
				payload := `{"id":"msg_model","type":"message","role":"assistant","model":"reported-kimi-build","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`
				header := http.Header{"Content-Type": []string{"application/json"}}
				if tc.stream {
					header.Set("Content-Type", "text/event-stream")
					payload = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_model\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"reported-kimi-build\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				} else if tc.format == sdktranslator.FormatOpenAI {
					payload = `{"id":"chatcmpl_model","object":"chat.completion","model":"reported-kimi-build","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
				} else if tc.format == sdktranslator.FormatOpenAIResponse {
					payload = `{"id":"resp_model","object":"response","model":"reported-kimi-build","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
				}
				var responseBody io.ReadCloser = io.NopCloser(strings.NewReader(payload))
				if tc.compressed {
					var compressed bytes.Buffer
					writer := gzip.NewWriter(&compressed)
					if _, err := writer.Write([]byte(payload)); err != nil {
						return nil, err
					}
					if err := writer.Close(); err != nil {
						return nil, err
					}
					header.Set("Content-Encoding", "gzip")
					responseBody = io.NopCloser(bytes.NewReader(compressed.Bytes()))
				}
				return &http.Response{StatusCode: 200, Header: header, Body: responseBody, Request: req}, nil
			}))
			cfg := &config.Config{}
			if tc.override {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "kimi-k2.5"}}, Params: map[string]any{"model": "final-payload-model"}}}
			}
			executor := NewKimiExecutor(cfg)
			auth := &cliproxyauth.Auth{ID: authID, Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "test-kimi-key"}}
			payload := []byte(`{"model":"client-kimi","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
			if tc.format == sdktranslator.FormatOpenAIResponse {
				payload = []byte(`{"model":"client-kimi","input":"hello"}`)
			}
			req := cliproxyexecutor.Request{Model: "kimi-k2.5", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: tc.format, OriginalRequest: payload, Stream: tc.stream}
			if tc.stream {
				result, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else {
				response, err := executor.Execute(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				if tc.format == sdktranslator.FormatClaude && gjson.GetBytes(response.Payload, "model").String() != req.Model {
					t.Fatalf("expected existing downstream alias rewrite: %s", response.Payload)
				}
			}
			wantWire := "k2.5"
			if tc.override {
				wantWire = "final-payload-model"
			}
			select {
			case record := <-plugin.records:
				if wireModel != wantWire || record.UpstreamModel != wireModel || record.UpstreamResponseModel != "reported-kimi-build" || record.UpstreamResponseModelSource != "body" || record.RequestedModel != "client-kimi" || record.Model != "kimi-k2.5" {
					t.Fatalf("wire=%q, usage=%+v", wireModel, record)
				}
				if record.Detail.InputTokens != 2 || record.Detail.OutputTokens != 1 {
					t.Fatalf("token usage changed: %+v", record.Detail)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for usage record")
			}
		})
	}
}

type captureKimiModelObservationUsage struct {
	authID  string
	records chan usage.Record
}

func (p *captureKimiModelObservationUsage) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID == p.authID {
		select {
		case p.records <- record:
		default:
		}
	}
}
