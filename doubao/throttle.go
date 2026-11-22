package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

// 本文件是账号级上游闸门的唯一实现：同一账号同时只允许有限个在途请求，两次发起
// 之间保持最小间隔，被上游判为访问频繁后进入短时软退避。
//
// 对话、消息链、保活、模型发现四类出上游的调用都必须经 acquireUpstream 取槽、
// releaseUpstream 归还，不得绕开本文件直接调 client.Do。

// upstreamKind 标识一次上游调用的用途。
type upstreamKind int

const (
	// upstreamKindChat 是对话与生成主链路。
	upstreamKindChat upstreamKind = iota
	// upstreamKindChain 是消息链回灌轮询。
	upstreamKindChain
	// upstreamKindProbe 是保活探测与模型发现等后台请求。
	upstreamKindProbe
)

// rateLimitFrequentCode 是上游「当前服务访问频繁」的业务码。
const rateLimitFrequentCode = 710022002

const (
	// defaultAccountMaxInflight 是同一账号默认最大在途请求数。
	defaultAccountMaxInflight = 1
	// defaultAccountMinIntervalMs 是同一账号两次请求的默认最小间隔（毫秒）。
	defaultAccountMinIntervalMs = 2000
	// defaultRateLimitBackoffSeconds 是被判访问频繁后的默认软退避时长（秒）。
	defaultRateLimitBackoffSeconds = 60
)

// upstreamTicket 是一次已取到的上游发起许可。
type upstreamTicket struct {
	// authID 是许可所属账号。
	authID string
	// kind 是本次调用的用途。
	kind upstreamKind
}

// accountGate 是单个账号的闸门状态。
type accountGate struct {
	// inflight 是当前在途请求数。
	inflight int
	// nextAt 是下一次允许发起的时刻。
	nextAt time.Time
	// softUntil 是软退避截止时刻；零值表示未退避。
	softUntil time.Time
}

var (
	// upstreamGateMu 保护 upstreamGates 与 upstreamGateChanged。
	upstreamGateMu sync.Mutex
	// upstreamGates 按账号标识保存闸门状态。
	upstreamGates = make(map[string]*accountGate)
	// upstreamGateChanged 在每次归还许可时关闭并替换，用来唤醒排队者。
	upstreamGateChanged = make(chan struct{})
)

// acquireUpstream 取得某账号的一次上游发起许可，必要时排队等待。
//
// 等待上限由调用方的 ctx 决定：每个请求本身都带超时，闸门不再叠加第二层等待上限。
// [参数] ctx: 上下文；authID: 账号标识；kind: 调用用途。
// [返回] 许可票据与错误；ctx 结束时返回 ctx 的错误。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，账号级并发与速率闸门。
func acquireUpstream(ctx context.Context, authID string, kind upstreamKind) (*upstreamTicket, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return &upstreamTicket{kind: kind}, nil
	}

	for {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}

		now := time.Now()
		setting := effectiveThrottleSetting()
		maxInflight := setting.maxInflight
		minInterval := setting.minInterval

		upstreamGateMu.Lock()
		gate := upstreamGates[authID]
		if gate == nil {
			gate = &accountGate{}
			upstreamGates[authID] = gate
		}

		// 1. 软退避最优先：窗口内该账号不发起任何请求。
		if now.Before(gate.softUntil) {
			wait := gate.softUntil.Sub(now)
			changed := upstreamGateChanged
			upstreamGateMu.Unlock()
			if errWait := waitUpstreamGate(ctx, changed, wait); errWait != nil {
				return nil, errWait
			}
			continue
		}

		// 2. 在途已满：等任意一次归还。
		if gate.inflight >= maxInflight {
			changed := upstreamGateChanged
			upstreamGateMu.Unlock()
			if errWait := waitUpstreamGate(ctx, changed, 0); errWait != nil {
				return nil, errWait
			}
			continue
		}

		// 3. 未到最小间隔：等到该时刻。
		if now.Before(gate.nextAt) {
			wait := gate.nextAt.Sub(now)
			changed := upstreamGateChanged
			upstreamGateMu.Unlock()
			if errWait := waitUpstreamGate(ctx, changed, wait); errWait != nil {
				return nil, errWait
			}
			continue
		}

		gate.inflight++
		gate.nextAt = now.Add(minInterval)
		upstreamGateMu.Unlock()
		return &upstreamTicket{authID: authID, kind: kind}, nil
	}
}

