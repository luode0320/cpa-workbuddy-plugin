// content_moderation_test.go 锁定「内容审核拦截进入失败分类」的判据边界与冷却落点。
//
// 生产背景（2026-11-12，会话 session-3304f06f）：`deepseek-v4-pro` 在中文成人向
// 正文续写场景下，上游以 `finish_reason=content_filter` 终止流（宿主转译后呈现为
// `Response incomplete: content_filter`）。该分片是**正常终止**而非错误帧，
// `sseErrorFrame` 捕获不到，因此旧实现既不记账也不冷却——模型 100% 被审时，
// 插件没有任何机制让它退出路由。实测 42 次失败覆盖 31/37 个 turn。
package main

import (
	"strings"
	"testing"
)

// TestIsContentModerationFailure 验证内容审核拦截的识别边界。
//
// 覆盖三层书写形态：宿主直出的 finish_reason 值、OpenAI 语义帧的
// incomplete_details、以及通用 sensitive content 文案；同时锁定长度类截断
// 不得被误判为审核（两者共用 incomplete_details 字段）。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，content_filter 未进入失败分类。
func TestIsContentModerationFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		// 形态 1：宿主直出的 finish_reason 值（workbuddy 实际收到的形态）。
		{
			name: "宿主 finish_reason 审核终止分片",
			body: `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			want: true,
		},
		// 形态 2：OpenAI 语义帧。
		{
			name: "response.incomplete 的 incomplete_details",
			body: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`,
			want: true,
		},
		// 形态 3：通用审核文案（与 traework 对齐的兜底口径）。
		{
			name: "sensitive content 文案",
			body: "upstream 200: sensitive content error finish reason: sensitive",
			want: true,
		},
		{
			name: "大写同样识别（保留错误语境标记）",
			body: "UPSTREAM 200: SENSITIVE CONTENT ERROR FINISH REASON: SENSITIVE",
			want: true,
		},
		// 刻意不成立：无错误语境标记的裸词组不判定为审核。
		// 本判据运行在每个流式分片上，正文即用户内容；若无条件认词，正文里出现
		// 这些词就会被误判成审核失败并给健康号记冷却（中文小说正文场景必踩）。
		{
			name: "裸词组无错误上下文不判定",
			body: "SENSITIVE CONTENT ERROR",
			want: false,
		},
		// 反例：长度类截断必须排除（与审核共用 incomplete_details 字段）。
		{
			name: "长度截断不归此类",
			body: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
			want: false,
		},
		{
			name: "max_tokens 长度语义不归此类",
			body: `{"choices":[{"finish_reason":"length"}],"incomplete_details":{"reason":"max_tokens"}}`,
			want: false,
		},
		// 反例：其它失败类型不得误判。
		{
			name: "配额耗尽不归此类",
			body: "upstream 200: Your requests have exceeded the quota.",
			want: false,
		},
		{
			name: "渠道拦截不归此类",
			body: `{"code":11128,"msg":"unapproved channel"}`,
			want: false,
		},
		{
			name: "空正文不归此类",
			body: "",
			want: false,
		},
		{
			name: "纯空白不归此类",
			body: "   ",
			want: false,
		},
		{
			name: "正常 stop 终止不归此类",
			body: `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			want: false,
		},
		// 反例（关键）：正常正文里出现审核同名词组不得误判。
		// 本判据运行在每一个流式分片上，正文即用户内容；中文小说正文里
		// "content error" / "sensitive content" 完全中性，误判会给健康号记冷却。
		{
			name: "正常正文含 content error 字样不误判",
			body: `{"choices":[{"index":0,"delta":{"content":"他写下 content error 这两个词，然后又划掉"}}]}`,
			want: false,
		},
		{
			name: "正常正文含 sensitive content 字样不误判",
			body: `{"choices":[{"index":0,"delta":{"content":"她念出 sensitive content 这个短语"}}]}`,
			want: false,
		},
		// 正例：错误帧形态下审核文案才成立。
		{
			name: "error 包裹的审核文案命中",
			body: `{"error":{"message":"sensitive content error finish reason: sensitive"}}`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentModerationFailure(tc.body); got != tc.want {
				t.Fatalf("isContentModerationFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamFailureContentModeration 验证审核失败归入模型级冷却。
//
// 落 model_rate_limit 而非 account：审核与账号健康无关，禁号会把整池逐个摘掉；
// 但必须记冷却，否则 100% 被审的模型没有任何机制退出路由。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，锁定审核失败的分类落点。
func TestClassifyUpstreamFailureContentModeration(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "200 承载 finish_reason 审核终止",
			status: 200,
			body:   `{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
		},
		{
			name:   "403 承载审核文案（上游改用 4xx 时不得被账号级吞掉）",
			status: 403,
			body:   "upstream 403: sensitive content error finish reason: sensitive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstreamFailure(tc.status, tc.body)
			if got != failureScopeModelRateLimit {
				t.Fatalf("classifyUpstreamFailure(%d, %q) = %v, want %v",
					tc.status, tc.body, got, failureScopeModelRateLimit)
			}
			// 审核失败与账号健康无关：不得写盘禁用。
			if got.disablesAccount() {
				t.Fatalf("审核失败分类 %v 不得禁用账号", got)
			}
			// 但必须记冷却，否则模型无法退出路由。
			if !got.recordsCooldown() {
				t.Fatalf("审核失败分类 %v 必须记冷却", got)
			}
		})
	}
}

