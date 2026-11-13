package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// liveExecutorRequest 用真实账号构造一份宿主执行请求。
// [参数] t: 测试上下文；payload: OpenAI 请求体。
// [返回] 请求 JSON；无凭据时返回 nil（调用方应跳过）。
// 最近修改时间：2026-10-17；改动原因：新增，端到端验证的请求装配。
func liveExecutorRequest(t *testing.T, payload string) []byte {
	t.Helper()
	sa := liveAccountForTest(t)
	if sa == nil {
		return nil
	}
	storage, errMarshal := json.Marshal(sa)
	if errMarshal != nil {
		t.Fatalf("序列化账号失败：%v", errMarshal)
	}
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{
		AuthID:       sa.AuthID,
		AuthProvider: providerName,
		Model:        "doubao/default",
		Payload:      []byte(payload),
		StorageJSON:  storage,
	})
	return raw
}

// TestExecutorLiveNonStream 走完整 executor 非流式链路调用真实上游。
//
// 覆盖：宿主请求解析 → 提示词折叠 → 真实 SSE → OpenAI 响应构造。
// 默认跳过，设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，AC-004 非流式端到端。
func TestExecutorLiveNonStream(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	raw := liveExecutorRequest(t, `{"model":"doubao/default","messages":[{"role":"user","content":"1+1=?"}]}`)
	if raw == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过端到端验证")
	}

	out, errRun := executorExecute(raw)
	if errRun != nil {
		t.Fatalf("非流式执行失败：%v", errRun)
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Payload []byte `json:"Payload"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
		t.Fatalf("响应应为合法信封：%v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("执行应成功：%s", truncateText(string(out), 300))
	}

	var resp map[string]any
	if errUnmarshal := json.Unmarshal(env.Result.Payload, &resp); errUnmarshal != nil {
		t.Fatalf("Payload 应为 OpenAI JSON：%v", errUnmarshal)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("object 应为 chat.completion，实际 %v", resp["object"])
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices 结构错误：%+v", resp["choices"])
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if strings.TrimSpace(content) == "" {
		t.Fatal("真实非流式响应应有正文")
	}
	t.Logf("非流式端到端成功：正文长度=%d，会话 id 长度=%d", len(content), len(asString(resp["conversation_id"])))
}

// TestExecutorLiveStreamSync 走完整 executor 流式链路（同步收集模式）调用真实上游。
//
// 覆盖：增量帧转换、首帧 role、终止帧 finish_reason=stop。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，AC-004 流式端到端。
func TestExecutorLiveStreamSync(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	raw := liveExecutorRequest(t, `{"model":"doubao/default","stream":true,"messages":[{"role":"user","content":"请用一句话介绍你自己"}]}`)
	if raw == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过流式端到端验证")
	}

	out, errRun := executorExecuteStream(raw)
	if errRun != nil {
		t.Fatalf("流式执行失败：%v", errRun)
	}

	var env struct {
		Result struct {
			Headers map[string][]string `json:"headers"`
			Chunks  []struct {
				Payload []byte `json:"Payload"`
			} `json:"chunks"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
		t.Fatalf("响应应为合法信封：%v", errUnmarshal)
	}
	if len(env.Result.Chunks) < 2 {
		t.Fatalf("应至少产生增量帧与终止帧，实际 %d 帧", len(env.Result.Chunks))
	}

	var assembled strings.Builder
	for i, chunk := range env.Result.Chunks {
		var frame map[string]any
		if errUnmarshal := json.Unmarshal(chunk.Payload, &frame); errUnmarshal != nil {
			t.Fatalf("第 %d 帧不是合法 JSON：%v", i, errUnmarshal)
		}
		choices, _ := frame["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("第 %d 帧 choices 结构错误：%+v", i, frame)
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			assembled.WriteString(c)
		}
		if i == 0 && delta["role"] != "assistant" {
			t.Errorf("首帧应带 role=assistant，实际 %+v", delta)
		}
	}

	last := env.Result.Chunks[len(env.Result.Chunks)-1]
	var lastFrame map[string]any
	_ = json.Unmarshal(last.Payload, &lastFrame)
	if lastFrame["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
		t.Errorf("末帧应为终止帧，实际 %+v", lastFrame)
	}

	if strings.TrimSpace(assembled.String()) == "" {
		t.Fatal("流式帧拼接后应有正文")
	}
	t.Logf("流式端到端成功：帧数=%d，拼接正文长度=%d", len(env.Result.Chunks), assembled.Len())
}

// asString 把任意值安全转成字符串（仅用于日志）。
// [参数] v: 任意值。
// [返回] 字符串；非字符串返回空串。
// 最近修改时间：2026-10-17；改动原因：新增，日志辅助。
func asString(v any) string {
	s, _ := v.(string)
	return s
}
