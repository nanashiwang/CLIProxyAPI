package usage

import "math"

// ResponseTiming keeps local elapsed observations separate from upstream clocks.
// Missing historical or unreported metrics must remain unknown, not zero.
type ResponseTiming struct {
	FirstByteMs        *float64 `json:"first_byte_ms,omitempty"`
	FirstOutputMs      *float64 `json:"first_output_ms,omitempty"`
	FirstTextMs        *float64 `json:"first_text_ms,omitempty"`
	FirstReasoningMs   *float64 `json:"first_reasoning_ms,omitempty"`
	UpstreamResponseMs *float64 `json:"upstream_response_ms,omitempty"`
	UpstreamEngineMs   *float64 `json:"upstream_engine_ms,omitempty"`
	UpstreamOverheadMs *float64 `json:"upstream_overhead_ms,omitempty"`
}

// NormalizeResponseTiming detaches pointers and rejects invalid imported facts.
func NormalizeResponseTiming(value *ResponseTiming) *ResponseTiming {
	if value == nil {
		return nil
	}
	result := *value
	for _, field := range []**float64{&result.FirstByteMs, &result.FirstOutputMs, &result.FirstTextMs, &result.FirstReasoningMs, &result.UpstreamResponseMs, &result.UpstreamEngineMs, &result.UpstreamOverheadMs} {
		if *field == nil {
			continue
		}
		n := **field
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64(math.MaxInt64)/1e6 {
			*field = nil
		} else {
			*field = &n
		}
	}
	return &result
}
