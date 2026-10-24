// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// upstreamModelNames 保存「上游官方展示名 → 内部代号」的运行时映射。
//
// 上游会在不改内部代号的前提下给模型改名（2026-10-24 实测客户端已把
// kmodel_latest 显示为 Kimi-K3.1-Pro、mmodel 显示为 MiniMax-M3.1-Flash，
// 而本文件的静态表仍停在改名前的旧名字）。硬编码表无法跟上这种漂移，
// 因此动态发现拿到的 display_name 必须同时用于两件事：对外主条目命名，
// 以及请求回译（见 registerUpstreamModelNames / lookupUpstreamKey）。
var upstreamModelNames struct {
	sync.RWMutex
	byDisplay map[string]string
}

// registerUpstreamModelNames 把一批动态发现结果登记进运行时映射。
//
// 写入策略是「先到先得」：多个账号的清单可能不同（旧账号缓存仍带旧名），
// 后到的旧名不得覆盖先到的映射；同名不同代号属上游数据异常，同样不覆盖，
// 避免把请求按错误代号发出去。
// [参数] models：动态发现得到的模型条目（ID 为内部代号，DisplayName 为上游官方名）
// [返回] 无
// 最近修改时间 2026-10-25（新增，承接上游模型改名）
func registerUpstreamModelNames(models []pluginapi.ModelInfo) {
	upstreamModelNames.Lock()
	defer upstreamModelNames.Unlock()
	if upstreamModelNames.byDisplay == nil {
		upstreamModelNames.byDisplay = make(map[string]string, len(models))
	}
	for _, m := range models {
		// 内部代号只能取 ID：ModelInfo.Name 在不同来源下语义不一致
		// （动态发现存展示名、exposeModelIDs 输出存内部代号），用它当 key
		// 会把映射登记成「展示名 → 展示名」，请求回译时原样返回错值。
		key := strings.ToLower(strings.TrimSpace(m.ID))
		name := strings.ToLower(strings.TrimSpace(m.DisplayName))
		if key == "" || name == "" || name == key {
			continue
		}
		if _, exists := upstreamModelNames.byDisplay[name]; exists {
			continue
		}
		upstreamModelNames.byDisplay[name] = key
	}
}

// lookupUpstreamKey 按上游官方名查内部代号。
// [参数] officialName：客户端请求里的模型名（大小写不敏感）
// [返回] 命中的内部代号与 true；未登记时返回空串与 false
// 最近修改时间 2026-10-25（新增，承接上游模型改名）
func lookupUpstreamKey(officialName string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(officialName))
	if key == "" {
		return "", false
	}
	upstreamModelNames.RLock()
	defer upstreamModelNames.RUnlock()
	name, ok := upstreamModelNames.byDisplay[key]
	return name, ok
}

// internalKeyDisplayNames 映射上游内部模型代号到官方展示名。
//
// 官方名取自 Qoder 客户端模型目录（与 qoder2api-hub 的 qoder_catalog_cn.json
// 逐项一致）。客户端下拉里展示的就是这些名字，而 /algo/api/v2/model/list
// 只返回内部代号，两者必须在这里对上。
//
// 本表**只是兜底**：动态发现返回的 display_name 才是上游权威名，上游改名后
// 本表会滞后（见 upstreamModelNames）。仅当上游没给 display_name 时才回落到这里。
var internalKeyDisplayNames = map[string]string{
	"auto":          "Auto",
	"cmodel":        "Cantus",
	"qmodel_38max":  "Qwen3.8-Max",
	"qfmodel":       "Qwen3.8-Flash",
	"qmodel_latest": "Qwen3.7-Max",
	"qmodel":        "Qwen3.7-Plus",
	"q37fmodel":     "Qwen3.7-Flash",
	"gmodel":        "GLM-5.3",
	"gfmodel":       "GLM-5.3-Flash",
	"gm51model":     "GLM-5.2",
	"kmodel_latest": "Kimi-K3",
	"kmodel":        "Kimi-K2.8-Preview",
	"dmodel":        "DeepSeek-V4-Pro",
	"dfmodel":       "DeepSeek-Flash",
	"mmodel":        "MiniMax-M3",
}

// modelDisplayName 返回模型的官方展示名：内部代号查表，已是官方名则原样返回。
// [参数] modelID：模型标识（内部代号或官方名，允许带 provider/ 前缀）
// [返回] 官方展示名；无法识别时返回去掉前缀后的原值
// 最近修改时间 2026-10-20（新增，模型对外 ID 改用官方名）
func modelDisplayName(modelID string) string {
	id := strings.TrimSpace(modelID)
	if i := strings.Index(id, "/"); i >= 0 {
		id = strings.TrimSpace(id[i+1:])
	}
	if id == "" {
		return ""
	}
	if name, ok := internalKeyDisplayNames[strings.ToLower(id)]; ok {
		return name
	}
	return id
}

