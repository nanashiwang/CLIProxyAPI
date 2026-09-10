package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const DefaultAccountPoolID = "default"

// AccountPoolsConfig assigns credentials to named groups and limits client keys.
// Credentials without an explicit assignment belong to the default group.
type AccountPoolsConfig struct {
	Enabled  bool                 `yaml:"enabled" json:"enabled"`
	Groups   []AccountPoolGroup   `yaml:"groups" json:"groups"`
	KeyRules []AccountPoolKeyRule `yaml:"key-rules" json:"key-rules"`
}
type AccountPoolGroup struct {
	ID            string   `yaml:"id" json:"id"`
	Name          string   `yaml:"name" json:"name"`
	Description   string   `yaml:"description,omitempty" json:"description,omitempty"`
	Disabled      bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	CredentialIDs []string `yaml:"credential-ids" json:"credential-ids"`
}
type AccountPoolKeyRule struct {
	KeyHash  string   `yaml:"key-hash" json:"key-hash"`
	Name     string   `yaml:"name,omitempty" json:"name,omitempty"`
	Scope    string   `yaml:"scope" json:"scope"`
	GroupIDs []string `yaml:"group-ids" json:"group-ids"`
}

// ClientKeyFingerprint identifies a key without storing it in routing metadata.
func ClientKeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// ValidateAccountPools rejects ambiguous policies before publishing a snapshot.
func (cfg *Config) ValidateAccountPools() error {
	p := cfg.AccountPools
	if p.Enabled && cfg.Home.Enabled {
		return fmt.Errorf("account-pools: Home dispatch does not yet support account groups")
	}
	if !p.Enabled && len(p.Groups) == 0 && len(p.KeyRules) == 0 {
		return nil
	}
	if len(p.Groups) > 1000 || len(p.KeyRules) > 10000 {
		return fmt.Errorf("account-pools: too many groups or key rules")
	}
	groups := make(map[string]bool)
	members := make(map[string]bool)
	for _, group := range p.Groups {
		if group.ID == "" || group.ID != strings.TrimSpace(group.ID) || strings.TrimSpace(group.Name) == "" {
			return fmt.Errorf("account-pools: group id and name are required")
		}
		if groups[group.ID] {
			return fmt.Errorf("account-pools: duplicate group id %q", group.ID)
		}
		groups[group.ID] = true
		if group.ID == DefaultAccountPoolID && len(group.CredentialIDs) != 0 {
			return fmt.Errorf("account-pools: default group membership is implicit")
		}
		for _, id := range group.CredentialIDs {
			if id == "" || id != strings.TrimSpace(id) || members[id] {
				return fmt.Errorf("account-pools: empty or duplicate credential assignment")
			}
			members[id] = true
		}
	}
	if !groups[DefaultAccountPoolID] {
		return fmt.Errorf("account-pools: default group is required")
	}
	rules := make(map[string]bool)
	for _, rule := range p.KeyRules {
		hash, err := hex.DecodeString(rule.KeyHash)
		if err != nil || len(hash) != sha256.Size || rule.KeyHash != strings.ToLower(rule.KeyHash) || rules[rule.KeyHash] {
			return fmt.Errorf("account-pools: invalid or duplicate key fingerprint")
		}
		rules[rule.KeyHash] = true
		if rule.Scope != "selected" && rule.Scope != "all" {
			return fmt.Errorf("account-pools: key scope must be selected or all")
		}
		if rule.Scope == "all" && len(rule.GroupIDs) > 0 {
			return fmt.Errorf("account-pools: all scope cannot also select groups")
		}
		seen := make(map[string]bool)
		for _, id := range rule.GroupIDs {
			if !groups[id] || seen[id] {
				return fmt.Errorf("account-pools: missing or duplicate group reference %q", id)
			}
			seen[id] = true
		}
	}
	return nil
}

// AccountPoolPolicy is immutable after compilation and shared by runtime readers.
type AccountPoolPolicy struct {
	revision   string
	groups     map[string]bool
	membership map[string]string
	keys       map[string]*AccountPoolScope
	invalid    bool
}
type AccountPoolScope struct {
	policy *AccountPoolPolicy
	keyID  string
	all    bool
	groups map[string]bool
}

func (cfg *Config) CompileAccountPoolPolicy() *AccountPoolPolicy {
	if !cfg.AccountPools.Enabled {
		return nil
	}
	p := &AccountPoolPolicy{groups: make(map[string]bool), membership: make(map[string]string), keys: make(map[string]*AccountPoolScope)}
	if cfg.ValidateAccountPools() != nil {
		p.invalid = true
		return p
	}
	data, _ := json.Marshal(cfg.AccountPools)
	sum := sha256.Sum256(data)
	p.revision = hex.EncodeToString(sum[:])
	for _, group := range cfg.AccountPools.Groups {
		p.groups[group.ID] = !group.Disabled
		for _, id := range group.CredentialIDs {
			p.membership[id] = group.ID
		}
	}
	rules := make(map[string]AccountPoolKeyRule)
	for _, rule := range cfg.AccountPools.KeyRules {
		rules[rule.KeyHash] = rule
	}
	for _, key := range cfg.APIKeys {
		hash := ClientKeyFingerprint(key)
		scope := &AccountPoolScope{policy: p, keyID: hash, groups: map[string]bool{DefaultAccountPoolID: true}}
		if rule, ok := rules[hash]; ok {
			scope.all = rule.Scope == "all"
			scope.groups = make(map[string]bool, len(rule.GroupIDs))
			for _, id := range rule.GroupIDs {
				scope.groups[id] = true
			}
		}
		p.keys[hash] = scope
	}
	return p
}
func (p *AccountPoolPolicy) Resolve(hash string) (*AccountPoolScope, bool) {
	if p == nil || p.invalid {
		return nil, false
	}
	scope, ok := p.keys[hash]
	return scope, ok
}
func (p *AccountPoolPolicy) GroupForCredential(id string) string {
	if group := p.membership[id]; group != "" {
		return group
	}
	return DefaultAccountPoolID
}
func (s *AccountPoolScope) Allows(id string) bool {
	if s == nil {
		return true
	}
	group := s.policy.GroupForCredential(id)
	return s.policy.groups[group] && (s.all || s.groups[group])
}
func (s *AccountPoolScope) Namespace() string { return s.keyID + ":" + s.policy.revision }
func (s *AccountPoolScope) KeyID() string     { return s.keyID }
func (s *AccountPoolScope) GroupForCredential(id string) string {
	return s.policy.GroupForCredential(id)
}
