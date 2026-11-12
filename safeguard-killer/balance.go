package main

import (
	"sort"
	"sync"
	"time"
)

const (
	// 宿主通过执行器选项元数据透传的会话标识键名。
	metadataCanonicalSessionID = "canonical_session_id"
	metadataDerivedSessionID   = "derived_session_id"

	maxDecisions     = 200
	sessionRetention = 24 * time.Hour
)

var (
	// hostCall 是宿主回调入口；单元测试没有宿主，会替换它。破甲流式路径与分组执行链共用同一个变量。
	hostCall = callHost

	stateMu   sync.Mutex
	decisions []decision
)

// sessionKey 取宿主为该请求推导出的会话标识。
// [参数] metadata：执行器选项里宿主透传的元数据
// [返回] 规范会话 ID、带 derived: 前缀的派生 ID，或空串
// 最近修改时间 2026-11-13 00:35:00, 修改, 精简说明，删除已废弃的调度语义
func sessionKey(metadata map[string]any) string {
	if canonical, ok := metadata[metadataCanonicalSessionID].(string); ok && canonical != "" {
		return canonical
	}
	if derived, ok := metadata[metadataDerivedSessionID].(string); ok && derived != "" {
		return "derived:" + derived
	}
	return ""
}

// pruneSessions 清理空闲超过 sessionRetention 的分组会话粘性绑定。
// [参数] now：当前时间
// 最近修改时间 2026-11-13 00:35:00, 修改, 只保留分组会话清理
func pruneSessions(now time.Time) {
	for key, binding := range groupSessions {
		if now.Sub(binding.LastSeen) > sessionRetention {
			delete(groupSessions, key)
		}
	}
}

// recordDecision 向环形缓冲追加一条决策记录；调用方需已持有 stateMu。
// [参数] entry：待记录的决策
// 最近修改时间 2026-11-13 00:35:00, 修改, 删除凭据调度决策后只保留分组决策
func recordDecision(entry decision) {
	decisions = append(decisions, entry)
	if len(decisions) > maxDecisions {
		decisions = decisions[len(decisions)-maxDecisions:]
	}
}

// buildUIState 组装管理面板轮询的状态 JSON。
// [返回] 含版本、配置、分组模型集合、分组会话绑定与最近决策的面板状态
// 最近修改时间 2026-11-13 00:35:00, 修改, 模型集合改由配置里的分组推导，删除凭据与配额字段
func buildUIState() uiState {
	now := time.Now()
	cfg := currentConfig()
	state := uiState{Version: pluginVersion, Now: now, Config: cfg}
	models := make(map[string]bool)
	for _, group := range cfg.Groups {
		models[group.ID] = true
		for _, model := range group.Models {
			models[model] = true
		}
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	for model := range models {
		state.Models = append(state.Models, model)
	}
	sort.Strings(state.Models)
	for _, binding := range groupSessions {
		state.Sessions = append(state.Sessions, *binding)
	}
	sort.Slice(state.Sessions, func(i, j int) bool { return state.Sessions[i].LastSeen.After(state.Sessions[j].LastSeen) })
	for index := len(decisions) - 1; index >= 0; index-- {
		state.Decisions = append(state.Decisions, decisions[index])
	}
	return state
}
