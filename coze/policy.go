package main

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件定义「积分路由红线」策略。
//
// 扣子不声明 scheduler 能力：宿主只调用排序后的第一个 scheduler 插件
// （internal/pluginhost/scheduler.go 的 schedulerRecord 只取一条记录），
// 抢这个槽位会顶掉现有插件的调度内核。因此本插件走执行入口闸门——
// 宿主选到低额账号时，在执行前直接回 403，宿主按既有账号级故障通道
// 换号。语义与上游 1000521「积分耗尽」完全一致，只是提前到不打上游。
//
// 阈值取 100 而不是兄弟插件的 20：扣子单次对话消耗远高于按次计费的
// 套餐类产品，20 分只够一两次请求，起不到缓冲作用。

const (
	// defaultMinRoutableCredits 是扣子账号的积分红线默认值。
	//
	// 低于该值的账号不再承载新请求，并会被巡检写盘禁用、等签到恢复。
	// 取值 200（2026-10-25 用户裁定）：扣子单次对话消耗远高于按次计费的
	// 套餐类产品，100 分只够一两次请求，且每日签到固定发放 1500 分，
	// 200 的分界既能挡住残血号，又不会把「刚领到签到、正在消耗」的号误杀。
	defaultMinRoutableCredits int64 = 200

	// creditRefreshDefaultSeconds 是后台积分巡检间隔默认值。
	//
	// 3600 秒（2026-10-25 用户裁定）：巡检要承担签到、落盘与分级三重职责，
	// 5 分钟一次会把上游打成周期性压力且毫无收益（签到是幂等的）。
	// 数据新鲜度由面板单卡「刷新积分」提供即时通道。
	creditRefreshDefaultSeconds = 3600

	// creditRefreshConcurrency 是单轮积分刷新的并发上限。
	//
	// 与续期循环同口径：账号多时串行会把一轮拖很久，并发过高又容易
	// 触发上游风控，取 4 作为折中。
	creditRefreshConcurrency = 4

	// creditDepletedTrustWindow 是「全池耗尽」判据允许采信缓存的年龄。
	//
	// 必须显著宽于巡检周期（1 小时）：若沿用旧的 15 分钟，每轮中后段
	// 缓存都会被判成「数据不可信」，快速失败静默失效、请求又回去白打上游。
	// 90 分钟 = 一个周期 + 50% 余量，容忍一轮延迟完成。
	creditDepletedTrustWindow = 90 * time.Minute
)

var (
	minRoutableCredits   int64 = defaultMinRoutableCredits
	minRoutableCreditsMu sync.RWMutex

	// creditPoolSnapshot 是闸门读的账号池快照，由积分循环每轮重建。
	//
	// 闸门位于每个请求的执行入口，绝不能在那里调 host.auth.list——
	// 那是「一次列目录 + 每账号一次读取」的 RPC 扇出，会把每次推理的
	// 首字时间拖慢数毫秒到数十毫秒。快照是纯内存读，代价可忽略。
	creditPoolSnapshot atomic.Value // []creditPoolEntry
)

// creditPoolEntry 是闸门判定所需的单账号内存视图。
type creditPoolEntry struct {
	authID string
	usable bool
	remain int64
	known  bool
}

// loadedMinRoutableCredits 返回当前生效的积分路由红线。
// [参数] 无。
// [返回] 当前红线值。
func loadedMinRoutableCredits() int64 {
	minRoutableCreditsMu.RLock()
	defer minRoutableCreditsMu.RUnlock()
	return minRoutableCredits
}

// setMinRoutableCredits 设置积分路由红线并返回还原函数。
// [参数] n: 目标红线值。
// [返回] 还原原值并解除设置的函数。
func setMinRoutableCredits(n int64) func() {
	minRoutableCreditsMu.Lock()
	old := minRoutableCredits
	minRoutableCredits = n
	minRoutableCreditsMu.Unlock()
	return func() {
		minRoutableCreditsMu.Lock()
		minRoutableCredits = old
		minRoutableCreditsMu.Unlock()
	}
}

// applyMinRoutableCredits 直接设置积分路由红线，不返回还原函数。
//
// 配置热更新用它：还原闭包是给单测做隔离的，生产路径不需要。
// [参数] n: 目标红线值。
// [返回] 无。
func applyMinRoutableCredits(n int64) {
	minRoutableCreditsMu.Lock()
	minRoutableCredits = n
	minRoutableCreditsMu.Unlock()
}

