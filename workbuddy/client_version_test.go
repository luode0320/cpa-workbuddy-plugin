package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsNewerVersion 覆盖「只升不降」的全部判定分支：非法输入、相等、降级、
// 跨位数进位都必须给出一致答案，避免上游回退或脏数据削弱现有 UA。
func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		cur, next string
		want      bool
	}{
		{"2.63.2", "2.174.0", true},   // 次版本号按数值比较，不是字符串比较
		{"2.174.0", "2.63.2", false},  // 防降级
		{"2.174.0", "2.174.0", false}, // 幂等
		{"2.174.0", "2.174.1", true},
		{"2.174.1", "2.175.0", true},
		{"2.174.1", "3.0.0", true},
		{"3.0.0", "2.999.999", false},
		{"2.174.0", "v2.175.0", false},     // 拒绝 v 前缀
		{"2.174.0", "2.175.0-beta", false}, // 拒绝预发布
		{"2.174.0", "", false},             // 空串
		{"", "2.175.0", false},             // 当前值非法时不升级
		{"2.174.0", "2.175", false},        // 段数不足
		{"2.174.0", "2.175.0.1", false},    // 段数过多
	}
	for _, tc := range cases {
		if got := isNewerVersion(tc.cur, tc.next); got != tc.want {
			t.Fatalf("isNewerVersion(%q, %q) = %v, want %v", tc.cur, tc.next, got, tc.want)
		}
	}
}

