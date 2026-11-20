package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现扣子模型目录的动态发现。
//
// 模型清单有两个上游来源，职责不同：
//   - POST /v1/model/list：按 model_type 分类的权威目录（llm / image_generation /
//     video_generation / tts / asr / embedding），对话模型只在这里。
//   - POST /api/coze_claw/resource/get_multimodal_model_catalog：能力分组目录，
//     生视频的完整模型族（Seedance 2.5 / 2.0 / MiniMax H3）只在这里。
//
// 两者都不需要任何项目上下文，带会话 Cookie 即可调用，因此模型发现不依赖
// CYCLE-01 的对话链路。

const (
	// modelTypeLLM 等是 /v1/model/list 的 model_type 枚举值。
	//
	// 必须是字符串：传数字返回 InvalidRequest（2026-10-18 实测）。
	modelTypeLLM   = "llm"
	modelTypeImage = "image_generation"
	modelTypeVideo = "video_generation"
	modelTypeTTS   = "tts"
	modelTypeASR   = "asr"
	modelTypeEmbed = "embedding"

	// modelCatalogTTL 是模型目录的缓存有效期。
	//
	// 扣子模型清单随账号等级与灰度变化，但变化不频繁；10 分钟足以避免
	// 宿主每次轮询都打上游，又不会让用户等太久才看到新模型。
	modelCatalogTTL = 10 * time.Minute

	// modelCatalogPageSize 是单次拉取的条数上限。
	modelCatalogPageSize = 200

	// modelDiscoverTimeout 是一次完整发现的单请求超时。
	//
	// 发现要打 6 个类型端点 + 1 个多模态目录，串行时最坏会拖到 7 倍超时；
	// 宿主在注册模型与打开按账号模型弹窗时都会等这次调用，因此必须并发，
	// 且单请求超时要短于面板可接受的等待。
	modelDiscoverTimeout = 8 * time.Second

	// modelIDPrefix 是对外模型名的前缀。
	//
	// 宿主按 provider 聚合多个插件的模型，裸名容易与其他服务商的同名模型冲突。
	modelIDPrefix = "coze/"

	// openAIImageModelType 是宿主图片端点白名单认的模型类型。
	//
	// 必须与宿主 internal/registry 的 OpenAIImageModelType 字面量一致：
	// POST /v1/images/generations 只接受 Type == "openai-image" 的模型，
	// 填 "image" 会被 rejectUnsupportedImagesModel 直接 400 拒绝。
	openAIImageModelType = "openai-image"

	// referenceCreated 是目录条目的固定创建时间。
	//
	// 用固定值而非 time.Now()：该字段参与客户端缓存判定，每次轮询都变化会让
	// 客户端误以为模型目录在不停更新。
	referenceCreated = 1767225600 // 2026-01-01T00:00:00Z
)

// modelListType 是一条 (model_type, 对外 Type) 映射。
//
// 上游的 model_type 与宿主 ModelInfo.Type 不是同一套词汇：宿主只认
// chat / image / video / embedding 等少数值，因此需要显式映射表。
type modelListType struct {
	// Upstream 是上游 model_type 枚举值。
	Upstream string
	// Host 是宿主 ModelInfo.Type 取值。
	Host string
}

// modelListTypes 是逐类型探测清单。
//
// 顺序决定输出顺序：对话模型在前，生成类在后，与用户在面板里的使用频率一致。
var modelListTypes = []modelListType{
	{Upstream: modelTypeLLM, Host: "chat"},
	{Upstream: modelTypeImage, Host: openAIImageModelType},
	{Upstream: modelTypeVideo, Host: "video"},
	{Upstream: modelTypeEmbed, Host: "embedding"},
	{Upstream: modelTypeTTS, Host: "audio"},
	{Upstream: modelTypeASR, Host: "audio"},
}

// upstreamModel 是 /v1/model/list 返回的单条模型。
type upstreamModel struct {
	ModelID     string `json:"model_id"`
	ModelName   string `json:"model_name"`
	ShowName    string `json:"show_name"`
	ModelDesc   string `json:"model_desc"`
	ModelType   string `json:"model_type"`
	ModelFamily int    `json:"model_family"`
	IsOffline   bool   `json:"is_offline"`
	InputTypes  []int  `json:"input_types"`
	OutputTypes []int  `json:"output_types"`
	ModelTags   []struct {
		TagName string `json:"tag_name"`
	} `json:"model_tag_list"`
}

