// failover_disable.go 把「账号上游失败」直接落成宿主认识的禁用状态：失败即写盘
// `disabled: true`，宿主文件 watcher 毫秒级感知并停止把新请求路由到该账号；插件
// 每 10 分钟串行遍历被本机制禁用的账号，只在失败时记住的那一个模型上探活，探通才
// 写回 `disabled: false` 恢复流量。
//
// 与内存冷却的关系（2026-10-21 用户裁定为双写）：accountFailover.go 的阶梯冷却
// 仍然保留，但职责收窄为两段禁用覆盖不到的地方——写盘到 watcher 生效之间的毫秒级
// 窗口，以及写盘失败（宿主 RPC 不通、auth_index 解析不到）时唯一的排除手段。
// 冷却探活（原 cooldown_probe.go）已移除：冷却只按时间衰减、不再到期前回放，
// 「是否恢复」统一由本文件的 10 分钟探活裁决。
//
// 为什么必须有禁用这一层：冷却只存在插件内存里，宿主选择器看不见（宿主只读
// `Auth.Disabled` / `Status` / `ModelStates`）。于是同一个请求内，宿主仍会把这个
// 已知的坏号发给插件、拿一个 400 再换下一个号，生产实测单请求连试 14 个账号。
// 禁用写盘走的是宿主的路由判据本身，同请求轮转和跨请求路由一起被挡住。
//
// 与既有 `manual_disable` / `exhausted_disable` 的边界（本文件最重要的约束）：
// 本机制只认领、只解除自己写的 `failover_disable` 标记。用户手动禁用（manual）
// 由用户自己恢复；额度耗尽禁用（exhausted）已随「零积分可用」口径废弃，仅由
// lifecycle 一次性解禁历史遗留。本文件既不覆盖它们的 disabled，也绝不替它们解除。
//
// 落盘一律走「读物理文件 → 改顶层键 → 整体写回」，不重建文档，也不用
// host.auth.save：前者保留盘上所有既有顶层键，后者会重建记录。
//
// 最近修改时间：2026-10-21；改动原因：新增，并改为与阶梯冷却双写。
package main

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// failoverProbeInterval 是探活调度周期。用户裁定为固定 10 分钟：账号规模会到
	// 200 个量级，60 秒扫一轮会让上游每分钟承受一整轮 ping，且单轮串行本就跑不完。
	failoverProbeInterval = 10 * time.Minute

	// failoverConfirmTimeout 是写盘后等待宿主吸收该状态的时限。watcher 无去抖，
	// 正常毫秒级完成；超时说明宿主侧没读到这个状态（路径异常、文件不在监听目录、
	// 或该记录根本没有物理文件），必须告警——否则「写了就算禁了」会把失效隐藏起来。
	failoverConfirmTimeout = 3 * time.Second

	// failoverDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，model 是
	// 探活回放的目标（进程重启后盘上是唯一可靠来源），at 供面板与人工诊断。
	failoverDisableKey      = "failover_disable"
	failoverDisableModelKey = "failover_disable_model"
	failoverDisableAtKey    = "failover_disable_at"
)

var (
	// 宿主读写注入点：生产路径使用真实实现，单测替换为内存假宿主。
	// 与 active_ping.go 的 sendActivePingWorkbuddyFn 保持同一种注入形态。
	failoverNowFn          = time.Now
	failoverPingFn         = sendActivePingWorkbuddy
	failoverWaitConfirmed  = waitAuthDisabledState
	failoverListFn         = hostAuthList
	failoverGetPhysicalFn  = hostAuthGetPhysical
	failoverGetAuthFn      = hostAuthGet
	failoverResolveIndexFn = resolveAuthIndexAndID
	failoverPersistFn      = persistAuthDirect

	// failoverProbeModelMu 保护 failoverProbeModels 与 failoverDisabledLocal。
	failoverProbeModelMu sync.Mutex
	// failoverProbeModels 记录账号最近一次失败的模型，键为归一化账号标识。
	// 仅作内存加速：禁用期间同一个账号又换了个模型失败，盘上还留着旧模型，
	// 而最新故障才是探活要验证的对象。
	failoverProbeModels = make(map[string]string)
	// failoverDisabledLocal 记录「本进程写入过禁用」的账号，键含归一化标识与
	// auth_index 两种形态。它只是热路径的快速判据，不是真相源：真相源是物理文件，
	// 探活候选每轮从盘上枚举。用它替代查盘的原因是——若每次调度/成功都发一次
	// host.auth.get，会把 RPC 塞进每个请求的关键路径。
	//
	// 进程重启后本集合为空，但被禁用的账号此时已由宿主自己跳过，插件侧不需要
	// 再判一次；等探活把它恢复，集合始终为空，行为仍然正确。
	failoverDisabledLocal = make(map[string]struct{})
	// failoverDisableAtLocal 记录本进程写入禁用时的时刻，与 failoverDisabledLocal
	// 同键。宿主每次请求结束都会用内存 Metadata 重建 auth 文件并抹掉本机制的三个
	// 顶层键，重建标记时 at 已无法从盘上读到，只能从这里取。
	failoverDisableAtLocal = make(map[string]string)

	// failoverTickMu 是单轮探活的不重叠闸门。
	failoverTickMu sync.Mutex

	// failoverDocLocksMu 保护 failoverDocLocks 这张按物理文档分片的锁表。
	failoverDocLocksMu sync.Mutex
	// failoverDocLocks 按 auth_index 分片串行化「读盘 → 判状态 → 写盘」这段
	// 读改写。同一次失败会有多个记账入口（宿主 auth ID 形态与账号 UID 形态
	// 各一条），它们解析到同一个物理文件；不加锁就会各写一次，每写一次都触发
	// 宿主 watcher 增量重载。本地端到端实测：一个失败请求产生 2~3 次禁用写盘。
	failoverDocLocks = make(map[string]*sync.Mutex)
)

