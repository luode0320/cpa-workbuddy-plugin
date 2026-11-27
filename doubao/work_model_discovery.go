package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现工作模式（agent_mode=1）模型的动态发现。
//
// 工作模式的可选模型不在 /alice/basic/launch 的 model_list 里，而在
// /alice/slot/action_bar_v3/brief_list 的 entry_list[].active_switch_conf：
// 每条模型项带 model_item_key、可读名与记忆档位，正好是工作模式请求需要的三件套。
// 因此该端点能让新增模型自动出现，不必每次改静态别名表。

const (
	// workModelDiscoveryPath 是承载工作模式模型菜单的业务端点。
	workModelDiscoveryPath = "/alice/slot/action_bar_v3/brief_list"

	// workLaunchPath 是取 assistant_bot_id 的启动端点。
	workLaunchPath = "/alice/user/launch"

	// workModelDiscoveryTTL 是工作模式模型菜单的缓存时长。
	workModelDiscoveryTTL = 10 * time.Minute
)

// workMenuEntry 是菜单里的一条模型记录。
type workMenuEntry struct {
	// ModelItemKey 是上游模型主键，工作模式请求三处都要用它。
	ModelItemKey string `json:"model_item_key"`
	// Name 是菜单展示名（如 GLM 5.4）。
	Name string `json:"name"`
	// ModelExtraParams 承载记忆档位与上下文窗口。
	ModelExtraParams json.RawMessage `json:"model_extra_params"`
	// ReasoningEffortConfig 承载默认推理档位。
	ReasoningEffortConfig struct {
		DefaultLevel int `json:"default_level"`
	} `json:"reasoning_effort_config"`
}

// workModelExtra 是 model_extra_params 的取值。
type workModelExtra struct {
	MemoryProfile string `json:"memory_profile"`
	TotalWindow   string `json:"total_window_size"`
}

// workModeGroup 是菜单里的一种模式及其模型列表。
type workModeGroup struct {
	AgentMode int    `json:"agent_mode"`
	ModeID    string `json:"mode_id"`
	ModelList struct {
		ItemList []workMenuEntry `json:"item_list"`
	} `json:"model_list"`
}

// workMenuConf 是菜单配置，新旧两种形态并存。
type workMenuConf struct {
	UseMenuConfV2 bool `json:"use_menu_conf_v2"`
	MenuConf      struct {
		ItemList []workMenuEntry `json:"item_list"`
	} `json:"menu_conf"`
	MenuConfV2 struct {
		ModeList struct {
			ItemList []workModeGroup `json:"item_list"`
		} `json:"mode_list"`
		ModelList struct {
			ItemList []workMenuEntry `json:"item_list"`
		} `json:"model_list"`
	} `json:"menu_conf_v2"`
}

// workBriefResponse 是 brief_list 的最小响应结构。
type workBriefResponse struct {
	Code int `json:"code"`
	Data struct {
		EntryList []struct {
			ActiveSwitchConf *workMenuConf `json:"active_switch_conf"`
		} `json:"entry_list"`
	} `json:"data"`
}

// workLaunchResponse 是 launch 的最小响应结构。
type workLaunchResponse struct {
	Code int `json:"code"`
	Data struct {
		AssistantBotID json.Number `json:"assistant_bot_id"`
	} `json:"data"`
}

// workModelCache 缓存一次工作模式模型菜单。
type workModelCache struct {
	mu        sync.Mutex
	aliases   []modelAlias
	fetchedAt time.Time
	authID    string
}

var workAliasCache workModelCache

// workBriefEndpointOverride 是工作模式菜单端点的测试覆盖点；非空时优先使用。
var workBriefEndpointOverride string

// workLaunchEndpointOverride 是启动端点的测试覆盖点；非空时优先使用。
var workLaunchEndpointOverride string

