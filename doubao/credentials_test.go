package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// validCookieHeader 构造一份字段齐备的 Cookie header（值均为测试假值）。
const validCookieHeader = "flow_cur_user_sec_id=test_sec_user_handle_value; sessionid=test_session_value; ttwid=test_ttwid_value"

// TestParseCredentialsValid 校验合法 Cookie 的解析结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定解析层对合法输入的字段提取。
func TestParseCredentialsValid(t *testing.T) {
	c, err := parseCredentials(validCookieHeader)
	if err != nil {
		t.Fatalf("合法 Cookie 解析不应报错：%v", err)
	}
	if c.FlowSecUserID != "test_sec_user_handle_value" {
		t.Errorf("flow_cur_user_sec_id 提取错误，实际 %q", c.FlowSecUserID)
	}
}

// TestParseCredentialsStripsCookiePrefix 校验用户连 "Cookie: " 前缀一起复制时的容错。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，覆盖真实复制粘贴形态。
func TestParseCredentialsStripsCookiePrefix(t *testing.T) {
	c, err := parseCredentials("Cookie: " + validCookieHeader)
	if err != nil {
		t.Fatalf("带 Cookie 前缀的输入应被容错处理：%v", err)
	}
	if c.FlowSecUserID == "" {
		t.Error("剥离前缀后应能提取 flow_cur_user_sec_id")
	}
}

// TestValidateRejectsMissingFields 校验必需字段缺失时被拒绝。
//
// 覆盖三种拒绝场景：缺 flow_cur_user_sec_id、缺 sessionid 族、空 Cookie。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，凭据闸门的负向用例。
func TestValidateRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"缺 flow_cur_user_sec_id", "sessionid=test_session_value; ttwid=x"},
		{"缺 sessionid 族", "flow_cur_user_sec_id=test_sec_user_handle_value; ttwid=x"},
		{"空 Cookie", ""},
		{"只有无关字段", "ttwid=abc; i18next=zh"},
	}
	for _, c := range cases {
		if _, err := parseCredentials(c.header); err == nil {
			t.Errorf("%s：应被拒绝但通过了校验", c.name)
		}
	}
}

// TestValidateAcceptsSessionIdVariants 校验 sessionid 三种形态任一均可通过字段校验。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，覆盖 sessionid_ss / sid_tt 形态。
func TestValidateAcceptsSessionIdVariants(t *testing.T) {
	for _, name := range []string{"sessionid", "sessionid_ss", "sid_tt"} {
		header := "flow_cur_user_sec_id=test_sec_user_handle_value; " + name + "=test_value"
		if _, err := parseCredentials(header); err != nil {
			t.Errorf("%s 形态应通过校验，实际报错：%v", name, err)
		}
	}
}

// TestCookieValueParsing 校验 Cookie 值提取的边界情形。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，覆盖空格、无值、前缀重名等边界。
func TestCookieValueParsing(t *testing.T) {
	cases := []struct {
		header string
		name   string
		want   string
	}{
		{"a=1; b=2", "b", "2"},
		{"a=1;b=2", "b", "2"},
		{"  a = 1 ;  b = 2  ", "b", "2"},
		{"sessionid_ss=abc", "sessionid", ""},
		{"a=1", "missing", ""},
		{"noequals", "noequals", ""},
		{"", "any", ""},
	}
	for _, c := range cases {
		if got := cookieValue(c.header, c.name); got != c.want {
			t.Errorf("cookieValue(%q, %q) = %q，期望 %q", c.header, c.name, got, c.want)
		}
	}
}

// TestProbeAccountInfoRejectsGarbage 校验垃圾凭据被判据端点拒绝。
//
// 这是 TASK-002 最关键的反假成功用例：实测伪造 Cookie 仍能让业务端点
// /alice/basic/launch 返回 code:0，故判据必须依赖 /passport/account/info/v2/
// 的 message 字段，不得依赖业务端点状态码。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防假成功闸门。
func TestProbeAccountInfoRejectsGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/passport/account/info/v2/") {
			t.Errorf("判据端点路径错误：%s", r.URL.Path)
		}
		// 模拟无会话响应：message=error，data 内无 sec_user_id。
		_, _ = w.Write([]byte(`{"message":"error","data":{"description":"not login","user_id":0}}`))
	}))
	defer srv.Close()

	c := &credentials{CookieHeader: validCookieHeader, FlowSecUserID: "test_sec_user_handle_value"}
	if _, err := probeAccountInfoAgainst(c, srv.URL+"/passport/account/info/v2/", time.Second); err == nil {
		t.Fatal("垃圾凭据应被拒绝，但判据通过了")
	}
}

