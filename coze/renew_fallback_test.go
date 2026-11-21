package main

import (
	"errors"
	"strings"
	"testing"
)

// 本文件覆盖续期降级链（TASK-08）。
//
// 背景：扣子把裸 HTTP 打令牌端点按客户端特征封了——同一份 Cookie，Go
// 进程直连 /passport/token/beat/v2/ 得「会话过期」，真实页面内同源 fetch
// 却能换发（2026-11-21 实测）。差别不在 Cookie，在 TLS 指纹 / UA / 埋点
// Cookie / 浏览器侧前置会话状态。因此裸 HTTP 判失效后再用真浏览器兜一次。
//
// 这里锁定的是「何时降级」与「降级失败怎么呈现」两条判定，真实浏览器
// 行为由 browser_keep_test.go 与 cdp_test.go 各自覆盖，本文件全程用桩，
// 不发任何上游请求、不起任何进程。

// stubRenewSession 替换裸 HTTP 换发实现。
// [参数] t: 测试上下文；fn: 桩实现。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，降级链用例共用。
func stubRenewSession(t *testing.T, fn func(*StoredAuth) (*renewResult, error)) {
	t.Helper()
	prev := renewSessionFn
	renewSessionFn = fn
	t.Cleanup(func() { renewSessionFn = prev })
}

// stubBrowserRevive 替换浏览器保活实现，并记录调用次数。
// [参数] t: 测试上下文；fn: 桩实现。
// [返回] 指向调用计数的指针。
// 最近修改时间：2026-11-21；改动原因：新增，锁定「只兜一次」与「不该调就不调」。
func stubBrowserRevive(t *testing.T, fn func(*StoredAuth) (*renewResult, error)) *int {
	t.Helper()
	calls := 0
	prev := browserReviveFn
	browserReviveFn = func(sa *StoredAuth) (*renewResult, error) {
		calls++
		return fn(sa)
	}
	t.Cleanup(func() { browserReviveFn = prev })
	return &calls
}

// newFallbackAuth 准备一个可落盘的账号。
// [参数] t: 测试上下文。
// [返回] 账号与错误。
// 最近修改时间：2026-11-21；改动原因：新增，复用 credentials_guard_test.go 的落盘夹具。
func newFallbackAuth(t *testing.T) (*StoredAuth, error) {
	t.Helper()
	// 降级链会走 applyRenewal，活跃实例闸门必须放行，否则「是否落盘」
	// 这个观测点会被闸门吃掉，用例就测不到真实行为。
	stubRenewCanWrite(t, true)
	return newWritableAuth(t)
}

// TestRenewWithBrowserFallback_SkipsBrowserWhenPlainSucceeds 验证裸 HTTP 成功时不启动浏览器。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，浏览器是兜底不是常规路径。
func TestRenewWithBrowserFallback_SkipsBrowserWhenPlainSucceeds(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return &renewResult{Renewed: true, CookieHeader: "sessionid=fresh", Note: "双向换发完成"}, nil
	})
	calls := stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		t.Fatal("裸 HTTP 已成功，不得启动浏览器")
		return nil, nil
	})

	res, errRenew := renewWithBrowserFallback(sa)
	if errRenew != nil {
		t.Fatalf("不应报错，实际 %v", errRenew)
	}
	if res == nil || !res.Renewed {
		t.Fatal("应返回成功结果")
	}
	if *calls != 0 {
		t.Fatalf("浏览器保活调用次数应为 0，实际 %d", *calls)
	}
	if sa.CookieHeader != "sessionid=fresh" {
		t.Fatalf("应落盘新凭据，实际 %q", sa.CookieHeader)
	}
}

// TestRenewWithBrowserFallback_NonSessionErrorDoesNotFallBack 验证非会话类错误不进降级。
//
// 网络抖动、限流、5xx 与「会话过期」是两种病：前者过一会儿自己会好，
// 起浏览器只是白跑一趟 90 秒超时。降级必须只针对会话被判无效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，避免降级被网络问题触发。
func TestRenewWithBrowserFallback_NonSessionErrorDoesNotFallBack(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return nil, errors.New("dial tcp 1.2.3.4:443: i/o timeout")
	})
	calls := stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		t.Fatal("网络错误不得触发浏览器降级")
		return nil, nil
	})

	_, errRenew := renewWithBrowserFallback(sa)
	if errRenew == nil {
		t.Fatal("应上抛原始错误")
	}
	if *calls != 0 {
		t.Fatalf("浏览器保活调用次数应为 0，实际 %d", *calls)
	}
}

