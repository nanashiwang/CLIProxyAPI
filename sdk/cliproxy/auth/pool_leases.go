package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/tidwall/gjson"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/poollease"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type poolLeaseRuntime struct {
	mu      sync.Mutex
	store   *poollease.Store
	path    string
	checked bool
	err     error
}
type requestPoolLeaseKey struct{}
type requestPoolLease struct {
	Owner string
	Lease poollease.Lease
}

func leaseError(code, message string, status int) error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}
func leaseOwner(ctx context.Context, scope *config.AccountPoolScope) (string, error) {
	identity := sdkaccess.GetGatewayIdentity(ctx)
	if identity.Instance != scope.LeaseInstance() || identity.Instance == "" || identity.User == "" {
		return "", leaseError("pool_identity_required", "valid New API identity is required", http.StatusForbidden)
	}
	sum := sha256.Sum256([]byte(identity.Instance + "\x00" + identity.User))
	return hex.EncodeToString(sum[:]), nil
}
func (m *Manager) leaseStore(cfg *config.Config) (*poollease.Store, error) {
	r := &m.poolLeaseRuntime
	r.mu.Lock()
	defer r.mu.Unlock()
	enabled := cfg.AccountPools.Enabled && cfg.AccountPoolPolicy.HasLeasePools()
	if cfg.AuthDir == "" {
		if enabled {
			return nil, leaseError("pool_lease_storage_unavailable", "auth directory is required for leases", 503)
		}
		return nil, nil
	}
	dir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".pool-leases.state")
	if r.path != "" && r.path != path {
		return nil, leaseError("pool_lease_storage_changed", "restart is required to change lease storage", 503)
	}
	if r.store != nil {
		return r.store, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	if !enabled && r.checked {
		return nil, nil
	}
	if !enabled {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if _, markerErr := os.Stat(path + ".lock"); os.IsNotExist(markerErr) {
				r.checked = true
				return nil, nil
			}
		} else if err != nil {
			return nil, err
		}
	}
	r.path = path
	r.store, r.err = poollease.Open(path)
	if r.err != nil {
		return nil, leaseError("pool_lease_storage_unavailable", "lease state cannot be opened; routing is blocked", 503)
	}
	return r.store, nil
}
func (m *Manager) checkLeaseConfiguration(cfg *config.Config) (*poollease.Store, error) {
	store, err := m.leaseStore(cfg)
	if err != nil {
		return nil, err
	}
	if store != nil {
		if err = store.CheckPolicy(runtimeLeaseRevision(cfg), time.Now()); err != nil {
			return nil, leaseError("pool_lease_policy_changed", "active leases must finish before changing groups", 503)
		}
	}
	return store, nil
}
func (m *Manager) scopeWithLease(ctx context.Context, cfg *config.Config, scope *config.AccountPoolScope, store *poollease.Store) (*config.AccountPoolScope, error) {
	if scope.LeaseInstance() == "" || store == nil {
		return scope, nil
	}
	owner, err := leaseOwner(ctx, scope)
	// Identity is mandatory at execution time, but model discovery does not allocate leases.
	if err != nil {
		return scope, nil
	}
	lease, ok, err := store.Lookup(owner, runtimeLeaseRevision(cfg), time.Now())
	if err != nil {
		return nil, err
	}
	binding, _ := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease)
	if binding != nil && (!ok || binding.Owner != owner || binding.Lease.ID != lease.ID) {
		return nil, leaseError("pool_lease_expired", "pool lease is no longer valid", 403)
	}
	if !ok {
		return scope, nil
	}
	if !scope.AuthorizesGroup(lease.Group) {
		return nil, leaseError("pool_access_denied", "current lease is outside key permissions", 403)
	}
	if !time.Now().Before(lease.Expires) {
		return nil, leaseError("pool_lease_expired", "pool lease expired; finish the current request before allocating again", 503)
	}
	return scope.WithLease(lease.Group, lease.ID), nil
}

