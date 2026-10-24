// credits_persist.go 把积分快照持久化到物理 auth 文件的顶层 JSON，
// 使容器重启 / 插件热重载后积分仍能立刻显示，不必等首轮上游刷新。
//
// 与 counter.go 的关系：两者结构同源（内存为准、JSON 为兜底备份、watchdog
// 节奏落盘、UID 为键、persistAuthDirect 直写），但数据性质不同——
//   - 计数器是累加量：flusher 只折叠「尚未落盘的增量」；
//   - 积分是快照：整份替换，没有增量语义，因此落盘判据是「内存快照是否已
//     写过盘」，用内存里的 serial 与 persistedSerial 对齐实现幂等。
//
// 为什么需要：积分原本只活在 accountCache（纯内存 sync.Map）里，进程一退
// 就全丢，面板首屏的积分列会退回「加载中…」。cache.go 的 TTL 只有 45 秒，
// 且 GC 会把超过 4×TTL 的条目直接删除，所以内存缓存本身也不承担长期存储。
//
// 字段命名选 credits_snapshot 而不是复用 total_remain 等：宿主 auth 类型
// 不认这些键，独立键名让「这是插件自己写的快照」一眼可辨，避免与上游原始
// 字段产生真假二义。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// creditsSnapshotKey 是物理 auth 文件里承载积分快照的顶层键。
const creditsSnapshotKey = "credits_snapshot"

// creditsPersistEntry 记录一个账号的积分持久化状态。
//
// serial 是内存快照的版本号：每次 cachedAccountDetails 成功刷新积分就自增。
// persistedSerial 是最后一次写进物理文件的版本号，二者相等说明无需再写。
type creditsPersistEntry struct {
	cr              *creditsSummary
	serial          uint64
	persistedSerial uint64
}

var (
	creditsPersistMu sync.Mutex
	// creditsPersistEntries 以账号 UID 为键——与计数器一致，watchdog 侧
	// 能通过 resolveAuthIndexAndID 反查到物理文件。
	creditsPersistEntries = make(map[string]creditsPersistEntry)
	// creditsPersistLoaded 标记该 UID 是否已从盘上播种过，保证幂等。
	creditsPersistLoaded = make(map[string]bool)
)

// tagCreditsPersist 在积分快照被刷新后打上「待落盘」标记。
//
// 由 cachedAccountDetails 在写入 accountCache 后调用；纯内存操作，不写盘。
// [参数] uid：账号 UID，空表示不可归属（对照组为无 UID 的遗留账号），直接跳过；
// cr：最新积分快照，nil 表示本次没取到。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func tagCreditsPersist(uid string, cr *creditsSummary) {
	uid = strings.TrimSpace(uid)
	if uid == "" || cr == nil {
		return
	}
	creditsPersistMu.Lock()
	defer creditsPersistMu.Unlock()
	e := creditsPersistEntries[uid]
	e.cr = cr
	e.serial++
	creditsPersistEntries[uid] = e
}

// ensureCreditsPersistLoaded 从物理 auth JSON 播种内存快照，仅执行一次。
//
// 与 ensureCounterLoaded 的关键差异：积分是快照不是累加，所以这里「内存优先」
// 的语义是——若本次进程已经取到了更新的快照（serial > 0），就保留内存值，
// 只把 persistedSerial 对齐盘上版本，避免用旧盘值覆盖刚拿到的实时数据。
// [参数] uid：账号 UID；physJSON：物理 auth 文件原文。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func ensureCreditsPersistLoaded(uid string, physJSON []byte) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	creditsPersistMu.Lock()
	defer creditsPersistMu.Unlock()
	if creditsPersistLoaded[uid] {
		return
	}
	creditsPersistLoaded[uid] = true
	cr := parseCreditsSnapshotFromAuthJSON(physJSON)
	if cr == nil {
		return
	}
	e := creditsPersistEntries[uid]
	if e.serial == 0 {
		// 本进程还没取过积分：用盘上快照作为起点，并视为已落盘。
		e.cr = cr
		e.persistedSerial = e.serial
		creditsPersistEntries[uid] = e
		return
	}
	// 已有更新的实时快照：保留内存值，只记录盘上已有旧快照这一事实，
	// 让后续 flush 把新快照覆盖上去。
	e.persistedSerial = 0
	creditsPersistEntries[uid] = e
}

// seedCreditsCacheFromDisk 把盘上积分快照回灌进 accountCache。
//
// 这是持久化对面板的全部价值：进程重启后首屏不必等上游刷新就能显示数字。
// 只在该 authID 尚无有效缓存条目时回灌，绝不覆盖本进程已取到的新数据；
// fetched 设为当前时间而非快照里的旧时间，避免刚回灌就被 45s TTL 判为过期
// 而在下一轮刷新前又变回「加载中…」。
// [参数] authID：accountCache 的键（auth.ID）；physJSON：物理 auth 文件原文。
// [返回] true 表示确实回灌了一条。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func seedCreditsCacheFromDisk(authID string, physJSON []byte) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	if v, ok := accountCache.Load(authID); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil {
			return false
		}
	}
	cr := parseCreditsSnapshotFromAuthJSON(physJSON)
	if cr == nil {
		return false
	}
	accountCache.Store(authID, &accountCacheEntry{credits: cr, fetched: time.Now()})
	return true
}

// loadCreditsFromDisk 遍历全部本插件 auth 文件，回灌内存与 accountCache。
//
// 在 watchdog 启动时调用一次（紧跟 loadCountersFromDisk），让面板在
// 「打开即刷」的后台刷新完成之前就有数据可显示。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func loadCreditsFromDisk() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		sa, phys, err := hostAuthGetBundle(f.AuthIndex)
		if err != nil || sa == nil || phys == nil {
			continue
		}
		uid := strings.TrimSpace(sa.Account.UID)
		if uid != "" {
			ensureCreditsPersistLoaded(uid, phys.JSON)
		}
		seedCreditsCacheFromDisk(f.ID, phys.JSON)
	}
}

