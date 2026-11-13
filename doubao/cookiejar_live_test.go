package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDurableJarLiveTTWidSetCookie 走真实上游验证 Jar 捕获滚动下发的 Set-Cookie。
//
// 实测 accounts.doubao.com/ttwid/check/ 会下发 Set-Cookie: gd_random=...; Max-Age=604800，
// 这是可复现的下发源（无需登录态）。本用例验证生产 durableJar 能真实捕获并落盘，
// 默认跳过（CI 无网络），设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，真实 Set-Cookie 捕获（通过标准①）。
func TestDurableJarLiveTTWidSetCookie(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}

	jar := newDurableJar()
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	req, errReq := http.NewRequest(http.MethodGet,
		"https://accounts.doubao.com/ttwid/check/?aid=497858&device_platform=web&region=CN&sys_region=CN", nil)
	if errReq != nil {
		t.Fatalf("构造请求失败：%v", errReq)
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", doubaoSiteHost+"/")

	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("请求失败：%v", errDo)
	}
	defer resp.Body.Close()

	snap := jar.snapshot()
	if len(snap) == 0 {
		t.Fatal("真实上游应下发至少一条 Set-Cookie，但 Jar 未捕获到")
	}
	names := make([]string, 0, len(snap))
	for _, rec := range snap {
		names = append(names, rec.Cookie.Name)
	}
	t.Logf("真实上游下发并被捕获的 Cookie 名：%v", names)

	// 落盘后回读，验证持久化生效（通过标准②）。
	dir := t.TempDir()
	path := filepath.Join(dir, "doubao-live.json")
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "live",
		CookieHeader: validCookieHeader,
		Label:        "真实下发测试",
	}
	if err := writeAuthFileDirect(path, sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}
	jar.bind(path)
	if err := jar.persist(sa); err != nil {
		t.Fatalf("persist 失败：%v", err)
	}
	back, errRead := readAuthFile(path)
	if errRead != nil {
		t.Fatalf("回读失败：%v", errRead)
	}
	if len(back.Cookies) == 0 {
		t.Fatal("真实下发的 Cookie 应已落盘")
	}
	t.Logf("落盘后回读 Cookie 条数=%d（持久化生效）", len(back.Cookies))
}
