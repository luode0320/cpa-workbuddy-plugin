package main

import "testing"

// TestClassifyUpstreamFailureTable 按实施总览第 3 节的边界表逐行断言分类结果。
// 这张表是冷却维度与探活策略的唯一判据来源，任何一行漂移都必须在这里失败。
func TestClassifyUpstreamFailureTable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   failureScope
	}{
		{
			name:   "传输错误归账号级",
			status: 0,
			body:   "connection reset by peer",
			want:   failureScopeAccount,
		},
		{
			name:   "5xx 归账号级",
			status: 502,
			body:   "bad gateway",
			want:   failureScopeAccount,
		},
		{
			name:   "401 归账号级",
			status: 401,
			body:   `{"error":"invalid token"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "403 归账号级",
			status: 403,
			body:   `{"error":"forbidden"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "404 归账号级",
			status: 404,
			body:   `{"error":"not found"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "405 归账号级",
			status: 405,
			body:   `{"error":"method not allowed"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "402 硬额度归账号级",
			status: 402,
			body:   `{"error":"payment required"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "200 带额度标记归账号级",
			status: 200,
			body:   `{"message":"insufficient credits"}`,
			want:   failureScopeAccount,
		},
		{
			name:   "429 归账号×模型级",
			status: 429,
			body:   `{"error":"too many requests"}`,
			want:   failureScopeAuthModel,
		},
		{
			name:   "200 软限流文案归账号×模型级",
			status: 200,
			body:   `{"message":"rate limit exceeded"}`,
			want:   failureScopeAuthModel,
		},
		{
			name:   "零字节断流归账号×模型级",
			status: 200,
			body:   "upstream stream closed before first payload",
			want:   failureScopeAuthModel,
		},
		{
			name:   "400 纯请求形状不冷却",
			status: 400,
			body:   `{"error":"invalid request body"}`,
			want:   failureScopeNone,
		},
		{
			name:   "422 请求形状不冷却",
			status: 422,
			body:   `{"error":"unprocessable entity"}`,
			want:   failureScopeNone,
		},
		{
			// 历史实现先看正文标记再看状态码：400 携带限流文案仍会记账。
			// 周期 1 要求行为零变化，此边界保留原语义并在此显式锁定，
			// 是否收紧为「400 一律不记账」留待实施总览的待裁定项确认。
			name:   "400 携带限流文案按账号×模型记账（复刻历史语义）",
			status: 400,
			body:   `{"error":"too many requests"}`,
			want:   failureScopeAuthModel,
		},
		{
			name:   "正常 200 正文不冷却",
			status: 200,
			body:   `{"choices":[{"message":{"content":"hi"}}]}`,
			want:   failureScopeNone,
		},
		// 上游拥堵维度：渠道满载不罚号，模型级限流只隔离该模型。
		{
			name:   "429 带 6021 渠道满载归拥堵",
			status: 429,
			body:   `{"code":6021,"msg":"queue.full.title"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			name:   "400 带 queue.full 文案归拥堵",
			status: 400,
			body:   `{"error":"queue.full, please retry later"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			name:   "503 带 6021 仍归拥堵（先于 5xx 判定）",
			status: 503,
			body:   `{"code": 6021, "msg": "capacity exhausted"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			name:   "429 带 6004 归模型级限流",
			status: 429,
			body:   `{"code":6004,"message":"usage exceeds frequency limit"}`,
			want:   failureScopeModelRateLimit,
		},
		{
			name:   "500 带 6004 仍归模型级限流（先于 5xx 判定）",
			status: 500,
			body:   `{"code": 6004, "message": "将在 2026-10-31 20:00:00 重置"}`,
			want:   failureScopeModelRateLimit,
		},
		{
			name:   "字符串形态 6004 同样命中",
			status: 429,
			body:   `{"code":"6004","message":"model rate limited"}`,
			want:   failureScopeModelRateLimit,
		},
		{
			// 渠道满载比模型限流更具体：两者同时出现时按拥堵处理，
			// 因为此时换模型也会撞同一堵墙。
			name:   "6021 与 6004 同时出现时拥堵优先",
			status: 429,
			body:   `{"code":6021,"message":"queue full for model 6004"}`,
			want:   failureScopeUpstreamBusy,
		},
		{
			// 硬额度先于拥堵判定：额度耗尽不是容量问题。
			name:   "402 带 queue.full 仍归账号级硬额度",
			status: 402,
			body:   `{"code":6021,"message":"queue.full but payment required"}`,
			want:   failureScopeAccount,
		},
		{
			// 11128 是账号×模型拦截，不能被 6004 的判定吞掉。
			name:   "400 带 11128 归账号×模型拦截",
			status: 400,
			body:   `{"code":11128,"msg":"request illegal"}`,
			want:   failureScopeAuthModel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamFailure(tc.status, tc.body); got != tc.want {
				t.Fatalf("classifyUpstreamFailure(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyUpstreamFailureMatchesLegacyCountGate 锁定与历史记账判据的等价关系：
// 重构前 recordAccountFailure 的门槛是
// isAccountFailure(status, body) || isTransientThrottle(status, body)，
// 只要分类不是 failureScopeNone 就必须为真，反之亦然。
// 遍历一组枚举状态与代表性正文，避免改动分类时悄悄放宽或收紧记账范围。
//
// 唯一例外是 6021 渠道满载（2026-10-31 引入）：历史实现会把它按限流逐号冷却，
// 而新实现刻意不记账。该差异由下方的显式断言单独锁定，不属于「等价性被破坏」。
func TestClassifyUpstreamFailureMatchesLegacyCountGate(t *testing.T) {
	bodies := []string{
		"",
		"connection reset by peer",
		`{"error":"invalid request body"}`,
		`{"error":"unprocessable entity"}`,
		`{"error":"unauthorized"}`,
		`{"error":"forbidden"}`,
		`{"error":"not found"}`,
		`{"error":"method not allowed"}`,
		`{"error":"too many requests"}`,
		`{"message":"rate limit exceeded"}`,
		`{"message":"insufficient credits"}`,
		`{"message":"payment required"}`,
		"upstream stream closed before first payload",
		"invalid SSE response: missing output and done event",
		`{"choices":[{"message":{"content":"hi"}}]}`,
	}
	statuses := []int{0, 200, 400, 401, 402, 403, 404, 405, 422, 429, 500, 502, 503, 504}

	for _, status := range statuses {
		for _, body := range bodies {
			legacy := isAccountFailure(status, body) || isTransientThrottle(status, body)
			counted := classifyUpstreamFailure(status, body).recordsCooldown()
			if legacy != counted {
				t.Fatalf("记账判据不一致 status=%d body=%q: legacy=%v classified=%v",
					status, body, legacy, counted)
			}
		}
	}
}

// TestQueueFullNotCounted 单独锁定 6021 渠道满载的免罚语义。
//
// 历史实现把渠道满载当成普通限流逐号冷却，参考实现记录 2026-10-15 实案：
// 按该口径处置会在 2 秒内把整池健康号逐个冷却。新实现刻意偏离历史门槛，
// 因此这里显式断言「历史会记账、新实现不记账」，防止后续重构悄悄改回去。
// 最近修改时间：2026-10-31；改动原因：新增，锁定拥堵维度的免罚例外。
func TestQueueFullNotCounted(t *testing.T) {
	busyBodies := []string{
		`{"code":6021,"msg":"queue.full.title"}`,
		`{"code": 6021}`,
		`{"code":"6021"}`,
		`{"error":"queue.full, please retry later"}`,
		`{"error":"Queue Full"}`,
	}
	for _, body := range busyBodies {
		// 1. 历史门槛确实会记账（这正是误杀整池的来源）
		if !(isAccountFailure(429, body) || isTransientThrottle(429, body)) {
			t.Fatalf("前置条件不成立：历史门槛应记账 body=%q", body)
		}
		// 2. 新分类不记账、不禁用
		scope := classifyUpstreamFailure(429, body)
		if scope != failureScopeUpstreamBusy {
			t.Fatalf("classifyUpstreamFailure(429, %q) = %v, want upstream_busy", body, scope)
		}
		if scope.recordsCooldown() {
			t.Fatalf("渠道满载不得记账 body=%q", body)
		}
		if scope.disablesAccount() {
			t.Fatalf("渠道满载不得写盘禁用 body=%q", body)
		}
	}
}

// TestModelRateLimitDoesNotDisable 锁定 6004 的「只隔离模型、不禁用账号」语义。
// 6004 表明账号整体健康、只是该模型当下限流；禁用会让健康号退出路由，
// 在高峰期把可用池无谓缩小。
// 最近修改时间：2026-10-31；改动原因：新增，锁定模型级限流的禁用边界。
func TestModelRateLimitDoesNotDisable(t *testing.T) {
	for _, body := range []string{
		`{"code":6004,"message":"usage exceeds frequency limit"}`,
		`{"code": 6004}`,
		`{"code":"6004"}`,
	} {
		scope := classifyUpstreamFailure(429, body)
		if scope != failureScopeModelRateLimit {
			t.Fatalf("classifyUpstreamFailure(429, %q) = %v, want model_rate_limit", body, scope)
		}
		// 仍要冷却该模型（否则同一模型会被反复撞），但不得禁用账号。
		if !scope.recordsCooldown() {
			t.Fatalf("模型级限流必须冷却该模型 body=%q", body)
		}
		if scope.disablesAccount() {
			t.Fatalf("模型级限流不得写盘禁用 body=%q", body)
		}
	}
}

// TestRecordAccountFailureHonorsClassification 验证记账入口真的按分类结果执行：
// 400 不入账、429 入账、402 入账，且计数按阶梯递增。
func TestRecordAccountFailureHonorsClassification(t *testing.T) {
	clearFailoverStates()
	t.Cleanup(clearFailoverStates)

	const authID = "auth-class"

	// 1. 请求形状失败不入账，不产生任何状态
	if recordAccountFailure(authID, 400, `{"error":"invalid request body"}`) {
		t.Fatal("400 不应计入冷却")
	}
	if _, _, ok := failoverStateSnapshot(authID); ok {
		t.Fatal("400 不应留下冷却状态")
	}

	// 2. 429 入账并进入冷却
	if !recordAccountFailure(authID, 429, `{"error":"too many requests"}`) {
		t.Fatal("429 应计入冷却")
	}
	count, cooldownUntil, ok := failoverStateSnapshot(authID)
	if !ok || count != 1 || cooldownUntil.IsZero() {
		t.Fatalf("429 应产生 1 次计数与冷却截止时间，got count=%d ok=%v", count, ok)
	}

	// 3. 402 继续累加，验证阶梯复用同一计数
	if !recordAccountFailure(authID, 402, `{"error":"payment required"}`) {
		t.Fatal("402 应计入冷却")
	}
	count, _, _ = failoverStateSnapshot(authID)
	if count != 2 {
		t.Fatalf("连续失败计数应为 2，got %d", count)
	}
}

// TestFailureScopeString 锁定分类名，供日志与面板展示保持一致。
func TestFailureScopeString(t *testing.T) {
	cases := map[failureScope]string{
		failureScopeNone:           "none",
		failureScopeAccount:        "account",
		failureScopeAuthModel:      "account_model",
		failureScopeModelRateLimit: "model_rate_limit",
		failureScopeUpstreamBusy:   "upstream_busy",
	}
	for scope, want := range cases {
		if got := scope.String(); got != want {
			t.Fatalf("failureScope(%d).String() = %q, want %q", scope, got, want)
		}
	}
}

// TestFailureScopeRecordAndDisableMatrix 用一张矩阵锁定两个派生判据的组合，
// 避免后续新增维度时漏掉其中一侧：
//   - recordsCooldown：是否写冷却；
//   - disablesAccount：是否写盘禁用。
//
// 三个结论都是刻意的设计选择，不是实现细节。
// 最近修改时间：2026-10-31；改动原因：新增，锁定维度与副作用的关系。
func TestFailureScopeRecordAndDisableMatrix(t *testing.T) {
	cases := []struct {
		scope        failureScope
		wantCooldown bool
		wantDisable  bool
	}{
		// 不冷却：请求形状问题换号同样失败。
		{failureScopeNone, false, false},
		// 账号级：冷却 + 禁用，两者都要。
		{failureScopeAccount, true, true},
		// 账号×模型拦截（11128）：冷却该模型并禁用账号（探活按模型回放）。
		{failureScopeAuthModel, true, true},
		// 模型级限流（6004）：冷却模型，但账号健康，不得禁用。
		{failureScopeModelRateLimit, true, false},
		// 渠道满载（6021）：既不冷却也不禁用，罚号只会放大拥堵。
		{failureScopeUpstreamBusy, false, false},
	}
	for _, tc := range cases {
		if got := tc.scope.recordsCooldown(); got != tc.wantCooldown {
			t.Fatalf("%v.recordsCooldown() = %v, want %v", tc.scope, got, tc.wantCooldown)
		}
		if got := tc.scope.disablesAccount(); got != tc.wantDisable {
			t.Fatalf("%v.disablesAccount() = %v, want %v", tc.scope, got, tc.wantDisable)
		}
	}
}