// TestStreamChunkContentFilterDetection 验证审核终止分片可被判据识别。
//
// 该判据是流式路径记账的唯一入口：sseErrorFrame 对 finish_reason 分片返回空串，
// 因此必须在这里命中，否则流式路径永远不会记冷却。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，锁定流式路径的识别前提。
func TestStreamChunkContentFilterDetection(t *testing.T) {
	// 1. 审核终止分片：sseErrorFrame 必须返回空（它不是 error 包裹帧）。
	chunk := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	if msg := sseErrorFrame(chunk); msg != "" {
		t.Fatalf("审核终止分片不应被 sseErrorFrame 命中，实际返回 %q", msg)
	}
	// 2. 但必须被审核判据命中，作为流式路径唯一的记账入口。
	if !isContentModerationFailure(chunk) {
		t.Fatal("审核终止分片必须被 isContentModerationFailure 识别")
	}
}

// TestRecordSSEErrorFailureKeepsAccountEnabled 锁定审核失败不得写盘禁用账号。
//
// 这是防「整池清零」事故的关键断言：recordSSEErrorFailureForModel 历史上把
// scope 写死为 failureScopeAuthModel（disablesAccount()==true），审核失败一旦
// 走该分支就会把与审核无关的健康号逐个摘掉。
//
// 必须连续记账到禁用门限（failoverDisableMinCount=3）以上才有区分度：
// 单次失败只写内存冷却、不写盘禁用，因此单次断言无法捕获该变异。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，锁定 SSE 记账的维度来源。
func TestRecordSSEErrorFailureKeepsAccountEnabled(t *testing.T) {
	resetFailover(t)
	// 桩化 auth_index 解析：disableAccountForFailover 在解析为空时直接返回 false，
	// 不桩化的话变异体也禁用不了账号，断言会假通过。
	origResolve := failoverResolveIndexFn
	origPersist := failoverPersistFn
	failoverResolveIndexFn = func(authID string) (string, string) { return "idx-cf", "cf-id" }
	failoverPersistFn = func(name, path, legacyPath string, raw []byte) error { return nil }
	t.Cleanup(func() {
		failoverResolveIndexFn = origResolve
		failoverPersistFn = origPersist
		clearFailoverDisabledLocalAll()
	})
	chunk := `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`
	// 连续记账超过禁用门限，才能区分 AuthModel（会禁用）与 ModelRateLimit（不禁用）。
	for i := 0; i < defaultFailoverDisableMinCount+2; i++ {
		if !recordSSEErrorFailureForModel("acc-cf", "deepseek-v4-pro", chunk) {
			t.Fatalf("第 %d 次审核失败必须记入冷却，否则模型无法退出路由", i+1)
		}
	}
	// 冷却必须存在（证明已记账）。
	if _, _, ok := failoverStateSnapshot("acc-cf"); !ok {
		t.Fatal("审核失败必须写入冷却状态")
	}
	// 但绝不能禁用账号：审核与账号健康无关。
	if isFailoverDisabledLocal("acc-cf") {
		t.Fatal("内容审核失败不得写盘禁用账号（会整池清零）")
	}
	if failoverStateIsDisabled("acc-cf") {
		t.Fatal("内容审核失败不得在 failover 状态里标记 disabled")
	}
}

// TestAggregateCompletionContentFilterKeepsContent 锁定审核终止不污染正文聚合。
//
// finish_reason=content_filter 是正常终止分片，aggregateCompletion 必须照常
// 折叠出完整 completion（含已生成的正文），不得返回错误 —— 返回错误会让宿主
// 丢弃整份结果，用户会连已生成的内容一起失去。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，锁定审核终止不破坏聚合语义。
func TestAggregateCompletionContentFilterKeepsContent(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"已生成的正文\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	out, err := aggregateCompletion(strings.NewReader(sse), "m", 200, nil)
	if err != nil {
		t.Fatalf("审核终止不得让聚合报错（会丢弃已生成正文）: %v", err)
	}
	if !strings.Contains(string(out), "已生成的正文") {
		t.Fatalf("已生成正文必须保留，实际: %s", out)
	}
	if !strings.Contains(string(out), "content_filter") {
		t.Fatalf("finish_reason 必须透传 content_filter，实际: %s", out)
	}
}

// TestRecordSSEErrorFailureQueueFullNotRecorded 锁定渠道满载经 SSE 记账路径时不记账。
//
// 渠道满载（6021 / queue.full）换任何账号都会撞同一堵墙，罚号只会缩小可用池
// 并放大拥堵。该语义必须由 classifyUpstreamFailure 的 UpstreamBusy 维度承载，
// 不得在「统一判据未命中」的兜底里被回退成 AuthModel。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-12；改动原因：新增，防止兜底逻辑吃掉拥堵不记账语义。
func TestRecordSSEErrorFailureQueueFullNotRecorded(t *testing.T) {
	resetFailover(t)
	if recordSSEErrorFailureForModel("acc-qf", "deepseek-v4-pro", `{"code":6021,"message":"queue.full"}`) {
		t.Fatal("渠道满载不得记账（罚号会放大拥堵）")
	}
	if _, _, ok := failoverStateSnapshot("acc-qf"); ok {
		t.Fatal("渠道满载不得写入冷却状态")
	}
}
