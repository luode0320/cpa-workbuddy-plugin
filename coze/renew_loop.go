package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// 本文件实现插件内的定时续期循环。
//
// 为什么必须自建定时器（2026-10-18 宿主源码取证）：
// authDataForLogin 的 refresh_interval_seconds 只决定宿主「何时把账号排进
// 刷新队列」，而 auto_refresh_loop.handleDueAuth 在 exec == nil 时会直接
// `l.upsert(authID, now.Add(l.interval)); return` —— 只重排、不调用
// auth.refresh。宿主 RegisterExecutors 只注册声明了 executor 能力的插件，
// 扣子不声明 executor（对话链路尚未实现），因此宿主的 4 小时排期对扣子
// 永远只是空转。真正让会话滚动的只能是插件自己的定时器。
//
// 循环语义：
//   - 启动后先等宿主桥就绪（host.auth.list 可调用），再立即跑一轮，
//     让容器重启 / 插件热重载后马上把会话刷新一次；
//   - 之后按配置 renew_interval_seconds（默认 4 小时）周期性执行；
//   - configurePlugin 触发的 reconfigure 会合并成一次立即重排，
//     使间隔改动无需重启即可生效。
//
// 失败处理：单个账号续期失败只记日志，不改变账号的 disabled 状态——
// 续期失败多为临时网络问题，把账号下线会让它在下一轮彻底失去自愈机会。

const (
	// renewStartupWait 是等待宿主桥就绪的上限。
	//
	// 与 workbuddy watchdog 同口径：实测 <1s，15s 覆盖冷启动与慢扫描。
	renewStartupWait = 15 * time.Second

	// renewReadyPoll 是等待宿主就绪时的轮询间隔。
	renewReadyPoll = 250 * time.Millisecond

	// renewConcurrency 是单轮续期的并发上限。
	//
	// 续期是「Cookie → token → Cookie」两次上游往返，串行在账号多时会把
	// 一轮拖到几分钟；并发过高又容易触发上游风控，取 4 作为折中。
	renewConcurrency = 4
)

var (
	// renewTickCh 合并外部触发（reconfigure）到同一轮。
	// 缓冲 1：已有 tick 在排队时直接丢弃，避免堆积。
	renewTickCh = make(chan struct{}, 1)

	// renewStopCh 在插件 shutdown 时关闭，用于立即唤醒循环。
	//
	// 只关闭不写入：向已关闭 channel 发送会 panic，因此 requestRenewTick
	// 始终只写 renewTickCh，两者互不干扰。
	renewStopCh   = make(chan struct{})
	renewStopOnce sync.Once

	// renewStartOnce 保证循环只启动一次。
	renewStartOnce sync.Once
)

// requestRenewTick 请求立即执行一轮续期（非阻塞）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，配置热更新触发重排。
func requestRenewTick() {
	select {
	case renewTickCh <- struct{}{}:
	default:
	}
}

// hostReadyForRenew 判断宿主桥是否可用。
//
// 判据是 host.auth.list 调用成功：空列表也算就绪——IPC 通了，只是还没有
// 账号。这是不依赖宿主私有状态的最强探针（与 workbuddy watchdog 同口径）。
// [参数] 无。
// [返回] 就绪时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，首轮启动判据。
func hostReadyForRenew() bool {
	if _, errCall := hostCall(pluginabi.MethodHostAuthList, nil); errCall != nil {
		return false
	}
	return true
}

// waitHostReadyForRenew 轮询等待宿主就绪，超时后返回最后一次判定结果。
// [参数] maxWait: 最长等待时间；ready: 就绪判定函数。
// [返回] 就绪时为 true。
// 最近修改时间：2026-10-18；改动原因：新增，避免 init 早于宿主装配的竞态。
func waitHostReadyForRenew(maxWait time.Duration, ready func() bool) bool {
	if maxWait <= 0 {
		return ready()
	}
	deadline := time.Now().Add(maxWait)
	for {
		if ready() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ready()
		}
		if remaining > renewReadyPoll {
			remaining = renewReadyPoll
		}
		time.Sleep(remaining)
	}
}

