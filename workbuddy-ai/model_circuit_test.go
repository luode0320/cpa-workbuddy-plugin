// model_circuit_test.go 验证 11128「请求被拦截」的记账落点。
//
// 历史：本文件曾覆盖独立熔断状态机（跨账号聚合、60 秒闸门、窗口清理）。
// 2026-10-20 重构为「11128 直接写账号×模型冷却」，2026-10-21 起改为双写：
// 冷却阶梯（accountFailover）+ 盘上禁用（failover_disable），恢复只由 10 分钟
// 禁用探活决定，冷却探活已移除。因此这里的用例聚焦三件事：
//  1. 判据边界（11128 / 11140 / 6004 不能混淆）；
//  2. 命中后两侧都落：模型级冷却 + 账号写盘禁用；
//  3. 探活成功一起解除、失败保持禁用且不写任何盘——这是取代熔断的那条闭环。
package main

import (
	"errors"
	"strings"
	"testing"
)

// errFakeProbeFailed 是探活失败的注入错误，用于验证保持禁用路径。
var errFakeProbeFailed = errors.New("fake probe failed")

// TestIsModelBlockedErrorClassifiesUpstreamRiskControl 验证风控错误分类边界：
// 11128 是被拦截、11140 是账号级封禁、6004 是频率限制，三者必须区分开。
func TestIsModelBlockedErrorClassifiesUpstreamRiskControl(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "11128 请求被拦截",
			status: 400,
			body:   `{"code":11128,"msg":"request illegal","displayMsg":{"zh":"请求被拦截，请重新发送"}}`,
			want:   true,
		},
		{
			name:   "11128 以 403 返回同样命中",
			status: 403,
			body:   `{"code":11128,"msg":"request illegal"}`,
			want:   true,
		},
		{
			name:   "11140 账号级封禁必须排除",
			status: 403,
			body:   `{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been blocked"}}`,
			want:   false,
		},
		{
			name:   "11140 与 11128 混排时以账号级为准",
			status: 403,
			body:   `{"code":11140,"msg":"request illegal","ref":"11128"}`,
			want:   false,
		},
		{
			name:   "6004 频率限制不属拦截",
			status: 400,
			body:   `{"code":6004,"msg":"usage exceeds frequency limit"}`,
			want:   false,
		},
		{
			name:   "429 不在判定范围",
			status: 429,
			body:   `{"code":11128}`,
			want:   false,
		},
		{
			name:   "空正文不算",
			status: 400,
			body:   "",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModelBlockedError(tc.status, tc.body); got != tc.want {
				t.Fatalf("isModelBlockedError(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamFailureBlocks11128AtModelScope 验证 11128 被归到账号×模型
// 冷却，而不是「不记账」。这是整个方案的根因修复点：旧实现把它判成
// failureScopeNone，导致被拦截的组合永远不会冷却、也不会被探活验证。
func TestClassifyUpstreamFailureBlocks11128AtModelScope(t *testing.T) {
	body := `{"code":11128,"msg":"request illegal"}`
	if got := classifyUpstreamFailure(400, body); got != failureScopeAuthModel {
		t.Fatalf("11128 应归入账号×模型冷却, got %v", got)
	}
	// 11140 仍是账号级：封号换模型也不可用。
	if got := classifyUpstreamFailure(403, `{"code":11140}`); got != failureScopeAccount {
		t.Fatalf("11140 应归入账号级冷却, got %v", got)
	}
	// 普通业务 400 不记账：换账号同样失败。
	if got := classifyUpstreamFailure(400, `{"code":1234,"msg":"bad request"}`); got != failureScopeNone {
		t.Fatalf("普通 400 不应记账, got %v", got)
	}
}

// TestNoteModelBlockedHitWritesModelCooldown 验证一次 11128 命中就把该账号的
// 这个模型放进冷却，进而被路由跳过。
func TestNoteModelBlockedHitWritesModelCooldown(t *testing.T) {
	resetFailover(t)

	const (
		authA = "workbuddyai-auth-a.json"
		model = "deepseek-v4.1-flash"
	)

	if !noteModelBlockedHit(authA, model) {
		t.Fatal("命中 11128 应写入冷却")
	}
	if !isAuthModelCoolingDown(authA, model) {
		t.Fatal("写入后该账号应在该模型上处于冷却，路由才会跳过它")
	}
	// 只影响该模型：同账号的其它模型不受牵连。
	if isAuthModelCoolingDown(authA, "glm-5") {
		t.Fatal("11128 只应冷却命中的模型，不应牵连同账号的其它模型")
	}
	// 只影响该账号：其它账号的同模型不受牵连。
	if isAuthModelCoolingDown("workbuddyai-auth-b.json", model) {
		t.Fatal("11128 只应冷却命中账号，不应牵连其它账号")
	}
}

// TestModelBlockedDualWritesCooldownAndDisable 验证一次 11128 命中的双写落点：
// 既写账号×模型冷却（面板展示、写盘失败时的兜底排除），又把账号写盘禁用
// （宿主真正会跳过的判据）。冷却探活已按用户裁定移除，恢复只由 10 分钟禁用探活
// 决定，因此这里断言的是「两侧都落」，而不是「进入探活候选」。
func TestModelBlockedDualWritesCooldownAndDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)

	const (
		authA = "workbuddyai-auth-a"
		model = "deepseek-v4.1-flash"
	)
	h.addAccount(authA, "idx-"+authA, nil)

	if !noteModelBlockedHit(authA, model) {
		t.Fatal("命中 11128 应记账")
	}
	// 1. 冷却侧：模型维度只记到命中的那个模型，同账号其它模型没有冷却记录。
	//    注意隔离只能看快照，不能用 isAuthModelCoolingDown——它先看账号级，
	//    而本机制已把整个号禁用，所有模型都会被它判为不可用（用户裁定接受的
	//    精度降级），那样断言不出「模型维度只记一个」这个事实。
	if _, until, ok := authModelFailoverSnapshot(authA, model); !ok || until.IsZero() {
		t.Fatal("缺少账号×模型级冷却记录")
	}
	if _, until, ok := authModelFailoverSnapshot(authA, "glm-5"); ok && !until.IsZero() {
		t.Fatal("11128 只应给命中的模型记冷却，不应牵连同账号其它模型")
	}
	// 2. 禁用侧：盘上出现 disabled + 本机制标记 + 回放模型。
	doc := h.docOf("idx-" + authA)
	if doc["disabled"] != true {
		t.Fatalf("11128 应同时禁用该账号（宿主唯一真判据）, doc=%v", doc)
	}
	if doc[failoverDisableKey] != true {
		t.Fatalf("缺少本机制所有权标记: %v", doc[failoverDisableKey])
	}
	if doc[failoverDisableModelKey] != model {
		t.Fatalf("回放模型应为被拦截的那个, got %v", doc[failoverDisableModelKey])
	}
	// 3. 热路径判据：本地排除集合同步登记，覆盖写盘到 watcher 生效的窗口。
	if !isAccountCoolingDown(authA) {
		t.Fatal("记账后该账号必须被路由判据跳过")
	}
	// 4. 跨账号隔离：别的账号既没被禁、也没在同模型上记冷却。
	if isAccountCoolingDown("workbuddyai-auth-b") {
		t.Fatal("11128 只应禁用命中账号，不应牵连其它账号")
	}
	if isAuthModelCoolingDown("workbuddyai-auth-b", model) {
		t.Fatal("11128 只应冷却命中账号，不应牵连其它账号的同模型")
	}
}

// TestProbeSuccessClearsBothCooldownAndDisable 锁定双写的收口：禁用探活成功
// 必须同时清掉盘上禁用与内存冷却。只解一边会得到「盘上可用、插件仍排除」的
// 半恢复账号——宿主把请求路由过来，scheduler.pick 与同请求换号却仍跳过它。
func TestProbeSuccessClearsBothCooldownAndDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)

	const (
		authA = "workbuddyai-auth-a"
		model = "deepseek-v4.1-flash"
	)
	h.addAccount(authA, "idx-"+authA, nil)

	noteModelBlockedHit(authA, model)
	// 再加一次账号级失败，让账号级冷却也留着（探活不该只清模型维度）。
	recordFailureScoped(authA, "", failureScopeAccount)
	if !isAccountCoolingDown(authA) {
		t.Fatal("前置条件不成立：应处于禁用+冷却")
	}

	failoverPingFn = func(*storedAuth, string) error { return nil }
	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }); probed != 1 {
		t.Fatalf("应探测 1 个禁用账号, got %d", probed)
	}
	if h.docOf("idx-" + authA)["disabled"] != false {
		t.Fatal("探活成功应解除盘上禁用")
	}
	if isAccountCoolingDown(authA) {
		t.Fatal("解除禁用必须同时清掉冷却，否则该号回来了也接不到流量")
	}
	if _, until, ok := authModelFailoverSnapshot(authA, model); ok && !until.IsZero() {
		t.Fatal("模型维度冷却应随恢复一起清除")
	}
}