// swapWorkBriefEndpoint 临时覆盖菜单端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-11-27 23:32:00；改动原因：新增，避免测试打真实上游。
func swapWorkBriefEndpoint(endpoint string) func() {
	prev := workBriefEndpointOverride
	workBriefEndpointOverride = endpoint
	return func() { workBriefEndpointOverride = prev }
}

// swapWorkLaunchEndpoint 临时覆盖启动端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-11-27 23:32:00；改动原因：新增，避免测试打真实上游。
func swapWorkLaunchEndpoint(endpoint string) func() {
	prev := workLaunchEndpointOverride
	workLaunchEndpointOverride = endpoint
	return func() { workLaunchEndpointOverride = prev }
}

// workBriefTarget 返回菜单端点地址，测试覆盖点优先。
// [参数] sa: 账号。
// [返回] 完整 URL。
// 最近修改时间：2026-11-27 23:33:00；改动原因：新增，收敛端点选择。
func workBriefTarget(sa *StoredAuth) string {
	if workBriefEndpointOverride != "" {
		return workBriefEndpointOverride
	}
	return endpointURL(sa, workModelDiscoveryPath, nil)
}

// workLaunchTarget 返回启动端点地址，测试覆盖点优先。
// [参数] sa: 账号。
// [返回] 完整 URL。
// 最近修改时间：2026-11-27 23:33:00；改动原因：新增，收敛端点选择。
func workLaunchTarget(sa *StoredAuth) string {
	if workLaunchEndpointOverride != "" {
		return workLaunchEndpointOverride
	}
	return endpointURL(sa, workLaunchPath, nil)
}

// discoverWorkModels 拉取指定账号的工作模式模型菜单。
// [参数] sa: 账号；timeout: 请求超时。
// [返回] 模型别名列表与错误。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，让工作模式模型随上游菜单自动更新。
func discoverWorkModels(sa *StoredAuth, timeout time.Duration) ([]modelAlias, error) {
	if sa == nil {
		return nil, fmt.Errorf("账号为空")
	}
	if timeout <= 0 {
		timeout = modelDiscoveryTimeout
	}

	// 1. 先取 assistant_bot_id：菜单端点必须带它，缺失会返回空列表。
	botID, errBot := fetchAssistantBotID(sa, timeout)
	if errBot != nil {
		return nil, errBot
	}

	// 2. 用 bot_id 拉取菜单。
	body, errMarshal := json.Marshal(map[string]string{"language_code": "zh", "bot_id": botID})
	if errMarshal != nil {
		return nil, fmt.Errorf("构造菜单请求体失败：%w", errMarshal)
	}
	raw, errFetch := postWorkEndpointURL(sa, workBriefTarget(sa), body, timeout)
	if errFetch != nil {
		return nil, errFetch
	}

	var out workBriefResponse
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		return nil, fmt.Errorf("菜单解析失败：%w", errUnmarshal)
	}
	if out.Code != 0 {
		return nil, &upstreamError{Code: out.Code, Message: fmt.Sprintf("菜单端点返回业务错误 %d", out.Code)}
	}
	aliases := workAliasesFromMenu(out)
	if len(aliases) == 0 {
		return nil, fmt.Errorf("菜单没有返回工作模式模型")
	}
	return aliases, nil
}

// fetchAssistantBotID 取账号的 assistant_bot_id。
// [参数] sa: 账号；timeout: 请求超时。
// [返回] bot_id 与错误。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，菜单端点依赖该标识。
func fetchAssistantBotID(sa *StoredAuth, timeout time.Duration) (string, error) {
	raw, errFetch := postWorkEndpointURL(sa, workLaunchTarget(sa), []byte("{}"), timeout)
	if errFetch != nil {
		return "", errFetch
	}
	var out workLaunchResponse
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		return "", fmt.Errorf("启动端点解析失败：%w", errUnmarshal)
	}
	botID := strings.TrimSpace(out.Data.AssistantBotID.String())
	if botID == "" {
		botID = defaultBotID
	}
	return botID, nil
}