// TestProbeAccountInfoAcceptsValid 校验有效会话通过判据并回填身份字段。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，判据正向用例。
func TestProbeAccountInfoAcceptsValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_handle_abcdefghijklmnopqrstuvwxyz0123456789","user_id":12345678,"screen_name":"测试昵称","mobile":"138****0000"}}`))
	}))
	defer srv.Close()

	c := &credentials{CookieHeader: validCookieHeader, FlowSecUserID: "test_sec_user_handle_value"}
	out, err := probeAccountInfoAgainst(c, srv.URL+"/passport/account/info/v2/", time.Second)
	if err != nil {
		t.Fatalf("有效会话应通过判据：%v", err)
	}
	if out.SecUserID == "" {
		t.Error("应回填 sec_user_id")
	}
	if out.UserID != 12345678 {
		t.Errorf("应回填 user_id，实际 %d", out.UserID)
	}
	if out.ScreenName != "测试昵称" {
		t.Errorf("应回填 screen_name，实际 %q", out.ScreenName)
	}
}

// TestProbeAccountInfoRejectsSuccessWithoutSecUserId 校验 message=success 但缺 sec_user_id 时仍拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，判据的第二重条件覆盖。
func TestProbeAccountInfoRejectsSuccessWithoutSecUserId(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"user_id":12345678}}`))
	}))
	defer srv.Close()

	c := &credentials{CookieHeader: validCookieHeader, FlowSecUserID: "test_sec_user_handle_value"}
	if _, err := probeAccountInfoAgainst(c, srv.URL+"/passport/account/info/v2/", time.Second); err == nil {
		t.Fatal("message=success 但缺 sec_user_id 时应被拒绝")
	}
}

// TestAuthIDForStable 校验账号标识派生稳定且以 sec_user_id 优先。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定账号标识口径。
func TestAuthIDForStable(t *testing.T) {
	a := &credentials{SecUserID: "sec-A", FlowSecUserID: "flow-A"}
	b := &credentials{SecUserID: "sec-A", FlowSecUserID: "flow-B"}

	if authIDFor(a) != authIDFor(b) {
		t.Error("相同 sec_user_id 应派生相同账号标识（sec_user_id 优先于 flow_cur_user_sec_id）")
	}
	if !strings.HasPrefix(authIDFor(a), doubaoAuthFilePrefix) {
		t.Errorf("账号标识应以 %s 为前缀，实际 %q", doubaoAuthFilePrefix, authIDFor(a))
	}
	c := &credentials{FlowSecUserID: "flow-A"}
	if authIDFor(c) == "" {
		t.Error("仅有 flow_cur_user_sec_id 时也应能派生账号标识")
	}
}

// TestSaveCredentialsWritesFile 校验导入成功后凭据落盘且字段完整。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，导入主链路落盘验证。
func TestSaveCredentialsWritesFile(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"sec_user_id":"sec_handle_for_test_0123456789abcdef","user_id":87654321,"screen_name":"落盘测试"}}`))
	}))
	defer srv.Close()
	restore := swapAccountInfoEndpoint(srv.URL + "/passport/account/info/v2/")
	defer restore()

	sa, err := saveCredentials(validCookieHeader, "", time.Second)
	if err != nil {
		t.Fatalf("导入应成功：%v", err)
	}
	if sa.AuthID == "" || sa.SecUserID == "" {
		t.Fatalf("落盘账号字段不完整：%+v", sa)
	}
	if sa.Label != "落盘测试" {
		t.Errorf("未指定备注名时应回退昵称，实际 %q", sa.Label)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("应落盘 1 个凭据文件，实际 %d", len(entries))
	}
	raw, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var back StoredAuth
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("落盘文件应为合法 JSON：%v", err)
	}
	if back.CookieHeader != validCookieHeader {
		t.Error("落盘文件应保留完整 Cookie header")
	}
	if back.BotID == "" {
		t.Error("落盘文件应带默认 bot_id")
	}
}

