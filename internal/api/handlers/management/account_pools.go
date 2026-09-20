package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

func poolConfigForManagement(cfg *config.Config) config.AccountPoolsConfig {
	pools := cfg.CloneForRuntime().AccountPools
	validKeys := make(map[string]bool)
	for _, key := range cfg.APIKeys {
		validKeys[config.ClientKeyFingerprint(key)] = true
	}
	rules := pools.KeyRules[:0]
	for _, rule := range pools.KeyRules {
		if validKeys[rule.KeyHash] {
			rules = append(rules, rule)
		}
	}
	pools.KeyRules = rules
	if len(pools.Groups) == 0 {
		pools.Groups = []config.AccountPoolGroup{{ID: config.DefaultAccountPoolID, Name: "Default", CredentialIDs: []string{}}}
	}
	if pools.KeyRules == nil {
		pools.KeyRules = []config.AccountPoolKeyRule{}
	}
	for i := range pools.Groups {
		if pools.Groups[i].CredentialIDs == nil {
			pools.Groups[i].CredentialIDs = []string{}
		}
	}
	return pools
}
func poolConfigRevision(cfg *config.Config) string {
	hashes := make([]string, 0, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		hashes = append(hashes, config.ClientKeyFingerprint(key))
	}
	data, _ := json.Marshal(struct {
		Pools config.AccountPoolsConfig
		Keys  []string
	}{poolConfigForManagement(cfg), hashes})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *Handler) GetAccountPools(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg.CloneForRuntime()
	h.mu.Unlock()
	pools := poolConfigForManagement(cfg)
	rules := make(map[string]config.AccountPoolKeyRule)
	for _, rule := range pools.KeyRules {
		rules[rule.KeyHash] = rule
	}
	keys := make([]gin.H, 0, len(cfg.APIKeys))
	for i, key := range cfg.APIKeys {
		hash := config.ClientKeyFingerprint(key)
		rule, ok := rules[hash]
		if !ok {
			rule = config.AccountPoolKeyRule{KeyHash: hash, Scope: "selected", GroupIDs: []string{config.DefaultAccountPoolID}}
		}
		keys = append(keys, gin.H{"key-hash": hash, "preview": util.HideAPIKey(key), "index": i + 1, "name": rule.Name, "scope": rule.Scope, "group-ids": rule.GroupIDs, "lease-instance": rule.LeaseInstance})
	}
	members := make(map[string]string)
	for _, group := range pools.Groups {
		for _, id := range group.CredentialIDs {
			members[id] = group.ID
		}
	}
	credentials := make([]gin.H, 0)
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a == nil {
				continue
			}
			group := members[a.ID]
			if group == "" {
				group = config.DefaultAccountPoolID
			}
			name := a.FileName
			if name == "" {
				name = a.Label
				if name != "" {
					name += " · " + a.EnsureIndex()
				}
			}
			if name == "" {
				name = a.ID
			}
			unavailable, _, _, _ := a.AvailabilityView(time.Now().UTC())
			credentials = append(credentials, gin.H{"id": a.ID, "name": name, "provider": a.Provider, "disabled": a.Disabled, "unavailable": unavailable, "group-id": group})
		}
	}
	present := make(map[string]bool)
	for _, item := range credentials {
		present[item["id"].(string)] = true
	}
	for id, group := range members {
		if !present[id] {
			credentials = append(credentials, gin.H{"id": id, "name": id, "provider": "missing", "disabled": true, "unavailable": true, "group-id": group})
		}
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i]["id"].(string) < credentials[j]["id"].(string) })
	leases := []gin.H{}
	leaseError := ""
	if h.authManager != nil {
		rows, err := h.authManager.AccountPoolLeases()
		if err != nil {
			leaseError = "lease state unavailable"
		}
		for _, l := range rows {
			leases = append(leases, gin.H{"id": l.ID, "group-id": l.Group, "credential-id": l.Credential, "legacy-group": l.LegacyGroup, "temporary": l.Temporary, "owner": l.Owner, "expires-at": l.Expires, "active": l.Active})
		}
	}
	c.JSON(http.StatusOK, gin.H{"lease-unit": "account", "leases": leases, "lease-error": leaseError, "config": pools, "keys": keys, "credentials": credentials, "revision": poolConfigRevision(cfg), "home-enabled": cfg.Home.Enabled})
}

// PutAccountPools publishes membership and key authorization as one versioned change.
func (h *Handler) PutAccountPools(c *gin.Context) {
	var body struct {
		Config   config.AccountPoolsConfig `json:"config"`
		Revision string                    `json:"revision"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid account pool configuration"})
		return
	}
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	if body.Revision == "" || body.Revision != poolConfigRevision(h.cfg) {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "account pool configuration changed; reload before saving"})
		return
	}
	next := h.cfg.CloneForRuntime()
	next.AccountPools = body.Config
	if err := next.ValidateAccountPools(); err != nil {
		h.mu.Unlock()
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	knownKeys := make(map[string]bool)
	for _, key := range next.APIKeys {
		knownKeys[config.ClientKeyFingerprint(key)] = true
	}
	for _, rule := range next.AccountPools.KeyRules {
		if !knownKeys[rule.KeyHash] {
			h.mu.Unlock()
			c.JSON(400, gin.H{"error": "key no longer exists; reload before saving"})
			return
		}
	}
	knownCredentials := make(map[string]bool)
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a != nil {
				knownCredentials[a.ID] = true
			}
		}
	}
	// Keep existing missing references inert; never turn a failed lookup into all-access.
	for _, group := range h.cfg.AccountPools.Groups {
		for _, id := range group.CredentialIDs {
			knownCredentials[id] = true
		}
	}
	for _, group := range next.AccountPools.Groups {
		for _, id := range group.CredentialIDs {
			if !knownCredentials[id] {
				h.mu.Unlock()
				c.JSON(400, gin.H{"error": "credential no longer exists; reload before saving"})
				return
			}
		}
	}
	if h.authManager != nil {
		if err := h.authManager.ValidateAccountPoolLeaseChange(next); err != nil {
			h.mu.Unlock()
			c.JSON(http.StatusConflict, gin.H{"error": "active leases prevent group membership changes"})
			return
		}
	}
	previous := h.cfg.AccountPools
	h.cfg.AccountPools = next.AccountPools
	snapshot, saved := h.saveConfigAndSnapshotLocked(c)
	if !saved {
		h.cfg.AccountPools = previous
		h.mu.Unlock()
		return
	}
	// Apply restrictions before acknowledging the save, even without a watcher.
	if h.authManager != nil {
		h.authManager.SetConfigSnapshot(snapshot.cfg)
	}
	hook, host := h.configReloadHook, h.pluginHost
	h.appliedReloadGeneration = snapshot.generation
	h.mu.Unlock()
	reloadCtx := context.WithoutCancel(c.Request.Context())
	if hook != nil {
		hook(reloadCtx, snapshot.cfg)
	} else if host != nil {
		host.ApplyConfig(reloadCtx, snapshot.cfg)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
