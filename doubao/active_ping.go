package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件实现面板「测试」按钮的单账号活跃测试。
//
// 与定时探活的分工：本插件不做后台自动探活（豆包按账号限流，后台轮询会
// 无谓消耗配额），只保留用户手动触发的一次真实推理，用于确认账号当前可用。

// activeTestTimeout 是单次手动测试的等待上限。
//
// 测试只等首个有效输出帧，正常在 1~3 秒内返回；20 秒用于覆盖上游排队
// 与网络抖动，超过则判定账号或链路不可用。
const activeTestTimeout = 20 * time.Second

// testPingText 是测试请求的固定提示词。
//
// 用最短的「hi」而不是让它写点什么：测试只验证链路，回复越短越好。
const testPingText = "hi"

// handleModelsQuery 返回指定账号可测试的模型列表。
//
// 列表来源固定为插件别名目录（与宿主 modelForAuth 暴露的 /v1/models 同源），
// 而不是上游内部代号：代号既不在能力别名也不在工作模式别名里，选中后只能回落
// 到工作模式默认模型，「测试成功」与所选模型无关。上游代号仍随附在
// upstream_models 供排障对照，但不作为可点击项。
// [参数] req: 管理请求，query 参数 auth_index 指定账号；force=1 强制刷新上游清单。
// [返回] {models, details, source, upstream_models, upstream_error, notice}；
// 缺少 auth_index 或账号不存在时返回 {error}。
// 最近修改时间：2026-11-14 16:05:00；改动原因：模型列表改为插件别名目录，上游代号仅作对照。
func handleModelsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	if authIndex == "" {
		return map[string]any{"error": "auth_index 不能为空"}
	}
	force := false
	if vals := req.Query["force"]; len(vals) > 0 {
		force = strings.TrimSpace(vals[0]) == "1"
	}

	sa, errFind := findAccountForTest(authIndex)
	if errFind != nil {
		return map[string]any{"error": errFind.Error()}
	}

	// 1. 可测试模型一律取插件别名目录，保证与宿主模型目录同源。
	res := map[string]any{
		"models":  testableModelIDs(),
		"details": testableModelDetails(),
		"source":  "aliases",
	}

	// 2. 上游代号只作对照展示：它们无法映射到工作模式模型主键。
	upstream, errDiscover := cachedUpstreamModels(sa, force)
	if errDiscover != nil {
		res["upstream_error"] = errDiscover.Error()
		res["notice"] = fmt.Sprintf("未能读取上游模型清单（%v），仅显示插件别名目录。", errDiscover)
		return res
	}
	codes := make([]string, 0, len(upstream))
	for _, m := range upstream {
		codes = append(codes, m.ID)
	}
	res["upstream_models"] = codes
	res["notice"] = fmt.Sprintf("列表为插件别名目录；上游另返回 %d 个内部代号，代号无法映射到具体模型，故不参与测试。", len(codes))
	return res
}

// handleTestActive 处理面板「测试」按钮发起的单账号活跃测试。
//
// 请求体：{auth_index, model?}。model 为空时使用目录里的第一个文本模型。
// [参数] req: 管理请求。
// [返回] 测试结果；失败时带 error 字段。
// 最近修改时间：2026-10-17；改动原因：新增，手动活跃测试入口。
func handleTestActive(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &body)

	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return map[string]any{"error": "auth_index 不能为空"}
	}
	sa, errFind := findAccountForTest(authIndex)
	if errFind != nil {
		return map[string]any{"error": errFind.Error()}
	}

	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = modelIDPrefix + defaultWorkModelID
	}
	return runActiveTest(sa, model)
}

// findAccountForTest 按账号标识定位账号。
//
// 同时接受裸 id 与带 doubao- 前缀的写法：面板下发的是 auth_id，而用户在
// 宿主侧看到的可能是去掉前缀的文件名。
// [参数] authIndex: 账号标识。
// [返回] 账号与错误。
// 最近修改时间：2026-10-17；改动原因：新增，测试账号定位。
func findAccountForTest(authIndex string) (*StoredAuth, error) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, fmt.Errorf("读取账号失败：%w", errList)
	}
	normalized := authIndex
	if !strings.HasPrefix(normalized, doubaoAuthFilePrefix) {
		normalized = doubaoAuthFilePrefix + normalized
	}
	for _, acc := range accounts {
		if acc.AuthID == authIndex || acc.AuthID == normalized {
			return acc, nil
		}
	}
	return nil, fmt.Errorf("账号不存在：%s", authIndex)
}

