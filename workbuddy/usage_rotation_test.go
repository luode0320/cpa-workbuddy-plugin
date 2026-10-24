// usage_rotation_test.go 覆盖连续使用轮换的核心判据。
//
// 重点保护三条容易回归的语义：
//  1. 连续使用计时：首次打点、每次续期、中断超阈值后重置；
//  2. 让位条件：未满 serve 不让位，满了才让位，且冷却期为 cooldown；
//  3. 防抽干：除本号外全池已冷却时，先放开一半而不是把最后一个可用号也关掉。
package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// withRotationConfig 临时改写轮换配置并在测试结束后恢复。
func withRotationConfig(t *testing.T, enabled bool, serve, cooldown, idle time.Duration) {
	t.Helper()
	rotationEnabledMu.Lock()
	oldEnabled := rotationEnabled
	rotationEnabled = enabled
	rotationEnabledMu.Unlock()

	rotationConfigMu.Lock()
	oldServe := rotationServeMinutes
	oldCooldown := rotationCooldownMinutes
	oldIdle := rotationIdleResetMinutes
	rotationServeMinutes = int(serve.Minutes())
	rotationCooldownMinutes = int(cooldown.Minutes())
	rotationIdleResetMinutes = int(idle.Minutes())
	rotationConfigMu.Unlock()

	rotationMu.Lock()
	rotationStates = make(map[string]*rotationState)
	rotationMu.Unlock()

	t.Cleanup(func() {
		rotationEnabledMu.Lock()
		rotationEnabled = oldEnabled
		rotationEnabledMu.Unlock()
		rotationConfigMu.Lock()
		rotationServeMinutes = oldServe
		rotationCooldownMinutes = oldCooldown
		rotationIdleResetMinutes = oldIdle
		rotationConfigMu.Unlock()
		rotationMu.Lock()
		rotationStates = make(map[string]*rotationState)
		rotationMu.Unlock()
	})
}

func TestRotation_TracksContinuousUsageAndResetsAfterIdle(t *testing.T) {
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	// 步长必须小于 idle 阈值（10m），否则会被判定为中断——这正是该用例要守护的语义，
	// 因此按 5 分钟间隔推进到 40 分钟。
	for minute := 0; minute <= 40; minute += 5 {
		markAuthUsed("acct-a", base.Add(time.Duration(minute)*time.Minute))
	}

	if got := rotationServingMinutes("acct-a", base.Add(40*time.Minute)); got != 40 {
		t.Fatalf("continuous usage should keep the first touch as origin: got %d minutes, want 40", got)
	}

	// 中断超过 idle 阈值：重新计时，而不是继续累加到 100 分钟。
	markAuthUsed("acct-a", base.Add(40*time.Minute+11*time.Minute))
	if got := rotationServingMinutes("acct-a", base.Add(51*time.Minute)); got != 0 {
		t.Fatalf("idle reset should restart the clock: got %d minutes, want 0", got)
	}
}

func TestRotation_DoesNotRotateBeforeServeThreshold(t *testing.T) {
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	pool := []string{"acct-a", "acct-b"}
	markAuthUsed("acct-a", base)

	// 59 分钟：未达阈值，不应让位。
	if evaluateRotation("acct-a", pool, base.Add(59*time.Minute)) {
		t.Fatal("account must not rotate before reaching serve threshold")
	}
	if rotationCoolingDown("acct-a", base.Add(59*time.Minute)) {
		t.Fatal("account must stay routable before serve threshold")
	}
}

func TestRotation_RotatesAtServeThresholdAndHonorsCooldown(t *testing.T) {
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	pool := []string{"acct-a", "acct-b"}
	markAuthUsed("acct-a", base)

	if !evaluateRotation("acct-a", pool, base.Add(60*time.Minute)) {
		t.Fatal("account should rotate once serve threshold is reached")
	}
	if !rotationCoolingDown("acct-a", base.Add(60*time.Minute)) {
		t.Fatal("rotated account must be cooling down")
	}
	// 冷却 1 小时：59 分钟时仍在冷却，61 分钟时已解除。
	if !rotationCoolingDown("acct-a", base.Add(60*time.Minute+59*time.Minute)) {
		t.Fatal("rotation cooldown must last the configured window")
	}
	if rotationCoolingDown("acct-a", base.Add(60*time.Minute+61*time.Minute)) {
		t.Fatal("rotation cooldown must expire after the configured window")
	}
}

func TestRotation_ReleasesHalfWhenEveryoneElseIsCooling(t *testing.T) {
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	pool := []string{"acct-a", "acct-b", "acct-c", "acct-d", "acct-e"}

	// 让 b/c/d/e 先进入冷却。冷却窗口必须严格晚于后面的判定时刻，
	// 否则「冷却中」判定会因为 now == cooldownUntil 而失效。
	for _, id := range []string{"acct-b", "acct-c", "acct-d", "acct-e"} {
		rotationMu.Lock()
		st := rotationStateLocked(id, true)
		st.cooldownUntil = base.Add(2 * time.Hour)
		rotationMu.Unlock()
	}

	markAuthUsed("acct-a", base)
	// acct-a 满 1 小时：除它以外全池已冷却，必须先放开一半再冷却它，
	// 否则全池无可用号。
	if !evaluateRotation("acct-a", pool, base.Add(time.Hour)) {
		t.Fatal("account should still rotate after releasing peers")
	}

	rotationMu.Lock()
	stillCooling := 0
	for _, id := range pool {
		st := rotationStateLocked(id, false)
		if st != nil && base.Add(time.Hour).Before(st.cooldownUntil) {
			stillCooling++
		}
	}
	rotationMu.Unlock()

	// 原先 4 个冷却，放开一半（2 个），加上本次新冷却的 acct-a：3 个仍在冷却。
	if stillCooling != 3 {
		t.Fatalf("expected 3 accounts still cooling (2 released + 1 new), got %d", stillCooling)
	}
}

