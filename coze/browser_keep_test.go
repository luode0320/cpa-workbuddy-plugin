package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖浏览器保活的纯逻辑部分（TASK-07）。
//
// 起不起得来真浏览器属集成验证；这里钉住的是「不依赖浏览器也能判错」的
// 那些分支：未启用、账号为空、凭据为空、并发重入、profile 落点与命名。
// 这些分支如果错了，生产上会表现为「保活静默不干活」或「路径穿越」。

// TestReviveSession_DisabledByDefault 验证未启用时直接拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，默认关闭语义。
func TestReviveSession_DisabledByDefault(t *testing.T) {
	stubBrowserConfig(t, false)
	keeper := newBrowserKeeper()
	_, errRevive := keeper.reviveSession(&StoredAuth{AuthID: "a", CookieHeader: "sessionid=x"})
	if errRevive == nil {
		t.Fatal("未启用时必须报错，不能静默返回 nil 结果")
	}
	if !strings.Contains(errRevive.Error(), "未启用") {
		t.Fatalf("错误文案应说明未启用，实际 %q", errRevive.Error())
	}
}

// TestReviveSession_RejectsEmptyCredential 验证空凭据提前拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，避免白起浏览器。
func TestReviveSession_RejectsEmptyCredential(t *testing.T) {
	stubBrowserConfig(t, true)
	keeper := newBrowserKeeper()
	_, errRevive := keeper.reviveSession(&StoredAuth{AuthID: "a"})
	if errRevive == nil || !strings.Contains(errRevive.Error(), "凭据为空") {
		t.Fatalf("空凭据必须提前拒绝，实际 %v", errRevive)
	}
}

// TestReviveSession_RejectsNilAccount 验证空账号不 panic。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，边界。
func TestReviveSession_RejectsNilAccount(t *testing.T) {
	stubBrowserConfig(t, true)
	keeper := newBrowserKeeper()
	if _, errRevive := keeper.reviveSession(nil); errRevive == nil {
		t.Fatal("空账号必须报错")
	}
}

// TestBrowserKeeper_ConcurrentReentryBlocked 验证同账号并发保活被挡。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，profile 锁保护。
func TestBrowserKeeper_ConcurrentReentryBlocked(t *testing.T) {
	keeper := newBrowserKeeper()
	if !keeper.tryAcquire("k1") {
		t.Fatal("首次占用应成功")
	}
	if keeper.tryAcquire("k1") {
		t.Fatal("同账号并发占用必须被拒")
	}
	if !keeper.tryAcquire("k2") {
		t.Fatal("不同账号互不影响")
	}
	if !keeper.busy("k1") {
		t.Fatal("占用中应可查")
	}
	keeper.release("k1")
	if keeper.busy("k1") {
		t.Fatal("释放后不应再判占用")
	}
	if !keeper.tryAcquire("k1") {
		t.Fatal("释放后应可重新占用")
	}
}

// TestSafeProfileName_Sanitizes 验证目录名过滤。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，防路径穿越。
func TestSafeProfileName_Sanitizes(t *testing.T) {
	cases := map[string]string{
		"coze-123": "coze-123",
		// "../" 两段共 6 个非法字符（. . / . . /）压成 6 个下划线，
		// 名字里的 / 也压成下划线 —— 整串不含路径分隔符，无法穿越。
		"../../etc/passwd": "______etc_passwd",
		"a b/c":            "a_b_c",
		"":                 "unknown",
	}
	for input, want := range cases {
		if got := safeProfileName(input); got != want {
			t.Fatalf("safeProfileName(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestSafeProfileName_Truncates 验证超长名截断。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，避免路径过长。
func TestSafeProfileName_Truncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	if got := safeProfileName(long); len(got) != 64 {
		t.Fatalf("超长名应截到 64，实际 %d", len(got))
	}
}

// TestBrowserProfileDir_UsesConfiguredRoot 验证 profile 根目录可配。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，落点语义。
func TestBrowserProfileDir_UsesConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	got, errDir := browserProfileDir(root, "coze-1")
	if errDir != nil {
		t.Fatalf("计算 profile 目录失败：%v", errDir)
	}
	want := filepath.Join(root, "coze-1")
	if got != want {
		t.Fatalf("profile 目录 = %q，期望 %q", got, want)
	}
	// 目录必须按账号隔离，不能共用。
	other, _ := browserProfileDir(root, "coze-2")
	if other == got {
		t.Fatal("不同账号必须落在不同目录")
	}
}

// TestBrowserProfileDir_DefaultsUnderAccountRoot 验证缺省落点。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，缺省路径。
func TestBrowserProfileDir_DefaultsUnderAccountRoot(t *testing.T) {
	got, errDir := browserProfileDir("", "coze-9")
	if errDir != nil {
		t.Fatalf("计算缺省 profile 目录失败：%v", errDir)
	}
	if !strings.Contains(got, browserKeepProfileRoot) {
		t.Fatalf("缺省路径应包含 %q，实际 %q", browserKeepProfileRoot, got)
	}
	if filepath.Base(got) != "coze-9" {
		t.Fatalf("末级目录应是账号名，实际 %q", filepath.Base(got))
	}
}

// TestCookiesFromHeader_ParsesPairs 验证 Cookie 头解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，注入前置。
func TestCookiesFromHeader_ParsesPairs(t *testing.T) {
	got := cookiesFromHeader("sessionid=abc; sid_guard=def; malformed")
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d 条", len(got))
	}
	values := map[string]string{}
	for _, c := range got {
		values[c.Name] = c.Value
		if c.Path != "/" {
			t.Fatalf("注入 Cookie 必须显式给路径，实际 %q", c.Path)
		}
	}
	if values["sessionid"] != "abc" || values["sid_guard"] != "def" {
		t.Fatalf("解析结果错误：%v", values)
	}
}

// TestCookiesFromHeader_Empty 验证空头返回空列表。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，边界。
func TestCookiesFromHeader_Empty(t *testing.T) {
	if got := cookiesFromHeader("   "); len(got) != 0 {
		t.Fatalf("空头应返回空列表，实际 %d 条", len(got))
	}
}

// stubBrowserConfig 临时改写运行期配置。
//
// 配置是全局原子值，用例必须还原，否则会污染同包其他用例
// （尤其 TestRenewIntervalDefaultIsFourHours 这类读默认值的）。
// [参数] t: 测试上下文；enabled: 是否启用浏览器保活。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，用例隔离。
func stubBrowserConfig(t *testing.T, enabled bool) {
	t.Helper()
	prev, _ := activeConfig.Load().(Config)
	next := currentConfig()
	next.BrowserKeepEnabled = enabled
	// 关掉自动探测：用例不该真去磁盘上找 Chrome，也不该因为
	// 开发机装了 Chrome 就走出不同分支。
	next.BrowserKeepChromePath = filepath.Join(os.TempDir(), "definitely-not-chrome")
	activeConfig.Store(next)
	t.Cleanup(func() {
		if prev.TimeoutSeconds == 0 {
			activeConfig.Store(defaultConfig())
			return
		}
		activeConfig.Store(prev)
	})
}
