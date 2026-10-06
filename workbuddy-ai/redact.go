// redact.go strips credentials and token-shaped material from any string that
// might end up in logs, error responses, or CPAMP usage bodies. All plugin
// code that surfaces upstream error text must route through redactSecrets
// (or truncateRedacted when a length cap is also needed).
package main

import "regexp"

var (
	redactREBearer  = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]{12,}`)
	redactREJWT     = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`)
	redactRETokenKV = regexp.MustCompile(`(?i)((?:access_token|refresh_token|id_token)\s*[=:]\s*)([A-Za-z0-9._\-+/=]{12,})`)
	// redactREJWTLoose catches JWTs that appear bare in a JSON value or path —
	// no Bearer prefix, no access_token key. Two-segment and three-segment both
	// match (some upstreams return header.payload only when signature is empty).
	redactREJWTLoose = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}(?:\.[A-Za-z0-9_\-]{4,}){1,2}\b`)
)

// redactSecrets strips bearer tokens / JWT-like blobs from error bodies before usage publish.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	// Bearer tokens
	s = redactREBearer.ReplaceAllString(s, "Bearer ***")
	// long JWT-ish segments
	s = redactREJWT.ReplaceAllString(s, "***jwt***")
	// access_token / refresh_token query-or-json fragments (best-effort)
	s = redactRETokenKV.ReplaceAllString(s, "${1}***")
	// loose JWT fallback: bare header.payload(.sig) without Bearer / kv context
	s = redactREJWTLoose.ReplaceAllString(s, "***jwt***")
	return s
}

// truncateRedacted redacts secrets then truncates — use for any error body
// returned to clients / logs (A-37). publishUsage already redacts Fail.Body.
func truncateRedacted(s string, n int) string {
	return truncate(redactSecrets(s), n)
}

// truncate cuts s to at most n bytes. Caller is responsible for rune-boundary
// safety when the string may contain multi-byte UTF-8 (most callers pass
// upstream JSON which is ASCII-safe at token boundaries).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
