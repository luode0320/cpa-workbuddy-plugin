package main

import (
	"encoding/json"
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
}

// defaultRenewDeps 返回生产依赖：宿主账号列表、真实续期与宿主日志。
// [参数] 无。
// [返回] 依赖集合。
// 最近修改时间：2026-10-18；改动原因：新增，生产实现装配。
func defaultRenewDeps() renewDeps {
	return renewDeps{
		list: listAllAuthFiles,
		renew: func(sa *StoredAuth) (*renewResult, error) {
			res, errRenew := renewSession(sa)
			if errRenew != nil {
				return nil, errRenew
			}
			if errApply := applyRenewal(sa, res); errApply != nil {
				return nil, errApply
			}
			return res, nil
		},
		logf: hostLogf,
	}
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

	sem := make(chan struct{}, renewConcurrency)
	var wg sync.WaitGroup
	var renewed atomic.Int32
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
				if deps.logf != nil {
					deps.logf("warn", "定时续期失败", map[string]any{
						"auth_id": acc.AuthID,
						"error":   errRenew.Error(),
					})
				}
				return
			}
			if res != nil && res.Renewed {
				renewed.Add(1)
			}
			if deps.logf != nil {
				deps.logf("info", "定时续期完成", map[string]any{
					"auth_id": acc.AuthID,
					"note":    renewNote(res),
				})
			}
		}(sa)
	}
	wg.Wait()

	if deps.logf != nil {
		deps.logf("info", "定时续期一轮结束", map[string]any{
			"accounts": len(targets),
			"renewed":  int(renewed.Load()),
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
		go renewLoop()
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