// modelListResponse 是 /v1/model/list 的响应。
type modelListResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ModelInfoList []upstreamModel `json:"model_info_list"`
	} `json:"data"`
}

// multimodalCatalog 是多模态目录响应。
type multimodalCatalog struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Capabilities []struct {
			CapabilityType   string `json:"capability_type"`
			DisplayName      string `json:"display_name"`
			DefaultModelName string `json:"default_model_name"`
			Models           []struct {
				ModelName    string `json:"model_name"`
				DisplayName  string `json:"display_name"`
				Description  string `json:"description"`
				UserLevelGte int    `json:"user_level_gte"`
			} `json:"models"`
		} `json:"capabilities"`
	} `json:"data"`
}

// modelCatalogCache 缓存各账号的模型目录。
//
// 按 AuthID 分桶而不是单条缓存：多账号下宿主会逐个账号调用 model.for_auth，
// 单条缓存会让它们互相覆盖，表现为「每次调用都重新打上游」。
type modelCatalogCache struct {
	mu      sync.Mutex
	entries map[string]modelCatalogEntry
}

type modelCatalogEntry struct {
	models  []pluginapi.ModelInfo
	fetched time.Time
}

var catalogCache = modelCatalogCache{entries: map[string]modelCatalogEntry{}}

// modelListOverride / multimodalOverride 是端点的测试覆盖点；非空时优先使用。
//
// 生产路径下始终为空，端点由 protocol.go 的常量拼装。
var (
	modelListOverride  string
	multimodalOverride string
)

// resolveModelListURL 返回实际使用的模型清单端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-10-18；改动原因：新增，测试覆盖入口。
func resolveModelListURL() string {
	if modelListOverride != "" {
		return modelListOverride
	}
	return modelListURL()
}

// resolveMultimodalURL 返回实际使用的多模态目录端点。
// [参数] 无。
// [返回] 端点 URL。
// 最近修改时间：2026-10-18；改动原因：新增，测试覆盖入口。
func resolveMultimodalURL() string {
	if multimodalOverride != "" {
		return multimodalOverride
	}
	return multimodalCatalogURL()
}