// markFailoverDisabledLocal 记录账号已被本进程禁用。
// [参数] authID：账号标识；authIndex：宿主 auth_index（可为空）。
// 最近修改时间：2026-10-21；改动原因：新增，供热路径快速判据。
func markFailoverDisabledLocal(authID, authIndex string) {
	markFailoverDisabledLocalAt(authID, authIndex, failoverNowFn().UTC().Format(time.RFC3339))
}

// markFailoverDisabledLocalAt 记录账号已被本进程禁用，并记下禁用时刻。
// [参数] authID：账号标识；authIndex：宿主 auth_index（可为空）；at：禁用时刻（RFC3339）。
// 最近修改时间：2026-10-25；改动原因：新增，供重建标记时取回 at。
func markFailoverDisabledLocalAt(authID, authIndex, at string) {
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	if key := failoverProbeKey(authID); key != "" {
		failoverDisabledLocal[key] = struct{}{}
		failoverDisableAtLocal[key] = at
	}
	if idx := strings.TrimSpace(authIndex); idx != "" {
		key := strings.ToLower(idx)
		failoverDisabledLocal[key] = struct{}{}
		failoverDisableAtLocal[key] = at
	}
}

// clearFailoverDisabledLocal 清除账号的本地禁用标记。
// [参数] authID：账号标识；authIndex：宿主 auth_index（可为空）。
// 最近修改时间：2026-10-21；改动原因：新增。
func clearFailoverDisabledLocal(authID, authIndex string) {
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	if key := failoverProbeKey(authID); key != "" {
		delete(failoverDisabledLocal, key)
		delete(failoverDisableAtLocal, key)
	}
	if idx := strings.TrimSpace(authIndex); idx != "" {
		key := strings.ToLower(idx)
		delete(failoverDisabledLocal, key)
		delete(failoverDisableAtLocal, key)
	}
}

// isFailoverDisabledLocal 判定账号是否被本进程禁用。
// [参数] authID：账号标识。
// [返回] true 表示本地记录为禁用。
// 最近修改时间：2026-10-21；改动原因：新增。
func isFailoverDisabledLocal(authID string) bool {
	key := failoverProbeKey(authID)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(authID))
	}
	if key == "" {
		return false
	}
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	_, ok := failoverDisabledLocal[key]
	return ok
}

// failoverDisableAtFor 读取本进程为该账号记录过的禁用时刻。
// [参数] authID：账号标识。
// [返回] RFC3339 时刻；无记录时为空串。
// 最近修改时间：2026-10-25；改动原因：新增，供重建标记。
func failoverDisableAtFor(authID string) string {
	key := failoverProbeKey(authID)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(authID))
	}
	if key == "" {
		return ""
	}
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	return failoverDisableAtLocal[key]
}

