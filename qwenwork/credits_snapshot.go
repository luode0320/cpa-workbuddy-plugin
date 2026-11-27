// credits_snapshot.go 把账号积分快照落到插件自有的 sidecar 文件，作为跨重启的
// 唯一真相源：面板进页面只读它，不再为每个账号打上游；刷新由 1 小时定时扫描负责。
//
// 为什么不写进 auth 文件：每小时全量刷一次积分，若每次都改 auth 文件，宿主
// fsnotify 会按账号逐个触发整池 reload；而禁用状态是低频事件，只有它值得写进
// 宿主看得见的 auth 文件。
//
// 快照缺失一律按「未知」处理，绝不按 0 解释——0 分会命中付费模型排除判据，
// 把「没数据」当成「没钱」会让整池账号退出路由。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// defaultCreditsSnapshotName 是 sidecar 文件名，落在 <CLIProxyAPI root>/data/。
	defaultCreditsSnapshotName = "qwenwork-credits.json"

	// creditsSnapshotVersion 是 sidecar 结构版本。读到不认识的版本按空快照处理：
	// 旧版插件读新文件时宁可当作没有数据，也不要按错误 schema 解释积分。
	creditsSnapshotVersion = 1
)

// creditSnapshotEntry 是单个账号的积分快照。
type creditSnapshotEntry struct {
	// Remain 是拉取时刻可用于消费的积分总和，未知时为 -1。
	Remain int64 `json:"remain"`
	// FetchedAt 是这次拉取的完成时刻，供面板显示新鲜度。
	FetchedAt time.Time `json:"fetched_at"`
	// Nickname 是账号昵称，让 sidecar 在宿主 RPC 不可用时仍可读。
	Nickname string `json:"nickname,omitempty"`
	// Region 是账号归属，供面板分区展示。
	Region string `json:"region,omitempty"`
	// Credits 是完整积分明细。面板进页面只读落盘数据，缺了它就只能显示
	// 一个余量、丢掉已用/池总量/套餐明细。
	Credits *creditsSummary `json:"credits,omitempty"`
}

// creditSnapshotFile 是 sidecar 的落盘结构。
type creditSnapshotFile struct {
	Version   int                            `json:"version"`
	UpdatedAt time.Time                      `json:"updated_at"`
	Accounts  map[string]creditSnapshotEntry `json:"accounts"`
}

var (
	// creditSnapshotMu 保护下面三份内存状态：整轮扫描的批量写入与面板读取并发。
	creditSnapshotMu sync.RWMutex
	// creditSnapshotData 是内存镜像，键为宿主 auth.ID 归一化后的值。
	creditSnapshotData = map[string]creditSnapshotEntry{}
	// creditSnapshotLoaded 标记磁盘快照是否已完成载入（成功或确认无可用数据）。
	// 读盘遇到真实 IO 错误时保持 false，让下一次调用重新尝试。
	creditSnapshotLoaded bool
	// creditSnapshotLoadErr 记录最近一次载入的真实 IO 错误。
	creditSnapshotLoadErr error
)

// creditSnapshotFilePathFn 是 creditSnapshotPath 的可替换入口，供单测注入临时目录。
var creditSnapshotFilePathFn = creditSnapshotPath

// creditSnapshotPath 返回 sidecar 的绝对路径，与 usage feed 共用根目录推导。
// [参数] 无。
// [返回] sidecar 文件路径；无法定位安装根目录时退化为相对路径。
func creditSnapshotPath() string {
	// 1. 优先按工作目录推导安装根，插件运行时这是最可靠的来源。
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", defaultCreditsSnapshotName)
	}
	// 2. 回落到可执行文件所在目录。
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", defaultCreditsSnapshotName)
	}
	// 3. 都拿不到时用相对路径，由调用方的写盘错误兜底。
	return filepath.Join("data", defaultCreditsSnapshotName)
}

