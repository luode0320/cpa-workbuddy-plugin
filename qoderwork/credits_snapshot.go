// credits_snapshot.go 把账号积分快照落到插件自有的 sidecar 文件，作为跨重启的
// 记忆：面板刷新前先读它，调度排序也能在宿主 RPC 慢或不可达时继续工作。
//
// 为什么不写进 auth 文件：积分为每小时刷新一次，若每次都改 auth 文件，宿主
// fsnotify 会按账号逐个触发整池 reload，等于每小时抖动几十次；而禁用状态是
// 低频事件，值得写进宿主看得见的 auth 文件（侧边记忆另由 failover_state.go 承担）。
//
// 快照缺失一律按「未知」处理，绝不按 0 解释——0 分会触发禁用，
// 把「没数据」当成「没钱」会把整池账号写死。
//
// 与 failover 状态分文件：积分是高频刷新的业务数据，禁用是低频的故障状态，
// 混在一个文件里会让高频写入不断覆盖低频状态（原子重命名会整份替换）。
//
// 最近修改时间：2026-10-25；改动原因：新增，从 workbuddy 移植积分快照持久化。
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
	// defaultCreditsSnapshotName 是 sidecar 文件名，落在 <CLIProxyAPI root>/data/。
	defaultCreditsSnapshotName = "qoderwork-credits.json"

	// creditsSnapshotVersion 是 sidecar 结构版本。读到不认识的版本按空快照处理：
	// 旧版插件读新文件时宁可当作没有数据，也不要按错误 schema 解释积分。
	creditsSnapshotVersion = 1
)

// creditSnapshotEntry 是单个账号的积分快照。
type creditSnapshotEntry struct {
	// Remain 是拉取时刻可用于消费的积分总和，未知时为 -1。
	Remain int64 `json:"remain"`
	// FetchedAt 是这次拉取的完成时刻，供面板显示新鲜度。
	FetchedAt string `json:"fetched_at,omitempty"`
	// Plan 是套餐标识，跨重启保留可让面板首屏不闪空。
	Plan string `json:"plan,omitempty"`
	// Credits 是完整积分明细。面板进页面只读落盘数据，缺了它就只能显示
	// 一个余量、丢掉已用/池总量/套餐明细，面板会比刷新后明显变薄。
	Credits *creditsSummary `json:"credits,omitempty"`
}

// creditSnapshotFile 是 sidecar 的落盘结构。
type creditSnapshotFile struct {
	Version   int                            `json:"version"`
	UpdatedAt string                         `json:"updated_at"`
	Accounts  map[string]creditSnapshotEntry `json:"accounts"`
}

var (
	// creditSnapshotMu 保护内存镜像：整轮扫描的批量写入与面板/调度读取并发。
	creditSnapshotMu sync.RWMutex
	// creditSnapshotData 是内存镜像，键为归一化账号标识。
	creditSnapshotData = map[string]creditSnapshotEntry{}
	// creditSnapshotLoaded 标记是否已从磁盘载入过，避免每次读取都读盘。
	creditSnapshotLoaded bool
	// creditSnapshotPathOverride 覆盖 sidecar 路径，仅供测试隔离使用。
	creditSnapshotPathOverride string
)

// creditSnapshotPath 返回 sidecar 的绝对路径，与 usage feed 共用根目录推导。
// [参数] 无。
// [返回] sidecar 文件路径；无法定位安装根目录时退化为相对路径。
// 最近修改时间：2026-10-25；改动原因：新增。
func creditSnapshotPath() string {
	if override := strings.TrimSpace(creditSnapshotPathOverride); override != "" {
		return override
	}
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", defaultCreditsSnapshotName)
	}
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", defaultCreditsSnapshotName)
	}
	return filepath.Join("data", defaultCreditsSnapshotName)
}

