package main

import (
	"encoding/json"
)

// parseTestFailedFromAuthJSON reads the top-level test_failed flag. The flag
// is the single source of truth — lives on the physical auth JSON, never on
// the host's auth record, because host.auth.save rebuilds the record and
// drops top-level fields the host doesn't recognize (same root cause as
// preserve / manual_disable).
func parseTestFailedFromAuthJSON(raw []byte) bool {
	var m struct {
		TestFailed bool `json:"test_failed"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.TestFailed
}

// persistTestFailedToggle writes the top-level test_failed flag to the
// physical auth file. Direct write is mandatory (NOT host.auth.save) so the
// host's file watcher preserves the flag alongside disabled / note /
// manual_disable. on=true sets test_failed: true; on=false drops the key
// entirely so the file stays clean and the flag reads false on the next load.
func persistTestFailedToggle(authIndex string, on bool) error {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	if parseTestFailedFromAuthJSON(phys.JSON) == on {
		return nil // already in the desired state — no write, no watcher churn
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		// Treat malformed JSON as a fresh doc — losing the existing top-level
		// flags is better than refusing the write (consistent with
		// persistPreserveToggle).
		doc = map[string]any{}
	}
	if on {
		doc["test_failed"] = true
	} else {
		delete(doc, "test_failed")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return persistAuthDirect(phys.Name, phys.Path, "", raw)
}

// hostAuthIndexForPhys walks hostAuthList to map an auth ID back to the host
// RPC index (auth.ID is stable for cache keys, but direct writes need the
// RPC-facing auth_index).
func hostAuthIndexForPhys(authID string) (string, error) {
	files, err := hostAuthList()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.ID == authID {
			return f.AuthIndex, nil
		}
	}
	return "", errAuthMissing()
}
