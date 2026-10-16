package helps

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResponseTimingPreludeOutputAndUpstreamClock(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "test", nil)
	r.requestedAt = time.Now().Add(-time.Second)
	r.BeginModelObservation([]byte(`{"model":"test"}`))
	feed, finish := r.NewResponseModelStreamObserver("text/event-stream")
	feed([]byte("data: {\"type\":\"response.created\"}\n\n"))
	if r.responseTiming().FirstOutputMs != nil {
		t.Fatal("prelude counted as output")
	}
	for _, part := range []string{"data: {\"type\":\"response.output_", "item.added\"}\r\n\r\n"} {
		feed([]byte(part))
	}
	first := r.responseTiming().FirstOutputMs
	if first == nil || *first < 1000 {
		t.Fatalf("first output does not use request clock: %+v", first)
	}
	feed([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"text\"}\n\n"))
	feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"created_at\":10,\"completed_at\":12.5},\"timing_metrics\":{\"critical_path\":{\"scope\":\"response\",\"coverage\":\"complete\",\"boundary_type\":\"actionable_output_item_done\",\"engine_wall_ms\":2000,\"responses_pre_inference_ms\":20,\"responses_other_ms\":30}}}\n\n"))
	finish()
	got := r.responseTiming()
	if got.FirstTextMs == nil || got.FirstReasoningMs != nil || got.UpstreamResponseMs == nil || *got.UpstreamResponseMs != 2500 || got.UpstreamOverheadMs == nil || *got.UpstreamOverheadMs != 50 || *got.FirstOutputMs != *first {
		t.Fatalf("timings = %+v", got)
	}
	*got.FirstOutputMs = 0
	if *r.responseTiming().FirstOutputMs == 0 {
		t.Fatal("timing aliases reporter")
	}
	r.BeginModelObservation([]byte(`{"model":"retry"}`))
	feed([]byte("data: {\"type\":\"response.output_item.added\"}\n\n"))
	if r.responseTiming().FirstOutputMs != nil {
		t.Fatal("old exchange polluted retry")
	}
}

func TestResponseTimingOversizedAndInvalidFramesStayUnknown(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "test", nil)
	r.BeginModelObservation(nil)
	feed, _ := r.NewResponseModelStreamObserver("text/event-stream")
	feed([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", timingFrameLimit) + "\"}\n\n"))
	if r.responseTiming().FirstOutputMs != nil {
		t.Fatal("oversized frame should be skipped")
	}
	for _, span := range []string{`"created_at":12,"completed_at":12`, `"created_at":12,"completed_at":10`, `"completed_at":12`, `"created_at":"10","completed_at":12`} {
		r.ObserveResponseModelPayload([]byte(fmt.Sprintf(`{"type":"response.completed","response":{%s},"timing_metrics":{"critical_path":{"scope":"logical_turn","engine_wall_ms":100}}}`, span)), "body")
		if got := r.responseTiming(); got.UpstreamResponseMs != nil || got.UpstreamEngineMs != nil {
			t.Fatal("invalid or cross-response clock accepted")
		}
	}
	feed([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	if r.responseTiming().FirstOutputMs != nil {
		t.Fatal("role is not output")
	}
	feed([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"thinking\":\"thought\"}}\n\n"))
	if got := r.responseTiming(); got.FirstReasoningMs == nil || got.FirstOutputMs == nil || got.FirstTextMs != nil {
		t.Fatal("reasoning observation missing")
	}
}

func TestHTTPTimingObservationPreservesBodyAndRejectsStaleReader(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "test", nil)
	r.BeginModelObservation(nil)
	body := "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_item.added\"}\n\n"
	response := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	r.ObserveResponse(response)
	got, err := io.ReadAll(response.Body)
	if err != nil || string(got) != body {
		t.Fatal("observation changed forwarded bytes")
	}
	timing := r.responseTiming()
	if timing.FirstByteMs == nil || timing.FirstOutputMs == nil || *timing.FirstOutputMs < *timing.FirstByteMs {
		t.Fatal("HTTP timing not wired")
	}
	stale := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	r.ObserveResponse(stale)
	r.BeginModelObservation(nil)
	_, _ = io.ReadAll(stale.Body)
	if got := r.responseTiming(); got.FirstByteMs != nil || got.FirstOutputMs != nil {
		t.Fatal("stale body polluted new attempt")
	}
}

func TestUsageFailureRetainsOnlyClassifiedCauseForDiagnostics(t *testing.T) {
	fail := failFromErrors(fmt.Errorf("private: %w", &net.DNSError{Err: "private-details", Name: "private-host"}))
	if fail.Category != "dns" {
		t.Fatal("failure publication lost wrapped cause")
	}
}
