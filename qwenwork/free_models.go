// free_models.go 维护 QwenWork 的免费模型集合与「账号×模型」路由判据。
//
// 上游 chat-modes catalog 用 price_factor=0 标记免费模型（实测仅
// minimax-m3.1-flash）；show_zero_price_factor 对所有模型均为 true，不能作判据。
// 0 积分账号仍可用免费模型，因此路由准入必须按请求模型判定，而非账号级一刀切。
package main

import "strings"

// defaultFreeModelIDs 是动态发现不可用时的免费模型兜底集合。
var defaultFreeModelIDs = []string{"minimax-m3.1-flash"}

var freeModelRegistry struct {
	ids        map[string]struct{}
	discovered bool
}

// setFreeModels 用动态发现结果替换免费模型集合。ids 为空表示上游未标记任何免费模型。
func setFreeModels(ids []string) {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" {
			m[id] = struct{}{}
		}
	}
	freeModelRegistry.ids = m
	freeModelRegistry.discovered = true
}

// isFreeModel 判定客户端模型名是否为免费模型。
// 动态发现成功时以其结果为准；未发现时回退静态兜底集合。
func isFreeModel(model string) bool {
	key := strings.ToLower(strings.TrimSpace(resolveUpstreamModel(model, nil)))
	if key == "" {
		return false
	}
	if freeModelRegistry.discovered {
		_, ok := freeModelRegistry.ids[key]
		return ok
	}
	for _, id := range defaultFreeModelIDs {
		if key == strings.ToLower(strings.TrimSpace(id)) {
			return true
		}
	}
	return false
}

// hasFreeModels 报告当前是否存在已知免费模型。
// 动态发现成功时以其结果为准；未发现时回退静态兜底集合。
func hasFreeModels() bool {
	if freeModelRegistry.discovered {
		return len(freeModelRegistry.ids) > 0
	}
	return len(defaultFreeModelIDs) > 0
}

// firstModelArg 取出变参里的模型名，未传时返回空串。
func firstModelArg(model []string) string {
	if len(model) == 0 {
		return ""
	}
	return strings.TrimSpace(model[0])
}

// accountUsableForModel 判定账号能否承载指定模型的请求。
// 免费模型跳过积分准入；付费模型沿用原「耗尽或低于安全红线即排除」判据。
// [参数] authID：账号标识；model：请求模型名。
// [返回] true 表示积分维度允许路由。
func accountUsableForModel(authID, model string) bool {
	if isFreeModel(model) {
		return true
	}
	_, lowOrExhausted := cachedCreditsScore(authID)
	return !lowOrExhausted
}
