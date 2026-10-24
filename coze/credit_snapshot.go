package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 本文件把账号积分落到插件自有的 sidecar 文件，作为跨重启的唯一真相源。
//
// 为什么不写进 auth 文件：巡检每小时为全部账号刷新一次积分，若每次都改
// auth 文件，宿主 fsnotify 会按账号逐个触发整池 reload，等于每小时抖动
// 十几次；而禁用状态是低频事件，只有它值得写进宿主看得见的 auth 文件
// （见 credit_grade.go）。
//
// 快照缺失一律按「未知」处理，绝不按 0 解释——0 分会触发禁用，把
// 「没数据」当成「没钱」会把整池账号写死。

const (
	// creditSnapshotName 是 sidecar 文件名，落在 <CLIProxyAPI root>/data/。
	creditSnapshotName = "coze-credits.json"

	// creditSnapshotVersion 是 sidecar 结构版本。读到不认识的版本按空快照
	// 处理：旧版插件读新文件时宁可当作没有数据，也不要按错误 schema 解释积分。
	creditSnapshotVersion = 1

	// creditUnknownRemain 是「积分未知」的哨兵值。
	//
	// 用它而不是 0：0 是合法的真实余额（账号确实耗尽），
	// 两者混用会让「查不到」被当成「没钱」直接禁用掉健康账号。
	creditUnknownRemain int64 = -1
)

// creditSnapshotEntry 是单个账号的积分快照。
type creditSnapshotEntry struct {
	// Remain 是拉取时刻的可用积分，未知时为 creditUnknownRemain。
	Remain int64 `json:"remain"`
	// FetchedAt 是这次拉取的完成时刻，供面板显示新鲜度。
	FetchedAt string `json:"fetched_at"`
	// Nickname 是账号昵称，让 sidecar 在宿主 RPC 不可用时仍可读。
	Nickname string `json:"nickname,omitempty"`
	// Credits 是完整积分明细。面板进页面只读落盘数据，缺了它就只能显示
	// 一个余量、丢掉已用/池总量/过期明细，面板会比刷新后明显变薄。
	Credits *creditSummary `json:"credits,omitempty"`
	// DailyClaimedAt 是最近一次「确认当日签到到账」的时刻（RFC3339）。
	//
	// 必须单独存，不能只靠 Credits.Items 反推：当日额度被消耗光后，
	// 该条目会从「即将过期」列表里消失，纯明细反推会把「已领且已用完」
	// 误报成「今天还没领」，面板会诱导用户重复点击补领。
	DailyClaimedAt string `json:"daily_claimed_at,omitempty"`
	// DailyClaimedAmount 是确认到账的当日额度，0 表示上游未返回。
	DailyClaimedAmount int64 `json:"daily_claimed_amount,omitempty"`
}

// creditSnapshotFile 是 sidecar 的落盘结构。
type creditSnapshotFile struct {
	Version   int                            `json:"version"`
	UpdatedAt string                         `json:"updated_at"`
	Accounts  map[string]creditSnapshotEntry `json:"accounts"`
}

var (
	// creditSnapshotMu 保护下面两份内存状态：整轮扫描的批量写入与面板读取并发。
	creditSnapshotMu sync.RWMutex
	// creditSnapshotData 是内存镜像，键为账号标识。
	creditSnapshotData = map[string]creditSnapshotEntry{}
	// creditSnapshotLoaded 标记是否已从磁盘载入过，避免每次读取都读盘。
	// 进程重启后由首个读者触发一次载入；热重载不重置它，磁盘是唯一真相源。
	creditSnapshotLoaded bool
)

// creditSnapshotPath 返回 sidecar 的绝对路径。
//
// 与 usage feed 共用根目录推导：先按工作目录向上找 plugins/ 定位安装根，
// 再退回可执行文件所在目录；两者都拿不到时用相对路径，由写盘错误兜底。
// [参数] 无。
// [返回] sidecar 文件路径。
// 最近修改时间：2026-10-25；改动原因：新增，积分快照落盘位置。
func creditSnapshotPath() string {
	// 1. 优先按工作目录推导安装根，插件运行时这是最可靠的来源。
	if root, ok := cliProxyRootFromWorkingDir(); ok {
		return filepath.Join(root, "data", creditSnapshotName)
	}
	// 2. 回落到可执行文件所在目录。
	if root, ok := cliProxyRootFromExecutable(); ok {
		return filepath.Join(root, "data", creditSnapshotName)
	}
	// 3. 都拿不到时用相对路径。
	return filepath.Join("data", creditSnapshotName)
}

// cliProxyRootFromWorkingDir 从当前工作目录向上查找 CPA 安装根。
// [参数] 无。
// [返回] 安装根目录与是否找到。
// 最近修改时间：2026-10-25；改动原因：新增，coze 此前无安装根推导。
func cliProxyRootFromWorkingDir() (string, bool) {
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "", false
	}
	absolute, errAbs := filepath.Abs(wd)
	if errAbs != nil {
		return "", false
	}
	return cliProxyRootFromDir(absolute)
}

