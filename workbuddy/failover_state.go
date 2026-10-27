// failover_state.go 把「账号被本插件禁用了」这件事落到插件自有的 sidecar 文件，
// 作为跨重启、跨宿主回写的唯一真相源。
//
// 为什么不继续用宿主读得到的 auth 文件 `disabled` 字段（2026-10-25 生产实证）：
//  1. 宿主重建 auth 文档时按内存里的 Status 写回 disabled，且只保留白名单键
//     （type/provider/logo/note/disabled/auth/account），本机制的三键被丢弃；
//  2. 宿主 fsnotify 对 auths 目录的监听会静默失效：qoderwork 生产实测目录 mtime
//     持续更新、文件持续被写入，但 5 小时窗口内 01:00 之后零事件。
//
// 两条路同时断掉，写盘禁用就无法到达宿主路由判据。本文件让插件自己记住状态，
// 由 scheduler.pick 与 pickNextAuth 直接读它做排除——这条链路不经过宿主。
//
// 落盘位置：`<CPA root>/data/workbuddy-failover.json`，与 usage feed 同级。
// 读取一律容错：文件缺失、JSON 损坏、版本不认识都按空状态处理并继续。
// 禁用状态丢失只会让账号重新接流量（比整池卡死更可接受），绝不能因读取失败
// 让插件 panic 或拒绝服务。
//
// 与积分快照刻意分成两个文件：`workbuddy-credits.json` 是高频写（每轮刷新整份
// 原子替换），若与低频故障态共用一个文件，高频替换会把禁用状态冲掉。
//
// 最近修改时间：2026-10-27；改动原因：新增，摆脱对宿主 auth 文件回写的依赖。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// failoverStateFileName 是 sidecar 文件名，落在 <CPA root>/data/。
	failoverStateFileName = "workbuddy-failover.json"

	// failoverStateVersion 是 sidecar 结构版本。读到不认识的版本按空状态处理：
	// 旧插件读新文件时宁可当作没有数据，也不要按错误 schema 解释禁用状态。
	failoverStateVersion = 1
)

// failoverStateEntry 是单个账号在 sidecar 里的状态。
type failoverStateEntry struct {
	// Disabled 表示该账号被本插件判定为不可用，路由必须跳过它。
	Disabled bool `json:"disabled"`
	// Model 是触发禁用的上游模型名，探活只回放这一个模型。
	Model string `json:"model,omitempty"`
	// At 是禁用写入时刻，供面板与人工诊断。
	At string `json:"at,omitempty"`
}

// failoverStateFile 是 sidecar 的顶层结构。
type failoverStateFile struct {
	Version   int                           `json:"version"`
	UpdatedAt string                        `json:"updated_at"`
	Accounts  map[string]failoverStateEntry `json:"accounts"`
}

var (
	// failoverStateMu 保护 sidecar 的内存镜像与落盘互斥。
	failoverStateMu sync.Mutex
	// failoverStateAccounts 是 sidecar 的内存镜像，键为归一化账号标识。
	// 首次访问时从磁盘载入；每次写操作同步落盘。
	failoverStateAccounts = make(map[string]failoverStateEntry)
	// failoverStateLoaded 标记内存镜像是否已从磁盘载入过。
	failoverStateLoaded bool
	// failoverStatePathOverride 覆盖 sidecar 路径，仅供测试隔离使用。
	// 为空时走 failoverStatePath 的自动推导。
	failoverStatePathOverride string
)

// failoverStatePath 返回 sidecar 的落盘路径。
// [参数] 无。
// [返回] 绝对路径；根目录推导失败时返回相对路径，由写盘错误兜底。
// 最近修改时间：2026-10-27；改动原因：新增。
func failoverStatePath() string {
	if override := strings.TrimSpace(failoverStatePathOverride); override != "" {
		return override
	}
	// 1. 优先按工作目录推导安装根，插件运行时这是最可靠的来源。
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", failoverStateFileName)
	}
	// 2. 回落到可执行文件所在目录。
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", failoverStateFileName)
	}
	// 3. 都拿不到时用相对路径，由调用方的写盘错误兜底。
	return filepath.Join("data", failoverStateFileName)
}

// ensureFailoverStateLoadedLocked 在首次访问时把磁盘状态读进内存。
// 读失败、文件不存在、JSON 损坏、版本不认识一律按空状态处理并继续。
// 调用方必须持有 failoverStateMu。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func ensureFailoverStateLoadedLocked() {
	if failoverStateLoaded {
		return
	}
	failoverStateLoaded = true
	raw, err := os.ReadFile(failoverStatePath())
	if err != nil {
		return
	}
	var doc failoverStateFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	if doc.Version != failoverStateVersion {
		return
	}
	for id, entry := range doc.Accounts {
		key := normalizeFailoverStateKey(id)
		if key == "" {
			continue
		}
		failoverStateAccounts[key] = entry
	}
}