// exposeModelIDs 把上游内部代号改写成客户端可读的官方模型名。
//
// 每个条目产出两个名字（共用同一份元数据，Name 恒为内部代号）：
//  1. 主条目 ID = 官方名：动态发现优先使用上游返回的 display_name，缺失时
//     回落到 internalKeyDisplayNames；客户端下拉里显示的就是它；
//  2. 兼容条目 ID = 内部代号，仅当两者不同时追加。
//
// 之所以主条目必须用上游 display_name 而不是硬编码表：上游改模型展示名时
// 内部代号不变（2026-10-24 实测 Kimi-K3 → Kimi-K3.1-Pro），硬编码表会一直
// 暴露旧名，用户在客户端模型表里找不到新版模型。
//
// 兼容条目必须真实注册而不能只做映射：宿主用模型名反查 provider
// （internal/util/provider.go GetProviderName → registry.GetModelProviders），
// 未注册的名字直接回 `unknown provider for model` 400；选号侧同样按完整模型名
// 匹配账号（sdk/cliproxy/auth/conductor_selection.go authSupportsRouteModel）。
//
// 无法识别的 ID（用户配置的模型、上游新增但未登记官方名的代号）原样透传。
// [参数] models：内部代号列表（DisplayName 为上游官方名，可为空）
// [返回] 主条目在前、兼容条目随后的新切片
// 最近修改时间 2026-10-25（主条目改用上游 display_name，硬编码表降为兜底）
func exposeModelIDs(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)
	add := func(m pluginapi.ModelInfo, id, displayName, internalKey string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		m.ID = id
		m.DisplayName = displayName
		m.Name = internalKey
		out = append(out, m)
	}
	for _, m := range models {
		base := strings.TrimSpace(m.ID)
		if i := strings.Index(base, "/"); i >= 0 {
			base = strings.TrimSpace(base[i+1:])
		}
		if base == "" {
			continue
		}
		// 1. 官方名优先取上游 display_name，其次查硬编码表。
		display := strings.TrimSpace(m.DisplayName)
		if display == "" || strings.EqualFold(display, base) {
			display = internalKeyDisplayNames[strings.ToLower(base)]
		}
		if display == "" {
			// 2. 上游与本地表都认不出：原样透传，不凭空造名字，也不追加
			//    兼容条目（没有官方名就没有第二个名字可注册）。
			add(m, base, m.DisplayName, base)
			continue
		}
		add(m, display, display, base)
		// 兼容条目按原始写法注册：auto / ultimate 这类代号与官方名只差大小写，
		// EqualFold 判等会漏注册，而宿主反查 provider 区分大小写之外的精确名字，
		// 用 add 内部的 seen 精确比较即可，这里不跳过。
		add(m, base, "", base)
	}
	return out
}

// wbModels is the static fallback model list for QoderWork CN. Keys mirror
// /root/qoderwork/models_list.json (KNOWLEDGE §6.2). Aliases use the qoder/
// prefix in AuthAttributes; bare IDs work too. Dynamic refresh via
// /algo/api/v2/model/list replaces this at runtime when an account is present.
//
// 只放上游内部代号，所有对外暴露统一由 exposeModelIDs 展开成官方名主条目
// + 兼容代号条目，保证静态兜底与动态发现的对外口径完全一致。
//
// 清单口径：2026-10-24 生产 18 个 Free 账号实测，健康账号动态发现稳定返回
// 这 10 个代号。此前多出的 cmodel（官方目录已彻底下线）与 qmodel_latest /
// qmodel / q37fmodel / gm51model（官方目录仍在但 Free 档 enable=false）
// 已移出：静态兜底只在动态完全失败时生效，列出当前档位必然失败的模型
// 只会让用户选到后收到上游能力错误。代号映射本身仍保留在
// internalKeyDisplayNames / cpaToUpstreamKey，上游恢复即可用。
// [参数] 无
// [返回] 静态兜底模型列表（内部代号 + 各自官方规格算出的 max_tokens）
// 最近修改时间 2026-10-25（移除上游当前档位不可用的 5 个代号）
func wbModels() []pluginapi.ModelInfo {
	list := []pluginapi.ModelInfo{
		{ID: "auto", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel_38max", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qfmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gfmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kmodel_latest", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dfmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "mmodel", Name: "", ContextLength: 1000000, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
	for i := range list {
		spec := resolveOfficialModelSpec(list[i].ID)
		if spec.ReasoningMax > 0 {
			list[i].MaxCompletionTokens = applyMaxTokensFloor(spec.ReasoningMax)
		} else {
			list[i].MaxCompletionTokens = applyMaxTokensFloor(spec.MaxOutputTokens)
		}
	}
	return list
}

// configuredModels holds the config_yaml `models:` override. Empty means
// fall back to dynamic discovery / wbModels(). Guarded by its own RWMutex:
// configure() runs on a host RPC thread while model.static / model.for_auth
// run on other RPC threads, so the slice must never be written/read unlocked.
var (
	configuredModels   []pluginapi.ModelInfo
	configuredModelsMu sync.RWMutex
)

// setConfiguredModels 整体替换 config_yaml `models:` 覆盖列表。
// [参数] models：新的模型列表（非 nil 非空）
// [返回] 无
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func setConfiguredModels(models []pluginapi.ModelInfo) {
	configuredModelsMu.Lock()
	configuredModels = models
	configuredModelsMu.Unlock()
}

// clearConfiguredModels 清空覆盖，恢复动态获取 / 静态默认路径。
// [参数] 无
// [返回] 无
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func clearConfiguredModels() {
	configuredModelsMu.Lock()
	configuredModels = nil
	configuredModelsMu.Unlock()
}

// getConfiguredModels 返回当前覆盖列表的引用（调用方不得原地修改）。
// [参数] 无
// [返回] configuredModels 当前值，空切片语义为"未覆盖"
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func getConfiguredModels() []pluginapi.ModelInfo {
	configuredModelsMu.RLock()
	defer configuredModelsMu.RUnlock()
	return configuredModels
}

// parseModelsConfig decodes the config_yaml `models:` list. Items may be a
// string (model id) or an object {id, name, alias, context, max_tokens,
// enabled, reasoning}; enabled=false entries are skipped. An explicit empty
// list clears the override (back to dynamic discovery / static defaults); a
// list whose entries are all malformed leaves the current value untouched so
// a bad edit never silently wipes a working override.
// [参数] v：config_yaml `models:` 的 JSON 值（字符串列表或对象列表）
// [返回] 无（内部写入 configuredModels）
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func parseModelsConfig(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return
	}
	// 显式 `models: []` 表示"停止覆盖"，恢复动态获取 / 静态默认。
	if len(items) == 0 {
		clearConfiguredModels()
		return
	}
	var out []pluginapi.ModelInfo
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil && strings.TrimSpace(s) != "" {
			out = append(out, modelInfoFromConfig(strings.TrimSpace(s), "", 0, 0))
			continue
		}
		var mi struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Alias     string `json:"alias"`
			Context   int64  `json:"context"`
			MaxTokens int64  `json:"max_tokens"`
			Enabled   *bool  `json:"enabled"`
			Reasoning bool   `json:"reasoning"`
		}
		if err := json.Unmarshal(item, &mi); err != nil || strings.TrimSpace(mi.ID) == "" {
			continue
		}
		if mi.Enabled != nil && !*mi.Enabled {
			continue
		}
		out = append(out, modelInfoFromConfig(strings.TrimSpace(mi.ID), strings.TrimSpace(mi.Name), mi.Context, mi.MaxTokens))
	}
	if len(out) > 0 {
		setConfiguredModels(out)
	}
}