// releaseUpstream 归还许可，上游判为访问频繁时顺带写入该账号的软退避。
// [参数] ticket: acquireUpstream 返回的票据，可为 nil；err: 本次调用的错误，可为 nil。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，归还许可并记录软退避。
func releaseUpstream(ticket *upstreamTicket, err error) {
	if ticket == nil {
		return
	}
	_, code := classifyUpstreamError(err)

	upstreamGateMu.Lock()
	if gate := upstreamGates[ticket.authID]; gate != nil {
		if gate.inflight > 0 {
			gate.inflight--
		}
		if code == rateLimitFrequentCode {
			gate.softUntil = time.Now().Add(rateLimitBackoffValue())
		}
	}
	close(upstreamGateChanged)
	upstreamGateChanged = make(chan struct{})
	upstreamGateMu.Unlock()
}

// waitUpstreamGate 等待闸门状态变化或超时，wait <= 0 时只等状态变化。
// [参数] ctx: 上下文；changed: 状态变化信号；wait: 最长等待时长。
// [返回] ctx 的错误；正常唤醒或到时返回 nil。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，闸门排队原语。
func waitUpstreamGate(ctx context.Context, changed <-chan struct{}, wait time.Duration) error {
	if wait <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
			return nil
		}
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-timer.C:
		return nil
	}
}

// rateLimitBackoffValue 返回当前生效的软退避时长。
// [参数] 无。
// [返回] 软退避时长。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，读取软退避配置。
func rateLimitBackoffValue() time.Duration {
	return effectiveThrottleSetting().backoff
}

// softBackoffRemaining 返回账号软退避的剩余时长，未退避时为 0。
// [参数] authID: 账号标识。
// [返回] 剩余时长。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，供面板与用例观察退避状态。
func softBackoffRemaining(authID string) time.Duration {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}

	upstreamGateMu.Lock()
	defer upstreamGateMu.Unlock()
	gate := upstreamGates[authID]
	if gate == nil {
		return 0
	}
	if remaining := time.Until(gate.softUntil); remaining > 0 {
		return remaining
	}
	return 0
}

// resetUpstreamGates 清空全部闸门状态（仅测试使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，避免测试间状态串扰。
func resetUpstreamGates() {
	upstreamGateMu.Lock()
	defer upstreamGateMu.Unlock()
	upstreamGates = make(map[string]*accountGate)
	upstreamGateChanged = make(chan struct{})
}

// throttleSettingOverride 是闸门设置的测试覆盖值；nil 表示走正常读取路径。
//
// 存在的理由：把轮询节奏压到毫秒级的用例（video_chain_test.go 的
// swapVideoPollTiming）会让闸门的最小间隔成为新的瓶颈，用例本该几百毫秒
// 结束却要等满 2000ms 一轮。用例需要能把闸门一起放开。
var throttleSettingOverride *throttleSetting

// swapThrottleSetting 临时覆盖闸门设置，返回恢复函数。
// [参数] setting: 覆盖值；传零值表示使用最小间隔 0 与较大的在途上限。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，让压测与轮询用例能绕开真实节流。
func swapThrottleSetting(setting throttleSetting) func() {
	prev := throttleSettingOverride
	throttleSettingOverride = &setting
	resetUpstreamGates()
	return func() {
		throttleSettingOverride = prev
		resetUpstreamGates()
	}
}