// normalizeFailoverStateKey 把账号标识归一化成 sidecar 的键。
// 与冷却机制共用 normalizeFailoverKey，保证两套判据对同一账号得到同一个键。
// [参数] authID：账号标识（可为文件名、UID、auth_index 任一形态）。
// [返回] 归一化后的键；输入为空时返回空串。
// 最近修改时间：2026-10-27；改动原因：新增。
func normalizeFailoverStateKey(authID string) string {
	if key := normalizeFailoverKey(authID); key != "" {
		return key
	}
	return strings.TrimSpace(strings.ToLower(authID))
}

// failoverStateGet 读取账号的 sidecar 状态。
// [参数] authID：账号标识。
// [返回] 状态条目与是否存在。
// 最近修改时间：2026-10-27；改动原因：新增。
func failoverStateGet(authID string) (failoverStateEntry, bool) {
	key := normalizeFailoverStateKey(authID)
	if key == "" {
		return failoverStateEntry{}, false
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	ensureFailoverStateLoadedLocked()
	entry, ok := failoverStateAccounts[key]
	return entry, ok
}

// failoverStateIsDisabled 判定账号是否被本插件禁用。
// [参数] authID：账号标识。
// [返回] true 表示路由必须跳过该账号。
// 最近修改时间：2026-10-27；改动原因：新增，供热路径快速判据。
func failoverStateIsDisabled(authID string) bool {
	entry, ok := failoverStateGet(authID)
	return ok && entry.Disabled
}

// failoverStateMarkDisabled 记录账号被禁用并立即落盘。
// [参数] authID：账号标识；model：触发禁用的模型名，可为空。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func failoverStateMarkDisabled(authID, model string) {
	key := normalizeFailoverStateKey(authID)
	if key == "" {
		return
	}
	failoverStateMu.Lock()
	ensureFailoverStateLoadedLocked()
	entry := failoverStateAccounts[key]
	// 已禁用且模型未变时不重复落盘：失败可能在同模型上连续触发，
	// 每次都写盘等于把高频失败放大成高频磁盘 IO。
	if entry.Disabled && entry.Model == strings.TrimSpace(model) {
		failoverStateMu.Unlock()
		return
	}
	entry.Disabled = true
	entry.Model = strings.TrimSpace(model)
	entry.At = time.Now().UTC().Format(time.RFC3339)
	failoverStateAccounts[key] = entry
	failoverStateMu.Unlock()
	persistFailoverState()
}

// failoverStateClearDisabled 清除账号的禁用标记并立即落盘。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-27；改动原因：新增。
func failoverStateClearDisabled(authID string) {
	key := normalizeFailoverStateKey(authID)
	if key == "" {
		return
	}
	failoverStateMu.Lock()
	ensureFailoverStateLoadedLocked()
	entry, ok := failoverStateAccounts[key]
	if !ok {
		failoverStateMu.Unlock()
		return
	}
	entry.Disabled = false
	entry.Model = ""
	entry.At = ""
	failoverStateAccounts[key] = entry
	failoverStateMu.Unlock()
	persistFailoverState()
}

// failoverStateDisabledIDs 返回当前全部被禁用的账号键。
// [参数] 无。
// [返回] 归并排序后的键列表；无禁用账号时返回 nil。
// 最近修改时间：2026-10-27；改动原因：新增，供探活枚举与面板统计。
func failoverStateDisabledIDs() []string {
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	ensureFailoverStateLoadedLocked()
	var out []string
	for key, entry := range failoverStateAccounts {
		if entry.Disabled {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// persistFailoverState 把内存镜像整体写回磁盘。
//
// 写盘失败只记日志不阻断：sidecar 是加速与跨重启记忆，写失败最多让状态丢一次，
// 若在这里返回错误会让禁用判定在写盘失败时整体失效，反而更危险。
//
// [参数] 无。
// [返回] 写盘错误，供测试断言；生产调用方忽略。
// 最近修改时间：2026-10-27；改动原因：新增。
func persistFailoverState() error {
	failoverStateMu.Lock()
	doc := failoverStateFile{
		Version:   failoverStateVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Accounts:  make(map[string]failoverStateEntry, len(failoverStateAccounts)),
	}
	for key, entry := range failoverStateAccounts {
		// 全空条目不值得占盘。
		if !entry.Disabled && entry.Model == "" && entry.At == "" {
			continue
		}
		doc.Accounts[key] = entry
	}
	failoverStateMu.Unlock()

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := failoverStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".workbuddy-failover-*")
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
	// 原子替换：读者要么看到旧文件要么看到新文件，不会读到写一半的 JSON。
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}
