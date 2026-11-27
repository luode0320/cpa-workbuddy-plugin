package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// 本文件实现会话续期的定时循环。
//
// 宿主不会替插件驱动会话续期，必须由插件自己按间隔跑。循环在 configure
// 后重建，热重载时旧实例先 stop 再启新实例，避免两代定时器同时写盘。

const (
	// defaultRenewInterval 是会话续期的默认间隔。
	defaultRenewInterval = 4 * time.Hour

	// renewStartupWait 是等待宿主桥就绪的上限。
	renewStartupWait = 15 * time.Second

	// renewReadyPoll 是等待宿主就绪的轮询间隔。
	renewReadyPoll = 250 * time.Millisecond

	// renewConcurrency 是单轮续期的并发上限。
	renewConcurrency = 4

	// renewMaxBackoff 是账号续期失败退避的上限。
	renewMaxBackoff = 24 * time.Hour
)

// renewRuntime 保存定时器的运行期句柄。
type renewRuntime struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

var activeRenew renewRuntime

// renewDeps 是一轮续期的可注入依赖，供测试替换。
type renewDeps struct {
	listAccounts func() ([]*StoredAuth, error)
	renew        func(*StoredAuth) (*renewResult, error)
	apply        func(*StoredAuth, *renewResult, string) error
}

// defaultRenewDeps 返回生产环境的续期依赖。
// [参数] 无。
// [返回] 生产依赖。
// 最近修改时间：2026-11-27；改动原因：新增，生产依赖集中出口。
func defaultRenewDeps() renewDeps {
	return renewDeps{
		listAccounts: listAllAuthFiles,
		renew:        renewSession,
		apply: func(sa *StoredAuth, res *renewResult, digest string) error {
			return applyRenewal(sa, res, digest)
		},
	}
}

// startRenewLoop 按当前配置启动会话续期定时器。
// [参数] interval: 续期间隔，<=0 时用默认值。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，会话续期定时驱动。
func startRenewLoop(interval time.Duration) {
	stopRenewLoop()
	if interval <= 0 {
		interval = defaultRenewInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	activeRenew.mu.Lock()
	activeRenew.cancel = cancel
	activeRenew.done = done
	activeRenew.mu.Unlock()
	go renewLoop(ctx, interval, done, defaultRenewDeps())
}

// stopRenewLoop 停止会话续期定时器并等待协程退出。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，配合热重载与卸载清理。
func stopRenewLoop() {
	activeRenew.mu.Lock()
	cancel := activeRenew.cancel
	done := activeRenew.done
	activeRenew.cancel = nil
	activeRenew.done = nil
	activeRenew.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// renewLoop 定时驱动会话续期，启动即跑一轮。
// [参数] ctx: 取消上下文。
// [参数] interval: 续期间隔。
// [参数] done: 退出确认通道。
// [参数] deps: 续期依赖。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，续期循环主体。
func renewLoop(ctx context.Context, interval time.Duration, done chan struct{}, deps renewDeps) {
	defer close(done)
	waitHostReady(ctx)

	// 启动错峰：容器重启时多个插件会同时进来，错开首轮续期，避免瞬时内存与上游请求峰值。
	// 用 ctx 感知的等待而非常规 goroutine 延迟，保证 stopRenewLoop 能立刻返回、不拖慢配置热更新。
	if !waitStartupJitter(ctx) {
		return
	}

	// 1. 启动即跑一轮，让容器重启或热重载后立刻刷新会话。
	runRenewOnce(ctx, deps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runRenewOnce(ctx, deps)
		}
	}
}

// waitHostReady 等待宿主桥就绪，超时或无宿主时直接返回。
// [参数] ctx: 取消上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，冷启动时避免打空宿主 API。
func waitHostReady(ctx context.Context) {
	deadline := time.Now().Add(renewStartupWait)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		accounts, errList := listAllAuthFiles()
		if errList == nil && len(accounts) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(renewReadyPoll):
		}
	}
}

