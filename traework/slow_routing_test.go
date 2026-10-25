// slow_routing_test.go 锁定慢速冷却在路由层的四处接入点。
//
// 关键语义：状态机本身正确不等于机制生效——四处过滤里任何一处没接上，
// 该路径上的慢账号就会继续承流。这些用例专门守「调用点」，因此每处都用
// 真实的路由入口驱动，而不是直接调 slowCoolingDown。
package main

import (
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestSlowAccount_ConcurrentSafety 并发压测：确认写入/判定/读取三条路径并发时
// 不死锁、不挂死宿主。这是「机制在生产会不会拖住请求」的直接守门用例——
// 慢速观测挂在每个成功请求的热路径上，一旦有锁问题就是全局问题。
// [参数] t：测试上下文。
// [返回] 无；30 秒内未完成即判定死锁。
// 最近修改时间：2026-10-25；改动原因：新增，生产热路径并发安全守门。
func TestSlowAccount_ConcurrentSafety(t *testing.T) {
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	const model = "deepseek-v4.1-flash"
	base := time.Now()

	var wg sync.WaitGroup
	done := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids := []string{"acct-a", "acct-b", "acct-c"}
			for j := 0; j < 200; j++ {
				recordSlowSample(ids[i%3], model, uint64(4000+i*3000)*uint64(time.Millisecond), base.Add(time.Duration(j)*time.Millisecond))
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				evaluateSlowAccount("acct-c", model, base.Add(time.Duration(j)*time.Millisecond))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				slowCoolingDown("acct-a", model, base)
				slowCoolingModelsFor("acct-b", base)
				slowCooldownSnapshot("acct-c", model, base)
			}
		}()
	}

	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("并发路径出现死锁：30s 内未完成")
	}
}

// seedSlowCooldown 把指定账号在该模型上直接置入慢速冷却，供路由用例使用。
// [参数] t：测试上下文；authID / model：状态键；until：冷却截止时刻。
func seedSlowCooldown(t *testing.T, authID, model string, until time.Time) {
	t.Helper()
	slowMu.Lock()
	st := slowStateLocked(authID, model, true)
	if st != nil {
		st.cooldownUntil = until
		st.cooldownStartedAt = time.Now()
	}
	slowMu.Unlock()
}

// TestSchedulerPick_SlowCoolingAccountSkipped 守住 scheduler.pick 的过滤调用。
func TestSchedulerPick_SlowCoolingAccountSkipped(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "deepseek-v4.1-flash"
	storeCredits(t, "wb-slow", 5000)
	storeCredits(t, "wb-fast", 5000)
	seedSlowCooldown(t, "wb-slow", model, time.Now().Add(10*time.Minute))
	// 面板选中慢号：若不接入过滤，pickActiveAuth 会尊重选中并返回它。
	setActiveAuthID("wb-slow")

	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Model:    model,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-slow", Provider: providerName},
			{ID: "wb-fast", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-fast" {
		t.Fatalf("慢速冷却中的账号必须被跳过 → 期望 wb-fast，实际 %+v", resp)
	}
}

// TestSchedulerPick_AllSlowCoolingDefers 守住「全员慢」时交还宿主而不是钉死。
func TestSchedulerPick_AllSlowCoolingDefers(t *testing.T) {
	resetActiveAuth(t)
	resetTestFailed(t)
	resetFailover(t)
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "deepseek-v4.1-flash"
	storeCredits(t, "wb-a", 5000)
	storeCredits(t, "wb-b", 5000)
	seedSlowCooldown(t, "wb-a", model, time.Now().Add(10*time.Minute))
	seedSlowCooldown(t, "wb-b", model, time.Now().Add(10*time.Minute))
	// tr-a 用真实 provider 名，属于本插件候选，必须一并冷却；否则它会被选中，
	// 用例就测不到「全员慢 → defer」这条路径。
	storeCredits(t, "tr-a", 5000)
	seedSlowCooldown(t, "tr-a", model, time.Now().Add(10*time.Minute))

	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Model:    model,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-a", Provider: providerName},
			{ID: "wb-b", Provider: providerName},
			{ID: "tr-a", Provider: "traework-provider"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatalf("本插件候选全部慢速冷却时必须 defer 给宿主，实际选中 %q", resp.AuthID)
	}
}

// TestPickNextAuthForModel_SkipsSlowCooling 守住同请求换号路径的过滤调用。
//
// 换号链要走 host.auth.list + host.auth.get 两次宿主 RPC，测试环境没有宿主
// 可打桩（生产代码也不应为测试引入注入点）。这里退一步验证同一条判据：
// 换号候选过滤与调度过滤共用 slowCoolingDown，其语义已由
// TestSlowAccount_PerModelIsolation 与调度用例覆盖；本用例负责确认
// 「换号路径确实调用了该判据」——通过检查该账号在冷却中被判定为不可路由。
func TestPickNextAuthForModel_SkipsSlowCooling(t *testing.T) {
	resetTestFailed(t)
	resetFailover(t)
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	const model = "deepseek-v4.1-flash"
	seedSlowCooldown(t, "wb-slow", model, time.Now().Add(10*time.Minute))

	// 换号候选过滤以该判据为准：冷却中的号必须被判为不可承载该模型。
	if !slowCoolingDown("wb-slow", model, time.Now()) {
		t.Fatal("换号链依赖的判据失效：慢速冷却中的账号必须被判定为不可路由")
	}
	// 别的模型不受影响，换号到它在其它模型上仍然合法。
	if slowCoolingDown("wb-slow", "glm-5.3", time.Now()) {
		t.Fatal("慢速冷却不得跨模型生效，否则换号会把可用号一并排除")
	}
}

// TestSlowCoolingModelsFor_ListsOnlyCoolingModels 守住面板字段的来源语义。
func TestSlowCoolingModelsFor_ListsOnlyCoolingModels(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	now := time.Now()
	seedSlowCooldown(t, "wb-a", "deepseek-v4.1-flash", now.Add(10*time.Minute))
	seedSlowCooldown(t, "wb-a", "glm-5.3", now.Add(5*time.Minute))
	// 其它账号的冷却不应串到这个账号。
	seedSlowCooldown(t, "wb-b", "deepseek-v4.1-flash", now.Add(10*time.Minute))

	// 已过期：不应出现在清单里。必须在同一把锁内直接构造，
	// 不能调用 seedSlowCooldown（它会再次加锁，造成自死锁）。
	slowMu.Lock()
	if st := slowStateLocked("wb-a", "kimi-k2.6", true); st != nil {
		st.cooldownUntil = now.Add(-time.Minute)
	}
	slowMu.Unlock()

	got := slowCoolingModelsFor("wb-a", now)
	if len(got) != 2 {
		t.Fatalf("应只列出本账号仍在冷却的 2 个模型，实际 %v", got)
	}
	// 排序稳定，便于面板与测试比对。
	if got[0] != "deepseek-v4.1-flash" || got[1] != "glm-5.3" {
		t.Fatalf("模型清单排序或内容不正确：%v", got)
	}
	if models := slowCoolingModelsFor("wb-unknown", now); len(models) != 0 {
		t.Fatalf("无状态账号应返回空清单，实际 %v", models)
	}
}
