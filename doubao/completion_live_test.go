package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveAccountForTest 读取真实 Cookie 并构造一个用于真实上游调用的账号。
//
// 凭据来自 doubao/.local_cookie.txt（已 gitignore，仅 local 使用）。
// 设备标识在测试内固定生成，避免写回真实账号目录。
// [参数] t: 测试上下文。
// [返回] 账号；文件缺失时返回 nil（调用方应跳过）。
// 最近修改时间：2026-10-17；改动原因：新增，真实链路测试的账号装配。
func liveAccountForTest(t *testing.T) *StoredAuth {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join("doubao", ".local_cookie.txt"))
	if errRead != nil {
		raw, errRead = os.ReadFile(".local_cookie.txt")
	}
	if errRead != nil {
		return nil
	}
	c, errParse := parseCredentials(string(raw))
	if errParse != nil {
		t.Fatalf("真实 Cookie 解析失败：%v", errParse)
	}
	out, errProbe := probeAccountInfo(c, 20*time.Second)
	if errProbe != nil {
		t.Fatalf("真实 Cookie 应通过判据：%v", errProbe)
	}
	return &StoredAuth{
		Type:          providerName,
		Provider:      providerName,
		AuthID:        authIDFor(out),
		CookieHeader:  out.CookieHeader,
		FlowSecUserID: out.FlowSecUserID,
		SecUserID:     out.SecUserID,
		ScreenName:    out.ScreenName,
		BotID:         defaultBotID,
		DeviceID:      "7300000000000000001",
		WebID:         "7300000000000000002",
		WebTabID:      "00000000-0000-4000-8000-000000000001",
		Label:         "真实链路测试",
	}
}

// TestCompletionLiveTextSmoke 用真实账号跑通一轮对话。
//
// 验证：① 生产 query 参数集与请求体被上游接受；② SSE 流可解析；
// ③ 能提取到非空文本回复。默认跳过，设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，TASK-004 真实冒烟（通过标准②）。
func TestCompletionLiveTextSmoke(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过真实链路冒烟")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, errRun := collectCompletion(ctx, sa, "1+1=?", textMode(), "", true, nil)
	if errRun != nil {
		t.Fatalf("真实对话失败：%v", errRun)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("真实对话应返回非空文本")
	}
	if res.ConversationID == "" {
		t.Error("上游应返回会话 id")
	}
	t.Logf("真实对话成功：回复长度=%d，会话 id 长度=%d，思考长度=%d",
		len(res.Text), len(res.ConversationID), len(res.Reasoning))
}

// TestCompletionLiveStreamDeltas 用真实账号验证流式增量回调。
//
// 验证 onDelta 在流式过程中被真实调用（通过标准②的流式侧）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流式增量真实冒烟。
func TestCompletionLiveStreamDeltas(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过流式冒烟")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var deltas int
	res, errRun := collectCompletion(ctx, sa, "请用一句话介绍你自己", textMode(), "", true, func(string) {
		deltas++
	})
	if errRun != nil {
		t.Fatalf("真实流式对话失败：%v", errRun)
	}
	if deltas == 0 {
		t.Fatal("流式请求应触发至少一次增量回调")
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("流式对话应返回非空文本")
	}
	t.Logf("真实流式对话成功：增量回调 %d 次，回复长度=%d", deltas, len(res.Text))
}

// TestCompletionLiveReplyNotTruncated 校验提前退出不会截断回复。
//
// 正文在首个 SSE_REPLY_END 之前到齐，本用例用确定性任务验证完整性：
// 要求模型列出 1~5，回复必须包含全部五个数字。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防「提前退出截断」回归。
func TestCompletionLiveReplyNotTruncated(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过完整性验证")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, errRun := collectCompletion(ctx, sa, "请只输出 1 到 5 这五个数字，用空格分隔，不要任何其他文字", textMode(), "", true, nil)
	if errRun != nil {
		t.Fatalf("真实对话失败：%v", errRun)
	}
	for _, digit := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(res.Text, digit) {
			t.Fatalf("回复缺少数字 %s，说明流被提前截断：%q", digit, res.Text)
		}
	}
	t.Logf("完整性验证通过：回复长度=%d，1~5 全部出现", len(res.Text))
}
