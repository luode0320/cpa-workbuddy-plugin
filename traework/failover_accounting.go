// failover_accounting.go bridges the failover state (accountFailover.go) and
// the anomaly pool (anomaly.go) with host auth records. noteAccountFailure is
// the single entry point the executor calls on upstream failures; it records
// the cooldown under the ID the caller passes and mirrors the failure to the
// canonical auth ID when the caller used an auth_index.
package main

import "strings"

// noteAccountFailure records an upstream failure for failover + anomaly
// accounting. Returns true when the failure was counted. The caller may pass
// either an auth ID (scheduler key) or an auth_index; the canonical ID is
// resolved and mirrored so the panel's failover snapshot stays consistent.
func noteAccountFailure(authID string, status int, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordAccountFailure(authID, status, body) {
		return false
	}
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
		recordAccountFailure(id, status, body)
	}()
	return true
}

// noteForcedAccountFailure records a failure that bypasses isAccountFailure
// classification (see recordForcedFailure). Used by pseudo-completion
// detection, which must drive failover off an account even though the
// upstream response is a protocol-valid HTTP 200 + done.
func noteForcedAccountFailure(authID string, body string) bool {
	if !failoverActive() || strings.TrimSpace(authID) == "" {
		return false
	}
	if !recordForcedFailure(authID, body) {
		return false
	}
	go func() {
		_, id := resolveAuthIndexAndID(authID)
		if id == "" || id == authID {
			return
		}
		recordForcedFailure(id, body)
	}()
	return true
}

// resolveAuthIndexAndID maps either an auth_index or an auth ID to the
// (authIndex, authID) pair the host understands. Returns empty strings when
// unresolvable. Used by anomaly freeze and failover mirroring.
func resolveAuthIndexAndID(authID string) (string, string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "", ""
	}
	// Fast path: already an auth_index the host understands.
	if _, err := hostAuthGet(authID); err == nil {
		if files, err := hostAuthList(); err == nil {
			for _, f := range files {
				if f.AuthIndex == authID {
					return authID, f.ID
				}
			}
		}
		return authID, ""
	}
	files, err := hostAuthList()
	if err != nil {
		return "", ""
	}
	wantName := "traework-" + authID + ".json"
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			return f.AuthIndex, f.ID
		}
		if f.Name == wantName {
			return f.AuthIndex, f.ID
		}
	}
	return "", ""
}