// resolveModels 按优先级链求最终模型列表：动态发现 > 配置 > 静态默认。
//
// 语义（2026-09-12 优先级反转）：
//   - 动态发现有结果时**完全忽略** config_yaml 覆盖与静态默认：上游是权威
//     全集，动态列表即最终列表，配置不再参与合并；
//   - 动态发现无结果（无凭据 / 上游失败 / 空列表）时，才使用配置覆盖；
//   - 配置也为空时，最后回退静态默认列表。
//
// 之所以要"动态优先"：上游新增模型只能从动态发现拿到；若配置优先，用户手工
// 配过的旧列表会永久遮蔽上游新模型。配置与静态列表降级为**保底**。
//
// [参数] dynamic：动态发现列表（可为空，空表示"动态不可用"）
//
//	configured：config_yaml `models:` 覆盖列表（可为空）
//	fallback：静态默认列表（wbModels()）
//
// [返回] 按优先级链选出的最终列表
// 最近修改时间 2026-09-12（由"配置优先"反转为"动态优先、配置保底"）
func resolveModels(dynamic, configured, fallback []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	// 先过滤空 ID 再看长度：只含空 ID 的动态列表语义上等于"没有结果"，
	// 不能因为它 len>0 就遮蔽配置或静态兜底。
	if dm := nonEmptyModels(dynamic); len(dm) > 0 {
		return dm
	}
	if cm := nonEmptyModels(configured); len(cm) > 0 {
		return cm
	}
	return nonEmptyModels(fallback)
}

