// slow_logging_test.go 锁定慢速冷却的可观测性契约。
//
// 背景：0.21.0 上线后排查误判时，只能靠轮询 /accounts 与反推 token-usage
// 数据，无法从日志回答「判了谁、为什么判、参数是什么时候被改的」。
// 本文件守住三条留痕：判定进入、防抽干放开、冷却到期恢复，以及配置变更。
package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog 把标准库日志重定向到缓冲区，返回读取函数与恢复函数。
// [参数] 无。
// [返回] buf：日志缓冲区；restore：恢复原输出（必须 defer 调用）。
func captureLog() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	return buf, func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	}
}

// TestSlowCooldownLogsJudgementInputs 守住判定日志必须带齐判据三要素。
//
// 只记「某号进入冷却」没有排障价值：必须能直接看出中位数、基线、阈值倍数，
// 否则无法判断是账号真慢还是阈值配错。
func TestSlowCooldownLogsJudgementInputs(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	base := time.Now()
	model := "deepseek-v4.1-flash"
	feedSlowSamples("log-fast", model, 4000, 5, base)
	feedSlowSamples("log-slow", model, 20000, 5, base)

	buf, restore := captureLog()
	defer restore()

	if !evaluateSlowAccount("log-slow", model, base.Add(time.Minute)) {
		t.Fatal("前置：慢号必须被判慢")
	}

	out := buf.String()
	for _, want := range []string{
		"slow cooldown entered",
		"auth=log-slow",
		"model=" + model,
		"median=20000ms",
		"baseline=4000ms",
		"ratio=5.0x",
		"threshold=2.5x",
		"samples=5",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("判定日志缺少关键字段 %q，实际输出：%s", want, out)
		}
	}
}

// TestSlowCooldownLogsPoolDrainRelease 守住防抽干放开必须单独留痕。
//
// 放开与「到期自然恢复」在面板上表现相同（都不再冷却），但成因完全不同。
// 没有这条日志时，线上无法区分「机制按预期腾出候选」与「冷却被意外清掉」。
func TestSlowCooldownLogsPoolDrainRelease(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	base := time.Now()
	model := "deepseek-v4.1-flash"
	// 先让两个号分别被判慢，制造「同模型已有冷却号」的局面。
	feedSlowSamples("drain-a", model, 20000, 5, base)
	feedSlowSamples("drain-b", model, 20000, 5, base)
	feedSlowSamples("drain-fast", model, 4000, 5, base)

	if !evaluateSlowAccount("drain-a", model, base.Add(time.Minute)) {
		t.Fatal("前置：drain-a 必须被判慢")
	}

	buf, restore := captureLog()
	defer restore()

	// 此时同模型只剩 drain-b 与 drain-fast 未冷却，drain-b 再判慢会触发放开。
	if !evaluateSlowAccount("drain-b", model, base.Add(2*time.Minute)) {
		t.Fatal("前置：drain-b 必须被判慢")
	}

	out := buf.String()
	if !strings.Contains(out, "slow cooldown released to avoid pool drain") {
		t.Fatalf("防抽干放开必须留痕，实际输出：%s", out)
	}
	if !strings.Contains(out, "model="+model) {
		t.Fatalf("放开日志必须带模型名，实际输出：%s", out)
	}
}

// TestSlowCooldownLogsExpiryOnRecovery 守住冷却到期恢复留痕。
//
// 恢复检测放在成功打点路径而非路由热路径：路由判定必须保持纯读。
// 这里验证冷却过期后下一次成功样本会打出 expired 日志。
func TestSlowCooldownLogsExpiryOnRecovery(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	base := time.Now()
	model := "deepseek-v4.1-flash"
	feedSlowSamples("expire-a", model, 20000, 5, base)
	feedSlowSamples("expire-fast", model, 4000, 5, base)

	if !evaluateSlowAccount("expire-a", model, base.Add(time.Minute)) {
		t.Fatal("前置：expire-a 必须被判慢")
	}

	buf, restore := captureLog()
	defer restore()

	// 冷却在 base+1min 起算 10 分钟，即 base+11min 到期；取 12 分钟确保已过期。
	recordSlowSample("expire-a", model, 4200*uint64(time.Millisecond), base.Add(12*time.Minute))

	out := buf.String()
	if !strings.Contains(out, "slow cooldown expired") {
		t.Fatalf("冷却到期恢复必须留痕，实际输出：%s", out)
	}
	if !strings.Contains(out, "auth=expire-a") {
		t.Fatalf("到期日志必须带账号标识，实际输出：%s", out)
	}
}