// restoreFailoverMarkersIntoDoc 在落盘前把本机制的三个顶层键补回文档。
//
// 为什么需要：宿主在每次请求结束后都会用内存里的 Metadata 重建 auth 文件，
// 而那份 Metadata 只含白名单键，本机制的三个键不在其中，于是「插件写禁 →
// 宿主 persist」之后盘上只剩 disabled:true。标记一旦丢失，探活循环就再也
// 找不到这个号，账号会停在「宿主禁用、插件不认领」的死角。
//
// 补标记只在「本进程写过禁用」且「盘上确实 disabled」时进行：前者保证不会替
// 用户手动禁用或额度耗尽禁用伪造归属，后者保证不会把已恢复的账号重新标成禁用。
//
// [参数] doc：待落盘的顶层键映射（原地修改）；authID：账号标识；model：失败模型。
// [返回] true 表示本次补写了标记。
// 最近修改时间：2026-10-25；改动原因：新增，堵住宿主 persist 抹标记的缺口。
func restoreFailoverMarkersIntoDoc(doc map[string]any, authID, model string) bool {
	if doc == nil || !isFailoverDisabledLocal(authID) {
		return false
	}
	if disabled, _ := doc["disabled"].(bool); !disabled {
		return false
	}
	if doc[failoverDisableKey] == true {
		return false
	}
	at := failoverDisableAtFor(authID)
	if at == "" {
		at = failoverNowFn().UTC().Format(time.RFC3339)
	}
	doc[failoverDisableKey] = true
	doc[failoverDisableModelKey] = strings.TrimSpace(model)
	doc[failoverDisableAtKey] = at
	return true
}

// failoverAuthDoc 是物理 auth 文档里与本机制相关的顶层状态快照。
type failoverAuthDoc struct {
	// disabled 是宿主路由判据读取的顶层 disabled 字段。
	disabled bool
	// failoverDisable 是本机制的所有权标记。
	failoverDisable bool
	// failoverModel 是禁用时写入的失败模型名。
	failoverModel string
	// manualDisable 是用户的禁用意图，恢复时必须让位，绝不解除。
	manualDisable bool
}

// parseFailoverAuthDoc 从物理 auth JSON 里取出本机制关心的顶层键。
// 解析失败按「全空」处理，让调用方走保守分支（不认领、不覆盖）。
// 额度耗尽禁用（exhausted_disable）无需单独识别：它不带本机制标记，
// 已被 doc.failoverDisable 这一道判据排除在外。
// [参数] raw：宿主返回的物理 auth JSON 原文。
// [返回] 顶层状态快照。
// 最近修改时间：2026-10-21；改动原因：新增。
func parseFailoverAuthDoc(raw []byte) failoverAuthDoc {
	var m struct {
		Disabled        bool   `json:"disabled"`
		FailoverDisable bool   `json:"failover_disable"`
		FailoverModel   string `json:"failover_disable_model"`
		ManualDisable   bool   `json:"manual_disable"`
	}
	_ = json.Unmarshal(raw, &m)
	return failoverAuthDoc{
		disabled:        m.Disabled,
		failoverDisable: m.FailoverDisable,
		failoverModel:   strings.TrimSpace(m.FailoverModel),
		manualDisable:   m.ManualDisable,
	}
}

// failoverProbeKey 把账号标识归一化成内存表的键。
// [参数] authID：账号标识。
// [返回] 归一化后的键；输入为空时返回空串。
// 最近修改时间：2026-10-21；改动原因：新增。
func failoverProbeKey(authID string) string {
	if key := normalizeFailoverKey(authID); key != "" {
		return key
	}
	return strings.TrimSpace(authID)
}

// rememberFailoverProbeModel 记录账号最近一次失败的模型，供探活回放。
// [参数] authID：账号标识（可为 UID / auth.ID / auth_index 任一形态）；
// model：已归一化的上游模型名，空串忽略。
// 最近修改时间：2026-10-21；改动原因：新增。
func rememberFailoverProbeModel(authID, model string) {
	model = strings.TrimSpace(model)
	key := failoverProbeKey(authID)
	if model == "" || key == "" {
		return
	}
	failoverProbeModelMu.Lock()
	failoverProbeModels[key] = model
	failoverProbeModelMu.Unlock()
}

// failoverProbeModelFor 读取账号内存里的最近失败模型。
// [参数] authID：账号标识。
// [返回] 模型名；无记录时为空串。
// 最近修改时间：2026-10-21；改动原因：新增。
func failoverProbeModelFor(authID string) string {
	key := failoverProbeKey(authID)
	if key == "" {
		return ""
	}
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	return failoverProbeModels[key]
}

// forgetFailoverProbeModel 清掉账号的内存失败模型。恢复启用后调用，
// 避免残留模型被后续禁用误用。
// [参数] authID：账号标识。
// 最近修改时间：2026-10-21；改动原因：新增。
func forgetFailoverProbeModel(authID string) {
	key := failoverProbeKey(authID)
	if key == "" {
		return
	}
	failoverProbeModelMu.Lock()
	delete(failoverProbeModels, key)
	failoverProbeModelMu.Unlock()
}