// renewDeps 是一轮续期的可注入依赖。
//
// 抽出来只为让单测在不碰宿主、不发真实请求的前提下覆盖过滤与失败隔离。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，续期循环测试注入点。
type renewDeps struct {
	list  func() ([]*StoredAuth, error)
	renew func(*StoredAuth) (*renewResult, error)
	logf  func(level, message string, fields map[string]any)
	// backoff 是退避状态注入点，nil 时回落全局表。
	//
	// 生产必须共用同一张表（跨轮次才有记忆），单测则需要每例独立，
	// 否则前一个用例记的失败会让后一个用例被莫名跳过。
	backoff *renewBackoffState
}

// renewSessionFn 是裸 HTTP 换发的注入点。
//
// 抽成变量只为让单测在不发真实上游请求的前提下驱动降级链：真实
// renewSession 会直连扣子令牌端点，测试环境里既不可达也不该被调用。
var renewSessionFn = renewSession

// browserReviveFn 是浏览器保活的注入点。
//
// 同理：降级链的判定逻辑（何时降级、降级失败如何合并错误）必须能在
// 没有 Chrome 的机器上被锁定。
var browserReviveFn = func(sa *StoredAuth) (*renewResult, error) {
	return browserKeeperDefault.reviveSession(sa)
}

// defaultRenewDeps 返回生产依赖：宿主账号列表、真实续期与宿主日志。
//
// 续期分两级：先用裸 HTTP 走令牌换发，换不到会话主项时再降级到真实
// 浏览器重试一次（2026-11-21 实测：同一份 Cookie，Go 进程直连令牌端点
// 被判会话过期，真实页面内同源 fetch 却能换发，差别在客户端特征）。
//
// 降级触发条件有三条，缺一不可：
//   - 裸 HTTP 报错且错误文本命中会话过期；
//   - 裸 HTTP 没报错但没换到会话主项（Renewed=false）——埋点 Cookie 不等于
//     换发，这种「看似成功实则无变化」与失效是同一处置；
//   - 浏览器保活开关已开启（未开启时保活自身返回 errBrowserKeepDisabled，
//     该错误会被合并进主错误，提示运维可以开开关）。
//
// [参数] 无。
// [返回] 依赖集合。
// 最近修改时间：2026-11-21；改动原因：新增浏览器保活降级，兜住被判失效的活会话。
func defaultRenewDeps() renewDeps {
	return renewDeps{
		list:  listAllAuthFiles,
		logf:  hostLogf,
		renew: renewWithBrowserFallback,
	}
}

// renewWithBrowserFallback 执行一轮续期，必要时降级到真实浏览器。
//
// 两级都失败时返回包装错误：保留裸 HTTP 的原始原因（isSessionExpiredError
// 仍能命中），并附上浏览器侧的原因，避免「已经被判失效」这个事实被
// 「浏览器保活未启用」之类的次要原因顶掉。
// [参数] sa: 账号。
// [返回] 续期结果与错误。
// 最近修改时间：2026-11-21；改动原因：新增，续期降级链主线。
func renewWithBrowserFallback(sa *StoredAuth) (*renewResult, error) {
	res, errRenew := renewSessionFn(sa)
	if errRenew != nil {
		if !isSessionExpiredError(errRenew.Error()) {
			return nil, errRenew
		}
		res, errRenew = renewViaBrowser(sa, errRenew)
		if errRenew != nil {
			return nil, errRenew
		}
		return finishRenewal(sa, res)
	}
	if res == nil || !res.Renewed {
		// 裸 HTTP 没换到会话主项：再给浏览器一次机会。
		// 降级失败不覆盖原结果，让调用方仍能拿到「未换发」的 Note 与条数。
		if resBrowser, errBrowser := renewViaBrowser(sa, nil); errBrowser == nil {
			return finishRenewal(sa, resBrowser)
		}
	}
	return finishRenewal(sa, res)
}

// renewViaBrowser 用真实浏览器再换发一次。
//
// prevErr 非空表示裸 HTTP 已失败，此时浏览器侧失败要合并进原错误；
// prevErr 为空表示裸 HTTP 未换发但无错误，浏览器侧失败只上抛自身错误，
// 由调用方决定是否回落原结果。
// [参数] sa: 账号；prevErr: 裸 HTTP 的原始错误，可为 nil。
// [返回] 浏览器换发结果与错误。
// 最近修改时间：2026-11-21；改动原因：新增，降级链第二级。
func renewViaBrowser(sa *StoredAuth, prevErr error) (*renewResult, error) {
	if browserReviveFn == nil {
		return nil, prevErr
	}
	res, errRevive := browserReviveFn(sa)
	if errRevive != nil {
		if prevErr != nil {
			return nil, fmt.Errorf("%w；浏览器保活也失败：%v", prevErr, errRevive)
		}
		return nil, errRevive
	}
	if res == nil || !res.Renewed {
		if prevErr != nil {
			return nil, prevErr
		}
		return nil, fmt.Errorf("浏览器保活未换到会话主项：%s", renewNote(res))
	}
	hostLogf("info", "浏览器保活已换发会话", map[string]any{
		"auth_id": alertKeyOf(sa),
		"note":    res.Note,
	})
	return res, nil
}

