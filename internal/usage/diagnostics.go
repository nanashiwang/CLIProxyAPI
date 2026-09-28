package usage

import coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"

type retainedDiagnosticTrace struct {
	refs     int
	snapshot *coreusage.Diagnostics
}

// All index operations hold s.mu; snapshots remain immutable.
func (s *RequestStatistics) addDiagnosticIndex(event storedEvent) {
	d := event.Detail.Diagnostics
	if d == nil {
		return
	}
	if s.diagnosticTraces == nil {
		s.diagnosticTraces = make(map[string]retainedDiagnosticTrace)
	}
	entry := s.diagnosticTraces[d.TraceID]
	entry.refs++
	if entry.snapshot == nil || !d.CapturedAt.Before(entry.snapshot.CapturedAt) {
		entry.snapshot = d
	}
	s.diagnosticTraces[d.TraceID] = entry
}
func (s *RequestStatistics) removeDiagnosticIndex(event storedEvent) {
	d := event.Detail.Diagnostics
	if d == nil {
		return
	}
	entry := s.diagnosticTraces[d.TraceID]
	entry.refs--
	if entry.refs <= 0 {
		delete(s.diagnosticTraces, d.TraceID)
	} else {
		s.diagnosticTraces[d.TraceID] = entry
	}
}
func (s *RequestStatistics) rebuildDiagnosticIndex() {
	s.diagnosticTraces = nil
	for _, event := range s.events {
		s.addDiagnosticIndex(event)
	}
}
