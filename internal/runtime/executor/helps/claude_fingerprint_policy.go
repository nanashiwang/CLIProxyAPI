package helps

import (
	"strings"
	"sync"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	ClaudeFingerprintProfileDefault       = config.ClaudeFingerprintProfileDefault
	ClaudeFingerprintProfileClaudeCodeCLI = config.ClaudeFingerprintProfileClaudeCodeCLI
	ClaudeFingerprintProfileAttr          = "fingerprint_profile"
)

// ClaudeFingerprintProfileWarned deduplicates the unrecognized-value warning.
// Profile resolution runs several times per request (policy, wire policy,
// headers), so warning on every call turns one config typo into a per-request
// log flood. Management writes reject unknown values outright; this only covers
// values that reached the process through a config file or auth JSON.
var ClaudeFingerprintProfileWarned sync.Map

// ClaudeFingerprintPolicy is a single switch-driven view of Claude fingerprint
// behavior for Anthropic Messages. The heavy algorithms stay shared:
//   - betas: claudeCodeCLIBetas(..., useOAuthBetas)
//   - CCH: claudeCCHSigningEnabled / finalizeAnthropicMessagesBodyCCH
//   - identity: EnsureClaudeCLIFingerprintIdentity + ApplyClaudeCredentialMetadata
//
// Goal: Anthropic Messages API keys, custom gateways, and delegated providers
// (such as Kimi) can opt into the Claude Code OAuth CLI request fingerprint via
// fingerprint-profile=claude-code-cli, without OAuth control-plane semantics.
// Real Claude OAuth tokens always keep the strict CLI fingerprint. First-party
// api.anthropic.com API keys stay caller-owned by default and only take the CLI
// Messages fingerprint when this field is set. MCP aliases and diagnostics are
// wire fingerprint behavior; refresh, profile and cancellation stay gated on
// AuthIsOAuthToken.
type ClaudeFingerprintPolicy struct {
	AuthIsOAuthToken     bool
	ProfileClaudeCodeCLI bool
	UseOAuthBetas        bool
	ApplyCLIIdentity     bool
	SynthesizeIdentity   bool
	MCPAlias             bool
	InjectDiagnostics    bool
	OAuthCancellation    bool
}

func NormalizeClaudeFingerprintProfile(raw string) string {
	profile, ok := config.NormalizeClaudeFingerprintProfile(raw)
	if !ok {
		if _, warned := ClaudeFingerprintProfileWarned.LoadOrStore(strings.TrimSpace(raw), struct{}{}); !warned {
			log.Warnf("unrecognized claude fingerprint-profile %q (supported: %q); falling back to default", raw, ClaudeFingerprintProfileClaudeCodeCLI)
		}
	}
	return profile
}

func ClaudeFingerprintProfileFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ClaudeFingerprintProfileDefault
	}
	if auth.Attributes != nil {
		if raw, ok := auth.Attributes[ClaudeFingerprintProfileAttr]; ok && strings.TrimSpace(raw) != "" {
			return NormalizeClaudeFingerprintProfile(raw)
		}
	}
	for _, key := range []string{ClaudeFingerprintProfileAttr, "fingerprint-profile"} {
		raw := claudeauth.ReadMetadataString(&auth.Metadata, key)
		if strings.TrimSpace(raw) != "" {
			return NormalizeClaudeFingerprintProfile(raw)
		}
	}
	return ClaudeFingerprintProfileDefault
}

func claudeFingerprintProfileFromEntry(entry *config.ClaudeKey, auth *cliproxyauth.Auth) string {
	if profile := ClaudeFingerprintProfileFromAuth(auth); profile != ClaudeFingerprintProfileDefault {
		return profile
	}
	if entry == nil {
		return ClaudeFingerprintProfileDefault
	}
	return NormalizeClaudeFingerprintProfile(entry.FingerprintProfile)
}

// ResolveClaudeFingerprintPolicy resolves credential-scoped fingerprint
// behavior. It is deliberately independent of the upstream origin: the wire
// profile follows the credential, while the one origin-sensitive decision (CCH
// signing) is resolved separately by claudeCCHSigningEnabled.
func ResolveClaudeFingerprintPolicy(entry *config.ClaudeKey, auth *cliproxyauth.Auth, authIsOAuth bool) ClaudeFingerprintPolicy {
	// Keep actual Claude OAuth lifecycle authority separate from the broader
	// request fingerprint policy used by API keys and delegated providers.
	profile := claudeFingerprintProfileFromEntry(entry, auth)
	profileClaudeCodeCLI := authIsOAuth || profile == ClaudeFingerprintProfileClaudeCodeCLI

	return ClaudeFingerprintPolicy{
		AuthIsOAuthToken:     authIsOAuth,
		ProfileClaudeCodeCLI: profileClaudeCodeCLI,
		UseOAuthBetas:        profileClaudeCodeCLI,
		ApplyCLIIdentity:     profileClaudeCodeCLI,
		SynthesizeIdentity:   profileClaudeCodeCLI && !authIsOAuth,
		MCPAlias:             profileClaudeCodeCLI,
		InjectDiagnostics:    profileClaudeCodeCLI,
		OAuthCancellation:    authIsOAuth,
	}
}