// finishRenewal 把续期结果写回账号。
// [参数] sa: 账号；res: 续期结果。
// [返回] 续期结果与写入错误。
// 最近修改时间：2026-11-21；改动原因：新增，抽出写盘收尾让降级链两处共用。
func finishRenewal(sa *StoredAuth, res *renewResult) (*renewResult, error) {
	if errApply := applyRenewal(sa, res); errApply != nil {
		return nil, errApply
	}
	return res, nil
}

// renewTargets 从账号列表里挑出本轮需要续期的账号。
//
// 不排除禁用账号（2026-10-25 起）：被低额机制禁用的号要靠「次日签到」
// 才能恢复，而签到必须用有效 Cookie；若禁用一个号就停止续期，
// Cookie 会在等待期内失效，签到失败后账号永久卡死无法自动恢复。
// 禁用号续期不会带来流量——它们不参与路由，只是维持凭据可用。
//
// 登录态已判为 expired 的账号同样要试：Cookie 可能只是主会话项过期，
// token beat 仍能换回新会话，直接跳过会让账号永久失修。
// 缺少物理路径的账号单独返回：续期结果无处落盘，继续跑等于白耗上游请求，
// 且这种情况通常意味着宿主账号发现链路异常，需要在上层日志里可见。
// [参数] accounts: 账号列表。
// [返回] 待续期账号与缺路径的账号标识。
// 最近修改时间：2026-10-25；改动原因：不再跳过禁用账号，解禁依赖其凭据可用。
func renewTargets(accounts []*StoredAuth) ([]*StoredAuth, []string) {
	out := make([]*StoredAuth, 0, len(accounts))
	var pathless []string
	for _, sa := range accounts {
		if sa == nil {
			continue
		}
		if sa.PhysicalPath == "" {
			pathless = append(pathless, sa.AuthID)
			continue
		}
		out = append(out, sa)
	}
	return out, pathless
}

// renewBackoffSteps 是连续失败后的阶梯退避间隔。
//
// 为什么需要退避：失效账号打 token beat 必定返回会话过期，而定时器每
// 4 小时就跑一轮。实测 12 个账号里 5 个已失效，8 天累计约 240 次必然
// 失败的上游请求——全是白耗配额与风控额度，还淹掉真正的告警。
// 阶梯到 48 小时封顶：即使账号长期失效，每天仍有一次机会自愈，不会
// 因为退避太长而错过上游恢复的窗口。
var renewBackoffSteps = []time.Duration{
	4 * time.Hour,
	8 * time.Hour,
	24 * time.Hour,
	48 * time.Hour,
}

// renewBackoffEntry 是单个账号的退避状态。
type renewBackoffEntry struct {
	// failCount 是连续失败次数，成功一次即清零。
	failCount int
	// nextTryAt 是下一次允许尝试的时刻。
	nextTryAt time.Time
}

// renewBackoffState 保存全部账号的退避状态。
//
// 只放内存不落盘：热重载后清零，失效账号会以 4 小时重新起步。这点代价
// 换掉一整套持久化与并发写盘逻辑，值得——退避是节流手段，不是业务事实，
// 丢了下轮重建即可。
type renewBackoffState struct {
	mu      sync.Mutex
	entries map[string]renewBackoffEntry
	// now 是时钟注入点，测试用它跳过真实等待。
	now func() time.Time
}

// newRenewBackoffState 创建空的退避状态。
// [参数] 无。
// [返回] 已初始化 entries 的状态。
// 最近修改时间：2026-11-21；改动原因：新增，退避表构造入口。
func newRenewBackoffState() *renewBackoffState {
	return &renewBackoffState{entries: map[string]renewBackoffEntry{}, now: time.Now}
}

