// anomaly_purge.go removes the legacy top-level `anomaly` flag from physical
// auth files. The anomaly pool mechanism itself was removed (2026-09-08):
// account failures now only apply the fixed 15s failover cooldown
// (accountFailover.go) — no quarantine set, no daily refresh loop, no
// freeze/unfreeze. Files written by older plugin versions may still carry
// `anomaly: true`; nothing reads that key anymore, so it is dead weight.
// purgeLegacyAnomalyFlags strips it once at watchdog startup so the files
// stay clean.
//
// It also purges LEGACY AUTO-DISABLED flags (purgeLegacyDisabledFlags):
// plugin versions ≤0.1.54 wrote disabled:true + note "Session expired..."
// from markSessionDead and a keepalive schedule (hours {22}) whose hot-
// reloaded goroutines outlive the plugin's retirement — on 2026-09-08 22:00
// four leaked pre-policy instances re-disabled three production accounts
// AFTER the 0.1.55/0.1.56 manual-toggle-only fix had shipped. Under the
// manual-toggle-only policy the only legitimate writer of disabled:true is
// persistDisabledToggle (panel), which never pairs it with a "Session
// expired" note — so a doc carrying BOTH is provably legacy and safe to
// re-enable. Docs with disabled:true and any other (or no) note are left
// untouched: the purge never guesses. Since 0.1.58 the exhausted lifecycle
// also writes disabled:true, but always paired with the exhausted_disable
// marker (and manual toggles pair it with manual_disable) — stripLegacy-
// DisabledFlag skips marker-carrying docs so the purge can never fight the
// current mechanisms.
//
// This file also hosts the shared auth-file error helpers (authFileErr /
// errAuthIndexRequired / errAuthMissing) that used to live in anomaly.go and
// are referenced by counter.go / keepalive.go / management.go / preserve.go.
package main

import (
	"encoding/json"
	"log"
	"strings"
)

// authFileErr / errAuthIndexRequired / errAuthMissing mirror workbuddy's
// helpers so persist*Toggle-style writers return stable error values without
// allocating fmt.Errorf strings.
type authFileErr struct{ msg string }

func (e *authFileErr) Error() string { return e.msg }

func errAuthIndexRequired() error { return &authFileErr{msg: "auth_index is required"} }
func errAuthMissing() error       { return &authFileErr{msg: "auth file missing or empty"} }

// legacySessionDeadNotePrefix is the note fingerprint written by every
// pre-manual-toggle-only markSessionDead (0.1.50 → 0.1.54). The current
// markSessionDead writes the SAME note text but never together with
// disabled:true, so disabled+this-note is unambiguous legacy evidence.
const legacySessionDeadNotePrefix = "Session expired"

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

// stripLegacyDisabledFlag re-enables a doc that carries the legacy
// session-dead auto-disable pair (disabled:true + "Session expired" note).
// The note itself is KEPT: it still truthfully describes the refresh token
// state. Returns the (possibly unchanged) raw bytes and whether anything
// changed. Unparsable input is returned unchanged — never blind-writes.
// Docs carrying manual_disable or exhausted_disable are never touched: those
// markers are owned by the manual toggle / the current exhausted lifecycle
// (2026-09-08 policy update), so a disabled doc with either marker is
// legitimate state, not legacy evidence. Docs that are not disabled, or
// disabled with a different/absent note, are also never touched (a manual
// toggle may be legitimate; the purge never guesses).
func stripLegacyDisabledFlag(raw []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw, false
	}
	if disabled, _ := doc["disabled"].(bool); !disabled {
		return raw, false
	}
	if _, manual, exhausted := authDocFlags(raw); manual || exhausted {
		return raw, false
	}
	note, _ := doc["note"].(string)
	if !strings.HasPrefix(strings.TrimSpace(note), legacySessionDeadNotePrefix) {
		return raw, false
	}
	doc["disabled"] = false
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return out, true
}

// purgeLegacyDisabledFlags walks every traework auth file and re-enables
// accounts carrying the legacy session-dead disable pair. Idempotent.
// Called once from preserveWatchdogLoop startup (after the host is
// reachable), next to purgeLegacyAnomalyFlags. This is the self-healing path
// for deployments upgraded from ≤0.1.54 whose auth files still carry flags
// written by the old auto-disabler (or re-written afterwards by its leaked
// hot-reloaded goroutines).
func purgeLegacyDisabledFlags() {
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
		cleaned, changed := stripLegacyDisabledFlag(phys.JSON)
		if !changed {
			continue
		}
		name := strings.TrimSpace(phys.Name)
		if name == "" {
			continue
		}
		if err := persistAuthDirect(name, phys.Path, "", cleaned); err != nil {
			log.Printf("[legacy-purge] re-enable %s failed: %v", authIndex, err)
			continue
		}
		log.Printf("[legacy-purge] re-enabled %s: legacy session-dead disable pair cleared (manual-toggle-only policy)", authIndex)
		purged++
	}
	if purged > 0 {
		log.Printf("[legacy-purge] re-enabled %d auth file(s) carrying legacy session-dead disables", purged)
	}
}

// purgeLegacyAnomalyFlags walks every traework auth file and strips the dead
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
