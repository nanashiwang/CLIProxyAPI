package auth

import (
	"context"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"testing"
	"time"
)

type noMoreRetryTestError struct {
	stop bool
	code int
}

func (e noMoreRetryTestError) Error() string                 { return "test retry metadata" }
func (e noMoreRetryTestError) StatusCode() int               { return e.code }
func (e noMoreRetryTestError) SameAccountRetryBlocked() bool { return true }
func (e noMoreRetryTestError) IsRequestStop() bool           { return e.stop }

func TestNoMoreRetryStopsRequestDespiteContinueRule(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		m := NewManager(nil, nil, nil)
		calls := 0
		exec := &mockCustomErrorExecutor{identifier: "codex"}
		run := func(context.Context, *Auth, core.Request, core.Options) (core.Response, error) {
			calls++
			return core.Response{}, noMoreRetryTestError{stop: true, code: 502}
		}
		exec.executeFn, exec.countFn = run, run
		m.RegisterExecutor(exec)
		reg := registry.GetGlobalRegistry()
		for _, id := range []string{"retry-meta-a", "retry-meta-b"} {
			a := &Auth{ID: id, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{Status: 502, Match: []string{"test retry metadata"}, Action: "continue"}}}}
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "test-model"}})
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		if countTokens {
			_, err = m.ExecuteCount(context.Background(), []string{"codex"}, core.Request{Model: "test-model"}, core.Options{})
		} else {
			_, err = m.Execute(context.Background(), []string{"codex"}, core.Request{Model: "test-model"}, core.Options{})
		}
		reg.UnregisterClient("retry-meta-a")
		reg.UnregisterClient("retry-meta-b")
		if err == nil || calls != 1 {
			t.Fatalf("count=%v calls=%d error=%v", countTokens, calls, err)
		}
	}
}

func TestNoMoreRetrySkipsWaitAndCredentialRefresh(t *testing.T) {
	m := NewManager(nil, nil, nil)
	err := noMoreRetryTestError{code: 429}
	if _, retry := m.shouldRetryAfterError(err, 0, []string{"codex"}, "m", time.Second); retry {
		t.Fatal("retry sleep allowed")
	}
	a := &Auth{ID: "a", Provider: "codex", Metadata: map[string]any{"access_token": "test"}}
	if _, ok := m.tryRefreshAfterUnauthorized(context.Background(), a, noMoreRetryTestError{code: 401, stop: true}, false); ok {
		t.Fatal("local refresh allowed")
	}
	if _, ok, err := m.tryRefreshExecutionAuthAfterUnauthorized(context.Background(), &mockCustomErrorExecutor{}, a, noMoreRetryTestError{code: 401, stop: true}, false, true); ok || err != nil {
		t.Fatal("Home refresh allowed")
	}
}

func TestNoMoreRetrySkipsStreamAliasPool(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		m := NewManager(nil, nil, nil)
		calls := 0
		exec := &customStreamMockExecutor{identifier: "codex"}
		exec.streamFn = func(context.Context, *Auth, core.Request, core.Options) (*core.StreamResult, error) {
			calls++
			err := noMoreRetryTestError{code: 429}
			if !bootstrap {
				return nil, err
			}
			chunks := make(chan core.StreamChunk, 1)
			chunks <- core.StreamChunk{Err: err}
			close(chunks)
			return &core.StreamResult{Chunks: chunks}, nil
		}
		a := &Auth{ID: "stream-retry-meta", Provider: "codex", Status: StatusActive}
		_, err := m.executeStreamWithModelPool(context.Background(), exec, a, "codex", core.Request{Model: "alias"}, core.Options{}, "alias", "", []string{"m1", "m2"}, true, OAuthModelAliasResult{}, nil, true, false, nil)
		if err == nil || calls != 1 || !sameAccountRetryBlocked(err) {
			t.Fatalf("bootstrap=%v calls=%d err=%v", bootstrap, calls, err)
		}
	}
}
