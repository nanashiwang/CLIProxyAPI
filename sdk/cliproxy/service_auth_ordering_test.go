package cliproxy

import (
	"context"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAuthUpdateOldEpochHasNoRegistrationSideEffects(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{cfg: &config.Config{}, coreManager: m}
	id := "old-epoch-no-side-effects"
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(id) })
	old, err := m.Register(ctx, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	m.Remove(ctx, id)
	s.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, Auth: &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}})
	retry := time.Hour
	m.MarkResult(ctx, coreauth.Result{AuthID: id, Model: "gpt-6-astra", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429}})
	before, _ := m.GetByID(id)
	_, epoch := reg.GetModelsAndEpochForClient(id)
	for _, action := range []watcher.AuthUpdateAction{watcher.AuthUpdateActionModify, watcher.AuthUpdateActionDelete} {
		s.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: action, ID: id, Auth: old})
		after, ok := m.GetByID(id)
		_, afterEpoch := reg.GetModelsAndEpochForClient(id)
		if !ok || !reflect.DeepEqual(before, after) || epoch != afterEpoch {
			t.Fatalf("stale %s changed runtime or registry: epoch %d -> %d", action, epoch, afterEpoch)
		}
	}
}

func TestAuthRevisionOrderingPreservesPoolAndCooldown(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	id := "revision-pool-auth"
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"allowed", "other"}, AccountPools: internalconfig.AccountPoolsConfig{
		Enabled: true,
		Groups:  []internalconfig.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "a", Name: "A", CredentialIDs: []string{id}}, {ID: "b", Name: "B"}},
		KeyRules: []internalconfig.AccountPoolKeyRule{
			{KeyHash: internalconfig.ClientKeyFingerprint("allowed"), Scope: "selected", GroupIDs: []string{"a"}},
			{KeyHash: internalconfig.ClientKeyFingerprint("other"), Scope: "selected", GroupIDs: []string{"b"}},
		},
	}}}
	m := coreauth.NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	s := &Service{cfg: cfg, coreManager: m}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	update := func(rev uint64, disabled bool) watcher.AuthUpdate {
		a := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Disabled: disabled}
		if disabled {
			a.Status = coreauth.StatusDisabled
		}
		u := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: id, Auth: a}
		u.SetRevision(rev)
		return u
	}
	s.handleAuthUpdate(ctx, update(3, false))
	retry := time.Hour
	m.MarkResult(ctx, coreauth.Result{AuthID: id, Model: "gpt-6-astra", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429}})
	before, _ := m.GetByID(id)
	_, epoch := GlobalModelRegistry().GetModelsAndEpochForClient(id)
	staleDelete := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: id}
	staleDelete.SetRevision(2)
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{update(1, true), staleDelete, update(3, true)})
	after, _ := m.GetByID(id)
	_, afterEpoch := GlobalModelRegistry().GetModelsAndEpochForClient(id)
	if !reflect.DeepEqual(before, after) || afterEpoch != epoch {
		t.Fatal("stale or duplicate event changed cooldown or registry")
	}
	// A newer file snapshot remains valid even after execution advances Generation.
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{update(5, false), update(4, true)})
	after, _ = m.GetByID(id)
	if after.Disabled || !after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || after.Quota.Reason != "credential_quota" {
		t.Fatal("new file snapshot lost credential cooldown", after)
	}
	allowed := sdkaccess.WithClientKeyIdentity(ctx, "allowed")
	other := sdkaccess.WithClientKeyIdentity(ctx, "other")
	if m.CheckAccountPoolAccess(allowed, id) != nil || m.CheckAccountPoolAccess(other, id) == nil {
		t.Fatal("auth synchronization changed pool authorization")
	}
	m.RegisterExecutor(&syncTestExecutor{})
	if _, err := m.SelectAuth(allowed, "codex", "gpt-6-astra", coreexecutor.Options{}); err == nil {
		t.Fatal("scheduler ignored preserved cooldown")
	}
	// Keep deletion revisions as tombstones until a genuinely newer re-add arrives.
	deleted := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: id}
	deleted.SetRevision(6)
	s.handleAuthUpdate(ctx, deleted)
	s.handleAuthUpdate(ctx, update(5, false))
	if _, ok := m.GetByID(id); ok {
		t.Fatal("stale update resurrected deleted auth")
	}
	s.handleAuthUpdate(ctx, update(7, false))
	if _, ok := m.GetByID(id); !ok {
		t.Fatal("newer update could not re-register auth")
	}
}

func TestAuthRevisionIsNotConsumedBeforeRuntimeReady(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{coreManager: m}
	id := "revision-runtime-ready"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	update := watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, Auth: &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}}
	update.SetRevision(1)
	s.handleAuthUpdate(ctx, update)
	s.cfg = &config.Config{}
	s.handleAuthUpdate(ctx, update)
	if _, ok := m.GetByID(id); !ok {
		t.Fatal("runtime initialization swallowed the pending revision")
	}
}
