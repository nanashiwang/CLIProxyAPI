package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type accountPoolScopeContextKey struct{}

func (m *Manager) AccountPoolsEnabled() bool {
	cfg := m.runtimeConfigSnapshot()
	return cfg != nil && cfg.AccountPools.Enabled
}

// AccountPoolScope resolves the latest policy for every request and retry.
// Missing identity or invalid policy must never revert to unrestricted routing.
func (m *Manager) AccountPoolScope(ctx context.Context) (*config.AccountPoolScope, error) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return nil, nil
	}
	store, err := m.checkLeaseConfiguration(cfg)
	if err != nil {
		return nil, err
	}
	if !cfg.AccountPools.Enabled {
		return nil, nil
	}
	if cfg.Home.Enabled {
		return nil, &Error{Code: "pool_routing_unsupported", Message: "account groups are not supported by Home dispatch", HTTPStatus: http.StatusServiceUnavailable}
	}
	scope, ok := cfg.AccountPoolPolicy.Resolve(sdkaccess.ClientKeyIdentity(ctx))
	if !ok {
		return nil, &Error{Code: "pool_access_denied", Message: "client key has no valid account group policy", HTTPStatus: http.StatusForbidden}
	}
	return m.scopeWithLease(ctx, cfg, scope, store)
}

func (m *Manager) preparePoolSelection(ctx context.Context, opts coreexecutor.Options) (context.Context, error) {
	scope, err := m.AccountPoolScope(ctx)
	if err != nil {
		return ctx, err
	}
	opts.EnsureMetadata()
	if scope != nil {
		opts.Metadata[coreexecutor.AccountPoolNamespaceMetadataKey] = scope.Namespace()
	} else {
		delete(opts.Metadata, coreexecutor.AccountPoolNamespaceMetadataKey)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, accountPoolScopeContextKey{}, scope), nil
}

// CheckAccountPoolAccess also covers direct/pinned credential reuse outside a selector.
func (m *Manager) CheckAccountPoolAccess(ctx context.Context, authID string) error {
	scope, err := m.AccountPoolScope(ctx)
	if err != nil {
		return err
	}
	if scope == nil {
		return nil
	}
	m.mu.RLock()
	current := m.auths[authID]
	exists := current != nil && !current.Disabled
	m.mu.RUnlock()
	if !exists || !scope.Allows(authID) {
		return &Error{Code: "pool_access_denied", Message: "credential is outside the client key account groups", HTTPStatus: http.StatusForbidden}
	}
	return nil
}

func poolUnavailable(ctx context.Context) *Error {
	if scope, _ := ctx.Value(accountPoolScopeContextKey{}).(*config.AccountPoolScope); scope != nil {
		return &Error{Code: "pool_unavailable", Message: "no available credential in the authorized account groups", HTTPStatus: http.StatusServiceUnavailable}
	}
	return &Error{Code: "auth_not_found", Message: "no auth available"}
}

// FilterAccountPoolModels exposes only models configured for an authorized credential.
// Cooldown does not alter permissions or the advertised model list.
func (m *Manager) FilterAccountPoolModels(ctx context.Context, models []map[string]any) ([]map[string]any, error) {
	scope, err := m.AccountPoolScope(ctx)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return models, nil
	}
	allowed := make([]*Auth, 0)
	m.mu.RLock()
	for _, a := range m.auths {
		if a != nil && !a.Disabled && (scope.Allows(a.ID) || (scope.LeaseInstance() != "" && scope.AuthorizesCredential(a.ID))) {
			allowed = append(allowed, a.Clone())
		}
	}
	m.mu.RUnlock()
	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			id, _ = model["name"].(string)
			id = strings.TrimPrefix(id, "models/")
		}
		for _, a := range allowed {
			if m.supportsPoolModel(a, id) {
				result = append(result, model)
				break
			}
		}
	}
	return result, nil
}

func (m *Manager) supportsPoolModel(a *Auth, model string) bool {
	return m.authSupportsRouteModel(registry.GetGlobalRegistry(), a, model)
}

func (m *Manager) accountPoolUsageContext(ctx context.Context, authID string) context.Context {
	if scope, err := m.AccountPoolScope(ctx); err == nil && scope != nil {
		return coreusage.WithAccountPoolAttribution(ctx, scope.KeyID(), scope.GroupForCredential(authID))
	}
	return ctx
}
