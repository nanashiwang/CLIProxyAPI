package auth

import (
	"context"
	"errors"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"testing"
)

func TestObservedStreamFailurePrecedesNextAttempt(t *testing.T) {
	m := NewManager(nil, nil, nil)
	root := withExecutionAttempts(context.Background())
	first := &Auth{ID: "first", Provider: "codex"}
	second := &Auth{ID: "second", Provider: "codex"}
	producer := make(chan core.StreamChunk, 1)
	producer <- core.StreamChunk{Err: errors.New("private failure")}
	close(producer)
	executor := capacityExecutor{stream: func(context.Context) (*core.StreamResult, error) { return &core.StreamResult{Chunks: producer}, nil }}
	result, err := m.executeStreamObserved(root, executor, first, core.Request{Model: "model"}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	chunk := <-result.Chunks
	if chunk.Err == nil {
		t.Fatal("failure was swallowed")
	}
	executor.execute = func(ctx context.Context) (core.Response, error) {
		snapshot := usage.CaptureExecutionDiagnostics(ctx, usage.Record{})
		if snapshot == nil || len(snapshot.Attempts) != 2 || snapshot.Attempts[0].Outcome != "failed" || snapshot.Attempts[1].RetryReason != "account_switch" {
			t.Fatalf("missing actual executor retry observations: %+v", snapshot)
		}
		return core.Response{}, nil
	}
	if _, err = m.executeObserved(root, executor, second, core.Request{Model: "model"}, core.Options{}); err != nil {
		t.Fatal(err)
	}
	for range result.Chunks {
	}
	if m.ExecutionCapacity("first").Active != 0 || m.ExecutionCapacity("second").Active != 0 {
		t.Fatal("diagnostics changed occupancy lifecycle")
	}
}