// runActiveTest 对指定账号发起一次真实对话，收到首个有效输出即判定成功。
//
// 为什么不等完整回复：测试只验证「账号此刻能不能出字」，等全文会拖到数十秒，
// 思考型回复尤其明显。首帧到达即返回，剩余流由 streamCompletion 的 defer
// 立即关闭，不会占用上游连接。
//
// 成功判定不能只看「流里有字节」：上游在 HTTP 200 下用 STREAM_ERROR 帧表达
// 额度耗尽/限流，只看首包会把死号判成活跃。故必须解析帧语义，错误帧立即失败。
// [参数] sa: 账号；model: 客户端模型名。
// [返回] 测试结果。
// 最近修改时间：2026-11-14 16:05:00；改动原因：模型名改为严格解析，未命中别名直接失败且成功消息回显上游模型主键。
func runActiveTest(sa *StoredAuth, model string) map[string]any {
	// 1. 严格解析模型名：未命中别名直接失败，避免静默替换模型后给出假结论。
	mode, alias, ok := testModeForModel(model)
	if !ok {
		return map[string]any{
			"error": fmt.Sprintf("不支持的测试模型：%s（请从模型列表中选择插件别名；上游内部代号无法映射到具体模型）", model),
			"model": model,
		}
	}
	model = modelIDPrefix + alias.ID

	start := time.Now()
	body := buildCompletionRequest(sa, testPingText, mode, "", "", 0, true)

	ctx, cancel := context.WithTimeout(context.Background(), activeTestTimeout)
	defer cancel()

	var (
		sawOutput bool
		preview   string
		streamErr error
	)

	errStream := streamCompletion(ctx, sa, body, func(ev completionEvent) bool {
		switch ev.Name {
		case "STREAM_ERROR", "ERROR":
			if e, ok := parseStreamError(ev.Data); ok {
				streamErr = &upstreamError{Code: e.ErrorCode, Message: statusCodeHint(e.ErrorCode, e.ErrorMsg)}
			}
			return false

		case "SSE_REPLY_END":
			// 回复已结束仍没拿到任何输出：判定失败，不继续等 Keep-Alive。
			return false
		}
		if text, ok := activeTestOutput(ev); ok {
			sawOutput = true
			preview = text
			return false
		}
		return true
	})

	elapsed := time.Since(start).Round(time.Millisecond)
	if streamErr != nil {
		return map[string]any{"error": fmt.Sprintf("测试失败（模型: %s）：%v", model, streamErr), "model": model}
	}
	if errStream != nil {
		return map[string]any{"error": fmt.Sprintf("测试失败（模型: %s）：%v", model, errStream), "model": model}
	}
	if !sawOutput {
		return map[string]any{"error": fmt.Sprintf("测试失败（模型: %s）：上游没有返回任何输出", model), "model": model}
	}

	log.Printf("[doubao] manual active test success for %s with model %s (took %v)", sa.AuthID, model, elapsed)
	message := fmt.Sprintf("活跃成功 (模型: %s → 上游 %s, 耗时: %v)", model, mode.ModelItemKey, elapsed)
	if p := strings.TrimSpace(preview); p != "" {
		message += "：" + truncateText(p, 40)
	}
	return map[string]any{
		"ok":      true,
		"model":   model,
		"elapsed": elapsed.String(),
		"message": message,
	}
}

// activeTestOutput 判断一帧是否携带可证明「账号能出字」的输出。
//
// 三类增量都要认（与正式对话链路同口径）：整消息、首块、补丁/增量。思考块与
// 生成块同样算输出——它们都证明上游真的开始响应，而不是空转。
// 提问回显（user_type=1）不算输出：那是上游把问题原样发回来。
// [参数] ev: 一条 SSE 事件。
// [返回] 可用于展示的文本片段与是否命中。
// 最近修改时间：2026-10-17；改动原因：新增，从测试内核抽出帧判定。
func activeTestOutput(ev completionEvent) (string, bool) {
	switch ev.Name {
	case "FULL_MSG_NOTIFY":
		var payload struct {
			Message struct {
				UserType     int             `json:"user_type"`
				ContentBlock []responseBlock `json:"content_block"`
			} `json:"message"`
		}
		if json.Unmarshal(ev.Data, &payload) != nil || payload.Message.UserType != 2 {
			return "", false
		}
		return contentOutput(streamMessageContent{ContentBlock: payload.Message.ContentBlock})

	case "STREAM_MSG_NOTIFY":
		var payload struct {
			Content streamMessageContent `json:"content"`
		}
		if json.Unmarshal(ev.Data, &payload) != nil {
			return "", false
		}
		return contentOutput(payload.Content)

	case "STREAM_CHUNK", "CHUNK_DELTA":
		var payload struct {
			Content streamMessageContent `json:"content"`
			PatchOp []patchOp            `json:"patch_op"`
			Delta   string               `json:"delta"`
			Text    string               `json:"text"`
		}
		if json.Unmarshal(ev.Data, &payload) != nil {
			return "", false
		}
		// 同一帧可能同时带多种形态，按优先级取一种，避免重复判定。
		if t := strings.TrimSpace(payload.Delta); t != "" {
			return payload.Delta, true
		}
		if t := strings.TrimSpace(payload.Text); t != "" {
			return payload.Text, true
		}
		for _, op := range payload.PatchOp {
			if t := op.PatchValue.textOf(); strings.TrimSpace(t) != "" {
				return t, true
			}
			if len(collectCreationAssets(op.PatchValue.blocks(), nil)) > 0 {
				return "", true
			}
		}
		return contentOutput(payload.Content)
	}
	return "", false
}

// contentOutput 判断一个消息内容体是否已有可见输出。
//
// 优先返回正文（用于结果预览）；只有思考内容或生成块时返回空文本但仍算命中。
// [参数] content: 消息内容体。
// [返回] 文本片段与是否命中。
// 最近修改时间：2026-10-17；改动原因：新增，三类事件共用的内容判定。
func contentOutput(content streamMessageContent) (string, bool) {
	if t := content.textOf(); strings.TrimSpace(t) != "" {
		return t, true
	}
	if content.reasoningOf() != "" {
		return "", true
	}
	if len(collectCreationAssets(content.ContentBlock, nil)) > 0 {
		return "", true
	}
	return "", false
}
