package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// writeFailoverAccount 在测试账号目录中直写一个账号文件。
// [参数] t: 测试上下文；dir: 账号目录；authID: 账号标识；disabled: 是否禁用。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，多账号测试夹具。
func writeFailoverAccount(t *testing.T, dir, authID string, disabled bool) {
	t.Helper()
	sa := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       authID,
		CookieHeader: validCookieHeader,
		Label:        authID,
		Disabled:     disabled,
	}
	if errWrite := writeAuthFileDirect(filepath.Join(dir, authID+".json"), sa); errWrite != nil {
		t.Fatalf("预置账号 %s 失败：%v", authID, errWrite)
	}
}

// TestFailoverCooldownFor 校验阶梯冷却时长：60s × 次数，封顶 600s。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试点①阶梯计算。
func TestFailoverCooldownFor(t *testing.T) {
	cases := []struct {
		count int
		want  time.Duration
	}{
		{0, 0},
		{-1, 0},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{9, 540 * time.Second},
		{10, 600 * time.Second},
		{100, 600 * time.Second},
	}
	for _, c := range cases {
		if got := failoverCooldownFor(c.count); got != c.want {
			t.Errorf("failoverCooldownFor(%d) = %v，期望 %v", c.count, got, c.want)
		}
	}
}

// TestIsAccountFailureClassification 校验只有账号级故障才计入冷却。
//
// 业务 400 必须排除：它说明请求本身有问题，换任何账号都会同样失败，
// 计入只会把健康账号逐个冷却掉。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，失败分类回归。
func TestIsAccountFailureClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   int
		want   bool
	}{
		{"传输错误", 0, 0, true},
		{"服务端 500", 500, 0, true},
		{"服务端 502", 502, 0, true},
		{"限流 429", 429, 0, true},
		{"额度耗尽 402", 402, 0, true},
		{"未授权 401", 401, 0, true},
		{"禁止 403", 403, 0, true},
		{"流内登录失效", 200, 710012001, true},
		{"流内限流", 200, 710022004, true},
		{"业务 400 不计入", 400, 0, false},
		{"业务 404 不计入", 404, 0, false},
	}
	for _, c := range cases {
		if got := isAccountFailure(c.status, c.code); got != c.want {
			t.Errorf("%s：isAccountFailure(%d, %d) = %v，期望 %v", c.name, c.status, c.code, got, c.want)
		}
	}
}

// TestShouldRotateClassification 校验同请求换号的触发条件。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，换号判定回归。
func TestShouldRotateClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   int
		want   bool
	}{
		{"429 换号", 429, 0, true},
		{"402 换号", 402, 0, true},
		{"401 换号", 401, 0, true},
		{"403 换号", 403, 0, true},
		{"流内限流换号", 200, 710022004, true},
		{"500 不烧预算", 500, 0, false},
		{"传输错误不烧预算", 0, 0, false},
		{"业务 400 不换号", 400, 0, false},
	}
	for _, c := range cases {
		if got := shouldRotate(c.status, c.code); got != c.want {
			t.Errorf("%s：shouldRotate(%d, %d) = %v，期望 %v", c.name, c.status, c.code, got, c.want)
		}
	}
}

// TestNoteAccountFailureAndReset 校验失败记账、冷却生效与成功清零。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试点②③冷却与清零。
func TestNoteAccountFailureAndReset(t *testing.T) {
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	const authID = "doubao-failovertest"
	if isAccountCoolingDown(authID) {
		t.Fatal("初始状态不应处于冷却")
	}
	if !noteAccountFailure(authID, http.StatusTooManyRequests, 0) {
		t.Fatal("429 应计入账号冷却")
	}
	if accountFailureCount(authID) != 1 {
		t.Errorf("连续失败次数应为 1，实际 %d", accountFailureCount(authID))
	}
	if !isAccountCoolingDown(authID) {
		t.Fatal("429 后账号应处于冷却窗口")
	}

	resetAccountFailover(authID)
	if isAccountCoolingDown(authID) {
		t.Fatal("一次成功后应解除冷却")
	}
	if accountFailureCount(authID) != 0 {
		t.Errorf("成功后失败计数应清零，实际 %d", accountFailureCount(authID))
	}
}

// TestNoteAccountFailureIgnoresBusiness400 校验业务 400 不进入冷却。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，6-review 明确要求的负向用例。
func TestNoteAccountFailureIgnoresBusiness400(t *testing.T) {
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	const authID = "doubao-business400"
	if noteAccountFailure(authID, http.StatusBadRequest, 0) {
		t.Fatal("业务 400 不应计入账号冷却")
	}
	if accountFailureCount(authID) != 0 {
		t.Errorf("业务 400 后失败次数应保持 0，实际 %d", accountFailureCount(authID))
	}
	if isAccountCoolingDown(authID) {
		t.Fatal("业务 400 后账号不应进入冷却")
	}
}

