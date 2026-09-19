package usage

import coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"

// normalizeRequestMetadata also protects JSONL reloads and imported snapshots.
// Missing historical fields are never inferred from a provider or endpoint.
func normalizeRequestMetadata(detail *RequestDetail) {
	detail.ClientTransport = coreusage.NormalizeTransport(detail.ClientTransport)
	detail.UpstreamTransport = coreusage.NormalizeTransport(detail.UpstreamTransport)
	detail.ClientIP = coreusage.NormalizeClientIP(detail.ClientIP)
	detail.UserAgent = coreusage.NormalizeUserAgent(detail.UserAgent)
}
