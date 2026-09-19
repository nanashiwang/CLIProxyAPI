package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageModelProtocolDocuments(t *testing.T) {
	for _, tt := range []struct{ name, payload, model, source string }{
		{"openai", `{"choices":[{"message":{"content":"hello"}}],"model":"gpt-build","usage":{"total_tokens":1}}`, "gpt-build", "body"},
		{"codex", `{"type":"response.completed","response":{"model":"gpt-build","output":[]}}`, "gpt-build", "body"},
		{"claude", `{"type":"message_start","message":{"model":"claude-build","usage":{"input_tokens":2}}}`, "claude-build", "body"},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"modelVersion":"gemini-build"}`, "gemini-build", "body"},
		{"gemini array", `[{"modelVersion":"gemini-build"}]`, "gemini-build", "body"},
		{"metadata string", `{"type":"response.metadata","headers":{"openai-model":"header-build"}}`, "header-build", "metadata"},
		{"metadata array before type", `{"headers":{"x-openai-model":["header-build"]},"type":"codex.response.metadata"}`, "header-build", "metadata"},
		{"terminal headers", `{"type":"response.completed","response":{"model":"body-build","headers":{"OpenAI-Model":["header-build"]}}}`, "header-build", "metadata"},
		{"terminal type last", `{"response":{"headers":{"openai-model":"header-build"},"model":"body-build"},"type":"response.completed"}`, "header-build", "metadata"},
		{"tool input ignored", `{"output":[{"arguments":{"model":"fake"}}],"input":{"model":"fake"},"tool":{"model":"fake"}}`, "", ""},
		{"untyped headers ignored", `{"headers":{"openai-model":"fake"}}`, "", ""},
		{"model number ignored", `{"model":42}`, "", ""},
		{"model control rejected", `{"model":"bad\nmodel"}`, "", ""},
		{"malformed suffix", `{"model":"fake"}oops`, "", ""},
		{"malformed comma", `{"model":"fake",}`, "", ""},
		{"malformed value", `{"model":"fake","usage":bad}`, "", ""},
		{"malformed nesting", `{"model":"fake","usage":[}}`, "", ""},
		{"truncated", `{"model":"fake"`, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, chunkSize := range []int{1, 7, len(tt.payload)} {
				reporter := NewUsageReporter(context.Background(), "provider", "billing", nil)
				observer := newResponseModelObserver("application/json", func(model, source string, rank uint8) { reporter.observeResponseModel(model, source, rank, 0) })
				for offset := 0; offset < len(tt.payload); offset += chunkSize {
					observer.Feed([]byte(tt.payload[offset:min(offset+chunkSize, len(tt.payload))]))
				}
				observer.Finish()
				record := reporter.buildRecord(usage.Detail{}, false)
				if record.UpstreamResponseModel != tt.model || record.UpstreamResponseModelSource != tt.source {
					t.Fatalf("chunk %d: model/source = %q/%q, want %q/%q", chunkSize, record.UpstreamResponseModel, record.UpstreamResponseModelSource, tt.model, tt.source)
				}
			}
		})
	}
}

func TestUsageModelSSEFragmentationAndRecovery(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "billing", nil)
	reporter.BeginModelObservation([]byte(`{"model":"wire"}`))
	observer := newResponseModelObserver("text/event-stream", func(model, source string, rank uint8) { reporter.observeResponseModel(model, source, rank, 1) })
	feed := func(text string) {
		for i := range text {
			observer.Feed([]byte{text[i]})
		}
	}
	feed("data: {\"model\":\"malformed\",}\r\n\r\n")
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "" {
		t.Fatalf("malformed event committed %q", got)
	}
	feed(": {\"model\":\"comment\"}\n\nevent: response.created\ndata: {\"response\":\n")
	feed("data: {\"model\":\"first\"},\"type\":\"response.created\"}\n\n")
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "first" {
		t.Fatalf("fragmented event model = %q", got)
	}
	feed("data: {\"type\":\"response.metadata\",\"headers\":{\"openai-model\":\"final\"}}\n\n")
	feed("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"body-fallback\"}}\n\n")
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.UpstreamResponseModel != "final" || record.UpstreamResponseModelSource != "metadata" {
		t.Fatalf("metadata priority = %+v", record)
	}
}

func TestUsageModelHTTPWireHeadersAndRetryIsolation(t *testing.T) {
	ctx := usage.WithRequestedModel(context.Background(), "client-alias")
	reporter := NewUsageReporter(ctx, "kimi", "billing-kimi", nil)
	var attempts int
	client := reporter.TrackHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"messages":[{"model":"tool-model"}],"model":"wire-kimi"}` {
			t.Fatalf("wire body altered: %s", body)
		}
		if attempts == 3 {
			return nil, errors.New("dial failed")
		}
		payload := `{"model":"body-version"}`
		header := http.Header{"Openai-Model": []string{"header-version"}, "Content-Type": []string{"application/json"}}
		if attempts == 2 {
			payload = `{}`
			header.Del("Openai-Model")
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	})})
	do := func() *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://provider.test/v1/messages", strings.NewReader(`{"messages":[{"model":"tool-model"}],"model":"wire-kimi"}`))
		resp, _ := client.Do(req)
		return resp
	}
	first := do()
	if _, err := io.ReadAll(first.Body); err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.Model != "billing-kimi" || record.RequestedModel != "client-alias" || record.UpstreamModel != "wire-kimi" || record.UpstreamResponseModel != "header-version" || record.UpstreamResponseModelSource != "header" {
		t.Fatalf("bad observed record: %+v", record)
	}
	second := do()
	if _, err := io.ReadAll(second.Body); err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "" {
		t.Fatalf("retry inherited model %q", got)
	}
	reporter.ObserveResponseModelPayload([]byte(`{"model":"second-model"}`), "body")
	do()
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "" {
		t.Fatalf("network failure inherited model %q", got)
	}
}

