package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// parseCreatedAtFromAccessToken extracts the JWT payload's auth_time claim,
// which is the account's real authorization timestamp.
//
// The host's HostAuthFileEntry.CreatedAt cannot be used for display: the file
// watcher synthesizes it with time.Now() on every rescan, so it drifts to the
// latest write (credit refresh, keepalive, test ping) instead of the account's
// creation time. auth_time is stamped once at login and survives token
// refreshes, which makes it the only reliable creation source we hold locally.
//
// The signature is deliberately not verified: the token is already trusted
// local state, and the host RPC has no key material to verify against.
func parseCreatedAtFromAccessToken(accessToken string) (time.Time, bool) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		AuthTime int64 `json:"auth_time"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	if claims.AuthTime <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.AuthTime, 0).UTC(), true
}