// runRenewOnce 对所有到期账号各跑一轮续期。
// [参数] ctx: 取消上下文。
// [参数] deps: 续期依赖。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，单轮续期并发调度。
func runRenewOnce(ctx context.Context, deps renewDeps) {
	accounts, errList := deps.listAccounts()
	if errList != nil {
		log.Printf("[doubao] renew skipped: list accounts failed: %v", errList)
		return
	}
	sem := make(chan struct{}, renewConcurrency)
	var wg sync.WaitGroup
	for _, sa := range accounts {
		if ctx.Err() != nil {
			break
		}
		if sa == nil || sa.Disabled {
			continue
		}
		if time.Now().Before(renewNextAt(sa)) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(account *StoredAuth) {
			defer wg.Done()
			defer func() { <-sem }()
			renewOneAccount(ctx, account, deps)
		}(sa)
	}
	wg.Wait()
}

// renewOneAccount 对单个账号执行一轮续期并写回。
// [参数] ctx: 取消上下文。
// [参数] sa: 账号。
// [参数] deps: 续期依赖。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，单账号续期与失败退避。
func renewOneAccount(ctx context.Context, sa *StoredAuth, deps renewDeps) {
	if ctx.Err() != nil {
		return
	}
	digest := ""
	if sa.PhysicalPath != "" {
		digest = fileDigest(sa.PhysicalPath)
	}
	res, errRenew := deps.renew(sa)
	if errRenew != nil {
		renewBackoffHit(sa.AuthID, errRenew.Error())
		log.Printf("[doubao] renew failed for %s: %v", sa.AuthID, errRenew)
		if isSessionExpiredError(errRenew.Error()) {
			markSessionInvalid(sa, errRenew.Error())
		}
		return
	}
	if !res.Renewed {
		renewBackoffHit(sa.AuthID, res.Note)
		log.Printf("[doubao] renew not rotated for %s: %s", sa.AuthID, res.Note)
		if isSessionExpiredError(res.Note) {
			markSessionInvalid(sa, res.Note)
		}
		return
	}
	if errApply := deps.apply(sa, res, digest); errApply != nil {
		log.Printf("[doubao] renew save failed for %s: %v", sa.AuthID, errApply)
		return
	}
	renewBackoffClear(sa.AuthID)
	log.Printf("[doubao] session renewed for %s (%s)", sa.AuthID, res.Note)
}

// renewBackoffHit 记录一次续期失败并累加退避。
// [参数] authID: 账号标识。
// [参数] reason: 失败原因。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，失败账号退避避免高频重试。
func renewBackoffHit(authID, reason string) {
	renewBackoffMu.Lock()
	defer renewBackoffMu.Unlock()
	cur := renewBackoff[authID]
	if cur == nil {
		cur = &renewBackoffState{}
		renewBackoff[authID] = cur
	}
	cur.Failures++
	cur.LastReason = reason
	cur.NextAt = time.Now().Add(renewBackoffDelay(cur.Failures))
}

// renewBackoffClear 清除账号的续期失败退避。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，成功即清零。
func renewBackoffClear(authID string) {
	renewBackoffMu.Lock()
	defer renewBackoffMu.Unlock()
	delete(renewBackoff, authID)
}

// renewNextAt 返回账号允许下次续期的最早时间。
// [参数] sa: 账号。
// [返回] 最早续期时间；无退避时返回零值。
// 最近修改时间：2026-11-27；改动原因：新增，退避账号跳过本轮。
func renewNextAt(sa *StoredAuth) time.Time {
	renewBackoffMu.RLock()
	defer renewBackoffMu.RUnlock()
	state := renewBackoff[sa.AuthID]
	if state == nil {
		return time.Time{}
	}
	return state.NextAt
}

// renewBackoffDelay 按连续失败次数计算退避时长。
// [参数] failures: 连续失败次数。
// [返回] 退避时长。
// 最近修改时间：2026-11-27；改动原因：新增，退避封顶 24 小时。
func renewBackoffDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := time.Duration(1<<uint(minInt(failures-1, 12))) * time.Hour
	if d > renewMaxBackoff {
		d = renewMaxBackoff
	}
	return d
}

// minInt 返回两个整数中较小者。
// [参数] a: 第一个整数。
// [参数] b: 第二个整数。
// [返回] 较小值。
// 最近修改时间：2026-11-27；改动原因：新增，退避指数封顶。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// renewBackoffState 是单个账号的续期退避状态。
type renewBackoffState struct {
	Failures   int
	LastReason string
	NextAt     time.Time
}

var (
	renewBackoffMu sync.RWMutex
	renewBackoff   = map[string]*renewBackoffState{}
)
