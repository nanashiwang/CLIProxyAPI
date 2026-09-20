# Experimental Codex WebSocket steering

This branch selectively ports upstream `42c9680eee553b047e46ac85a6b1543e87e526e7`.
Enable it explicitly in the server configuration:

```yaml
codex:
  response-steering: true
```

The default is `false`. This setting applies to Codex upstream WebSocket execution;
HTTP/SSE and other providers keep their existing request paths. It is independent
of account-level WebSocket eligibility. No production configuration is changed by
installing this release.

## Connection and input contract

- Start each downstream socket with a full `response.create`. A response ID from
  another socket is rejected before an upstream connection is opened.
- Once `response.created` arrives, the socket can receive `response.steer` while
  output is streaming. Steering acknowledgements and pending/failure events come
  from upstream and retain their payloads. There are no synthetic acceptances.
- Explicit `response.create`/`response.append` requests are queued around steering
  continuations. Pending tool results can resume the waiting response. Automatic
  successors retain their parent's response settings; queued explicit requests do
  not overwrite those settings.
- One live socket remains bound to one account, model and upstream connection.
  Model changes require a fresh connection and full history. Accepted input is
  never reconnected, replayed or transferred to another account. Only a rejected
  initial request, before `response.created`, can use existing bootstrap retry.
- Downstream input, pending creates and outstanding steering submissions are
  bounded (16 entries per queue). Steering overflow is a recoverable local error.
- Duplex mode forwards `response.created` immediately to unblock steering. The
  optional ordinary-stream bootstrap buffer does not delay that event in duplex
  mode; overloads after it cannot transparently retry on another account.

## Local account-pool protections

Exclusive leases remain active for the entire duplex producer lifetime, including
idle periods between completed responses. Cancellation closes the upstream socket,
joins its writer and drains the producer before releasing the in-flight lease.
Every subsequent create/append/steer write rechecks live account availability,
account-pool permissions and the admitted lease. Revocation, expiry or cooling
terminates the socket without changing its account.

User `1` temporary account access retains the ordinary per-request WebSocket path.
It cannot use duplex steering. Raw continuation frames (`previous_response_id` or
`response.append`) are rejected before normalization can remove their markers; use
full conversation history. The SDK also rejects temporary duplex admission before
allocating an account.

Socket failures, cancellation and local validation do not cool credentials or
permit exclusive-account replacement. Cancellation also cannot clear a cooldown
recorded by a concurrent request. Explicit upstream authentication and quota
failures retain the existing result policy, model/account scope, reset grace
period and cooldown-extension semantics. An already-started stream ends on such a
failure; account replacement can only happen through normal admission of a fresh
full-history request after active producers drain.

The port reuses this branch's request translation, native responses-lite output,
reasoning replay, usage/model/transport observation and quota event observer. It
does not import unrelated upstream model compatibility or plugin event changes.

## Validation and rollout

Regression coverage uses real loopback WebSockets through the handler, auth manager
and executor. It covers in-flight steering, successor settings, tool-input waits,
queued creates, rejected bootstrap input, recoverable errors, credential failures,
connection timeouts, cancellation, bounded steering, exclusive occupancy,
temporary continuation rejection, permission revocation and cooldown preservation.
Existing HTTP/SSE, previous-response, lease failover and result-policy tests remain
part of the full suite.

Enable the flag only when clients support this protocol. Keep the existing lease
state and lock files during deployment. Drain active sockets before rollback or
restart. Release publication does not deploy the service or enable the feature.