// fetchModelList 拉取单个 model_type 的模型清单。
// [参数] client: HTTP 客户端；cookieHeader: 会话 Cookie；modelType: 上游枚举值。
// [返回] 模型清单与错误。
// 最近修改时间：2026-10-18；改动原因：新增，逐类型拉取。
func fetchModelList(client httpDoer, cookieHeader, modelType string) ([]upstreamModel, error) {
	payload, _ := json.Marshal(map[string]any{
		"model_type": modelType,
		"page_size":  modelCatalogPageSize,
	})
	req, errReq := http.NewRequest(http.MethodPost, resolveModelListURL(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, fmt.Errorf("构造模型目录请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", tokenBeatContentType)
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", cozeSiteHost+"/")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("模型目录请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取模型目录响应失败：%w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("模型目录端点返回 HTTP %d", resp.StatusCode)
	}

	var parsed modelListResponse
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("模型目录响应解析失败：%w", errUnmarshal)
	}
	// 该端点成功时 code 为 0，失败时为业务码（如 700012006 登录校验不合法）。
	if parsed.Code != 0 {
		return nil, fmt.Errorf("模型目录返回 code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return parsed.Data.ModelInfoList, nil
}

// fetchMultimodalCatalog 拉取多模态能力目录。
// [参数] client: HTTP 客户端；cookieHeader: 会话 Cookie。
// [返回] 目录与错误。
// 最近修改时间：2026-10-18；改动原因：新增，生视频模型来源。
func fetchMultimodalCatalog(client httpDoer, cookieHeader string) (*multimodalCatalog, error) {
	req, errReq := http.NewRequest(http.MethodPost, resolveMultimodalURL(), bytes.NewReader([]byte("{}")))
	if errReq != nil {
		return nil, fmt.Errorf("构造多模态目录请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", tokenBeatContentType)
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Referer", cozeSiteHost+"/")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("多模态目录请求失败：%w", errDo)
	}
	defer resp.Body.Close()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取多模态目录响应失败：%w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("多模态目录端点返回 HTTP %d", resp.StatusCode)
	}

	var parsed multimodalCatalog
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("多模态目录响应解析失败：%w", errUnmarshal)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("多模态目录返回 code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return &parsed, nil
}

// modelInfoFromUpstream 把上游模型转成宿主模型结构。
// [参数] m: 上游模型；hostType: 宿主 Type。
// [返回] 宿主模型结构。
// 最近修改时间：2026-10-18；改动原因：新增，字段映射。
func modelInfoFromUpstream(m upstreamModel, hostType string) pluginapi.ModelInfo {
	display := strings.TrimSpace(m.ShowName)
	if display == "" {
		display = strings.TrimSpace(m.ModelName)
	}
	name := strings.TrimSpace(m.ModelName)
	if name == "" {
		name = m.ModelID
	}
	desc := strings.TrimSpace(m.ModelDesc)
	if desc == "" {
		desc = strings.Join(modelTagNames(m), " / ")
	}
	return pluginapi.ModelInfo{
		ID:          modelIDPrefix + m.ModelID,
		Object:      "model",
		Created:     referenceCreated,
		OwnedBy:     providerName,
		Type:        hostType,
		DisplayName: display,
		Name:        name,
		Description: desc,
	}
}

// modelTagNames 提取模型标签名，用于描述兜底。
// [参数] m: 上游模型。
// [返回] 标签名列表。
// 最近修改时间：2026-10-18；改动原因：新增，描述兜底。
func modelTagNames(m upstreamModel) []string {
	out := make([]string, 0, len(m.ModelTags))
	for _, tag := range m.ModelTags {
		if name := strings.TrimSpace(tag.TagName); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// discoverModels 从上游拉取该账号的完整模型目录。
//
// 各类型端点并发拉取：单类型失败不阻断整体（某一类接口改版时，其余类别
// 仍应可用），全部失败才返回错误。并发而非串行是因为宿主在注册模型与
// 打开按账号模型弹窗时都会同步等待本调用，串行 7 次超时不可接受。
// [参数] sa: 账号。
// [返回] 模型列表与错误。
// 最近修改时间：2026-10-18；改动原因：新增，动态发现主链路；改为并发拉取。
func discoverModels(sa *StoredAuth) ([]pluginapi.ModelInfo, error) {
	if sa == nil || strings.TrimSpace(sa.CookieHeader) == "" {
		return nil, fmt.Errorf("账号凭据为空")
	}
	client := newUpstreamClient(modelDiscoverTimeout)

	type typeResult struct {
		models []upstreamModel
		err    error
	}
	results := make([]typeResult, len(modelListTypes))
	var catalog *multimodalCatalog
	var errCatalog error

	var wg sync.WaitGroup
	for i, entry := range modelListTypes {
		wg.Add(1)
		go func(slot int, modelType string) {
			defer wg.Done()
			models, errFetch := fetchModelList(client, sa.CookieHeader, modelType)
			results[slot] = typeResult{models: models, err: errFetch}
		}(i, entry.Upstream)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		catalog, errCatalog = fetchMultimodalCatalog(client, sa.CookieHeader)
	}()
	wg.Wait()

	var out []pluginapi.ModelInfo
	seen := make(map[string]struct{})
	var firstErr error
	okCount := 0

	for i, entry := range modelListTypes {
		res := results[i]
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		okCount++
		for _, m := range res.models {
			if strings.TrimSpace(m.ModelID) == "" {
				continue
			}
			// 已下线模型不登记：用户选中后会直接失败，不如不展示。
			if m.IsOffline {
				continue
			}
			id := modelIDPrefix + m.ModelID
			if _, exists := seen[strings.ToLower(id)]; exists {
				continue
			}
			seen[strings.ToLower(id)] = struct{}{}
			out = append(out, modelInfoFromUpstream(m, entry.Host))
		}
	}

	// 多模态目录补充 model_list 缺失的模型族（尤其是生视频）。
	if errCatalog == nil {
		okCount++
		for _, cap := range catalog.Data.Capabilities {
			hostType := hostTypeForCapability(cap.CapabilityType)
			if hostType == "" {
				continue
			}
			for _, m := range cap.Models {
				name := strings.TrimSpace(m.ModelName)
				if name == "" {
					continue
				}
				id := modelIDPrefix + name
				if _, exists := seen[strings.ToLower(id)]; exists {
					continue
				}
				seen[strings.ToLower(id)] = struct{}{}
				display := strings.TrimSpace(m.DisplayName)
				if display == "" {
					display = name
				}
				out = append(out, pluginapi.ModelInfo{
					ID:          id,
					Object:      "model",
					Created:     referenceCreated,
					OwnedBy:     providerName,
					Type:        hostType,
					DisplayName: display,
					Name:        name,
					Description: strings.TrimSpace(m.Description),
				})
			}
		}
	} else if firstErr == nil {
		firstErr = errCatalog
	}

	if okCount == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("所有模型目录端点均失败")
		}
		return nil, firstErr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("上游未返回任何可用模型")
	}
	return out, nil
}

// hostTypeForCapability 把多模态能力类型映射为宿主 Type。
// [参数] capability: 上游能力类型（image_gen / generate_video）。
// [返回] 宿主 Type；未知能力返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，能力类型映射。
func hostTypeForCapability(capability string) string {
	switch strings.ToLower(strings.TrimSpace(capability)) {
	case "image_gen":
		return openAIImageModelType
	case "generate_video":
		return "video"
	default:
		return ""
	}
}

// cachedDiscoveredModels 返回该账号的模型目录，命中缓存时不打上游。
// [参数] sa: 账号；force: 为真时跳过缓存。
// [返回] 模型列表与错误。
// 最近修改时间：2026-10-18；改动原因：新增，带缓存的发现入口；缓存改为按账号分桶。
func cachedDiscoveredModels(sa *StoredAuth, force bool) ([]pluginapi.ModelInfo, error) {
	if sa == nil {
		return nil, fmt.Errorf("账号为空")
	}
	key := sa.AuthID
	if key == "" {
		key = sa.SecUserID
	}
	catalogCache.mu.Lock()
	if entry, ok := catalogCache.entries[key]; ok && !force && len(entry.models) > 0 &&
		time.Since(entry.fetched) < modelCatalogTTL {
		cached := entry.models
		catalogCache.mu.Unlock()
		return cached, nil
	}
	catalogCache.mu.Unlock()

	models, errDiscover := discoverModels(sa)
	if errDiscover != nil {
		return nil, errDiscover
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Type != models[j].Type {
			return modelTypeRank(models[i].Type) < modelTypeRank(models[j].Type)
		}
		return models[i].ID < models[j].ID
	})

	catalogCache.mu.Lock()
	catalogCache.entries[key] = modelCatalogEntry{models: models, fetched: time.Now()}
	catalogCache.mu.Unlock()
	return models, nil
}

// modelTypeRank 给宿主 Type 排序权重，让对话模型排在生成类之前。
// [参数] hostType: 宿主 Type。
// [返回] 排序权重，越小越靠前。
// 最近修改时间：2026-10-18；改动原因：新增，目录排序。
func modelTypeRank(hostType string) int {
	switch hostType {
	case "chat":
		return 0
	case openAIImageModelType:
		return 1
	case "video":
		return 2
	case "embedding":
		return 3
	default:
		return 4
	}
}

// resetModelCatalogCache 清空模型目录缓存（测试与凭据变更时使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-18；改动原因：新增，缓存失效入口；改为清空分桶表。
func resetModelCatalogCache() {
	catalogCache.mu.Lock()
	catalogCache.entries = map[string]modelCatalogEntry{}
	catalogCache.mu.Unlock()
}

// staticFallbackModels 返回没有账号可探测时对外暴露的模型目录。
//
// 宿主在启动期就会调用 model.register，此时可能一个账号都没有。目录不能为空，
// 否则用户看不到任何模型；这里只列上游**当前在役**的默认模型，等真实账号
// 接入后由 model.for_auth 用动态目录覆盖。
//
// 种子必须与上游现状同步：上游下线的模型一旦留在种子里，就会出现在全局模型
// 目录中却永远拿不到可用账号，用户选中后只能得到 503。2026-11-20 实测
// /v1/model/list?model_type=llm 仅返回 turbo-260628 / lite / mini 三条，
// 原旗舰 doubao-seed-2-0-pro-260215 已被上游下线，故以 turbo 顶替。
// [参数] 无。
// [返回] 兜底模型列表。
// 最近修改时间：2026-11-20；改动原因：pro-260215 被上游下线，旗舰位换为现役 turbo-260628。
func staticFallbackModels() []pluginapi.ModelInfo {
	seeds := []struct {
		id, display, desc, hostType string
	}{
		{"doubao-seed-2-1-turbo-260628", "Doubao-Seed-2.1-turbo", "旗舰级通用模型，擅长复杂推理与长链路任务。", "chat"},
		{"doubao-seed-2-0-lite-260215", "Doubao-Seed-2.0-lite", "轻量通用模型，兼顾速度与质量。", "chat"},
		{"doubao-seed-2-0-mini-260215", "Doubao-Seed-2.0-mini", "超轻量模型，响应最快。", "chat"},
		{"doubao-seedream-5-0-260128", "Doubao-Seedream-5.0-lite", "图像生成模型。", openAIImageModelType},
		{"doubao-seedance-2-0-260128", "Doubao-Seedance-2.0", "视频生成模型。", "video"},
	}
	out := make([]pluginapi.ModelInfo, 0, len(seeds))
	for _, s := range seeds {
		out = append(out, pluginapi.ModelInfo{
			ID:          modelIDPrefix + s.id,
			Object:      "model",
			Created:     referenceCreated,
			OwnedBy:     providerName,
			Type:        s.hostType,
			DisplayName: s.display,
			Name:        s.id,
			Description: s.desc,
		})
	}
	return out
}

// modelStatic 响应 model.register / model.static。
//
// 宿主在启动期同步调用本方法，因此这里**不做网络探测**：一旦上游慢或不可达，
// 会把宿主启动拖住数秒。启动期只返回内置种子目录，账号级真实目录由
// model.for_auth 提供（那条路径本来就是按账号、按需调用）。
// [参数] request: 宿主请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-18；改动原因：新增，静态目录入口；去掉启动期网络请求。
func modelStatic(request []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   filterExcludedModels(staticFallbackModels(), req.Host),
	})
}

