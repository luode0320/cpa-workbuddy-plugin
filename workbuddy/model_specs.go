// model_specs.go 提供统一的输出上限策略与 max_tokens 自适应计算逻辑。
//
// 2026-10-30 起不再维护逐模型官方规格表：所有模型（含上游动态发现的模型）
// 一律按「推理模式 128k / 非推理模式 64k」统一取值。逐模型表的问题是它会随
// 上游新增模型产生遗漏——未收录模型落到保守兜底值，长总结与上下文压缩
// 场景被提前截断，且用户无法感知。
package main

// 统一输出上限：所有模型共用，不再按模型区分。
const (
	// minMaxTokensFloor 是所有模型对外输出的统一最低 max_tokens 底线。
	minMaxTokensFloor int64 = 32768
	// defaultMaxOutputTokens 是非推理模式下的统一输出上限。
	defaultMaxOutputTokens int64 = 64000
	// defaultReasoningMaxTokens 是推理/思考模式下的统一输出上限。
	defaultReasoningMaxTokens int64 = 128000
	// defaultContextWindow 是模型元信息里 ContextLength 的兜底值。
	defaultContextWindow int64 = 1048576
)

// applyMaxTokensFloor 把低于统一底线的 max_tokens 抬升至底线。
// [参数] v: 待校验的 max_tokens 值
// [返回] 不低于 32768 的值
// 最近修改时间：2026-10-13 15:00:00；新增统一最低输出底线。
func applyMaxTokensFloor(v int64) int64 {
	if v < minMaxTokensFloor {
		return minMaxTokensFloor
	}
	return v
}

// ModelOfficialSpec 定义模型规格参数。
//
// 2026-10-30 起所有模型三个字段取值相同：ContextWindow 为
// defaultContextWindow，MaxOutputTokens 为 defaultMaxOutputTokens，
// ReasoningMax 为 defaultReasoningMaxTokens。保留结构体是为了让调用方
// （模型注册、自适应计算）无需改动。
type ModelOfficialSpec struct {
	ContextWindow   int64 // 上下文长度（仅用于模型元信息展示）
	MaxOutputTokens int64 // 非推理模式下的输出上限
	ReasoningMax    int64 // 推理模式下的输出上限
}

// lookupOfficialModelSpec 返回统一规格。
//
// 第二个返回值保留历史语义「是否命中已知规则库」；由于所有模型（含动态
// 发现）现在共用同一套上限，恒返回 true，调用方无需再区分已收录与未收录。
// [参数] model: 模型 ID 或展示名（不再参与取值，保留以兼容调用点）
// [返回] 统一规格与恒为 true 的命中标记
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限，删除逐模型规格表。
func lookupOfficialModelSpec(model string) (ModelOfficialSpec, bool) {
	return ModelOfficialSpec{
		ContextWindow:   defaultContextWindow,
		MaxOutputTokens: defaultMaxOutputTokens,
		ReasoningMax:    defaultReasoningMaxTokens,
	}, true
}

// resolveOfficialModelSpec 根据模型名称解析其规格。
// [参数] model: 模型 ID 或展示名（不再参与取值）
// [返回] 统一规格
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限。
func resolveOfficialModelSpec(model string) ModelOfficialSpec {
	spec, _ := lookupOfficialModelSpec(model)
	return spec
}

// computeAdaptiveMaxTokens 按统一上限计算发往上游的 max_tokens。
//
// 推理模式上限 128k，非推理模式上限 64k：客户端未传入时直接补足到当前模式
// 的上限；推理模式下客户端传入保守小值（<=8192）时放宽；超出上限时截断。
// [参数] model: 请求模型名称（不再参与取值）；requested: 客户端传入的 token 数（<=0 表示未传）；isReasoning: 是否处于思考/推理模式
// [返回] 最终 max_tokens，不低于统一底线
// 最近修改时间：2026-10-30；改动原因：改为全局统一上限，不再按模型查表。
func computeAdaptiveMaxTokens(model string, requested int64, isReasoning bool) int64 {
	targetMax := defaultMaxOutputTokens
	if isReasoning {
		targetMax = defaultReasoningMaxTokens
	}

	// 1. 客户端未传入：补足为该模式下的统一上限
	if requested <= 0 {
		return applyMaxTokensFloor(targetMax)
	}

	// 2. 思考模式下，若客户端传入保守过小值（<=8192），自动放宽破除死锁
	if isReasoning && requested <= 8192 && targetMax > 8192 {
		return applyMaxTokensFloor(targetMax)
	}

	// 3. 客户端显式传入值超过了统一上限，截断避免被上游网关报 400
	if requested > targetMax {
		return applyMaxTokensFloor(targetMax)
	}

	// 4. 处于合理区间内，原样保留客户端传入值（最终不低于统一输出底线）
	return applyMaxTokensFloor(requested)
}
