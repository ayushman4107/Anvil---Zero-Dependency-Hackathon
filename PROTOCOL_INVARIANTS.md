# Anvil — Protocol and State Invariants

## Purpose

These rules are non-negotiable behavioral constraints. An implementation change is incorrect if it violates an invariant even when happy-path tests pass.

Normative references:

- RFC 9110 — HTTP Semantics: https://www.rfc-editor.org/rfc/rfc9110.html
- RFC 9112 — HTTP/1.1: https://www.rfc-editor.org/rfc/rfc9112.html
- RFC 9209 — Proxy-Status: https://www.rfc-editor.org/rfc/rfc9209.html
- RFC 7239 — Forwarded: https://www.rfc-editor.org/rfc/rfc7239.html
- WHATWG Server-Sent Events: https://html.spec.whatwg.org/multipage/server-sent-events.html

## 1. TCP invariants

1. TCP is treated only as an ordered byte stream; read boundaries have no message meaning.
2. Every parser result must be identical whether input arrives in one read, one byte at a time, or arbitrary fragments.
3. A read can contain a partial message, exactly one message, or bytes belonging to multiple messages.
4. Bytes following a completed message remain available for the next sequential message.
5. A zero-byte read with an error is handled according to the error; EOF before a framed message completes is an incomplete-message failure.
6. Every admitted connection is eventually closed or deliberately remains in a bounded keep-alive state.
7. No client-controlled length causes an unchecked allocation or integer overflow.

## 2. Start-line invariants

1. HTTP/1.1 is the only supported protocol version in the mandatory implementation.
2. Request methods are validated as HTTP tokens and otherwise treated as method-agnostic; the proxy does not arbitrarily restrict valid method names.
3. The mandatory server accepts origin-form targets and the asterisk form needed by `OPTIONS *`.
4. Unsupported absolute-form or authority-form targets fail explicitly rather than being misinterpreted.
5. A malformed request line produces `400 Bad Request` and connection closure.
6. Upstream status codes must be three decimal digits; invalid status lines produce `502 Bad Gateway` downstream if no response has been committed.

## 3. Header invariants

1. Header names are compared case-insensitively and validated as tokens.
2. Original field order and duplicate fields are preserved until field-specific validation or proxy sanitation is complete.
3. Unknown end-to-end fields are forwarded unless blocked by explicit policy.
4. Obsolete folded field lines are rejected.
5. Bare LF, illegal control characters, whitespace before a colon, and CR/LF inside generated values are rejected.
6. Configured maximum header bytes and field count are enforced while reading, not after unbounded accumulation.
7. HTTP/1.1 requests require a valid `Host` field unless the request-target form defines equivalent authority handling.

## 4. Request body framing invariants

Framing is determined once, before body consumption.

1. A request containing both `Transfer-Encoding` and `Content-Length` is rejected and the connection is closed.
2. Unsupported transfer codings are rejected. If `Transfer-Encoding` is present, `chunked` must be final.
3. Multiple `Content-Length` values are accepted only if every parsed value is valid and identical; conflicting or invalid values are rejected.
4. A valid `Content-Length` means exactly that many octets are read.
5. Premature EOF or timeout before the declared length completes is an incomplete request and closes the connection.
6. A request without `Transfer-Encoding` or `Content-Length` has no message body; request bodies are never close-delimited.
7. A body exceeding the configured maximum produces `413 Content Too Large` where a safe response is possible, followed by connection closure.

## 5. Chunked coding invariants

1. Chunk size is parsed as hexadecimal with overflow and configured-size checks.
2. Chunk extensions may be ignored only after their syntax is bounded and safely skipped.
3. Each chunk is followed by the required CRLF.
4. A zero-size chunk terminates chunk data and begins the optional trailer section.
5. Trailer fields obey header syntax and size/count limits.
6. Forbidden framing or connection-specific trailer fields are rejected or discarded according to documented policy; they never alter the already determined framing.
7. Decoded body size is limited independently of encoded size.
8. The chunked writer never emits a terminal zero chunk until the stream is complete.

## 6. Response body invariants