// delayFor 返回连续失败 count 次后应等待的时长。
//
// 只做阶梯取值，不读状态也不加锁：持锁路径（recordFail）与非持锁调用方
// 共用同一份实现，避免出现两份迟早会漂移的阶梯。
// [参数] count: 连续失败次数（>=1）。
// [返回] 退避时长，超出阶梯长度时取最后一档。
// 最近修改时间：2026-11-21；改动原因：新增，阶梯取值唯一出口。
func (b *renewBackoffState) delayFor(count int) time.Duration {
	if count < 1 {
		count = 1
	}
	if count > len(renewBackoffSteps) {
		count = len(renewBackoffSteps)
	}
	return renewBackoffSteps[count-1]
}

// shouldSkip 判断账号是否仍在退避窗口内。
// [参数] authID: 账号标识。
// [返回] 未到下次尝试时刻时返回 true。
// 最近修改时间：2026-11-21；改动原因：新增，退避过滤判据。
func (b *renewBackoffState) shouldSkip(authID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[authID]
	if !ok {
		return false
	}
	return b.now().Before(entry.nextTryAt)
}

// recordFail 记一次失败并顺延下次尝试时刻。
// [参数] authID: 账号标识。
// [返回] 本次失败累计的连续次数。
// 最近修改时间：2026-11-21；改动原因：新增，失败累计与顺延。
func (b *renewBackoffState) recordFail(authID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[authID]
	entry.failCount++
	entry.nextTryAt = b.now().Add(b.delayFor(entry.failCount))
	b.entries[authID] = entry
	return entry.failCount
}

// recordSuccess 清除账号的退避状态。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，成功即清零。
func (b *renewBackoffState) recordSuccess(authID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, authID)
}

// renewBackoff 是定时续期共用的退避状态。
var renewBackoff = newRenewBackoffState()

// splitByBackoff 把待续期账号拆成「本轮执行」与「退避中跳过」两组。
// [参数] targets: 待续期账号；state: 退避状态。
// [返回] 本轮执行的账号与被跳过的账号标识。
// 最近修改时间：2026-11-21；改动原因：新增，退避过滤与目标筛选分离。
func splitByBackoff(targets []*StoredAuth, state *renewBackoffState) ([]*StoredAuth, []string) {
	if state == nil {
		return targets, nil
	}
	run := make([]*StoredAuth, 0, len(targets))
	var skipped []string
	for _, sa := range targets {
		if state.shouldSkip(sa.AuthID) {
			skipped = append(skipped, sa.AuthID)
			continue
		}
		run = append(run, sa)
	}
	return run, skipped
}

// runRenewTick 执行一轮续期：并发续期所有启用账号，单账号失败不影响其它账号。
// [参数] deps: 依赖集合。
// [返回] 本轮成功续期的账号数。
// 最近修改时间：2026-10-18；改动原因：新增，定时续期单轮执行体。
func runRenewTick(deps renewDeps) int {
	if deps.list == nil || deps.renew == nil {
		return 0
	}
	accounts, errList := deps.list()
	if errList != nil {
		if deps.logf != nil {
			deps.logf("warn", "定时续期读取账号失败", map[string]any{"error": errList.Error()})
		}
		return 0
	}
	targets, pathless := renewTargets(accounts)
	if len(pathless) > 0 && deps.logf != nil {
		deps.logf("warn", "定时续期跳过缺少物理路径的账号", map[string]any{"auth_ids": pathless})
	}
	if len(targets) == 0 {
		return 0
	}

	// 退避过滤：连续失败的账号在上游恢复前不再每轮打过去。
	backoff := deps.backoff
	if backoff == nil {
		backoff = renewBackoff
	}
	targets, skipped := splitByBackoff(targets, backoff)
	if len(skipped) > 0 && deps.logf != nil {
		deps.logf("info", "定时续期跳过退避中的账号", map[string]any{"auth_ids": skipped})
	}
	if len(targets) == 0 {
		return 0
	}

	sem := make(chan struct{}, renewConcurrency)
	var wg sync.WaitGroup
	var renewed atomic.Int32
	var notRenewed atomic.Int32
	for _, sa := range targets {
		wg.Add(1)
		go func(acc *StoredAuth) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res, errRenew := deps.renew(acc)
			if errRenew != nil {
				// 上游明确判定会话过期时落失效标记，面板才能如实显示
				// 「待重新授权」，而不是继续显示可用。
				markSessionInvalid(acc, errRenew.Error())
				failCount := backoff.recordFail(acc.AuthID)
				if deps.logf != nil {
					deps.logf("warn", "定时续期失败", map[string]any{
						"auth_id":     acc.AuthID,
						"error":       errRenew.Error(),
						"fail_count":  failCount,
						"retry_after": backoff.delayFor(failCount).String(),
					})
				}
				return
			}
			// 未换发与真换发必须分开上报：旧实现无论结果都打「定时续期完成」，
			// 生产日志里两种走向完全不可区分，会话死了很久也看不出来。
			if res != nil && res.Renewed {
				renewed.Add(1)
				backoff.recordSuccess(acc.AuthID)
				if deps.logf != nil {
					deps.logf("info", "定时续期完成", map[string]any{
						"auth_id": acc.AuthID,
						"note":    renewNote(res),
					})
				}
				return
			}
			// 未换发同样计入退避：上游已不认这个会话，每轮再打一遍只是
			// 徒增上游请求，结局不会变。与失败分支共用同一张退避表。
			notRenewed.Add(1)
			failCount := backoff.recordFail(acc.AuthID)
			if deps.logf != nil {
				deps.logf("warn", "定时续期未换发", map[string]any{
					"auth_id":     acc.AuthID,
					"note":        renewNote(res),
					"fail_count":  failCount,
					"retry_after": backoff.delayFor(failCount).String(),
				})
			}
		}(sa)
	}
	wg.Wait()

	if deps.logf != nil {
		deps.logf("info", "定时续期一轮结束", map[string]any{
			"accounts":    len(targets),
			"renewed":     int(renewed.Load()),
			"not_renewed": int(notRenewed.Load()),
		})
	}
	return int(renewed.Load())
}