// TestClientVersionFromConfig 校验配置值解析：只有合法三段版本号才算钉死，
// 其余输入一律回落为「自动更新」。
func TestClientVersionFromConfig(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2.174.0", "2.174.0"},
		{"  2.175.1  ", "2.175.1"},
		{"2.174.0-beta", ""},
		{"v2.174.0", ""},
		{"latest", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := clientVersionFromConfig(tc.in); got != tc.want {
			t.Fatalf("clientVersionFromConfig(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCurrentUserAgentShape 校验 UA 形态与头部同源：上游真实格式是
// `X-IDE-Name/版本`，品牌名必须等于 X-IDE-Name 的实际取值。
func TestCurrentUserAgentShape(t *testing.T) {
	want := ideNameHeaderValue + "/" + currentClientVersion()
	if got := currentUserAgent(); got != want {
		t.Fatalf("currentUserAgent() = %q, want %q", got, want)
	}
	// 旧形态 `CLI/x.x.x CodeBuddy/x.x.x` 必须彻底消失。
	if got := currentUserAgent(); got == "CLI/"+currentClientVersion()+" CodeBuddy/"+currentClientVersion() {
		t.Fatalf("currentUserAgent() 仍在返回已废弃的 CLI 前缀形态：%q", got)
	}
}

// TestRefreshClientVersionAdoptsOnlyNewer 验证自动更新的核心不变量：
// 更新源返回更旧、相同或非法版本时，当前值必须保持不变。
func TestRefreshClientVersionAdoptsOnlyNewer(t *testing.T) {
	original := clientVersionFetchFn
	defer func() { clientVersionFetchFn = original }()

	cases := []struct {
		name    string
		current string
		fetched string
		want    string
	}{
		{"升级", "2.174.0", "2.180.0", "2.180.0"},
		{"降级被拒", "2.180.0", "2.174.0", "2.180.0"},
		{"相同被拒", "2.174.0", "2.174.0", "2.174.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientVersion.Store(tc.current)
			defer clientVersion.Store(builtinClientVersion)
			fetched := tc.fetched
			clientVersionFetchFn = func() (string, error) { return fetched, nil }

			refreshClientVersionOnce()

			if got := currentClientVersion(); got != tc.want {
				t.Fatalf("刷新后版本 = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRefreshClientVersionKeepsValueOnError 验证拉取失败时保留旧值，
// 不能让一次网络抖动把 UA 打回或清空。
func TestRefreshClientVersionKeepsValueOnError(t *testing.T) {
	original := clientVersionFetchFn
	defer func() { clientVersionFetchFn = original; clientVersion.Store(builtinClientVersion) }()

	clientVersion.Store("2.170.0")
	clientVersionFetchFn = func() (string, error) { return "", errors.New("网络不可达") }

	refreshClientVersionOnce()

	if got := currentClientVersion(); got != "2.170.0" {
		t.Fatalf("拉取失败后版本 = %q, want 2.170.0", got)
	}
}

// TestPinnedVersionSkipsAutoUpdate 验证人工钉死后自动更新整体让位。
func TestPinnedVersionSkipsAutoUpdate(t *testing.T) {
	originalFetch := clientVersionFetchFn
	originalPinned := clientVersionPinned
	defer func() {
		clientVersionFetchFn = originalFetch
		clientVersionPinned = originalPinned
		clientVersion.Store(builtinClientVersion)
	}()

	setPinnedClientVersion("2.100.0")
	clientVersionFetchFn = func() (string, error) { return "2.180.0", nil }

	refreshClientVersionOnce()

	if got := currentClientVersion(); got != "2.100.0" {
		t.Fatalf("钉死版本被自动更新覆盖：got %q, want 2.100.0", got)
	}

	setPinnedClientVersion("")
}

// TestFetchVersionPlainRejectsDirtyBody 验证官方源返回非版本号内容
// （HTML 错误页、空响应、带后缀的开发版本）时一律拒绝。
func TestFetchVersionPlainRejectsDirtyBody(t *testing.T) {
	bodies := []string{
		"<html>404 Not Found</html>",
		"",
		"2.174.0-beta.1",
		"v2.174.0",
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		if _, err := fetchVersionPlain(srv.Client(), srv.URL); err == nil {
			t.Fatalf("脏响应 %q 未被拒绝", body)
		}
		srv.Close()
	}
}

// TestFetchVersionPlainAcceptsTrailingNewline 验证官方源的真实响应
// （版本号后带换行）能被正确解析——这是生产环境的实际形态。
func TestFetchVersionPlainAcceptsTrailingNewline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("2.174.0\n"))
	}))
	defer srv.Close()

	got, err := fetchVersionPlain(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("解析官方源响应失败：%v", err)
	}
	if got != "2.174.0" {
		t.Fatalf("got %q, want 2.174.0", got)
	}
}

// TestFetchClientVersionFallsBackToNPM 验证官方源不可用时切换到 npm 备用源。
func TestFetchClientVersionFallsBackToNPM(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	npm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.180.0"}`))
	}))
	defer npm.Close()

	// 直接验证备用源解析路径：主源失败后由 fetchClientVersion 内部切换，
	// 这里分别断言两段逻辑，避免测试依赖真实外网。
	if _, err := fetchVersionPlain(dead.Client(), dead.URL); err == nil {
		t.Fatal("主源 500 未被判为失败")
	}
	got, err := fetchVersionFromNPM(npm.Client(), npm.URL)
	if err != nil {
		t.Fatalf("npm 备用源解析失败：%v", err)
	}
	if got != "2.180.0" {
		t.Fatalf("got %q, want 2.180.0", got)
	}
}

// TestCommonHeadersCarryVersion 验证 commonHeaders 的最终头形态：
// UA 用动态产品版本号，X-IDE-Version 固定 0.0.0（两套语义不可混用）。
func TestCommonHeadersCarryVersion(t *testing.T) {
	clientVersion.Store("2.174.0")
	defer clientVersion.Store(builtinClientVersion)

	req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	commonHeaders(req)

	if got, want := req.Header.Get("User-Agent"), "WorkBuddy/2.174.0"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
	// 独立客户端无宿主 IDE，X-IDE-Version 必须是固定的 0.0.0，
	// 不能跟随 UA 的产品版本号（真实客户端即如此）。
	if got, want := req.Header.Get("X-IDE-Version"), "0.0.0"; got != want {
		t.Fatalf("X-IDE-Version = %q, want %q", got, want)
	}
}

// TestIDEVersionHeaderIndependentOfProductVersion 是不变量测试：无论 UA 版本号
// 怎么变，X-IDE-Version 都必须稳定为 0.0.0，防止将来有人把它「顺手统一」成
// 产品版本号而破坏与真实客户端的一致性。
func TestIDEVersionHeaderIndependentOfProductVersion(t *testing.T) {
	defer clientVersion.Store(builtinClientVersion)

	for _, v := range []string{"2.100.0", "2.174.0", "3.0.0"} {
		clientVersion.Store(v)
		if got := currentIDEVersionHeader(); got != "0.0.0" {
			t.Fatalf("产品版本 %s 时 X-IDE-Version = %q, want 0.0.0", v, got)
		}
		if ua := currentUserAgent(); ua != ideNameHeaderValue+"/"+v {
			t.Fatalf("产品版本 %s 时 UA = %q, want %s/%s", v, ua, ideNameHeaderValue, v)
		}
	}
}