// carryFailoverKeysIntoMetadata 把宿主交给我们读取的那份文档里的本机制键，并入
// 我们要回传给宿主的 AuthData.Metadata。
//
// 为什么必须回传：宿主吸收 auth 文件后，用它拿到的 StorageJSON + Metadata 反向
// 重建物理文档（auth_provider.go mergedStorageJSON / filestore SaveTokenToFile）。
// 那份 Metadata 来自本插件 auth.parse 的白名单（type/provider/logo/note/disabled），
// 本机制的键不在其中，于是宿主每次回存都会把它们抹掉；disabled 因为在白名单里反而
// 存活，结果账号停在「宿主永久禁用、探活却再也找不到标记」的死角。本地端到端实测
// 到该丢失（禁用写盘成功且被宿主吸收，数分钟后盘上只剩 disabled:true）。
//
// [参数] meta：待补全的 Metadata，可为 nil（此时不做任何事）；source：宿主交来的
// 文档，接受物理 JSON 原文（[]byte）或其 Metadata 映射。
// 最近修改时间：2026-10-22；改动原因：新增，让标记经宿主回存通道存活。
func carryFailoverKeysIntoMetadata(meta map[string]any, source any) {
	if meta == nil {
		return
	}
	var doc map[string]any
	switch v := source.(type) {
	case []byte:
		if err := json.Unmarshal(v, &doc); err != nil {
			return
		}
	case map[string]any:
		doc = v
	default:
		return
	}
	for key, value := range failoverMarkersFromMap(doc) {
		meta[key] = value
	}
}

// failoverMarkersFromMap 从任意 map（盘上 JSON 解析结果、宿主回传的 Metadata）里
// 取出本机制拥有的顶层键。
//
// [参数] doc：键值映射，可为 nil。
// [返回] 需要搬运的键值；不带本机制标记时返回 nil。
// 最近修改时间：2026-10-22；改动原因：抽出自 JSON 与 Metadata 共用的取键逻辑。
func failoverMarkersFromMap(doc map[string]any) map[string]any {
	if doc == nil || doc[failoverDisableKey] != true {
		return nil
	}
	out := map[string]any{failoverDisableKey: true}
	for _, k := range []string{failoverDisableModelKey, failoverDisableAtKey} {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	return out
}

// failoverMarkersFromJSON 取出盘上已有的本机制顶层键，供白名单重建文档时搬运。
//
// 为什么必须搬运：lifecycle 的 syncAuthNote 走 buildAuthFileJSON
// （白名单重建，只输出固定几个键 + extra）。若账号正被本机制禁用，重建会把
// failover_disable 抹掉——号仍是 disabled，但探活循环按标记挑候选，从此再也找不到
// 它，账号就永久卡在禁用态（静默遗忘）。搬运这三个键是唯一防线。
//
// [参数] raw：物理 auth JSON 原文。
// [返回] 需要并入 extra 的键值；账号不属于本机制时返回 nil。
// 最近修改时间：2026-10-21；改动原因：新增，防止整文件重建抹掉禁用标记。
func failoverMarkersFromJSON(raw []byte) map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return failoverMarkersFromMap(doc)
}

// putFailoverMarkersIntoExtra 把盘上已有的本机制键并入 extra 映射。
//
// 同时搬运积分快照 credits_snapshot：它虽然不属于本机制，但同样只能靠这条
// 路径逃过白名单重建。buildAuthFileJSON 只输出固定键 + extra，任何一次备注
// 刷新都会把快照抹掉；而备注刷新是 4 小时巡检的常规动作，等于「刚持久化的
// 积分很快又丢了」。把两类键统一在这里搬运，避免每个调用点各记一份清单。
// [参数] extra：待补全的映射，可为 nil；raw：物理 auth JSON 原文。
// [返回] 合并后的映射（无可搬运键时原样返回 extra）。
// 最近修改时间：2026-10-24；改动原因：积分快照持久化需要同样的白名单搬运。
func putFailoverMarkersIntoExtra(extra map[string]any, raw []byte) map[string]any {
	markers := failoverMarkersFromJSON(raw)
	snapshot := creditsSnapshotRawFromJSON(raw)
	if len(markers) == 0 && snapshot == nil {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, len(markers)+1)
	}
	for k, v := range markers {
		extra[k] = v
	}
	if snapshot != nil {
		extra[creditsSnapshotKey] = snapshot
	}
	return extra
}

