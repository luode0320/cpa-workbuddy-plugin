// failover_retry.go selects an alternate account for in-flight failover.
//
// When the executor hits an account-level 4xx (401/403/404/405) or 429, the
// request itself is fine — only this account is broken. Rather than failing
// the user, the executor tries the same request on a different traework
// account. pickNextAuth returns the next usable (authID, traeAuth) pair in a
// stable fallback order, skipping the current failing account and any that
// are disabled / cooling down / 测试失败 / failing-credits.
//
// Unlike qoderwork (per-account COSY signature over body+URL), the Trae
// llm_utils_chat request body is account-independent — only the auth headers
// (Bearer token + device fingerprint) change. Retrying means rebuilding the
// request with the new account's headers via rebuildRequestWithTraeAuth.
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

// pickNextAuth returns the next traework account to retry on after
// currentAuthID failed. It is best-effort: any error reading the host's auth
// list, loading a candidate's bundle, or evaluating a credential's state
// results in ok=false so the caller falls back to surfacing the original
// error to the user.
func pickNextAuth(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool) {
	return pickNextAuthForModel(currentAuthID, "")
}

// pickNextAuthForModel 在跳过冷却账号时带上模型维度。
//
// 传空模型时退化为纯账号维度判定，与历史 pickNextAuth 完全一致；传了模型时，
// 只在该模型冷却中的账号同样会被跳过——否则同请求换号仍会选到一个「这个模型
// 刚失败」的账号，换号等于没换。
//
// [参数] currentAuthID：当前失败账号；model：已归一化的上游模型名，可为空。
// [返回] 下一个可用账号标识、凭据与是否找到。
// 最近修改时间：2026-10-19 01:20:00；改动原因：新增账号×模型双层换号过滤。
func pickNextAuthForModel(currentAuthID, model string) (nextAuthID string, nextSA *traeAuth, ok bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	files, err := hostAuthList()
	if err != nil || len(files) == 0 {
		return "", nil, false
	}

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
		if isAuthModelCoolingDown(id, model) {
			continue
		}
		// 「测试」accounts are unusable by product definition — they must not
		// be picked up as an in-flight retry target either. Order stays
		// host-provided (no credit sort) so a mid-request switch remains
		// deterministic across the retry chain.
		if isTestFailed(id) {
			continue
		}
		// 跳过已耗尽或低于硬禁用线的账号，防止同请求换号再次中断。
		// 保活线不在这里过滤：它只降级排序，换号目标是「能接住这次请求」，
		// 一个 remain=19 的号仍远好过直接放弃整次请求。
		if _, lowOrExhausted := cachedCreditsScore(id); lowOrExhausted {
			continue
		}
		// 轮换冷却与故障冷却同属「暂时不该接流量」：换号若选到刚让位的号，
		// 轮换立刻被同一次重试抵消。
		if rotationCoolingDown(id, time.Now()) {
			continue
		}
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}

		sa, phys, loadErr := hostAuthGetBundle(f.AuthIndex)
		if loadErr != nil || sa == nil {
			_ = phys
			continue
		}

		// The credential must carry a usable access token; an empty token
		// would 401 identically and burn a budget slot.
		if !sa.hasToken() {
			continue
		}

		return id, sa, true
	}
	return "", nil, false
}

// rebuildRequestWithTraeAuth builds a fresh request against the same Trae
// llm_utils_chat endpoint with the same body, but authenticated for a
// different account. The caller keeps the original request untouched for its
// own retry bookkeeping; only the account-bound headers are rebuilt.
func rebuildRequestWithTraeAuth(sa *traeAuth, body []byte, apiHost string) (*http.Request, error) {
	if sa == nil {
		return nil, fmt.Errorf("rebuildRequestWithTraeAuth: nil traeAuth")
	}
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiHost+llmUtilsChatPath, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header = buildTraeAuthHeaders(sa)
	return req, nil
}

// readAllUpstreamErr drains the response body of a failed 4xx/5xx upstream
// call into a bounded byte slice so callers can pass it to
// noteAccountFailure / publishUsage without holding the body open. Empty
// bodies collapse to "" — callers already handle that.
func readAllUpstreamErr(r io.Reader) string {
	if r == nil {
		return ""
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(buf)
}

// parseUpstreamStatusFromErr extracts the HTTP status code from an error
// produced by the "upstream N: ..." error shape shared by the executor
// paths. Returns 0 for transport-level errors, parse failures, and nil.
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
