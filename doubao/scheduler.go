package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件负责账号选择与宿主 scheduler.pick 接线。
//
// 选择口径与冷却判定分离：冷却状态由 failover.go 维护，本文件只消费
// isAccountCoolingDown。宿主下发的候选集是唯一取号来源——插件不能返回
// 候选集之外的 AuthID，否则宿主会以 unknown auth id 拒绝整次请求。

var (
	// schedulerIndex 是候选集内的轮转游标，让流量在健康账号间平摊。
	schedulerIndex int
	// schedulerMu 保护 schedulerIndex。
	schedulerMu sync.Mutex
)

// errNoAvailableAccount 表示池内没有可用账号。
var errNoAvailableAccount = errors.New("没有可用的豆包账号：请先在插件面板导入登录态")

// pickAccount 从本地账号池中挑一个可用账号。
//
// 跳过已停用、跳过冷却中；其余按列表顺序取第一个。exclude 用于同请求换号，
// 避免把请求又打回刚刚失败的那个账号。
// [参数] exclude: 需要排除的账号标识（可为空）。
// [返回] 选中的账号与错误。
// 最近修改时间：2026-10-17；改动原因：新增，本地池选号（TASK-008）。
func pickAccount(exclude string) (*StoredAuth, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, errList
	}

	exclude = strings.TrimSpace(exclude)
	for _, account := range accounts {
		if account.Disabled || account.AuthID == exclude || isAccountCoolingDown(account.AuthID) {
			continue
		}
		return account, nil
	}
	return nil, errNoAvailableAccount
}

// candidateDisabled 判断宿主下发的候选是否已被标记为禁用。
// [参数] c: 宿主调度候选。
// [返回] true 表示该候选禁用、不应承载流量。
// 最近修改时间：2026-10-17；改动原因：新增，对齐宿主候选禁用语义。
func candidateDisabled(c pluginapi.SchedulerAuthCandidate) bool {
	if strings.EqualFold(strings.TrimSpace(c.Status), "disabled") {
		return true
	}
	if c.Metadata != nil {
		if v, ok := c.Metadata["disabled"]; ok {
			switch t := v.(type) {
			case bool:
				return t
			case string:
				return strings.EqualFold(strings.TrimSpace(t), "true")
			}
		}
	}
	return false
}

// handleSchedulerPick 响应宿主的 scheduler.pick 请求。
//
// 只在宿主下发的候选集内取号并原样返回候选 ID：候选集之外的账号宿主不认识，
// 返回它会被判为 unknown auth id。非本 provider 候选、已禁用候选、冷却中候选
// 一律剔除；剔除后没有候选就返回 Handled:false，让宿主内建调度器把请求
// 改派给其它 provider，而不是把请求钉死在本 provider 的死账号上。
// [参数] raw: 宿主调度请求载荷。
// [返回] 调度响应信封与错误。
// 最近修改时间：2026-10-17；改动原因：新增，TASK-008 多账号路由与全灭 defer。
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	var candidates []pluginapi.SchedulerAuthCandidate
	for _, c := range req.Candidates {
		if c.Provider != providerName {
			continue
		}
		if candidateDisabled(c) {
			continue
		}
		if isAccountCoolingDown(strings.TrimSuffix(c.ID, ".json")) {
			continue
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	schedulerMu.Lock()
	idx := schedulerIndex % len(candidates)
	schedulerIndex++
	schedulerMu.Unlock()

	return okEnvelope(pluginapi.SchedulerPickResponse{
		Handled: true,
		AuthID:  candidates[idx].ID,
	})
}