// ensureCreditSnapshotLoaded 在首次访问时把磁盘快照读进内存。
//
// 读失败、文件不存在、JSON 损坏、版本不认识，一律按空快照处理并继续：
// 快照只是加速与跨重启记忆，缺失时下一个扫描周期会重新填满；
// 若在这里 panic 或阻塞，等于让一个可自愈的数据缺失拖垮整个插件。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func ensureCreditSnapshotLoaded() {
	creditSnapshotMu.RLock()
	loaded := creditSnapshotLoaded
	creditSnapshotMu.RUnlock()
	if loaded {
		return
	}
	creditSnapshotMu.Lock()
	defer creditSnapshotMu.Unlock()
	if creditSnapshotLoaded {
		return
	}
	creditSnapshotLoaded = true
	raw, err := os.ReadFile(creditSnapshotPath())
	if err != nil {
		return
	}
	var file creditSnapshotFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return
	}
	if file.Version != creditsSnapshotVersion {
		return
	}
	for id, entry := range file.Accounts {
		key := normalizeFailoverKey(id)
		if key == "" {
			continue
		}
		creditSnapshotData[key] = entry
	}
}

// creditSnapshotGet 读取账号的积分快照。
// [参数] authID：账号标识。
// [返回] 快照条目与是否存在；不存在时 ok=false。
// 最近修改时间：2026-10-25；改动原因：新增。
func creditSnapshotGet(authID string) (creditSnapshotEntry, bool) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return creditSnapshotEntry{}, false
	}
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.RLock()
	defer creditSnapshotMu.RUnlock()
	entry, ok := creditSnapshotData[key]
	return entry, ok
}

// recordCreditSnapshot 记录一次积分拉取结果到内存镜像。
// 只写内存，落盘由 persistCreditSnapshot 在轮末统一执行。
// [参数] authID：账号标识；cr：积分汇总，nil 时忽略；plan：套餐标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func recordCreditSnapshot(authID string, cr *creditsSummary, plan string) {
	// 积分与账号标识任一缺失都不写，避免用「未知」覆盖真实快照。
	if cr == nil {
		return
	}
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	entry := creditSnapshotEntry{
		Remain:    cr.TotalRemain,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Plan:      strings.TrimSpace(plan),
		Credits:   cr,
	}
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.Lock()
	creditSnapshotData[key] = entry
	creditSnapshotMu.Unlock()
}

// forgetCreditSnapshot 删除一个账号的快照，供账号被删除时清理。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func forgetCreditSnapshot(authID string) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.Lock()
	delete(creditSnapshotData, key)
	creditSnapshotMu.Unlock()
}

// persistCreditSnapshot 把内存镜像整体写盘（临时文件 + 原子重命名）。
//
// 整轮扫描只在轮末调用一次：中途崩溃时磁盘保留上一轮的完整快照，
// 而不是一份写了一半、被读成「部分账号 0 分」的残缺文件。
// [参数] 无。
// [返回] 写盘错误；无错误时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增。
func persistCreditSnapshot() error {
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.RLock()
	file := creditSnapshotFile{
		Version:   creditsSnapshotVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Accounts:  make(map[string]creditSnapshotEntry, len(creditSnapshotData)),
	}
	for k, v := range creditSnapshotData {
		file.Accounts[k] = v
	}
	creditSnapshotMu.RUnlock()

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := creditSnapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resetCreditSnapshotForTest 清空内存镜像并允许重新从磁盘载入，仅供测试使用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增。
func resetCreditSnapshotForTest() {
	creditSnapshotMu.Lock()
	creditSnapshotData = map[string]creditSnapshotEntry{}
	creditSnapshotLoaded = false
	creditSnapshotMu.Unlock()
}

// isolateCreditSnapshotPathForTest 把 sidecar 指向调用方提供的临时文件，返回还原函数。
// [参数] path：临时文件路径。
// [返回] 还原函数。
// 最近修改时间：2026-10-25；改动原因：新增，避免用例之间经由真实 data 目录互相污染。
func isolateCreditSnapshotPathForTest(path string) func() {
	creditSnapshotMu.Lock()
	old := creditSnapshotPathOverride
	creditSnapshotPathOverride = path
	creditSnapshotData = map[string]creditSnapshotEntry{}
	creditSnapshotLoaded = false
	creditSnapshotMu.Unlock()
	return func() {
		creditSnapshotMu.Lock()
		creditSnapshotPathOverride = old
		creditSnapshotData = map[string]creditSnapshotEntry{}
		creditSnapshotLoaded = false
		creditSnapshotMu.Unlock()
	}
}
