package config

import (
	"fmt"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	DefaultOpenCodeZenURL = "https://opencode.ai/zen"
	DefaultOpenCodeGoURL  = "https://opencode.ai/zen/go"
)

// SanitizeOpenCode normalizes OpenCode settings without accepting arbitrary upstream URLs.
func (cfg *Config) SanitizeOpenCode() {
	if cfg == nil {
		return
	}
	oc := &cfg.OpenCode
	oc.Prefer = strings.ToLower(strings.TrimSpace(oc.Prefer))
	if oc.Prefer != "zen" && oc.Prefer != "go" {
		oc.Prefer = "go"
	}
	if oc.RefreshSeconds <= 0 {
		oc.RefreshSeconds = 300
	}
	if oc.RefreshSeconds > 86400 {
		oc.RefreshSeconds = 86400
	}
	oc.Zen = sanitizeOpenCodeTier(oc.Zen, DefaultOpenCodeZenURL)
	oc.Go = sanitizeOpenCodeTier(oc.Go, DefaultOpenCodeGoURL)
	if len(oc.ProtocolOverrides) > 0 {
		overrides := normalizeOpenCodeProtocolOverrides(oc.ProtocolOverrides)
		if len(overrides) == 0 {
			oc.ProtocolOverrides = nil
		} else {
			oc.ProtocolOverrides = overrides
		}
	}
}

func normalizeOpenCodeProtocolOverrides(overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return nil
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	clean := make(map[string]string, len(overrides))
	for _, rawModel := range keys {
		model := strings.ToLower(strings.TrimSpace(rawModel))
		protocol := strings.ToLower(strings.TrimSpace(overrides[rawModel]))
		if model == "" || (protocol != "chat" && protocol != "responses" && protocol != "anthropic") {
			continue
		}
		// Case-insensitive duplicate keys resolve to the first lexical key.
		if _, exists := clean[model]; !exists {
			clean[model] = protocol
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func normalizeOpenCodeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	clean := make(map[string]string, len(headers))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		value := strings.TrimSpace(headers[rawKey])
		if key == "" || value == "" || !httpguts.ValidHeaderFieldName(key) || strings.ContainsAny(value, "\r\n") {
			continue
		}
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if canonical == "" {
			continue
		}
		if _, exists := clean[canonical]; !exists {
			clean[canonical] = value
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func sanitizeOpenCodeTier(tier OpenCodeTierConfig, fallback string) OpenCodeTierConfig {
	tier.BaseURL = strings.TrimSpace(tier.BaseURL)
	if tier.BaseURL == "" {
		tier.BaseURL = fallback
	}
	tier.Headers = normalizeOpenCodeHeaders(tier.Headers)
	entries := make([]OpenCodeAPIKey, 0, len(tier.APIKeyEntries))
	for _, entry := range tier.APIKeyEntries {
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.Note = strings.TrimSpace(entry.Note)
		entry.APIKeyConfigured = false
		entry.APIKeyPreview = ""
		entry.SourceIndex = nil
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = normalizeOpenCodeHeaders(entry.Headers)
		if entry.APIKey == "" {
			continue
		}
		entries = append(entries, entry)
	}
	tier.APIKeyEntries = entries
	return tier
}

// ValidateOpenCode validates fixed OpenCode endpoints to prevent credential SSRF.
func (cfg *Config) ValidateOpenCode() error {
	if cfg == nil || !cfg.OpenCode.Enabled {
		return nil
	}
	if cfg.OpenCode.Prefer != "zen" && cfg.OpenCode.Prefer != "go" {
		return fmt.Errorf("opencode.prefer must be zen or go")
	}
	if err := validateOpenCodeURL(cfg.OpenCode.Zen.BaseURL, "/zen"); err != nil {
		return fmt.Errorf("opencode.zen.base-url: %w", err)
	}
	if err := validateOpenCodeURL(cfg.OpenCode.Go.BaseURL, "/zen/go"); err != nil {
		return fmt.Errorf("opencode.go.base-url: %w", err)
	}
	if len(cfg.OpenCode.Zen.APIKeyEntries) == 0 && len(cfg.OpenCode.Go.APIKeyEntries) == 0 && !cfg.OpenCode.Anonymous {
		return fmt.Errorf("opencode requires at least one Zen/Go key unless anonymous is enabled")
	}
	return nil
}

// IsAllowedOpenCodeBaseURL reports whether raw is one of the fixed OpenCode endpoints.
func IsAllowedOpenCodeBaseURL(raw string) bool {
	return IsAllowedOpenCodeBaseURLForTier(raw, "")
}

// IsAllowedOpenCodeBaseURLForTier reports whether raw is the fixed endpoint for tier.
// An empty tier accepts either endpoint for callers that do not have auth context.
func IsAllowedOpenCodeBaseURLForTier(raw, tier string) bool {
	raw = strings.TrimSpace(raw)
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		return validateOpenCodeURL(raw, "/zen") == nil || validateOpenCodeURL(raw, "/zen/go") == nil
	}
	switch tier {
	case "zen":
		return validateOpenCodeURL(raw, "/zen") == nil
	case "go":
		return validateOpenCodeURL(raw, "/zen/go") == nil
	default:
		return false
	}
}

func validateOpenCodeURL(raw, expectedPath string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "opencode.ai") || u.Path != expectedPath || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("must be exactly https://opencode.ai%s", expectedPath)
	}
	return nil
}
