# Kimi concurrency admission handling

Kimi can reject a request with `access_terminated_error` and the message
`You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again.`
That rejection means the request was not admitted. It does not establish that
credentials have expired or that the subscription quota is exhausted.

The Kimi executor normalizes only this exact structured error on HTTP 400, 403,
or 429 to a retryable HTTP 503 with code `upstream_concurrency`. The original
error remains in the cause chain and its body remains available for diagnostics.
Generic access termination, authentication failures, usage limits, malformed
responses, cancellation, and unsupported thinking settings retain their existing
classification. Unknown future upstream wording is deliberately not guessed.

The existing manager performs credential fallback within the caller's authorized
pool, retry limits, and request-scoped policy. The failed credential/model pair
receives a two-second admission cooldown instead of an authentication cooldown.
This is a local retry policy, not a provider-supplied reset estimate or network
timeout. Cooling opt-outs remain effective. Existing longer cooldowns are not
shortened, and concurrency failures do not create or increase quota backoff.
The cooldown is model-scoped because this response alone does not specify whether
the upstream limit is shared across all models. A sibling model is not presumed
unavailable.

Chat Completions, Responses, and delegated Messages execution all normalize
pre-stream errors at the Kimi boundary. No retries are added inside the executor.
Once stream data has been exposed, the existing manager forwards errors without
replaying the request on another credential.

## Verification

Regression tests cover structured-error classification and negative cases,
all three protocol entries in streaming and non-streaming mode, fallback from a
busy preferred credential to a healthy credential, automatic cooldown expiry,
cooling opt-outs, existing quota deadlines, and no replay after stream output.
Tests use synthetic credentials and mock upstream transports; they do not contact
Kimi or consume subscription quota.

This change does not alter thinking mode, model selection, proxy addresses,
account permissions, or subscription limits. Production deployment and live
acceptance remain separate from local verification.