// TestNoteAccountFailureCapsCooldown 校验连续失败超过阶梯上限时冷却不超过 600s。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，封顶回归。
func TestNoteAccountFailureCapsCooldown(t *testing.T) {
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	const authID = "doubao-laddercap"
	for i := 0; i < 25; i++ {
		noteAccountFailure(authID, http.StatusInternalServerError, 0)
	}
	if accountFailureCount(authID) != 25 {
		t.Errorf("连续失败次数应为 25，实际 %d", accountFailureCount(authID))
	}

	failoverMu.Lock()
	remaining := time.Until(failoverStates[authID].CooldownUntil)
	failoverMu.Unlock()
	if remaining <= 570*time.Second || remaining > 600*time.Second {
		t.Errorf("封顶冷却应接近 600s，实际剩余 %v", remaining)
	}
}

// TestPickAccountSkipsDisabledAndCooling 校验选号跳过停用与冷却账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试点②冷却期间跳过。
func TestPickAccountSkipsDisabledAndCooling(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-disabled", true)
	writeFailoverAccount(t, dir, "doubao-cooling", false)
	writeFailoverAccount(t, dir, "doubao-healthy", false)
	noteAccountFailure("doubao-cooling", http.StatusTooManyRequests, 0)

	picked, errPick := pickAccount("")
	if errPick != nil {
		t.Fatalf("应能选出健康账号：%v", errPick)
	}
	if picked.AuthID != "doubao-healthy" {
		t.Errorf("应跳过停用与冷却账号，实际选中 %s", picked.AuthID)
	}
}

// TestPickAccountExcludesFailedAccount 校验换号时排除刚失败的账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，同请求换号回归。
func TestPickAccountExcludesFailedAccount(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-first", false)
	writeFailoverAccount(t, dir, "doubao-second", false)

	picked, errPick := pickAccount("doubao-first")
	if errPick != nil {
		t.Fatalf("应能换到另一个账号：%v", errPick)
	}
	if picked.AuthID != "doubao-second" {
		t.Errorf("换号应排除刚失败的账号，实际选中 %s", picked.AuthID)
	}
}

// TestPickAccountExhausted 校验池内无可承载账号时返回明确错误。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试点④全灭。
func TestPickAccountExhausted(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-only", true)
	if _, errPick := pickAccount(""); errPick == nil {
		t.Fatal("全部账号停用时应返回无可用账号")
	}
}

// TestSetAccountDisabledPersistsPhysically 校验启停直写物理文件（不走宿主 RPC）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试点⑤账号持久化。
func TestSetAccountDisabledPersistsPhysically(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	const authID = "doubao-persist"
	writeFailoverAccount(t, dir, authID, false)

	if errSet := setAccountDisabled(authID, true); errSet != nil {
		t.Fatalf("禁用账号失败：%v", errSet)
	}

	sa, errRead := readAuthFile(filepath.Join(dir, authID+".json"))
	if errRead != nil {
		t.Fatalf("读取落盘账号失败：%v", errRead)
	}
	if !sa.Disabled {
		t.Fatal("禁用状态必须直写物理文件")
	}
	if sa.UpdatedAt == "" {
		t.Error("启停后应刷新 UpdatedAt")
	}
}

// TestHandleSchedulerPickDefersWhenNoUsableCandidate 校验无可用候选时让位宿主。
//
// 全灭 defer 是跨 provider 容灾的前提：只有返回 Handled:false，宿主才会把
// 请求改派给其它 provider，而不是钉死在本 provider 的死账号上。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，TASK-008 全灭 defer。
func TestHandleSchedulerPickDefersWhenNoUsableCandidate(t *testing.T) {
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	cases := []struct {
		name       string
		candidates []pluginapi.SchedulerAuthCandidate
	}{
		{"无候选", nil},
		{"仅其它 provider", []pluginapi.SchedulerAuthCandidate{{ID: "wb-1", Provider: "workbuddy"}}},
		{"已禁用", []pluginapi.SchedulerAuthCandidate{{ID: "doubao-off", Provider: providerName, Status: "disabled"}}},
		{"元数据禁用", []pluginapi.SchedulerAuthCandidate{{ID: "doubao-meta", Provider: providerName, Metadata: map[string]any{"disabled": true}}}},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Candidates: c.candidates})
		out, errPick := handleSchedulerPick(raw)
		if errPick != nil {
			t.Fatalf("%s：调度失败：%v", c.name, errPick)
		}
		if resp := decodeSchedulerPick(t, out); resp.Handled {
			t.Errorf("%s：应让位宿主（Handled:false），实际 %+v", c.name, resp)
		}
	}
}