// TestProbeFailureKeepsAccountDisabled 锁定探活失败侧：不写任何盘、保持禁用，
// 并且绝不写 test_failed（该标签硬排除、永不自动过期）。
func TestProbeFailureKeepsAccountDisabled(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)

	const (
		authA = "workbuddyai-auth-a"
		model = "deepseek-v4.1-flash"
	)
	h.addAccount(authA, "idx-"+authA, nil)
	noteModelBlockedHit(authA, model)
	writesAfterDisable := h.writeCount()

	failoverPingFn = func(*storedAuth, string) error { return errFakeProbeFailed }
	if probed := runFailoverProbeTick(func(string) *storedAuth { return &storedAuth{} }); probed != 1 {
		t.Fatalf("应探测 1 个禁用账号, got %d", probed)
	}
	if h.writeCount() != writesAfterDisable {
		t.Fatalf("探活失败不得写盘（会平白触发宿主 reload）, writes %d -> %d",
			writesAfterDisable, h.writeCount())
	}
	if h.docOf("idx-" + authA)["disabled"] != true {
		t.Fatal("探活失败应保持禁用，等下一轮")
	}
	if !isAccountCoolingDown(authA) {
		t.Fatal("探活失败后仍应被路由判据跳过")
	}
	if isTestFailed(authA) {
		t.Fatal("禁用探活失败绝不能写 test_failed 标签")
	}
}

