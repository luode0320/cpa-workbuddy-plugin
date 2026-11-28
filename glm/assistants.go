package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// knownChatModes 是上游 chat_mode 的内置基线。
// 真实取值来自会话历史实测：""（普通）、zero（思考）、chat_agent（智能体）、deep_research（深度研究）。
// 该列表只是基线，运行时以历史会话发现结果为准并集。
var knownChatModes = []string{"", "zero", "chat_agent", "deep_research"}

// chatModeLabel 把 chat_mode 映射成面板展示名。
var chatModeLabel = map[string]string{
	"":              "GLM 标准",
	"zero":          "GLM 思考",
	"chat_agent":    "GLM 智能体",
	"deep_research": "GLM 深度研究",
}

// modeCacheEntry 缓存一次历史会话档位发现结果。
type modeCacheEntry struct {
	// Modes 是发现到的 chat_mode 去重列表。
	Modes []string
	// Expires 是缓存过期时刻。
	Expires time.Time
}

var modeDiscoveryCache = struct {
	mu      sync.Mutex
	entries map[string]*modeCacheEntry
}{entries: make(map[string]*modeCacheEntry)}

// modeCacheTTL 是档位发现的缓存时长。
const modeCacheTTL = 10 * time.Minute

// conversationListEnvelope 是会话列表端点的响应信封。
type conversationListEnvelope struct {
	Status int `json:"status"`
	Result struct {
		ConversationList []struct {
			ID       string `json:"id"`
			MetaData struct {
				ChatMode    string `json:"chat_mode"`
				SubscribeID string `json:"subscribe_id"`
			} `json:"meta_data"`
		} `json:"conversation_list"`
		HasMore bool `json:"has_more"`
	} `json:"result"`
}

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
	body, err := json.Marshal(map[string]any{"page": 1, "page_size": 50})
	if err != nil {
		return nil, fmt.Errorf("marshal assistant list payload: %w", err)
	}
	raw, err := callJSONAPI(http.MethodPost, cfg.AssistantListEndpoint, body, accessToken, cfg)
	if err != nil {
		return nil, err
	}
	var envelope assistantListEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode assistant list response: %w", err)
	}
	return envelope.Result.AssistantList, nil
}

