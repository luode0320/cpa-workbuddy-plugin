// failover_disable.go 把「账号上游失败」直接落成宿主认识的禁用状态：失败即写盘
// `disabled: true`，宿主文件 watcher 毫秒级感知并停止把新请求路由到该账号；插件
// 每 10 分钟串行遍历被本机制禁用的账号，只在失败时记住的那一个模型上探活，探通才
// 写回 `disabled: false` 恢复流量。
//
// 与内存冷却的关系（2026-10-21 用户裁定为双写）：accountFailover.go 的阶梯冷却
// 仍然保留，但职责收窄为两段禁用覆盖不到的地方——写盘到 watcher 生效之间的毫秒级
// 窗口，以及写盘失败（宿主 RPC 不通、auth_index 解析不到）时唯一的排除手段。
//
// 为什么必须有禁用这一层：冷却只存在插件内存里，宿主选择器看不见（宿主只读
// `Auth.Disabled` / `Status` / `ModelStates`）。于是同一个请求内，宿主仍会把这个
// 已知的坏号发给插件、拿一次失败再换下一个号；禁用写盘走的是宿主的路由判据本身，
// 同请求轮转和跨请求路由一起被挡住。
//
// Qoder 专属红线（用户裁定，本文件最重要的业务约束）：
//   - 一个账号累计探活失败达到 failoverProbeFailLimit 次，当日不再探活，
//     本地时间 0 点后才恢复——这是对上游「日调用上限」的让位，避免探活本身
//     把账号的每日配额烧光。
//   - 排队帧（isQueueFrame）不是账号故障，仍在记账入口被拦下，不会走到禁用。
//   - 110 日限额（isDailyCountExceeded）有独立的 24 小时持久化冷却
//     （daily_cooling），本机制不认领、也不解除它。
//
// 与既有 `manual_disable` / `exhausted_disable` 的边界：本机制只认领、只解除
// 自己写的 `failover_disable` 标记。用户手动禁用（manual）与额度耗尽自动禁用
// （exhausted）都由各自的流程负责，本文件既不覆盖它们的 disabled，也绝不替它们
// 解除禁用。
//
// 落盘一律走「读物理文件 → 改顶层键 → 整体写回」，不重建文档，也不用
// host.auth.save：前者保留盘上所有既有顶层键，后者会重建记录。
//
// 最近修改时间：2026-10-22；改动原因：按 workbuddy 样板新增，含 Qoder 当日探活上限。
package main

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// failoverProbeInterval 是探活调度周期。用户裁定为固定 10 分钟：账号规模会到
	// 200 个量级，60 秒扫一轮会让上游每分钟承受一整轮 ping，且单轮串行本就跑不完。
	failoverProbeInterval = 10 * time.Minute

	// failoverConfirmTimeout 是写盘后等待宿主吸收该状态的时限。watcher 无去抖，
	// 正常毫秒级完成；超时说明宿主侧没读到这个状态（路径异常、文件不在监听目录、
	// 或该记录根本没有物理文件），必须告警——否则「写了就算禁了」会把失效隐藏起来。
	failoverConfirmTimeout = 3 * time.Second

	// failoverProbeFailLimit 是单账号单日允许的探活失败上限。用户裁定为 10 次：
	// 达到后当日不再探活，本地时间 0 点才重新开始计数。上游对同账号有日调用
	// 配额，无上限探活会把账号烧成「探活自己把额度用光」。
	failoverProbeFailLimit = 10

	// failoverDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，model 是
	// 探活回放的目标（进程重启后盘上是唯一可靠来源），at 供面板与人工诊断。
	failoverDisableKey      = "failover_disable"
	failoverDisableModelKey = "failover_disable_model"
	failoverDisableAtKey    = "failover_disable_at"

	// failoverDisableBudgetRatio 是单轮允许禁用的池子占比上限。
	// 取 0.5：一半以上账号在短时间内被判坏，几乎只能是上游整体故障或分类失误，
	// 而不是这些账号同时出问题。触发即回滚，把账号放回路由。
	failoverDisableBudgetRatio = 0.5

	// failoverDisableRoundWindow 界定「同一轮」的时间跨度。禁用是秒级完成的事，
	// 30 分钟外的失败属于新一轮观察，不应被上一轮的计数拖累。
	failoverDisableRoundWindow = 30 * time.Minute

	// failoverDisableBudgetMinPool 是启用比例熔断的最小池子规模。
	// 池子太小时「禁用一半」是正常的部分账号故障，拦下来等于取消禁用机制；
	// 生产池子 27 个，熔断要防的是一次抖动打空整池。
	failoverDisableBudgetMinPool = 8
)

