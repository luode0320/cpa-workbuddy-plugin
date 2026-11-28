// failover_retry.go selects an alternate account for in-flight failover.
//
// When the executor hits an account-level 4xx (401/403/404/405), the request
// itself is fine — only this account is broken. Rather than failing the
// user, the executor tries the same request on a different qwenwork
// account. pickNextAuth returns the next usable (authID, storedAuth) pair
// in a stable fallback order, skipping the current failing account and any
// that are disabled / cooling down / failing-credits.
//
// The selection intentionally mirrors pickActiveAuth's filter rules but
// is intentionally simpler (no random spread): when a request is already
// in flight and one account just failed, deterministically trying the next
// available one in host-reported order keeps recovery path predictable
// across logs.
//
// QwenWork 的鉴权是账号级 MaaS 票据（sk-mm-v1），每个账号一张，所以换号
// 必须重新签发并替换 Authorization。rebuildRequestWithQwenAuth 封装该逻辑。
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// failoverBackoff 是同请求换号前的最小退避：上游容量类故障（403/5xx 风暴）时，
// 无退避的连续换号会把单请求放大成瞬时 11 连发，多个并发请求叠加即成请求风暴
// （生产 OOM 次级放大器）。50ms 足以把风暴斜率压低一个数量级，
// 对端到端时延的影响上限为预算 × 50ms（默认 10 次 → 500ms）。
const failoverBackoff = 50 * time.Millisecond

// pickNextAuth returns the next qwenwork account to retry on after
// currentAuthID failed. It is best-effort: any error reading the host's
// auth list, loading a candidate's bundle, or evaluating a credential's
// state results in ok=false so the caller falls back to surfacing the
// original error to the user.
//
// ok=false means no alternate account was usable; the caller should stop
// retrying and propagate the last upstream error.
func pickNextAuth(currentAuthID string, model ...string) (nextAuthID string, nextSA *storedAuth, ok bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	files, err := hostAuthList()
	if err != nil || len(files) == 0 {
		return "", nil, false
	}

	// First pass: find the first file-entry whose host-reported ID is
	// NOT currentAuthID and passes the cheap filters (disabled,
	// cooling down, anomalously quarantined). Order is host-provided so
	// successive retries walk the same predictable path.
	normCurrent := normalizeFailoverKey(currentAuthID)
	for _, f := range files {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			continue
		}
		if id == currentAuthID || (normCurrent != "" && normalizeFailoverKey(id) == normCurrent) {
			continue
		}
		if f.Disabled {
			continue
		}
		if isAccountCoolingDown(id) {
			continue
		}
		// 积分准入按请求模型判定：免费模型放行 0 积分账号，付费模型跳过耗尽与残血号。
		if !accountUsableForModel(id, firstModelArg(model)) {
			continue
		}
		// Skip candidates with no usable auth index — we can't load
		// their stored body, so retrying on them would fail
		// identically and burn a budget slot.
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}

		sa, phys, loadErr := hostAuthGetBundle(f.AuthIndex)
		if loadErr != nil || sa == nil {
			// Treat as not-usable; keep walking. Don't surface the
			// load error — there may be a viable next candidate.
			_ = phys
			continue
		}

		// Defensive: ensure the stored auth actually carries a token
		// worth retrying on. An empty Access/Refresh would 401
		// identically (no MaaS ticket could be minted).
		if strings.TrimSpace(sa.Auth.AccessToken) == "" && strings.TrimSpace(sa.Auth.RefreshToken) == "" {
			continue
		}

		// 选定换号目标后、返回给调用方之前统一退避：所有换号路径（执行器
		// 循环、流式循环、前置冷却拦截）都经过本函数，单点收口避免上游
		// 容量故障时被 1+N 连发放大成请求风暴。
		time.Sleep(failoverBackoff)
		return id, sa, true
	}
	return "", nil, false
}

// rebuildRequestWithQwenAuth 用指定账号重建一个 MaaS 推理请求。
//
// QwenWork 的鉴权是账号级 MaaS 票据（sk-mm-v1），每个账号一张，所以换号
// 必须重新签发并替换 Authorization，不能复用上一个账号的头。
//
// [参数] sa 目标账号；body 原始请求体；maasSessionKey 上游 MaaS 会话键（可空）；
// ctx 可选上下文。
// [返回] 可直接发送的请求；签发失败返回错误。
// 最近修改时间：2026-11-28；改动原因：会话键语义拆分，换号后仍落同一上游会话。
func rebuildRequestWithQwenAuth(sa *storedAuth, body []byte, maasSessionKey string, ctx ...context.Context) (*http.Request, error) {
	if sa == nil {
		return nil, fmt.Errorf("rebuildRequestWithQwenAuth: nil stored auth")
	}
	reqCtx := context.Background()
	if len(ctx) > 0 && ctx[0] != nil {
		reqCtx = ctx[0]
	}
	sess, err := ensureMaasSession(sa, maasSessionKey)
	if err != nil {
		return nil, fmt.Errorf("maas: %w", err)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, maasInferenceURL(sess, "/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyMaasHeaders(req, sess)
	return req, nil
}

// readAllUpstreamErr drains the response body of a failed 4xx/5xx upstream
// call into a bounded byte slice so we can pass it to noteAccountFailure /
// publishUsage without holding the body open indefinitely. Empty bodies
// collapse to "" — callers already handle that.
//
// We intentionally read via the same host stream bridge as the success
// path so the framing behavior (chunked, capped) stays consistent.
// 上限对齐仓库惯例 1MiB：错误体只需前几百字节可读信息，无上限读取在
// 上游异常吐大 body 时会造成内存放大（OOM 次级放大器之一）。
func readAllUpstreamErr(r io.Reader) string {
	if r == nil {
		return ""
	}
	buf, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return ""
	}
	return string(buf)
}

// parseUpstreamStatusFromErr extracts the HTTP status code from an error
// produced by the "upstream N: ..." error shape shared by doExecuteOnceQoder
// / collectUpstreamStreamQoder / pumpUpstreamStream. Returns 0 for
// transport-level errors, parse failures, and nil.
func parseUpstreamStatusFromErr(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "upstream ") {
		return 0
	}
	rest := strings.TrimPrefix(msg, "upstream ")
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return 0
	}
	n, perr := strconv.Atoi(strings.TrimSpace(rest[:idx]))
	if perr != nil {
		return 0
	}
	return n
}