// callJSONAPI 用带签名与设备头的伪装请求访问上游 JSON 端点。
// [参数] method: HTTP 方法；endpoint: 完整地址；body: 请求体，GET 传 nil；
//
//	accessToken: 调用令牌；cfg: 插件配置。
//
// [返回] 响应体、错误。
// 最近修改时间：2026-11-29 新增, 统一上游 JSON 调用的头构造与状态判定。
func callJSONAPI(method, endpoint string, body []byte, accessToken string, cfg Config) ([]byte, error) {
	deviceID := deviceIDFromToken(accessToken)
	if deviceID == "" {
		return nil, fmt.Errorf("access_token 缺少 device_id，请重新授权")
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	h := refreshHeaders("", sign, deviceID)
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Accept", "application/json")
	req.Header = h

	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

// discoverChatModes 从账号的历史会话中发现上游实际用过的 chat_mode 取值。
// 这是模型目录动态化的核心：网页端一旦启用新档位，历史里就会出现该取值，
// 插件下次刷新目录即可自动获得，无需改代码。
// [参数] accessToken: 调用令牌；cfg: 插件配置。
// [返回] 发现的档位列表、错误。
// 最近修改时间：2026-11-29 新增, 替代写死的档位清单。
func discoverChatModes(accessToken string, cfg Config) ([]string, error) {
	cacheKey := deviceIDFromToken(accessToken)
	modeDiscoveryCache.mu.Lock()
	if entry, ok := modeDiscoveryCache.entries[cacheKey]; ok && time.Now().Before(entry.Expires) {
		modes := append([]string(nil), entry.Modes...)
		modeDiscoveryCache.mu.Unlock()
		return modes, nil
	}
	modeDiscoveryCache.mu.Unlock()

	seen := make(map[string]struct{})
	modes := make([]string, 0, 4)
	for _, assistantID := range discoveryAssistantIDs(accessToken, cfg) {
		for page := 1; page <= 3; page++ {
			endpoint := fmt.Sprintf("%s?assistant_id=%s&page=%d&page_size=50",
				cfg.ConversationListEndpoint, url.QueryEscape(assistantID), page)
			raw, err := callJSONAPI(http.MethodGet, endpoint, nil, accessToken, cfg)
			if err != nil {
				if page == 1 {
					break
				}
				continue
			}
			var envelope conversationListEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				break
			}
			items := envelope.Result.ConversationList
			for _, item := range items {
				mode := strings.TrimSpace(item.MetaData.ChatMode)
				if _, ok := seen[mode]; ok {
					continue
				}
				seen[mode] = struct{}{}
				modes = append(modes, mode)
			}
			if len(items) == 0 || !envelope.Result.HasMore {
				break
			}
		}
	}

	modeDiscoveryCache.mu.Lock()
	modeDiscoveryCache.entries[cacheKey] = &modeCacheEntry{
		Modes:   append([]string(nil), modes...),
		Expires: time.Now().Add(modeCacheTTL),
	}
	modeDiscoveryCache.mu.Unlock()
	return modes, nil
}

// discoveryAssistantIDs 返回用于档位发现扫描的智能体 ID 列表：默认智能体 + 自建智能体。
// [参数] accessToken: 调用令牌；cfg: 插件配置。
// [返回] 去重后的智能体 ID 列表。
// 最近修改时间：2026-11-29 新增, 档位发现需要覆盖自建智能体。
func discoveryAssistantIDs(accessToken string, cfg Config) []string {
	ids := []string{defaultAssistantID}
	seen := map[string]struct{}{defaultAssistantID: {}}
	if briefs, err := listUpstreamAssistants(accessToken, cfg); err == nil {
		for _, brief := range briefs {
			id := strings.TrimSpace(brief.AssistantID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// effectiveChatModes 返回内置基线与历史发现的并集，保持稳定顺序。
// [参数] discovered: 历史发现的档位。
// [返回] 合并后的档位列表。
// 最近修改时间：2026-11-29 新增, 目录基线 + 动态发现合并。
func effectiveChatModes(discovered []string) []string {
	seen := make(map[string]struct{}, len(knownChatModes)+len(discovered))
	out := make([]string, 0, len(knownChatModes)+len(discovered))
	for _, mode := range knownChatModes {
		seen[mode] = struct{}{}
		out = append(out, mode)
	}
	for _, mode := range discovered {
		if _, ok := seen[mode]; ok {
			continue
		}
		seen[mode] = struct{}{}
		out = append(out, mode)
	}
	return out
}

// chatModeModelID 把 chat_mode 映射成对外模型名。
// [参数] mode: chat_mode 取值。
// [返回] 模型名，如 glm/chat_agent；空档位沿用 glm/glm-4-plus 兼容名。
// 最近修改时间：2026-11-29 新增, 档位到模型名的稳定映射。
func chatModeModelID(mode string) string {
	if mode == "" {
		return "glm/glm-4-plus"
	}
	return "glm/" + mode
}

// chatModeDisplayName 返回档位的展示名，未知档位原样显示并标注来源。
// [参数] mode: chat_mode 取值。
// [返回] 展示名。
// 最近修改时间：2026-11-29 新增, 未知档位也能在面板上如实呈现。
func chatModeDisplayName(mode string) string {
	if label, ok := chatModeLabel[mode]; ok {
		return label
	}
	if mode == "" {
		return "GLM 标准"
	}
	return "GLM " + mode
}

// dynamicModelInfos 返回动态模型目录：上游实际可用的档位 + 账号自建智能体。
// 档位来自历史会话发现，未知档位会被自动纳入，因此上游新增模型无需改插件。
// [参数] accessToken: 调用令牌，为空表示只回放内置基线；cfg: 插件配置。
// [返回] 模型信息列表、告警列表。
// 最近修改时间：2026-11-29 新增, 面板模型目录动态化。
func dynamicModelInfos(accessToken string, cfg Config) ([]pluginapi.ModelInfo, []string) {
	if strings.TrimSpace(accessToken) == "" {
		return baselineModelInfos(), nil
	}
	var warnings []string
	discovered, err := discoverChatModes(accessToken, cfg)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	modes := effectiveChatModes(discovered)

	infos := make([]pluginapi.ModelInfo, 0, len(modes)+2)
	added := make(map[string]struct{}, len(modes)+2)
	for _, mode := range modes {
		id := chatModeModelID(mode)
		if _, ok := added[id]; ok {
			continue
		}
		added[id] = struct{}{}
		note := "chat_mode=" + mode
		if mode == "" {
			note = "chat_mode=normal（网页默认档）"
		}
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                chatModeDisplayName(mode),
			Description:                note,
			SupportedGenerationMethods: []string{"chat"},
		})
	}

	briefs, err := listUpstreamAssistants(accessToken, cfg)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	for _, brief := range briefs {
		id := strings.TrimSpace(brief.AssistantID)
		if id == "" {
			continue
		}
		modelID := "glm/" + id
		if _, ok := added[modelID]; ok {
			continue
		}
		added[modelID] = struct{}{}
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

	// 兼容别名始终保留，避免打断既有调用方。
	for _, entry := range legacyAliasModels {
		if _, ok := added[entry.ID]; ok {
			continue
		}
		added[entry.ID] = struct{}{}
		infos = append(infos, pluginapi.ModelInfo{
			ID:                         entry.ID,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                entry.DisplayName,
			Description:                entry.Note,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return infos, warnings
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

// handleMgmtModels 返回模型目录：动态档位 + 指定（或首个可用）账号的自建智能体。
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
