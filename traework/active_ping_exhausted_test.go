// active_ping_exhausted_test.go 锁定「积分耗尽账号不触发探活」。
//
// 生产缺陷回归（2026-11-09 23:20）：14 个已配额耗尽的账号在 2 分钟内被探活
// 各 2~3 次，全部返回 "Your requests have exceeded the quota."，纯属浪费上游调用。
package main

import (
	"testing"
)

// TestShouldActivePingForCredits 验证探活准入判据。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-10 00:25:00；改动原因：新增，配额耗尽不探活。
func TestShouldActivePingForCredits(t *testing.T) {
	cases := []struct {
		name string
		cr   *traeCredits
		want bool
	}{
		{name: "余额为 0 不探活", cr: &traeCredits{TotalRemain: 0}, want: false},
		{name: "余额为负不探活", cr: &traeCredits{TotalRemain: -5}, want: false},
		{name: "快照缺失不探活", cr: nil, want: false},
		{name: "余额 1 探活", cr: &traeCredits{TotalRemain: 1}, want: true},
		{name: "余额充足探活", cr: &traeCredits{TotalRemain: 500}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldActivePingForCredits(tc.cr); got != tc.want {
				t.Fatalf("shouldActivePingForCredits(%+v) = %v, want %v", tc.cr, got, tc.want)
			}
		})
	}
}
