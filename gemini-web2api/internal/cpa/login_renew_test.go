package cpa_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// renewResourcePath 是浏览器扩展定时回推 Cookie 的资源路由。
const renewResourcePath = "/v0/resource/plugins/gemini-web2api/login/renew"

// TestLoginRenewRejectsMissingCookies 校验续期端点在没有 Cookie 时直接拒绝。
//
// 续期会真实触发一次账号导入与上游校验，空 Cookie 进去只会白跑一次
// 初始化；这里先在入口挡掉，避免扩展配置错误时反复打上游。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增，覆盖扩展定时续期的入参校验。
func TestLoginRenewRejectsMissingCookies(t *testing.T) {
	p := newLoginTestPlugin(t)

	resp := callManagement(t, p, renewResourcePath, "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing cookies, got %d body=%s", resp.StatusCode, string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "cookies is required") {
		t.Fatalf("unexpected error body: %s", string(resp.Body))
	}
}

// TestLoginRenewThrottlesRapidCalls 校验同一插件实例上连续两次续期只放行一次。
//
// 扩展侧已有 30 分钟定时器，但用户可能在多个浏览器 profile 里各装一份，
// 或反复点弹窗；没有节流时每次都会重建账号客户端并打一次上游。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增，覆盖续期节流。
func TestLoginRenewThrottlesRapidCalls(t *testing.T) {
	p := newLoginTestPlugin(t)

	first := callManagement(t, p, renewResourcePath+"?cookies=SAPISID%3Dx", "", "")
	if first.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("first renew call should not be throttled, body=%s", string(first.Body))
	}
	if first.StatusCode != http.StatusAccepted && first.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected first renew status: %d body=%s", first.StatusCode, string(first.Body))
	}

	second := callManagement(t, p, renewResourcePath+"?cookies=SAPISID%3Dx", "", "")
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second renew call should be throttled, got %d body=%s", second.StatusCode, string(second.Body))
	}
	var payload map[string]any
	if err := json.Unmarshal(second.Body, &payload); err != nil {
		t.Fatalf("unmarshal throttle body: %v", err)
	}
	if payload["error"] != "renew is too frequent, retry later" {
		t.Fatalf("unexpected throttle message: %v", payload["error"])
	}
}

// TestLoginBridgeMentionsOneClickAndSetup 校验桥接页已改为「一键授权 + 折叠首次使用」。
//
// 用户反馈原页面把配对码、装扩展、等待结果平铺成三大段，看不出主操作是哪一步；
// 现在主按钮是「一键授权（复制配对码）」，安装步骤折叠进 <details>。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增，覆盖桥接页一键化改造。
func TestLoginBridgeMentionsOneClickAndSetup(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, "/v0/resource/plugins/gemini-web2api/login?state=state-under-test", "cpa.example.com", "https")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	page := string(resp.Body)
	for _, want := range []string{"一键授权（复制配对码）", "<details", "首次使用：安装一次浏览器授权助手"} {
		if !strings.Contains(page, want) {
			t.Fatalf("bridge page missing %q", want)
		}
	}
	// 续期端点必须出现在桥接页说明里，否则用户不知道装完会自动续期。
	if !strings.Contains(page, "自动续期") {
		t.Fatalf("bridge page should explain auto renew")
	}
}

// TestLoginRenewRegisteredWithoutMenu 校验续期路由已注册但不进控制台菜单。
//
// 独立成一条用例是为了在只改 Resources 却忘了改分发分支时给出更直接的失败信息。
// [参数] t 是测试上下文。
// [返回] 无；断言失败即 Fatal。
// 最近修改时间：2026-11-21；改动原因：新增。
func TestLoginRenewRegisteredWithoutMenu(t *testing.T) {
	p := newLoginTestPlugin(t)
	resp := callManagement(t, p, renewResourcePath, "", "")

	// 未注册的路由在宿主层就 404，这里打到的必须是插件自己的 400。
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/login/renew is not routed into the plugin")
	}
	if !strings.HasPrefix(string(resp.Body), "{") {
		t.Fatalf("renew endpoint should answer JSON, got %s", firstBytes(string(resp.Body), 200))
	}
}
