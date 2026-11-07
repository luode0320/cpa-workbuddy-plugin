// model_specs.go 提供模型元信息兜底常量与输出上限归一化策略。
//
// 2026-11-07 按社区共识改造：删除全部自研 max_tokens 控制。
// 改造前存在四类自研控制，GitHub 七家 WorkBuddy 反代项目（workbuddy2api /
// workbuddy2api-panel / workbuddy2api-hub / workbuddy-manager /
// dsh-workbuddy-connect / dsh-connect-workbuddy / workbuddy-cliproxy）
// 无一采用：
//  1. 统一 minMaxTokensFloor=32768 最低输出底线；
//  2. defaultMaxOutputTokens / defaultReasoningMaxTokens=128000 统一上限；
//  3. 推理模式下把客户端 <=8192 的显式值放宽到 128000；
//  4. 对外广播的 MaxCompletionTokens 无条件抬至 128000。
//
// 删除依据（社区实测 + 本方生产事故）：
//   - 上游对越界 max_tokens 是「静默钳制」而非 400：linguo2625469 实测 16 个
//     CN 模型中 8 个被钳制，最狠者声称 1M 实际 32K，请求更大值不报错只截断；
//   - 思考与最终回答共享 output 预算（linguo2625469 internal/upstream/client.go
//     独立确认「思考与最终回答共享此预算，上游无独立思考上限字段」），
//     抬高 max_tokens 不是解决正文被思考吃光的杠杆；
//   - 本方 2026-11-07 生产事故：max_tokens=2000 被抬到 128000，实际生成
//     12000~22000 tokens、耗时 80~145 秒，被调用方判为超时/卡死。
//
// 保留项：defaultContextWindow 仅用于 ContextLength 元信息兜底（上游不给时
// 报 1M 高估），不影响请求。context_length 与 max_tokens 是两套独立预算——
// 社区实测 prompt 70000 + max_tokens 64000 可通过 100000 窗口。
package main

import "strings"

// defaultContextWindow 是模型元信息里 ContextLength 的兜底值。
// 上游未提供 contextLength 时使用；高估比低估安全（低估会让客户端误以为
// 上下文不够而提前截断历史）。
const defaultContextWindow int64 = 1048576

// gptMinMaxTokens 是 GPT 系上游接受的 max_tokens 下限。
// 社区两家独立实测：15 拒、16 过（code=11133 model_param_invalid），
// 同账号同 body 对照仅差该值；非 GPT 模型无此限制。
const gptMinMaxTokens int64 = 16

// normalizeMaxTokensInPlace 按社区共识归一化输出上限字段（原地修改）。
//
// 只做三件事，且只做这三件：
//  1. 别名翻译：max_completion_tokens 译为 max_tokens。上游只认 max_tokens，
//     别名透传会被忽略后回落上游默认上限（实测 32000）。显式 max_tokens 优先，
//     别名无论是否翻译一律删除；非正整数（0/null/负数/字符串）不翻译，
//     不把畸形值洗成合法值，交由上游按自己的规则拒绝。
//  2. GPT 系下限：gpt- 前缀模型且 max_tokens < 16 时抬至 16，避免 11133。
//  3. 其余一律不动：未传则不写该字段（由上游用自己的默认值）；显式传值原样
//     发给上游——不抬升、不按推理模式区分、不套统一地板、不按模型上限预截断
//     （上游对越界值静默钳制，预截断只会掩盖上游真实规格）。
//
// [参数] obj：反序列化后的请求对象 map
// [返回] 是否对 obj 产生了修改
func normalizeMaxTokensInPlace(obj map[string]any) bool {
	changed := false

	// 1. 别名翻译：显式 max_tokens 优先，别名一律删除
	if alias, has := obj["max_completion_tokens"]; has {
		delete(obj, "max_completion_tokens")
		changed = true
		if _, explicit := obj["max_tokens"]; !explicit {
			if v, ok := positiveIntValue(alias); ok {
				obj["max_tokens"] = v
			}
		}
	}

	// 2. GPT 系下限
	if v, ok := positiveIntValue(obj["max_tokens"]); ok && v < gptMinMaxTokens {
		model, _ := obj["model"].(string)
		if strings.Contains(strings.ToLower(model), "gpt-") {
			obj["max_tokens"] = gptMinMaxTokens
			changed = true
		}
	}

	return changed
}

// positiveIntValue 把 JSON 解出的数值严格转为正整数。
// 只接受整数语义的数值：float64 必须是整数值，int / int64 必须为正。
// 字符串、bool、nil、小数、非正数一律返回 ok=false —— 不做隐式解析，
// 避免把畸形输入洗成看似合法的 max_tokens。
// [参数] v：来自 json.Unmarshal 或手构造 map 的任意值
// [返回] 正整数值与是否可用
func positiveIntValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), n > 0
	case int64:
		return n, n > 0
	case float64:
		if n <= 0 || n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}