// ensureCreditSnapshotLoaded 在首次访问时把磁盘快照读进内存。
//
// 读失败、文件不存在、JSON 损坏、版本不认识，一律按空快照处理并继续：
// 快照只是加速与跨重启记忆，缺失时下一个扫描周期会重新填满。
// [参数] 无。
// [返回] 无。
func ensureCreditSnapshotLoaded() {
	// 1. 快路径：已载入过就直接返回，避免每次读取都加写锁。
	creditSnapshotMu.RLock()
	loaded := creditSnapshotLoaded
	creditSnapshotMu.RUnlock()
	if loaded {
		return
	}
	// 2. 慢路径加写锁并二次确认，防止并发读者各读一次。
	creditSnapshotMu.Lock()
	defer creditSnapshotMu.Unlock()
	if creditSnapshotLoaded {
		return
	}
	// 3. 读盘。只有拿到确定结论才置位：文件不存在、内容为空、JSON 损坏、
	// 版本不认识都属于「磁盘没有可用数据」，按空快照定稿；真实 IO 错误则
	// 保持未载入并记录错误，交给下一次调用重试。
	raw, err := os.ReadFile(creditSnapshotFilePathFn())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			creditSnapshotLoaded = true
			creditSnapshotLoadErr = nil
			return
		}
		creditSnapshotLoadErr = err
		return
	}
	if len(raw) == 0 {
		creditSnapshotLoaded = true
		creditSnapshotLoadErr = nil
		return
	}
	var file creditSnapshotFile
	if err := json.Unmarshal(raw, &file); err != nil {
		// 损坏文件按空快照定稿：覆盖式落盘会把它替换成正确内容。
		creditSnapshotLoaded = true
		creditSnapshotLoadErr = nil
		return
	}
	if file.Version != creditsSnapshotVersion {
		creditSnapshotLoaded = true
		creditSnapshotLoadErr = nil
		return
	}
	// 4. 逐条归一化键后装入内存镜像，坏键直接跳过。内存里已有同键时保留内存值：
	// 磁盘是上一轮落盘的结果，本轮刚拉到的积分比它新，载入不能倒灌覆盖。
	for id, entry := range file.Accounts {
		key := normalizeFailoverKey(id)
		if key == "" {
			continue
		}
		if _, exists := creditSnapshotData[key]; exists {
			continue
		}
		creditSnapshotData[key] = entry
	}
	creditSnapshotLoaded = true
	creditSnapshotLoadErr = nil
}

// creditSnapshotGet 读取单个账号的积分快照。
// [参数] authID：账号标识（auth.ID / UID / auth_index 任一形态）。
// [返回] 快照条目与是否存在；不存在时返回零值与 false。
func creditSnapshotGet(authID string) (creditSnapshotEntry, bool) {
	ensureCreditSnapshotLoaded()
	key := normalizeFailoverKey(authID)
	if key == "" {
		return creditSnapshotEntry{}, false
	}
	creditSnapshotMu.RLock()
	defer creditSnapshotMu.RUnlock()
	entry, ok := creditSnapshotData[key]
	return entry, ok
}

// creditSnapshotAll 返回全部快照的副本，供面板一次性渲染。
// [参数] 无。
// [返回] 键为归一化账号标识的快照副本。
func creditSnapshotAll() map[string]creditSnapshotEntry {
	ensureCreditSnapshotLoaded()
	creditSnapshotMu.RLock()
	defer creditSnapshotMu.RUnlock()
	out := make(map[string]creditSnapshotEntry, len(creditSnapshotData))
	for k, v := range creditSnapshotData {
		out[k] = v
	}
	return out
}

// recordCreditSnapshot 把单个账号的最新积分写进内存镜像。
//
// 只写内存、不落盘：整轮扫描里逐账号写盘会让磁盘 IO 变成 N 次，
// 落盘由扫描在轮末统一调用 persistCreditSnapshot 完成。
// [参数] authID：账号标识；cr：本次拉到的积分明细，nil 时忽略；
// nickname/region：展示用元数据。
// [返回] 无。
func recordCreditSnapshot(authID string, cr *creditsSummary, nickname, region string) {
	// 1. 积分与账号标识任一缺失都不写，避免用「未知」覆盖真实快照。
	if cr == nil {
		return
	}
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	} // 2. 组装条目并加写锁写入内存镜像。
	entry := creditSnapshotEntry{
		Remain:    cr.TotalRemain,
		FetchedAt: time.Now().UTC(),
		Nickname:  nickname,
		Region:    region,
		Credits:   cr,
	}
	creditSnapshotMu.Lock()
	creditSnapshotData[key] = entry
	creditSnapshotMu.Unlock()
}