// creditsSnapshotRawFromJSON 取出盘上的积分快照原始值，供白名单重建搬运。
//
// 返回 any 而非 *creditsSummary：重建时按原样透传，避免中间再序列化一遍造成
// 字段漂移（例如 omitempty 让空包列表消失）。
// [参数] raw：物理 auth JSON 原文。
// [返回] 快照的原始 JSON 值；不存在时返回 nil。
// 最近修改时间：2026-10-24；改动原因：新增，配合积分快照持久化。
func creditsSnapshotRawFromJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return doc[creditsSnapshotKey]
}

// confirmFailoverHostState 轮询宿主内存状态，确认这次写盘真的被吸收。
//
// 必须由调用方异步发起：宿主 watcher 正常是毫秒级，但 `host.auth.list` 一次 RPC
// 的代价与最长 3 秒的轮询绝不能压在正在失败的请求上——那会让「禁用」反而拖慢
// 用户等待。这里只做一件事：把「写了但宿主没读到」这一静默失效暴露到日志。
//
// wait 由调用方在派生子 goroutine 之前读取并传入，而不是在子 goroutine 里读全局
// 注入变量：否则单测在 Cleanup 里还原该变量时，会与仍在轮询的子 goroutine 形成
// 数据竞争（`go test -race` 实测报出）。
//
// [参数] wait：宿主状态确认函数；authIndex：宿主 auth_index；want：期望的 disabled 状态；
// authID、path：仅用于日志定位。
// [返回] true 表示宿主内存状态已与写入一致。
// 最近修改时间：2026-10-21；改动原因：注入函数改为参数传入，消除子 goroutine 的数据竞争。
func confirmFailoverHostState(wait func(string, bool, time.Duration) bool, authIndex string, want bool, authID, path string) bool {
	if !wait(authIndex, want, failoverConfirmTimeout) {
		log.Printf("[workbuddy] failover state NOT confirmed by host: want_disabled=%v auth=%s path=%s",
			want, authID, path)
		return false
	}
	return true
}

// failoverDocLock 取出（并按需创建）某个物理文档的读改写串行锁。
//
// 键用 auth_index 而不是账号标识：同一账号在宿主里可能以多种身份形态出现
// （文件名 ID、UID、auth_index），只有 auth_index 能唯一指向同一个物理文件。
//
// [参数] authIndex：宿主 auth_index，调用方保证非空。
// [返回] 该文档专属的互斥锁。
// 最近修改时间：2026-10-22；改动原因：新增，消除同一次失败的多入口重复写盘。
func failoverDocLock(authIndex string) *sync.Mutex {
	failoverDocLocksMu.Lock()
	defer failoverDocLocksMu.Unlock()
	lock, ok := failoverDocLocks[authIndex]
	if !ok {
		lock = &sync.Mutex{}
		failoverDocLocks[authIndex] = lock
	}
	return lock
}