// postWorkEndpointURL 向工作模式业务端点发起一次 JSON POST。
// [参数] sa: 账号；target: 完整端点 URL；body: 请求体；timeout: 超时。
// [返回] 响应体与错误。
// 最近修改时间：2026-11-27 23:32:00；改动原因：改为接收完整 URL，便于测试覆盖端点。
func postWorkEndpointURL(sa *StoredAuth, target string, body []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if errReq != nil {
		return nil, fmt.Errorf("构造请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "application/json")
	applyUpstreamHeaders(req)

	client, jar := newUpstreamClient(sa, timeout)
	ticket, errTicket := acquireUpstream(ctx, sa.AuthID, upstreamKindProbe)
	if errTicket != nil {
		return nil, errTicket
	}
	var gateErr error
	defer func() {
		releaseUpstream(ticket, gateErr)
	}()

	resp, errDo := client.Do(req)
	if errDo != nil {
		gateErr = errDo
		return nil, fmt.Errorf("请求上游失败：%w", errDo)
	}
	defer resp.Body.Close()
	defer func() {
		_ = jar.persist(sa)
	}()

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		gateErr = errRead
		return nil, fmt.Errorf("读取响应失败：%w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		gateErr = &upstreamError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode),
		}
		return nil, gateErr
	}
	return raw, nil
}

// workAliasesFromMenu 从菜单响应里提取工作模式模型并转成别名。
// [参数] resp: 菜单响应。
// [返回] 别名列表（按展示名排序，按 model_item_key 去重）。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，菜单到别名的转换。
func workAliasesFromMenu(resp workBriefResponse) []modelAlias {
	seen := make(map[string]struct{})
	out := make([]modelAlias, 0, 16)
	for _, entry := range resp.Data.EntryList {
		conf := entry.ActiveSwitchConf
		if conf == nil {
			continue
		}
		// 1. 旧形态：菜单项直接平铺，无模式分组。
		if !conf.UseMenuConfV2 {
			for _, item := range conf.MenuConf.ItemList {
				appendWorkAlias(&out, seen, item, 1)
			}
			continue
		}
		// 2. 新形态：按模式分组，只取 agent_mode=1 的工作模式。
		for _, group := range conf.MenuConfV2.ModeList.ItemList {
			if group.AgentMode != 1 {
				continue
			}
			for _, item := range group.ModelList.ItemList {
				appendWorkAlias(&out, seen, item, group.AgentMode)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// appendWorkAlias 把一条菜单模型追加为别名，重复键跳过。
// [参数] out: 结果切片；seen: 已收录的键；item: 菜单项；agentMode: 该模式编号。
// [返回] 无。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，单条菜单模型转换。
func appendWorkAlias(out *[]modelAlias, seen map[string]struct{}, item workMenuEntry, agentMode int) {
	if agentMode != 1 {
		return
	}
	key := strings.TrimSpace(item.ModelItemKey)
	if key == "" {
		return
	}
	if _, dup := seen[key]; dup {
		return
	}
	seen[key] = struct{}{}

	var extra workModelExtra
	if len(item.ModelExtraParams) > 0 {
		if errExtra := json.Unmarshal(item.ModelExtraParams, &extra); errExtra != nil {
			// 额外参数解析失败只丢档位，不影响模型键可用。
			extra = workModelExtra{}
		}
	}
	name := strings.TrimSpace(item.Name)
	alias := modelAlias{
		ID:            workAliasID(name, key),
		DisplayName:   name,
		Description:   workAliasDescription(name),
		ModelItemKey:  key,
		MemoryProfile: strings.TrimSpace(extra.MemoryProfile),
		TotalWindow:   strings.TrimSpace(extra.TotalWindow),
	}
	if alias.DisplayName == "" {
		alias.DisplayName = alias.ID
	}
	if item.ReasoningEffortConfig.DefaultLevel > 0 {
		alias.ReasoningEffort = strconv.Itoa(item.ReasoningEffortConfig.DefaultLevel)
	}
	*out = append(*out, alias)
}

// workAliasID 由展示名派生对外别名；名字含中文时退回模型键。
// [参数] name: 菜单展示名；key: 上游模型键。
// [返回] 对外别名。
// 最近修改时间：2026-11-27 23:38:00；改动原因：中文名不再派生出与既有别名冲突的片段。
func workAliasID(name, key string) string {
	// 1. 中文名（如「豆包 2.1 Pro」）直接取模型键，避免派生出 2.1-pro 这类冲突片段。
	for _, r := range name {
		if r >= 0x4E00 && r <= 0x9FFF {
			return strings.TrimPrefix(key, "person-")
		}
	}

	// 2. 纯拉丁名按小写、空格转连字符派生。
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = strings.TrimPrefix(key, "person-")
	}
	return id
}

// workAliasDescription 生成菜单模型的说明。
// [参数] name: 菜单展示名。
// [返回] 说明文本。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，说明随上游菜单动态生成。
func workAliasDescription(name string) string {
	if strings.TrimSpace(name) == "" {
		return "上游模型菜单项。"
	}
	return fmt.Sprintf("%s，由上游模型菜单实时提供。", name)
}

// cachedWorkModels 返回工作模式模型别名，命中缓存时直接复用。
// [参数] sa: 账号；force: true 表示忽略缓存强制刷新。
// [返回] 别名列表与错误。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，按账号缓存菜单结果。
func cachedWorkModels(sa *StoredAuth, force bool) ([]modelAlias, error) {
	if sa == nil {
		return nil, fmt.Errorf("账号为空")
	}

	workAliasCache.mu.Lock()
	if !force && workAliasCache.authID == sa.AuthID && len(workAliasCache.aliases) > 0 &&
		time.Since(workAliasCache.fetchedAt) < workModelDiscoveryTTL {
		cached := workAliasCache.aliases
		workAliasCache.mu.Unlock()
		return cached, nil
	}
	workAliasCache.mu.Unlock()

	aliases, errDiscover := discoverWorkModels(sa, modelDiscoveryTimeout)
	if errDiscover != nil {
		return nil, errDiscover
	}

	workAliasCache.mu.Lock()
	workAliasCache.aliases = aliases
	workAliasCache.fetchedAt = time.Now()
	workAliasCache.authID = sa.AuthID
	workAliasCache.mu.Unlock()
	return aliases, nil
}

// refreshWorkModels 尝试刷新工作模式菜单缓存。
//
// 刷新失败只记日志，目录继续用静态别名兜底，不阻断模型列表可用性。
// [参数] sa: 账号。
// [返回] 无。
// 最近修改时间：2026-11-27 23:30:00；改动原因：新增，目录刷新入口。
func refreshWorkModels(sa *StoredAuth) {
	if _, errRefresh := cachedWorkModels(sa, false); errRefresh != nil {
		log.Printf("[doubao] 工作模式菜单刷新失败：%v", errRefresh)
	}
}

// workModelsSnapshot 返回当前缓存的工作模式模型（无缓存时为空）。
// [参数] 无。
// [返回] 别名列表副本。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，供目录合并读取缓存。
func workModelsSnapshot() []modelAlias {
	workAliasCache.mu.Lock()
	defer workAliasCache.mu.Unlock()
	if len(workAliasCache.aliases) == 0 {
		return nil
	}
	out := make([]modelAlias, len(workAliasCache.aliases))
	copy(out, workAliasCache.aliases)
	return out
}

// resetWorkModelCache 清空工作模式模型缓存（供测试与账号切换使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-27 23:20:00；改动原因：新增，缓存复位入口。
func resetWorkModelCache() {
	workAliasCache.mu.Lock()
	workAliasCache.aliases = nil
	workAliasCache.fetchedAt = time.Time{}
	workAliasCache.authID = ""
	workAliasCache.mu.Unlock()
}
