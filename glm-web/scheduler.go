package main

import (
	"encoding/json"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	schedulerIndex int
	schedulerMu    sync.Mutex
)

// accountRoutable 判断账号是否具备承载流量资格。
// [参数] sa: 账号凭据。
// [返回] 是否可调度。
// 最近修改时间：2026-11-28 新增, 账号准入门槛。
func accountRoutable(sa *StoredAuth) bool {
	if sa == nil || sa.Disabled || sa.TestFailed {
		return false
	}
	accountFailures.Lock()
	count := accountFailures.counts[sa.AuthID]
	accountFailures.Unlock()
	return count < 3
}

// pickActiveAuth 轮询挑选一个可用账号。
// [参数] 无。
// [返回] 账号指针；无可用返回 nil。
// 最近修改时间：2026-11-28 新增, 账号调度。
func pickActiveAuth() *StoredAuth {
	accounts, err := listAllAuthFiles()
	if err != nil || len(accounts) == 0 {
		return nil
	}
	var candidates []*StoredAuth
	for _, acc := range accounts {
		if accountRoutable(acc) {
			candidates = append(candidates, acc)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	idx := schedulerIndex % len(candidates)
	schedulerIndex++
	return candidates[idx]
}

// handleSchedulerPick 响应宿主调度请求。
// [参数] raw: 调度请求载荷。
// [返回] 调度响应信封、错误。
// 最近修改时间：2026-11-28 新增, 调度入口。
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	sa := pickActiveAuth()
	if sa == nil {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	return okEnvelope(pluginapi.SchedulerPickResponse{Handled: true, AuthID: sa.AuthID})
}
