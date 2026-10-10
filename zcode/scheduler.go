package main

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	schedulerIndex int
	schedulerMu    sync.Mutex
)

// accountRoutable 判断一个账号是否具备承载流量的资格。
func accountRoutable(sa *StoredAuth) bool {
	if sa == nil || sa.Disabled || sa.APIKey == "" {
		return false
	}
	if sa.TestFailed {
		return false
	}
	if isAccountCoolingDown(sa.AuthID) {
		return false
	}
	return true
}

// pickActiveAuth 从所有落盘账号中挑选一个可承载流量的可用账号。
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

// pickNextAuth 在当前账号发生故障时，选取下一个可用候选账号进行故障换号重试。
func pickNextAuth(currentAuthID string) (*StoredAuth, bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	accounts, err := listAllAuthFiles()
	if err != nil || len(accounts) <= 1 {
		return nil, false
	}

	var candidates []*StoredAuth
	for _, acc := range accounts {
		if acc.AuthID != currentAuthID && accountRoutable(acc) {
			candidates = append(candidates, acc)
		}
	}

	if len(candidates) == 0 {
		return nil, false
	}

	return candidates[0], true
}

// handleSchedulerPick 响应宿主的 MethodSchedulerPick RPC 请求。
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	sa := pickActiveAuth()
	if sa == nil {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	return okEnvelope(pluginapi.SchedulerPickResponse{
		Handled: true,
		AuthID:  sa.AuthID,
	})
}
