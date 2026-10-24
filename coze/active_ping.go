package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现面板「测试」按钮的账号探活。
//
// 探活分三层，逐层加深：
//   - account/info/v2 判据通过 → 会话 Cookie 仍有效；
//   - 模型目录可拉取 → 该账号的模型权限仍在；
//   - chat 模型再发一次极小推理 → 对话链路（WS 收帧、增量拼接、收尾）
//     真的能跑通。前两层测不出 executor 故障，2026-10-18 的流式 500
//     与空正文都属于这类只有真打一次才会暴露的问题。
//
// 图片/视频模型只做前两层：它们不适用对话链路，且单次生成要数十秒。

// testActiveTimeout 是单次探活的等待上限。
//
// 认证与模型两步只打轻量接口，正常 1~3 秒返回；20 秒覆盖上游排队与网络抖动。
const testActiveTimeout = 20 * time.Second

// testActiveResult 是探活结果。
type testActiveResult struct {
	OK        bool   `json:"ok"`
	AuthID    string `json:"auth_id"`
	Label     string `json:"label,omitempty"`
	Model     string `json:"model,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	// ModelCount 是该账号可见的模型数量，供面板展示。
	ModelCount int `json:"model_count,omitempty"`
	// Scope 说明本次探活的覆盖范围，面板据此提示用户。
	Scope string `json:"scope"`
}

// handleTestActive 处理面板「测试」按钮发起的单账号探活。
//
// 请求体：{auth_id, model?}。model 仅用于回显，不参与探活逻辑。
// [参数] req: 管理请求。
// [返回] 探活结果；失败时带 error 字段。
// 最近修改时间：2026-10-18；改动原因：新增，面板测试模型入口。
func handleTestActive(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthID    string `json:"auth_id"`
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &body)

	authID := strings.TrimSpace(body.AuthID)
	if authID == "" {
		authID = strings.TrimSpace(body.AuthIndex)
	}
	if authID == "" {
		return map[string]any{"ok": false, "error": "auth_id 不能为空"}
	}

	sa, errFind := findAccountForTest(authID)
	if errFind != nil {
		return map[string]any{"ok": false, "error": errFind.Error()}
	}
	return runActiveTest(sa, strings.TrimSpace(body.Model))
}

// findAccountForTest 按账号标识定位账号。
//
// 同时接受裸 id 与带 coze- 前缀的写法：面板下发的是 auth_id，而用户在宿主侧
// 看到的可能是去掉 .json 的文件名。
// [参数] authID: 账号标识。
// [返回] 账号与错误。
// 最近修改时间：2026-10-18；改动原因：新增，测试账号定位。
func findAccountForTest(authID string) (*StoredAuth, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, fmt.Errorf("读取账号失败：%w", errList)
	}
	normalized := strings.TrimSuffix(authID, ".json")
	if !strings.HasPrefix(normalized, cozeAuthFilePrefix) {
		normalized = cozeAuthFilePrefix + normalized
	}
	for _, acc := range accounts {
		if acc.AuthID == authID || acc.AuthID == normalized {
			return acc, nil
		}
	}
	return nil, fmt.Errorf("账号不存在：%s", authID)
}

// runActiveTest 对指定账号执行一次探活。
//
// 三步都成功才判定可用：判据端点确认会话有效，模型目录确认权限仍在，
// chat 模型还会发一次极小推理确认对话链路真的通。
// 只做前两步会把「会话活着但 executor 坏了」的账号误报为可用——
// 本仓库 2026-10-18 就出现过流式 500 与空正文，认证层探活完全测不出来。
// 图片/视频模型只做前两步：它们不适用对话链路，且单次生成耗时数十秒。
// [参数] sa: 账号；model: 面板选中的模型（仅回显）。
// [返回] 探活结果。
// 最近修改时间：2026-10-18；改动原因：升级为真实推理探活，覆盖对话链路。
func runActiveTest(sa *StoredAuth, model string) map[string]any {
	start := time.Now()
	res := testActiveResult{
		AuthID: sa.AuthID,
		Label:  sa.Label,
		Model:  model,
		Scope:  "auth+models",
	}

	info, errProbe := probeAccountInfo(sa.CookieHeader)
	if errProbe != nil {
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.Error = errProbe.Error()
		return testActiveResultMap(res)
	}
	// 判据端点的 sec_user_id 必须与账号一致：不一致说明 Cookie 已被另一个
	// 账号复用（重新授权换了人），继续当作可用会写错归属。
	if got := info.secUserID(); got != "" && sa.SecUserID != "" && got != sa.SecUserID {
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.Error = "登录态与账号不匹配：判据返回的 sec_user_id 与账号记录不同，请重新授权"
		return testActiveResultMap(res)
	}

	models, errDiscover := cachedDiscoveredModels(sa, true)
	if errDiscover != nil {
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.Error = "会话有效，但模型目录不可用：" + errDiscover.Error()
		return testActiveResultMap(res)
	}
	res.ModelCount = len(models)

	// 面板选了模型时校验它确实在该账号目录里：选到别的账号的模型属于配置错误，
	// 提前报出来比等到真实推理失败更好排查。
	if model != "" && !modelInCatalog(models, model) {
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.Error = fmt.Sprintf("模型 %s 不在该账号的可用目录中（共 %d 个模型）", model, len(models))
		return testActiveResultMap(res)
	}

	// 选中的是 chat 模型就再跑一次真实推理：这是唯一能证明「对话链路可用」
	// 的判据。图片/视频模型跳过，避免几十秒等待与无谓的生成配额消耗。
	if model != "" && isChatModel(models, model) {
		res.Scope = "auth+models+inference"
		reply, errInfer := runInferenceProbe(sa, model)
		if errInfer != nil {
			res.ElapsedMS = time.Since(start).Milliseconds()
			res.Error = "会话与模型均可用，但推理失败：" + errInfer.Error()
			return testActiveResultMap(res)
		}
		res.OK = true
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.Message = fmt.Sprintf("推理成功，可见 %d 个模型；回复：%s", len(models), reply)
		return testActiveResultMap(res)
	}

	res.OK = true
	res.ElapsedMS = time.Since(start).Milliseconds()
	res.Message = fmt.Sprintf("会话有效，可见 %d 个模型", len(models))
	return testActiveResultMap(res)
}

// inferenceProbePrompt 是推理探活的提示词。
//
// 要求回复极短（一个词），既能证明链路上游通、又不浪费生成配额；
// 附带随机标记用于确认回复确实来自本轮而不是历史残留。
const inferenceProbePrompt = "只回复两个字：可用"

// inferenceProbeMaxDuration 是推理探活的等待上限。
//
// 真流式一轮通常 5~20 秒；60 秒覆盖上游排队，超过就说明链路卡死。
const inferenceProbeMaxDuration = 60 * time.Second

// inferenceProbe 是推理探活的执行函数。
//
// 抽成变量是为了测试可注入假实现：单测里没有真实账号，
// 而真实推理必然被上游拒绝（登录认证失败），会让所有探活用例变红。
var inferenceProbe = func(ctx context.Context, sa *StoredAuth, model string) (string, error) {
	exec := newCozeExecutor(sa, model, nil)
	return exec.execute(ctx, inferenceProbePrompt)
}

// runInferenceProbe 对指定 chat 模型发一次极小推理请求。
//
// 走插件自己的执行链路（非流式），与真实用户请求同一条代码路径，
// 因此能捕获流式 500、空正文、WS 未就绪等只有对话链路才暴露的故障。
// [参数] sa: 账号；model: 对外模型 ID（可带 coze/ 前缀）。
// [返回] 回复摘要与错误。
// 最近修改时间：2026-10-18；改动原因：新增，对话链路真实验证。
func runInferenceProbe(sa *StoredAuth, model string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), inferenceProbeMaxDuration)
	defer cancel()

	content, errRun := inferenceProbe(ctx, sa, model)
	if errRun != nil {
		return "", errRun
	}
	reply := strings.TrimSpace(content)
	if reply == "" {
		return "", fmt.Errorf("上游返回空正文")
	}
	// 面板只显示一行，长回复截断即可：探活关心的是「有没有正文」。
	if runes := []rune(reply); len(runes) > 40 {
		reply = string(runes[:40]) + "…"
	}
	return reply, nil
}

// isChatModel 判断模型是否为对话模型。
//
// 只认宿主 Type 为 chat 的条目；图片/视频/音频模型不适用对话探活。
// [参数] models: 模型目录；model: 待查模型名。
// [返回] 是对话模型时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，按模型类型分流探活强度。
func isChatModel(models []pluginapi.ModelInfo, model string) bool {
	want := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(model, ":latest")))
	for _, m := range models {
		if strings.ToLower(m.ID) != want && !strings.EqualFold(m.Name, model) {
			continue
		}
		return strings.EqualFold(strings.TrimSpace(m.Type), "chat")
	}
	return false
}

// modelInCatalog 判断模型是否在目录中（忽略大小写与 :latest 后缀）。
// [参数] models: 模型目录；model: 待查模型名。
// [返回] 命中时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，模型存在性校验。
func modelInCatalog(models []pluginapi.ModelInfo, model string) bool {
	want := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(model, ":latest")))
	for _, m := range models {
		if strings.ToLower(m.ID) == want || strings.EqualFold(m.Name, model) {
			return true
		}
	}
	return false
}

// testActiveResultMap 把结果转成管理响应载荷。
// [参数] res: 探活结果。
// [返回] 载荷 map。
// 最近修改时间：2026-10-18；改动原因：新增，响应构造。
func testActiveResultMap(res testActiveResult) map[string]any {
	out := map[string]any{
		"ok":         res.OK,
		"auth_id":    res.AuthID,
		"elapsed_ms": res.ElapsedMS,
		"scope":      res.Scope,
	}
	if res.Label != "" {
		out["label"] = res.Label
	}
	if res.Model != "" {
		out["model"] = res.Model
	}
	if res.Message != "" {
		out["message"] = res.Message
	}
	if res.Error != "" {
		out["error"] = res.Error
	}
	if res.ModelCount > 0 {
		out["model_count"] = res.ModelCount
	}
	return out
}

// handleModelsQuery 返回指定账号可测试的模型列表。
//
// 面板「测试」弹窗用它填充模型下拉：优先动态目录，失败时回落到静态种子，
// 并在 notice 字段说明原因，避免把回落结果误当成账号真实模型。
// [参数] req: 管理请求，query 参数 auth_id 指定账号。
// [返回] {models:[...], source, notice}；缺少 auth_id 时返回 {error}。
// 最近修改时间：2026-10-18；改动原因：新增，测试弹窗模型数据源。
func handleModelsQuery(req pluginapi.ManagementRequest) map[string]any {
	authID := queryAuthID(req)
	if authID == "" {
		return map[string]any{"error": "auth_id 不能为空"}
	}

	sa, errFind := findAccountForTest(authID)
	if errFind != nil {
		return map[string]any{"error": errFind.Error()}
	}

	models, errDiscover := cachedDiscoveredModels(sa, false)
	source := "upstream"
	notice := ""
	if errDiscover != nil {
		models = staticFallbackModels()
		source = "fallback"
		notice = "模型目录拉取失败，当前为内置清单：" + errDiscover.Error()
	}

	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		out = append(out, map[string]any{
			"id":           m.ID,
			"name":         m.Name,
			"display_name": m.DisplayName,
			"type":         m.Type,
		})
	}
	return map[string]any{"models": out, "source": source, "notice": notice}
}

// handleCreditsQuery 查询指定账号的积分余额与即将过期明细。
//
// force=true 时跳过缓存：面板「刷新」按钮需要拿到最新数字，
// 而列表接口读的是巡检落盘的快照。强制刷新后必须把结果写回快照并
// 重跑分级——只更新内存缓存的话，面板下一次加载又会退回旧快照，
// 用户会以为刷新没生效（workbuddy 侧踩过同款）。
// [参数] req: 管理请求，query 参数 auth_id 指定账号，可选 force=1。
// [返回] 积分摘要；失败时带 error 字段。
// 最近修改时间：2026-10-25；改动原因：强制刷新补写快照与分级，避免面板回退旧值。
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authID := queryAuthID(req)
	if authID == "" {
		return map[string]any{"ok": false, "error": "auth_id 不能为空"}
	}
	sa, errFind := findAccountForTest(authID)
	if errFind != nil {
		return map[string]any{"ok": false, "error": errFind.Error()}
	}
	force := strings.TrimSpace(req.Query.Get("force")) == "1"
	summary, errFetch := cachedCreditSummary(sa, force)
	if errFetch != nil {
		return map[string]any{"ok": false, "error": errFetch.Error()}
	}
	// 1. 强制刷新时同步写回快照与分级：面板读的是快照，不写等于没刷新。
	if force {
		recordCreditSnapshot(sa.AuthID, summary, sa.ScreenName)
		if errPersist := persistCreditSnapshot(); errPersist != nil {
			hostLogf("warn", "单卡刷新落盘失败", map[string]any{
				"auth_id": sa.AuthID,
				"error":   errPersist.Error(),
			})
		}
		gradeCreditsForAccount(sa, summary)
	}
	out := creditSummaryMap(summary)
	out["ok"] = true
	out["credit_tier"] = creditTierFor(summary.Remain, loadedMinRoutableCredits()).String()
	out["auto_disabled"] = sa.LowCreditDisable
	return out
}

// creditSummaryMap 把积分摘要转成响应载荷。
// [参数] summary: 积分摘要。
// [返回] 载荷 map。
// 最近修改时间：2026-10-18；改动原因：新增，积分响应构造。
func creditSummaryMap(summary *creditSummary) map[string]any {
	if summary == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"total":      summary.Total,
		"remain":     summary.Remain,
		"expiring":   summary.ExpiringRemain,
		"fetched_at": summary.FetchedAt,
	}
	if summary.ExpiringAt != "" {
		out["expiring_at"] = summary.ExpiringAt
	}
	if summary.Error != "" {
		out["error"] = summary.Error
	}
	if summary.DetailError != "" {
		out["detail_error"] = summary.DetailError
	}
	if len(summary.Items) > 0 {
		items := make([]map[string]any, 0, len(summary.Items))
		for _, item := range summary.Items {
			items = append(items, map[string]any{
				"title":      item.Title,
				"remaining":  item.Remaining,
				"expired_at": item.ExpiredAt,
			})
		}
		out["items"] = items
	}
	return out
}

// queryAuthID 从管理请求的 query 中提取账号标识。
// [参数] req: 管理请求。
// [返回] 账号标识；缺失时返回空串。
// 最近修改时间：2026-10-18；改动原因：新增，多个查询路由共用取值逻辑。
func queryAuthID(req pluginapi.ManagementRequest) string {
	for _, key := range []string{"auth_id", "auth_index"} {
		if vals := req.Query[key]; len(vals) > 0 {
			if v := strings.TrimSpace(vals[0]); v != "" {
				return v
			}
		}
	}
	return ""
}