// cliProxyRootFromExecutable 从可执行文件所在目录向上查找 CPA 安装根。
// [参数] 无。
// [返回] 安装根目录与是否找到。
// 最近修改时间：2026-10-25；改动原因：新增，工作目录不可用时的兜底。
func cliProxyRootFromExecutable() (string, bool) {
	exe, errExecutable := os.Executable()
	if errExecutable != nil {
		return "", false
	}
	return cliProxyRootFromDir(filepath.Dir(exe))
}

// cliProxyRootFromDir 逐级向上查找含 plugins/ 子目录的目录。
//
// 判据是 plugins/ 存在：这是 CPA 安装根独有的结构，插件自身目录里没有它，
// 因此不会把账号目录或数据目录误判成安装根。
// [参数] dir: 起始目录。
// [返回] 安装根目录与是否找到。
// 最近修改时间：2026-10-25；改动原因：新增，安装根判定。
func cliProxyRootFromDir(dir string) (string, bool) {
	// 1. 从起始目录逐级向上，直到文件系统根。
	dir = filepath.Clean(dir)
	for {
		info, errStat := os.Stat(filepath.Join(dir, "plugins"))
		if errStat == nil && info.IsDir() && filepath.Dir(dir) != dir {
			return dir, true
		}
		parent := filepath.Dir(dir)
		// 2. 到达根目录仍未命中，判定失败。
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// ensureCreditSnapshotLoaded 在首次访问时把磁盘快照读进内存。
//
// 读失败、文件不存在、JSON 损坏、版本不认识，一律按空快照处理并继续：
// 快照只是加速与跨重启记忆，缺失时下一个巡检周期会重新填满；
// 若在这里 panic 或阻塞，等于让一个可自愈的数据缺失拖垮整个插件。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，首次读取载入。
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
	// 3. 先置位再读盘：读盘期间的并发读者不应该各读一次。
	creditSnapshotLoaded = true
	raw, errRead := os.ReadFile(creditSnapshotPath())
	if errRead != nil || len(raw) == 0 {
		return
	}
	var file creditSnapshotFile
	if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
		return
	}
	// 4. 版本不认识时按空快照处理，绝不按错误 schema 解释积分。
	if file.Version != creditSnapshotVersion {
		return
	}
	for id, entry := range file.Accounts {
		key := creditSnapshotKey(id)
		if key == "" {
			continue
		}
		creditSnapshotData[key] = entry
	}
}

// creditSnapshotKey 归一化快照键。
//
// 与积分缓存、闸门快照同口径：账号标识在宿主侧可能带 `.json` 后缀
// （补注册按文件名派生），必须剥掉后比对。大小写也归一，避免
// 同一账号因标识形态差异被存成两条。
//
// 刻意不剥 `coze-` 前缀：coze 的 AuthID 一律带该前缀（文件命名空间），
// 剥掉会让「按 raw AuthID 直接写入」的路径（测试夹具、外部补写）
// 与归一化读取失配，反而制造新的双键问题。
// [参数] authID: 账号标识。
// [返回] 归一化键；无法派生时返回空串。
// 最近修改时间：2026-10-25；改动原因：修正为只剥 .json 后缀，不再剥账号前缀。
func creditSnapshotKey(authID string) string {
	key := strings.ToLower(strings.TrimSpace(authID))
	key = strings.TrimSuffix(key, ".json")
	return strings.TrimSpace(key)
}

