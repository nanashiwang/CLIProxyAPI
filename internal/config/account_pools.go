package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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
	Lease         bool     `yaml:"lease,omitempty" json:"lease,omitempty"`
	ID            string   `yaml:"id" json:"id"`
	Name          string   `yaml:"name" json:"name"`
	Description   string   `yaml:"description,omitempty" json:"description,omitempty"`
	Disabled      bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	CredentialIDs []string `yaml:"credential-ids" json:"credential-ids"`
}
type AccountPoolKeyRule struct {
	LeaseInstance string   `yaml:"lease-instance,omitempty" json:"lease-instance,omitempty"`
	KeyHash       string   `yaml:"key-hash" json:"key-hash"`
	Name          string   `yaml:"name,omitempty" json:"name,omitempty"`
	Scope         string   `yaml:"scope" json:"scope"`
	GroupIDs      []string `yaml:"group-ids" json:"group-ids"`
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
		if group.Lease && group.ID == DefaultAccountPoolID {
			return fmt.Errorf("account-pools: default group cannot be leased")
		}
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
		if rule.LeaseInstance != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(rule.LeaseInstance) {
			return fmt.Errorf("account-pools: invalid lease instance")
		}
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
	leaseRevision string
	hasLeases     bool
	revision      string
	groups        map[string]bool
	membership    map[string]string
	keys          map[string]*AccountPoolScope
	leased        map[string]bool
	invalid       bool
}
type AccountPoolScope struct {
	policy        *AccountPoolPolicy
	leaseInstance string
	leaseGroup    string
	leaseID       string
	keyID         string
	all           bool
	groups        map[string]bool
}

func (cfg *Config) CompileAccountPoolPolicy() *AccountPoolPolicy {
	if !cfg.AccountPools.Enabled {
		return nil
	}
	p := &AccountPoolPolicy{leased: make(map[string]bool), groups: make(map[string]bool), membership: make(map[string]string), keys: make(map[string]*AccountPoolScope)}
	if cfg.ValidateAccountPools() != nil {
		p.invalid = true
		return p
	}
	p.leaseRevision = cfg.PoolLeaseRevision()
	data, _ := json.Marshal(cfg.AccountPools)
	sum := sha256.Sum256(data)
	p.revision = hex.EncodeToString(sum[:])
	for _, group := range cfg.AccountPools.Groups {
		p.groups[group.ID] = !group.Disabled
		p.leased[group.ID] = group.Lease
		p.hasLeases = p.hasLeases || group.Lease
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
			scope.leaseInstance = rule.LeaseInstance
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
	return s.AuthorizesGroup(group) && ((!s.policy.leased[group] && s.leaseInstance == "") || s.leaseGroup == group)
}
func (s *AccountPoolScope) Namespace() string {
	return s.keyID + ":" + s.policy.revision + ":" + s.leaseID
}
func (s *AccountPoolScope) KeyID() string { return s.keyID }
func (s *AccountPoolScope) GroupForCredential(id string) string {
	return s.policy.GroupForCredential(id)
}

func (cfg *Config) PoolLeaseRevision() string {
	if !cfg.AccountPools.Enabled {
		return "disabled"
	}
	type assignment struct {
		ID      string
		Lease   bool
		Members []string
	}
	groups := make([]assignment, 0, len(cfg.AccountPools.Groups))
	for _, g := range cfg.AccountPools.Groups {
		members := append([]string(nil), g.CredentialIDs...)
		sort.Strings(members)
		groups = append(groups, assignment{g.ID, g.Lease, members})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	raw, _ := json.Marshal(groups)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *AccountPoolScope) LeaseInstance() string { return s.leaseInstance }
func (s *AccountPoolScope) AuthorizesGroup(id string) bool {
	return s.policy.groups[id] && (s.all || s.groups[id])
}
func (s *AccountPoolScope) LeaseGroups() []string {
	result := []string{}
	for id, on := range s.policy.leased {
		if on && s.AuthorizesGroup(id) {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}
func (s *AccountPoolScope) WithLease(group, id string) *AccountPoolScope {
	copy := *s
	copy.leaseGroup = group
	copy.leaseID = id
	return &copy
}
func (s *AccountPoolScope) AuthorizesCredential(id string) bool {
	return s.AuthorizesGroup(s.GroupForCredential(id))
}

func (p *AccountPoolPolicy) LeaseRevision() string { return p.leaseRevision }
func (p *AccountPoolPolicy) HasLeasePools() bool   { return p != nil && p.hasLeases }
