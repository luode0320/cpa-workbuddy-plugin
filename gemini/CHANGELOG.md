# Changelog

All notable changes to the "Gemini Provider" plugin (`gemini-provider`) will be documented in this file.

## [0.1.0] - 2026-10-05

### Added
- Initial release of "Gemini Provider" for CLIProxyAPI (CPA), adapted from `cpa-plugin-gemini-cli`.
- Standard C ABI export (`cliproxy_plugin_init`, `cliproxyPluginCall`, `cliproxyPluginFree`, `cliproxyPluginShutdown`) with panic recover protection.
- Streaming and non-streaming token usage extraction (`usageMetadata`) piped into shared `<root>/data/token-usage-feed.ndjson`.
- OAuth client credential obfuscation to ensure security compliance and push protection.
- Support for Google Gemini CLI models and thinking reasoning budgets.
