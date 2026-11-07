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

import (
	"encoding/json"
	"strconv"
	"strings"
)

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

// effectiveMaxTokens 取客户端显式声明的输出上限。
//
// 优先级：显式 max_tokens > 别名 max_completion_tokens > 0（未传）。非正值一律
// 视为未传。这里刻意**不做**任何上限截断：trae 上游对越界值静默钳制而非报错，
// 预截断只会掩盖上游真实规格（见本文件顶部的社区共识说明）。
// 2026-11-07 新增此函数的原因：请求体此前只解析 max_tokens，新客户端只发
// max_completion_tokens 时该值被静默丢弃，等价于没设上限。
// [参数] req：已解析的客户端请求（可为 nil）
// [返回] 应发给上游的 max_tokens；0 表示客户端未声明
// 最近修改时间：2026-11-07 22:10:00；改动原因：补 max_completion_tokens 别名识别。
func effectiveMaxTokens(req *openAIRequest) int {
	if req == nil {
		return 0
	}
	if v := req.MaxTokens.Int(); v > 0 {
		return int(v)
	}
	if v := req.MaxCompletionTokens.Int(); v > 0 {
		return int(v)
	}
	return 0
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

// flexNumber 是一个容错的整数字段类型。
//
// 背景（2026-11-08 生产实证）：`openAIRequest.MaxTokens` 用 int 强类型时，
// 客户端只要发 `{"max_tokens":"abc"}` 这类字符串，整个 `json.Unmarshal` 就会失败，
// 请求在解析阶段就被拒；而宿主会把插件返回的错误当成「这个账号不行」，
// 遍历整池每个账号各失败一次（实测同一 requestId 在 1 秒内对 15 个账各记一条
// `payload parse: json: cannot unmarshal string into Go struct field ... of type int`），
// 最后合成 503。参数畸形属于**客户端问题**，必须在本地干净地降级，
// 绝不能让它演变成账号级故障。
//
// 接受三种形态：
//   - 数字 `2000` → 2000
//   - 数字字符串 `"2000"` → 2000（不少 SDK / 配置模板会把数值写成字符串）
//   - 其余（`"abc"` / null / 对象 / 布尔 / 负数）→ 0，即「未声明」
//
// 为什么是 0 而不是报错：max_tokens 是可选参数，非法值的安全语义就是「按未传处理」，
// 由上游用自己的默认值；请求的其余部分仍然合法，不应整单拒绝。
// [参数] 无（由 json.Unmarshal 调用 UnmarshalJSON）
// [返回] 无
// 最近修改时间：2026-11-08 01:00:00；改动原因：新增，堵住畸形数值把整池账号打穿。
type flexNumber int64

// UnmarshalJSON 实现宽容解析：数字、数字字符串取整，其余一律降级为 0。
// [参数] raw：JSON 原始字节
// [返回] 恒为 nil（不因业务数值畸形而让整包解析失败）
func (n *flexNumber) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	// 1. 数字字面量：直接转整数，非整数 / 越界一律 0
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		if v > 0 {
			*n = flexNumber(v)
		} else {
			*n = 0
		}
		return nil
	}
	// 2. 数字字符串："2000"
	if strings.HasPrefix(s, "\"") {
		var inner string
		if err := json.Unmarshal(raw, &inner); err == nil {
			inner = strings.TrimSpace(inner)
			if v, err2 := strconv.ParseInt(inner, 10, 64); err2 == nil && v > 0 {
				*n = flexNumber(v)
				return nil
			}
		}
	}
	// 3. 其余形态：按未声明处理
	*n = 0
	return nil
}

// Int 返回解析后的整数值（0 表示未声明或非法）。
func (n flexNumber) Int() int64 { return int64(n) }
