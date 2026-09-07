// anomaly_purge.go removes the legacy top-level `anomaly` flag from physical
// auth files. The anomaly pool mechanism itself was removed (2026-09-08):
// account failures now only apply the fixed 15s failover cooldown
// (accountFailover.go) — no quarantine set, no daily refresh loop, no
// freeze/unfreeze. Files written by older plugin versions may still carry
// `anomaly: true`; nothing reads that key anymore, so it is dead weight.
// purgeLegacyAnomalyFlags strips it once at watchdog startup so the files
// stay clean.
package main

import (
	"encoding/json"
	"log"
	"strings"
)

// stripAnomalyKey deletes the top-level "anomaly" key from an auth JSON doc.
// Returns the (possibly unchanged) raw bytes and whether anything changed.
// Unparsable input is returned unchanged — the purge never blind-writes.
// Extracted as a pure function so the fold is unit-testable without host RPC.
func stripAnomalyKey(raw []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw, false
	}
	if _, ok := doc["anomaly"]; !ok {
		return raw, false
	}
	delete(doc, "anomaly")
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return out, true
}

// purgeLegacyAnomalyFlags walks every workbuddy auth file and strips the dead
// top-level `anomaly` key. Idempotent: files without the key are never
// rewritten. Per-file failures are logged and skipped so one bad file cannot
// block the sweep. Called once from preserveWatchdogLoop startup (after the
// host is reachable, before the first tick).
func purgeLegacyAnomalyFlags() {
	files, err := hostAuthList()
	if err != nil {
		return // host not reachable yet; nothing to purge this boot
	}
	purged := 0
	for _, f := range files {
		authIndex := strings.TrimSpace(f.AuthIndex)
		if authIndex == "" {
			continue
		}
		phys, err := hostAuthGetPhysical(authIndex)
		if err != nil || phys == nil || len(phys.JSON) == 0 {
			continue
		}
		stripped, changed := stripAnomalyKey(phys.JSON)
		if !changed {
			continue
		}
		name := strings.TrimSpace(phys.Name)
		if name == "" {
			continue
		}
		if err := persistAuthDirect(name, phys.Path, "", stripped); err != nil {
			log.Printf("[anomaly-purge] strip %s failed: %v", authIndex, err)
			continue
		}
		purged++
	}
	if purged > 0 {
		log.Printf("[anomaly-purge] stripped legacy anomaly flag from %d auth file(s)", purged)
	}
}
