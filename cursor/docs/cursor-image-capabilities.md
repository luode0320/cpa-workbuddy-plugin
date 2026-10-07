# Cursor image capabilities and plugin contract

**Protocol checked:** 2026-09-11 (Asia/Shanghai). **Image-write collision handling updated:** 2026-09-25 for v0.6.4.

**Scope:** Cursor product documentation, xAI documentation, the installed first-party Cursor 3.19.19 application and Agent CLI `2026.07.16-899851b`, and the v0.5.10 plugin changes based on commit `e0d3f6b806c8a3136382f3920ccfeef166ed04b1`. The initial protocol investigation was read-only; implementation and deployment acceptance followed separately.

## Verdict

| Question | Result |
| --- | --- |
| Does Cursor support image input? | Yes. Agent accepts pasted, uploaded, and supported workspace images as vision context. |
| Can Cursor Agent generate images? | Yes. It exposes image generation as a built-in Agent tool. |
| Can Cursor's Grok 4.6 selection use that tool? | Yes. Cursor lists image generation among the Agent tools available to `grok-4.6`. |
| Does Grok 4.6 itself produce images? | No. xAI documents Grok 4.6 as text/image input and text output; Grok Imagine is a separate image model/API. |
| Does this plugin now bridge Cursor-generated images? | Yes, for the Cursor Agent chat flow. It handles the image approval interaction and completed image tool result. |
| Does this add `/v1/images/generations`? | No. The executor still declares `chat-completions`; generated images are returned as a chat-response extension. |

The important distinction is that `cursor/grok-4.6` is the Agent's reasoning model. Cursor's image generator is a built-in tool selected by that Agent, not native image output from the Grok 4.6 chat model.

## Implemented flow

1. The plugin sends the original chat request to `/agent.v1.AgentService/Run`.
   It includes the workspace/project context and answers subsequent `request_context_args` requests. Binary image writes are limited to direct children of the project assets directory, at most 16 MiB per image, and never overwrite existing files. Within a run, repeated writes with the same requested path and bytes reuse the successful result. A filename collision with another write or an existing file allocates an exclusive temporary filename in the same assets directory, preserving the extension and returning the actual absolute path upstream. Different image bytes never reuse a previous result. Files created for a run are removed when that run finishes; existing files are not removed. The project folder remains stable for checkpoint continuity.