var (
	// 宿主读写注入点：生产路径使用真实实现，单测替换为内存假宿主。
	failoverNowFn          = time.Now
	failoverPingFn         = sendActivePingQoder
	failoverWaitConfirmed  = waitAuthDisabledState
	failoverListFn         = hostAuthList
	failoverGetPhysicalFn  = hostAuthGetPhysical
	failoverGetAuthFn      = hostAuthGet
	failoverResolveIndexFn = resolveAuthIndexAndID
	failoverPersistFn      = persistAuthDirect

	// failoverProbeModelMu 保护本文件的三张内存表。
	failoverProbeModelMu sync.Mutex
	// failoverProbeModels 记录账号最近一次失败的模型，键为归一化账号标识。
	// qoderwork 的冷却没有模型维度（单层阶梯），因此这个映射也是模型级信息
	// 的唯一来源；禁用到盘上的那份同时写进 failover_disable_model。
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
	// failoverProbeQuota 记录每个账号当日的探活失败次数与计数所属的本地日期。
	// 只在内存：进程重启后计数重置，等于给账号一次重新开始的机会——这比把
	// 「今日已放弃」落盘更保守，也不会让一次插件热重载永久放弃某个号。
	failoverProbeQuota = make(map[string]*failoverProbeDay)

	// failoverTickMu 是单轮探活的不重叠闸门。
	failoverTickMu sync.Mutex

	// failoverDocLocksMu 保护 failoverDocLocks 这张按物理文档分片的锁表。
	failoverDocLocksMu sync.Mutex
	// failoverDocLocks 按 auth_index 分片串行化「读盘 → 判状态 → 写盘」这段
	// 读改写。同一次失败会有多个记账入口（宿主 auth ID 形态与账号 UID 形态
	// 各一条），它们解析到同一个物理文件；不加锁就会各写一次，每写一次都触发
	// 宿主 watcher 增量重载。
	failoverDocLocks = make(map[string]*sync.Mutex)

	// failoverRoundMu 保护本轮禁用台账。
	failoverRoundMu sync.Mutex
	// failoverRoundDisabled 记录本轮由本机制禁用的账号（归一化键 → auth_index）。
	// 熔断回滚要靠它知道该撤销谁；窗口过期即整体作废，重新开始计数。
	failoverRoundDisabled = make(map[string]string)
	// failoverRoundStart 是本轮台账的起始时刻。
	failoverRoundStart time.Time
)

// failoverProbeDay 是一个账号在某个本地日期内的探活失败计数。
type failoverProbeDay struct {
	// day 是本地日期（YYYY-MM-DD）。与新日期不符时计数归零重来。
	day string
	// fails 是当日累计探活失败次数。
	fails int
}

// failoverLocalDay 返回给定时刻的本地日期标识。用本地时区而不是 UTC：
// 用户裁定的恢复点是「本地 0 点」，与账号持有者的作息一致。
// [参数] now：当前时刻。
// [返回] YYYY-MM-DD 形式的本地日期。
// 最近修改时间：2026-10-22；改动原因：新增，供当日探活配额跨天重置。
func failoverLocalDay(now time.Time) string {
	return now.Format("2006-01-02")
}

// failoverProbeFailsToday 读取账号当日的探活失败次数。
// 跨天后旧计数视为 0（并顺手清掉旧条目，避免表无限增长）。
// [参数] authID：账号标识；now：当前时刻。
// [返回] 当日累计失败次数。
// 最近修改时间：2026-10-22；改动原因：新增。
func failoverProbeFailsToday(authID string, now time.Time) int {
	key := failoverProbeKey(authID)
	if key == "" {
		return 0
	}
	today := failoverLocalDay(now)
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	st := failoverProbeQuota[key]
	if st == nil {
		return 0
	}
	if st.day != today {
		delete(failoverProbeQuota, key)
		return 0
	}
	return st.fails
}

// noteFailoverProbeFailure 记一次探活失败，返回累计次数。
// [参数] authID：账号标识；now：当前时刻。
// [返回] 当日累计失败次数（含本次）。
// 最近修改时间：2026-10-22；改动原因：新增，落用户裁定的当日 10 次上限。
func noteFailoverProbeFailure(authID string, now time.Time) int {
	key := failoverProbeKey(authID)
	if key == "" {
		return 0
	}
	today := failoverLocalDay(now)
	failoverProbeModelMu.Lock()
	defer failoverProbeModelMu.Unlock()
	st := failoverProbeQuota[key]
	if st == nil || st.day != today {
		st = &failoverProbeDay{day: today}
		failoverProbeQuota[key] = st
	}
	st.fails++
	return st.fails
}

// failoverProbeExhaustedToday 判定账号当日探活是否已用尽。
// [参数] authID：账号标识；now：当前时刻。
// [返回] true 表示达到当日上限，本次应跳过探测。
// 最近修改时间：2026-10-22；改动原因：新增。
func failoverProbeExhaustedToday(authID string, now time.Time) bool {
	return failoverProbeFailsToday(authID, now) >= failoverProbeFailLimit
}

// resetFailoverProbeQuota 清掉账号的当日探活计数。恢复成功时调用。
// [参数] authID：账号标识。
// 最近修改时间：2026-10-22；改动原因：新增。
func resetFailoverProbeQuota(authID string) {
	key := failoverProbeKey(authID)
	if key == "" {
		return
	}
	failoverProbeModelMu.Lock()
	delete(failoverProbeQuota, key)
	failoverProbeModelMu.Unlock()
}

