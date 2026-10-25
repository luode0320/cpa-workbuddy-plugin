// failover_retry.go selects an alternate account for in-flight failover.
//
// When the executor hits an account-level 4xx (401/403/404/405), the request
// itself is fine — only this account is broken. Rather than failing the
// user, the executor tries the same request on a different qoderwork
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
// Unlike the workbuddy plugin, qoderwork signs every request with COSY
// (applyCosyHeaders) against a fixed gateway endpoint, so a retry rebuild
// means: same encoded body + same endpoint, fresh COSY signature for the
// new account. rebuildRequestWithQoderAuth encapsulates that.
package main

import (
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

// pickNextAuth returns the next qoderwork account to retry on after
// currentAuthID failed. It is best-effort: any error reading the host's
// auth list, loading a candidate's bundle, or evaluating a credential's
// state results in ok=false so the caller falls back to surfacing the
// original error to the user.
//
// ok=false means no alternate account was usable; the caller should stop
// retrying and propagate the last upstream error.
// 变参 model 让带模型的调用点（流式/非流式主路径）额外过滤「该模型上正慢速
// 冷却」的候选；不传时退化为纯账号维度，与历史行为完全一致。
func pickNextAuth(currentAuthID string, model ...string) (nextAuthID string, nextSA *storedAuth, ok bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	slowModel := ""
	if len(model) > 0 {
		slowModel = strings.TrimSpace(model[0])
	}
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
		// 轮换冷却的账号同样不承接换号流量：它被让位的原因是「用得太久」，
		// 不是「坏了」，但继续给它加压会让轮换目标落空。
		if rotationCoolingDown(id, time.Now()) {
			continue
		}
		// 慢速冷却：换到刚被判慢的号等于把慢请求换个来源。只有调用方给了模型
		// 才判定——该判据本身是「账号×模型」粒度，无模型时无从比较。
		if slowModel != "" && slowCoolingDown(id, slowModel, time.Now()) {
			continue
		}
		// 跳过残血账号（正积分低于安全红线）；0 积分账号可继续走免费模型
		if _, lowCredits := cachedCreditsScore(id); lowCredits {
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
		// identically (COSY would also fail to build a session).
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

// rebuildRequestWithQoderAuth builds a fresh request against the same
// QoderWork gateway with the same encoded body, but signed for a different
// account. COSY headers are per-account (session + signature over body+URL),
// so every account swap must re-run applyCosyHeaders.
//
// The caller keeps the original request untouched for its own retry book-
// keeping; this function only needs the (account-independent) encoded body
// and the upstream model key that goes into x-model-key. When an optional
// ctx is passed, it is inherited so cancellation and timeouts propagate.
func rebuildRequestWithQoderAuth(sa *storedAuth, encodedBody, modelKey string, ctx ...context.Context) (*http.Request, error) {
	if sa == nil {
		return nil, fmt.Errorf("rebuildRequestWithQoderAuth: nil stored auth")
	}
	reqCtx := context.Background()
	if len(ctx) > 0 && ctx[0] != nil {
		reqCtx = ctx[0]
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointChat, strings.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}
	if err := applyCosyHeaders(req, sa, encodedBody, endpointChat, modelKey, true); err != nil {
		return nil, fmt.Errorf("cosy: %w", err)
	}
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
