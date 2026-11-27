//go:build !windows

// instance_pid_unix.go 提供 Unix 平台的进程存活判定，供孤儿实例标记接管使用。
package main

import (
	"errors"
	"syscall"
)

// processAlive 报告 pid 对应的进程是否仍然存在。
//
// 实例标记里的 pid 可能属于一个早已退出的短命进程，这种「孤儿标记」不代表
// 存在更新实例，长驻实例必须能识别并接管，否则常驻循环会被永久压死。
// [参数] pid：目标进程号。
// [返回] true 表示进程仍在运行，或存在但无权探测（EPERM）。
// 最近修改时间：2026-11-27；改动原因：新增，支撑孤儿实例标记接管。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM 说明进程确实存在、只是不属于当前用户，按存活处理。
	return errors.Is(err, syscall.EPERM)
}