// markFailoverDisabledLocal 记录账号已被本进程禁用。
// [参数] authID：账号标识；authIndex：宿主 auth_index（可为空）。
// 最近修改时间：2026-10-22；改动原因：新增，供热路径快速判据。
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
// 最近修改时间：2026-10-22；改动原因：新增。
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
// 最近修改时间：2026-10-22；改动原因：新增。
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
	// dailyCooling 是 110 日限额的 24 小时持久化冷却标记；带它的文档
	// 由 daily_cooldown.go 负责，本机制不认领。
	dailyCooling bool
}

// parseFailoverAuthDoc 从物理 auth JSON 里取出本机制关心的顶层键。
// 解析失败按「全空」处理，让调用方走保守分支（不认领、不覆盖）。
// 额度耗尽禁用（exhausted_disable）无需单独识别：它不带本机制标记，
// 已被 doc.failoverDisable 这一道判据排除在外。
// [参数] raw：宿主返回的物理 auth JSON 原文。
// [返回] 顶层状态快照。
// 最近修改时间：2026-10-22；改动原因：新增，并识别 Qoder 的日冷却标记。
func parseFailoverAuthDoc(raw []byte) failoverAuthDoc {
	var m struct {
		Disabled        bool   `json:"disabled"`
		FailoverDisable bool   `json:"failover_disable"`
		FailoverModel   string `json:"failover_disable_model"`
		ManualDisable   bool   `json:"manual_disable"`
		DailyCooling    string `json:"daily_cooling_until"`
	}
	_ = json.Unmarshal(raw, &m)
	return failoverAuthDoc{
		disabled:        m.Disabled,
		failoverDisable: m.FailoverDisable,
		failoverModel:   strings.TrimSpace(m.FailoverModel),
		manualDisable:   m.ManualDisable,
		dailyCooling:    strings.TrimSpace(m.DailyCooling) != "",
	}
}

// failoverProbeKey 把账号标识归一化成内存表的键。
// [参数] authID：账号标识。
// [返回] 归一化后的键；输入为空时返回空串。
// 最近修改时间：2026-10-22；改动原因：新增。
func failoverProbeKey(authID string) string {
	if key := normalizeFailoverKey(authID); key != "" {
		return key
	}
	return strings.TrimSpace(authID)
}

// rememberFailoverProbeModel 记录账号最近一次失败的模型，供探活回放。
// [参数] authID：账号标识（可为 UID / auth.ID / auth_index 任一形态）；
// model：已归一化的上游模型名，空串忽略。
// 最近修改时间：2026-10-22；改动原因：新增。
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
// 最近修改时间：2026-10-22；改动原因：新增。
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
// 最近修改时间：2026-10-22；改动原因：新增。
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
// 那份 Metadata 来自本插件 enrichAuthMetadata 的白名单（type/provider/logo/note/
// disabled），本机制的键不在其中，于是宿主每次回存都会把它们抹掉；disabled 因为
// 在白名单里反而存活，结果账号停在「宿主永久禁用、探活却再也找不到标记」的死角。
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
// 为什么必须搬运：buildAuthFileJSON 是白名单重建（只输出固定几个键 + extra）。
// qoderwork 的 reenableAuth / syncAuthNote / deleteAuth 都走它。若账号正被本机制
// 禁用，重建会把 failover_disable 抹掉——号仍是 disabled，但探活循环按标记挑
// 候选，从此再也找不到它，账号就永久卡在禁用态（静默遗忘）。搬运这三个键是唯一防线。
//
// [参数] raw：物理 auth JSON 原文。
// [返回] 需要并入 extra 的键值；账号不属于本机制时返回 nil。
// 最近修改时间：2026-10-22；改动原因：新增，防止整文件重建抹掉禁用标记。
func failoverMarkersFromJSON(raw []byte) map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return failoverMarkersFromMap(doc)
}

// putFailoverMarkersIntoExtra 把盘上已有的本机制键并入 extra 映射。
// [参数] extra：待补全的映射，可为 nil；raw：物理 auth JSON 原文。
// [返回] 合并后的映射（无标记时原样返回 extra）。
// 最近修改时间：2026-10-22；改动原因：新增。
func putFailoverMarkersIntoExtra(extra map[string]any, raw []byte) map[string]any {
	markers := failoverMarkersFromJSON(raw)
	if len(markers) == 0 {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, len(markers))
	}
	for k, v := range markers {
		extra[k] = v
	}
	return extra
}

// confirmFailoverHostState 轮询宿主内存状态，确认这次写盘真的被吸收。
//
// 必须由调用方异步发起：宿主 watcher 正常是毫秒级，但 `host.auth.list` 一次 RPC
// 的代价与最长 3 秒的轮询绝不能压在正在失败的请求上——那会让「禁用」反而拖慢
// 用户等待。这里只做一件事：把「写了但宿主没读到」这一静默失效暴露到日志。
//
// wait 由调用方在派生子 goroutine 之前读取并传入，而不是在子 goroutine 里读全局
// 注入变量：否则单测在 Cleanup 里还原该变量时，会与仍在轮询的子 goroutine 形成
// 数据竞争。
//
// [参数] wait：宿主状态确认函数；authIndex：宿主 auth_index；want：期望的 disabled 状态；
// authID、path：仅用于日志定位。
// [返回] true 表示宿主内存状态已与写入一致。
// 最近修改时间：2026-10-22；改动原因：新增，注入函数以参数传入避免子 goroutine 数据竞争。
func confirmFailoverHostState(wait func(string, bool, time.Duration) bool, authIndex string, want bool, authID, path string) bool {
	if !wait(authIndex, want, failoverConfirmTimeout) {
		log.Printf("[qoderwork] failover state NOT confirmed by host: want_disabled=%v auth=%s path=%s",
			want, authID, path)
		return false
	}
	return true
}

