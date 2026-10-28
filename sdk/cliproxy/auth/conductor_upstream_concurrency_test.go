package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func concurrencyTestError() *Error {
	return &Error{Code: ErrorCodeUpstreamConcurrency, Message: `{"error":{"type":"access_terminated_error","message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`, HTTPStatus: 503, Retryable: true}
}

func TestUpstreamConcurrencyFallback(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			m := NewManager(nil, &RoundRobinSelector{}, nil)
			model := "concurrency-test"
			sibling := "concurrency-sibling"
			for _, a := range []*Auth{{ID: "busy", Provider: "kimi", Status: StatusActive, Attributes: map[string]string{"priority": "2"}}, {ID: "ready", Provider: "kimi", Status: StatusActive, Attributes: map[string]string{"priority": "1"}}} {
				registry.GetGlobalRegistry().RegisterClient(a.ID, "kimi", []*registry.ModelInfo{{ID: model}, {ID: sibling}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
			var attempts []string
			e := &customStreamMockExecutor{identifier: "kimi"}
			e.executeFn = func(_ context.Context, a *Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
				attempts = append(attempts, a.ID)
				if a.ID == "busy" {
					return ex.Response{}, concurrencyTestError()
				}
				return ex.Response{Payload: []byte("ok")}, nil
			}
			e.streamFn = func(_ context.Context, a *Auth, _ ex.Request, _ ex.Options) (*ex.StreamResult, error) {
				attempts = append(attempts, a.ID)
				if a.ID == "busy" {
					return nil, concurrencyTestError()
				}
				ch := make(chan ex.StreamChunk, 1)
				ch <- ex.StreamChunk{Payload: []byte("ok")}
				close(ch)
				return &ex.StreamResult{Chunks: ch}, nil
			}
			m.RegisterExecutor(e)
			before := time.Now()
			if stream {
				r, err := m.ExecuteStream(context.Background(), []string{"kimi"}, ex.Request{Model: model}, ex.Options{Stream: true})
				if err != nil {
					t.Fatal(err)
				}
				var out string
				for c := range r.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
					out += string(c.Payload)
				}
				if out != "ok" {
					t.Fatal(out)
				}
			} else {
				r, err := m.Execute(context.Background(), []string{"kimi"}, ex.Request{Model: model}, ex.Options{})
				if err != nil || string(r.Payload) != "ok" {
					t.Fatalf("%s %v", r.Payload, err)
				}
			}
			if len(attempts) != 2 || attempts[0] != "busy" || attempts[1] != "ready" {
				t.Fatalf("attempts=%v", attempts)
			}
			a, _ := m.GetByID("busy")
			s := a.ModelStates[model]
			if s == nil || s.NextRetryAfter.Before(before.Add(2*time.Second)) || s.NextRetryAfter.After(time.Now().Add(3*time.Second)) {
				t.Fatalf("unexpected cooldown: %+v", s)
			}
			if a.Quota.Exceeded || s.Quota.Exceeded {
				t.Fatal("concurrency marked quota exhausted")
			}
			if blocked, _, _ := isAuthBlockedForModel(a, sibling, time.Now()); blocked {
				t.Fatal("unrelated model was blocked")
			}
			if blocked, _, _ := isAuthBlockedForModel(a, model, s.NextRetryAfter.Add(time.Millisecond)); blocked {
				t.Fatal("account did not recover after cooldown")
			}
		})
	}
}

func TestUpstreamConcurrencyDoesNotReplayStartedStream(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	const model = "started-busy-stream"
	for _, id := range []string{"stream-a", "stream-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "kimi", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "kimi", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	attempts := 0
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "kimi", streamFn: func(context.Context, *Auth, ex.Request, ex.Options) (*ex.StreamResult, error) {
		attempts++
		ch := make(chan ex.StreamChunk, 2)
		ch <- ex.StreamChunk{Payload: []byte("data: started\n\n")}
		ch <- ex.StreamChunk{Err: concurrencyTestError()}
		close(ch)
		return &ex.StreamResult{Chunks: ch}, nil
	}})
	r, err := m.ExecuteStream(context.Background(), []string{"kimi"}, ex.Request{Model: model}, ex.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	chunks, failures := 0, 0
	for c := range r.Chunks {
		if len(c.Payload) > 0 {
			chunks++
		}
		if c.Err != nil {
			failures++
		}
	}
	if attempts != 1 || chunks != 1 || failures != 1 {
		t.Fatalf("attempts=%d chunks=%d failures=%d", attempts, chunks, failures)
	}
}

func TestUpstreamConcurrencyCooldownBoundaries(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, disabled := range []bool{false, true} {
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		a := &Auth{ID: "cooldown-boundary", Provider: "kimi", Status: StatusActive, Metadata: map[string]any{"disable_cooling": disabled}}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		m.MarkResult(context.Background(), Result{AuthID: a.ID, Model: "m", Error: concurrencyTestError()})
		got, _ := m.GetByID(a.ID)
		if got.ModelStates["m"].NextRetryAfter.IsZero() != disabled {
			t.Fatalf("disableCooling=%v state=%+v", disabled, got.ModelStates["m"])
		}
	}
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	a := &Auth{ID: "prior-quota", Provider: "kimi", Status: StatusActive}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	retry := time.Minute
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Model: "m", Error: &Error{HTTPStatus: 429, Message: "quota"}, RetryAfter: &retry})
	prior, _ := m.GetByID(a.ID)
	deadline := prior.ModelStates["m"].NextRetryAfter
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Model: "m", Error: concurrencyTestError()})
	after, _ := m.GetByID(a.ID)
	if after.ModelStates["m"].NextRetryAfter.Before(deadline) || !after.ModelStates["m"].Quota.Exceeded {
		t.Fatal("concurrency shortened prior quota cooldown")
	}
}

func TestUpstreamConcurrencyAllBusyIsBounded(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetRetryConfig(0, 0, 0)
	const model = "all-busy-kimi"
	for _, id := range []string{"busy-a", "busy-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "kimi", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "kimi", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	attempts := map[string]int{}
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "kimi", executeFn: func(_ context.Context, a *Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
		attempts[a.ID]++
		return ex.Response{}, concurrencyTestError()
	}})
	_, err := m.Execute(context.Background(), []string{"kimi"}, ex.Request{Model: model}, ex.Options{})
	if err == nil || attempts["busy-a"] != 1 || attempts["busy-b"] != 1 {
		t.Fatalf("err=%v attempts=%v", err, attempts)
	}
}
