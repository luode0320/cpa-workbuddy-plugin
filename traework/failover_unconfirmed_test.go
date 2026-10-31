package main

import "testing"

// TestFailoverUnconfirmed 锁定「盘上说禁用、宿主仍路由」的判据。
//
// 这是面板文案分叉的唯一来源：命中时显示「写盘未生效·仍在接流量」，
// 否则显示「探活中」。判据写错会让用户把仍在吃流量的号当成已下线。
// 最近修改时间：2026-10-31；改动原因：新增，锁定写盘与宿主状态的分叉判据。
func TestFailoverUnconfirmed(t *testing.T) {
	cases := []struct {
		name         string
		diskDisabled bool
		hostDisabled bool
		want         bool
	}{
		{name: "写盘成功且宿主已吸收", diskDisabled: true, hostDisabled: true, want: false},
		{name: "写盘成功但宿主未吸收", diskDisabled: true, hostDisabled: false, want: true},
		{name: "盘上未禁用时无分叉可言", diskDisabled: false, hostDisabled: false, want: false},
		{name: "盘上未禁用但宿主已禁用（他人停用）", diskDisabled: false, hostDisabled: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failoverUnconfirmed(tc.diskDisabled, tc.hostDisabled); got != tc.want {
				t.Fatalf("failoverUnconfirmed(%v, %v) = %v, want %v",
					tc.diskDisabled, tc.hostDisabled, got, tc.want)
			}
		})
	}
}
