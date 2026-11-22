package main

import (
	"testing"
	"time"
)

// TestLoadThrottleSettingFromYAML 校验能从宿主配置里读出闸门三项。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定闸门配置解析。
func TestLoadThrottleSettingFromYAML(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith(
		"        account_max_inflight: 2\n"+
			"        account_min_interval_ms: 1500\n"+
			"        rate_limit_backoff_seconds: 45\n"))

	setting, ok := loadThrottleSetting()
	if !ok {
		t.Fatal("配置存在时应读取成功")
	}
	if setting.maxInflight != 2 {
		t.Errorf("account_max_inflight 应为 2，实际 %d", setting.maxInflight)
	}
	if setting.minInterval != 1500*time.Millisecond {
		t.Errorf("account_min_interval_ms 应为 1500ms，实际 %v", setting.minInterval)
	}
	if setting.backoff != 45*time.Second {
		t.Errorf("rate_limit_backoff_seconds 应为 45s，实际 %v", setting.backoff)
	}
}

// TestLoadThrottleSettingEmptyYAML 校验 config_yaml 为空时返回未读到。
//
// 返回 false 让调用方沿用运行期值与默认值，而不是把三项都读成零值——
// 零值 maxInflight 会让请求永久排队。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定空配置口径。
func TestLoadThrottleSettingEmptyYAML(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith(""))

	if _, ok := loadThrottleSetting(); ok {
		t.Fatal("config_yaml 为空时不应报告读到设置")
	}
}

// TestLoadThrottleSettingMissingFile 校验配置文件不存在时返回未读到。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定读失败路径。
func TestLoadThrottleSettingMissingFile(t *testing.T) {
	installHostConfigDir(t)

	if _, ok := loadThrottleSetting(); ok {
		t.Fatal("配置文件不存在时不应报告读到设置")
	}
}

// TestEffectiveThrottleFallsBackToRuntimeConfig 校验读不到宿主配置时用运行期配置。
//
// 这条路径是宿主配置缺失时的唯一兜底，读挂就会退化成零节流。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定运行期兜底。
func TestEffectiveThrottleFallsBackToRuntimeConfig(t *testing.T) {
	installHostConfigDir(t)
	restore := swapThrottleSetting(throttleSetting{})
	defer restore()
	// 覆盖路径会走 normalize，这里要测运行期兜底，先清掉覆盖值。
	throttleSettingMu.Lock()
	throttleSettingOverride = nil
	throttleSettingExpiry = time.Time{}
	throttleSettingMu.Unlock()
	t.Cleanup(resetThrottleSettingCache)

	savedCfg := currentConfig()
	t.Cleanup(func() { activeConfig.Store(savedCfg) })
	activeConfig.Store(Config{
		AccountMaxInflight:      3,
		AccountMinIntervalMs:    700,
		RateLimitBackoffSeconds: 20,
	})
	resetThrottleSettingCache()

	setting := effectiveThrottleSetting()
	if setting.maxInflight != 3 {
		t.Errorf("应读运行期配置 3，实际 %d", setting.maxInflight)
	}
	if setting.minInterval != 700*time.Millisecond {
		t.Errorf("应读运行期配置 700ms，实际 %v", setting.minInterval)
	}
	if setting.backoff != 20*time.Second {
		t.Errorf("应读运行期配置 20s，实际 %v", setting.backoff)
	}
}

// TestEffectiveThrottlePrefersHostConfig 校验宿主配置优先于运行期配置。
//
// 宿主不回调 reconfigure，改配置只能靠读文件；这条优先级反了就等于兜底失效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，锁定优先级。
func TestEffectiveThrottlePrefersHostConfig(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith(
		"        account_max_inflight: 5\n"+
			"        account_min_interval_ms: 100\n"+
			"        rate_limit_backoff_seconds: 10\n"))
	t.Cleanup(resetThrottleSettingCache)

	savedCfg := currentConfig()
	t.Cleanup(func() { activeConfig.Store(savedCfg) })
	activeConfig.Store(Config{
		AccountMaxInflight:      1,
		AccountMinIntervalMs:    2000,
		RateLimitBackoffSeconds: 60,
	})
	resetThrottleSettingCache()

	setting := effectiveThrottleSetting()
	if setting.maxInflight != 5 {
		t.Errorf("宿主配置应优先，期望 5，实际 %d", setting.maxInflight)
	}
	if setting.minInterval != 100*time.Millisecond {
		t.Errorf("宿主配置应优先，期望 100ms，实际 %v", setting.minInterval)
	}
	if setting.backoff != 10*time.Second {
		t.Errorf("宿主配置应优先，期望 10s，实际 %v", setting.backoff)
	}
}
