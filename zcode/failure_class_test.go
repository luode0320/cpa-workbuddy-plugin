package main

import "testing"

// TestClassifyUpstreamFailureTable 按实施总览第 3 节的边界表逐行断言分类结果。
// 这张表是周期 2 冷却维度扩维与周期 3 探活策略的唯一判据来源。
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
			name:   "正常 200 正文不冷却",
			status: 200,
			body:   `{"choices":[{"message":{"content":"hi"}}]}`,
			want:   failureScopeNone,
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

// TestNoteAccountFailureHonorsClassification 验证记账入口已按分类执行：
// 请求形状失败不冷却，账号级与限流类失败仍照常记账。
// 本用例是周期 1 哨兵 TestNoteAccountFailureStillCountsUnconditionally 的
// 正向改写——哨兵失败即代表分类门禁已接入。
func TestNoteAccountFailureHonorsClassification(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		errMsg   string
		wantCool bool
	}{
		{
			name:     "400 请求形状不记账",
			status:   400,
			errMsg:   `upstream status 400: {"error":"invalid request body"}`,
			wantCool: false,
		},
		{
			name:     "422 请求形状不记账",
			status:   422,
			errMsg:   `upstream status 422: {"error":"unprocessable entity"}`,
			wantCool: false,
		},
		{
			name:     "401 账号级记账",
			status:   401,
			errMsg:   `upstream status 401: {"error":"unauthorized"}`,
			wantCool: true,
		},
		{
			name:     "429 限流记账",
			status:   429,
			errMsg:   `upstream status 429: {"error":"rate limit"}`,
			wantCool: true,
		},
		{
			name:     "传输错误记账",
			status:   0,
			errMsg:   "connection reset by peer",
			wantCool: true,
		},
		{
			name:     "400 携带限流文案仍记账",
			status:   400,
			errMsg:   `{"error":"too many requests"}`,
			wantCool: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 冷却状态下线后，「记账」的观测点是盘上禁用：分类命中才写盘。
			h := newFailoverFakeHost(t)
			h.addAccount("zcode-classified", "idx-cls", nil)

			noteAccountFailure("zcode-classified", tc.status, tc.errMsg)

			got := h.docOf("idx-cls")["disabled"] == true
			if got != tc.wantCool {
				t.Fatalf("noteAccountFailure(%d, %q) disabled = %v, want %v", tc.status, tc.errMsg, got, tc.wantCool)
			}
		})
	}
}

// TestStreamReadErrorStillCounts 锁定 T-2 里最容易出错的一条：
// 流式读错误必须按传输层失败（status 0）记账。
//
// 若某次改动把这里改回传 resp.StatusCode（读取阶段恒为 200），
// classifyUpstreamFailure 会判为成功响应，真实断流将不再进入冷却。
// 本用例用与生产调用点相同的入参形态直接断言记账结果。
func TestStreamReadErrorStillCounts(t *testing.T) {
	const readErr = "unexpected EOF"

	// 1. 传 0（正确形态）：必须写盘禁用。
	h := newFailoverFakeHost(t)
	h.addAccount("zcode-stream-err", "idx-sr", nil)
	noteAccountFailure("zcode-stream-err", 0, readErr)
	if h.docOf("idx-sr")["disabled"] != true {
		t.Fatal("流式读错误按 status 0 记账后应写盘禁用")
	}

	// 2. 传 200（错误形态）：不应记账——这正是必须避免的漏记形态。
	h2 := newFailoverFakeHost(t)
	h2.addAccount("zcode-stream-err2", "idx-sr2", nil)
	noteAccountFailure("zcode-stream-err2", 200, readErr)
	if h2.docOf("idx-sr2")["disabled"] == true {
		t.Fatal("status 200 不应记账；若此断言失败说明分类把成功响应误判为失败")
	}
}

// TestIsAccountLevel4xxExcludesRateLimit 验证账号级 4xx 判定不含 429，
// 429 必须由账号×模型维度承担。
func TestIsAccountLevel4xxExcludesRateLimit(t *testing.T) {
	for _, status := range []int{401, 403, 404, 405} {
		if !isAccountLevel4xx(status) {
			t.Fatalf("%d 应为账号级 4xx", status)
		}
	}
	if isAccountLevel4xx(429) {
		t.Fatal("429 不应计入账号级 4xx")
	}
	if isAccountLevel4xx(400) {
		t.Fatal("400 不应计入账号级 4xx")
	}
}