// TestHandleSchedulerPickSkipsCoolingCandidate 校验冷却账号被剔除，全灭时 defer。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，冷却与 defer 联动。
func TestHandleSchedulerPickSkipsCoolingCandidate(t *testing.T) {
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	noteAccountFailure("doubao-cooling", http.StatusTooManyRequests, 0)
	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "doubao-cooling", Provider: providerName},
	}
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Candidates: candidates})
	out, errPick := handleSchedulerPick(raw)
	if errPick != nil {
		t.Fatalf("调度失败：%v", errPick)
	}
	if resp := decodeSchedulerPick(t, out); resp.Handled {
		t.Errorf("唯一候选冷却中时应 defer，实际 %+v", resp)
	}
}

// TestHandleSchedulerPickReturnsHostCandidate 校验只返回宿主候选集内的账号。
//
// 插件不能返回候选集之外的 AuthID：宿主不认识那个 ID，会以 unknown auth id
// 拒绝整次请求。这里在本地池里额外放一个账号，验证调度不会越界取号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，越界取号回归。
func TestHandleSchedulerPickReturnsHostCandidate(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-local-only", false)

	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "doubao-host-a", Provider: providerName},
		{ID: "doubao-host-b", Provider: providerName},
	}
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Candidates: candidates})
	out, errPick := handleSchedulerPick(raw)
	if errPick != nil {
		t.Fatalf("调度失败：%v", errPick)
	}
	resp := decodeSchedulerPick(t, out)
	if !resp.Handled {
		t.Fatal("有可用候选时应接管调度")
	}
	if resp.AuthID != "doubao-host-a" && resp.AuthID != "doubao-host-b" {
		t.Errorf("只能返回宿主候选集内的 AuthID，实际 %q", resp.AuthID)
	}
}

// TestRunWithFailoverRotatesOn429 校验 429 时换号并在新账号上成功。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，换号端到端回归。
func TestRunWithFailoverRotatesOn429(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-aaa", false)
	writeFailoverAccount(t, dir, "doubao-bbb", false)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":710022004,"message":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: SSE_ACK",
			`data: {"ack_client_meta":{"conversation_id":"conv-failover"}}`,
			"",
			"event: STREAM_MSG_NOTIFY",
			`data: {"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"换号后成功"}}}]}}`,
			"",
			"event: SSE_REPLY_END",
			"data: {}",
			"",
		}, "\n")))
	}))
	restore := swapCompletionEndpoint(srv.URL)
	t.Cleanup(func() {
		restore()
		srv.Close()
	})

	first := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "doubao-aaa",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000001",
	}
	res, errRun := runWithFailover(first, 5*time.Second, textMode(), "1+1=?", nil)
	if errRun != nil {
		t.Fatalf("换号后应成功：%v", errRun)
	}
	if !strings.Contains(res.Text, "换号后成功") {
		t.Errorf("应返回新账号的回复，实际 %q", res.Text)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("应请求 2 次（首发 429 + 换号成功），实际 %d", got)
	}
	if !isAccountCoolingDown("doubao-aaa") {
		t.Error("429 账号应进入冷却")
	}
	if accountFailureCount("doubao-bbb") != 0 {
		t.Error("成功账号不应残留失败计数")
	}
}

// TestRunWithFailoverStopsOnBusiness400 校验业务 400 不换号、不冷却。
//
// 换号只会把同一个错误请求打到另一个健康账号上，既救不回来又会误伤账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，业务错误不换号回归。
func TestRunWithFailoverStopsOnBusiness400(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	resetFailoverStates()
	t.Cleanup(resetFailoverStates)

	writeFailoverAccount(t, dir, "doubao-aaa", false)
	writeFailoverAccount(t, dir, "doubao-bbb", false)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":710020202,"message":"common invalid param"}`))
	}))
	restore := swapCompletionEndpoint(srv.URL)
	t.Cleanup(func() {
		restore()
		srv.Close()
	})

	first := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "doubao-aaa",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		WebTabID:     "00000000-0000-4000-8000-000000000001",
	}
	if _, errRun := runWithFailover(first, 5*time.Second, textMode(), "1+1=?", nil); errRun == nil {
		t.Fatal("业务 400 应原样返回失败")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("业务 400 不应换号，实际请求 %d 次", got)
	}
	if isAccountCoolingDown("doubao-aaa") || isAccountCoolingDown("doubao-bbb") {
		t.Error("业务 400 不应冷却任何账号")
	}
}

// decodeSchedulerPick 解析调度响应信封。
// [参数] t: 测试上下文；raw: 信封 JSON。
// [返回] 调度响应。
// 最近修改时间：2026-10-17；改动原因：新增，调度响应断言辅助。
func decodeSchedulerPick(t *testing.T, raw []byte) pluginapi.SchedulerPickResponse {
	t.Helper()
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("解析调度信封失败：%v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("调度响应应为成功信封：%s", raw)
	}
	var resp pluginapi.SchedulerPickResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		t.Fatalf("解析调度结果失败：%v", errUnmarshal)
	}
	return resp
}