// disableAccountForFailover 把一次账号上游失败落成宿主可见的禁用状态。
//
// 幂等与认领规则（本函数的核心约束）：
//   - 盘上已 disabled：不再写盘。带本机制标记视为幂等成功；不带标记说明是
//     用户手动禁用或额度耗尽禁用，本机制不认领、不接管其恢复，返回 false。
//   - 盘上 enabled：写入 disabled + 三个自有键，一次写盘。
//
// 无论是否写盘，都刷新内存里的失败模型，让探活回放最新故障模型。
//
// [参数] authID：失败账号标识；model：已归一化的失败模型名，可为空。
// [返回] true 表示该账号当前处于本机制的禁用管辖之下。
// 最近修改时间：2026-10-22；改动原因：按物理文档加锁，消除同一次失败的多入口重复写盘。
func disableAccountForFailover(authID, model string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" || !failoverActive() {
		return false
	}
	rememberFailoverProbeModel(authID, model)

	authIndex, _ := failoverResolveIndexFn(authID)
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		log.Printf("[workbuddy] failover disable skipped: no auth_index for auth=%s", authID)
		return false
	}
	// 锁必须罩住「读盘 → 判 disabled → 写盘」整段：这三步之间只要让出，第二个
	// 记账入口就会读到同一份未禁用文档并再写一次，每写一次都触发宿主 reload。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		log.Printf("[workbuddy] failover disable skipped: no physical auth doc for auth=%s", authID)
		return false
	}
	doc := parseFailoverAuthDoc(phys.JSON)
	if doc.disabled {
		// 已被禁用：谁的禁用谁负责。只有本机制的标记才认领；
		// manual / exhausted 一律不接管，避免抢走别人的恢复职责。
		return doc.failoverDisable
	}

	var rawDoc map[string]any
	if err := json.Unmarshal(phys.JSON, &rawDoc); err != nil || rawDoc == nil {
		log.Printf("[workbuddy] failover disable skipped: unparsable auth doc for auth=%s", authID)
		return false
	}
	// 逐键改盘上现文档，不重建：盘上任何既有顶层键（note / 计数 / 其它标记）都原样保留。
	at := failoverNowFn().UTC().Format(time.RFC3339)
	rawDoc["disabled"] = true
	rawDoc[failoverDisableKey] = true
	rawDoc[failoverDisableModelKey] = strings.TrimSpace(model)
	rawDoc[failoverDisableAtKey] = at
	raw, err := json.Marshal(rawDoc)
	if err != nil {
		log.Printf("[workbuddy] failover disable skipped: marshal auth doc: %v", err)
		return false
	}
	// 优先经宿主管理 API 写进路由真相源（MySQL）；未配置密钥或调用失败时回落到
	// 直写物理文件，保证排除不会完全失效。
	apiErr := setHostAuthDisabled(phys.Name, authIndex, true)
	if apiErr == nil {
		markFailoverDisabledLocalAt(authID, authIndex, at)
		log.Printf("[workbuddy] failover disabled account via cpa api: auth=%s model=%s", authID, strings.TrimSpace(model))
		return true
	}
	if !errors.Is(apiErr, errCPAManagementNotConfigured) {
		log.Printf("[workbuddy] cpa disable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
	}
	if err := failoverPersistFn(phys.Name, phys.Path, "", raw); err != nil {
		log.Printf("[workbuddy] failover disable write failed: auth=%s err=%v", authID, err)
		return false
	}
	markFailoverDisabledLocalAt(authID, authIndex, at)
	wait := failoverWaitConfirmed
	go confirmFailoverHostState(wait, authIndex, true, authID, phys.Path)
	log.Printf("[workbuddy] failover disabled account: auth=%s model=%s", authID, strings.TrimSpace(model))
	return true
}

// reenableAccountForFailover 解除本机制写入的禁用，让账号重新接流量。
//
// 只认自己的标记：盘上没有 failover_disable 就一律不动，manual_disable 更不碰——
// 用户手动禁用的账号必须由用户自己启用。
//
// [参数] authIndex：宿主 auth_index，空串时按 authID 反查；authID：账号标识。
// [返回] true 表示已解除禁用。
// 最近修改时间：2026-10-22；改动原因：与禁用共用文档锁，避免恢复与再禁用交错写盘。
func reenableAccountForFailover(authIndex, authID string) bool {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		resolved, _ := failoverResolveIndexFn(authID)
		authIndex = strings.TrimSpace(resolved)
	}
	if authIndex == "" {
		return false
	}
	// 与 disableAccountForFailover 同一把文档锁：恢复读盘与写盘之间若让出，
	// 期间一个新失败可能已经写进 disabled，随后被这里覆盖回 false。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return false
	}
	doc := parseFailoverAuthDoc(phys.JSON)
	if !doc.failoverDisable || doc.manualDisable {
		// 不是本机制禁的，或用户已表达禁用意图：绝不解除他人的禁用。
		return false
	}

	var rawDoc map[string]any
	if err := json.Unmarshal(phys.JSON, &rawDoc); err != nil || rawDoc == nil {
		return false
	}
	rawDoc["disabled"] = false
	delete(rawDoc, failoverDisableKey)
	delete(rawDoc, failoverDisableModelKey)
	delete(rawDoc, failoverDisableAtKey)
	raw, err := json.Marshal(rawDoc)
	if err != nil {
		return false
	}
	// 恢复同样优先走宿主管理 API：宿主真相源里的 disabled 不置回 false，
	// 宿主会继续把这个号挡在路由外，插件侧解除就成了空转。
	apiErr := setHostAuthDisabled(phys.Name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[workbuddy] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := failoverPersistFn(phys.Name, phys.Path, "", raw); err != nil {
			log.Printf("[workbuddy] failover re-enable write failed: auth=%s err=%v", authID, err)
			return false
		}
	}
	// 解除禁用必须同时清掉内存冷却：双写下两侧是同一个故障的两份记录，只解一边
	// 会得到「盘上可用、插件仍排除」的半恢复账号——宿主会把请求路由过来，但
	// scheduler.pick 与同请求换号仍按冷却跳过它，等于这个号回来了却接不到流量。
	// 锁顺序：本函数不持 failoverMu，resetAccountFailover 才能安全取锁。
	// 国内版此处调用的 resetCooldownState 与本函数语义完全一致（同样清计数、
	// 窗口、失败模型与 modelStates），本插件直接复用 resetAccountFailover。
	resetAccountFailover(authID)
	forgetFailoverProbeModel(authID)
	clearFailoverDisabledLocal(authID, authIndex)
	wait := failoverWaitConfirmed
	go confirmFailoverHostState(wait, authIndex, false, authID, phys.Path)
	log.Printf("[workbuddy] failover re-enabled account: auth=%s", authID)
	return true
}