// nonEmptyModels 过滤空 ID 条目并返回新切片，避免调用方拿到含空 ID 的列表或
// 共享底层数组（dynamicModelsCache / wbModels 的切片不可原地改写）。
// [参数] models：待过滤列表
// [返回] 去掉空 ID 后的新切片
// 最近修改时间 2026-09-12（随优先级反转新增）
func nonEmptyModels(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// resolveModelsFromStorage 是 handleModelForAuth 的判定入口：先尝试账号级
// 动态发现，成功即返回动态列表，否则交给优先级链回退配置 / 静态默认。
// [参数] authID：宿主传入的账号标识，用于缓存分片
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 按优先级链选出的最终列表
// 最近修改时间 2026-10-20 03:10:00 修改, 缓存分片后需透传账号标识
func resolveModelsFromStorage(authID string, storageJSON []byte) []pluginapi.ModelInfo {
	dynamic := fetchDynamicModelsFromStorage(authID, storageJSON)
	return resolveModels(dynamic, getConfiguredModels(), wbModels())
}

// modelInfoFromConfig builds a ModelInfo for a config-declared model,
// normalizing fields to match the plugin's other model sources. alias /
// reasoning are accepted for schema compatibility but have no ModelInfo
// counterpart and are intentionally ignored here.
// [参数] id：模型 ID（必填）；name：显示名（空则用 id）；ctxLen：上下文长度；maxTok：最大输出 token 数
// [返回] 规范化后的 pluginapi.ModelInfo
// 最近修改时间 2026-08-28（新增 config_yaml models 覆盖支持）
func modelInfoFromConfig(id, name string, ctxLen, maxTok int64) pluginapi.ModelInfo {
	if name == "" {
		name = id
	}
	return pluginapi.ModelInfo{
		ID:                         id,
		Name:                       name,
		ContextLength:              ctxLen,
		MaxCompletionTokens:        applyMaxTokensFloor(maxTok),
		OwnedBy:                    providerName,
		SupportedGenerationMethods: []string{"chat"},
	}
}

// cachedDynamicModels 读取指定账号未过期的模型缓存。
// [参数] authID：账号标识，内部经 dynamicModelsKey 归一化；空串直接视为未命中
// [返回] 缓存命中的模型列表与 true；未命中或已过期返回 nil, false
// 最近修改时间 2026-10-20 03:10:00 修改, 缓存改为按账号分片，避免跨账号互相覆盖
func cachedDynamicModels(authID string) ([]pluginapi.ModelInfo, bool) {
	key := dynamicModelsKey(authID)
	if key == "" {
		return nil, false
	}
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	entry := dynamicModelsCache[key]
	if entry != nil && len(entry.models) > 0 && time.Since(entry.fetched) < dynamicModelsCacheTTL {
		return entry.models, true
	}
	return nil, false
}

// widestCachedDynamicModels 返回所有账号缓存中最完整的一份，供无账号上下文的
// model.static 使用。
//
// model.static 描述插件整体可提供的模型全集，没有账号信息；取份数最多的一份
// 而非任意一份，是因为各账号清单之间存在包含关系（健康账号 ⊇ 耗尽账号），
// 缺失模型会让宿主的全局模型表漏项。
// [参数] 无
// [返回] 未过期缓存中模型最多的一份；全部未命中或过期时返回 nil, false
// 最近修改时间 2026-10-20 03:10:00 新增, 分片缓存后的全局聚合视图
func widestCachedDynamicModels() ([]pluginapi.ModelInfo, bool) {
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	var best []pluginapi.ModelInfo
	for _, entry := range dynamicModelsCache {
		if entry == nil || len(entry.models) == 0 {
			continue
		}
		if time.Since(entry.fetched) >= dynamicModelsCacheTTL {
			continue
		}
		if len(entry.models) > len(best) {
			best = entry.models
		}
	}
	if len(best) == 0 {
		return nil, false
	}
	return best, true
}

// allDynamicModelsFailed 判断当前是否所有已记录账号都处于负缓存窗口内，
// 供无账号上下文的全局扫描决定是否直接放弃。
//
// 与 per-account 窗口的区别：全局扫描由 model.static 触发，它不针对某个账号，
// 只要还有一个账号不在窗口内就仍值得尝试，因此不能用单个账号的窗口拦截整轮扫描。
// [参数] 无
// [返回] true 表示无可用账号可试（含从未成功且全部失败的情况）
// 最近修改时间 2026-10-20 03:10:00 新增, 补齐分账号负缓存的全局扫描判定
func allDynamicModelsFailed() bool {
	now := time.Now()
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	if len(dynamicModelsFails) == 0 {
		return false
	}
	for _, fail := range dynamicModelsFails {
		if fail == nil || fail.failedAt.IsZero() || now.Sub(fail.failedAt) >= dynamicModelsFailCacheTTL {
			return false
		}
	}
	return true
}

// callModelsAPIFn 是 callModelsAPI 的可替换入口（同 sendActivePingQoderFn 惯例），
// 供负缓存行为单测注入假上游，避免真实网络与宿主 RPC 依赖。
var callModelsAPIFn = callModelsAPI

// hostAuthListFilesFn 是 hostAuthListFiles 的可替换入口（同上惯例），
// 供全量扫描单测注入账号列表——否则「遍历是否真的按额度排序」这一层
// 无法在 shim 环境断言，删掉排序调用也不会有用例失败。
var hostAuthListFilesFn = hostAuthListFiles

// hostAuthGetByIndexFn 是 hostAuthGetByIndex 的可替换入口，
// 与 hostAuthListFilesFn 配套提供全量扫描的可测性。
var hostAuthGetByIndexFn = hostAuthGetByIndex

// fetchDynamicModelsFallbackFn 是存储入口回退到全量扫描的调用点接缝。
// 单测据此断言「回退发生在开启负缓存窗口之前」——顺序颠倒会让窗口
// 立刻拦住回退本身，使另一账号本可成功的扫描永远走不到。
var fetchDynamicModelsFallbackFn = fetchDynamicModels

// inDynamicModelsFailWindow 判断指定账号当前是否处于模型发现负缓存窗口内。
// [参数] authID：账号标识，内部经 dynamicModelsKey 归一化；空串视为无窗口
// [返回] true 表示窗口内，调用方应直接放弃该账号的发现尝试
// 最近修改时间 2026-10-20 03:10:00 修改, 负缓存改为按账号隔离，单账号被拒不连带封锁其它账号
func inDynamicModelsFailWindow(authID string) bool {
	key := dynamicModelsKey(authID)
	if key == "" {
		return false
	}
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	entry := dynamicModelsFails[key]
	return entry != nil && !entry.failedAt.IsZero() && time.Since(entry.failedAt) < dynamicModelsFailCacheTTL
}

// markDynamicModelsFailed 记录指定账号的一次发现失败，开启该账号的负缓存窗口。
// [参数] authID：账号标识；空串直接忽略（无 key 无法分片，不能降级成全局窗口）
// [返回] 无
// 最近修改时间 2026-10-20 03:10:00 修改, 负缓存改为按账号分片
func markDynamicModelsFailed(authID string) {
	key := dynamicModelsKey(authID)
	if key == "" {
		return
	}
	dynamicModelsMu.Lock()
	dynamicModelsFails[key] = &dynamicModelsFailEntry{failedAt: time.Now()}
	dynamicModelsMu.Unlock()
}

// storeDynamicModels 写入指定账号的模型缓存，并清除该账号的负缓存窗口。
// [参数] authID：账号标识；models：该账号的模型列表
// [返回] 是否写入成功；账号标识为空时返回 false，调用方据此保留原返回路径
// 最近修改时间 2026-10-20 03:10:00 修改, 缓存按账号分片并清除该账号负缓存
func storeDynamicModels(authID string, models []pluginapi.ModelInfo) bool {
	key := dynamicModelsKey(authID)
	if key == "" {
		return false
	}
	dynamicModelsMu.Lock()
	dynamicModelsCache[key] = &dynamicModelsCacheEntry{models: models, fetched: time.Now()}
	delete(dynamicModelsFails, key) // 成功即清除该账号的负缓存窗口
	dynamicModelsMu.Unlock()
	return true
}

// modelDiscoveryRank 给出账号在模型发现遍历中的优先级档位（数值越小越先试）。
//
// 不能直接按积分数值排序：未测账号返回 -1，耗尽账号返回 0，数值大小会把
// 「耗尽」排在「未测」前面，与要求的「有额度 > 未测 > 耗尽」相反。
// [参数] authID：账号标识
// [返回] 0=有额度；1=未测；2=耗尽
// 最近修改时间 2026-10-20 03:10:00 修改, 新增模型发现的额度优先档位
func modelDiscoveryRank(authID string) int {
	remain, _ := cachedCreditsScore(authID)
	switch {
	case remain > 0:
		return 0 // 有额度：最可能返回完整清单
	case remain < 0:
		return 1 // 未测：信息不足，排在有额度之后
	default:
		return 2 // 耗尽：上游只给可用子集，最后再试
	}
}

// sortAuthsByCredits 把账号按「有额度 > 未测 > 耗尽」重排，同档保持入参顺序。
//
// 模型发现采用「首个成功即停」，因此遍历顺序直接决定哪份清单成为默认来源。
// 上游对额度耗尽的账号只返回可用子集（2026-10-20 生产实测 2 个 vs 14 个），
// 沿用宿主文件名顺序会让「字母序靠前的耗尽账号」垄断全局清单。用调度器同一套
// 积分口径排序后，排第一的必然是最可能返回完整清单的健康账号。
// [参数] files：宿主返回的账号列表
// [返回] 重排后的新切片，不修改入参
// 最近修改时间 2026-10-20 03:10:00 修改, 新增额度优先排序
func sortAuthsByCredits(files []pluginapi.HostAuthFileEntry) []pluginapi.HostAuthFileEntry {
	out := make([]pluginapi.HostAuthFileEntry, len(files))
	copy(out, files)
	sort.SliceStable(out, func(i, j int) bool {
		return modelDiscoveryRank(out[i].ID) < modelDiscoveryRank(out[j].ID)
	})
	return out
}

// fetchDynamicModels 按宿主已注册的 qoderwork 凭据逐个尝试动态发现。
//
// **不**在此处兜底静态列表：返回空切片语义为"动态发现不可用"，由调用方按
// resolveModels 的优先级链回退配置或静态默认。若这里返回 wbModels()，
// 调用方无法区分"上游真的只有这些模型"与"上游调用失败"。
// [参数] 无
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-10-20 03:10:00 修改, 改为额度优先排序，缓存写入按账号分片
func fetchDynamicModels() []pluginapi.ModelInfo {
	// 全局扫描不带账号上下文，只能命中"最完整的一份"聚合视图。
	if models, ok := widestCachedDynamicModels(); ok {
		return models
	}
	// 负缓存窗口内直接放弃逐账号重扫：宿主每次 auth 重写都会触发
	// model.for_auth，若失败不留窗口，N 账号 × 每事件一次全扫会放大成
	// N+1 请求风暴（生产 OOM 主因，见 BUG-QODER-MODELS-SIGNATURE-INVALID-20261012）。
	// 全局扫描的窗口按"零 key"聚合判定：只有当所有账号都在各自窗口内才跳过。
	if allDynamicModelsFailed() {
		return nil
	}
	files, err := hostAuthListFilesFn()
	if err != nil || len(files) == 0 {
		return nil
	}
	// Strict filename-prefix match — same filter as host_auth.go hostAuthList.
	// (Earlier code also matched files containing "codebuddy" anywhere, which
	// would wrongly include workbuddy-*.json auths here and cause us to call
	// the qoderwork models API with a workbuddy token.)
	prefix := authFilePrefix
	candidates := make([]pluginapi.HostAuthFileEntry, 0, len(files))
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			continue
		}
		candidates = append(candidates, f)
	}
	// 额度优先：健康账号先试，耗尽账号兜底，避免耗尽账号的小清单成为默认。
	candidates = sortAuthsByCredits(candidates)
	scanned := 0
	for _, f := range candidates {
		if inDynamicModelsFailWindow(f.ID) {
			continue
		}
		scanned++
		raw, err := hostAuthGetByIndexFn(f.AuthIndex)
		if err != nil {
			continue
		}
		sa, err := parseStored(raw)
		if err != nil || sa == nil {
			continue
		}
		dyn, err := callModelsAPIFn(sa)
		if err == nil && len(dyn) > 0 {
			storeDynamicModels(f.ID, dyn)
			return dyn
		}
		markDynamicModelsFailed(f.ID)
	}
	if scanned > 0 && allDynamicModelsFailed() {
		return nil
	}
	return nil
}

