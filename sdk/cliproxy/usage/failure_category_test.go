package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestFailureCauseCategoriesAreRedactedAndFollowWrappers(t *testing.T) {
	ctx := StartDiagnosticAttempt(WithExecutionDiagnostics(context.Background()), "test", "account", "model")
	FinishDiagnosticAttempt(ctx, fmt.Errorf("private-token: %w", &net.DNSError{Err: "private-secret", Name: "private-host"}))
	d := CaptureExecutionDiagnostics(ctx, Record{})
	if len(d.Attempts) != 1 || d.Attempts[0].FailureCategory != "dns" {
		t.Fatalf("wrapped DNS cause lost: %+v", d)
	}
	raw, _ := json.Marshal(d)
	if string(raw) == "" {
		t.Fatal("empty diagnostics")
	}
	for _, secret := range []string{"private-token", "private-secret", "private-host"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("cause leaked raw error")
		}
	}
	d.Attempts[0].FailureCategory = "private-secret"
	if NormalizeDiagnostics(d).Attempts[0].FailureCategory != "unknown" {
		t.Fatal("imported category bypassed allowlist")
	}
	if FailureCategory(errors.Join(errors.New("outer"), context.Canceled)) != "cancelled" {
		t.Fatal("joined cause lost")
	}
}

func TestFailureCategoryCapturedBeforeAttemptFinishes(t *testing.T) {
	ctx := StartDiagnosticAttempt(WithExecutionDiagnostics(context.Background()), "test", "account", "model")
	snapshot := CaptureExecutionDiagnostics(ctx, Record{Failed: true, Fail: Failure{StatusCode: 502, Category: "dns"}})
	if snapshot.Attempts[0].FailureCategory != "dns" {
		t.Fatal("publication lost the cause before deferred attempt finish")
	}
}