// modelForAuth 响应 model.for_auth。
//
// 按账号返回该账号真实可见的模型目录；探测失败时回落到静态种子，
// 并在目录里保留可用项——一次网络失败不该让用户没模型可选。
// [参数] request: 宿主请求体。
// [返回] 信封 JSON 与错误。
// 最近修改时间：2026-10-18；改动原因：新增，按账号动态目录。
func modelForAuth(request []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	models := staticFallbackModels()
	if sa, errAuth := authForModelRequest(&req); errAuth == nil {
		if discovered, errDiscover := cachedDiscoveredModels(sa, false); errDiscover == nil && len(discovered) > 0 {
			models = discovered
		}
	}
	return okEnvelope(pluginapi.ModelResponse{
		Provider: providerName,
		Models:   filterExcludedModels(models, req.Host),
	})
}

// filterExcludedModels 剔除宿主 oauth-excluded-models 中为本插件列出的模型。
//
// 宿主只在 model.for_auth 的按账号路径上替插件过滤；model.static 的返回会被
// 原样登记进全局模型目录，因此插件必须在出口自行过滤——这是同名模型被多个
// provider 同时认领时唯一能把模型从本服务商摘掉的入口。
// [参数] models: 已解析的模型列表；host: 宿主配置摘要。
// [返回] 过滤后的列表；未配置排除项时原样返回入参。
// 最近修改时间：2026-10-18；改动原因：新增，与既有插件保持一致的排除语义。
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
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

	// 返回新切片而非就地压缩：入参可能来自共享缓存，就地写回会把缓存
	// 永久变成「已过滤列表」。
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// authForModelRequest 从宿主请求中解析出要查询模型的账号。
//
// 宿主在 model.for_auth 里下发该账号的凭据快照；没有快照时按 AuthID 回落。
// 解析不出来不算错误路径——调用方会退回静态种子。
// [参数] req: 宿主按账号请求。
// [返回] 账号与错误。
// 最近修改时间：2026-10-18；改动原因：新增，模型发现的账号解析。
func authForModelRequest(req *pluginapi.AuthModelRequest) (*StoredAuth, error) {
	if req == nil {
		return nil, fmt.Errorf("请求为空")
	}
	if len(req.StorageJSON) > 0 {
		var sa StoredAuth
		if errUnmarshal := json.Unmarshal(req.StorageJSON, &sa); errUnmarshal == nil && sa.CookieHeader != "" {
			if sa.AuthID == "" {
				sa.AuthID = strings.TrimSuffix(req.AuthID, ".json")
			}
			return &sa, nil
		}
	}
	authID := strings.TrimSuffix(strings.TrimSpace(req.AuthID), ".json")
	if authID == "" {
		return nil, fmt.Errorf("缺少账号标识")
	}
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, errList
	}
	for _, account := range accounts {
		if account.AuthID == authID {
			return account, nil
		}
	}
	return nil, fmt.Errorf("账号不存在：%s", authID)
}
