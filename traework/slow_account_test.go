// slow_account_test.go 覆盖慢速账号 TTFT 冷却的核心判据。
//
// 重点保护五条容易回归的语义：
//  1. 相对判据：慢号被冷却，同池快号不受影响；
//  2. 全池同慢不冷却：基准随全池抬高，不把「上游整体拥堵」误判成账号问题；
//  3. 样本门槛与绝对下限：样本不足、延迟过低的场景都不判定；
//  4. 失败请求不计入样本：错误耗时不是首字延迟；
//  5. 防抽干：同模型全池都被判慢时放开一半，不把调度饿死。
package main

import (
	"encoding/json"
	"testing"
	"time"
)

// withSlowConfig 临时改写慢速冷却配置并在测试结束后恢复。
// [参数] t：测试上下文；enabled：开关；ratioX10：倍数阈值 ×10；
//
//	window / cooldown：窗口与冷却时长；minSamples：样本门槛；
//	minTTFT：绝对下限。
func withSlowConfig(t *testing.T, enabled bool, ratioX10, minSamples, minTTFTMs int, window, cooldown time.Duration) {
	t.Helper()
	slowAccountEnabledMu.Lock()
	oldEnabled := slowAccountEnabled
	slowAccountEnabled = enabled
	slowAccountEnabledMu.Unlock()

	slowConfigMu.Lock()
	oldRatio := slowTTFTRatioX10
	oldWindow := slowWindowMinutes
	oldMinSamples := slowMinSamples
	oldCooldown := slowCooldownMinutes
	oldMinTTFT := slowMinTTFTMs
	slowTTFTRatioX10 = ratioX10
	slowWindowMinutes = int(window.Minutes())
	slowMinSamples = minSamples
	slowCooldownMinutes = int(cooldown.Minutes())
	slowMinTTFTMs = minTTFTMs
	slowConfigMu.Unlock()

	resetSlowStates()

	t.Cleanup(func() {
		slowAccountEnabledMu.Lock()
		slowAccountEnabled = oldEnabled
		slowAccountEnabledMu.Unlock()
		slowConfigMu.Lock()
		slowTTFTRatioX10 = oldRatio
		slowWindowMinutes = oldWindow
		slowMinSamples = oldMinSamples
		slowCooldownMinutes = oldCooldown
		slowMinTTFTMs = oldMinTTFT
		slowConfigMu.Unlock()
		resetSlowStates()
	})
}

// resetSlowStates 清空慢速状态表，保证用例之间互不污染。
func resetSlowStates() {
	slowMu.Lock()
	slowStates = make(map[string]*slowAccountState)
	slowMu.Unlock()
}

// feedSlowSamples 向指定账号×模型写入 count 条固定延迟样本。
// [参数] authID / model：状态键；ttftMS：延迟毫秒；count：样本条数；base：时间基准。
func feedSlowSamples(authID, model string, ttftMS int, count int, base time.Time) {
	ttftNS := uint64(ttftMS) * uint64(time.Millisecond)
	for i := 0; i < count; i++ {
		recordSlowSample(authID, model, ttftNS, base.Add(time.Duration(i)*time.Second))
	}
}

func TestSlowAccount_SlowAccountCoolsDownFastOnesDoNot(t *testing.T) {
	// AC-001：一个慢号 + 两个快号，只有慢号被冷却。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4200, 5, base)
	feedSlowSamples("slow-c", model, 20000, 5, base)

	now := base.Add(time.Minute)
	if !evaluateSlowAccount("slow-c", model, now) {
		t.Fatal("慢号（20s vs 4s 基准）必须被判慢")
	}
	for _, id := range []string{"fast-a", "fast-b"} {
		if evaluateSlowAccount(id, model, now) {
			t.Fatalf("快号 %s 不应被冷却", id)
		}
	}
	if !slowCoolingDown("slow-c", model, now) {
		t.Fatal("被判慢的账号必须处于冷却中")
	}
	if slowCoolingDown("fast-a", model, now) {
		t.Fatal("快号不应处于慢速冷却")
	}
}

func TestSlowAccount_AllSlowPoolDoesNotCoolAnyone(t *testing.T) {
	// AC-004：全池同模型都慢时基准同步抬高，无人被冷却。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	feedSlowSamples("acct-a", model, 20000, 5, base)
	feedSlowSamples("acct-b", model, 20500, 5, base)
	feedSlowSamples("acct-c", model, 21000, 5, base)

	now := base.Add(time.Minute)
	for _, id := range []string{"acct-a", "acct-b", "acct-c"} {
		if evaluateSlowAccount(id, model, now) {
			t.Fatalf("全池同慢时 %s 不应被冷却（基准应随全池抬高）", id)
		}
	}
}

