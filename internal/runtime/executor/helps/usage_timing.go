package helps

import (
	"bytes"
	"math"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

const timingFrameLimit = 256 << 10

// timingObserver is a bounded observer, never a delivery or retry buffer.
// Oversized frames are ignored without changing the forwarded stream.
type timingObserver struct {
	sse         bool
	line, frame []byte
	overflow    bool
	emit        func([]byte)
}

func (o *timingObserver) Feed(data []byte) {
	if !o.sse {
		if o.overflow {
			return
		}
		if len(o.frame)+len(data) > timingFrameLimit {
			o.frame = nil
			o.overflow = true
			return
		}
		o.frame = append(o.frame, data...)
		return
	}
	for _, b := range data {
		if b == '\n' {
			if !o.overflow {
				line := bytes.TrimSuffix(o.line, []byte{'\r'})
				if bytes.HasPrefix(line, []byte("data:")) {
					payload := bytes.TrimSpace(line[5:])
					if len(o.frame)+len(payload)+1 <= timingFrameLimit {
						o.frame = append(o.frame, payload...)
						// Existing executors consume complete single-line JSON before
						// the next blank line; observe at that same boundary.
						if gjson.ValidBytes(o.frame) {
							o.emit(o.frame)
							o.frame = nil
						} else {
							o.frame = append(o.frame, '\n')
						}
					} else {
						o.overflow = true
						o.frame = nil
					}
				}
			}
			if len(bytes.TrimSpace(o.line)) == 0 {
				o.frame = nil
				o.overflow = false
			}
			o.line = o.line[:0]
			continue
		}
		if len(o.line) < timingFrameLimit {
			o.line = append(o.line, b)
		} else {
			o.overflow = true
			o.frame = nil
		}
	}
}

func (o *timingObserver) Finish() {
	if !o.overflow {
		if o.sse {
			o.Feed([]byte{'\n'})
		} else if gjson.ValidBytes(o.frame) {
			o.emit(o.frame)
		}
	}
	o.line = nil
	o.frame = nil
}

func (r *UsageReporter) newTimingObserver(contentType string, generation uint64) *timingObserver {
	return &timingObserver{sse: strings.Contains(strings.ToLower(contentType), "text/event-stream"), emit: func(payload []byte) { r.observeTimingPayload(payload, generation) }}
}

func (r *UsageReporter) observeTimingPayload(payload []byte, generation uint64) {
	if len(payload) > timingFrameLimit || !gjson.ValidBytes(payload) {
		return
	}
	v := gjson.ParseBytes(payload)
	kind := v.Get("type").String()
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	if generation != r.modelGeneration {
		return
	}
	if r.timing == nil {
		r.timing = &usage.ResponseTiming{}
	}
	t := r.timing
	elapsed := float64(time.Since(r.requestedAt)) / float64(time.Millisecond)
	first := func(p **float64) {
		if *p == nil {
			n := elapsed
			*p = &n
		}
	}
	text, reasoning, output := false, false, false
	switch kind {
	case "response.output_item.added":
		output = true
	case "response.output_text.delta":
		text = v.Get("delta").String() != ""
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		reasoning = v.Get("delta").String() != ""
	case "response.function_call_arguments.delta":
		output = v.Get("delta").String() != ""
	case "content_block_delta":
		text = v.Get("delta.text").String() != ""
		reasoning = v.Get("delta.thinking").String() != ""
		output = v.Get("delta.partial_json").String() != ""
	case "content_block_start":
		text = v.Get("content_block.text").String() != ""
		reasoning = v.Get("content_block.thinking").String() != ""
		output = v.Get("content_block.type").String() == "tool_use"
	}
	for _, choice := range v.Get("choices").Array() {
		text = text || choice.Get("delta.content").String() != ""
		reasoning = reasoning || choice.Get("delta.reasoning_content").String() != "" || choice.Get("delta.reasoning").String() != ""
		for _, call := range choice.Get("delta.tool_calls").Array() {
			output = output || call.Get("function.name").String() != "" || call.Get("function.arguments").String() != ""
		}
	}
	for _, candidate := range v.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("thought").Bool() {
				reasoning = reasoning || part.Get("text").String() != ""
			} else {
				text = text || part.Get("text").String() != ""
			}
			output = output || part.Get("functionCall").IsObject()
		}
	}
	if text {
		first(&t.FirstTextMs)
	}
	if reasoning {
		first(&t.FirstReasoningMs)
	}
	if output || text || reasoning {
		first(&t.FirstOutputMs)
	}
	if kind != "response.completed" {
		return
	}
	created, completed := v.Get("response.created_at"), v.Get("response.completed_at")
	if created.Type == gjson.Number && completed.Type == gjson.Number && created.Float() >= 0 && completed.Float() > created.Float() {
		n := (completed.Float() - created.Float()) * 1000
		if !math.IsInf(n, 0) && n < float64(math.MaxInt64)/1e6 {
			t.UpstreamResponseMs = &n
		}
	}
	// Only complete response-scoped metrics belong to this billing record.
	metrics := v.Get("timing_metrics.critical_path")
	if metrics.Get("scope").String() == "response" && metrics.Get("coverage").String() == "complete" && metrics.Get("boundary_type").String() == "actionable_output_item_done" {
		number := func(path string) *float64 {
			value := metrics.Get(path)
			n := value.Float()
			if value.Type != gjson.Number || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return nil
			}
			return &n
		}
		t.UpstreamEngineMs = number("engine_wall_ms")
		before, other := number("responses_pre_inference_ms"), number("responses_other_ms")
		if before != nil && other != nil {
			n := *before + *other
			t.UpstreamOverheadMs = &n
		}
	}
}

func (r *UsageReporter) responseTiming() *usage.ResponseTiming {
	r.modelMu.RLock()
	defer r.modelMu.RUnlock()
	return usage.NormalizeResponseTiming(r.timing)
}