// failoverProbeCandidate 描述一个待探活的禁用账号。
type failoverProbeCandidate struct {
	authIndex string
	authID    string
	model     string
}

// failoverProbeTargets 从盘上枚举被本机制禁用的账号。
//
// 真相源是物理文件而不是内存集合：禁用状态本身就在盘上，插件热重载或进程重启后
// 不需要重建步骤就能继续把号探回来，也不会出现「内存记了、盘上没有」的双态。
// host.auth.list 会原样返回 disabled 条目（宿主只在 runtime-only 或文件已消失时
// 才隐藏），因此禁用中的账号仍在候选范围内。
//
// [返回] 待探活候选列表；无候选时返回 nil。
// 最近修改时间：2026-10-21；改动原因：新增，取代按内存冷却挑候选。
func failoverProbeTargets() []failoverProbeCandidate {
	files, err := failoverListFn()
	if err != nil {
		return nil
	}
	out := make([]failoverProbeCandidate, 0, len(files))
	for _, f := range files {
		authIndex := strings.TrimSpace(f.AuthIndex)
		if !f.Disabled || authIndex == "" {
			continue
		}
		phys, errGet := failoverGetPhysicalFn(authIndex)
		if errGet != nil || phys == nil || len(phys.JSON) == 0 {
			continue
		}
		doc := parseFailoverAuthDoc(phys.JSON)
		if !doc.failoverDisable {
			continue
		}
		model := failoverProbeModelFor(f.ID)
		if model == "" {
			model = doc.failoverModel
		}
		out = append(out, failoverProbeCandidate{authIndex: authIndex, authID: f.ID, model: model})
	}
	return out
}

// resolveFailoverAuth 把候选的 auth_index 解析为可用的账号凭据。
// [参数] authIndex：宿主 auth_index。
// [返回] 账号凭据；解析失败返回 nil。
// 最近修改时间：2026-10-21；改动原因：新增。
func resolveFailoverAuth(authIndex string) *storedAuth {
	sa, err := failoverGetAuthFn(authIndex)
	if err != nil {
		return nil
	}
	return sa
}

// runFailoverProbeTick 执行一轮禁用账号探活：串行遍历账号，每个账号只回放当初
// 失败的那一个模型，该模型探通才解除禁用。
//
// 为什么只探失败模型（2026-10-22 用户裁定）：每个账号的每个模型额度彼此独立，
// 在别的模型上探通不能证明失败模型已恢复。全量轮测会让账号带着仍然坏着的模型
// 回到路由，下一个请求照样失败——只是把故障推迟到下一次请求去发现，还白烧了
// 其它模型的额度。
//
// 没有失败模型可回放（禁用文档里记了空模型）时跳过该账号：无法验证就不恢复，
// 保持禁用比盲目放回安全。这条日志必须显眼，否则该号会停在「禁用且永远探不了」
// 的死角，只能人工启用。
//
// 探活失败的账号不写任何盘、不递增任何计数，保持禁用等下一轮——禁用状态本身
// 就是「不可用」的完整表达，再写一次只会平白触发宿主 reload。
//
// [参数] resolve：auth_index 到凭据的解析函数（测试注入）。
// [返回] 本轮实际探测的账号数。
// 最近修改时间：2026-10-22；改动原因：探活范围从全量模型任一成功收缩为仅失败模型。
func runFailoverProbeTick(resolve func(string) *storedAuth) int {
	targets := failoverProbeTargets()
	probed := 0
	for _, tc := range targets {
		sa := resolve(tc.authIndex)
		if sa == nil {
			continue
		}
		model := strings.TrimSpace(tc.model)
		if model == "" {
			log.Printf("[workbuddy] failover probe skipped: no failed model recorded for auth=%s", tc.authIndex)
			continue
		}
		probed++
		if err := failoverPingFn(sa, model); err != nil {
			log.Printf("[workbuddy] failover probe failed: auth=%s model=%s err=%s",
				tc.authIndex, model, truncateRedacted(err.Error(), 160))
			continue
		}
		log.Printf("[workbuddy] failover probe ok: auth=%s model=%s", tc.authIndex, model)
		reenableAccountForFailover(tc.authIndex, tc.authID)
	}
	return probed
}