func TestSlowAccount_MajoritySlowPoolStillDetected(t *testing.T) {
	// 慢号占多数是常见形态（上游对部分账号限速）。基准若用中位数会被多数
	// 慢号抬到慢号水平，结果一个也判不出来——机制静默失效。
	// 用下四分位后，少数健康号仍能定义基准。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("slow-b", model, 20000, 5, base)
	feedSlowSamples("slow-c", model, 20500, 5, base)

	now := base.Add(time.Minute)
	if !evaluateSlowAccount("slow-c", model, now) {
		t.Fatal("慢号占多数时仍必须能判出慢号（基准不得被多数慢号抬走）")
	}
	if evaluateSlowAccount("fast-a", model, now) {
		t.Fatal("唯一的快号不应被判慢")
	}
}

func TestSlowAccount_BaselineUsesLowQuantileNotMinimum(t *testing.T) {
	// 基准既不能被多数慢号抬走（P50 的问题），也不能被单个异常快号压到
	// 地板上（最小值的问题）——后者会让正常账号成批被判慢。
	// 五账号：1 个异常快 + 1 个健康 + 3 个正常偏快，验证基准落在 P25 而非最小。
	withSlowConfig(t, true, 25, 3, 500, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	feedSlowSamples("ultra-fast", model, 100, 5, base)
	feedSlowSamples("fast-a", model, 5000, 5, base)
	feedSlowSamples("mid-a", model, 6000, 5, base)
	feedSlowSamples("mid-b", model, 6200, 5, base)
	feedSlowSamples("mid-c", model, 6400, 5, base)

	// 基准为 P25：{100, 5000, 6000, 6200, 6400} 排序后取 idx=int(0.25*4)=1 → 5000。
	// 若退化取最小值 100，则 mid-* 三个号都超过 2.5 倍，会被批量误判。
	now := base.Add(time.Minute)
	for _, id := range []string{"fast-a", "mid-a", "mid-b", "mid-c"} {
		if evaluateSlowAccount(id, model, now) {
			t.Fatalf("%s 是正常量级账号，不应被判慢（基准不得被单个超快号压到最小值）", id)
		}
	}
}

func TestSlowAccount_InsufficientSamplesNeverJudged(t *testing.T) {
	// AC-005：样本数低于门槛时不判定。
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4000, 5, base)
	feedSlowSamples("slow-c", model, 60000, 4, base) // 只差一条

	if evaluateSlowAccount("slow-c", model, base.Add(time.Minute)) {
		t.Fatal("样本数不足时不得判慢")
	}
}

func TestSlowAccount_AbsoluteFloorBlocksLowLatencyMisjudgement(t *testing.T) {
	// AC-001 下界：全池都很快时，相对倍数放大不能触发冷却。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	// 600ms 是中位数基准的 3 倍，但绝对低于 3000ms 下限。
	feedSlowSamples("fast-a", model, 200, 5, base)
	feedSlowSamples("fast-b", model, 200, 5, base)
	feedSlowSamples("acct-c", model, 600, 5, base)

	if evaluateSlowAccount("acct-c", model, base.Add(time.Minute)) {
		t.Fatal("绝对延迟低于下限时不得判慢")
	}
}

func TestSlowAccount_CooldownExpiresWithoutExternalAction(t *testing.T) {
	// AC-003：冷却到期自动放行，无需探活或人工解冷。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4200, 5, base)
	feedSlowSamples("slow-c", model, 20000, 5, base)

	judgeAt := base.Add(time.Minute)
	if !evaluateSlowAccount("slow-c", model, judgeAt) {
		t.Fatal("前置：慢号必须被判慢")
	}
	if !slowCoolingDown("slow-c", model, judgeAt.Add(5*time.Minute)) {
		t.Fatal("冷却窗口内不应提前放行")
	}
	if slowCoolingDown("slow-c", model, judgeAt.Add(11*time.Minute)) {
		t.Fatal("冷却到期后必须自动放行")
	}
}

func TestSlowAccount_ExpiredCooldownDoesNotInstantlyRecooldown(t *testing.T) {
	// 防抖：解冷后旧样本不得让它立刻被再次判慢，否则会形成冷却死循环。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4200, 5, base)
	feedSlowSamples("slow-c", model, 20000, 5, base)

	judgeAt := base.Add(time.Minute)
	if !evaluateSlowAccount("slow-c", model, judgeAt) {
		t.Fatal("前置：慢号必须被判慢")
	}

	// 冷却到期后立刻再判：旧样本在冷却起点之前，不应参与判定。
	afterExpiry := judgeAt.Add(11 * time.Minute)
	if slowCoolingDown("slow-c", model, afterExpiry) {
		t.Fatal("前置：此时应已解冷")
	}
	if evaluateSlowAccount("slow-c", model, afterExpiry) {
		t.Fatal("解冷后不得凭冷却前的旧样本立刻再次判慢")
	}
}