// fetchDynamicModelsFromStorage 用请求自带的账号凭据优先尝试动态发现，
// 失败时再回退到已注册凭据扫描。**不**兜底配置与静态列表。
//
// 缓存按账号分片：先查本账号的缓存，未命中才真正打上游。这样额度耗尽账号
// 与健康账号各拿各的清单，彼此的发现结果互不覆盖。
// [参数] authID：宿主传入的账号标识，用作缓存分片 key
// [参数] storageJSON：宿主传入的账号凭据 JSON
// [返回] 动态模型列表；动态不可用时返回 nil
// 最近修改时间 2026-10-20 03:10:00 修改, 缓存与负缓存均改为按账号分片
func fetchDynamicModelsFromStorage(authID string, storageJSON []byte) []pluginapi.ModelInfo {
	if models, ok := cachedDynamicModels(authID); ok {
		return models
	}
	// 负缓存窗口内直接返回 nil，让上层走 resolveModels 静态兜底。
	// 窗口按本账号判定：别的账号失败不应该连带封锁本账号的发现。
	if inDynamicModelsFailWindow(authID) {
		return nil
	}
	sa, err := parseStored(storageJSON)
	if err != nil || sa == nil {
		return fetchDynamicModelsFallbackFn()
	}
	dyn, dynErr := callModelsAPIFn(sa)
	if dynErr == nil && len(dyn) > 0 {
		storeDynamicModels(authID, dyn)
		return dyn
	}
	// 本账号被拒后先回退全量扫描，回退必须在开窗之前：窗口一旦置位，
	// 本账号的后续调用会被立即拦住，本函数声明的「回退到已注册凭据扫描」
	// 就变成死路径（另一账号本可成功）。
	if dyn := fetchDynamicModelsFallbackFn(); len(dyn) > 0 {
		return dyn
	}
	// 回退也拿不到结果才开窗：本账号确实打过上游，属真实失败而非
	// 「本地没账号」，可以安全进入负缓存。
	markDynamicModelsFailed(authID)
	return nil
}

