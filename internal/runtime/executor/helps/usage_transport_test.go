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

func TestUsageUpstreamTransportResetsAfterSSERetryAndNetworkFailure(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "billing", nil)
	var attempt int
	client := reporter.TrackHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt++
		if attempt == 2 {
			return nil, errors.New("TLS connection failed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("data: {}\n\n")), Request: req}, nil
	})})
	request := func() *http.Request {
		req, _ := http.NewRequest("POST", "https://upstream.test/v1/responses", strings.NewReader(`{"model":"wire","stream":true}`))
		return req
	}
	first, err := client.Do(request())
	if err != nil {
		t.Fatal(err)
	}
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamTransport; got != "sse" {
		t.Fatalf("actual SSE response transport=%q", got)
	}
	if _, err := client.Do(request()); err == nil {
		t.Fatal("expected failed second attempt")
	}
	if got := reporter.buildRecord(usage.Detail{}, true).UpstreamTransport; got != "http" {
		t.Fatalf("network failure inherited SSE instead of attempted HTTP: %q", got)
	}
	_, _ = io.ReadAll(first.Body)
	_ = first.Body.Close()
	if got := reporter.buildRecord(usage.Detail{}, true).UpstreamTransport; got != "http" {
		t.Fatalf("stale response changed retry transport: %q", got)
	}
	reporter.BeginModelObservation([]byte(`{"model":"wire"}`))
	reporter.ObserveUpstreamTransport("ws")
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamTransport; got != "ws" {
		t.Fatalf("WS exchange transport=%q", got)
	}
	reporter.BeginModelObservation([]byte(`{"model":"wire"}`))
	if got := reporter.buildRecord(usage.Detail{}, true).UpstreamTransport; got != "" {
		t.Fatalf("unobserved exchange inherited previous WS: %q", got)
	}
}

func TestUsageSDKDoesNotGuessUpstreamTransport(t *testing.T) {
	for _, provider := range []string{"codex", "aistudio", "xai", "custom-plugin"} {
		reporter := NewUsageReporter(context.Background(), provider, "billing", nil)
		record := reporter.buildRecord(usage.Detail{}, false)
		if record.ClientTransport != "" || record.UpstreamTransport != "" || record.ClientIP != "" || record.UserAgent != "" {
			t.Fatalf("provider name inferred client/upstream facts: %+v", record)
		}
	}
}