func TestSlowAccount_FailedRequestsDoNotEnterSamples(t *testing.T) {
	// AC-007：失败请求不计入慢速样本。
	// noteSlowObservation 只在成功路径调用；这里锁定「ttft 为 0 不记样本」这一层闸门。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	for i := 0; i < 10; i++ {
		recordSlowSample("acct-a", model, 0, base.Add(time.Duration(i)*time.Second))
	}
	slowMu.Lock()
	st := slowStateLocked("acct-a", model, false)
	count := 0
	if st != nil {
		count = len(st.samples)
	}
	slowMu.Unlock()
	if count != 0 {
		t.Fatalf("ttft=0 的样本不得入库，实际 %d 条", count)
	}
}

func TestSlowAccount_WindowDropsExpiredSamples(t *testing.T) {
	// 窗口语义：过期样本必须被裁剪，否则旧的高延迟会让账号永久被判慢。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	// 慢样本发生在很久以前，窗口内只剩快样本。
	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4000, 5, base)
	feedSlowSamples("acct-c", model, 60000, 5, base)

	// 1 小时后再补 5 条快样本：窗口（15 分钟）内只剩这 5 条快的。
	feedSlowSamples("acct-c", model, 4000, 5, base.Add(time.Hour))

	if evaluateSlowAccount("acct-c", model, base.Add(time.Hour+time.Minute)) {
		t.Fatal("窗口外的旧慢样本不得让账号继续被判慢")
	}
}

func TestSlowAccount_SharedReleasePreventsPoolStarvation(t *testing.T) {
	// AC-006：同模型全池都被判慢时，先放开一半再冷却本号，保证仍有号可路由。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("acct-a", model, 4000, 5, base)
	feedSlowSamples("acct-b", model, 4200, 5, base)
	feedSlowSamples("acct-c", model, 20000, 5, base)

	now := base.Add(time.Minute)
	if !evaluateSlowAccount("acct-c", model, now) {
		t.Fatal("前置：acct-c 应被判慢")
	}
	if !slowCoolingDown("acct-c", model, now) {
		t.Fatal("前置：acct-c 应在冷却中")
	}

	// 把两个快号也强行置入冷却，模拟「同模型全池都已被判慢」。
	slowMu.Lock()
	for _, id := range []string{"acct-a", "acct-b"} {
		if st := slowStateLocked(id, model, false); st != nil {
			st.cooldownUntil = now.Add(10 * time.Minute)
			st.cooldownStartedAt = now
		}
	}
	slowMu.Unlock()

	// 全池已冷时，防抽干必须先放开一半，最终仍需保留可路由账号。
	slowMu.Lock()
	released := releaseSharedSlowCooldownsLocked(model, "acct-c", now)
	slowMu.Unlock()
	if len(released) == 0 {
		t.Fatal("全池同模型都已冷却时必须放开一半，否则调度无号可选")
	}

	routeable := 0
	for _, id := range []string{"acct-a", "acct-b", "acct-c"} {
		if !slowCoolingDown(id, model, now) {
			routeable++
		}
	}
	if routeable == 0 {
		t.Fatal("防抽干失效：同模型全部账号都被冷却，调度将无号可选")
	}
}

func TestSlowAccount_ConfigParsedFromYAMLAndRegistered(t *testing.T) {
	// AC-009：6 个配置键必须既被 configure 解析，也登记进 ConfigFields。
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	configure(buildConfigYAML(
		"slow_account_enabled: true",
		"slow_ttft_ratio_x10: 40",
		"slow_window_minutes: 20",
		"slow_min_samples: 8",
		"slow_cooldown_minutes: 5",
		"slow_min_ttft_ms: 1500",
	))

	cfg := loadedSlowConfig()
	if !cfg.enabled {
		t.Fatal("slow_account_enabled: true 必须启用慢速冷却")
	}
	if cfg.ratioX10 != 40 {
		t.Fatalf("slow_ttft_ratio_x10 未生效，实际 %d", cfg.ratioX10)
	}
	if cfg.window != 20*time.Minute || cfg.minSamples != 8 || cfg.cooldown != 5*time.Minute {
		t.Fatalf("窗口/样本/冷却未生效：window=%v minSamples=%d cooldown=%v", cfg.window, cfg.minSamples, cfg.cooldown)
	}
	if cfg.minTTFTNano != 1500*uint64(time.Millisecond) {
		t.Fatalf("slow_min_ttft_ms 未生效，实际 %d ns", cfg.minTTFTNano)
	}

	names := map[string]bool{}
	for _, field := range wbRegistration().Metadata.ConfigFields {
		names[field.Name] = true
	}
	for _, want := range []string{
		"slow_account_enabled",
		"slow_ttft_ratio_x10",
		"slow_window_minutes",
		"slow_min_samples",
		"slow_cooldown_minutes",
		"slow_min_ttft_ms",
	} {
		if !names[want] {
			t.Fatalf("配置项 %q 必须登记进 ConfigFields，否则管理端看不到也改不了", want)
		}
	}
}