// renewLoop 周期性执行续期，直到插件 shutdown。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，定时续期主循环。
func renewLoop() {
	renewLoopWith(renewStartupWait, hostReadyForRenew, defaultRenewDeps(), renewStopCh)
}

// renewLoopWith 是 renewLoop 的可注入实现。
//
// 参数化只为让单测用极短等待与假依赖驱动真实循环体：生产入口 renewLoop
// 始终传固定启动等待、宿主探针、生产依赖与全局停止信号。
// [参数] startupWait: 等待宿主就绪的上限；ready: 就绪探针；
//
//	deps: 单轮依赖；stop: 停止信号。
//
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，循环行为可测。
func renewLoopWith(startupWait time.Duration, ready func() bool, deps renewDeps, stop <-chan struct{}) {
	waitHostReadyForRenew(startupWait, ready)
	// 排空等待期间由 reconfigure 触发的 tick，避免紧接着重复跑一轮。
	select {
	case <-renewTickCh:
	default:
	}

	runRenewTick(deps)

	for {
		interval := currentConfig().RenewIntervalSeconds
		if interval <= 0 {
			interval = defaultConfig().RenewIntervalSeconds
		}
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-renewTickCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		select {
		case <-stop:
			return
		default:
		}
		runRenewTick(deps)
	}
}

// startRenewLoop 启动续期循环（只生效一次）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，宿主 init 时装配。
func startRenewLoop() {
	renewStartOnce.Do(func() {
		// 启动错峰：避免多个插件在同一时刻拉起后台循环形成内存与请求峰值。
		goAfterStartupJitter(renewLoop)
	})
}

// stopRenewLoop 通知循环退出。
//
// 不等待 goroutine 结束：宿主热重载会立即卸载动态库，阻塞等待反而会拖住
// 卸载流程。关闭 renewStopCh 能让正在 sleep 的循环立刻醒来并返回，
// 避免旧实例在卸载后仍持有 goroutine。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，热重载时停止循环。
func stopRenewLoop() {
	renewStopOnce.Do(func() { close(renewStopCh) })
}

// hostLogf 通过宿主日志接口上报一条插件日志。
//
// 插件运行在动态库内，没有独立日志出口；宿主的 host.log 是唯一能让
// `docker logs` 看到的通道，定时续期这类无 HTTP 触发的后台动作必须靠它
// 留痕，否则生产上无法判断续期是否真的发生。
// [参数] level: 日志级别；message: 日志正文；fields: 结构化字段。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，后台动作可观测。
func hostLogf(level, message string, fields map[string]any) {
	body, errMarshal := json.Marshal(map[string]any{
		"level":   level,
		"message": message,
		"fields":  fields,
	})
	if errMarshal != nil {
		return
	}
	_, _ = hostCall(pluginabi.MethodHostLog, body)
}