// noteFailoverDisableThisRound 把刚禁用的账号记进本轮台账。
// [参数] authID：账号标识；authIndex：宿主 auth_index。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，供熔断回滚定位对象。
func noteFailoverDisableThisRound(authID, authIndex string) {
	key := normalizeFailoverStateKey(authID)
	if key == "" {
		return
	}
	failoverRoundMu.Lock()
	defer failoverRoundMu.Unlock()
	now := time.Now()
	if failoverRoundStart.IsZero() || now.Sub(failoverRoundStart) > failoverDisableRoundWindow {
		failoverRoundDisabled = make(map[string]string)
		failoverRoundStart = now
	}
	failoverRoundDisabled[key] = strings.TrimSpace(authIndex)
}

// failoverDisableBudgetExceeded 判定再禁用这一个账号是否会击穿池子。
//
// 判据用「本轮已禁用数 + 1 >= 池子总数 × 比例」：真正要防的是「几乎全部账号
// 在短时间内一起被判坏」，用绝对数量而不是比例对小池子更稳健——3 个账号的池子
// 禁用 2 个就已经是整池失效。
//
// [参数] authID：即将被禁用的账号（已在本轮台账里则不计新增）。
// [返回] true 表示应触发熔断，拒绝本次禁用。
// 最近修改时间：2026-10-25；改动原因：新增。
func failoverDisableBudgetExceeded(authID string) bool {
	total := failoverRoutablePoolSize()
	if total <= 0 {
		return false
	}
	key := normalizeFailoverStateKey(authID)
	failoverRoundMu.Lock()
	// 窗口过期时台账作废，按本轮首次禁用处理。
	round := 0
	if !failoverRoundStart.IsZero() && time.Since(failoverRoundStart) <= failoverDisableRoundWindow {
		round = len(failoverRoundDisabled)
		if _, already := failoverRoundDisabled[key]; already {
			failoverRoundMu.Unlock()
			return false
		}
	}
	failoverRoundMu.Unlock()

	budget := int(float64(total) * failoverDisableBudgetRatio)
	if budget < 1 {
		budget = 1
	}
	// 小池子不做比例熔断：3~5 个号时「禁用 2 个」可能是真实的部分账号故障，
	// 而熔断会把它退化成「谁都不禁」，等于取消这套机制。生产池子是 27 个，
	// 熔断要防的是「一次抖动打空整池」，那才是真正的危险。
	if total < failoverDisableBudgetMinPool {
		return false
	}
	return round+1 > budget
}

// failoverRoutablePoolSize 统计当前可路由的 qoderwork 账号总数。
// [返回] 账号数；宿主列表不可用时返回 0（表示无法判定，调用方应按不熔断处理）。
// 最近修改时间：2026-10-25；改动原因：新增。
func failoverRoutablePoolSize() int {
	files, err := failoverListFn()
	if err != nil {
		return 0
	}
	count := 0
	for _, f := range files {
		if strings.TrimSpace(f.ID) == "" {
			continue
		}
		count++
	}
	return count
}

// rollbackFailoverDisablesThisRound 撤销本轮全部禁用并把账号放回路由。
//
// 触发时说明「短时间内大批账号被判坏」，这在真实账号故障里几乎不可能同时发生，
// 更可能是上游整体故障或分类失误。回滚比继续禁用更安全：宁可让账号带着故障
// 继续接流量（用户看到的是单次失败并可重试），也不要把整池打空（服务完全不可用）。
//
// [返回] 实际回滚的账号数。
// 最近修改时间：2026-10-25；改动原因：新增。
func rollbackFailoverDisablesThisRound() int {
	failoverRoundMu.Lock()
	pending := make(map[string]string, len(failoverRoundDisabled))
	for key, idx := range failoverRoundDisabled {
		pending[key] = idx
	}
	failoverRoundDisabled = make(map[string]string)
	failoverRoundStart = time.Time{}
	failoverRoundMu.Unlock()

	rolledBack := 0
	for key, authIndex := range pending {
		failoverStateClearDisabled(key)
		clearFailoverDisabledLocal(key, authIndex)
		forgetFailoverProbeModel(key)
		resetFailoverProbeQuota(key)
		resetCooldownState(key)
		rolledBack++
	}
	if rolledBack > 0 {
		log.Printf("[qoderwork] failover disable CIRCUIT BREAKER: rolled back %d disables in one round "+
			"(more than %.0f%% of the pool flagged within %v) — treating as upstream-wide failure",
			rolledBack, failoverDisableBudgetRatio*100, failoverDisableRoundWindow)
	}
	return rolledBack
}

