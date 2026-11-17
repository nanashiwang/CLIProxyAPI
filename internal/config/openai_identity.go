package config

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// EnsureOpenAICompatibilityIDs freezes legacy identities before mutable fields are edited.
// Existing IDs (including their historical spelling) are opaque and never recomputed.
func (cfg *Config) EnsureOpenAICompatibilityIDs() bool {
	if cfg == nil {
		return false
	}
	changed := false
	missing := make(map[*OpenAICompatibilityAPIKey]bool)
	providers, credentials := NewStableIDGenerator(), NewStableIDGenerator()
	for i := range cfg.OpenAICompatibility {
		p := &cfg.OpenAICompatibility[i]
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" {
			name = "openai-compatibility"
		}
		kind := "openai-compatibility:" + name
		if p.Disabled {
			kind = "openai-disabled:" + name
		}
		providerID, _ := providers.Next("openai-provider", name, p.BaseURL)
		if len(p.APIKeyEntries) == 0 {
			// Keyless providers also use their ID as the runtime credential ID.
			providerID, _ = credentials.Next(kind, p.BaseURL)
		}
		if p.ID == "" {
			p.ID = providerID
			changed = true
		}
		for j := range p.APIKeyEntries {
			key := &p.APIKeyEntries[j]
			id, _ := credentials.Next(kind, key.APIKey, p.BaseURL, key.ProxyURL)
			if key.ID == "" {
				missing[key] = true
				key.ID = id
				changed = true
			}
			key.Note = strings.TrimSpace(key.Note)
		}
	}
	cfg.recoverLegacyOpenAIGroupIdentity(missing)
	return changed
}

// recoverLegacyOpenAIGroupIdentity adopts an existing orphan ID instead of moving
// membership or leases. Never guess across duplicate credentials or multiple bindings.
func (cfg *Config) recoverLegacyOpenAIGroupIdentity(missing map[*OpenAICompatibilityAPIKey]bool) {
	if len(missing) == 0 {
		return
	}
	present := make(map[string]bool)
	bound := make(map[string]bool)
	for _, p := range cfg.OpenAICompatibility {
		present[p.ID] = true
		for _, key := range p.APIKeyEntries {
			present[key.ID] = true
		}
	}
	for _, group := range cfg.AccountPools.Groups {
		for _, id := range group.CredentialIDs {
			bound[id] = true
		}
	}
	candidates := make(map[*OpenAICompatibilityAPIKey][]string)
	for id := range bound {
		if present[id] || !strings.HasPrefix(id, "openai-compatibility:") {
			continue
		}
		separator := strings.LastIndex(id, ":")
		kind := id[:separator]
		var matched *OpenAICompatibilityAPIKey
		count := 0
		for i := range cfg.OpenAICompatibility {
			p := &cfg.OpenAICompatibility[i]
			for j := range p.APIKeyEntries {
				key := &p.APIKeyEntries[j]
				legacy, _ := NewStableIDGenerator().Next(kind, key.APIKey, p.BaseURL, key.ProxyURL)
				if legacy == id {
					matched = key
					count++
				}
			}
		}
		if count == 1 && missing[matched] && !bound[matched.ID] {
			candidates[matched] = append(candidates[matched], id)
		}
	}
	for key, ids := range candidates {
		if len(ids) == 1 {
			key.ID = ids[0]
		}
	}
}

// ValidateOpenAICompatibilityIDs prevents two entries from sharing runtime identity.
func (cfg *Config) ValidateOpenAICompatibilityIDs() error {
	seen := make(map[string]bool)
	for _, p := range cfg.OpenAICompatibility {
		for _, id := range append([]string{p.ID}, openAIKeyIDs(p.APIKeyEntries)...) {
			if id == "" {
				continue
			}
			if strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\x00") || len(id) > 256 {
				return fmt.Errorf("invalid OpenAI-compatible identity")
			}
			if seen[strings.ToLower(id)] {
				return fmt.Errorf("duplicate OpenAI-compatible identity")
			}
			seen[strings.ToLower(id)] = true
		}
	}
	return nil
}

func openAIKeyIDs(keys []OpenAICompatibilityAPIKey) []string {
	ids := make([]string, len(keys))
	for i := range keys {
		ids[i] = keys[i].ID
	}
	return ids
}

// PreserveOpenAICompatibilityIDs supports older clients that omit identity metadata.
// Matching must be unique; array position is never an identity fallback.
func PreserveOpenAICompatibilityIDs(next, previous []OpenAICompatibility) {
	for i := range next {
		p := &next[i]
		var old *OpenAICompatibility
		for j := range previous {
			candidate := &previous[j]
			match := p.ID != "" && p.ID == candidate.ID
			if p.ID == "" {
				match = p.Name == candidate.Name && p.BaseURL == candidate.BaseURL
			}
			if match {
				if old != nil {
					old = nil
					break
				}
				old = candidate
			}
		}
		if old != nil && p.ID == "" {
			p.ID = old.ID
		}
		if p.ID == "" {
			p.ID = "openai-provider:" + uuid.NewString()
		}
		for j := range p.APIKeyEntries {
			key := &p.APIKeyEntries[j]
			if key.ID != "" {
				continue
			}
			if old != nil {
				var match *OpenAICompatibilityAPIKey
				for k := range old.APIKeyEntries {
					candidate := &old.APIKeyEntries[k]
					if candidate.APIKey == key.APIKey && candidate.ProxyURL == key.ProxyURL {
						if match != nil {
							match = nil
							break
						}
						match = candidate
					}
				}
				if match != nil {
					key.ID = match.ID
				}
			}
			if key.ID == "" {
				key.ID = "openai-key:" + uuid.NewString()
			}
		}
	}
}
