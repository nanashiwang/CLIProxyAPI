package auth

import (
	"context"
	"errors"
	"testing"

	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type capacityExecutor struct {
	ProviderExecutor
	stream  func(context.Context) (*core.StreamResult, error)
	execute func(context.Context) (core.Response, error)
}

func (e capacityExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ core.Request, _ core.Options) (*core.StreamResult, error) {
	return e.stream(ctx)
}
func (e capacityExecutor) Execute(ctx context.Context, _ *Auth, _ core.Request, _ core.Options) (core.Response, error) {
	return e.execute(ctx)
}

func TestExecutionCapacityTracksProducerAndDuplexIdle(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "capacity", Provider: "codex"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	producer := make(chan core.StreamChunk)
	var observe func(bool)
	e := capacityExecutor{stream: func(ctx context.Context) (*core.StreamResult, error) {
		observe = func(active bool) { core.ObserveExecutionActivity(ctx, active) }
		if m.ExecutionCapacity(a.ID).Active != 1 {
			t.Fatal("execution not observed before provider call")
		}
		return &core.StreamResult{Chunks: producer}, nil
	}}
	result, err := m.executeStreamObserved(ctx, e, a, core.Request{}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	observe(false)
	if m.ExecutionCapacity(a.ID).Active != 0 {
		t.Fatal("idle duplex counted as execution")
	}
	observe(true)
	observe(true)
	if m.ExecutionCapacity(a.ID).Active != 1 {
		t.Fatal("activity observation double counted")
	}
	cancel()
	if m.ExecutionCapacity(a.ID).Active != 1 {
		t.Fatal("cancellation freed a live producer")
	}
	close(producer)
	for range result.Chunks {
	}
	if got := m.ExecutionCapacity(a.ID); got.Active != 0 || !got.Unlimited || got.Limit != nil || got.Scope != "local" {
		t.Fatalf("capacity after drain: %+v", got)
	}
	observe(true)
	if m.ExecutionCapacity(a.ID).Active != 0 {
		t.Fatal("late callback resurrected execution")
	}
}
func TestExecutionCapacityFailureAndCrossAccountContinuation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ctx := withExecutionAttempts(context.Background())
	first := &Auth{ID: "first", Provider: "codex"}
	second := &Auth{ID: "second", Provider: "codex"}
	e := capacityExecutor{execute: func(ctx context.Context) (core.Response, error) { return core.Response{}, errors.New("refused") }}
	_, _ = m.executeObserved(ctx, e, first, core.Request{}, core.Options{})
	if m.ExecutionCapacity(first.ID).Active != 0 {
		t.Fatal("failed request retained occupancy")
	}
	e.execute = func(ctx context.Context) (core.Response, error) {
		if !core.AccountSwitched(ctx) {
			t.Fatal("missing proven account switch")
		}
		return core.Response{}, nil
	}
	if _, err := m.executeObserved(ctx, e, second, core.Request{}, core.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.executeObserved(ctx, e, second, core.Request{Payload: []byte(`{"previous_response_id":"first-response"}`)}, core.Options{}); !core.IsUpstreamWebsocketReplayRequired(err) {
		t.Fatalf("unsafe continuation: %v", err)
	}
}
