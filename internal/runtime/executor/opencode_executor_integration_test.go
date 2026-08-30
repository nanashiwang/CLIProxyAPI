package executor

import (
	"context"
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

type openCodeRoundTripper func(*http.Request) (*http.Response, error)

func (f openCodeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func responsesAuth() *cliproxyauth.Auth {
	return openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, "responses")
}

func TestOpenCodeExecutorResponsesRequestAndDirectResponse(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)),
		}, nil
	}))

	payload := []byte(`{"model":"m","system":"be concise","messages":[{"role":"user","content":"hello"}]}`)
	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, responsesAuth(), cliproxyexecutor.Request{Model: "m", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(upstreamBody, "input.0.type").String() != "message" || gjson.GetBytes(upstreamBody, "stream").Bool() {
		t.Fatalf("unexpected Responses request: %s", upstreamBody)
	}
	if gjson.GetBytes(resp.Payload, "type").String() != "message" || gjson.GetBytes(resp.Payload, "content.0.text").String() != "ok" {
		t.Fatalf("unexpected Claude response: %s", resp.Payload)
	}
}

func TestOpenCodeExecutorResponsesStreamTerminalWithoutDone(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\nevent: response.completed\ndata: {\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	result, err := executor.ExecuteStream(ctx, responsesAuth(), cliproxyexecutor.Request{
		Model:   "m",
		Payload: []byte(`{"model":"m","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		ResponseFormat:  sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var payloads [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error = %v", chunk.Err)
		}
		payloads = append(payloads, chunk.Payload)
	}
	if len(payloads) == 0 {
		t.Fatal("stream produced no payloads")
	}
	foundContent := false
	for _, payload := range payloads {
		if gjson.GetBytes(bytesAfterData(payload), "choices.0.delta.content").String() == "ok" {
			foundContent = true
		}
	}
	if !foundContent {
		t.Fatalf("stream did not contain content: %q", payloads)
	}
}

func TestOpenCodeExecutorResponsesStreamFailureAndTruncated(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "failed event", body: "event: response.failed\ndata: {\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"bad upstream\"}}}\n\n"},
		{name: "done failed status", body: "event: response.done\ndata: {\"response\":{\"status\":\"failed\"}}\n\n"},
		{name: "missing terminal", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			}))
			result, err := NewOpenCodeExecutor(&config.Config{}).ExecuteStream(ctx, responsesAuth(), cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","input":"hello"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil {
				t.Fatal("stream completed without an error")
			}
		})
	}
}

func bytesAfterData(payload []byte) []byte {
	payload = []byte(strings.TrimSpace(string(payload)))
	if strings.HasPrefix(string(payload), "data:") {
		return []byte(strings.TrimSpace(string(payload[len("data:"):])))
	}
	return payload
}

func TestOpenCodeExecutorResponsesStreamIgnoresFramesAfterTerminal(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(*http.Request) (*http.Response, error) {
		body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))

	result, err := NewOpenCodeExecutor(&config.Config{}).ExecuteStream(ctx, responsesAuth(), cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","input":"hello"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error = %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "late") {
			t.Fatalf("post-terminal payload was forwarded: %s", chunk.Payload)
		}
	}
}

func TestOpenCodeExecutorRejectsInvalidNonStreamBody(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("not-json"))}, nil
	}))

	_, err := NewOpenCodeExecutor(&config.Config{}).Execute(ctx, responsesAuth(), cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","input":"hello"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatal("Execute() accepted an invalid non-stream response body")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("error status = %v, want %d", err, http.StatusBadGateway)
	}
}

func TestOpenCodeExecutorRejectsOversizedSSEFrame(t *testing.T) {
	part := strings.Repeat("x", maxOpenCodeSSEFrameBytes/2)
	body := "data: " + part + "\n" + "data: " + part + "\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", openCodeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))
	result, err := NewOpenCodeExecutor(&config.Config{}).ExecuteStream(ctx, responsesAuth(), cliproxyexecutor.Request{
		Model:   "m",
		Payload: []byte(`{"model":"m","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		ResponseFormat:  sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("oversized SSE frame did not produce a stream error")
	}
	status, ok := streamErr.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error = %v, want HTTP 502 status error", streamErr)
	}
}