// cachedCreditRemain 读取账号的缓存积分余量。
//
// 只用缓存，绝不在这里发起上游请求：闸门位于每个请求的执行入口，
// 在那里同步打积分接口会把每次推理的首字时间拖慢一次往返。
// 未知积分返回 (-1, false)，由调用方决定放行还是拦截。
// [参数] authID: 账号标识。
// [返回] 余量与「是否已知」。
// 最近修改时间：2026-10-25；改动原因：键归一化，兼容宿主侧带前缀/后缀的标识形态。
func cachedCreditRemain(authID string) (int64, bool) {
	key := creditSnapshotKey(authID)
	if key == "" {
		return creditUnknownRemain, false
	}
	creditsCache.mu.Lock()
	defer creditsCache.mu.Unlock()
	entry, ok := creditsCache.entries[key]
	if !ok || entry.summary.Error != "" {
		return creditUnknownRemain, false
	}
	return entry.summary.Remain, true
}

// cachedCreditTrustworthy 判断账号积分缓存是否还值得采信。
//
// 「已知」不等于「可信」：余额接口只写成功结果，巡检停摆时缓存会长期
// 停在旧值。全池耗尽的快速失败会把请求直接判死，必须确认数据是新的，
// 否则一次巡检停滞就会把整池误判成 0 分而全线拒服务。
// [参数] authID: 账号标识。
// [返回] 缓存有值、无错误且年龄小于采信窗口时为 true。
// 最近修改时间：2026-10-25；改动原因：键归一化，与写入侧保持一致。
func cachedCreditTrustworthy(authID string) bool {
	key := creditSnapshotKey(authID)
	if key == "" {
		return false
	}
	creditsCache.mu.Lock()
	defer creditsCache.mu.Unlock()
	entry, ok := creditsCache.entries[key]
	if !ok || entry.summary.Error != "" {
		return false
	}
	return time.Since(entry.fetched) < creditDepletedTrustWindow
}

// isCreditsBelowRedline 判断账号积分是否已低于路由红线。
//
// 未知积分一律返回 false（放行）：查不到余额时把账号挡在门外，
// 会在积分接口抖动时让整池账号同时不可用，代价远大于多打一次上游。
// [参数] authID: 账号标识。
// [返回] 已知且低于红线时为 true。
func isCreditsBelowRedline(authID string) bool {
	remain, known := cachedCreditRemain(authID)
	if !known {
		return false
	}
	return remain < loadedMinRoutableCredits()
}

// publishCreditPool 用账号列表与缓存重建闸门快照。
//
// 只由积分循环调用：它已经持有账号列表，顺手把「谁可用、谁还有多少分」
// 拍平成纯内存结构，请求路径就不必再碰宿主 RPC。
// [参数] accounts: 全量账号。
// [返回] 无。
func publishCreditPool(accounts []*StoredAuth) {
	entries := make([]creditPoolEntry, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		remain, known := cachedCreditRemain(acc.AuthID)
		entries = append(entries, creditPoolEntry{
			authID: acc.AuthID,
			usable: !acc.Disabled && authStateOf(acc) == "ok",
			remain: remain,
			known:  known,
		})
	}
	creditPoolSnapshot.Store(entries)
}

// loadCreditPool 读取闸门快照。
// [参数] 无。
// [返回] 快照；尚未发布过时返回 nil。
func loadCreditPool() []creditPoolEntry {
	if v, ok := creditPoolSnapshot.Load().([]creditPoolEntry); ok {
		return v
	}
	return nil
}

// hasRoutableAccountExcept 判断除指定账号外是否还有账号可以承载流量。
//
// 这是闸门的核心取舍：只有当「池里还有能用的号」时才拦截当前请求，
// 否则把最后一个残血号也挡掉，用户会直接看到无号可用。全池都低时
// 宁可放行让上游自己判，也不做无意义的拒绝。
// [参数] excludeAuthID: 当前请求已经选中的账号；pool: 账号池快照。
// [返回] 存在其他可用账号时为 true。
func hasRoutableAccountExcept(excludeAuthID string, pool []creditPoolEntry) bool {
	redline := loadedMinRoutableCredits()
	for _, entry := range pool {
		if entry.authID == excludeAuthID || !entry.usable {
			continue
		}
		// 未知积分的账号按可用算：宁可换过去试一次，
		// 也不要在余额接口抖动时判定整池不可用。
		if entry.known && entry.remain < redline {
			continue
		}
		return true
	}
	return false
}

// allRoutableAccountsDepleted 判断池内全部可用账号是否都「确定零积分」。
//
// 只认 remain 恰好为 0 且缓存新鲜的账号：低于红线但不为 0 的残血号
// 仍可能跑完这一轮，那是原有「放行让上游自己判」的语义，不能在这里收紧。
// 池空（刚热重载、循环还没跑过第一轮）时返回 false——否则会误把整池判死、
// 全线拒绝服务。
// [参数] pool: 账号池快照。
// [返回] 至少有一个可用账号且它们全部确定零积分时为 true。
func allRoutableAccountsDepleted(pool []creditPoolEntry) bool {
	usable := 0
	for _, entry := range pool {
		if !entry.usable {
			continue
		}
		usable++
		if !entry.known || entry.remain != 0 {
			return false
		}
		if !cachedCreditTrustworthy(entry.authID) {
			return false
		}
	}
	return usable > 0
}