func TestRotation_DisabledConfigIsInert(t *testing.T) {
	withRotationConfig(t, false, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	pool := []string{"acct-a", "acct-b"}
	markAuthUsed("acct-a", base)

	if evaluateRotation("acct-a", pool, base.Add(3*time.Hour)) {
		t.Fatal("rotation must be inert when disabled")
	}
	if rotationCoolingDown("acct-a", base.Add(3*time.Hour)) {
		t.Fatal("rotation must not cool down accounts when disabled")
	}
}

func TestRotation_ZeroServeOrCooldownFallsBackToDisabled(t *testing.T) {
	// 显式填 0 表示关闭：loadedRotationConfig 必须把 enabled 降级为 false，
	// 否则面板写 0 会被误读成「立即轮换」。
	withRotationConfig(t, true, 0, time.Hour, 10*time.Minute)
	if loadedRotationConfig().enabled {
		t.Fatal("serve=0 must disable rotation")
	}

	withRotationConfig(t, true, time.Hour, 0, 10*time.Minute)
	if loadedRotationConfig().enabled {
		t.Fatal("cooldown=0 must disable rotation")
	}
}

func TestRotation_AuthIDKeyNormalization(t *testing.T) {
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	base := time.Now()
	pool := []string{"acct-a", "acct-b"}
	// 执行入口用文件名形态，调度器用归一化形态：两者必须命中同一份状态。
	markAuthUsed("workbuddy-acct-a.json", base)

	if !evaluateRotation("acct-a", pool, base.Add(60*time.Minute)) {
		t.Fatal("normalized and raw auth IDs must share one rotation state")
	}
}

func TestRotation_NoteUsageSkipsPoolLookupBeforeThreshold(t *testing.T) {
	// 全池视图要走 host.auth.list 跨进程 RPC：未达阈值时不得触发，
	// 否则每个成功请求都会多一次宿主调用。
	withRotationConfig(t, true, time.Hour, time.Hour, 10*time.Minute)

	origList := hostAuthListFn
	t.Cleanup(func() { hostAuthListFn = origList })
	lookups := 0
	hostAuthListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		lookups++
		return []pluginapi.HostAuthFileEntry{{ID: "acct-a"}, {ID: "acct-b"}}, nil
	}

	noteRotationUsage("acct-a")
	if lookups != 0 {
		t.Fatalf("pool lookup must not happen before serve threshold, got %d lookups", lookups)
	}
}

func TestRotationConfig_ParsesFromConfigYAML(t *testing.T) {
	// 面板写入的 4 个键必须真的被 configure 解析并写回运行时变量，
	// 否则轮换无法关闭也无法调整窗口。
	withRotationConfig(t, false, 30*time.Minute, 15*time.Minute, 5*time.Minute)

	configure(buildConfigYAML(
		"rotation_enabled: true",
		"rotation_serve_minutes: 45",
		"rotation_cooldown_minutes: 20",
		"rotation_idle_reset_minutes: 3",
	))

	cfg := loadedRotationConfig()
	if !cfg.enabled {
		t.Fatal("rotation_enabled: true must enable rotation")
	}
	if cfg.serve != 45*time.Minute || cfg.cooldown != 20*time.Minute || cfg.idle != 3*time.Minute {
		t.Fatalf("unexpected rotation config: serve=%v cooldown=%v idle=%v", cfg.serve, cfg.cooldown, cfg.idle)
	}

	// 显式关闭必须生效。
	configure(buildConfigYAML("rotation_enabled: false"))
	if loadedRotationConfig().enabled {
		t.Fatal("rotation_enabled: false must disable rotation")
	}
}

func TestRotation_EnabledByDefault(t *testing.T) {
	// 生产裁定默认开启：单号长时间承流会被上游限流，轮换必须开箱生效。
	// 这里守住默认常量本身，避免后续把默认值改回 false 而无人察觉。
	if !defaultRotationEnabled {
		t.Fatal("rotation must be enabled by default")
	}
	if defaultRotationServeMinutes != 60 || defaultRotationCooldownMinutes != 60 {
		t.Fatalf("unexpected default serve/cooldown: %d/%d", defaultRotationServeMinutes, defaultRotationCooldownMinutes)
	}
}

func TestRotationConfig_RegisteredAsConfigFields(t *testing.T) {
	// 配置项必须登记进 ConfigFields，否则管理端看不到，写了也不生效。
	names := map[string]bool{}
	for _, field := range wbRegistration().Metadata.ConfigFields {
		names[field.Name] = true
	}
	for _, want := range []string{
		"rotation_enabled",
		"rotation_serve_minutes",
		"rotation_cooldown_minutes",
		"rotation_idle_reset_minutes",
	} {
		if !names[want] {
			t.Fatalf("config field %q must be registered", want)
		}
	}
}