func TestUsageModelStaleReaderAndAdditionalBilling(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-main", nil)
	reporter.BeginModelObservation([]byte(`{"model":"first"}`))
	response := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"model":"stale"}`))}
	reporter.ObserveResponse(response)
	reporter.BeginModelObservation([]byte(`{"model":"second"}`))
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "" {
		t.Fatalf("stale reader contaminated retry: %q", got)
	}
	reporter.ObserveResponseModelPayload([]byte(`{"model":"current"}`), "body")
	record, ok := reporter.buildAdditionalModelRecord("gpt-image", usage.Detail{InputTokens: 1})
	if !ok || record.Model != "gpt-image" || record.UpstreamModel != "" || record.UpstreamResponseModel != "" || record.UpstreamResponseModelSource != "" {
		t.Fatalf("tool billing claimed a separate model observation: %+v", record)
	}
}

func TestUsageModelRequestMetadataUpdatesInitialHeader(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "billing", nil)
	reporter.BeginModelObservation([]byte(`{"model":"wire"}`))
	reporter.ObserveResponseModelHeaders(http.Header{"Openai-Model": []string{"initial-http-model"}})
	reporter.ObserveResponseModelPayload([]byte(`{"model":"body-cannot-override-header"}`), "body")
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "initial-http-model" {
		t.Fatalf("ordinary body replaced initial header: %q", got)
	}
	reporter.ObserveResponseModelPayload([]byte(`{"type":"response.metadata","headers":{"openai-model":"request-metadata-model"}}`), "body")
	reporter.ObserveResponseModelPayload([]byte(`{"response":{"model":"body-cannot-override-metadata"}}`), "body")
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.UpstreamResponseModel != "request-metadata-model" || record.UpstreamResponseModelSource != "metadata" {
		t.Fatalf("request metadata priority = %q (%s)", record.UpstreamResponseModel, record.UpstreamResponseModelSource)
	}
}

func TestUsageModelGeminiEndpointAndBoundedParsing(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "gemini", "billing", nil)
	req, _ := http.NewRequest("POST", "https://example.test/v1/projects/test/publishers/google/models/gemini-wire:streamGenerateContent", strings.NewReader(`{"model":"misleading-body"}`))
	reporter.observeHTTPRequestModel(req)
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamModel; got != "gemini-wire" {
		t.Fatalf("wire URL model = %q", got)
	}
	large := `{"choices":[{"text":"` + strings.Repeat("x", 2<<20) + `"}],"model":"after-large-content"}`
	var got string
	parser := modelJSONObserver{emit: func(model, _ string, _ uint8) { got = model }}
	for offset := 0; offset < len(large); offset += 4096 {
		parser.Feed([]byte(large[offset:min(offset+4096, len(large))]))
	}
	parser.Finish()
	if got != "after-large-content" || cap(parser.token) > 4096 {
		t.Fatalf("bounded projection model=%q retained=%d", got, cap(parser.token))
	}
	req, _ = http.NewRequest("POST", "https://example.test/v1/chat/completions", strings.NewReader(`{"input":"`+strings.Repeat("x", 2<<20)+`","model":"beyond-budget"}`))
	reporter.observeHTTPRequestModel(req)
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamModel; got != "" {
		t.Fatalf("request exceeded observation budget: %q", got)
	}
}

type eofModelReader struct{ data []byte }

func (r *eofModelReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.EOF
}
func (r *eofModelReader) Close() error { return nil }

func TestUsageModelObservesFinalBytesReturnedWithEOF(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "billing", nil)
	resp := &http.Response{Header: http.Header{}, Body: &eofModelReader{data: []byte(`{"model":"final"}`)}}
	reporter.ObserveResponse(resp)
	_, _ = io.ReadAll(resp.Body)
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamResponseModel; got != "final" {
		t.Fatalf("n>0/EOF model = %q", got)
	}
}
