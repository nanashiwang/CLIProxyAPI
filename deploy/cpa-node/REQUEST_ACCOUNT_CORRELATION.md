# Request-to-account lookup

For ordinary HTTP/SSE API requests, the existing selected-auth callback now
emits one `credential selected` record at INFO level per selection notification.
Full request logging and debug logging are not required. The configured log
level must include INFO, and retained application logs must be available.

The record contains:

- `request_id`: the validated `X-NewAPI-Request-ID` from NAN, the compatibility
  `X-NAN-REQUEST-ID` header, or CPA's generated fallback.
- `cpa_execution_id`: a random internal ID for this incoming CPA request.
- `selection_seq`: the selection sequence within that CPA execution.
- `auth_index`: the existing credential index, also available in authenticated
  management account listings.
- `state`: always `selected`, never an inferred success or quality verdict.

Search the pool's application logs for the NAN request ID. Group matching
selection records by `cpa_execution_id`, then order by `selection_seq`.
Resolve the index using that pool's authenticated management account listing.
NAN channel logs identify the pool; this patch does not add a pool registry.

If NAN retries into the same pool, the main request ID stays unchanged but
`cpa_execution_id` changes. CPA-internal reselections share the execution ID.
Concurrent callbacks may be written out of sequence; use `selection_seq`
rather than physical line order.

Selection does not prove an upstream request was sent or completed, and the
last selected account must not automatically be called the successful account.
The existing HTTP completion log describes the overall incoming request only.
The existing `X-CPA-TRACE-ID` header remains unchanged and may reflect only
the account selected before response headers were committed.

No prompt, response body, email, credential filename, API key, or token is added
to these records. Identifiers are bounded to 128 ASCII letters, digits, dots,
underscores, or hyphens. Invalid incoming request IDs fall back to a local ID;
invalid/empty account indices are not written to selection logs.
Correlation headers are not authentication and can be supplied by callers.

This patch preserves existing callback coverage. In particular, the ordinary
request-metadata callback intentionally excludes downstream WebSocket upgrades;
it does not add per-turn tracing for persistent downstream WebSockets, infer
unreported executor-internal retries, or reconstruct older requests. It also
does not change log retention, account selection, billing, or account scoring.
Account removal, index changes, and expired logs can prevent historical lookup.
