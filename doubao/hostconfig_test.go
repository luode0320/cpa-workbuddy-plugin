package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// installHostConfigDir 建一个临时目录并把兜底读的路径指向其中的 config.yaml。
// [参数] t: 测试上下文。
// [返回] 配置文件路径，供后续改写同一份文件。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，宿主配置文件夹具。
func installHostConfigDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), hostConfigFileName)
	prev := hostConfigPathOverride
	hostConfigPathOverride = path
	t.Cleanup(func() { hostConfigPathOverride = prev })
	return path
}

// writeHostConfigFile 把配置文本写到指定路径。
// [参数] t: 测试上下文；path: 目标路径；body: 完整配置文本。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，支持在同一路径上改写配置。
func writeHostConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if errWrite := os.WriteFile(path, []byte(body), 0o600); errWrite != nil {
		t.Fatalf("写宿主配置失败：%v", errWrite)
	}
}

// hostConfigWith 拼一份含本插件条目的宿主配置。
// [参数] pluginYAML: 缩进好的 config_yaml 内容，空串表示留空。
// [返回] 宿主配置文本。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，配置文本构造。
func hostConfigWith(pluginYAML string) string {
	if pluginYAML == "" {
		return "plugins:\n  configs:\n    doubao-provider:\n      config_yaml: \"\"\n      enabled: true\n"
	}
	return "plugins:\n  configs:\n    doubao-provider:\n      config_yaml: |\n" + pluginYAML + "      enabled: true\n"
}

// TestLoadKeepaliveSettingDefaults 校验 config_yaml 为空时回落到默认值。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，空配置口径与 configurePlugin 一致。
func TestLoadKeepaliveSettingDefaults(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith(""))

	setting, ok := loadKeepaliveSetting()
	if !ok {
		t.Fatal("配置存在时应读取成功")
	}
	if !setting.enabled {
		t.Error("config_yaml 为空表示全用默认值，保活应默认开启")
	}
	want := time.Duration(defaultKeepaliveIntervalSeconds) * time.Second
	if setting.interval != want {
		t.Errorf("默认间隔应为 %v，实际 %v", want, setting.interval)
	}
}

// TestLoadKeepaliveSettingFromYAML 校验能解析出配置里的两项。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，主解析路径。
func TestLoadKeepaliveSettingFromYAML(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith("        keepalive_enabled: true\n        keepalive_interval_seconds: 90\n"))

	setting, ok := loadKeepaliveSetting()
	if !ok {
		t.Fatal("合法配置应读取成功")
	}
	if !setting.enabled {
		t.Error("配置写了 keepalive_enabled: true")
	}
	if setting.interval != 90*time.Second {
		t.Errorf("间隔应为 90s，实际 %v", setting.interval)
	}
}

// TestLoadKeepaliveSettingExplicitOff 校验显式 false 能被读出来。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，指针语义在文件路径上同样成立。
func TestLoadKeepaliveSettingExplicitOff(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith("        keepalive_enabled: false\n"))

	setting, ok := loadKeepaliveSetting()
	if !ok {
		t.Fatal("合法配置应读取成功")
	}
	if setting.enabled {
		t.Error("配置里显式写了 false，应读出关闭")
	}
}

// TestLoadKeepaliveSettingMissingPlugin 校验本插件条目缺失时返回未读到。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，缺条目不能误判为关闭。
func TestLoadKeepaliveSettingMissingPlugin(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, "plugins:\n  configs:\n    other-provider:\n      enabled: true\n")

	if _, ok := loadKeepaliveSetting(); ok {
		t.Fatal("没有本插件条目时应返回未读到，由调用方沿用当前运行值")
	}
}

// TestLoadKeepaliveSettingBrokenFile 校验文件不存在或 YAML 非法时返回未读到。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，兜底读失败不得让保活停摆。
func TestLoadKeepaliveSettingBrokenFile(t *testing.T) {
	path := installHostConfigDir(t)
	if _, ok := loadKeepaliveSetting(); ok {
		t.Fatal("文件不存在时应返回未读到")
	}

	writeHostConfigFile(t, path, "plugins: [broken\n")
	if _, ok := loadKeepaliveSetting(); ok {
		t.Fatal("YAML 非法时应返回未读到")
	}
}

