package main

import (
	"context"
	"runtime/debug"
	"testing"
	"time"
)

// TestPluginMemoryLimitBytes 校验默认上限、环境变量覆盖与非法值回落。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，锁定内存自限口径。
func TestPluginMemoryLimitBytes(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int64
	}{
		{"未设置回落默认", "", int64(defaultPluginMemoryLimitMB) << 20},
		{"正常覆盖", "64", int64(64) << 20},
		{"非法值回落默认", "abc", int64(defaultPluginMemoryLimitMB) << 20},
		{"零值回落默认", "0", int64(defaultPluginMemoryLimitMB) << 20},
	}
	for _, tc := range cases {
		t.Setenv(pluginMemoryLimitEnv, tc.env)
		if got := pluginMemoryLimitBytes(); got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestInitAppliedMemoryLimit 校验 init 已把上限真正写进本插件 runtime。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，确认自限不是只算不用。
func TestInitAppliedMemoryLimit(t *testing.T) {
	want := pluginMemoryLimitBytes()
	if got := debug.SetMemoryLimit(-1); got != want {
		t.Fatalf("runtime 内存软上限 = %d, 期望 %d", got, want)
	}
}

// TestGoAfterStartupJitterRuns 校验错峰助手在延迟后确实执行目标函数。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，防止错峰实现把循环永久挂住。
func TestGoAfterStartupJitterRuns(t *testing.T) {
	old := startupJitterMax
	startupJitterMax = time.Millisecond
	defer func() { startupJitterMax = old }()

	done := make(chan struct{})
	goAfterStartupJitter(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("错峰启动未在预期时间内执行")
	}
}

// TestWaitStartupJitterHonoursCancel 校验取消时错峰等待立即返回 false。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，保证热更新停止循环不被错峰睡眠拖住。
func TestWaitStartupJitterHonoursCancel(t *testing.T) {
	old := startupJitterMax
	startupJitterMax = time.Hour
	defer func() { startupJitterMax = old }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if waitStartupJitter(ctx) {
		t.Fatal("已取消的上下文应返回 false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("取消时应立即返回，实际耗时 %s", elapsed)
	}
}
