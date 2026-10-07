# Cursor stream failure boundaries

Version 0.6.3 repairs plugin-owned handoff and checkpoint replay decisions.
The full host-level behavior in the structured-failure table below additionally
requires the matching CPA host structured-plugin-error patch. Official
CLIProxyAPI v7.3.17 still discards stream-close details at its native JSON bridge.
Its non-stream RPC envelope preserves code/message/HTTP status, but not the
request-scope or exposure flags. An official host upgrade alone does not provide
the full behavior.

## Native tools

The gateway never runs native Cursor shell/read/grep operations. It returns the
complete failure/stream-close protocol and only recommends a matching tool
actually present in the current client catalog, such as `bash` or `read_file`.
If there is no matching client tool, it reports the operation as unavailable.
The client still owns permissions and tool execution.

A native refusal counts as an interaction response. A later timeout must not
replay the checkpoint after that response, even when no client text or tool call
has yet been emitted. Existing post-output and tool-result replay guards remain.

## Structured failures

`host.stream.close` retains the legacy `error` string and adds optional
`error_details` with `code`, `message`, `http_status`, `retryable`,
`request_scoped`, `output_exposed`, `tool_exposed`, and
`interaction_responded`. RPC execution errors carry the same fields.

| Failure | Classification | Current request | Next distinct request |
| --- | --- | --- | --- |
| Meaningful-progress watchdog | `cursor_progress_timeout`, HTTP 504, request-scoped | Stop; do not replay | Account remains eligible |
| First-data/frame watchdog | `cursor_transport_timeout`, HTTP 504, request-scoped | Stop; do not replay | Account remains eligible |
| Request deadline/cancellation | HTTP 504/499, request-scoped | Stop | Account remains eligible |
| Real upstream HTTP/Connect auth or quota error | Preserve 401/403/429 | Existing host policy, unless exposure forbids replay | Existing credential/quota policy |

`retryable` is false after output, a tool call, an interaction response, or a
request-scoped failure. Other pre-output failures remain subject to the host's
existing retry policy. Do not globally disable credential cooling to handle a
request timeout. Do not replay an entire partially completed agent operation.

An unsupported host still receives the legacy stream error text but ignores the
additional stream-close JSON fields; it does **not** gain the corrected
cooldown/retry classification.
Deploy and verify a supporting host and plugin together for that behavior. No native ABI
layout changes or account-file migration are required.

## Client retry boundary

The guarantees above apply inside the plugin and CPA host, not to a new HTTP
request issued by the caller. OpenCode 1.18.32 with its bundled
`@ai-sdk/openai-compatible` 2.0.41 adapter does not honor the error body's
`retryable:false`: HTTP 504 is still retryable, and OpenCode's outer retry policy
also treats 5xx as retryable. An isolated probe of the installed SDK confirmed
that `x-should-retry:false` does not override this adapter's decision.
`Retry-After` and `retry-after-ms` control delay, not whether to retry.

Reliable end-to-end replay prevention therefore also requires the client to
recognize explicit terminal errors before applying status/message retry rules.
This plugin does not modify OpenCode. Its structured errors preserve truthful HTTP timeout
semantics instead of disguising an upstream timeout as a client input error.

## Diagnostics and acceptance limits

Progress timeouts retain aggregate event counts and add a bounded trace of the
latest 16 decoded event types, relative milliseconds, and content-progress flags.
The trace does not contain prompt text, tool arguments, tool results, credentials,
or event IDs. A progress flag describes a decoded content event, not proof that an
outbound acknowledgment was received by Cursor.

Timeout durations, HTTP keepalives, account policy, and OpenCode session contents
are unchanged. Native-tool handoff, error classification, one-account recovery,
and non-replay tests are distinct from proof that the original large private
session can complete. Synthetic fixtures and short live tool round trips must not
be reported as an exact replay of that incident.
