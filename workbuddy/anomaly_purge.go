// anomaly_purge.go removes the legacy top-level `anomaly` flag from physical
// auth files. The anomaly pool mechanism itself was removed (2026-09-08):
// account failures now only apply the fixed 15s failover cooldown
// (accountFailover.go) — no quarantine set, no daily refresh loop, no
// freeze/unfreeze. Files written by older plugin versions may still carry
// `anomaly: true`; nothing reads that key anymore, so it is dead weight.
// purgeLegacyAnomalyFlags strips it once at watchdog startup so the files
// stay clean.
//
// This file also hosts the shared auth-file error helpers (authFileErr /
// errAuthIndexRequired / errAuthMissing) salvaged from the removed preserve.go
// (2026-09-29 移除保号池时随迁)；仍被 counter.go / test_failed_tag.go 引用。
package main

import (
	"encoding/json"
	"log"
	"strings"
)

// authFileErr / errAuthIndexRequired / errAuthMissing 沿用原 preserve.go 的
// 轻量错误包装，供直写物理 auth 文件的 persist*Toggle 复用：避免为稳定错误
// 值额外分配 fmt.Errorf 字符串。
type authFileErr struct{ msg string }

func (e *authFileErr) Error() string { return e.msg }

func errAuthIndexRequired() error { return &authFileErr{msg: "auth_index is required"} }
func errAuthMissing() error       { return &authFileErr{msg: "auth file missing or empty"} }

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
// block the sweep. Called once from watchdogLoop startup (after the
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
