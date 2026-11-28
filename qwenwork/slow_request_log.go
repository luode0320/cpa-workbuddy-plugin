// slow_request_log.go 记录「首字慢」的推理请求，供持续监控使用。
//
// 口径来自用户诉求：「有些推理请求很慢（首字大于 10s 的）」。插件必须能自己
// 看见这类请求——用量 feed 只落统计口径（ttft_ns / cached_tokens），一旦现场
// 没有样本，就无法区分「上游处理慢」「前缀缓存没命中」「账号本身慢」三种成因。
//
// 这不是新的计时口径：首字延迟仍由 sseUsageCollector.ttftNS 给出（基准早于
// 发请求），本文件只做「超过阈值就留痕 + 按账号节流」，不改变请求行为。
package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// slowRequestLogThreshold 是首字慢的观测阈值（毫秒）。
//
// 取 10s：与用户口径「首字大于 10s」一致。低于该值属正常长上下文首字开销，
// 逐条打日志只会淹没关键信息。
const slowRequestLogThreshold = 10 * time.Second

// slowRequestLogInterval 是同一账号两条慢请求日志之间的最小间隔。
//
// 慢请求往往成簇出现（同一次上游拥堵会连着命中多条），按账号节流保证日志里
// 仍能看出「哪些账号在慢」，而不是被同一个账号刷屏。
const slowRequestLogInterval = 30 * time.Second

var (
	slowRequestLogMu   sync.Mutex
	slowRequestLastLog = make(map[string]time.Time)
)

// logSlowRequest 记录一条首字超阈值的请求。
//
// [参数] authID：承载请求的账号标识；model：上游模型名；ttftNS：首字延迟纳秒值；
// detail：本次请求的用量明细（提供输入/缓存/输出 token 数）；total：请求总耗时。
// [返回] true 表示本次真的落了日志（未超阈值或被节流时为 false）。
// 最近修改时间：2026-11-28；改动原因：新增，首字慢请求的可观测埋点。
func logSlowRequest(authID, model string, ttftNS uint64, detail usage.Detail, total time.Duration) bool {
	if ttftNS == 0 {
		return false
	}
	ttft := time.Duration(ttftNS)
	if ttft < slowRequestLogThreshold {
		return false
	}
	key := strings.TrimSpace(authID)
	now := time.Now()

	slowRequestLogMu.Lock()
	if last, ok := slowRequestLastLog[key]; ok && now.Sub(last) < slowRequestLogInterval {
		slowRequestLogMu.Unlock()
		return false
	}
	slowRequestLastLog[key] = now
	slowRequestLogMu.Unlock()

	cacheRatio := 0.0
	if detail.InputTokens > 0 {
		cacheRatio = float64(detail.CachedTokens) / float64(detail.InputTokens)
	}
	log.Printf("[qwenwork] slow first token: auth=%s model=%s ttft=%dms total=%s input=%d cached=%d cache_ratio=%.3f output=%d reasoning=%d",
		key, model, ttft.Milliseconds(), total.Round(time.Millisecond),
		detail.InputTokens, detail.CachedTokens, cacheRatio,
		detail.OutputTokens, detail.ReasoningTokens)
	return true
}