func (m *Manager) beginPoolLease(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (context.Context, func(), error) {
	noop := func() {}
	scope, err := m.AccountPoolScope(ctx)
	if err != nil {
		return ctx, noop, err
	}
	if scope == nil || scope.LeaseInstance() == "" {
		return ctx, noop, nil
	}
	owner, err := leaseOwner(ctx, scope)
	if err != nil {
		return ctx, noop, err
	}
	cfg := m.runtimeConfigSnapshot()
	store, err := m.checkLeaseConfiguration(cfg)
	if err != nil {
		return ctx, noop, err
	}
	if store == nil {
		return ctx, noop, leaseError("pool_unavailable", "no lease pools configured", 503)
	}
	if _, exists, e := store.Lookup(owner, runtimeLeaseRevision(cfg), time.Now()); e != nil {
		return ctx, noop, e
	} else if !exists && (gjson.GetBytes(req.Payload, "previous_response_id").String() != "" || gjson.GetBytes(opts.OriginalRequest, "previous_response_id").String() != "") {
		return ctx, noop, leaseError("pool_lease_session_expired", "start a new conversation after the pool lease expires", http.StatusConflict)
	}
	groups := scope.LeaseGroups()
	allowed := map[string]bool{}
	for _, g := range groups {
		allowed[g] = true
	}
	ready := map[string]bool{}
	providerSet := map[string]bool{}
	for _, p := range m.normalizeProviders(providers) {
		providerSet[p] = true
	}
	model := authSelectionModelFromOptions(opts, req.Model)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	m.mu.RLock()
	for _, a := range m.auths {
		if a == nil || a.Disabled || !providerSet[executorKeyFromAuth(a)] || !eligibility.allows(a) || (pinned != "" && a.ID != pinned) {
			continue
		}
		group := scope.GroupForCredential(a.ID)
		if !allowed[group] || !m.supportsPoolModel(a, model) {
			continue
		}
		if available, e := getAvailableAuths([]*Auth{a}, a.Provider, model, time.Now()); e == nil && len(available) > 0 {
			ready[group] = true
		}
	}
	m.mu.RUnlock()
	candidates := []string{}
	for g := range ready {
		candidates = append(candidates, g)
	}
	sort.Strings(candidates)
	lease, release, err := store.Acquire(owner, runtimeLeaseRevision(cfg), allowed, candidates, time.Now())
	if err != nil {
		if errors.Is(err, poollease.ErrDenied) {
			return ctx, noop, leaseError("pool_access_denied", "current lease is outside key permissions", 403)
		}
		if errors.Is(err, poollease.ErrBusy) {
			return ctx, noop, leaseError("pool_busy", "no free authorized pool; retry later", 503)
		}
		return ctx, noop, leaseError("pool_lease_unavailable", "lease allocation failed", 503)
	}
	return context.WithValue(ctx, requestPoolLeaseKey{}, &requestPoolLease{owner, lease}), release, nil
}

// Keep the in-flight lease until the upstream producer closes. Cancellation stops
// forwarding but still drains the producer, so the next owner never overlaps it.
func holdPoolLeaseStream(ctx context.Context, result *coreexecutor.StreamResult, release func()) *coreexecutor.StreamResult {
	if _, leased := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease); !leased {
		release()
		return result
	}
	if result == nil || result.Chunks == nil {
		release()
		return result
	}
	out := make(chan coreexecutor.StreamChunk)
	go func() {
		defer release()
		defer close(out)
		for chunk := range result.Chunks {
			select {
			case out <- chunk:
			case <-ctx.Done():
			}
		}
	}()
	return &coreexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

func (m *Manager) AccountPoolLeases() ([]poollease.Lease, error) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return nil, nil
	}
	store, err := m.leaseStore(cfg)
	if err != nil || store == nil {
		return nil, err
	}
	return store.Snapshot(time.Now()), nil
}
func (m *Manager) ValidateAccountPoolLeaseChange(next *config.Config) error {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return nil
	}
	store, err := m.leaseStore(cfg)
	if err != nil {
		return err
	}
	if store != nil {
		return store.CheckPolicy(next.PoolLeaseRevision(), time.Now())
	}
	return nil
}

func runtimeLeaseRevision(cfg *config.Config) string {
	if cfg.AccountPools.Enabled && cfg.AccountPoolPolicy != nil {
		return cfg.AccountPoolPolicy.LeaseRevision()
	}
	return "disabled"
}
