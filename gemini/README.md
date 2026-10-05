# Gemini Provider Plugin

This plugin adds Google Gemini Code Assist / Gemini CLI upstream provider support to CLIProxyAPI through the native plugin ABI.

## Features

- Provider display name: **Gemini Provider** (published as `gemini-provider`).
- Parses `gemini` and `gemini-cli` auth storage files.
- Expands one physical auth file into multiple virtual auths when `project_ids` contains more than one Google Cloud project.
- Supports Web OAuth through `/v0/management/oauth-callback`.
- Supports command-line login via `--geminicli-login`.
- Automatic EXPERIMENTAL release channel enablement for saved project IDs.
- Request/response translation across OpenAI, Responses, Claude, Gemini, and Codex formats.
- Token usage tracking: records real token consumption into the shared usage feed (`token-usage-feed.ndjson`) for `workbuddy-token-usage` visualization.

## Upstream Endpoints

Targets Cloud Code upstream endpoints:
- `POST https://cloudcode-pa.googleapis.com/v1internal:generateContent`
- `POST https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse`
- `POST https://cloudcode-pa.googleapis.com/v1internal:countTokens`

## License

MIT License.
