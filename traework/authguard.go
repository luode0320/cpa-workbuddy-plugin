// authguard.go re-applies the plugin-owned disabled flag when the host's
// auto-refresh rebuilds an auth file and silently drops it. Observed
// 2026-09-05/06 on the production server: core's 15-minute auto-refresh
// rewrote auth files and wiped the top-level disabled/note written by
// markSessionDead / persistDisabledToggle, leaving the panel and the disk
// out of sync and allowing dead accounts back into routing.
//
// Mechanics: registry entries are registered on manual disable and session
// death, unregistered on enable. A 5-minute loop folds the flag back into
// the CURRENT physical file content (read-latest → mutate → write), so any
// token fields the host rotated are preserved — only disabled/note are
// re-applied. Gate via config_yaml `auth_flag_guard: false`.
package main

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// guardEntry is one protected disabled flag. authID (file id) is kept for
// logging; the registry key is the host auth index used by hostAuthGetPhysical.
type guardEntry struct {
	authID string
	note   string
	source string // "manual" | "session-dead"
}

var (
	guardMu       sync.Mutex
	guardRegistry = map[string]guardEntry{}
	guardAuto     = true
	guardAutoMu   sync.RWMutex
)

func guardEnabled() bool {
	guardAutoMu.RLock()
	defer guardAutoMu.RUnlock()
	return guardAuto
}

func setGuardEnabled(on bool) {
	guardAutoMu.Lock()
	guardAuto = on
	guardAutoMu.Unlock()
}

func guardRegister(authIndex, authID, note, source string) {
	key := strings.TrimSpace(authIndex)
	if key == "" {
		return
	}
	guardMu.Lock()
	guardRegistry[key] = guardEntry{authID: authID, note: note, source: source}
	guardMu.Unlock()
}

func guardUnregister(authIndex string) {
	key := strings.TrimSpace(authIndex)
	if key == "" {
		return
	}
	guardMu.Lock()
	delete(guardRegistry, key)
	guardMu.Unlock()
}

// reapplyDisabledFlag folds the guarded disabled flag (+note) into the
// current physical file JSON, preserving every other key the host may have
// rewritten. Returns the folded doc and whether anything changed. Unparsable
// input is returned unchanged — the guard never blind-writes.
func reapplyDisabledFlag(physJSON []byte, entry guardEntry) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(physJSON, &doc); err != nil || doc == nil {
		return physJSON, false
	}
	if cur, _ := doc["disabled"].(bool); cur {
		return physJSON, false // already disabled; nothing to do
	}
	doc["disabled"] = true
	if entry.note != "" {
		doc["note"] = entry.note
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return physJSON, false
	}
	return raw, true
}

func authFlagGuardTick() {
	guardMu.Lock()
	snapshot := make(map[string]guardEntry, len(guardRegistry))
	for k, v := range guardRegistry {
		snapshot[k] = v
	}
	guardMu.Unlock()
	if len(snapshot) == 0 {
		return
	}
	for authIndex, entry := range snapshot {
		phys, err := hostAuthGetPhysical(authIndex)
		if err != nil || phys == nil || len(phys.JSON) == 0 {
			continue // missing/unreadable this tick; retry next tick
		}
		folded, changed := reapplyDisabledFlag(phys.JSON, entry)
		if !changed {
			continue
		}
		name := strings.TrimSpace(phys.Name)
		if name == "" {
			continue
		}
		if err := persistAuthDirect(name, phys.Path, "", folded); err != nil {
			log.Printf("[auth-guard] re-apply failed for %s: %v", authIndex, err)
			continue
		}
		log.Printf("[auth-guard] re-applied disabled flag to %s (source=%s)", authIndex, entry.source)
	}
}

// authFlagGuardLoop runs the 5-minute reconcile tick. Registry is in-memory:
// a plugin restart clears it and protection resumes from the next manual
// disable / session-death event.
func authFlagGuardLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if !guardEnabled() {
			continue
		}
		authFlagGuardTick()
	}
}

func init() {
	go authFlagGuardLoop()
}