// resetFailoverRoundForTest 清空本轮禁用台账，仅供测试使用。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，避免用例之间共享熔断计数。
func resetFailoverRoundForTest() {
	failoverRoundMu.Lock()
	failoverRoundDisabled = make(map[string]string)
	failoverRoundStart = time.Time{}
	failoverRoundMu.Unlock()
}

// expireFailoverRoundForTest 把本轮台账起点拨到窗口之外，仅供测试使用。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，验证窗口过期后计数归零。
func expireFailoverRoundForTest() {
	failoverRoundMu.Lock()
	if !failoverRoundStart.IsZero() {
		failoverRoundStart = time.Now().Add(-failoverDisableRoundWindow - time.Minute)
	}
	failoverRoundMu.Unlock()
}

// failoverDocLock 取出（并按需创建）某个物理文档的读改写串行锁。//
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
//     用户手动禁用、额度耗尽禁用或 110 日限额冷却，本机制不认领、不接管其恢复。
//   - 盘上 enabled：写入 disabled + 三个自有键，一次写盘。
//
// 无论是否写盘，都刷新内存里的失败模型，让探活回放最新故障模型。
//
// [参数] authID：失败账号标识；model：已归一化的失败模型名，可为空。
// [返回] true 表示该账号当前处于本机制的禁用管辖之下。
// 最近修改时间：2026-10-22；改动原因：新增，按物理文档加锁消除重复写盘。
func disableAccountForFailover(authID, model string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" || !failoverActive() {
		return false
	}
	rememberFailoverProbeModel(authID, model)

	authIndex, _ := failoverResolveIndexFn(authID)
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		// 解析不到 auth_index 说明宿主侧查不到这个账号（RPC 不通或账号已消失），
		// 此时不写禁用：禁用是为了让路由跳过它，而连身份都无法确认的账号
		// 本就不会被路由到，写一份无主禁用只会留下永远探不回来的死角。
		// 内存阶梯冷却仍会生效，覆盖短时故障。
		log.Printf("[qoderwork] failover disable skipped: no auth_index for auth=%s", authID)
		return false
	}

	// 池级熔断：禁用是「把账号从路由里摘掉」，它必须是小概率事件。
	// 当本轮已禁用的账号占可路由池达到 failoverDisableBudgetRatio 时，更可能的
	// 解释是上游整体故障或分类失误，继续禁用只会把整池打空——2026-10-25 生产
	// 实证正是如此：单请求连撞 9 个号全失败，全部禁用后另一个请求又撞上剩余账号。
	// 触发熔断时回滚本轮全部禁用，并保留内存冷却观察真实故障是否在扩散。
	if failoverDisableBudgetExceeded(authID) {
		rollbackFailoverDisablesThisRound()
		return false
	}

	// 先写插件自有 sidecar：它不经过宿主，是路由判据的真实来源。
	// 宿主侧写盘（disabled 字段）降级为尽力而为——宿主会按内存 Status 写回
	// 覆盖它、fsnotify 也会静默失效，因此绝不能再把「宿主认了」当作禁用成功。
	failoverStateMarkDisabled(authID, model)
	markFailoverDisabledLocal(authID, authIndex)
	noteFailoverDisableThisRound(authID, authIndex)

	// 锁必须罩住「读盘 → 判 disabled → 写盘」整段：这三步之间只要让出，第二个
	// 记账入口就会读到同一份未禁用文档并再写一次，每写一次都触发宿主 reload。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		log.Printf("[qoderwork] failover disabled via sidecar, host doc unavailable auth=%s", authID)
		return true
	}
	doc := parseFailoverAuthDoc(phys.JSON)
	if doc.disabled {
		// 已被禁用：谁的禁用谁负责。只有本机制的标记才认领；
		// manual / exhausted / daily_cooling 一律不接管，避免抢走别人的恢复职责。
		// 但 sidecar 已写下禁用，路由照样排除——这里只决定探活归属。
		return doc.failoverDisable
	}

	var rawDoc map[string]any
	if err := json.Unmarshal(phys.JSON, &rawDoc); err != nil || rawDoc == nil {
		log.Printf("[qoderwork] failover disable: sidecar written, auth doc unparsable auth=%s", authID)
		return true
	}
	// 逐键改盘上现文档，不重建：盘上任何既有顶层键（note / 计数 / 其它标记）都原样保留。
	at := failoverNowFn().UTC().Format(time.RFC3339)
	rawDoc["disabled"] = true
	rawDoc[failoverDisableKey] = true
	rawDoc[failoverDisableModelKey] = strings.TrimSpace(model)
	rawDoc[failoverDisableAtKey] = at
	raw, err := json.Marshal(rawDoc)
	if err != nil {
		log.Printf("[qoderwork] failover disable: sidecar written, marshal auth doc: %v", err)
		return true
	}
	// 优先经宿主管理 API 写进路由真相源（MySQL）；未配置密钥或调用失败时回落到
	// 直写物理文件，保证排除不会完全失效。
	apiErr := setHostAuthDisabled(phys.Name, authIndex, true)
	if apiErr == nil {
		markFailoverDisabledLocalAt(authID, authIndex, at)
		log.Printf("[qoderwork] failover disabled account via cpa api: auth=%s model=%s", authID, strings.TrimSpace(model))
		return true
	}
	if !errors.Is(apiErr, errCPAManagementNotConfigured) {
		log.Printf("[qoderwork] cpa disable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
	}
	if err := failoverPersistFn(phys.Name, phys.Path, "", raw); err != nil {
		// 宿主侧写盘失败不影响 sidecar 已经生效的排除，只是探活还要靠 sidecar 枚举。
		log.Printf("[qoderwork] failover disable: sidecar written, host write failed auth=%s err=%v", authID, err)
		return true
	}
	markFailoverDisabledLocalAt(authID, authIndex, at)
	wait := failoverWaitConfirmed
	go confirmFailoverHostState(wait, authIndex, true, authID, phys.Path)
	log.Printf("[qoderwork] failover disabled account: auth=%s model=%s", authID, strings.TrimSpace(model))
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
	// sidecar 才是路由判据的真实来源：即使拿不到 auth_index（宿主 RPC 不通、
	// 账号已从 auth.list 消失），也必须能解除插件自己的禁用，否则这个号
	// 会永远停在「插件禁用、宿主不管」的死角。
	if authIndex == "" {
		return failoverStateClearAndReport(authID, "no auth_index")
	}
	// 与 disableAccountForFailover 同一把文档锁：恢复读盘与写盘之间若让出，
	// 期间一个新失败可能已经写进 disabled，随后被这里覆盖回 false。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	phys, err := failoverGetPhysicalFn(authIndex)
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return failoverStateClearAndReport(authID, "no physical doc")
	}
	doc := parseFailoverAuthDoc(phys.JSON)
	if doc.manualDisable {
		// 用户已表达禁用意图：sidecar 里就算有本机制的禁用也必须让位，
		// 否则探活会在用户明确禁用后把号放回路由。
		return false
	}
	if !doc.failoverDisable {
		// 盘上没有本机制标记，但 sidecar 可能仍有本进程写的禁用（宿主回写把它抹了）。
		// 这种情况必须清 sidecar，否则账号永远回不来。
		return failoverStateClearAndReport(authID, "no host marker")
	}

	var rawDoc map[string]any
	if err := json.Unmarshal(phys.JSON, &rawDoc); err != nil || rawDoc == nil {
		return failoverStateClearAndReport(authID, "unparsable host doc")
	}
	rawDoc["disabled"] = false
	delete(rawDoc, failoverDisableKey)
	delete(rawDoc, failoverDisableModelKey)
	delete(rawDoc, failoverDisableAtKey)
	raw, err := json.Marshal(rawDoc)
	if err != nil {
		return failoverStateClearAndReport(authID, "marshal failed")
	}
	// 恢复同样优先走宿主管理 API：宿主真相源里的 disabled 不置回 false，
	// 宿主会继续把这个号挡在路由外，插件侧解除就成了空转。
	apiErr := setHostAuthDisabled(phys.Name, authIndex, false)
	if apiErr != nil {
		if !errors.Is(apiErr, errCPAManagementNotConfigured) {
			log.Printf("[qoderwork] cpa enable api failed, fallback to disk: auth=%s err=%v", authID, apiErr)
		}
		if err := failoverPersistFn(phys.Name, phys.Path, "", raw); err != nil {
			log.Printf("[qoderwork] failover re-enable host write failed, clearing sidecar only: auth=%s err=%v", authID, err)
		}
	}
	// 解除禁用必须同时清掉内存冷却：双写下两侧是同一个故障的两份记录，只解一边
	// 会得到「盘上可用、插件仍排除」的半恢复账号——宿主会把请求路由过来，但
	// scheduler.pick 与同请求换号仍按冷却跳过它，等于这个号回来了却接不到流量。
	// 锁顺序：本函数不持 failoverMu，resetAccountFailover 才能安全取锁。
	//
	// 注意 resetAccountFailover 本身也会尝试解除盘上禁用（成功请求路径），
	// 这里直接清内存 + 探活配额，避免它再次进入本函数的文档锁造成自锁。
	resetCooldownState(authID)
	forgetFailoverProbeModel(authID)
	resetFailoverProbeQuota(authID)
	clearFailoverDisabledLocal(authID, authIndex)
	failoverStateClearDisabled(authID)
	wait := failoverWaitConfirmed
	go confirmFailoverHostState(wait, authIndex, false, authID, phys.Path)
	log.Printf("[qoderwork] failover re-enabled account: auth=%s", authID)
	return true
}

