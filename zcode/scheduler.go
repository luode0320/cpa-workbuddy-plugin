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
// [参数] sa：账号凭据；model：已归一化的上游模型名，传空串时退化为纯账号维度判定。
// [返回] true 表示可参与调度。
// 最近修改时间：2026-10-19 02:00:00；改动原因：冷却判定带模型维度。
func accountRoutable(sa *StoredAuth, model string) bool {
	if sa == nil || sa.Disabled || sa.APIKey == "" {
		return false
	}
	if sa.TestFailed {
		return false
	}
	// 失败禁用是账号级的：宿主侧已禁用，插件这里只需判内存镜像与盘上标记。
	// 原「账号×模型冷却」判据已随冷却状态下线移除。
	if isFailoverDisabled(sa.AuthID) {
		return false
	}
	if q := cachedQuota(sa.AuthID); q != nil {
		if q.PlanStatus == "expired" || q.PlanStatus == "inactive" || (q.PlanID == "" && q.PlanName == "") {
			return false
		}
	}
	return true
}

// pickActiveAuth 从所有落盘账号中挑选一个可承载流量的可用账号。
// [参数] model：已归一化的上游模型名，可为空。
// [返回] 目标账号；无可选账号时返回 nil。
// 最近修改时间：2026-10-19 02:00:00；改动原因：冷却判定带模型维度。
func pickActiveAuth(model string) *StoredAuth {
	accounts, err := listAllAuthFiles()
	if err != nil || len(accounts) == 0 {
		return nil
	}

	var candidates []*StoredAuth
	for _, acc := range accounts {
		if accountRoutable(acc, model) {
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
// [参数] currentAuthID：当前失败账号标识；model：已归一化的上游模型名，可为空。
// [返回] 下一个候选账号与是否存在。
// 最近修改时间：2026-10-19 02:00:00；改动原因：冷却判定带模型维度。
func pickNextAuth(currentAuthID, model string) (*StoredAuth, bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	accounts, err := listAllAuthFiles()
	if err != nil || len(accounts) <= 1 {
		return nil, false
	}

	var candidates []*StoredAuth
	for _, acc := range accounts {
		if acc.AuthID != currentAuthID && accountRoutable(acc, model) {
			candidates = append(candidates, acc)
		}
	}

	if len(candidates) == 0 {
		return nil, false
	}

	return candidates[0], true
}

// handleSchedulerPick 响应宿主的 MethodSchedulerPick RPC 请求。
// [参数] raw：宿主调度请求载荷。
// [返回] 调度响应信封与错误。
// 最近修改时间：2026-10-19 02:00:00；改动原因：冷却判定带模型维度。
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	// 冷却判定带模型维度：账号×模型级冷却只应挡住同一模型的请求。
	sa := pickActiveAuth(mappedModel(req.Model, currentConfig()))
	if sa == nil {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	return okEnvelope(pluginapi.SchedulerPickResponse{
		Handled: true,
		AuthID:  sa.AuthID,
	})
}
