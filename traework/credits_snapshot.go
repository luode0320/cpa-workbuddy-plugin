// credits_snapshot.go 把每个账号的积分快照落到插件自有的 sidecar 文件，
// 让面板冷加载与路由判据在进程重启后仍然拿得到积分。
//
// 为什么需要：积分原本只存在 cache.go 的 sync.Map 里。插件热重载/宿主重启后
// 内存清空，面板首屏每个账号都要现查一次上游（N 个账号 = N 次上游请求），
// 而且调度排序在查回来之前全是「未知」。积分不是安全敏感数据，落盘代价极低。
//
// 为什么不复用 failover_state.go：两者写入频率差两个数量级。故障态是低频
// 事件驱动；积分是每轮刷新整份重写。合并成一个文件会让高频的原子替换把
// 低频的禁用状态一起冲掉——这是 sidecar 模式的通用陷阱。
//
// 落盘位置：`<CPA root>/data/traework-credits.json`，与 usage feed 同级。
// 读取一律容错：文件缺失、JSON 损坏、版本不认识都按空状态处理并继续。
//
// 最近修改时间：2026-10-25；改动原因：新增，让积分跨重启存活。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// creditSnapshotName 是积分 sidecar 文件名，落在 <CPA root>/data/。
	creditSnapshotName = "traework-credits.json"

	// creditSnapshotVersion 是结构版本。不认识时按空状态处理。
	creditSnapshotVersion = 1
)

// creditSnapshotEntry 是单个账号的积分快照。
type creditSnapshotEntry struct {
	// Remain 是剩余积分，冗余自 Credits 便于快速读。
	Remain int64 `json:"remain"`
	// FetchedAt 是这次快照的拉取时刻（RFC3339）。
	FetchedAt string `json:"fetched_at,omitempty"`
	// Credits 是完整明细，供面板进度条（已用/总量/额度包数）。
	Credits *traeCredits `json:"credits,omitempty"`
}

// creditSnapshotFile 是 sidecar 的顶层结构。
type creditSnapshotFile struct {
	Version   int                            `json:"version"`
	UpdatedAt string                         `json:"updated_at"`
	Accounts  map[string]creditSnapshotEntry `json:"accounts"`
}

var (
	// creditSnapshotMu 保护内存镜像与落盘互斥。
	creditSnapshotMu sync.Mutex
	// creditSnapshotAccounts 是 sidecar 的内存镜像，键为归一化账号标识。
	creditSnapshotAccounts = make(map[string]creditSnapshotEntry)
	// creditSnapshotLoaded 标记内存镜像是否已从磁盘载入过。
	creditSnapshotLoaded bool
	// creditSnapshotPathOverride 覆盖路径，仅供测试隔离使用。
	creditSnapshotPathOverride string
)

// creditSnapshotPath 返回积分 sidecar 的落盘路径。
// [参数] 无。
// [返回] 绝对路径；根目录推导失败时返回相对路径，由写盘错误兜底。
// 最近修改时间：2026-10-25；改动原因：新增。
func creditSnapshotPath() string {
	if override := strings.TrimSpace(creditSnapshotPathOverride); override != "" {
		return override
	}
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", creditSnapshotName)
	}
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", creditSnapshotName)
	}
	return filepath.Join("data", creditSnapshotName)
}

// ensureCreditSnapshotLoaded 在首次访问时把磁盘快照读进内存。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func ensureCreditSnapshotLoaded() {
	creditSnapshotMu.Lock()
	defer creditSnapshotMu.Unlock()
	ensureCreditSnapshotLoadedLocked()
}

// ensureCreditSnapshotLoadedLocked 是 ensureCreditSnapshotLoaded 的持锁版本。
// 调用方必须持有 creditSnapshotMu。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func ensureCreditSnapshotLoadedLocked() {
	if creditSnapshotLoaded {
		return
	}
	creditSnapshotLoaded = true
	raw, err := os.ReadFile(creditSnapshotPath())
	if err != nil {
		return
	}
	var doc creditSnapshotFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	if doc.Version != creditSnapshotVersion {
		return
	}
	for id, entry := range doc.Accounts {
		key := normalizeFailoverKey(id)
		if key == "" {
			key = strings.TrimSpace(strings.ToLower(id))
		}
		if key == "" {
			continue
		}
		creditSnapshotAccounts[key] = entry
	}
}