// TestAccountLevelFailureDualWritesCooldownAndDisable 验证双写不只对 11128 生效：
// 账号级失败（401）同样既写冷却阶梯又写盘禁用。盘上记录的模型就是探活唯一的目标
// ——虽然账号级故障与模型无关，但没有这个键就无法验证恢复，所以记账侧必须带上
// 当时正在用的那个模型。
//
// [返回] 无；断言失败直接 t.Fatal。
// 最近修改时间：2026-10-21；改动原因：新增，补齐账号级维度的双写覆盖。
func TestAccountLevelFailureDualWritesCooldownAndDisable(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)

	const authA = "acct-dual"
	h.addAccount(authA, "idx-"+authA, nil)

	if !recordAccountFailureForModel(authA, "glm-4.6", 401, "") {
		t.Fatal("401 应计入记账")
	}
	count, until, ok := failoverStateSnapshot(authA)
	if !ok || count != 1 || until.IsZero() {
		t.Fatalf("冷却侧未落: count=%d until=%v ok=%v", count, until, ok)
	}
	doc := h.docOf("idx-" + authA)
	if doc["disabled"] != true || doc[failoverDisableKey] != true {
		t.Fatalf("禁用侧未落: %v", doc)
	}
	if doc[failoverDisableModelKey] != "glm-4.6" {
		t.Fatalf("优先回放模型应为记账时的模型, got %v", doc[failoverDisableModelKey])
	}
	if !isAccountCoolingDown(authA) {
		t.Fatal("账号级失败后该号必须被路由判据跳过")
	}
}

// TestRepeatedFailuresDoNotRewriteDisk 锁定双写下的失败风暴成本：同一号在一个
// 故障生命周期内只写一次盘，后续失败只递增冷却阶梯。否则上游抖动会让宿主
// watcher 被反复触发 reload，并把 RPC 塞进每个请求。
//
// [返回] 无；断言失败直接 t.Fatal。
// 最近修改时间：2026-10-21；改动原因：新增，锁定禁用写盘的 RPC 短路。
func TestRepeatedFailuresDoNotRewriteDisk(t *testing.T) {
	resetFailover(t)
	h := newFailoverFakeHost(t)

	const authA = "storm"
	h.addAccount(authA, "idx-"+authA, nil)

	for i := 1; i <= 3; i++ {
		if !recordAccountFailureForModel(authA, "glm-4.6", 500, "") {
			t.Fatalf("第 %d 次失败应记账", i)
		}
	}
	if got := h.writeCount(); got != 1 {
		t.Fatalf("3 次连续失败应只写盘 1 次, got %d", got)
	}
	count, _, ok := failoverStateSnapshot(authA)
	if !ok || count != 3 {
		t.Fatalf("冷却阶梯应随失败递增到 3, count=%d ok=%v", count, ok)
	}
}

// TestBlockedErrorBodyMarkersCoverProductionShape 用生产实测报文做回归，
// 防止上游文案变化后判定静默失效。
func TestBlockedErrorBodyMarkersCoverProductionShape(t *testing.T) {
	// 生产原样报文（2026-10-18，1,173 次）
	production := `{"code":11128,"msg":"request illegal","requestId":"e1a6043f-92ab-4eb8-b5c0-a1791dea9a24","displayMsg":{"en":"Request blocked. Please send it again","zh":"请求被拦截，请重新发送"}}`
	if !isModelBlockedError(400, production) {
		t.Fatal("生产 11128 报文必须被识别为请求被拦截")
	}
	if !strings.Contains(production, "11128") {
		t.Fatal("回归样本本身应包含 11128")
	}
}
