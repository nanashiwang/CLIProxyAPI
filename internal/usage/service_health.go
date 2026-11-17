package usage

// ServiceHealth is additive: legacy request totals and billing remain unchanged.
// It counts retained usage records, not inferred final outcomes of retry chains.
type ServiceHealth struct {
	Succeeded    int64    `json:"succeeded"`
	Failed       int64    `json:"failed"`
	Incomplete   int64    `json:"incomplete"`
	Cancelled    int64    `json:"cancelled"`
	CallerErrors int64    `json:"caller_errors"`
	Unknown      int64    `json:"unknown"`
	Samples      int64    `json:"samples"`
	SuccessRate  *float64 `json:"success_rate"`
	ErrorRate    *float64 `json:"error_rate"`
}

func serviceOutcome(failed bool, category string, status int) string {
	if category == "cancelled" || status == 499 {
		return "cancelled"
	}
	if category == "stream_incomplete" {
		return "incomplete"
	}
	if !failed {
		return "succeeded"
	}
	// Upstream authentication and capacity errors are service failures, not caller faults.
	if category == "authentication" || category == "rate_limited" || status == 401 || status == 403 || status == 429 {
		return "failed"
	}
	if status >= 500 || category == "timeout" || category == "dns" || category == "tls" || category == "connection_refused" || category == "connection_closed" || category == "upstream_server" {
		return "failed"
	}
	if category == "invalid_request" || (status >= 400 && status < 500) {
		return "caller_error"
	}
	return "unknown"
}

func (s *ServiceHealth) add(d RequestDetail) {
	outcome := d.ServiceOutcome
	// Legacy successful delivery and explicit HTTP outcomes remain usable; missing
	// failure evidence cannot identify cancellation, recovery or incomplete work.
	if outcome == "" {
		outcome = serviceOutcome(d.Failed, "", d.StatusCode)
	}
	switch outcome {
	case "succeeded":
		s.Succeeded++
	case "failed":
		s.Failed++
	case "incomplete":
		s.Incomplete++
	case "cancelled":
		s.Cancelled++
	case "caller_error":
		s.CallerErrors++
	default:
		s.Unknown++
	}
}

func (s ServiceHealth) rates() ServiceHealth {
	s.Samples = s.Succeeded + s.Failed + s.Incomplete
	if s.Samples > 0 {
		success := float64(s.Succeeded) / float64(s.Samples)
		failure := float64(s.Failed+s.Incomplete) / float64(s.Samples)
		s.SuccessRate, s.ErrorRate = &success, &failure
	}
	return s
}