// TestSlowCoolingDownStaysReadOnly 守住路由判定不写状态。
//
// slowCoolingDown 挂在每请求热路径，且被面板与换号过滤共用。
// 若它顺手回收冷却状态，读取语义就会变成写入，面板展示与路由判断会互相干扰。
func TestSlowCoolingDownStaysReadOnly(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	base := time.Now()
	model := "deepseek-v4.1-flash"
	seedSlowCooldown(t, "ro-a", model, base.Add(10*time.Minute))

	// 远超冷却截止时刻后反复查询：状态必须保持不变。
	after := base.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if slowCoolingDown("ro-a", model, after) {
			t.Fatal("冷却已过期，slowCoolingDown 必须返回 false")
		}
	}

	slowMu.Lock()
	st := slowStateLocked("ro-a", model, false)
	stillSet := st != nil && !st.cooldownUntil.IsZero()
	slowMu.Unlock()
	if !stillSet {
		t.Fatal("slowCoolingDown 不得修改 cooldownUntil，到期回收不属于路由判定的职责")
	}
}

// TestSlowConfigChangeLogsApplied 守住配置变更留痕。
//
// 生产曾因临时调低的阈值被遗留而持续误判，且无任何日志痕迹。
// 这条用例确保参数真正变化时一定打印生效值。
func TestSlowConfigChangeLogsApplied(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	buf, restore := captureLog()
	defer restore()

	if err := configurePlugin(buildConfigYAML(
		"slow_account_enabled: true",
		"slow_ttft_ratio_x10: 40",
		"slow_window_minutes: 20",
		"slow_min_samples: 8",
		"slow_cooldown_minutes: 5",
		"slow_min_ttft_ms: 2500",
	)); err != nil {
		t.Fatalf("configurePlugin: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "slow config applied") {
		t.Fatalf("配置变更必须留痕，实际输出：%s", out)
	}
	for _, want := range []string{"ratio=4.0x", "window=20m", "min_samples=8", "cooldown=5m", "min_ttft=2500ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("配置日志缺少 %q，实际输出：%s", want, out)
		}
	}
}

// TestSlowConfigUnchangedStaysQuiet 守住重载不刷屏。
//
// 插件配置重载在宿主侧是高频动作；参数没变时不应产生日志噪音。
func TestSlowConfigUnchangedStaysQuiet(t *testing.T) {
	withSlowConfig(t, true, 25, 5, 3000, 15*time.Minute, 10*time.Minute)

	yaml := buildConfigYAML(
		"slow_account_enabled: true",
		"slow_ttft_ratio_x10: 25",
		"slow_window_minutes: 15",
		"slow_min_samples: 5",
		"slow_cooldown_minutes: 10",
		"slow_min_ttft_ms: 3000",
	)
	// 先用同一份配置对齐一次，消除与默认值的差异。
	_ = configurePlugin(yaml)

	buf, restore := captureLog()
	defer restore()

	_ = configurePlugin(yaml)

	if strings.Contains(buf.String(), "slow config applied") {
		t.Fatalf("参数未变化时不应打印配置日志，实际输出：%s", buf.String())
	}
}

// seedSlowCooldown 把指定账号在该模型上直接置入慢速冷却，供断言使用。
// [参数] t：测试上下文；authID / model：状态键；until：冷却截止时刻。
func seedSlowCooldown(t *testing.T, authID, model string, until time.Time) {
	t.Helper()
	slowMu.Lock()
	st := slowStateLocked(authID, model, true)
	if st != nil {
		st.cooldownUntil = until
	}
	slowMu.Unlock()
}