1. Responses to `HEAD` never include body octets.
2. `1xx`, `204`, and `304` responses never include a message body.
3. A successful `CONNECT` tunnel is unsupported and must not accidentally enter normal response-body logic.
4. If upstream response framing is invalid, the upstream connection is closed and a `502` is generated only if downstream bytes have not been committed.
5. A close-delimited upstream response is read until close subject to body and timeout limits; that upstream connection is not reused.
6. Generated non-streaming responses have an accurate `Content-Length`.
7. Generated streaming/SSE responses use valid chunked coding and do not also send `Content-Length`.

## 7. Persistence invariants

1. HTTP/1.1 connections are persistent by default unless either side requests closure or an error makes reuse unsafe.
2. Anvil processes requests sequentially per downstream connection.
3. Concurrent HTTP/1.1 pipelining is not supported; documentation states this limitation.
4. A connection with an unrecoverable parse/framing error is never reused.
5. Read, write, and idle deadlines are refreshed only at defined lifecycle boundaries.

## 8. Router invariants

1. Method matching occurs before path matching, except explicitly registered any-method proxy routes.
2. Static segments take precedence over parameters; parameters take precedence over terminal wildcards.
3. A wildcard is terminal.
4. Registration rejects ambiguous duplicates.
5. Percent-decoding policy is applied once and consistently; matching never double-decodes.
6. Query strings are not part of path-tree matching.
7. The route tree is immutable after traffic starts.

## 9. Proxy invariants

1. A request is parsed and validated before proxy mutation; Anvil never assumes the first TCP read contains all headers.
2. `Connection` is parsed first, every field it names is removed, and `Connection` itself is removed or replaced.
3. Known hop-by-hop fields such as `Proxy-Connection`, `Keep-Alive`, `TE`, and inbound `Transfer-Encoding` are handled according to HTTP semantics rather than forwarded blindly.
4. Anvil adds an appropriate `Via` entry to forwarded requests.
5. Backend aliases, not private addresses, are used in user-visible `Proxy-Status` by default.
6. Client-supplied forwarding headers are untrusted unless the immediate peer is configured as trusted.
7. Generated forwarding metadata cannot contain unsanitized client text.
8. `Host`/authority is rewritten according to route configuration.
9. Sensitive request or response headers are never copied into telemetry events.
10. A backend ineligible by health or circuit state cannot be selected.
11. Every in-flight reservation is released exactly once on every return path.

## 10. Retry invariants

1. A retry is considered only before any downstream response bytes are committed.
2. Automatic retries default to `GET` and `HEAD`; other methods require an explicit, documented replay policy.
3. A request body is replayed only when fully buffered and known to be unchanged.
4. Retry count and total retry time are bounded.
5. Each attempt receives a separate event and backend outcome.
6. An upstream application status is not retried unless an explicit policy declares that status retryable.
7. A retry targets a distinct backend alias; exhaustion never loops back to the same failed node within one transaction.
8. If an application response selected for retry cannot reach another eligible backend, the original complete response is returned.

## 11. Health and circuit invariants

1. Active health and passive request outcomes are distinct inputs.
2. Circuit transitions are serialized per backend.
3. `OPEN` denies normal traffic.
4. `HALF_OPEN` permits no more than the configured number of probe requests.
5. A failed half-open probe returns the circuit to `OPEN` and restarts cooldown.
6. Recovery requires the configured success evidence; time passing alone does not mark a backend healthy.
7. Every transition has one ledger event containing previous state, new state, backend alias, and reason.
8. Selection reads a coherent state snapshot and never holds a state lock during network I/O.
9. Active health failure/recovery thresholds change health eligibility; they do not silently rewrite passive counters.
10. Transition callbacks execute after the backend state lock is released.

## 12. Upstream connection-pool invariants

1. Idle capacity and idle lifetime are finite per backend.
2. A connection is reusable only after Anvil has parsed one complete persistent HTTP response with unambiguous framing.
3. Close-delimited, `Connection: close`, expired, failed, timed-out, canceled, malformed, surplus, and shutdown connections are closed rather than pooled.
4. A checked-out connection has one transaction owner. Idle-pool locks are never held during network I/O.
5. Pool shutdown prevents later recycling and closes every currently idle connection.

