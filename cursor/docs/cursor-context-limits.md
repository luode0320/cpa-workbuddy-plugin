# Cursor context metadata and client policy

The plugin no longer advertises the same 200,000-token context and 32,768-token
output limit for every model. Those were metadata constants, not a local history
truncation policy.

`model.for_auth` still obtains usable IDs from
`agent.v1.AgentService/GetUsableModels` and applies the account's disabled-model
list. It also queries `aiserver.v1.AiService/AvailableModels` using
`useModelParameters`, `useReactModelPicker`, and `excludeMaxNamedModels`. The
parameterized response carries `contextTokenLimit`; the legacy flat response
observed on 2026-09-28 did not.

The plugin matches exact model names, server names, explicit aliases and
non-MAX variant legacy slugs. It uses the default context limit because the
current executor does not enable MAX mode. A slug can occur in both default and
MAX variants; the larger MAX value must not overwrite its default capacity.
Display names such as "1M" are not authoritative numeric metadata.

The plugin publishes three separate values in `model.for_auth`:

| Field | Meaning |
| --- | --- |
| `NativeContextLength` | Positive upstream `contextTokenLimit` for the non-MAX mode actually used; not a measured capacity. |
| `ClientContextLimit` | Plugin policy ceiling, always 1,000,000. This is not a claim about native capacity. |
| `ContextLength` | `min(NativeContextLength, ClientContextLimit)` when native capacity is known. |

Static discovery and models without a positive observed default limit omit
both native and effective lengths while still publishing the client policy.
`MaxCompletionTokens` is omitted because neither inspected
catalog publishes a verified output ceiling. These omissions mean unknown,
not zero capacity or unlimited capacity. Optional metadata lookup failures retain
the authenticated usable model IDs but omit native/effective lengths; the plugin
does not replace them with invented limits. Caller cancellation is still an error.
Both `AvailableModels` and `GetUsableModels` have their own 15-second deadlines
and a 4 MiB response bound. A new
account-model discovery fetches current metadata rather than persisting a
model-family table.

The authenticated `/v0/management/plugins/cursor-provider/status` response also exposes
`native_context_length`, `client_context_limit`, and `context_length` on each
model, plus `context_policy` and per-account `model_context_status`. A failed
metadata lookup in this status view is explicitly `unavailable`; the usable
model IDs and policy remain visible, but native/effective lengths are omitted.
The unchanged host may discard custom model fields from ordinary `/v1/models`;
use the management status response to read both values reliably. This change
does not modify the public host serializer or the management HTML display.

## Pre-upstream admission budget

All three executor entrypoints (`execute`, `execute_stream`, `count_tokens`)
share the same check, before upstream execution or checkpoint lookup. Both
`Payload` and the `OriginalRequest` fallback follow it. Rejections return
`context_length_exceeded`, HTTP 400, request-scoped and non-retryable. The native
ABI rejects executor envelopes larger than 4 MiB before copying into Go memory.

There is no verified tokenizer common to all supported upstream models.
`conservative_utf8_bytes_v1` therefore uses a conservative **admission budget**,
not exact model tokens. The policy takes the larger of input JSON bytes and
parsed UTF-8 content bytes, including system/history, tool schemas, arguments,
results and decoded attachments. It adds 32 units per message, 256 per tool or
text attachment in the parsed-content calculation, at least 65,536 per image,
and a 16,384-unit reserve. Input JSON alone may not exceed 983,616 bytes.
At most 8,192 messages and 256 tools are accepted. The stricter aggregate check
can reject individual attachments that otherwise fit their 16 MiB parser cap.
Before history and lineage construction, input JSON bytes plus tool names
inherited by unnamed tool results must also fit the remaining input budget.
This prevents one long assistant function name from being copied thousands of
times before the parsed-content check. Explicit result names remain independent,
and a repeated call ID uses the latest preceding assistant function name.