// TestDecideKeepaliveTick 校验三种决策分支。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，决策表驱动。
func TestDecideKeepaliveTick(t *testing.T) {
	cases := []struct {
		name     string
		plugin   string
		current  time.Duration
		wantNext time.Duration
		wantAct  keepaliveTickAction
	}{
		{name: "同间隔照常", plugin: "        keepalive_interval_seconds: 90\n", current: 90 * time.Second, wantNext: 90 * time.Second, wantAct: keepaliveProceed},
		{name: "换间隔重建", plugin: "        keepalive_interval_seconds: 30\n", current: 90 * time.Second, wantNext: 30 * time.Second, wantAct: keepaliveReschedule},
		{name: "显式关闭退出", plugin: "        keepalive_enabled: false\n", current: 90 * time.Second, wantNext: 90 * time.Second, wantAct: keepaliveStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := installHostConfigDir(t)
			writeHostConfigFile(t, path, hostConfigWith(tc.plugin))
			next, action := decideKeepaliveTick(tc.current)
			if action != tc.wantAct {
				t.Fatalf("动作应为 %v，实际 %v", tc.wantAct, action)
			}
			if next != tc.wantNext {
				t.Fatalf("间隔应为 %v，实际 %v", tc.wantNext, next)
			}
		})
	}
}

// TestRestartKeepalivePicksUpHostConfigInterval 校验协程巡检到新间隔后真的按新周期跑。
//
// 这条用例守的是「宿主 PUT /config 不回调 reconfigure」的兜底：初始间隔设成 1 小时
// （等价于永不 tick），宿主配置里写 1 秒，巡检周期压到 50ms；若 ticker 真的被重建，
// 2 秒内必然出现上游请求。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:25:00；改动原因：新增，兜底重建 ticker 的行为验证。
func TestRestartKeepalivePicksUpHostConfigInterval(t *testing.T) {
	sa := newKeepaliveProbeAccount(t)
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		t.Fatalf("预置账号失败：%v", errWrite)
	}

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set(msTokenHeader, "PickedUpIntervalTokenABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	restoreEndpoint := swapBusinessEndpoint(srv.URL)
	defer restoreEndpoint()

	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith("        keepalive_interval_seconds: 1\n"))

	prevPeriod := keepaliveConfigCheckPeriod
	keepaliveConfigCheckPeriod = 50 * time.Millisecond
	t.Cleanup(func() {
		keepaliveConfigCheckPeriod = prevPeriod
		stopKeepalive()
	})

	// 1. 初始间隔 1 小时，不巡检就不会有任何请求。
	restartKeepalive(Config{KeepaliveEnabled: true, KeepaliveIntervalSeconds: 3600})
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("重置前不该有请求，实际 %d 次", got)
	}

	// 2. 巡检把间隔换成 1 秒后，应开始按秒级跑。
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("巡检后 4 秒内仍无保活请求，实际 %d 次", atomic.LoadInt32(&hits))
}

// TestRestartKeepaliveStopsWhenHostConfigDisables 校验宿主把开关改成 false 后协程自行退出。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:25:00；改动原因：新增，关闭也要能靠巡检生效。
func TestRestartKeepaliveStopsWhenHostConfigDisables(t *testing.T) {
	path := installHostConfigDir(t)
	writeHostConfigFile(t, path, hostConfigWith("        keepalive_enabled: false\n"))

	prevPeriod := keepaliveConfigCheckPeriod
	keepaliveConfigCheckPeriod = 50 * time.Millisecond
	t.Cleanup(func() {
		keepaliveConfigCheckPeriod = prevPeriod
		stopKeepalive()
	})

	restartKeepalive(Config{KeepaliveEnabled: true, KeepaliveIntervalSeconds: 3600})

	keepaliveMu.Lock()
	done := keepaliveDone
	keepaliveMu.Unlock()
	if done == nil {
		t.Fatal("开启后应有运行中的定时器")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("宿主配置已关闭，协程应在巡检后退出")
	}
}

// TestDecideKeepaliveTickKeepsCurrentOnReadFailure 校验读不到配置时沿用当前间隔。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:20:00；改动原因：新增，读失败必须保活继续跑。
func TestDecideKeepaliveTickKeepsCurrentOnReadFailure(t *testing.T) {
	prev := hostConfigPathOverride
	hostConfigPathOverride = filepath.Join(t.TempDir(), "missing.yaml")
	t.Cleanup(func() { hostConfigPathOverride = prev })

	next, action := decideKeepaliveTick(42 * time.Second)
	if action != keepaliveProceed || next != 42*time.Second {
		t.Fatalf("读失败应沿用 42s 并照常跑，实际 %v / %v", next, action)
	}
}