2. When Cursor sends `InteractionQuery.generate_image_request_query`, the plugin mirrors the query ID and description in an approved `InteractionResponse.generate_image_request_response`.
3. This matches the installed Cursor Agent CLI's non-interactive behavior. The interactive CLI may ask a human first; a headless API has no interactive approval surface.
4. When Cursor sends a completed `generate_image_tool_call`, the plugin decodes `result.success.image_data`, validates that the bytes are an image, and derives the MIME type from the content or returned file extension.
5. Image-generation failures, missing bytes, invalid base64, and non-image payloads become explicit executor errors instead of empty or generic streaming completions.
6. The plugin maps valid image bytes to the same Chat Completions extension already used by CLIProxyAPI translators:

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "images": [{
        "index": 0,
        "type": "image_url",
        "image_url": {"url": "data:image/png;base64,..."}
      }]
    }
  }]
}
```

Streaming emits the same item under `choices[0].delta.images`, followed by a normal final chunk and `[DONE]`.

Image-only completions are valid. A generated-image turn is deliberately not committed as a text-only conversation checkpoint, because doing so would claim a lineage that omits the binary output. This preserves correctness at the cost of using the safer replay path on a later turn.

## Protocol coverage

The embedded Agent descriptor now includes the first-party fields needed by the current flow:

| Message | Field |
| --- | --- |
| `GenerateImageArgs` | `aspect_ratio = 6` |
| `InteractionQuery` | `generate_image_request_query = 12` |
| `InteractionResponse` | `generate_image_request_response = 12` |
| `GenerateImageRequestQuery` | `args = 1`, `tool_call_id = 2` |
| `GenerateImageRequestResponse` | `approved = 1`, `rejected = 2` |
| `GenerateImageRequestResponse.Approved` | `description = 1` |
| `GenerateImageRequestResponse.Rejected` | `reason = 1` |

The pre-existing descriptor already contained `ToolCall.generate_image_tool_call = 28`, `GenerateImageToolCall`, `GenerateImageResult`, `GenerateImageSuccess.file_path`, and `GenerateImageSuccess.image_data`.

The default `x-cursor-client-version` header is aligned with the inspected Agent CLI build. Structural tests pin these field numbers so a later descriptor change fails visibly rather than silently dropping image requests.

## Evidence

| Claim | Primary evidence |
| --- | --- |
| Cursor accepts image input. | Cursor [Prompting agents](https://cursor.com/docs/agent/prompting#image-input), [Agent tools](https://cursor.com/docs/agent/overview#tools), and [1.7 image-file support notes](https://cursor.com/changelog/1-7#image-file-support-for-agent). |
| Cursor Agent has image generation. | Cursor [Agent tools](https://cursor.com/docs/agent/overview#tools) and [2.4 image-generation notes](https://cursor.com/changelog/2-4#image-generation). |
| Grok 4.6 can call Cursor's image tool. | Cursor's [Grok 4.6 model page](https://cursor.com/docs/models/grok-4-6). |
| Grok 4.6 itself is text output. | xAI's [Grok 4.6 release note](https://docs.x.ai/developers/release-notes#grok-46) and [model page](https://docs.x.ai/developers/models/grok-4.6). |
| xAI image generation is separate. | xAI's [image-generation API](https://docs.x.ai/developers/model-capabilities/images/generation) and [image-generation tool](https://docs.x.ai/developers/tools/image-generation). |
| Cursor's current protocol and headless behavior. | The installed first-party Cursor bundle and Agent CLI schemas/handlers were inspected locally; the Agent CLI auto-approves the image query in headless mode. |
| CLIProxyAPI output convention. | CLIProxyAPI v7.2.131 commit `323b7276bc5bd251e5497699e42c556d6316b30c` emits generated chat images under `message.images` and `delta.images`. |

## Verification boundary

Automated tests exercise the entire local bridge with a protocol-faithful synthetic Cursor stream: image query, client approval frame, completed PNG tool result, turn end, non-stream response, and streaming response. The build therefore verifies framing, protobuf fields, event state, MIME/base64 handling, and OpenAI-compatible serialization without consuming paid quota.

A live Cursor account/model call is also required for deployment acceptance; the local tests do not establish remote availability, entitlement, moderation, or quota. Those remain account- and request-dependent. If Cursor returns only a remote `file_path` without image bytes, the plugin fails clearly instead of claiming a successful image response.

There is also a separate client boundary. `message.images` / `delta.images` is a CLIProxyAPI extension, not a standard Chat Completions output field. At checked OpenBitFun commit [`39c60853ad19593fad089cbc06456646d7b193ff`](https://github.com/GCWing/OpenBitFun/blob/39c60853ad19593fad089cbc06456646d7b193ff/src/crates/adapters/ai-adapters/src/stream/types/openai.rs#L76-L88), its OpenAI stream adapter deserializes reasoning, text, and tool calls but not `images`; an image-only delta is therefore discarded by that client. This patch completes the Cursor-plugin bridge, but it does not make the current BitFun UI render the result. That requires a separate BitFun adapter/UI change and end-to-end acceptance.

## Boundaries

- Image inputs must remain inline data URLs; the plugin does not fetch remote URLs.
- Generated images are returned inline and are bounded by the existing 32 MiB Connect-frame decoder.
- This implementation does not call xAI directly and does not require separate xAI credentials.
- This implementation does not invoke Cursor desktop's local `AiService/RunGenerateImage` path.
- This implementation does not claim a stable public Cursor API; it follows the checked first-party private protocol and is guarded by structural and end-to-end tests.
