package usage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type diagnosticStatusError struct{}

func (diagnosticStatusError) Error() string   { return "private upstream error" }
func (diagnosticStatusError) StatusCode() int { return 429 }
func TestDiagnosticsRetrySnapshotsAndBounds(t *testing.T) {
	root := WithExecutionDiagnostics(context.Background())
	first := StartDiagnosticAttempt(root, "codex", "account-a", "model")
	ObserveDiagnosticEvent(first, "upstream_request")
	ObserveDiagnosticEvent(first, "first_byte")
	FinishDiagnosticAttempt(first, diagnosticStatusError{})
	before := CaptureExecutionDiagnostics(first, Record{Failed: true, Fail: Failure{StatusCode: 429}})
	second := StartDiagnosticAttempt(root, "codex", "account-b", "model")
	ObserveDiagnosticTransport(second, "ws")
	ObserveDiagnosticEvent(second, "interrupt_confirmed")
	after := CaptureExecutionDiagnostics(second, Record{})
	if len(before.Attempts) != 1 || len(after.Attempts) != 2 || after.Attempts[0].StatusCode != 429 || after.Attempts[1].RetryReason != "account_switch" || after.Attempts[1].Outcome != "interrupted" {
		t.Fatalf("incorrect retry facts: %+v", after)
	}
	*before.Attempts[0].FirstByteMs = 999999
	if *CaptureExecutionDiagnostics(second, Record{}).Attempts[0].FirstByteMs == 999999 {
		t.Fatal("mutable snapshot alias")
	}
	ObserveDiagnosticEvent(second, "response_started")
	if CaptureExecutionDiagnostics(second, Record{}).Attempts[1].Outcome != "succeeded" {
		t.Fatal("previous interruption leaked to next duplex response")
	}
	for i := 0; i < 120; i++ {
		ObserveDiagnosticEvent(second, "upstream_request")
	}
	for i := 0; i < 40; i++ {
		StartDiagnosticAttempt(root, "codex", "account-b", "model")
	}
	capped := CaptureExecutionDiagnostics(second, Record{})
	if len(capped.Attempts) != 32 || len(capped.Events) != 96 || !capped.Truncated {
		t.Fatal("unbounded trace")
	}
	encoded, _ := json.Marshal(capped)
	if strings.Contains(string(encoded), "private upstream error") {
		t.Fatal("raw error persisted")
	}
}
func TestDiagnosticsImportAndCancellation(t *testing.T) {
	if NormalizeDiagnostics(&Diagnostics{}) != nil || CaptureExecutionDiagnostics(context.Background(), Record{}) != nil {
		t.Fatal("fabricated old diagnostics")
	}
	root := WithExecutionDiagnostics(context.Background())
	ctx := StartDiagnosticAttempt(root, "p", "a", "m")
	FinishDiagnosticAttempt(ctx, errors.Join(context.Canceled, errors.New("secret")))
	trace, _ := diagnosticValues(ctx)
	if trace.data.Attempts[0].Outcome != "cancelled" {
		t.Fatal("cancellation not observed")
	}
	value := CaptureExecutionDiagnostics(ctx, Record{})
	value.Attempts = append(value.Attempts, value.Attempts[0])
	value.Attempts[0].Transport = "secret"
	value.Attempts[0].Phase = "secret"
	value.Attempts[0].RetryReason = "secret"
	value.Events = append(value.Events, DiagnosticEvent{Attempt: 1, Kind: "secret", OffsetMs: 0}, DiagnosticEvent{Attempt: 1, Kind: "first_byte", OffsetMs: int64(time.Hour / time.Millisecond)})
	clean := NormalizeDiagnostics(value)
	if len(clean.Attempts) != 1 || clean.Attempts[0].Phase != "unknown" || clean.Attempts[0].Transport != "" || len(clean.Events) != 0 {
		t.Fatalf("invalid import not filtered: %+v", clean)
	}
}
