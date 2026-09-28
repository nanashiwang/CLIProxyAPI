package usage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const MaxDiagnosticAttempts = 32
const MaxDiagnosticEvents = 96

// Diagnostics contains bounded observations, never request bodies, headers or errors.
// A snapshot ends at its usage publication; it does not predict later retries.
type Diagnostics struct {
	TraceID    string              `json:"trace_id"`
	StartedAt  time.Time           `json:"started_at"`
	CapturedAt time.Time           `json:"captured_at"`
	Attempts   []DiagnosticAttempt `json:"attempts"`
	Events     []DiagnosticEvent   `json:"events"`
	Truncated  bool                `json:"truncated"`
}
type DiagnosticAttempt struct {
	Sequence    int        `json:"sequence"`
	Provider    string     `json:"provider"`
	AuthID      string     `json:"auth_id,omitempty"`
	Model       string     `json:"model,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Outcome     string     `json:"outcome"`
	StatusCode  int        `json:"status_code,omitempty"`
	Phase       string     `json:"phase"`
	RetryReason string     `json:"retry_reason"`
	Transport   string     `json:"transport,omitempty"`
	FirstByteMs *int64     `json:"first_byte_ms,omitempty"`
}
type DiagnosticEvent struct {
	Attempt  int    `json:"attempt"`
	Kind     string `json:"kind"`
	OffsetMs int64  `json:"offset_ms"`
}
type diagnosticTraceKey struct{}
type diagnosticAttemptKey struct{}
type diagnosticTrace struct {
	mu       sync.Mutex
	data     Diagnostics
	sequence int
}

func WithExecutionDiagnostics(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(diagnosticTraceKey{}).(*diagnosticTrace); ok {
		return ctx
	}
	return context.WithValue(ctx, diagnosticTraceKey{}, &diagnosticTrace{data: Diagnostics{TraceID: uuid.NewString(), StartedAt: time.Now().UTC()}})
}

// StartDiagnosticAttempt is called exactly at the executor invocation boundary.
func StartDiagnosticAttempt(ctx context.Context, provider, authID, model string) context.Context {
	trace, _ := ctx.Value(diagnosticTraceKey{}).(*diagnosticTrace)
	if trace == nil {
		return ctx
	}
	trace.mu.Lock()
	trace.sequence++
	seq := trace.sequence
	if len(trace.data.Attempts) >= MaxDiagnosticAttempts {
		trace.data.Truncated = true
	} else {
		reason := "initial"
		if n := len(trace.data.Attempts); n > 0 {
			previous := trace.data.Attempts[n-1]
			reason = "same_account_retry"
			if previous.AuthID != authID || previous.Provider != provider {
				reason = "account_switch"
			} else if previous.Model != model {
				reason = "model_fallback"
			}
		}
		trace.data.Attempts = append(trace.data.Attempts, DiagnosticAttempt{Sequence: seq, Provider: provider, AuthID: authID, Model: model, StartedAt: time.Now().UTC(), Outcome: "running", Phase: "executor", RetryReason: reason})
	}
	trace.mu.Unlock()
	return context.WithValue(ctx, diagnosticAttemptKey{}, seq)
}

func diagnosticValues(ctx context.Context) (*diagnosticTrace, int) {
	if ctx == nil {
		return nil, 0
	}
	trace, _ := ctx.Value(diagnosticTraceKey{}).(*diagnosticTrace)
	seq, _ := ctx.Value(diagnosticAttemptKey{}).(int)
	return trace, seq
}
func FinishDiagnosticAttempt(ctx context.Context, err error) {
	trace, seq := diagnosticValues(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if seq <= 0 || seq > len(trace.data.Attempts) {
		return
	}
	a := &trace.data.Attempts[seq-1]
	if a.EndedAt != nil {
		return
	}
	now := time.Now().UTC()
	a.EndedAt = &now
	if a.Outcome != "interrupted" {
		a.Outcome = "succeeded"
	}
	if err != nil {
		a.Outcome = "failed"
		if errors.Is(err, context.Canceled) {
			a.Outcome = "cancelled"
		}
		var status interface{ StatusCode() int }
		if errors.As(err, &status) {
			a.StatusCode = status.StatusCode()
		}
	}
}

var diagnosticKinds = map[string]bool{
	"upstream_request": true, "upstream_headers": true, "first_byte": true,
	"response_started": true, "interrupt_sent": true, "interrupt_confirmed": true,
	"interrupt_rejected": true, "http_size_fallback": true, "replay_required": true,
	"buffer_byte_limit": true, "buffer_event_limit": true, "buffer_output": true,
	"buffer_terminal": true, "buffer_upstream_error": true,
}

func ObserveDiagnosticEvent(ctx context.Context, kind string) {
	trace, seq := diagnosticValues(ctx)
	if trace == nil || !diagnosticKinds[kind] {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if seq <= 0 || seq > len(trace.data.Attempts) {
		return
	}
	a := &trace.data.Attempts[seq-1]
	switch kind {
	case "upstream_request":
		a.Phase = "upstream_request"
	case "upstream_headers":
		a.Phase = "upstream_headers"
	case "first_byte":
		if a.FirstByteMs != nil {
			return
		}
		ms := max(int64(0), time.Since(a.StartedAt).Milliseconds())
		a.FirstByteMs = &ms
		a.Phase = "response_body"
	case "response_started":
		a.Phase = "response_body"
		a.Outcome = "running"
	case "interrupt_confirmed":
		a.Outcome = "interrupted"
	}
	if len(trace.data.Events) >= MaxDiagnosticEvents {
		trace.data.Truncated = true
		return
	}
	trace.data.Events = append(trace.data.Events, DiagnosticEvent{Attempt: seq, Kind: kind, OffsetMs: max(int64(0), time.Since(trace.data.StartedAt).Milliseconds())})
}
func ObserveDiagnosticTransport(ctx context.Context, transport string) {
	trace, seq := diagnosticValues(ctx)
	transport = NormalizeTransport(transport)
	if trace == nil || transport == "" {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if seq > 0 && seq <= len(trace.data.Attempts) {
		trace.data.Attempts[seq-1].Transport = transport
	}
}

func CaptureExecutionDiagnostics(ctx context.Context, record Record) *Diagnostics {
	trace, seq := diagnosticValues(ctx)
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	value := trace.data
	value.CapturedAt = time.Now().UTC()
	value.Attempts = append([]DiagnosticAttempt(nil), trace.data.Attempts...)
	value.Events = append([]DiagnosticEvent(nil), trace.data.Events...)
	if seq > 0 && seq <= len(value.Attempts) {
		a := &value.Attempts[seq-1]
		a.EndedAt = &value.CapturedAt
		if a.Outcome == "running" {
			a.Outcome = "succeeded"
		}
		if record.Failed {
			a.Outcome = "failed"
			a.StatusCode = record.Fail.StatusCode
			if errors.Is(ctx.Err(), context.Canceled) {
				a.Outcome = "cancelled"
			}
		}
		if transport := NormalizeTransport(record.UpstreamTransport); transport != "" {
			a.Transport = transport
		}
	}
	return NormalizeDiagnostics(&value)
}

// NormalizeDiagnostics is also applied to imports. Only enumerated facts survive.
func NormalizeDiagnostics(value *Diagnostics) *Diagnostics {
	if value == nil || value.StartedAt.IsZero() || value.CapturedAt.Before(value.StartedAt) {
		return nil
	}
	if _, err := uuid.Parse(value.TraceID); err != nil {
		return nil
	}
	out := *value
	out.Attempts = nil
	out.Events = nil
	if len(value.Attempts) > MaxDiagnosticAttempts || len(value.Events) > MaxDiagnosticEvents {
		out.Truncated = true
	}
	text := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) > 512 {
			return ""
		}
		return s
	}
	seen := make(map[int]bool)
	for _, a := range value.Attempts[:min(len(value.Attempts), MaxDiagnosticAttempts)] {
		if a.Sequence <= 0 || a.Sequence > MaxDiagnosticAttempts || a.StartedAt.Before(value.StartedAt) || a.StartedAt.After(value.CapturedAt) || seen[a.Sequence] {
			continue
		}
		seen[a.Sequence] = true
		a.Provider, a.AuthID, a.Model = text(a.Provider), text(a.AuthID), text(a.Model)
		a.Transport = NormalizeTransport(a.Transport)
		if a.StatusCode < 100 || a.StatusCode > 599 {
			a.StatusCode = 0
		}
		switch a.Outcome {
		case "running", "succeeded", "failed", "cancelled", "interrupted":
		default:
			a.Outcome = "unknown"
		}
		switch a.Phase {
		case "executor", "upstream_request", "upstream_headers", "response_body":
		default:
			a.Phase = "unknown"
		}
		switch a.RetryReason {
		case "initial", "same_account_retry", "account_switch", "model_fallback":
		default:
			a.RetryReason = "unknown"
		}
		if a.EndedAt != nil {
			t := *a.EndedAt
			if t.Before(a.StartedAt) || t.After(value.CapturedAt) {
				a.EndedAt = nil
			} else {
				a.EndedAt = &t
			}
		}
		if a.FirstByteMs != nil {
			ms := *a.FirstByteMs
			if ms < 0 {
				a.FirstByteMs = nil
			} else {
				a.FirstByteMs = &ms
			}
		}
		out.Attempts = append(out.Attempts, a)
	}
	for _, e := range value.Events[:min(len(value.Events), MaxDiagnosticEvents)] {
		if diagnosticKinds[e.Kind] && seen[e.Attempt] && e.OffsetMs >= 0 && e.OffsetMs <= value.CapturedAt.Sub(value.StartedAt).Milliseconds() {
			out.Events = append(out.Events, e)
		}
	}
	return &out
}
