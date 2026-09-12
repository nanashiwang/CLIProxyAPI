package config

import (
	"testing"
)

func poolConfigFixture() *Config {
	return &Config{SDKConfig: SDKConfig{APIKeys: []string{"a", "b", "all", "legacy"}, AccountPools: AccountPoolsConfig{Enabled: true, Groups: []AccountPoolGroup{
		{ID: "default", Name: "Default"}, {ID: "a", Name: "Custom A", CredentialIDs: []string{"auth-a"}}, {ID: "b", Name: "Custom B", CredentialIDs: []string{"auth-b"}},
	}, KeyRules: []AccountPoolKeyRule{
		{KeyHash: ClientKeyFingerprint("a"), Scope: "selected", GroupIDs: []string{"a"}},
		{KeyHash: ClientKeyFingerprint("b"), Scope: "selected", GroupIDs: []string{"b"}},
		{KeyHash: ClientKeyFingerprint("all"), Scope: "all"},
	}}}}
}
func TestAccountPoolPolicyScopeAndRename(t *testing.T) {
	cfg := poolConfigFixture()
	if err := cfg.ValidateAccountPools(); err != nil {
		t.Fatal(err)
	}
	p := cfg.CompileAccountPoolPolicy()
	a, _ := p.Resolve(ClientKeyFingerprint("a"))
	b, _ := p.Resolve(ClientKeyFingerprint("b"))
	all, _ := p.Resolve(ClientKeyFingerprint("all"))
	legacy, _ := p.Resolve(ClientKeyFingerprint("legacy"))
	if !a.Allows("auth-a") || a.Allows("auth-b") || a.Allows("unassigned") || !b.Allows("auth-b") || !all.Allows("auth-a") || !all.Allows("unassigned") || legacy.Allows("auth-a") || !legacy.Allows("unassigned") {
		t.Fatal("unexpected group permissions")
	}
	cfg.AccountPools.Groups[1].Name = "Renamed"
	changed, _ := cfg.CompileAccountPoolPolicy().Resolve(ClientKeyFingerprint("a"))
	if !changed.Allows("auth-a") || changed.Namespace() == a.Namespace() {
		t.Fatal("rename lost binding or did not invalidate cached policy")
	}
	cfg.APIKeys = []string{"b"}
	if _, ok := cfg.CompileAccountPoolPolicy().Resolve(ClientKeyFingerprint("a")); ok {
		t.Fatal("removed key still has access")
	}
}
func TestAccountPoolPolicyAllIncludesFutureGroupsAndSelectedDoesNot(t *testing.T) {
	cfg := poolConfigFixture()
	cfg.AccountPools.Groups = append(cfg.AccountPools.Groups, AccountPoolGroup{ID: "future", Name: "Future", CredentialIDs: []string{"new"}})
	p := cfg.CompileAccountPoolPolicy()
	all, _ := p.Resolve(ClientKeyFingerprint("all"))
	a, _ := p.Resolve(ClientKeyFingerprint("a"))
	if !all.Allows("new") || a.Allows("new") {
		t.Fatal("future groups not handled correctly")
	}
	cfg.AccountPools.Groups[3].Disabled = true
	all, _ = cfg.CompileAccountPoolPolicy().Resolve(ClientKeyFingerprint("all"))
	if all.Allows("new") {
		t.Fatal("disabled group is callable")
	}
	cfg.AccountPools.KeyRules[0].GroupIDs = nil
	a, _ = cfg.CompileAccountPoolPolicy().Resolve(ClientKeyFingerprint("a"))
	if a.Allows("auth-a") || a.Allows("unassigned") {
		t.Fatal("empty selection became all-access")
	}
}
func TestAccountPoolConfigRejectsAmbiguousBindings(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"duplicate group", func(c *Config) { c.AccountPools.Groups = append(c.AccountPools.Groups, c.AccountPools.Groups[1]) }},
		{"duplicate account", func(c *Config) { c.AccountPools.Groups[2].CredentialIDs = []string{"auth-a"} }},
		{"missing group", func(c *Config) { c.AccountPools.KeyRules[0].GroupIDs = []string{"missing"} }},
		{"unknown mode", func(c *Config) { c.AccountPools.KeyRules[0].Scope = "typo" }},
		{"no default", func(c *Config) { c.AccountPools.Groups = c.AccountPools.Groups[1:] }},
		{"all with explicit groups", func(c *Config) { c.AccountPools.KeyRules[0].Scope = "all" }},
		{"malformed hash", func(c *Config) { c.AccountPools.KeyRules[0].KeyHash = "not-a-key" }},
		{"Home", func(c *Config) { c.Home.Enabled = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := poolConfigFixture()
			tc.change(cfg)
			if cfg.ValidateAccountPools() == nil {
				t.Fatal("invalid configuration accepted")
			}
			if _, ok := cfg.CompileAccountPoolPolicy().Resolve(ClientKeyFingerprint("all")); ok {
				t.Fatal("invalid policy fell back to unrestricted access")
			}
		})
	}
}

func TestLeaseRevisionIgnoresNamesButProtectsMembership(t *testing.T) {
	c := &Config{SDKConfig: SDKConfig{AccountPools: AccountPoolsConfig{Enabled: true, Groups: []AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "a", Name: "A", Lease: true, CredentialIDs: []string{"one", "two"}}}}}}
	original := c.PoolLeaseRevision()
	c.AccountPools.Groups[1].Name = "Renamed"
	c.AccountPools.Groups[1].Disabled = true
	if c.PoolLeaseRevision() != original {
		t.Fatal("rename/disable changes membership signature")
	}
	c.AccountPools.Groups[1].CredentialIDs = []string{"two", "one"}
	if c.PoolLeaseRevision() != original {
		t.Fatal("ordering changes membership signature")
	}
	c.AccountPools.Groups[1].CredentialIDs = []string{"one"}
	if c.PoolLeaseRevision() == original {
		t.Fatal("membership change not detected")
	}
}