// failoverStateClearAndReport 在宿主侧无法完成恢复时，仅清 sidecar 并记录原因。
//
// 为什么需要这条降级路径：sidecar 是路由判据的真实来源，若因为拿不到 auth_index
// 或宿主导档读不到就拒绝清除，账号会永久停在「插件禁用、宿主不管」的死角，
// 只能人工介入。清 sidecar 至少让账号回到路由，宿主侧的 disabled 由它自己负责。
//
// [参数] authID：账号标识；reason：降级原因，仅用于日志定位。
// [返回] true 表示 sidecar 已完成清除。
// 最近修改时间：2026-10-25；改动原因：新增，避免宿主不可达时账号永久禁用。
func failoverStateClearAndReport(authID, reason string) bool {
	if strings.TrimSpace(authID) == "" {
		return false
	}
	if !failoverStateIsDisabled(authID) {
		return false
	}
	resetCooldownState(authID)
	forgetFailoverProbeModel(authID)
	resetFailoverProbeQuota(authID)
	clearFailoverDisabledLocal(authID, "")
	failoverStateClearDisabled(authID)
	log.Printf("[qoderwork] failover re-enabled via sidecar only (%s): auth=%s", reason, authID)
	return true
}

// failoverProbeCandidate 描述一个待探活的禁用账号。
type failoverProbeCandidate struct {
	authIndex string
	authID    string
	model     string
}

