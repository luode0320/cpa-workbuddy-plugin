package main

import (
	"sync"
	"time"
)

// 本文件实现后台积分刷新循环。
//
// 为什么必须有它：执行入口的积分闸门只读缓存（见 policy.go），
// 缓存不更新闸门就是瞎的——账号可能早就耗尽，闸门却仍以为有 500 分。
// 面板的按需查询不足以为此负责：没有用户打开面板时它根本不会跑。
//
// 循环语义与续期循环一致：启动后先等宿主桥就绪，再立即跑一轮，
// 之后按 credit_refresh_interval_seconds（默认 300 秒）周期执行；
// reconfigure 触发的 tick 会合并成一次立即重排。
//
// 失败处理：单个账号刷新失败只记日志，不写入缓存——一次网络抖动
// 不该让闸门把好号当成未知或低额。

const (
	// creditStartupWait 是等待宿主桥就绪的上限。
	creditStartupWait = 15 * time.Second
)

var (
	// creditTickCh 合并外部触发（reconfigure）到同一轮。
	creditTickCh = make(chan struct{}, 1)

	// creditStopCh 在插件 shutdown 时关闭，用于立即唤醒循环。
	creditStopCh   = make(chan struct{})
	creditStopOnce sync.Once

	// creditStartOnce 保证循环只启动一次。
	creditStartOnce sync.Once
)

// requestCreditTick 请求立即执行一轮积分刷新（非阻塞）。
// [参数] 无。
// [返回] 无。
func requestCreditTick() {
	select {
	case creditTickCh <- struct{}{}:
	default:
	}
}

// startCreditLoop 启动积分刷新循环（只生效一次）。
// [参数] 无。
// [返回] 无。
func startCreditLoop() {
	creditStartOnce.Do(func() {
		go creditLoop()
	})
}

// stopCreditLoop 通知循环退出。
//
// 与续期循环同口径：只关闭 channel 不等待 goroutine，
// 宿主热重载会立即卸载动态库，阻塞等待会拖住卸载流程。
// [参数] 无。
// [返回] 无。
func stopCreditLoop() {
	creditStopOnce.Do(func() { close(creditStopCh) })
}

// creditLoop 是积分刷新的主循环。
// [参数] 无。
// [返回] 无。
func creditLoop() {
	if !waitHostReadyForRenew(creditStartupWait, hostReadyForRenew) {
		hostLogf("warn", "积分刷新循环未等到宿主就绪，仍按计划继续", nil)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-creditStopCh:
			return
		case <-creditTickCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
		case <-timer.C:
			runCreditTick(defaultCreditDeps())
			timer.Reset(loadedCreditRefreshInterval())
		}
	}
}

// loadedCreditRefreshInterval 返回当前生效的积分刷新间隔。
// [参数] 无。
// [返回] 刷新间隔；配置非法时回退默认值。
func loadedCreditRefreshInterval() time.Duration {
	seconds := currentConfig().CreditRefreshSeconds
	if seconds <= 0 {
		seconds = creditRefreshDefaultSeconds
	}
	return time.Duration(seconds) * time.Second
}

// creditDeps 是一轮积分刷新的可注入依赖。
//
// 抽出来只为让单测在不碰宿主、不发真实请求的前提下覆盖过滤与失败隔离。
// [参数] 无。
// [返回] 无。
type creditDeps struct {
	list  func() ([]*StoredAuth, error)
	fetch func(*StoredAuth) (*creditSummary, error)
	store func(*StoredAuth, *creditSummary)
	// claim 领取当日登录积分；为 nil 时跳过领取。
	//
	// 必须在 fetch 之前执行：check 端点触发发放后余额才会变，
	// 顺序反了会让本轮余额仍是领取前的旧值，闸门继续把账号判成 0 分。
	claim func(*StoredAuth) bool
	// confirm 用刚查到的积分摘要确认领取是否真的到账；为 nil 时跳过确认。
	//
	// 上游的 check 响应不携带发放结果（见 daily_credit.go），
	// 到账事实只能从明细里读，因此确认必须在 fetch 之后。
	confirm func(*StoredAuth, *creditSummary)
	logf    func(level, message string, fields map[string]any)
}

// defaultCreditDeps 返回生产依赖：宿主账号列表、真实积分查询与宿主日志。
// [参数] 无。
// [返回] 依赖集合。
func defaultCreditDeps() creditDeps {
	return creditDeps{
		list:    listAllAuthFiles,
		fetch:   fetchCreditSummary,
		store:   storeCreditSummary,
		claim:   claimDailyCreditForAccount,
		confirm: confirmDailyCreditBySummary,
		logf:    hostLogf,
	}
}

// storeCreditSummary 把积分摘要写入缓存。
// [参数] sa: 账号；summary: 积分摘要。
// [返回] 无。
func storeCreditSummary(sa *StoredAuth, summary *creditSummary) {
	if sa == nil || summary == nil {
		return
	}
	key := sa.AuthID
	if key == "" {
		key = sa.SecUserID
	}
	if key == "" {
		return
	}
	creditsCache.mu.Lock()
	creditsCache.entries[key] = creditCacheEntry{summary: *summary, fetched: time.Now()}
	creditsCache.mu.Unlock()
}

// runCreditTick 执行一轮积分刷新。
//
// 并发上限由 creditRefreshConcurrency 控制：串行在账号多时会把一轮
// 拖到分钟级，导致闸门读到的数据长期滞后。
// [参数] deps: 可注入依赖。
// [返回] 刷新成功的账号数。
func runCreditTick(deps creditDeps) int {
	accounts, errList := deps.list()
	if errList != nil {
		deps.logf("warn", "积分刷新读取账号失败", map[string]any{"error": errList.Error()})
		return 0
	}

	type target struct {
		sa *StoredAuth
	}
	queue := make(chan target)
	var wg sync.WaitGroup
	var mu sync.Mutex
	refreshed := 0

	workers := creditRefreshConcurrency
	if workers > len(accounts) {
		workers = len(accounts)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				// 先领当日登录积分再查余额：领取会改变余额，
				// 顺序反了本轮拿到的仍是领取前的旧值，闸门会继续把
				// 账号判成 0 分并把请求挡回宿主（见 daily_credit.go）。
				if deps.claim != nil {
					deps.claim(item.sa)
				}
				summary, errFetch := deps.fetch(item.sa)
				if errFetch != nil {
					deps.logf("warn", "积分刷新失败", map[string]any{
						"auth_id": item.sa.AuthID,
						"error":   errFetch.Error(),
					})
					continue
				}
				// 到账事实只能从明细确认：check 的响应不携带发放结果。
				if deps.confirm != nil {
					deps.confirm(item.sa, summary)
				}
				deps.store(item.sa, summary)
				mu.Lock()
				refreshed++
				mu.Unlock()
			}
		}()
	}

	for _, sa := range accounts {
		if sa == nil || sa.Disabled {
			continue
		}
		select {
		case <-creditStopCh:
			close(queue)
			wg.Wait()
			publishCreditPool(accounts)
			return refreshed
		case queue <- target{sa: sa}:
		}
	}
	close(queue)
	wg.Wait()
	// 刷新完成后再发布快照：先发布会让闸门读到上一轮的余额。
	publishCreditPool(accounts)
	return refreshed
}
