package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	return scope.WithLease(lease.Group, lease.ID, lease.Credential), nil
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
	if previous, exists, e := store.Lookup(owner, runtimeLeaseRevision(cfg), time.Now()); e != nil {
		return ctx, noop, e
	} else if (!exists || previous.LegacyGroup || previous.Reassigned) && (gjson.GetBytes(req.Payload, "previous_response_id").String() != "" || gjson.GetBytes(opts.OriginalRequest, "previous_response_id").String() != "") {
		return ctx, noop, leaseError("pool_lease_session_expired", "start a new conversation after the pool lease expires", http.StatusConflict)
	}
	candidates := m.poolLeaseCandidates(ctx, scope, providers, req, opts)
	allowed := map[string]bool{}
	for _, group := range scope.LeaseGroups() {
		allowed[group] = true
	}
	lease, release, err := store.Acquire(owner, runtimeLeaseRevision(cfg), allowed, candidates, time.Now())
	if err != nil {
		if errors.Is(err, poollease.ErrDenied) {
			return ctx, noop, leaseError("pool_access_denied", "current lease is outside key permissions", 403)
		}
		if errors.Is(err, poollease.ErrBusy) {
			return ctx, noop, leaseError("pool_busy", "no free authorized account for the requested model; retry later", 503)
		}
		return ctx, noop, leaseError("pool_lease_unavailable", "lease allocation failed", 503)
	}
	ctx = context.WithValue(ctx, requestPoolLeaseKey{}, &requestPoolLease{owner, lease})
	// A previous request may have recorded a terminal failure while another
	// in-flight request prevented replacement. Retry allocation at admission.
	next, done, changed, err := m.replaceFailedPoolLease(ctx, providers, req, opts, nil)
	if err != nil {
		release()
		return ctx, noop, err
	}
	if changed {
		release()
		return next, done, nil
	}
	return ctx, release, nil
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

func (m *Manager) poolLeaseCandidates(ctx context.Context, scope *config.AccountPoolScope, providers []string, req coreexecutor.Request, opts coreexecutor.Options) []poollease.Candidate {
	groups := scope.LeaseGroups()
	allowed := map[string]bool{}
	for _, g := range groups {
		allowed[g] = true
	}
	candidates := []poollease.Candidate{}
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
			candidates = append(candidates, poollease.Candidate{Group: group, Credential: a.ID})
		}
	}
	m.mu.RUnlock()
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Group < candidates[j].Group || (candidates[i].Group == candidates[j].Group && candidates[i].Credential < candidates[j].Credential)
	})
	return candidates
}

// Only explicit account quota/authentication failures permit replacement.
// Generic 429s, transport errors, overload and request policy errors do not.
func poolLeaseTerminalFailure(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) && e.Code == ErrorCodeRequestScoped {
		return false
	}
	if IsTerminalAuthError(err) {
		return true
	}
	status := 0
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		status = sc.StatusCode()
	}
	msg := strings.ToLower(err.Error())
	if status == 429 {
		return strings.Contains(msg, "usage_limit_reached") || strings.Contains(msg, "insufficient_quota") || strings.Contains(msg, "you've hit your usage limit")
	}
	if status == 401 {
		return true
	}
	return status == 403 && (strings.Contains(msg, "account_deactivated") || strings.Contains(msg, "account_disabled") || strings.Contains(msg, "token_revoked"))
}

func (m *Manager) replaceFailedPoolLease(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options, failure error) (context.Context, func(), bool, error) {
	binding, ok := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease)
	if !ok || ctx.Err() != nil {
		return ctx, nil, false, nil
	}
	terminal := poolLeaseTerminalFailure(failure)
	m.mu.RLock()
	if a := m.auths[binding.Lease.Credential]; a != nil {
		if _, unavailable := getAvailableAuths([]*Auth{a}, a.Provider, authSelectionModelFromOptions(opts, req.Model), time.Now()); unavailable != nil || a.Disabled {
			terminal = terminal || poolLeaseTerminalFailure(a.LastError)
		}
	}
	m.mu.RUnlock()
	if !terminal {
		return ctx, nil, false, nil
	}
	if gjson.GetBytes(req.Payload, "previous_response_id").String() != "" || gjson.GetBytes(opts.OriginalRequest, "previous_response_id").String() != "" {
		return ctx, nil, false, leaseError("pool_lease_session_expired", "leased account is unavailable; start a new conversation with full history", http.StatusConflict)
	}
	scope, err := m.AccountPoolScope(ctx)
	if err != nil {
		return ctx, nil, false, err
	}
	store, err := m.checkLeaseConfiguration(m.runtimeConfigSnapshot())
	if err != nil {
		return ctx, nil, false, err
	}
	if store == nil || scope == nil {
		return ctx, nil, false, nil
	}
	next, release, err := store.Replace(binding.Lease, m.poolLeaseCandidates(ctx, scope, providers, req, opts), time.Now())
	if errors.Is(err, poollease.ErrBusy) {
		return ctx, nil, false, leaseError("pool_lease_failover_pending", "no free account in the leased group or previous requests are still running; retry later", 503)
	}
	if err != nil {
		return ctx, nil, false, leaseError("pool_lease_unavailable", "cannot persist account replacement", 503)
	}
	log.WithFields(log.Fields{"owner": next.Owner[:10], "group": next.Group, "lease": next.ID, "expires_at": next.Expires}).Info("exclusive account lease replaced after quota or authentication failure")
	return context.WithValue(ctx, requestPoolLeaseKey{}, &requestPoolLease{binding.Owner, next}), release, true, nil
}

func (m *Manager) discardLeasedStream(ctx context.Context, chunks <-chan coreexecutor.StreamChunk) {
	if chunks == nil {
		return
	}
	if ctx == nil {
		discardStreamChunks(chunks)
		return
	}
	binding, leased := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease)
	if !leased {
		discardStreamChunks(chunks)
		return
	}
	m.poolLeaseRuntime.mu.Lock()
	store := m.poolLeaseRuntime.store
	m.poolLeaseRuntime.mu.Unlock()
	if store != nil {
		if done, ok := store.Hold(binding.Lease.ID); ok {
			go func() {
				defer done()
				for range chunks {
				}
			}()
			return
		}
	}
	// If retention fails, do not release the caller's lease before draining.
	for range chunks {
	}
}