// failoverProbeTargets 枚举待探活的禁用账号。
//
// 真相源是本插件的 sidecar，而不是宿主 auth 文件上的 disabled 标记。原因（2026-10-25
// 生产实证）：宿主重建 auth 文档会抹掉标记、fsnotify 监听会静默失效，若仍按盘上标记
// 挑候选，账号被禁用后永远进不了探活队列，只能人工启用。
//
// 候选来源分两路合并：
//  1. sidecar 里标记为 disabled 的账号（本插件的权威记录）；
//  2. 宿主 auth.list 里 disabled 且盘上带本机制标记的账号（兼容旧数据）。
//
// 判定条件不再要求 f.Disabled：宿主看不到 sidecar 写的禁用，若要求宿主先认账，
// 这条链路就又回到依赖宿主的老路上。
//
// [返回] 待探活候选列表；无候选时返回 nil。
// 最近修改时间：2026-10-25；改动原因：真相源从宿主盘上标记改为插件 sidecar。
func failoverProbeTargets() []failoverProbeCandidate {
	files, err := failoverListFn()
	if err != nil {
		return nil
	}
	// 先建索引：sidecar 的键是归一化标识，需要映射回宿主记录才能拿到 auth_index。
	byKey := make(map[string]pluginapi.HostAuthFileEntry, len(files))
	for _, f := range files {
		if key := normalizeFailoverStateKey(f.ID); key != "" {
			byKey[key] = f
		}
	}

	seen := make(map[string]struct{}, len(files))
	out := make([]failoverProbeCandidate, 0, len(files))
	appendCandidate := func(f pluginapi.HostAuthFileEntry, model string) {
		authIndex := strings.TrimSpace(f.AuthIndex)
		if authIndex == "" {
			return
		}
		if _, dup := seen[authIndex]; dup {
			return
		}
		seen[authIndex] = struct{}{}
		out = append(out, failoverProbeCandidate{authIndex: authIndex, authID: f.ID, model: model})
	}

	// 1. sidecar 权威来源。
	for _, key := range failoverStateDisabledIDs() {
		f, ok := byKey[key]
		if !ok {
			continue
		}
		entry, okEntry := failoverStateGet(f.ID)
		if !okEntry {
			continue
		}
		model := failoverProbeModelFor(f.ID)
		if model == "" {
			model = entry.Model
		}
		appendCandidate(f, model)
	}

	// 2. 兼容旧数据：宿主盘上带本机制标记的账号。
	for _, f := range files {
		if !f.Disabled || strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}
		phys, errGet := failoverGetPhysicalFn(f.AuthIndex)
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
		appendCandidate(f, model)
	}
	return out
}

// failoverFallbackProbeModel 在模型标记缺失时给出一个可回放的候选模型。
//
// 只在「盘上标记、sidecar、内存都没记模型」这一死角上使用：此时按严格口径该号
// 永远不会被探活，只能人工启用。优先取该账号的可用模型（随机选一个，
// 与手动「测试」按钮同源），确实没有候选时返回空串让调用方保持禁用。
//
// [参数] sa：已解析的账号凭据，可为 nil。
// [返回] 可回放的模型名；无候选时返回空串。
// 最近修改时间：2026-10-31；改动原因：新增，修复标记缺失导致探活永久跳过。
func failoverFallbackProbeModel(sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	return strings.TrimSpace(pickRandomQoderModel(sa))
}

