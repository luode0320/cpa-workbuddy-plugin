package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// assistantBrief 是上游智能体列表的一项。
type assistantBrief struct {
	AssistantID string `json:"assistant_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ChatMode    string `json:"chat_mode"`
}

// assistantListEnvelope 是智能体列表端点的响应信封。
type assistantListEnvelope struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Result  struct {
		AssistantList []assistantBrief `json:"assistant_list"`
		HasMore       bool             `json:"has_more"`
	} `json:"result"`
}

// listUpstreamAssistants 拉取当前账号的自建智能体列表。
// 上游要求 page 与 page_size 必填，缺省会返回 400 校验错误。
// [参数] accessToken: 调用令牌；cfg: 插件配置。
// [返回] 智能体列表、错误。
// 最近修改时间：2026-11-29 新增, 动态模型目录来源。
func listUpstreamAssistants(accessToken string, cfg Config) ([]assistantBrief, error) {
	deviceID := deviceIDFromToken(accessToken)
	if deviceID == "" {
		return nil, fmt.Errorf("access_token 缺少 device_id，请重新授权")
	}
	body, err := json.Marshal(map[string]any{"page": 1, "page_size": 50})
	if err != nil {
		return nil, fmt.Errorf("marshal assistant list payload: %w", err)
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	req, err := http.NewRequest(http.MethodPost, cfg.AssistantListEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build assistant list request: %w", err)
	}
	h := refreshHeaders("", sign, deviceID)
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Accept", "application/json")
	req.Header = h

	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("assistant list request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read assistant list response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("assistant list status %d: %s", resp.StatusCode, string(raw))
	}
	var envelope assistantListEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode assistant list response: %w", err)
	}
	return envelope.Result.AssistantList, nil
}

// dynamicModelInfos 返回预置档位目录 + 账号自建智能体。
// 拉取失败时降级为纯预置目录，并把原因作为 warning 一并返回。
// [参数] accessToken: 调用令牌，为空表示不回源；cfg: 插件配置。
// [返回] 模型信息列表、告警列表。
// 最近修改时间：2026-11-29 新增, 面板模型目录动态化。
func dynamicModelInfos(accessToken string, cfg Config) ([]pluginapi.ModelInfo, []string) {
	infos := allModelInfos()
	if strings.TrimSpace(accessToken) == "" {
		return infos, nil
	}
	briefs, err := listUpstreamAssistants(accessToken, cfg)
	if err != nil {
		return infos, []string{err.Error()}
	}
	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		seen[info.ID] = struct{}{}
	}
	for _, brief := range briefs {
		id := strings.TrimSpace(brief.AssistantID)
		if id == "" {
			continue
		}
		modelID := "glm/" + id
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		name := strings.TrimSpace(brief.Name)
		if name == "" {
			name = id
		}
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         modelID,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                name + "（自建智能体）",
			Description:                strings.TrimSpace(brief.Description),
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return infos, nil
}

// firstUsableAuth 返回第一个未停用的账号，供模型目录回源使用。
// [参数] 无。
// [返回] 账号指针；没有可用账号时返回 nil。
// 最近修改时间：2026-11-29 新增, 模型目录缺省账号选择。
func firstUsableAuth() *StoredAuth {
	auths, err := listAllAuthFiles()
	if err != nil {
		return nil
	}
	for _, sa := range auths {
		if sa != nil && !sa.Disabled {
			return sa
		}
	}
	return nil
}

// handleMgmtModels 返回模型目录：预置档位 + 指定（或首个可用）账号的自建智能体。
// [参数] query: 管理请求查询参数，auth_id 可选。
// [返回] 管理响应信封字节、错误。
// 最近修改时间：2026-11-29 新增, 模型目录改为接口动态获取。
func handleMgmtModels(query url.Values) ([]byte, error) {
	cfg := currentConfig()
	authID := strings.TrimSpace(query.Get("auth_id"))
	var sa *StoredAuth
	if authID != "" {
		sa = findAuthByID(authID)
	} else {
		sa = firstUsableAuth()
	}
	accessToken := ""
	if sa != nil {
		if token, err := acquireToken(sa.RefreshToken, cfg); err == nil {
			accessToken = token
		}
	}
	infos, warnings := dynamicModelInfos(accessToken, cfg)
	out := map[string]any{"models": infos}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return jsonResponse(out)
}