// forgetCreditSnapshot 删除一个账号的快照，供账号被删除时清理。
// [参数] authID：账号标识。
// [返回] 无。
func forgetCreditSnapshot(authID string) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	creditSnapshotMu.Lock()
	delete(creditSnapshotData, key)
	creditSnapshotMu.Unlock()
}

// persistCreditSnapshot 把内存镜像合并写盘（唯一临时文件 + 原子重命名）。
//
// 整轮扫描只在轮末调用一次：中途崩溃时磁盘保留上一轮的完整快照，而不是一份
// 写了一半、被读成「部分账号 0 分」的残缺文件。写盘前先与磁盘做键级合并：
// 多实例并存时，别的实例刚写进去的账号不会因为本实例内存里没有它们而被抹掉。
// [参数] 无。
// [返回] 写盘错误；无错误时返回 nil。
func persistCreditSnapshot() error {
	// 1. 确保磁盘快照已载入。载入遇到真实 IO 错误时重试一次，仍失败就放弃本次
	// 落盘：拿不完整的内存镜像覆盖磁盘，比不写更糟。
	ensureCreditSnapshotLoaded()
	if err := currentCreditSnapshotLoadErr(); err != nil {
		ensureCreditSnapshotLoaded()
		if err := currentCreditSnapshotLoadErr(); err != nil {
			return fmt.Errorf("load existing snapshot: %w", err)
		}
	}
	path := creditSnapshotFilePathFn()
	// 2. 先读磁盘做键级合并：磁盘有、内存没有的条目保留下来，把整文件覆盖
	// 降级为合并覆盖。读不到或解析失败时按空处理，走覆盖式写入自愈。
	file := creditSnapshotFile{
		Version:   creditsSnapshotVersion,
		UpdatedAt: time.Now().UTC(),
		Accounts:  make(map[string]creditSnapshotEntry),
	}
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		var disk creditSnapshotFile
		if err := json.Unmarshal(raw, &disk); err == nil && disk.Version == creditsSnapshotVersion {
			for id, entry := range disk.Accounts {
				key := normalizeFailoverKey(id)
				if key == "" {
					continue
				}
				file.Accounts[key] = entry
			}
		}
	}
	// 3. 在读锁内把内存镜像叠加到合并结果上，缩短持锁时间。
	creditSnapshotMu.RLock()
	for k, v := range creditSnapshotData {
		file.Accounts[k] = v
	}
	creditSnapshotMu.RUnlock()
	// 4. 序列化并确保目录存在。
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 5. 临时文件名必须唯一：固定 path+".tmp" 会让并发的第二个写者先删掉
	// 第一个写者的临时文件，导致对方的 rename 拿到 ENOENT。
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	// 6. 原子重命名；失败时清理临时文件，并在 ErrNotExist 场景重试一次。
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tmp2 := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
		if werr := os.WriteFile(tmp2, raw, 0o644); werr != nil {
			return werr
		}
		if rerr := os.Rename(tmp2, path); rerr != nil {
			_ = os.Remove(tmp2)
			return rerr
		}
		return nil
	}
	return nil
}

// currentCreditSnapshotLoadErr 读取最近一次载入的真实 IO 错误。
// [参数] 无。
// [返回] 内存镜像是否处于「载入失败」状态；无错误时返回 nil。
func currentCreditSnapshotLoadErr() error {
	creditSnapshotMu.RLock()
	defer creditSnapshotMu.RUnlock()
	return creditSnapshotLoadErr
}

// resetCreditSnapshotForTest 清空内存镜像并允许重新从磁盘载入，仅供测试使用。
// [参数] 无。
// [返回] 无。
func resetCreditSnapshotForTest() {
	creditSnapshotMu.Lock()
	creditSnapshotData = map[string]creditSnapshotEntry{}
	creditSnapshotLoaded = false
	creditSnapshotLoadErr = nil
	creditSnapshotMu.Unlock()
}
