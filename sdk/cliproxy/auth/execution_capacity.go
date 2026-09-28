package auth

import (
	"context"
	"sync"
	"time"

	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// ExecutionCapacity describes this CPA process, never upstream or lease capacity.
type ExecutionCapacity struct {
	Active     int64     `json:"active"`
	Limit      *int64    `json:"limit"`
	Unlimited  bool      `json:"unlimited"`
	Scope      string    `json:"scope"`
	ObservedAt time.Time `json:"observed_at"`
}

func (m *Manager) ExecutionCapacity(authID string) ExecutionCapacity {
	m.executionMu.Lock()
	active := m.executionActive[authID]
	m.executionMu.Unlock()
	// Local execution imposes no per-account concurrency cap. Home may impose
	// a shared cap which cannot be inferred from this process's observations.
	return ExecutionCapacity{Active: active, Unlimited: !m.HomeEnabled(), Scope: "local", ObservedAt: time.Now().UTC()}
}

func (m *Manager) executionActivity(authID string) (func(bool), func()) {
	var mu sync.Mutex
	active, ended := false, false
	set := func(next bool, end bool) {
		mu.Lock()
		defer mu.Unlock()
		if ended {
			return
		}
		if active != next {
			m.executionMu.Lock()
			if m.executionActive == nil {
				m.executionActive = make(map[string]int64)
			}
			if next {
				m.executionActive[authID]++
			} else {
				m.executionActive[authID]--
			}
			if m.executionActive[authID] == 0 {
				delete(m.executionActive, authID)
			}
			m.executionMu.Unlock()
			active = next
		}
		ended = end
	}
	set(true, false)
	return func(active bool) { set(active, false) }, func() { set(false, true) }
}

type executionAttemptsKey struct{}
type executionAttempts struct {
	mu     sync.Mutex
	origin string
}

func withExecutionAttempts(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(executionAttemptsKey{}).(*executionAttempts); ok {
		return ctx
	}
	return context.WithValue(ctx, executionAttemptsKey{}, &executionAttempts{})
}

func executionAttemptContext(ctx context.Context, auth *Auth, req core.Request, opts core.Options) (context.Context, error) {
	if state, ok := ctx.Value(executionAttemptsKey{}).(*executionAttempts); ok && auth != nil {
		state.mu.Lock()
		if state.origin == "" {
			state.origin = auth.ID
		}
		switched := state.origin != auth.ID
		state.mu.Unlock()
		if switched && auth.Provider == "codex" && (core.RequiredUpstreamWebsocket(ctx) || gjson.GetBytes(req.Payload, "previous_response_id").String() != "" || gjson.GetBytes(opts.OriginalRequest, "previous_response_id").String() != "" || gjson.GetBytes(opts.OriginalRequest, "type").String() == "response.append") {
			return ctx, core.NewUpstreamWebsocketReplayRequiredError()
		}
		ctx = core.WithAccountSwitched(ctx, switched)
	}
	return ctx, nil
}

func (m *Manager) executeObserved(ctx context.Context, executor ProviderExecutor, auth *Auth, req core.Request, opts core.Options) (core.Response, error) {
	ctx, err := executionAttemptContext(ctx, auth, req, opts)
	if err != nil {
		return core.Response{}, err
	}
	_, end := m.executionActivity(auth.ID)
	defer end()
	return executor.Execute(ctx, auth, req, opts)
}

func (m *Manager) executeStreamObserved(ctx context.Context, executor ProviderExecutor, auth *Auth, req core.Request, opts core.Options) (*core.StreamResult, error) {
	ctx, err := executionAttemptContext(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	observe, end := m.executionActivity(auth.ID)
	result, err := executor.ExecuteStream(core.WithExecutionActivity(ctx, observe), auth, req, opts)
	if err != nil || result == nil || result.Chunks == nil {
		end()
		return result, err
	}
	out := make(chan core.StreamChunk)
	go func() {
		defer close(out)
		defer end()
		// The conductor owns cancellation and drains every producer. Preserve
		// tail errors so observation cannot change result/cooldown semantics.
		// Occupancy ends only after the producer has closed, not on cancellation.
		for chunk := range result.Chunks {
			out <- chunk
		}
	}()
	return &core.StreamResult{Headers: result.Headers, Chunks: out}, nil
}
