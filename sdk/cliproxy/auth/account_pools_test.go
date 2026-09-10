package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type poolCaptureExecutor struct {
	mu       sync.Mutex
	ids      []string
	failures map[string]bool
}

func (*poolCaptureExecutor) Identifier() string { return "pool-test" }
func (e *poolCaptureExecutor) Execute(_ context.Context, a *Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids = append(e.ids, a.ID)
	if e.failures[a.ID] {
		return coreexecutor.Response{}, &Error{HTTPStatus: 503, Message: "upstream unavailable"}
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *poolCaptureExecutor) CountTokens(ctx context.Context, a *Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *poolCaptureExecutor) ExecuteStream(ctx context.Context, a *Auth, r coreexecutor.Request, o coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	resp, err := e.Execute(ctx, a, r, o)
	if err != nil {
		return nil, err
	}
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Payload: resp.Payload}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func (*poolCaptureExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (*poolCaptureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

func newPoolManager(t *testing.T) (*Manager, *config.Config, *poolCaptureExecutor, []string) {
	t.Helper()
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetRetryConfig(0, 0, 0)
	e := &poolCaptureExecutor{failures: map[string]bool{}}
	m.RegisterExecutor(e)
	root := uuid.NewString()
	ids := []string{root + "-a1", root + "-a2", root + "-b", root + "-default"}
	reg := registry.GetGlobalRegistry()
	for _, id := range ids {
		reg.RegisterClient(id, "pool-test", []*registry.ModelInfo{{ID: "pool-model"}})
		a := &Auth{ID: id, Provider: "pool-test", Status: StatusActive}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"key-a", "key-b", "key-all", "key-multi", "key-default"}, AccountPools: config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{
		{ID: "default", Name: "Default"}, {ID: "a", Name: "A", CredentialIDs: ids[:2]}, {ID: "b", Name: "B", CredentialIDs: ids[2:3]},
	}, KeyRules: []config.AccountPoolKeyRule{
		{KeyHash: config.ClientKeyFingerprint("key-a"), Scope: "selected", GroupIDs: []string{"a"}},
		{KeyHash: config.ClientKeyFingerprint("key-b"), Scope: "selected", GroupIDs: []string{"b"}},
		{KeyHash: config.ClientKeyFingerprint("key-all"), Scope: "all"},
		{KeyHash: config.ClientKeyFingerprint("key-multi"), Scope: "selected", GroupIDs: []string{"a", "b"}},
	}}}}
	m.SetConfig(cfg)
	return m, cfg, e, ids
}
func poolCaller(key string) context.Context {
	return sdkaccess.WithClientKeyIdentity(context.Background(), key)
}
func TestAccountPoolsConstrainEveryExecutionModeAndRetry(t *testing.T) {
	for _, mode := range []string{"normal", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m, _, e, ids := newPoolManager(t)
			e.failures[ids[0]] = true
			e.failures[ids[1]] = true
			req := coreexecutor.Request{Model: "pool-model"}
			opts := coreexecutor.Options{}
			var err error
			switch mode {
			case "normal":
				_, err = m.Execute(poolCaller("key-a"), []string{"pool-test"}, req, opts)
			case "count":
				_, err = m.ExecuteCount(poolCaller("key-a"), []string{"pool-test"}, req, opts)
			case "stream":
				_, err = m.ExecuteStream(poolCaller("key-a"), []string{"pool-test"}, req, opts)
			}
			if err == nil || len(e.ids) != 2 {
				t.Fatalf("expected both authorized accounts to fail: ids=%v err=%v", e.ids, err)
			}
			for _, id := range e.ids {
				if id != ids[0] && id != ids[1] {
					t.Fatal("retry escaped group")
				}
			}
		})
	}
}
func TestAccountPoolsSingleMultiAllAndDefaultSelection(t *testing.T) {
	m, _, _, ids := newPoolManager(t)
	for _, tc := range []struct {
		key      string
		expected map[string]bool
	}{
		{"key-a", map[string]bool{ids[0]: true, ids[1]: true}}, {"key-b", map[string]bool{ids[2]: true}}, {"key-multi", map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}}, {"key-all", map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true, ids[3]: true}}, {"key-default", map[string]bool{ids[3]: true}},
	} {
		seen := map[string]bool{}
		for i := 0; i < 12; i++ {
			a, err := m.SelectAuth(poolCaller(tc.key), "pool-test", "pool-model", coreexecutor.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.expected[a.ID] {
				t.Fatalf("%s selected unauthorized %s", tc.key, a.ID)
			}
			seen[a.ID] = true
		}
		if len(seen) != len(tc.expected) {
			t.Fatalf("%s did not rotate within group: %v", tc.key, seen)
		}
	}
}
func TestAccountPoolsPinnedAuthAndForgedMetadataCannotEscape(t *testing.T) {
	m, _, _, ids := newPoolManager(t)
	for _, ctx := range []context.Context{context.Background(), poolCaller("invalid"), poolCaller("key-a")} {
		_, err := m.SelectAuth(ctx, "pool-test", "pool-model", coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: ids[2], "group-ids": []string{"b"}, coreexecutor.AccountPoolNamespaceMetadataKey: "forged"}})
		if err == nil {
			t.Fatal("unauthorized pinned credential accepted")
		}
	}
}
func TestAccountPoolsPolicyChangeRevokesExistingContextAndBinding(t *testing.T) {
	m, cfg, _, ids := newPoolManager(t)
	ctx := poolCaller("key-a")
	if err := m.CheckAccountPoolAccess(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	cfg.AccountPools.KeyRules[0].GroupIDs = []string{"b"}
	m.SetConfig(cfg)
	if m.CheckAccountPoolAccess(ctx, ids[0]) == nil {
		t.Fatal("old binding survived policy revocation")
	}
	if err := m.CheckAccountPoolAccess(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = []string{"key-b"}
	m.SetConfig(cfg)
	if _, err := m.AccountPoolScope(ctx); err == nil {
		t.Fatal("deleted key survived in a long-lived context")
	}
}
func TestAccountPoolsExplicitSessionIDsAreNamespaced(t *testing.T) {
	m, _, _, _ := newPoolManager(t)
	scopes := []string{}
	for _, key := range []string{"key-a", "key-b"} {
		opts := coreexecutor.Options{Metadata: map[string]any{}}
		_, err := m.preparePoolSelection(poolCaller(key), opts)
		if err != nil {
			t.Fatal(err)
		}
		sid, _ := extractSessionIDs(http.Header{"Session-Id": []string{"same-session"}}, nil, opts.Metadata)
		scopes = append(scopes, sid)
	}
	if scopes[0] == scopes[1] {
		t.Fatal("different keys share an explicit session namespace")
	}
}
func TestAccountPoolsModelListingIgnoresCooldownButHonorsGroups(t *testing.T) {
	m, _, _, ids := newPoolManager(t)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(ids[2], "pool-test", []*registry.ModelInfo{{ID: "private-b"}})
	a, _ := m.GetByID(ids[0])
	a.Unavailable = true
	a.NextRetryAfter = time.Now().Add(time.Hour)
	_, _ = m.Update(context.Background(), a)
	models, err := m.FilterAccountPoolModels(poolCaller("key-a"), []map[string]any{{"id": "pool-model"}, {"id": "private-b"}})
	if err != nil || len(models) != 1 || models[0]["id"] != "pool-model" {
		t.Fatalf("wrong visible models: %v %v", models, err)
	}
}

type roguePoolSelector struct{ target *Auth }

func (s *roguePoolSelector) Pick(context.Context, string, string, coreexecutor.Options, []*Auth) (*Auth, error) {
	return s.target, nil
}
func TestAccountPoolsValidateCustomSelectorResult(t *testing.T) {
	m, _, _, ids := newPoolManager(t)
	target, _ := m.GetByID(ids[2])
	m.SetSelector(&roguePoolSelector{target: target})
	if _, err := m.SelectAuth(poolCaller("key-a"), "pool-test", "pool-model", coreexecutor.Options{}); err == nil {
		t.Fatal("custom selector escaped candidate group")
	}
}