// newModelsAPIRequest 构造一次 COSY 签名的模型清单 GET 请求。
//
// model/list 语义上无入参，但 COSY 签名覆盖 body，且服务端会校验
// 「签名声明的 body == 实际收到的 body」：裸 GET（body=nil）恒被判
// Signature invalid（403 code 101，生产累计 5449 条，见
// BUG-QODER-MODELS-SIGNATURE-INVALID-20261012）。参照可正常工作的
// qoder2api-hub read_dynamic_models，必须把编码后的 "{}" 真作为 GET
// 请求体发出，与签名使用同一字符串。
// [参数] ctx：请求上下文；sa：签名与鉴权所用账号；rawURL：模型清单端点。
// [返回] 已附带 COSY 头的请求；签名构建失败时返回错误。
// 最近修改时间：2026-10-19 12:30:00；改动原因：从 callModelsAPI 抽出，便于断言签名 body 与实际 body 一致。
func newModelsAPIRequest(ctx context.Context, sa *storedAuth, rawURL string) (*http.Request, error) {
	encodedBody := qoderEncode([]byte("{}"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, strings.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}
	// COSY signing needs the encoded body even for GET (signature includes body).
	if err := applyCosyHeaders(req, sa, encodedBody, rawURL, "", false); err != nil {
		return nil, fmt.Errorf("cosy sign: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientUA)
	return req, nil
}

// callModelsAPI GETs /algo/api/v2/model/list from the QoderWork gateway
// with COSY signing and parses every chat-capable scene into ModelInfo.
// 请求构造（含签名）见 newModelsAPIRequest。
//
// 为什么取**全部场景并集**而不是只读 chat：官方客户端的模型目录按场景切分
// （chat / developer / assistant / inline / quest / qwork / experts / qwake …），
// 同一账号在不同场景下可见的模型集合并不相同（2026-10-25 本机客户端目录样本：
// chat 15 个、qwork 13 个且不含 mmodel、experts 仅 5 个）。只读单个场景时，
// 任何"只出现在其它场景"的模型都对用户不可见。推理链路直接打
// agent_chat_generation，与场景无关，并集不会引入不可用的模型。
// [参数] sa：用于签名与鉴权的账号凭据
// [返回] 动态模型列表；请求或解析失败时返回错误，由调用方按优先级链回退配置 / 静态默认。
// 最近修改时间 2026-10-25（改为全场景并集，并保留上游 display_name 供对外命名与回译）
func callModelsAPI(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := newModelsAPIRequest(ctx, sa, endpointModels)
	if err != nil {
		return nil, err
	}
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("[qoderwork] callModelsAPI: upstream status %d, body: %s", resp.StatusCode, truncateRedacted(string(resp.Body), 200))
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	// Response is plain JSON: {"chat":[{key,display_name,...}], "developer":[...], ...}
	return parseModelsResponse(resp.Body)
}

// parseModelsResponse 把 model/list 的原始响应解析为模型条目并集。
//
// 抽成纯函数是为了让「多场景合并 / 去重 / 脏场景跳过」这几条判定可被单测
// 直接覆盖：callModelsAPI 自身依赖宿主 HTTP 桥与 COSY 签名，在 shim 环境
// 无法驱动，若把解析内联在里面，删掉合并逻辑不会有任何用例失败。
// [参数] body：上游响应原始字节
// [返回] 去重后的模型列表；无可用模型时返回错误
// 最近修改时间 2026-10-25（从 callModelsAPI 抽出，支持多场景并集测试）
func parseModelsResponse(body []byte) ([]pluginapi.ModelInfo, error) {
	var apiResp map[string]json.RawMessage
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	// 按场景名排序合并，保证同一账号多次发现的模型顺序稳定。
	scenes := make([]string, 0, len(apiResp))
	for scene := range apiResp {
		scenes = append(scenes, scene)
	}
	sort.Strings(scenes)
	out := make([]pluginapi.ModelInfo, 0, 32)
	seen := make(map[string]struct{}, 32)
	for _, scene := range scenes {
		raw := apiResp[scene]
		if len(raw) == 0 || raw[0] != '[' {
			// 非模型数组的场景（字符串 / 对象）直接跳过，避免一次脏数据
			// 让整个发现失败并连带开启负缓存。
			continue
		}
		var models []struct {
			Key            string  `json:"key"`
			DisplayName    string  `json:"display_name"`
			Enable         bool    `json:"enable"`
			IsReasoning    bool    `json:"is_reasoning"`
			IsVL           bool    `json:"is_vl"`
			MaxInputTokens int64   `json:"max_input_tokens"`
			PriceFactor    float64 `json:"price_factor"`
		}
		if err := json.Unmarshal(raw, &models); err != nil {
			continue
		}
		for _, m := range models {
			if !m.Enable {
				continue
			}
			key := strings.TrimSpace(m.Key)
			if key == "" {
				continue
			}
			// 并集去重：同一代号在多个场景重复出现时只保留首个（场景名有序，
			// 结果可复现），否则同一模型会注册出重复条目。
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			ctxLen := int64(180000)
			if m.MaxInputTokens > 0 {
				ctxLen = m.MaxInputTokens
			}
			// 缓存同时保留内部代号（ID）与上游官方名（DisplayName）：对外命名
			// 与请求回译都以上游官方名为准，硬编码表只是兜底。
			out = append(out, pluginapi.ModelInfo{
				ID:            key,
				Name:          modelDisplayName(key),
				DisplayName:   strings.TrimSpace(m.DisplayName),
				ContextLength: ctxLen,
				MaxCompletionTokens: func() int64 {
					spec := resolveOfficialModelSpec(key)
					if spec.ReasoningMax > 0 {
						return applyMaxTokensFloor(spec.ReasoningMax)
					}
					return applyMaxTokensFloor(spec.MaxOutputTokens)
				}(),
				OwnedBy:                    providerName,
				SupportedGenerationMethods: []string{"chat"},
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no enabled models in any scene")
	}
	// 登记「官方名 → 内部代号」：上游改名后客户端会按新名请求，
	// 没有这层映射 cpaToUpstreamKey 会把新名透传给上游并静默落到 auto。
	registerUpstreamModelNames(out)
	log.Printf("[qoderwork] callModelsAPI: dynamic discovery ok: %d models in %d scenes", len(out), len(scenes))
	return out, nil
}

func cacheModelAliases(host pluginapi.HostConfigSummary) {
	entries := host.OAuthModelAlias[providerName]
	if len(entries) == 0 {
		// Host may key the channel case-insensitively; fall back to a scan.
		for channel, list := range host.OAuthModelAlias {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				entries = list
				break
			}
		}
	}
	byAlias := make(map[string]string, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		alias := strings.TrimSpace(e.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		byAlias[strings.ToLower(alias)] = name
	}
	modelAliasCache.Lock()
	modelAliasCache.byAlias = byAlias
	modelAliasCache.Unlock()
}

// defaultModelAliasMap provides canonical mapping from client-facing model
// names to upstream Qoder gateway model keys.
var defaultModelAliasMap = map[string]string{
	// Cantus
	"cantus": "cmodel",
	// Qwen 3.8
	"qwen3.8-max":         "qmodel_38max",
	"qwen3.8-max-preview": "qmodel_38max",
	"qmodel_preview":      "qmodel_38max",
	"qwen3.8-flash":       "qfmodel",
	"qwen3.6-flash":       "qfmodel",
	"q36fmodel":           "qfmodel",
	// Qwen 3.7
	"qwen3.7-max":   "qmodel_latest",
	"qwen3.7-plus":  "qmodel",
	"qwen3.7-flash": "q37fmodel",
	// GLM 5.3 / 5.2
	"glm-5.3":       "gmodel",
	"glm-5.2":       "gm51model",
	"gm51model":     "gm51model",
	"glm-5.3-flash": "gfmodel",
	// Kimi
	"kimi-k3":           "kmodel_latest",
	"kimi-k2.8-preview": "kmodel",
	"kimi-k2.7-code":    "kmodel",
	// DeepSeek
	"deepseek-v4-pro":   "dmodel",
	"deepseek-flash":    "dfmodel",
	"deepseek-v4-flash": "dfmodel",
	// MiniMax
	"minimax-m3":   "mmodel",
	"minimax-m2.7": "mmodel",
}

// resolveUpstreamModel maps an aliased requested model back to the real
// upstream model ID. Returns the input unchanged when nothing matches.
//
// 优先级：账号级 alias > 宿主 oauth-model-alias > 运行时上游官方名 > 静态别名表。
// 用户显式配置的 alias 必须压倒自动映射；运行时上游官方名排在静态表之前，
// 因为静态表在上游改名后只会给出旧名。
// [参数] model：客户端请求的模型名；attributes：账号级 alias 配置
// [返回] 上游内部代号；全部未命中时返回原值
// 最近修改时间 2026-10-25（接入上游官方名运行时映射）
func resolveUpstreamModel(model string, attributes map[string]string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return model
	}
	key := strings.ToLower(m)
	if name, ok := parseModelAliasAttribute(attributes)[key]; ok {
		return name
	}
	modelAliasCache.RLock()
	name, ok := modelAliasCache.byAlias[key]
	modelAliasCache.RUnlock()
	if ok {
		return name
	}
	if upstream, ok := lookupUpstreamKey(m); ok {
		return upstream
	}
	if canonical, ok := defaultModelAliasMap[key]; ok {
		return canonical
	}
	return m
}

// parseModelAliasAttribute decodes a per-auth alias override from auth
// attributes. Accepts JSON ([{"name":...,"alias":...}] or {alias:name}) or
// comma-separated "alias=name" pairs.
func parseModelAliasAttribute(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	raw := ""
	for _, k := range []string{"model_alias", "model-alias", "oauth-model-alias"} {
		if v := strings.TrimSpace(attributes[k]); v != "" {
			raw = v
			break
		}
	}
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	add := func(name, alias string) {
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" && alias != "" && !strings.EqualFold(name, alias) {
			out[strings.ToLower(alias)] = name
		}
	}
	if strings.HasPrefix(raw, "[") {
		var list []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, e := range list {
				add(e.Name, e.Alias)
			}
			return out
		}
	}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for alias, name := range m {
				add(name, alias)
			}
			return out
		}
	}
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			add(kv[1], kv[0])
		}
	}
	return out
}

