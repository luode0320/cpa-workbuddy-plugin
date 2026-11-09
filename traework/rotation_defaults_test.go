// rotation_defaults_test.go 锁定轮换默认参数的具体取值。
//
// 用户反馈（2026-11-10）：「轮询好像没有生效，一直使用的是一个号」。
// 生产实证（24h）：serve=60min 配置下全池只让位 4 次，最长连续段 351 个请求 /
// 29 分钟全压在一个号上，主号占比 21%~35%（理想 1/13≈8%）。
// 本测试锁死调整后的具体值，防止被无意改回。
package main

import (
	"testing"
	"time"
)

// TestRotationDefaultValues 锁定轮换三个默认值的具体取值与相互关系。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-10 00:35:00；改动原因：新增，锁死轮询参数。
func TestRotationDefaultValues(t *testing.T) {
	// 1. serve 必须足够短，否则单号会长时间粘住（60 分钟实测只让位 4 次/天）。
	if defaultRotationServeMinutes > 15 {
		t.Fatalf("serve=%d 分钟过长，单号会长时间粘住，负载无法摊开", defaultRotationServeMinutes)
	}
	if defaultRotationServeMinutes <= 0 {
		t.Fatalf("serve=%d 必须为正", defaultRotationServeMinutes)
	}

	// 2. 冷却必须与 serve 同量级，否则一轮后大量账号同时冷却、可用池骤缩。
	if defaultRotationCooldownMinutes > defaultRotationServeMinutes*2 {
		t.Fatalf("cooldown=%d 远大于 serve=%d，会让可用池骤缩",
			defaultRotationCooldownMinutes, defaultRotationServeMinutes)
	}

	// 3. idle 必须显著小于 serve，否则「中断后重新计时」与「已服务满」判据冲突。
	if defaultRotationIdleResetMinutes >= defaultRotationServeMinutes {
		t.Fatalf("idle=%d 必须小于 serve=%d，否则中断重算后会立刻又被让位",
			defaultRotationIdleResetMinutes, defaultRotationServeMinutes)
	}

	// 4. 锁定调整后的目标值，防止无意回退。
	if defaultRotationServeMinutes != 5 {
		t.Fatalf("serve 应为 5 分钟（宿主自然切换周期 6~7 分钟，须早于它），got %d", defaultRotationServeMinutes)
	}
	if defaultRotationCooldownMinutes != 10 {
		t.Fatalf("cooldown 应为 10 分钟，got %d", defaultRotationCooldownMinutes)
	}
	if defaultRotationIdleResetMinutes != 3 {
		t.Fatalf("idle 应为 3 分钟，got %d", defaultRotationIdleResetMinutes)
	}

	// 5. 三个值必须都是合法的正时长。
	if time.Duration(defaultRotationServeMinutes)*time.Minute <= 0 {
		t.Fatal("serve 时长必须为正")
	}
}
