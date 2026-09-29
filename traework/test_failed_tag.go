package main

import (
	"encoding/json"
	"strings"
	"sync"
)

// parseTestFailedFromAuthJSON reads the top-level test_failed flag. The flag
// is the single source of truth — lives on the physical auth JSON, never on
// the host's auth record, because host.auth.save rebuilds the record and
// drops top-level fields the host doesn't recognize (same root cause as
// manual_disable).
func parseTestFailedFromAuthJSON(raw []byte) bool {
	var m struct {
		TestFailed bool `json:"test_failed"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.TestFailed
}

// testFailedSet mirrors the top-level `test_failed` flag on the physical auth
// file. Membership means "the scheduled active ping failed while credits
// remained" — such an account is kept out of routing until a later ping
// succeeds. scheduler.pick / pickNextAuth read this on every request, so a
// disk read per candidate is not an option; the set is rebuilt from disk by
// refreshTestFailedSetFromDisk (same contract as the other disk mirrors).
var (
	testFailedSetMu sync.RWMutex
	testFailedSet   = make(map[string]struct{})
)

// isTestFailed reports whether auth.ID currently carries the 测试 flag.
// Routing reads this on every request — keep it cheap.
func isTestFailed(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	testFailedSetMu.RLock()
	_, ok := testFailedSet[authID]
	testFailedSetMu.RUnlock()
	return ok
}

// testFailedSnapshot returns a copy of the current set (auth.ID → true).
func testFailedSnapshot() map[string]bool {
	testFailedSetMu.RLock()
	defer testFailedSetMu.RUnlock()
	out := make(map[string]bool, len(testFailedSet))
	for k := range testFailedSet {
		out[k] = true
	}
	return out
}

// testFailedSetPut marks an auth as test-failed. Callers MUST persist the
// change via persistTestFailedToggle before returning, otherwise the next
// refreshTestFailedSetFromDisk would revert the in-memory-only mark.
// Idempotent.
func testFailedSetPut(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	testFailedSetMu.Lock()
	testFailedSet[authID] = struct{}{}
	testFailedSetMu.Unlock()
}

// testFailedSetClear unmarks an auth. Idempotent.
func testFailedSetClear(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	testFailedSetMu.Lock()
	delete(testFailedSet, authID)
	testFailedSetMu.Unlock()
}

// refreshTestFailedSetFromDisk rebuilds the in-memory set from the host's
// current auth file list, so a restart (or a tag written by another session)
// is reflected in routing. Errors are intentionally swallowed: a transient
// host RPC failure must not blank the set before the next /accounts lands.
// Returns the new size.
func refreshTestFailedSetFromDisk() int {
	files, err := hostAuthList()
	if err != nil {
		return len(testFailedSnapshot())
	}
	next := make(map[string]struct{}, len(files))
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.ID] = struct{}{}
		phys, physErr := hostAuthGetPhysical(f.AuthIndex)
		if physErr != nil || phys == nil {
			continue
		}
		if parseTestFailedFromAuthJSON(phys.JSON) {
			next[f.ID] = struct{}{}
		}
	}
	testFailedSetMu.Lock()
	testFailedSet = next
	testFailedSetMu.Unlock()
	// Prune any in-memory entry whose auth is no longer live on disk.
	for id := range testFailedSnapshot() {
		if _, ok := live[id]; !ok {
			testFailedSetClear(id)
		}
	}
	return len(testFailedSnapshot())
}

// persistTestFailedToggle writes the top-level test_failed flag to the
// physical auth file. Direct write is mandatory (NOT host.auth.save) so the
// host's file watcher preserves the flag alongside disabled / note /
// manual_disable. on=true sets test_failed: true; on=false drops the key
// entirely so the file stays clean and the flag reads false on the next load.
//
// [参数] authIndex：宿主 RPC 面的 auth_index（直写物理文件需要）；
//
//	authID：auth.ID，用于同步内存镜像 testFailedSet。
//
// [返回] 索引/文件缺失或落盘失败时的错误；已是目标状态时返回 nil。
// 最近修改时间：2026-09-29；改动原因：新增内存镜像，供路由排除「测试」标签账号。
func persistTestFailedToggle(authIndex, authID string, on bool) error {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" {
		return errAuthIndexRequired()
	}
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	if parseTestFailedFromAuthJSON(phys.JSON) == on {
		// Already in the desired state — no write, no watcher churn. The
		// in-memory mirror still has to agree, otherwise routing would keep
		// using a 测试 account whose flag was written before this process
		// started.
		if on {
			testFailedSetPut(authID)
		} else {
			testFailedSetClear(authID)
		}
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil {
		// Treat malformed JSON as a fresh doc — losing the existing top-level
		// flags is better than refusing the write (consistent with
		// persistTestFailedToggle).
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
	if err := persistAuthDirect(phys.Name, phys.Path, "", raw); err != nil {
		return err
	}
	if on {
		testFailedSetPut(authID)
	} else {
		testFailedSetClear(authID)
	}
	return nil
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
