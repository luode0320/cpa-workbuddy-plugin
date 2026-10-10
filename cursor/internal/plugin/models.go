package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

// defaultCursorModels 是 Cursor 官方主流支持的常用模型列表。
// 当网络抖动或上游 GetUsableModels 暂时失败时作为稳定兜底，保障账号高可用。
var defaultCursorModels = []string{
	"auto",
	"cursor-fast",
	"composer-2.5",
	"claude-3.5-sonnet",
	"claude-3-5-sonnet",
	"gpt-4o",
	"gpt-4o-mini",
	"gpt-5.3-codex",
	"deepseek-v3",
}

type authModelRequest struct {
	StorageJSON []byte `json:"StorageJSON"`
}

type modelContextProvider interface {
	ModelContextLengths(context.Context, string) (map[string]int64, error)
}

// modelsForAuth 获取指定认证凭据可用的模型列表。
// [参数] ctx: 上下文；raw: 包含 StorageJSON 的模型请求字节切片。
// [返回] 过滤禁用模型后的模型列表响应；上游失败时回退到默认常用模型，保障服务可用。
// 最近修改时间 2026-10-11（增加上游模型拉取失败时的默认常用模型优雅降级）
func (handler *Handler) modelsForAuth(ctx context.Context, raw []byte) (any, error) {
	var request authModelRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode auth model request: %w", err)
	}
	credentials, err := cursorauth.ParseCredentials(request.StorageJSON)
	if err != nil {
		return nil, err
	}
	// 1. 尝试从 Cursor 上游动态拉取可用模型
	var models []string
	if handler.cursor != nil {
		models, _ = handler.cursor.DiscoverModels(ctx, credentials.AccessToken)
	}
	// 2. 上游失败或返回空时，优雅降级到默认常用模型
	if len(models) == 0 {
		models = append([]string(nil), defaultCursorModels...)
	}
	var contexts map[string]int64
	if provider, ok := handler.cursor.(modelContextProvider); ok {
		contexts, err = provider.ModelContextLengths(ctx, credentials.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			contexts = nil
		}
	}
	return modelResponse(filterDisabledModels(models, credentials.DisabledModels), contexts), nil
}

func filterDisabledModels(models, disabled []string) []string {
	if len(disabled) == 0 {
		return append([]string(nil), models...)
	}
	blocked := make(map[string]struct{}, len(disabled))
	for _, id := range disabled {
		normalized := strings.TrimPrefix(strings.TrimSpace(id), "cursor/")
		if normalized != "" {
			blocked[normalized] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(models))
	for _, id := range models {
		normalized := strings.TrimPrefix(strings.TrimSpace(id), "cursor/")
		if _, found := blocked[normalized]; !found && normalized != "" {
			filtered = append(filtered, normalized)
		}
	}
	return filtered
}

func modelResponse(ids []string, contexts map[string]int64) any {
	models := make([]modelInfo, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		models = append(models, modelInfo{
			ID:                         "cursor/" + id,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                "Cursor " + id,
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text", "image"},
			ContextLength:              effectiveContextLength(contexts[id]),
			NativeContextLength:        contexts[id],
			ClientContextLimit:         clientContextLimit,
			UserDefined:                true,
		})
	}
	return struct {
		Provider string      `json:"Provider"`
		Models   []modelInfo `json:"Models"`
	}{Provider: providerName, Models: models}
}