// filterExcludedModels removes models listed in oauth-excluded-models for
// the qoderwork provider. The host passes this config via HostConfigSummary.
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
	// Try exact provider match, then case-insensitive scan.
	excluded := host.ExcludedModels[providerName]
	if len(excluded) == 0 {
		for channel, list := range host.ExcludedModels {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				excluded = list
				break
			}
		}
	}
	if len(excluded) == 0 {
		return models
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		excludeSet[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	// ID 已带 provider 前缀且主条目用官方名，排除规则可能是官方名、内部代号或
	// 完整 ID 任一形态，三者都要能命中，否则清空/配置排除项的行为会随 ID
	// 改写而静默失效。
	isExcluded := func(m pluginapi.ModelInfo) bool {
		candidates := []string{m.ID, modelDisplayName(m.ID)}
		if m.Name != "" {
			candidates = append(candidates, m.Name)
		}
		for _, c := range candidates {
			if _, skip := excludeSet[strings.ToLower(strings.TrimSpace(c))]; skip {
				return true
			}
		}
		return false
	}
	// Use a fresh slice — models[:0] would alias the input's backing array,
	// which may be the dynamicModelsCache's own slice. Mutating it in place
	// would corrupt the cache for subsequent callers (P0 bug: after one
	// filterExcludedModels call, cache returns the filtered list as the
	// "full" list on the next fetch).
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if isExcluded(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// publishUsage reports one upstream attempt into CPAMP request monitoring.
// requestedModel is client-facing (may be alias); upstreamModel is resolved.

// handleModelStatic 返回宿主要求的全局模型列表，优先级为动态 > 配置 > 静态默认。
// 动态发现仅能命中已有缓存（StaticModelRequest 不带账号凭据），缓存未命中时
// fetchDynamicModels 会扫描已注册凭据；都失败则回退配置 / 静态默认。
// [参数] raw：宿主传入的 StaticModelRequest
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-09-12（由"配置非空即不查动态"反转为动态优先）
func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := resolveModels(fetchDynamicModels(), getConfiguredModels(), wbModels())
	models = exposeModelIDs(models)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// handleModelForAuth 返回指定账号的模型列表，优先级为动态 > 配置 > 静态默认。
// [参数] raw：宿主传入的 AuthModelRequest（含 StorageJSON 凭据）
// [返回] 成功 envelope；请求解析失败时返回错误
// 最近修改时间 2026-10-20 03:10:00 修改, 把 AuthID 透传给分片缓存
func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Always return the plugin's canonical provider key. The host skips any
	// response whose Provider doesn't match the auth's provider, so echoing
	// req.AuthProvider back would silently drop the model list whenever the
	// auth file carries a non-canonical provider string.
	cacheModelAliases(req.Host)
	models := resolveModelsFromStorage(req.AuthID, req.StorageJSON)
	models = exposeModelIDs(models)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// authModelsForIndex 返回指定账号当前可用的模型列表，供面板「测试」弹窗选择。
//
// 复用账号级动态发现链路：优先该账号未过期缓存，未命中时用该账号凭据实时拉取；
// 末尾追加随机兜底模型，保证上游瞬时失败时弹窗仍有一个可测选项。
// [参数] authIndex：宿主账号索引。
// [返回] 去重后的模型 ID 列表；账号凭据不可用时返回 nil。
// 最近修改时间 2026-10-20 03:10:00 修改, 改为按账号读取缓存，避免拿到别的账号清单
func authModelsForIndex(authIndex string) []string {
	sa, err := hostAuthGet(authIndex)
	if err != nil || sa == nil {
		return nil
	}
	// 缓存 key 用本账号凭据的 uid：hostAuthGet 已按 auth_index 取到具体账号，
	// 不能用 authIndex 当 key（它与宿主 auth.ID 不同形，会命中不到分片）。
	accountKey := sa.Account.UID
	models, _ := cachedDynamicModels(accountKey)
	if len(models) == 0 && strings.TrimSpace(sa.Auth.AccessToken) != "" {
		if dyn, ferr := callModelsAPI(sa); ferr == nil && len(dyn) > 0 {
			storeDynamicModels(accountKey, dyn)
			models = dyn
		} else if ferr != nil {
			log.Printf("[qoderwork] authModelsForIndex: dynamic discovery failed (%v), falling back to static list", ferr)
		}
	}
	if len(models) == 0 {
		models = resolveModels(nil, getConfiguredModels(), wbModels())
	}
	models = exposeModelIDs(models)
	seen := make(map[string]struct{}, len(models)+1)
	out := make([]string, 0, len(models)+1)
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if fallback := pickRandomQoderModel(sa); fallback != "" {
		if _, ok := seen[fallback]; !ok {
			out = append(out, fallback)
		}
	}
	return out
}

// handleModelsQuery 返回指定账号当前可用模型列表，供面板「测试」弹窗选择。
//
// [参数] req：管理请求，query 参数 auth_index 指定账号。
// [返回] {models:[...]}；缺少 auth_index 时返回 {error}。
// 最近修改时间 2026-10-10（新增测试按钮指定模型选择支持，同步自 workbuddy）
func handleModelsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	if authIndex == "" {
		return map[string]any{"error": "auth_index is required"}
	}
	return map[string]any{"models": authModelsForIndex(authIndex)}
}