## 13. Concurrency and resource invariants

1. One slow client cannot prevent acceptance or processing of unrelated admitted clients.
2. Connection, body, header, event, subscriber, benchmark-worker, and fixture counts are bounded.
3. No mutex is held across dial, read, write, sleep, handler execution, or observer publication.
4. Metrics updates do not require a global data-plane lock.
5. Slow SSE clients lose events or are disconnected; they never back-pressure the proxy.
6. Graceful shutdown stops acceptance, cancels background work, allows bounded drain time, and then closes remaining connections.
7. Goroutines started by a component have an explicit cancellation and join path.

## 14. Administration and telemetry invariants

1. The administration listener binds to loopback unless explicitly configured otherwise.
2. Experiment mutation endpoints are not exposed on the public proxy listener.
3. The ledger has fixed capacity and monotonically increasing sequence numbers.
4. Ledger events contain metadata only: no bodies, credentials, cookies, or authorization values.
5. Receipt metrics are derived from ledger events and counters, not browser-rendered values.
6. Percentiles are labelled as histogram estimates.
7. SSE uses UTF-8 `text/event-stream`, event IDs, bounded queues, and heartbeat comments.
8. Phase 6 accepts only an explicit loopback IP for the administration listener; hostnames, wildcard, and non-loopback addresses are rejected before bind.
9. Event sequence assignment, ledger order, and live publication order are identical.
10. Subscription is established before replay is captured; duplicate replay/live IDs are suppressed.
11. An expired or future `Last-Event-ID` produces an explicit gap event and replays the retained window.
12. A full subscriber queue never waits; it increments the observer-drop counter.
13. Metrics JSON and the dashboard are read-only, non-cacheable, and served only by the administration listener.
14. Runtime and histogram values are observations, never inputs to backend selection or circuit transitions.

## 15. Error mapping invariants

| Condition | Downstream result before commit |
|---|---|
| Malformed request/framing | `400 Bad Request` and close |
| Missing required body length by policy | `411 Length Required` |
| Body over configured limit | `413 Content Too Large` and close |
| Unsupported transfer coding | `501 Not Implemented` or documented `400`, and close |
| Route absent | `404 Not Found` |
| Method unsupported by internal route | `405 Method Not Allowed` |
| No eligible backend / admission saturation | `503 Service Unavailable` |
| Upstream protocol/refusal/incomplete response | `502 Bad Gateway` |
| Upstream timeout | `504 Gateway Timeout` |
| Internal handler failure | `500 Internal Server Error` without process crash |

The chosen mapping must remain consistent across status, `Proxy-Status`, ledger reason, metrics, tests, and documentation.

Phase 5 emits the following RFC 9209 error types. `next-hop` is omitted when no backend was selected and otherwise contains only the validated backend alias.

| Anvil condition | `Proxy-Status` error |
|---|---|
| No healthy/circuit-eligible backend | `destination_unavailable` |
| Backend admission exhausted | `connection_limit_reached` |
| Loopback/backend connection refused | `connection_refused` |
| Dial timeout | `connection_timeout` |
| Write timeout | `connection_write_timeout` |
| Connection terminates during write | `connection_terminated` |
| Response read timeout | `connection_read_timeout` |
| Incomplete framed response | `http_response_incomplete` |
| Other invalid upstream HTTP | `http_protocol_error` |

A forwarded response uses `received-status=<code>` plus the backend alias. Existing upstream `Proxy-Status` members are preserved before Anvil appends its own member.

## 16. Unsupported-feature behavior

- HTTP/2 preface: reject safely; do not parse as HTTP/1.1.
- `Upgrade`/WebSocket: reject explicitly and close.
- `Expect: 100-continue`: either implement correctly or return a documented `417 Expectation Failed`; never wait indefinitely.
- CONNECT: reject explicitly.
- TLS bytes on a plaintext listener: fail safely without logging raw payloads.
