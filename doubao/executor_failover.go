package main

import (
	"context"
	"errors"
	"time"
)

// 本文件实现执行期的账号故障转移。
//
// 与 failover.go 的分工：那个文件只维护冷却状态与选号，本文件负责在一次具体
// 请求里把「失败 → 记账 → 换号 → 重试」串起来。

// runWithFailover 执行一次对话，失败时按冷却策略换号重试。
//
// 每次失败先记账（决定是否冷却该账号），再判断是否值得同请求换号：
// 429 / 额度耗尽 / 登录态失效值得立刻换号；5xx 与传输错误只记冷却，不烧预算。
// 换号时排除刚失败的账号，避免又打回去。
// [参数] sa: 首选账号；timeout: 单次尝试超时；mode: 生成模式；prompt: 提示词；
// onDelta: 文本增量回调。
// [返回] 聚合结果与错误；全部账号失败时返回最后一个错误。
// 最近修改时间：2026-10-17；改动原因：新增，TASK-008 同请求换号。
func runWithFailover(sa *StoredAuth, timeout time.Duration, mode generationMode, prompt string, onDelta func(string)) (*completionResult, error) {
	const maxAttempts = 3

	current := sa
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if current == nil {
			lastErr = errNoAvailableAccount
			break
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		res, errRun := collectCompletion(ctx, current, prompt, mode, "", true, onDelta)
		cancel()

		if errRun == nil {
			resetAccountFailover(current.AuthID)
			return res, nil
		}
		lastErr = errRun

		status, code := classifyUpstreamError(errRun)
		noteAccountFailure(current.AuthID, status, code)
		if !shouldRotate(status, code) {
			break
		}

		next, errPick := pickAccount(current.AuthID)
		if errPick != nil {
			break
		}
		current = next
	}

	if lastErr == nil {
		lastErr = errNoAvailableAccount
	}
	return nil, lastErr
}

// classifyUpstreamError 从错误中取出状态码与上游业务码，供故障分类使用。
// [参数] err: 上游错误。
// [返回] HTTP 状态码与业务码；非上游错误时两者均为 0。
// 最近修改时间：2026-10-17；改动原因：新增，故障分类入参提取。
func classifyUpstreamError(err error) (int, int) {
	var up *upstreamError
	if errors.As(err, &up) {
		return up.StatusCode, up.Code
	}
	return 0, 0
}