// creditSnapshotGet 读取账号的积分快照。
// [参数] authID：账号标识（任一形态，内部归一化）。
// [返回] 快照条目与是否存在。
// 最近修改时间：2026-10-25；改动原因：新增。
func creditSnapshotGet(authID string) (creditSnapshotEntry, bool) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		key = strings.TrimSpace(strings.ToLower(authID))
	}
	if key == "" {
		return creditSnapshotEntry{}, false
	}
	creditSnapshotMu.Lock()
	defer creditSnapshotMu.Unlock()
	ensureCreditSnapshotLoadedLocked()
	entry, ok := creditSnapshotAccounts[key]
	return entry, ok
}

// recordCreditSnapshot 记录一次积分拉取结果到内存镜像。
// 只写内存，落盘由 persistCreditSnapshot 在轮末统一执行。
//
// cr 为 nil 时直接返回：把「未知」写成 0 会让账号被判定为耗尽/低于禁用线，
// 这是比「没有数据」严重得多的错误方向。
//
// [参数] authID：账号标识；cr：积分明细。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func recordCreditSnapshot(authID string, cr *traeCredits) {
	if cr == nil {
		return
	}
	key := normalizeFailoverKey(authID)
	if key == "" {
		key = strings.TrimSpace(strings.ToLower(authID))
	}
	if key == "" {
		return
	}
	entry := creditSnapshotEntry{
		Remain:  cr.TotalRemain,
		Credits: cr,
	}
	entry.FetchedAt = strings.TrimSpace(cr.FetchedAt)
	if entry.FetchedAt == "" {
		entry.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}
	creditSnapshotMu.Lock()
	ensureCreditSnapshotLoadedLocked()
	creditSnapshotAccounts[key] = entry
	creditSnapshotMu.Unlock()
}

// forgetCreditSnapshot 删除账号的积分快照（账号被删除时调用）。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func forgetCreditSnapshot(authID string) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	creditSnapshotMu.Lock()
	ensureCreditSnapshotLoadedLocked()
	if _, ok := creditSnapshotAccounts[key]; ok {
		delete(creditSnapshotAccounts, key)
		creditSnapshotMu.Unlock()
		persistCreditSnapshot()
		return
	}
	creditSnapshotMu.Unlock()
}

// persistCreditSnapshot 把内存镜像整体写盘（临时文件 + 原子重命名）。
// [参数] 无。
// [返回] 写盘错误，供调用方记日志；不影响内存状态。
// 最近修改时间：2026-10-25；改动原因：新增。
func persistCreditSnapshot() error {
	creditSnapshotMu.Lock()
	doc := creditSnapshotFile{
		Version:   creditSnapshotVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Accounts:  make(map[string]creditSnapshotEntry, len(creditSnapshotAccounts)),
	}
	for key, entry := range creditSnapshotAccounts {
		if entry.Credits == nil {
			continue
		}
		doc.Accounts[key] = entry
	}
	creditSnapshotMu.Unlock()

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := creditSnapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".traework-credits-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	// 原子替换：读者要么看到旧快照要么看到新快照，不会读到写一半的 JSON。
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// resetCreditSnapshotForTest 清空内存镜像与载入标记，仅供测试使用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，避免并发用例互相污染。
func resetCreditSnapshotForTest() {
	creditSnapshotMu.Lock()
	creditSnapshotAccounts = make(map[string]creditSnapshotEntry)
	creditSnapshotLoaded = false
	creditSnapshotMu.Unlock()
}

// isolateCreditSnapshotPathForTest 把 sidecar 指向调用方提供的临时文件，返回还原函数。
// [参数] path：临时文件路径。
// [返回] 还原函数。
// 最近修改时间：2026-10-25；改动原因：新增。
func isolateCreditSnapshotPathForTest(path string) func() {
	creditSnapshotMu.Lock()
	old := creditSnapshotPathOverride
	creditSnapshotPathOverride = path
	creditSnapshotAccounts = make(map[string]creditSnapshotEntry)
	creditSnapshotLoaded = false
	creditSnapshotMu.Unlock()
	return func() {
		creditSnapshotMu.Lock()
		creditSnapshotPathOverride = old
		creditSnapshotAccounts = make(map[string]creditSnapshotEntry)
		creditSnapshotLoaded = false
		creditSnapshotMu.Unlock()
	}
}