// parseCreditsSnapshotFromAuthJSON 读取顶层 credits_snapshot。
//
// 缺失、格式错误或内容为空账本（三项全 0 且无包）一律返回 nil：空快照与
// 「余额未知」在面板上是两种不同展示，不能把空对象当成有效数据回灌。
// [参数] raw：物理 auth 文件原文。
// [返回] 解析成功的积分快照；不可用时返回 nil。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func parseCreditsSnapshotFromAuthJSON(raw []byte) *creditsSummary {
	if len(raw) == 0 {
		return nil
	}
	var m struct {
		Snapshot *creditsSummary `json:"credits_snapshot"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Snapshot == nil {
		return nil
	}
	cr := m.Snapshot
	if cr.TotalRemain == 0 && cr.TotalUsed == 0 && cr.TotalSize == 0 && len(cr.Packages) == 0 {
		return nil
	}
	return cr
}

// flushCreditsSnapshots 把内存里尚未落盘的积分快照写进物理 auth 文件。
//
// 与 flushCounters 同一节奏（watchdog tick，默认 10 分钟）调用。幂等：
// 成功后才推进 persistedSerial，失败保持原值以便下一轮重试。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func flushCreditsSnapshots() {
	type item struct {
		uid string
		cr  *creditsSummary
	}
	creditsPersistMu.Lock()
	items := make([]item, 0, len(creditsPersistEntries))
	serials := make(map[string]uint64, len(creditsPersistEntries))
	for uid, e := range creditsPersistEntries {
		if e.cr == nil || e.serial == e.persistedSerial {
			continue
		}
		items = append(items, item{uid: uid, cr: e.cr})
		serials[uid] = e.serial
	}
	creditsPersistMu.Unlock()

	for _, it := range items {
		if err := persistCreditsSnapshot(it.uid, it.cr); err != nil {
			continue
		}
		creditsPersistMu.Lock()
		e := creditsPersistEntries[it.uid]
		e.persistedSerial = serials[it.uid]
		creditsPersistEntries[it.uid] = e
		creditsPersistMu.Unlock()
	}
}

// persistCreditsSnapshot 把一份积分快照写入账号物理 auth 文件。
//
// 走 persistAuthDirect（不是 host.auth.save）：后者会按宿主类型重建记录，
// 丢掉它不认识的顶层键——与 test_failed / 计数器 / failover 标记同一条红线。
// [参数] uid：账号 UID；cr：待写入的积分快照。
// [返回] 写盘失败时返回错误，供调用方保留待写状态。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func persistCreditsSnapshot(uid string, cr *creditsSummary) error {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return fmt.Errorf("credits persist: empty uid")
	}
	if cr == nil {
		return fmt.Errorf("credits persist: nil snapshot for uid %s", uid)
	}
	authIndex, _ := resolveAuthIndexAndID(uid)
	if authIndex == "" {
		return fmt.Errorf("credits persist: no auth_index for uid %s", uid)
	}
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	if phys == nil || len(phys.JSON) == 0 {
		return errAuthMissing()
	}
	raw := foldCreditsSnapshotIntoDoc(phys.JSON, cr)
	name := phys.Name
	if name == "" {
		name = "workbuddy-" + uid + ".json"
	}
	return persistAuthDirect(name, phys.Path, "", raw)
}

// foldCreditsSnapshotIntoDoc 把积分快照并入顶层 auth JSON，保留其余所有键。
//
// 抽成纯函数以便单测：cgo shim 下无法驱动 host.auth.get 这条 RPC。
// [参数] base：物理 auth JSON 原文；cr：待写入的快照。
// [返回] 合并后的 JSON；序列化失败时原样返回 base（宁可不写也不写坏）。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func foldCreditsSnapshotIntoDoc(base []byte, cr *creditsSummary) []byte {
	if cr == nil {
		return base
	}
	var doc map[string]any
	if json.Unmarshal(base, &doc) != nil || doc == nil {
		// 与计数器一致：容忍坏 JSON，折进一份新文档。
		doc = map[string]any{}
	}
	doc[creditsSnapshotKey] = cr
	raw, err := json.Marshal(doc)
	if err != nil {
		return base
	}
	return raw
}

// creditsSnapshotForAuthID 返回该账号可用的最新积分快照：内存优先，其次盘上。
//
// 面板 / accounts 在没有实时缓存时用它兜底，使重启后首屏立刻有数字。
// [参数] authID：accountCache 键；uid：账号 UID，可为空；physJSON：物理文件原文。
// [返回] 可用的积分快照；都没有时返回 nil。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func creditsSnapshotForAuthID(authID, uid string, physJSON []byte) *creditsSummary {
	if v, ok := accountCache.Load(authID); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil {
			return e.credits
		}
	}
	if cr := parseCreditsSnapshotFromAuthJSON(physJSON); cr != nil {
		return cr
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return nil
	}
	creditsPersistMu.Lock()
	defer creditsPersistMu.Unlock()
	return creditsPersistEntries[uid].cr
}

// countCreditsPersistPending 返回尚未落盘的账号数，供日志与测试观测。
// [参数] 无。
// [返回] 待落盘账号数量。
// 最近修改时间：2026-10-24；改动原因：新增积分持久化。
func countCreditsPersistPending() int {
	creditsPersistMu.Lock()
	defer creditsPersistMu.Unlock()
	n := 0
	for _, e := range creditsPersistEntries {
		if e.cr != nil && e.serial != e.persistedSerial {
			n++
		}
	}
	return n
}
