package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (h *Handler) GetAPIKeys(c *gin.Context) {
	h.mu.Lock()
	keys := append([]string(nil), h.cfg.APIKeys...)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"api-keys": keys})
}
func (h *Handler) PutAPIKeys(c *gin.Context) {
	raw, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var keys []string
	if err = json.Unmarshal(raw, &keys); err != nil {
		var body struct {
			Items []string `json:"items"`
		}
		if err = json.Unmarshal(raw, &body); err != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		keys = body.Items
	}
	h.mutateClientKeys(c, func([]string) ([]string, error) { return keys, nil })
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	var body struct {
		Old   *string `json:"old"`
		New   *string `json:"new"`
		Index *int    `json:"index"`
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mutateClientKeys(c, func(keys []string) ([]string, error) {
		if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(keys) {
			keys[*body.Index] = *body.Value
			return keys, nil
		}
		if body.Old != nil && body.New != nil {
			for i, key := range keys {
				if key == *body.Old {
					keys[i] = *body.New
					return keys, nil
				}
			}
			return append(keys, *body.New), nil
		}
		return nil, fmt.Errorf("missing fields")
	})
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.mutateClientKeys(c, func(keys []string) ([]string, error) {
		if index, err := strconv.Atoi(c.Query("index")); err == nil && index >= 0 && index < len(keys) {
			return append(keys[:index], keys[index+1:]...), nil
		}
		if value := strings.TrimSpace(c.Query("value")); value != "" {
			kept := make([]string, 0, len(keys))
			for _, key := range keys {
				if strings.TrimSpace(key) != value {
					kept = append(kept, key)
				}
			}
			return kept, nil
		}
		return nil, fmt.Errorf("missing index or value")
	})
}

// reconcilePoolKeyRules preserves single-key rotations and drops revoked bindings.
// Ambiguous bulk replacement of bound keys is rejected rather than broadening access.
func reconcilePoolKeyRules(rules []config.AccountPoolKeyRule, oldKeys, newKeys []string) ([]config.AccountPoolKeyRule, error) {
	oldSet, newSet := make(map[string]bool), make(map[string]bool)
	for _, key := range oldKeys {
		oldSet[config.ClientKeyFingerprint(key)] = true
	}
	for _, key := range newKeys {
		newSet[config.ClientKeyFingerprint(key)] = true
	}
	removed, added := []string{}, []string{}
	for hash := range oldSet {
		if !newSet[hash] {
			removed = append(removed, hash)
		}
	}
	for hash := range newSet {
		if !oldSet[hash] {
			added = append(added, hash)
		}
	}
	rotation := len(removed) == 1 && len(added) == 1
	result := make([]config.AccountPoolKeyRule, 0, len(rules))
	for _, rule := range rules {
		if newSet[rule.KeyHash] {
			result = append(result, rule)
			continue
		}
		if rotation && rule.KeyHash == removed[0] {
			rule.KeyHash = added[0]
			result = append(result, rule)
			continue
		}
		if len(added) > 0 && oldSet[rule.KeyHash] {
			return nil, fmt.Errorf("replace grouped keys one at a time to preserve their permissions")
		}
	}
	return result, nil
}

func (h *Handler) mutateClientKeys(c *gin.Context, mutate func([]string) ([]string, error)) {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	keys, err := mutate(append([]string(nil), h.cfg.APIKeys...))
	if err != nil {
		h.mu.Unlock()
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]bool)
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" && !seen[key] {
			seen[key] = true
			normalized = append(normalized, key)
		}
	}
	rules, err := reconcilePoolKeyRules(h.cfg.AccountPools.KeyRules, h.cfg.APIKeys, normalized)
	if err != nil {
		h.mu.Unlock()
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	oldKeys, oldRules := h.cfg.APIKeys, h.cfg.AccountPools.KeyRules
	h.cfg.APIKeys = normalized
	h.cfg.AccountPools.KeyRules = rules
	snapshot, saved := h.saveConfigAndSnapshotLocked(c)
	if !saved {
		h.cfg.APIKeys = oldKeys
		h.cfg.AccountPools.KeyRules = oldRules
		h.mu.Unlock()
		return
	}
	if h.authManager != nil {
		h.authManager.SetConfigSnapshot(snapshot.cfg)
	}
	hook, host := h.configReloadHook, h.pluginHost
	h.appliedReloadGeneration = snapshot.generation
	h.mu.Unlock()
	ctx := context.WithoutCancel(c.Request.Context())
	if hook != nil {
		hook(ctx, snapshot.cfg)
	} else if host != nil {
		host.ApplyConfig(ctx, snapshot.cfg)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
