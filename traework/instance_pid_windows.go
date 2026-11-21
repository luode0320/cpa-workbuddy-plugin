//go:build windows

// instance_pid_windows.go 提供 Windows 平台的进程存活判定。
package main

import "os"

// processAlive 报告 pid 对应的进程是否仍然存在。
//
// Windows 不是插件生产平台（宿主为 linux/amd64 / darwin / windows 桌面），
// 这里用 os.FindProcess 完成探测：Windows 实现会真正 OpenProcess，pid 不存在
// 时返回 error，因此本地开发机上也能覆盖孤儿标记接管逻辑。
// [参数] pid：目标进程号。
// [返回] true 表示进程仍然存在。
// 最近修改时间：2026-11-21 13:45:00；改动原因：新增，支撑孤儿实例标记接管。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}