// runFailoverTickNonOverlap 在不重叠的前提下执行一轮探活。
//
// 上一轮还在探测时 TryLock 失败，本轮直接丢弃而不是排队——堆积轮次只会让同一批
// 账号被重复探测。真正的探测在子 goroutine 里跑，好让 ticker 自身不阻塞。
//
// [参数] tick：本轮要执行的动作。
// [返回] true 表示本轮已启动，false 表示因上一轮未结束而被跳过。
// 最近修改时间：2026-10-21；改动原因：新增，把用户裁定的「立即跳过」变成可测函数。
func runFailoverTickNonOverlap(tick func()) bool {
	if !failoverTickMu.TryLock() {
		return false
	}
	go func() {
		defer failoverTickMu.Unlock()
		tick()
	}()
	return true
}

// reconcileFailoverLocalSet 把本地禁用集合与盘上真相核对一轮，清掉已被外部启用的号。
//
// 为什么必须做：本地集合只在「本进程写的恢复」时清除，但启用不止这一条路——用户
// 在 CPA 面板启用、宿主 UI 改 disabled:false、lifecycle 重建文档，
// 都不会经过 reenableAccountForFailover。那些号在宿主侧已经可路由，插件内存里却
// 仍判它坏，于是调度与入口前置拦截会永久跳过它，直到插件重启（静默遗忘的镜像形态）。
//
// 判据只认「盘上明确为 enabled 的号」：列表里没有的键（账号已删除、RPC 未覆盖）
// 一律保留，避免用「查不到」当成「已恢复」。反过来若我们的写盘尚未落定而列表仍显
// enabled，这里清掉也只是让下一个请求重新失败一次、重新触发禁用——错杀可自愈，
// 漏杀会把号永久挡在路由外，所以取保守方向。
//
// [返回] 本轮清除的键数（仅供测试与日志观察）。
// 最近修改时间：2026-10-21；改动原因：新增，防止外部启用后本地排除集合卡死。
func reconcileFailoverLocalSet() int {
	files, err := failoverListFn()
	if err != nil {
		// 列表不可用时什么都不清：此时「查不到」不代表「已恢复」。
		return 0
	}
	enabled := make(map[string]struct{})
	for _, f := range files {
		if f.Disabled {
			continue
		}
		for _, raw := range []string{f.ID, f.Name, f.AuthIndex} {
			key := strings.ToLower(strings.TrimSpace(raw))
			if key == "" {
				continue
			}
			// auth_index 保持原样即可命中；文件名/ID 还要再过一次归一化，
			// 才能对上 markFailoverDisabledLocal 写入的裸 UID 形态。
			enabled[key] = struct{}{}
			if norm := normalizeFailoverKey(raw); norm != "" {
				enabled[norm] = struct{}{}
			}
		}
	}
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	cleared := 0
	for key := range failoverDisabledLocal {
		if _, ok := enabled[key]; !ok {
			continue
		}
		delete(failoverDisabledLocal, key)
		delete(failoverProbeModels, key)
		cleared++
	}
	return cleared
}

// failoverProbeLoop 禁用账号探活的常驻循环。
//
// 固定 10 分钟一轮；单轮串行遍历，可能超过一轮周期（候选数 × 单账号 ping 超时），
// 期间后续 tick 会被 runFailoverTickNonOverlap 逐个跳过，等效探活间隔随禁用号数
// 线性劣化——这是用户裁定的调度形态，不额外加整轮预算。
//
// 周期从 60 秒放宽到 10 分钟是因为账号规模会到 200 个：60 秒一扫会让上游每分钟
// 承受整轮 ping，而单轮本来就跑不完，只是把请求堆在 TryLock 上被丢弃。
//
// 最近修改时间：2026-10-22；改动原因：周期由 60 秒改为 10 分钟（账号规模 200 级）。
func failoverProbeLoop() {
	// init() 早于宿主装配，立即 tick 会静默失败，与 watchdog 一致先等宿主就绪。
	waitHostReadyForWatchdog(watchdogStartupWait, hostReadyForWatchdog)
	ticker := time.NewTicker(failoverProbeInterval)
	defer ticker.Stop()
	for range ticker.C {
		runFailoverTickNonOverlap(func() {
			// 先对账本地集合：外部（面板 / 宿主 UI / lifecycle）已把号启用时，
			// 必须让插件不再排除它，否则它会一直被跳过直到重启。
			reconcileFailoverLocalSet()
			runFailoverProbeTick(resolveFailoverAuth)
		})
	}
}
