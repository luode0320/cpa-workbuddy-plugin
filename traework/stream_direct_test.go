// stream_direct_test.go 锁定「流式直连开关」的行为。
//
// 生产背景（2026-11-10）：生产流式请求默认走宿主 `host.stream.do` 桥，
// 插件侧的连接池配置（MaxIdleConnsPerHost 等）对它无效 —— 0.5.40 扩容后
// TTFT 未见改善即因于此。本开关让运维可以做「直连 vs 桥接」的 A/B 对照。
package main

import (
	"testing"
)

// TestStreamDirectForceToggle 验证开关的读写与默认值。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-10 21:35:00；改动原因：新增，支持路径对照实验。
func TestStreamDirectForceToggle(t *testing.T) {
	// 1. 默认必须是 false —— 走宿主桥与既有行为一致，不能因加开关改变生产路径。
	setStreamDirectForce(false)
	if streamDirectForced() {
		t.Fatal("默认必须为 false（走宿主桥），否则会改变生产流量路径")
	}

	// 2. 打开后生效。
	setStreamDirectForce(true)
	if !streamDirectForced() {
		t.Fatal("setStreamDirectForce(true) 后 streamDirectForced() 应为 true")
	}

	// 3. 关闭后恢复。
	setStreamDirectForce(false)
	if streamDirectForced() {
		t.Fatal("setStreamDirectForce(false) 后应恢复 false")
	}
}
