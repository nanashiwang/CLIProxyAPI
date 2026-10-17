# Agent Note: Preserve Claude protocol capabilities independently of fingerprint recognition

Status: implemented

## Problem

The OAuth request builder replaced extensions from unrecognized clients with a measured CLI baseline. New Claude Code clients therefore lost server review and per-turn capabilities even though their request bodies still contained the corresponding fields. Version equality is a fingerprint policy, not a valid authority for dropping requested protocol features.

## Decision

Merge caller header and body beta values into the existing credential baseline in the shared request builder. Deduplicate case-sensitive tokens and discard whitespace/empty entries. This covers JSON, streaming and token-counting paths for OAuth, API keys and explicit CLI profiles. Preserve existing credential-specific additions and custom-header precedence.

Do not broaden native-client detection. Its other consumers control cloaking, device identity, cache placement and TTL, so changing it would exceed the narrowly verified protocol fix. No new API, billing change, approval fabrication or deployment is included.

## Alternatives considered

- Spoofing an older client version hides protocol skew and changes unrelated behavior; rejected.
- Adding only the observed beta to a fixed list fails again for future extensions; rejected.
- Globally relaxing native-client detection changes cache and identity behavior; protocol capabilities instead have independent ownership.
- Forwarding all HTTP headers exposes credentials and cookies; only beta capability values are merged here.

## Consequences

Caller capabilities can now reach the upstream even if the client is newer than the local fingerprint baseline. Unknown betas may be rejected by the provider; that rejection must remain visible and must not trigger silent safety downgrades. Existing strict identity and cache behavior remains, so this does not claim to fix all client-shape or cache issues. No production behavior is changed until a separately approved deployment.

## Testing

The full Go suite and server build pass locally. Targeted race tests cover credential modes, direct/gateway URLs, streaming and non-streaming requests, missing identity, multiple beta lines, body betas, deduplication and credential isolation. Local HTTP executor tests preserve safeguards, message-level output_config, flagged/not_flagged results and tool IDs. Unsupported-capability errors remain visible without silent retries. These are synthetic local tests, not a new production-provider acceptance run.