// TestRenewWithBrowserFallback_RevivesExpiredSession 验证会话被判失效后由浏览器救回。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，降级链主行为。
func TestRenewWithBrowserFallback_RevivesExpiredSession(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	sa.AuthInvalid = true
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return nil, errors.New("续期失败（第一步）：会话过期，请重新登录")
	})
	calls := stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		return &renewResult{Renewed: true, CookieHeader: "sessionid=revived", Note: "浏览器保活完成"}, nil
	})

	res, errRenew := renewWithBrowserFallback(sa)
	if errRenew != nil {
		t.Fatalf("浏览器救回后不应报错，实际 %v", errRenew)
	}
	if res == nil || !res.Renewed {
		t.Fatal("应返回成功结果")
	}
	if *calls != 1 {
		t.Fatalf("浏览器保活应调用 1 次，实际 %d", *calls)
	}
	if sa.CookieHeader != "sessionid=revived" {
		t.Fatalf("应落盘浏览器换回的凭据，实际 %q", sa.CookieHeader)
	}
	if sa.AuthInvalid {
		t.Fatal("成功换发后应清掉失效标记")
	}
	loaded, errLoad := readAuthFile(sa.PhysicalPath)
	if errLoad != nil {
		t.Fatalf("回读失败：%v", errLoad)
	}
	if loaded.CookieHeader != "sessionid=revived" {
		t.Fatalf("文件应写入新凭据，实际 %q", loaded.CookieHeader)
	}
}

// TestRenewWithBrowserFallback_MergesBothFailures 验证两级失败时保留会话过期的判定。
//
// 合并顺序是刻意的：原错误在前，浏览器原因在后。isSessionExpiredError
// 依赖「会话过期」这类字样做退避与告警判定，若被「浏览器保活未启用」
// 顶掉，失效账号就会退化成普通失败，告警也不推了。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，锁定错误合并顺序。
func TestRenewWithBrowserFallback_MergesBothFailures(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return nil, errors.New("续期失败（第一步）：会话过期，请重新登录")
	})
	stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		return nil, errBrowserKeepDisabled
	})

	_, errRenew := renewWithBrowserFallback(sa)
	if errRenew == nil {
		t.Fatal("两级都失败时应上抛错误")
	}
	if !isSessionExpiredError(errRenew.Error()) {
		t.Fatalf("合并错误必须仍可判为会话过期，实际 %q", errRenew.Error())
	}
	if !strings.Contains(errRenew.Error(), "浏览器保活未启用") {
		t.Fatalf("应附上浏览器侧原因，实际 %q", errRenew.Error())
	}
}

// TestRenewWithBrowserFallback_NotRenewedTriggersBrowser 验证「看似成功但没换发」也进降级。
//
// 死会话打 beat 仍会下发 9 条埋点 Cookie（ttwid / s_v_web_id / biz_trace_id
// 等），一条会话主项都没有。这种「有下发、无换发」与失效是同一处置：
// 凭证没变，下一轮还是这个结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，埋点 Cookie 不算换发。
func TestRenewWithBrowserFallback_NotRenewedTriggersBrowser(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return &renewResult{Renewed: false, Note: "上游既未下发会话 Cookie 也未返回 x-tt-token（本轮下发 9 条 Cookie，无会话主项）"}, nil
	})
	calls := stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		return &renewResult{Renewed: true, CookieHeader: "sessionid=fresh", Note: "浏览器保活完成"}, nil
	})

	res, errRenew := renewWithBrowserFallback(sa)
	if errRenew != nil {
		t.Fatalf("浏览器换发成功后不应报错，实际 %v", errRenew)
	}
	if res == nil || !res.Renewed {
		t.Fatal("应返回浏览器换发的结果")
	}
	if *calls != 1 {
		t.Fatalf("未换发时应降级 1 次，实际 %d", *calls)
	}
}

// TestRenewWithBrowserFallback_FallsBackToPlainResult 验证降级失败时仍返回裸 HTTP 的结论。
//
// 裸 HTTP 没报错只是没换到东西，此时浏览器侧失败不该把整轮判成异常：
// 调用方需要拿到原始 Note（下发了几条、缺什么），面板才能显示真实原因。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，降级不得掩盖原结论。
func TestRenewWithBrowserFallback_FallsBackToPlainResult(t *testing.T) {
	sa, err := newFallbackAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	stubRenewSession(t, func(*StoredAuth) (*renewResult, error) {
		return &renewResult{Renewed: false, Note: "第二步未换到会话主 Cookie（本轮下发 3 条 Cookie，无会话主项）"}, nil
	})
	stubBrowserRevive(t, func(*StoredAuth) (*renewResult, error) {
		return nil, errors.New("未找到 Chrome/Chromium 可执行文件")
	})

	res, errRenew := renewWithBrowserFallback(sa)
	if errRenew != nil {
		t.Fatalf("降级失败不应把「未换发」升级成错误，实际 %v", errRenew)
	}
	if res == nil || res.Renewed {
		t.Fatal("应保留裸 HTTP 的未换发结论")
	}
	if !strings.Contains(res.Note, "无会话主项") {
		t.Fatalf("应保留原始说明，实际 %q", res.Note)
	}
}