The larger of `max_tokens` and `max_completion_tokens`, when supplied, is an
additional reserve; negative, fractional or nonnumeric values are rejected.
These fields also activate the output budget described below. They are not
forwarded as an unsupported Cursor protocol generation parameter.
Unspecified upstream output, hidden system context, image tokenization and
server-managed checkpoint content remain outside this local accounting.
Thus the guard blocks oversized client probes but is not a proof that a whole
upstream generation consumes at most exactly 1M native tokens. It can reject
requests well below a model's real capacity, including heavily escaped JSON or
large inline images. It does not truncate or summarize requests automatically.

`count_tokens` uses this same admission estimate and returns `estimated: true`,
`count_method: "conservative_utf8_bytes_v1"`, and `client_context_limit: 1000000`.
This count differs from the existing usage estimate and is not billing usage.
No live long-context probing is needed to exercise the guard. Normal full-history
checks still apply before checkpoint suffix optimization, so a cached session
cannot bypass admission by sending only a small incremental upstream suffix.

## Output budget and cancellation

Positive `max_tokens` or `max_completion_tokens` limits now stop output locally.
If both are positive, the smaller value controls emitted output, while the larger
is reserved on input admission. Zero or omitted fields retain the previous
unspecified-output behavior. The unit is **conservative UTF-8 bytes**, not a
verified native tokenizer: text, observed thinking text, and complete tool-call
IDs/names/arguments share one budget. This can finish before the requested number
of native tokens, especially for long English words and multi-byte text.

Text truncation preserves valid UTF-8. Tool calls are atomic: an oversized tool
call is withheld, never emitted with truncated arguments. Reaching the limit
cancels the upstream HTTP request and returns `finish_reason: "length"` for both
JSON and SSE, with a final `[DONE]` for SSE. Even if only thinking consumed the
budget, an empty length-limited completion is valid. Truncated turns are not
committed as resumable checkpoints. Generated image payload bytes and hidden
upstream reasoning are not an authoritative output-token count; the budget does
not claim to control image billing or work the upstream already performed.

## Bounded session admission and blob retention

Session-backed runs serialize the complete checkpoint lookup/run/commit interval.
At most eight followers wait behind one active turn, and at most 64 active plus
waiting session-backed runs are admitted per plugin handler. Waiting honors caller
cancellation and has a 30-second deadline even when the native host supplied a
background context. Capacity or wait-time rejection returns request-scoped HTTP
409 `cursor_session_busy`, without automatic retry or account cooldown. Requests
without stable account/session identity retain full replay and are not claimed
to be covered by this session admission count. Model normalization is shared with
checkpoint keying, including whitespace after the provider prefix.

The per-run BlobStore now charges retained key and value bytes to both its 16 MiB
entry budget and 64 MiB total budget, while keeping the 4,096-entry limit.
Replacing a value counts its key only once; shrinking a value returns capacity.
Rejected writes preserve the old value and counters. Binary IDs remain opaque,
and empty values remain values rather than a new delete operation. These are
retained-payload limits, not process peak-memory bounds including temporary copies.

## Observed account metadata, 2026-09-28

| Model ID | Default context | MAX context, not enabled by this patch |
| --- | ---: | ---: |
| `grok-4.7-xhigh-fast` | 256,000 | 500,000 |
| `claude-fable-5-1-thinking-xhigh` | 300,000 | 1,000,000 |
| `gpt-5.6-sol-xhigh` | 272,000 | 1,000,000 |

These are authenticated live catalog observations, not hard-coded defaults or
exhaustive boundary/load tests. Account policies and upstream values can change.
Input sent to Cursor includes its own system/tool overhead; the declared context
window is not a promise that all of it is available for user text. Plugin usage
estimates are not an authoritative upstream tokenizer measurement.

The contract was cross-checked against the installed first-party Cursor 3.22.7
`AvailableModels` schema and request construction. Cursor's
[model documentation](https://cursor.com/docs/models-and-pricing) also distinguishes
default context from MAX mode. This patch neither enables MAX billing nor changes
CPA host code, account settings, client compaction settings, or replay semantics.

Regression coverage includes dynamic capacity refresh, default/MAX duplicate
slugs, explicit aliases, disabled IDs, unknown/invalid capacities, bounded
metadata responses, upstream errors, separate native/policy capacities, and
offline admission boundaries across all executor entrypoints. Deployment and client model-catalog
refresh are separate from editing and validating this source.