// creditSnapshotGet 读取单个账号的积分快照。
// [参数] authID: 账号标识。
// [返回] 快照条目与是否存在；不存在时返回零值与 false。
// 最近修改时间：2026-10-25；改动原因：新增，单账号读取。
func creditSnapshotGet(authID string) (creditSnapshotEntry, bool) {
	// 1. 确保磁盘快照已载入内存。
	ensureCreditSnapshotLoaded()
	// 2. 归一化键后加读锁查询。
	key := creditSnapshotKey(authID)
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
// 最近修改时间：2026-10-25；改动原因：新增，面板批量读取。
func creditSnapshotAll() map[string]creditSnapshotEntry {
	// 1. 确保磁盘快照已载入内存。
	ensureCreditSnapshotLoaded()
	// 2. 在读锁内整表复制，调用方可安全遍历而不持锁。
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
// 只写内存、不落盘：整轮巡检里逐账号写盘会让磁盘 IO 变成 N 次，
// 落盘由 runCreditSweep 在轮末统一调用 persistCreditSnapshot 完成。
//
// 已确认的签到态会从旧条目沿用：签到确认发生在 fetch 之后，
// 若这里把条目整体覆盖，同一轮里刚确认的「今日已领」会被抹掉。
// [参数] authID: 账号标识；cr: 本次拉到的积分明细，nil 时写入未知；
//
//	nickname: 展示用昵称。
//
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增签到态沿用，避免同轮覆盖。
func recordCreditSnapshot(authID string, cr *creditSummary, nickname string) {
	// 1. 账号标识缺失不写，避免产生无法回收的孤儿条目。
	key := creditSnapshotKey(authID)
	if key == "" {
		return
	}
	// 2. 组装条目；积分未知时用哨兵值而不是 0。
	entry := creditSnapshotEntry{
		Remain:    creditUnknownRemain,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Nickname:  nickname,
	}
	if cr != nil {
		entry.Remain = cr.Remain
		entry.Credits = cr
	}
	// 3. 加写锁写入内存镜像，同时沿用旧条目的签到态。
	creditSnapshotMu.Lock()
	if prev, ok := creditSnapshotData[key]; ok {
		entry.DailyClaimedAt = prev.DailyClaimedAt
		entry.DailyClaimedAmount = prev.DailyClaimedAmount
	}
	creditSnapshotData[key] = entry
	creditSnapshotMu.Unlock()
}

// recordDailyClaimedSnapshot 把「今日签到已确认到账」写进快照并落盘。
//
// 签到确认必须持久化：内存态在热重载 / 重启后丢失，而当日额度可能
// 早已被消耗光、从明细里消失，纯明细反推会把已领误报成未领。
// [参数] authID: 账号标识；amount: 确认到的当日额度。
// [返回] 无（落盘失败只记日志，签到结果本身仍然有效）。
// 最近修改时间：2026-10-25；改动原因：新增，签到态跨重启持久化。
func recordDailyClaimedSnapshot(authID string, amount int64) {
	key := creditSnapshotKey(authID)
	if key == "" {
		return
	}
	creditSnapshotMu.Lock()
	entry, ok := creditSnapshotData[key]
	if !ok {
		// 条目尚未建立时先建一条未知积分的占位，积分由下一轮巡检补齐。
		entry = creditSnapshotEntry{Remain: creditUnknownRemain}
	}
	entry.DailyClaimedAt = time.Now().UTC().Format(time.RFC3339)
	if amount > 0 {
		entry.DailyClaimedAmount = amount
	}
	creditSnapshotData[key] = entry
	creditSnapshotMu.Unlock()
	if errPersist := persistCreditSnapshot(); errPersist != nil {
		hostLogf("warn", "签到态落盘失败", map[string]any{
			"auth_id": authID,
			"error":   errPersist.Error(),
		})
	}
}

// dailyClaimedFromSnapshot 读取账号在快照里记录的签到确认时刻。
// [参数] authID: 账号标识。
// [返回] 确认时刻（RFC3339）与当日额度；无记录时返回空串与 0。
// 最近修改时间：2026-10-25；改动原因：新增，签到态读取入口。
func dailyClaimedFromSnapshot(authID string) (string, int64) {
	entry, ok := creditSnapshotGet(authID)
	if !ok {
		return "", 0
	}
	return entry.DailyClaimedAt, entry.DailyClaimedAmount
}

// forgetCreditSnapshot 删除一个账号的快照，供账号被删除时清理。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，删除账号时清理残留。
func forgetCreditSnapshot(authID string) {
	// 1. 归一化键后从内存镜像删除。
	key := creditSnapshotKey(authID)
	if key == "" {
		return
	}
	creditSnapshotMu.Lock()
	delete(creditSnapshotData, key)
	creditSnapshotMu.Unlock()
}

// persistCreditSnapshot 把内存镜像整体写盘（临时文件 + 原子重命名）。
//
// 整轮巡检只在轮末调用一次：中途崩溃时磁盘保留上一轮的完整快照，
// 而不是一份写了一半、被读成「部分账号 0 分」的残缺文件。
// [参数] 无。
// [返回] 写盘错误；无错误时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，轮末整表落盘。
func persistCreditSnapshot() error {
	// 1. 确保已有磁盘快照载入，避免写盘时丢掉未载入的旧条目。
	ensureCreditSnapshotLoaded()
	// 2. 在读锁内复制整表，缩短持锁时间。
	creditSnapshotMu.RLock()
	file := creditSnapshotFile{
		Version:   creditSnapshotVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Accounts:  make(map[string]creditSnapshotEntry, len(creditSnapshotData)),
	}
	for k, v := range creditSnapshotData {
		file.Accounts[k] = v
	}
	creditSnapshotMu.RUnlock()

	// 3. 序列化并确保目录存在。
	raw, errMarshal := json.MarshalIndent(file, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	path := creditSnapshotPath()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return errMkdir
	}
	// 4. 先写临时文件再原子重命名，避免读者看到半截文件。
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, raw, 0o644); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, path)
}

// resetCreditSnapshotForTest 清空内存镜像并允许重新从磁盘载入，仅供测试使用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，测试隔离。
func resetCreditSnapshotForTest() {
	creditSnapshotMu.Lock()
	creditSnapshotData = map[string]creditSnapshotEntry{}
	creditSnapshotLoaded = false
	creditSnapshotMu.Unlock()
}