// resolveFailoverAuth 把候选的 auth_index 解析为可用的账号凭据。
// [参数] authIndex：宿主 auth_index。
// [返回] 账号凭据；解析失败返回 nil。
// 最近修改时间：2026-10-22；改动原因：新增。
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
// 回到路由，下一个请求照样失败；在 Qoder 这里代价更大——探活与真实推理共享同一份
// 日调用配额，多探一个模型就是多烧一次额度。
//
// 没有失败模型可回放（禁用文档里记了空模型）时兜底出一个可回放模型：
// 探通即解禁，探不通保持禁用。旧实现「无模型就跳过」看似安全，实际制造了
// 「禁用且永远探不了」的死角——2026-10-29 事故里主力快号就是这样被禁
// 8 小时零探活，只能人工启用。代价是可能探错一次模型（Qoder 探活与真实推理
// 共享日配额），远小于健康号永久失联；兜底同样受 failoverProbeFailLimit 约束。
//
// Qoder 专属：当日探活失败达到 failoverProbeFailLimit 次的账号本轮直接跳过，
// 不消耗任何上游额度；计数在本地日期翻篇时自动归零。
//
// 探活失败的账号不写任何盘，保持禁用等下一轮——禁用状态本身就是「不可用」的
// 完整表达，再写一次只会平白触发宿主 reload。
//
// [参数] resolve：auth_index 到凭据的解析函数（测试注入）；now：当前时刻（测试注入）。
// [返回] 本轮实际探测的账号数。
// 最近修改时间：2026-10-31；改动原因：空模型改为兜底回放，避免禁用号永久失联。
func runFailoverProbeTick(resolve func(string) *storedAuth, now time.Time) int {
	targets := failoverProbeTargets()
	probed := 0
	for _, tc := range targets {
		sa := resolve(tc.authIndex)
		if sa == nil {
			continue
		}
		if failoverProbeExhaustedToday(tc.authID, now) {
			log.Printf("[qoderwork] failover probe skipped: daily limit reached (%d) auth=%s",
				failoverProbeFailLimit, tc.authID)
			continue
		}
		model := strings.TrimSpace(tc.model)
		if model == "" {
			// 兜底：优先用该账号可用的模型，保证标记残缺的号仍有恢复通道。
			model = failoverFallbackProbeModel(sa)
			if model == "" {
				log.Printf("[qoderwork] failover probe skipped: no probe model available for auth=%s", tc.authIndex)
				continue
			}
			log.Printf("[qoderwork] failover probe adopts fallback model: auth=%s model=%s", tc.authIndex, model)
		}
		probed++
		if err := failoverPingFn(sa, model); err != nil {
			log.Printf("[qoderwork] failover probe failed: auth=%s model=%s err=%s",
				tc.authIndex, model, truncateRedacted(err.Error(), 160))
			// 失败才消耗当日配额：达到上限后当日不再探测，
			// 把剩余额度留给真实业务请求。
			if fails := noteFailoverProbeFailure(tc.authID, now); fails == failoverProbeFailLimit {
				log.Printf("[qoderwork] failover probe daily limit reached (%d) auth=%s: no probe until local midnight",
					fails, tc.authID)
			}
			continue
		}
		log.Printf("[qoderwork] failover probe ok: auth=%s model=%s", tc.authIndex, model)
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
// 最近修改时间：2026-10-22；改动原因：新增，把用户裁定的「立即跳过」变成可测函数。
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
// 在 CPA 面板启用、宿主 UI 改 disabled:false、management 的禁用开关，
// 都不会经过 reenableAccountForFailover。那些号在宿主侧已经可路由，插件内存里却
// 仍判它坏，于是调度与入口前置拦截会永久跳过它，直到插件重启（静默遗忘的镜像形态）。
//
// 判据只认「盘上明确为 enabled 的号」：列表里没有的键（账号已删除、RPC 未覆盖）
// 一律保留，避免用「查不到」当成「已恢复」。反过来若我们的写盘尚未落定而列表仍显
// enabled，这里清掉也只是让下一个请求重新失败一次、重新触发禁用——错杀可自愈，
// 漏杀会把号永久挡在路由外，所以取保守方向。
//
// [返回] 本轮清除的键数（仅供测试与日志观察）。
// 最近修改时间：2026-10-22；改动原因：新增，防止外部启用后本地排除集合卡死。
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
		delete(failoverProbeQuota, key)
		cleared++
	}
	// sidecar 必须同步对账：否则用户在宿主面板把号启用后，插件仍按 sidecar
	// 判据一直跳过它，形成「宿主说可用、插件说禁用」的死角。
	for _, key := range failoverStateDisabledIDs() {
		if _, ok := enabled[key]; ok {
			failoverStateClearDisabled(key)
			cleared++
		}
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
			// 先对账本地集合：外部（面板 / 宿主 UI / management）已把号启用时，
			// 必须让插件不再排除它，否则它会一直被跳过直到重启。
			reconcileFailoverLocalSet()
			runFailoverProbeTick(resolveFailoverAuth, failoverNowFn())
		})
	}
}

// failoverProbeQuotaSnapshot 返回账号当日探活失败次数，供面板与诊断使用。
// [参数] authID：账号标识。
// [返回] 当日累计失败次数。
// 最近修改时间：2026-10-22；改动原因：新增，让面板能显示「今日已放弃探活」。
func failoverProbeQuotaSnapshot(authID string) int {
	return failoverProbeFailsToday(authID, failoverNowFn())
}

// failoverProbeQuotaBudget 返回当日探活上限，供面板展示 x/y。
// [返回] 上限次数。
// 最近修改时间：2026-10-22；改动原因：新增。
func failoverProbeQuotaBudget() int {
	return failoverProbeFailLimit
}
