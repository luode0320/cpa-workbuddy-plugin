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
//
// 两点与视频强相关：
//   - 续接请求（resume 非零）一律不换号。上游只认建会话的那个账号，换号后
//     conversation_id 在新账号上不存在，等于把一次注定失败的追问打到健康账号上，
//     既救不回来，还会把它记进冷却。
//   - 返回值带上实际执行成功的账号。产物回灌必须用这个账号，不能用调用方给的
//     首选账号 —— 换号后两者可能不同。
//
// [参数] sa: 首选账号；timeout: 单次尝试超时；mode: 生成模式；prompt: 提示词；
// resume: 会话续接点（零值表示新会话）；onDelta: 文本增量回调。
// [返回] 聚合结果、实际执行成功的账号与错误；全部尝试失败时账号为 nil。
// 最近修改时间：2026-11-21 00:22:00；改动原因：续接请求禁止换号，并回传实际执行账号供回灌使用。
func runWithFailover(sa *StoredAuth, timeout time.Duration, mode generationMode, prompt string, resume resumePoint, onDelta func(string), params ...videoParams) (*completionResult, *StoredAuth, error) {
	const maxAttempts = 3

	current := sa
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if current == nil {
			lastErr = errNoAvailableAccount
			break
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		res, errRun := collectCompletionWithResume(ctx, current, prompt, mode, resume, resume.isZero(), onDelta, params...)
		cancel()

		if errRun == nil {
			resetAccountFailover(current.AuthID)
			// 1. 首轮把会话钉在真正跑通的账号上，确认轮才能回到同一个账号。
			if resume.isZero() && res != nil {
				rememberSessionAuth(res.ConversationID, current.AuthID)
			}
			return res, current, nil
		}
		lastErr = errRun

		status, code := classifyUpstreamError(errRun)
		noteAccountFailure(current.AuthID, status, code)

		// 2. 续接请求到此为止：会话已经绑死在当前账号上，换号只会污染下一个账号。
		if !resume.isZero() {
			break
		}
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
	return nil, nil, lastErr
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