// verifyAccountCredits 在执行前校验所选账号的积分是否足够承载本次请求。
//
// 两条独立判据，顺序不能反：
//  1. 全池确定零积分 → 返回 400 请求级失败。此时换号没有目的地，
//     继续放行的代价是宿主把整池轮一遍、每个号都白打一次上游
//     （2026-10-21 生产实证：7 个 0 分号各耗 1.2-3.2 秒才拿到 1000521，
//     再叠加 new-api 5 次重试，一次生图刷出 9 条错误）。
//     400 命中宿主请求级白名单：立即终止轮转、不冷却账号；
//     也不在 new-api 的自动重试区间内，两层重试一起止住。
//  2. 本号低于红线但池里还有别的可用号 → 返回 403 走账号级换号通道，
//     与上游 1000521 同一条路径。
//
// 全程只读内存快照与积分缓存，不发任何宿主 RPC 或上游请求。
// [参数] sa: 本次请求选中的账号。
// [返回] 应换号或应直接失败时返回带状态码的错误，否则 nil。
func verifyAccountCredits(sa *StoredAuth) error {
	if sa == nil {
		return nil
	}
	redline := loadedMinRoutableCredits()
	pool := loadCreditPool()

	remain, known := cachedCreditRemain(sa.AuthID)
	if known && remain < redline {
		if hasRoutableAccountExcept(sa.AuthID, pool) {
			// 只在真正换号时留痕：这是宿主的选中结果与积分现状不一致的证据，
			// 常驻日志里出现即说明账号池的余额分布需要人工关注。
			hostLogf("warn", "账号积分低于路由红线，转由宿主换号", map[string]any{
				"auth_id": sa.AuthID,
				"remain":  remain,
				"redline": redline,
			})
			return &creditsBelowRedlineError{remain: remain, redline: redline}
		}
	}

	// 走到这里说明换号没有目的地。若「本号确定零积分」且整池也都确定零积分，
	// 就不要再把请求送往上游赌一次——残血但非零的号仍会落到放行分支。
	// 必须同时看本号：池快照最多滞后一个刷新周期，若本号是快照之后才
	// 加入的新账号，池里全零并不代表此刻选中的号不可用。
	// 红线为 0（kill switch）时 remain < redline 恒不成立，闸门整体失效。
	if known && remain < redline && cachedCreditTrustworthy(sa.AuthID) &&
		allRoutableAccountsDepleted(pool) {
		hostLogf("warn", "账号池积分已全部耗尽，直接返回失败", map[string]any{
			"auth_id":  sa.AuthID,
			"accounts": len(pool),
			"redline":  redline,
		})
		return &creditsPoolDepletedError{}
	}

	return nil
}

// creditsPoolDepletedError 表示账号池已无积分、本次请求无法服务。
//
// 状态码取 400 而不是 403：403 会让宿主继续轮转下一个账号并冷却当前号，
// 全池耗尽时那等于把整池白打一遍上游。400 命中宿主请求级白名单
// （clienterror.IsRequestFault），一次都不打上游直接终止。
// [参数] 无。
// [返回] 无。
type creditsPoolDepletedError struct{}

// Error 返回面向用户的失败说明。
// [参数] 无。
// [返回] 错误文案。
func (e *creditsPoolDepletedError) Error() string {
	return "账号池积分已耗尽：当前无可用扣子账号，请稍后重试或补充积分"
}

// StatusCode 返回宿主分类用的 HTTP 状态码。
// [参数] 无。
// [返回] 400。
func (e *creditsPoolDepletedError) StatusCode() int {
	return http.StatusBadRequest
}

// creditsBelowRedlineError 表示账号积分低于红线、应换号的失败。
//
// 状态码取 403 而不是 402/429：宿主的换号白名单
// （isCredentialRetryRoundStatus）只认 403/408/429/500/502/503/504，
// 而 429 的冷却下限只有 10 秒，不足以让账号等到积分恢复。
// [参数] 无。
// [返回] 无。
type creditsBelowRedlineError struct {
	remain  int64
	redline int64
}

// Error 返回面向用户的失败说明。
// [参数] 无。
// [返回] 错误文案。
func (e *creditsBelowRedlineError) Error() string {
	return fmt.Sprintf("账号积分不足：剩余 %d 分，低于路由红线 %d 分，正在切换账号", e.remain, e.redline)
}

// StatusCode 返回宿主分类用的 HTTP 状态码。
// [参数] 无。
// [返回] 403。
func (e *creditsBelowRedlineError) StatusCode() int {
	return http.StatusForbidden
}