func TestSlowAccount_DisabledByConfigIsInert(t *testing.T) {
	// 总开关关闭时，任何样本都不得触发冷却。
	withSlowConfig(t, false, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "glm-5.3"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4200, 5, base)
	feedSlowSamples("slow-c", model, 20000, 5, base)

	if evaluateSlowAccount("slow-c", model, base.Add(time.Minute)) {
		t.Fatal("总开关关闭时不得判定慢速冷却")
	}
	if slowCoolingDown("slow-c", model, base.Add(time.Minute)) {
		t.Fatal("总开关关闭时 slowCoolingDown 必须恒为 false")
	}
}

func TestSlowAccount_PerModelIsolation(t *testing.T) {
	// AC-002 的核心：慢速冷却只作用于该模型，同账号其它模型不受影响。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const slowModel = "deepseek-v4.1-flash"
	const otherModel = "glm-5.3"

	feedSlowSamples("fast-a", slowModel, 4000, 5, base)
	feedSlowSamples("fast-b", slowModel, 4200, 5, base)
	feedSlowSamples("acct-c", slowModel, 20000, 5, base)

	if !evaluateSlowAccount("acct-c", slowModel, base.Add(time.Minute)) {
		t.Fatal("前置：acct-c 在慢模型上应被冷却")
	}
	if !slowCoolingDown("acct-c", slowModel, base.Add(time.Minute)) {
		t.Fatal("该账号在慢模型上必须处于冷却")
	}
	if slowCoolingDown("acct-c", otherModel, base.Add(time.Minute)) {
		t.Fatal("该账号在其它模型上不得被连带冷却")
	}
}

func TestSlowAccount_SnapshotReportsRemainingAndAverage(t *testing.T) {
	// 面板读取入口：冷却中应给出剩余时长与窗口平均 TTFT；未冷却时标记为 false。
	withSlowConfig(t, true, 25, 3, 3000, 15*time.Minute, 10*time.Minute)
	base := time.Now()
	const model = "deepseek-v4.1-flash"

	feedSlowSamples("fast-a", model, 4000, 5, base)
	feedSlowSamples("fast-b", model, 4200, 5, base)
	feedSlowSamples("slow-c", model, 20000, 5, base)

	if !evaluateSlowAccount("slow-c", model, base.Add(time.Minute)) {
		t.Fatal("前置：慢号必须被判慢")
	}
	remain, avg, cooling := slowCooldownSnapshot("slow-c", model, base.Add(time.Minute))
	if !cooling {
		t.Fatal("冷却中应返回 cooling=true")
	}
	if remain <= 0 || remain > 10*time.Minute {
		t.Fatalf("剩余冷却时长异常：%v", remain)
	}
	// 冷却时样本保留（冷却起点之前的样本不参与判定但仍可展示），
	// 因此平均值应等于喂入的 20s。
	if avg != uint64(20000*time.Millisecond) {
		t.Fatalf("冷却期间应能展示窗口平均延迟，实际 %d ns", avg)
	}

	_, _, coolingFast := slowCooldownSnapshot("fast-a", model, base.Add(time.Minute))
	if coolingFast {
		t.Fatal("快号不应被报告为冷却")
	}
}

// buildConfigYAML 把单行配置包成 plugin.register 期望的 config_yaml 结构。
// [参数] lines：配置行。
// [返回] 可直接传给 configure 的 JSON 字节。
// 最近修改时间：2026-10-25；改动原因：慢速冷却移植，本插件原无用例需要构造配置原文。
func buildConfigYAML(lines ...string) []byte {
	yaml := ""
	for _, line := range lines {
		yaml += line + "\n"
	}
	raw, _ := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
	return raw
}