// TestSaveCredentialsRejectsGarbageAndWritesNothing 校验垃圾凭据被拒且不产生文件。
//
// 这是反假成功的端到端用例：判据失败时不得落盘，避免把垃圾凭据写成可用账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，导入链路防假成功。
func TestSaveCredentialsRejectsGarbageAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"error","data":{"description":"not login"}}`))
	}))
	defer srv.Close()
	restore := swapAccountInfoEndpoint(srv.URL + "/passport/account/info/v2/")
	defer restore()

	if _, err := saveCredentials(validCookieHeader, "", time.Second); err == nil {
		t.Fatal("垃圾凭据应被拒绝")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("校验失败时不得落盘，实际产生 %d 个文件", len(entries))
	}
}

// TestSaveCredentialsRejectsMissingFieldsBeforeNetwork 校验缺字段在发请求前就被拒绝。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，确认字段闸门先于网络调用。
func TestSaveCredentialsRejectsMissingFieldsBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	_, err := saveCredentials("ttwid=only_unrelated_field", "", time.Second)
	if err == nil {
		t.Fatal("缺必需字段应被拒绝")
	}
	if !strings.Contains(err.Error(), "flow_cur_user_sec_id") {
		t.Errorf("错误提示应指明缺失字段，实际：%v", err)
	}
}

// TestHandleImportRouteRejectsBadBody 校验导入路由对非法请求体的处理。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，路由层负向用例。
func TestHandleImportRouteRejectsBadBody(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/doubao-provider/import",
		Body:   []byte(`{"label":"no cookie"}`),
	}
	raw, _ := json.Marshal(req)

	out, err := handleManagement(raw)
	if err != nil {
		t.Fatalf("handleManagement 返回错误：%v", err)
	}
	if body := decodeMgmtBody(t, out); !strings.Contains(body, "cookie") {
		t.Errorf("缺 cookie 时应给出明确提示，实际响应体：%s", truncateForTest(body))
	}
}

// TestHandleAccountLifecycleRoutes 校验账号启停与删除路由。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，账号管理路由闭环。
func TestHandleAccountLifecycleRoutes(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	// 先落盘一个账号，再验证禁用 → 启用 → 删除。
	sa := &StoredAuth{
		AuthID:       doubaoAuthFilePrefix + "testlife",
		CookieHeader: validCookieHeader,
		Label:        "生命周期测试",
	}
	if err := writeAuthFileDirect(filepath.Join(dir, sa.AuthID+".json"), sa); err != nil {
		t.Fatalf("预置账号失败：%v", err)
	}

	call := func(subPath string, body string) string {
		req := pluginapi.ManagementRequest{
			Method: http.MethodPost,
			Path:   "/v0/management/plugins/doubao-provider" + subPath,
			Body:   []byte(body),
		}
		raw, _ := json.Marshal(req)
		out, err := handleManagement(raw)
		if err != nil {
			t.Fatalf("%s 调用失败：%v", subPath, err)
		}
		return decodeMgmtBody(t, out)
	}

	call("/disable", `{"auth_id":"`+sa.AuthID+`"}`)
	accounts, _ := listAllAuthFiles()
	if len(accounts) != 1 || !accounts[0].Disabled {
		t.Fatal("禁用后账号应为 disabled")
	}

	call("/enable", `{"auth_id":"`+sa.AuthID+`"}`)
	accounts, _ = listAllAuthFiles()
	if len(accounts) != 1 || accounts[0].Disabled {
		t.Fatal("启用后账号应为 enabled")
	}

	call("/delete", `{"auth_id":"`+sa.AuthID+`"}`)
	accounts, _ = listAllAuthFiles()
	if len(accounts) != 0 {
		t.Fatalf("删除后应无账号，实际 %d", len(accounts))
	}
}

// TestIsSafeDoubaoAuthPath 校验落盘路径安全闸门。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，路径穿越防护用例。
func TestIsSafeDoubaoAuthPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/home/u/.antigravity_cockpit/doubao_accounts/doubao-abc12345.json", true},
		{"doubao-abc12345.json", true},
		{"/tmp/../etc/doubao-abc12345.json", false},
		{"/tmp/copilot-abc12345.json", false},
		{"/tmp/doubao-abc12345.txt", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSafeDoubaoAuthPath(c.path); got != c.want {
			t.Errorf("isSafeDoubaoAuthPath(%q) = %v，期望 %v", c.path, got, c.want)
		}
	}
}

// TestProbeAccountInfoLiveRejectsGarbage 走真实生产路径对真实判据端点做负向验证。
//
// 与 TestProbeAccountInfoRejectsGarbage 的区别：本用例不替换端点，真实走
// accountInfoHost + buildAccountInfoQuery 拼出的线上 URL，验证「生产参数集能被上游接受、
// 且伪造凭据确实被拒」。默认跳过（CI 无网络/不该打上游），设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，真实链路负向闸门（防假成功的线上回归）。
func TestProbeAccountInfoLiveRejectsGarbage(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	c := &credentials{
		CookieHeader:  "flow_cur_user_sec_id=fake_live_probe; sessionid=fake_live_probe",
		FlowSecUserID: "fake_live_probe",
	}
	if _, err := probeAccountInfo(c, 20*time.Second); err == nil {
		t.Fatal("线上判据端点对伪造凭据返回了成功——判据失去区分度，必须重新选端点")
	} else {
		t.Logf("线上负向验证通过：%v", err)
	}
}

// TestProbeAccountInfoLiveAcceptsRealCookie 走真实生产路径对真实判据端点做正向验证。
//
// 需要用户从浏览器 DevTools 导出完整 Cookie（含 HttpOnly 的 sessionid），
// 写入 doubao/.local_cookie.txt（已 gitignore，仅 local 使用），并设 DOUBAO_LIVE_TEST=1。
// 该文件缺失时跳过——正向验证是 AC-001 的最终证据，缺它时按需求降级条款记录偏差。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，真实链路正向闸门。
func TestProbeAccountInfoLiveAcceptsRealCookie(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	raw, errRead := os.ReadFile(filepath.Join("doubao", ".local_cookie.txt"))
	if errRead != nil {
		raw, errRead = os.ReadFile(".local_cookie.txt")
	}
	if errRead != nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过正向验证")
	}
	c, errParse := parseCredentials(string(raw))
	if errParse != nil {
		t.Fatalf("真实 Cookie 解析失败：%v", errParse)
	}
	out, errProbe := probeAccountInfo(c, 20*time.Second)
	if errProbe != nil {
		t.Fatalf("真实 Cookie 应通过判据：%v", errProbe)
	}
	if out.SecUserID == "" {
		t.Fatal("判据通过但未回填 sec_user_id")
	}
	t.Logf("线上正向验证通过：sec_user_id 长度=%d，昵称长度=%d", len(out.SecUserID), len(out.ScreenName))
}

// TestSaveCredentialsLiveEndToEnd 用真实 Cookie 走完整导入链路并验证落盘。
//
// 与 TestProbeAccountInfoLiveAcceptsRealCookie 的区别：本用例覆盖 AC-001 的落盘环节，
// 走 saveCredentials（解析 → 校验 → 真实判据 → 落盘 → 回读），只把账号目录指向临时目录，
// 不污染真实账号目录。默认跳过，设 DOUBAO_LIVE_TEST=1 且提供 .local_cookie.txt 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，AC-001 端到端落盘验证。
func TestSaveCredentialsLiveEndToEnd(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	raw, errRead := os.ReadFile(filepath.Join("doubao", ".local_cookie.txt"))
	if errRead != nil {
		raw, errRead = os.ReadFile(".local_cookie.txt")
	}
	if errRead != nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过端到端验证")
	}

	dir := t.TempDir()
	setAccountsDir(dir)
	defer setAccountsDir("")

	sa, errSave := saveCredentials(string(raw), "", 20*time.Second)
	if errSave != nil {
		t.Fatalf("真实 Cookie 导入应成功：%v", errSave)
	}

	// 1. 落盘文件必须存在且命名符合 doubao-<hash8>.json。
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("应落盘 1 个凭据文件，实际 %d", len(entries))
	}
	if entries[0].Name() != sa.AuthID+".json" {
		t.Errorf("文件名应与 AuthID 一致，实际 %q", entries[0].Name())
	}

	// 2. 回读校验字段完整性。
	back, errBack := readAuthFile(filepath.Join(dir, entries[0].Name()))
	if errBack != nil {
		t.Fatalf("回读落盘文件失败：%v", errBack)
	}
	if back.CookieHeader != strings.TrimSpace(string(raw)) {
		t.Error("落盘文件应保留完整 Cookie header")
	}
	if back.SecUserID != sa.SecUserID || back.SecUserID == "" {
		t.Error("落盘文件应保留判据回填的 sec_user_id")
	}
	if back.FlowSecUserID == "" {
		t.Error("落盘文件应保留 flow_cur_user_sec_id")
	}
	if back.BotID != defaultBotID {
		t.Errorf("落盘文件应带默认 bot_id，实际 %q", back.BotID)
	}
	if back.Disabled {
		t.Error("新导入账号应为启用状态")
	}

	// 3. 面板状态判据必须为 ok（会话 Cookie 未过期）。
	if state := authStateOf(back); state != "ok" {
		t.Errorf("刚导入的账号 auth_state 应为 ok，实际 %q", state)
	}

	t.Logf("端到端落盘验证通过：文件=%s，sec_user_id 长度=%d，昵称长度=%d，auth_state=%s",
		entries[0].Name(), len(back.SecUserID), len(back.ScreenName), authStateOf(back))
}
