package main

import (
	"context"
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

// 本文件实现上游模型目录的动态发现。
//
// 上游 /alice/basic/launch 的 model_list 返回 67 条记录，name 是内部代号
// （Aoe / anceba / ogt ...），model_type 是数字 ID。这些代号没有公开映射，
// 但它们是上游真正认得的模型标识：原样透传可以让用户访问账号实际拥有的模型，
// 而不是插件自造的四个能力别名。
//
// 能力别名（doubao/default、pro、image、video）继续保留：它们描述的是能力语义，
// 上游改名时仍然成立，且生图/生视频必须靠 skill 参数触发。两者并存。

const (
	// modelDiscoveryPath 是承载模型清单的业务端点。
	modelDiscoveryPath = "/alice/basic/launch"

	// modelDiscoveryTTL 是模型清单的缓存时长。
	//
	// 模型清单随账号套餐变化，但不会分钟级抖动；缓存 10 分钟既避免每次打开
	// 测试弹窗都打上游（豆包按账号限流），又能在用户换套餐后及时刷新。
	modelDiscoveryTTL = 10 * time.Minute

	// modelDiscoveryTimeout 是单次模型发现请求的超时。
	modelDiscoveryTimeout = 15 * time.Second
)

// upstreamModel 是上游 model_list 的一条记录。
type upstreamModel struct {
	// Name 是上游内部代号，也是 model_name 的取值。
	Name string `json:"name"`
	// ModelName 与 Name 通常相同；分开保留以免上游将来只填其中一个。
	ModelName string `json:"model_name"`
	// ModelType 是上游的数字模型 ID。
	ModelType int64 `json:"model_type"`
	// SupportPicGen 表示该模型支持生图开关。
	SupportPicGen bool `json:"support_pic_gen_switch"`
	// SupportWebSearch 表示该模型支持联网搜索开关。
	SupportWebSearch bool `json:"support_web_search_switch"`
	// Typewriter 表示该模型走打字机输出。
	Typewriter bool `json:"typewriter_switch"`
}

// modelDiscoveryCache 缓存一次发现结果，避免每个请求都打上游。
type modelDiscoveryCache struct {
	mu        sync.Mutex
	models    []pluginapi.ModelInfo
	fetchedAt time.Time
	authID    string
}

var modelCache modelDiscoveryCache

// launchResponse 是 /alice/basic/launch 的最小响应结构。
type launchResponse struct {
	Code int `json:"code"`
	Data struct {
		ModelList []upstreamModel `json:"model_list"`
	} `json:"data"`
}

// launchEndpointOverride 是模型发现端点的测试覆盖点；非空时优先使用。
var launchEndpointOverride string

// swapLaunchEndpoint 临时覆盖模型发现端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整端点 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-17；改动原因：新增，让动态发现可在 httptest 下验证。
func swapLaunchEndpoint(endpoint string) func() {
	prev := launchEndpointOverride
	launchEndpointOverride = endpoint
	return func() { launchEndpointOverride = prev }
}

// discoverUpstreamModels 拉取指定账号的上游模型清单。
//
// 失败时返回错误而不是静默回落：调用方据此决定是否退回静态能力别名，
// 这样用户能区分「上游确实没返回」与「插件压根没去拉」。
// [参数] sa: 账号（提供 Cookie 与设备标识）；timeout: 请求超时。
// [返回] 上游模型列表与错误。
// 最近修改时间：2026-10-17；改动原因：新增，动态模型发现内核。
func discoverUpstreamModels(sa *StoredAuth, timeout time.Duration) ([]upstreamModel, error) {
	if sa == nil {
		return nil, fmt.Errorf("账号为空")
	}
	if timeout <= 0 {
		timeout = modelDiscoveryTimeout
	}

	endpoint := launchEndpointOverride
	if endpoint == "" {
		endpoint = endpointURL(sa, modelDiscoveryPath, nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if errReq != nil {
		return nil, fmt.Errorf("构造模型发现请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "application/json")
	// 模型发现走 /alice/* 业务端点，与对话端点不同套，固定用网页端指纹。
	applyUpstreamHeaders(req, false)

	client, jar := newUpstreamClient(sa, timeout)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("请求模型清单失败：%w", errDo)
	}
	defer resp.Body.Close()
	defer func() {
		_ = jar.persist(sa)
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取模型清单失败：%w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("模型清单端点返回 HTTP %d", resp.StatusCode),
		}
	}

	var out launchResponse
	if errUnmarshal := json.Unmarshal(body, &out); errUnmarshal != nil {
		return nil, fmt.Errorf("模型清单解析失败：%w", errUnmarshal)
	}
	if out.Code != 0 {
		return nil, &upstreamError{
			Code:    out.Code,
			Message: fmt.Sprintf("模型清单端点返回业务错误 %d", out.Code),
		}
	}
	if len(out.Data.ModelList) == 0 {
		return nil, fmt.Errorf("上游没有返回任何模型")
	}
	return out.Data.ModelList, nil
}

// modelsFromUpstream 把上游模型记录转成宿主模型结构。
//
// 上游 name 可能重复（实测 67 条里 26 个 distinct，Aoe 出现 27 次但 model_type
// 各不相同）。同名不同 model_type 的条目对外只能暴露一个名字，因此按名字去重，
// 保留首个；真正决定上游行为的是名字而不是 model_type（后者未在请求体中消费）。
// [参数] models: 上游模型记录。
// [返回] 去重后的宿主模型列表，按名字排序。
// 最近修改时间：2026-10-17；改动原因：新增，上游记录转换与去重。
func modelsFromUpstream(models []upstreamModel) []pluginapi.ModelInfo {
	seen := make(map[string]struct{}, len(models))
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = strings.TrimSpace(m.ModelName)
		}
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, pluginapi.ModelInfo{
			ID:          modelIDPrefix + name,
			Object:      "model",
			Created:     referenceCreated,
			OwnedBy:     providerName,
			Type:        "chat",
			DisplayName: name,
			Name:        name,
			Description: upstreamModelDescription(name, m),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// upstreamModelDescription 为上游模型生成说明。
//
// 上游只给内部代号，没有可读名，编造中文名会误导用户；如实标注来源与能力开关
// 才能让人判断这是不是自己要找的模型。
// [参数] name: 上游模型代号；m: 上游记录。
// [返回] 说明文本。
// 最近修改时间：2026-10-17；改动原因：新增，透传模型的说明。
func upstreamModelDescription(name string, m upstreamModel) string {
	var caps []string
	if m.SupportPicGen {
		caps = append(caps, "生图")
	}
	if m.SupportWebSearch {
		caps = append(caps, "联网搜索")
	}
	if m.Typewriter {
		caps = append(caps, "打字机输出")
	}
	desc := fmt.Sprintf("上游模型代号 %s（model_type=%d）", name, m.ModelType)
	if len(caps) > 0 {
		desc += "，支持：" + strings.Join(caps, "、")
	}
	return desc + "。代号由豆包内部使用，无公开可读名。"
}

// cachedUpstreamModels 返回指定账号的上游模型清单，命中缓存时直接复用。
//
// 缓存按账号隔离：不同套餐的账号模型清单可能不同，跨账号复用会把 A 账号的
// 模型暴露给 B 账号。
// [参数] sa: 账号；force: true 表示忽略缓存强制刷新。
// [返回] 模型列表与错误。
// 最近修改时间：2026-10-17；改动原因：新增，按账号缓存的上游模型读取。
func cachedUpstreamModels(sa *StoredAuth, force bool) ([]pluginapi.ModelInfo, error) {
	if sa == nil {
		return nil, fmt.Errorf("账号为空")
	}

	modelCache.mu.Lock()
	if !force && modelCache.authID == sa.AuthID && len(modelCache.models) > 0 &&
		time.Since(modelCache.fetchedAt) < modelDiscoveryTTL {
		cached := modelCache.models
		modelCache.mu.Unlock()
		return cached, nil
	}
	modelCache.mu.Unlock()

	raw, errDiscover := discoverUpstreamModels(sa, modelDiscoveryTimeout)
	if errDiscover != nil {
		return nil, errDiscover
	}
	models := modelsFromUpstream(raw)

	modelCache.mu.Lock()
	modelCache.models = models
	modelCache.fetchedAt = time.Now()
	modelCache.authID = sa.AuthID
	modelCache.mu.Unlock()
	return models, nil
}

// resetModelCache 清空模型缓存（供测试与账号切换使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，缓存复位入口。
func resetModelCache() {
	modelCache.mu.Lock()
	modelCache.models = nil
	modelCache.fetchedAt = time.Time{}
	modelCache.authID = ""
	modelCache.mu.Unlock()
}
