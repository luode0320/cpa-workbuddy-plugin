package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// activePingSSEServer 启动一个假上游，按 frames 依次吐 SSE 帧。
//
// 多帧时在首帧之后挂起 3 秒再吐后续帧：真实上游在 Keep-Alive 下不会立刻关流，
// 若实现要等后续帧或 EOF 才返回，就会表现为「字早就出来了却一直转圈」。
// 只有真正首帧即停的实现才能在挂起期间提前返回。
// [参数] t: 测试上下文；frames: 依次下发的原始 SSE 文本。
// [返回] 服务端。
// 最近修改时间：2026-10-17；改动原因：新增，测试链路可控上游（含帧间挂起）。
func activePingSSEServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i, f := range frames {
			if i == 1 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
			if _, errWrite := w.Write([]byte(f)); errWrite != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	return srv
}

// textChunkFrame 构造一帧携带正文增量的 STREAM_CHUNK。
// [参数] text: 正文片段。
// [返回] 完整 SSE 帧文本。
// 最近修改时间：2026-10-17；改动原因：新增，测试帧构造。
func textChunkFrame(text string) string {
	payload, _ := json.Marshal(map[string]any{"delta": text})
	return "event: STREAM_CHUNK\ndata: " + string(payload) + "\n\n"
}

// TestRunActiveTestStopsOnFirstFrame 校验测试链路在首个有效输出帧即返回。
//
// 上游在首帧之后挂起 3 秒再结束；若实现等全文，本次调用必然超过 1 秒。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定首帧即停契约。
func TestRunActiveTestStopsOnFirstFrame(t *testing.T) {
	frames := []string{
		textChunkFrame("OK"),
		"event: SSE_REPLY_END\ndata: {\"end_type\":1}\n\n",
	}
	srv := activePingSSEServer(t, frames)
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	sa := completionTestAccount()
	start := time.Now()
	res := runActiveTest(sa, "doubao/default")
	elapsed := time.Since(start)

	if res["error"] != nil {
		t.Fatalf("首帧成功却报错：%v", res["error"])
	}
	if res["ok"] != true {
		t.Fatalf("应判定成功，实际 %+v", res)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("首帧即停未生效：耗时 %v", elapsed)
	}
}

// TestRunActiveTestRejectsStreamError 校验 HTTP 200 下的错误帧被判为失败。
//
// 上游用 STREAM_ERROR 表达额度耗尽/限流，只看「有字节流出」会把死号判成活跃。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，HTTP 200 假成功防护。
func TestRunActiveTestRejectsStreamError(t *testing.T) {
	frames := []string{
		"event: STREAM_ERROR\ndata: {\"error_code\":710022004,\"error_msg\":\"rate limited\"}\n\n",
	}
	srv := activePingSSEServer(t, frames)
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	res := runActiveTest(completionTestAccount(), "doubao/default")
	if res["ok"] == true {
		t.Fatalf("错误帧被误判为成功：%+v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "上游风控") {
		t.Fatalf("错误提示应含可操作说明，实际 %q", msg)
	}
}

// TestRunActiveTestRejectsEmptyReply 校验只有回复结束帧、没有正文时判失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空回复防护。
func TestRunActiveTestRejectsEmptyReply(t *testing.T) {
	srv := activePingSSEServer(t, []string{"event: SSE_REPLY_END\ndata: {\"end_type\":1}\n\n"})
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	res := runActiveTest(completionTestAccount(), "doubao/default")
	if res["ok"] == true {
		t.Fatalf("空回复被误判为成功：%+v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "没有返回任何输出") {
		t.Fatalf("应提示上游无输出，实际 %q", msg)
	}
}

// TestHandleModelsQueryFallsBackToAliases 校验上游不可达时回落到能力别名。
//
// 上游模型清单需要真实账号凭据；这里把端点指向一个必然失败的地址，确保走的是
// 回落路径。此时必须返回可用的能力别名并显式标注 source=fallback，而不是让弹窗空白。
// 生图/生视频别名不在此列：它们会真正触发上游生成，既耗额度又要等数十秒，
// 失败原因与账号不可用难区分。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：改为覆盖动态发现失败后的回落路径。
func TestHandleModelsQueryFallsBackToAliases(t *testing.T) {
	dir := isolateAccounts(t)
	resetModelCache()
	t.Cleanup(resetModelCache)
	// 指向已关闭的本地端口，模拟上游不可达，避免误打真实上游。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	defer swapLaunchEndpoint(deadURL)()

	writeLoginFixture(t, dir, "doubao-modeltest01", "模型测试号", time.Now().Format(time.RFC3339))

	res := handleModelsQuery(pluginapi.ManagementRequest{
		Query: map[string][]string{"auth_index": {"doubao-modeltest01"}},
	})
	models, _ := res["models"].([]string)
	if len(models) == 0 {
		t.Fatalf("回落时模型列表不应为空：%+v", res)
	}
	if res["source"] != "fallback" {
		t.Fatalf("上游不可达时应标注 fallback，实际 %+v", res)
	}
	if notice, _ := res["notice"].(string); !strings.Contains(notice, "能力别名") {
		t.Fatalf("回落应说明当前显示的是能力别名，实际 %q", notice)
	}
	for _, m := range models {
		if m == "doubao/image" || m == "doubao/video" {
			t.Fatalf("活跃测试不应包含生成类模型，实际 %v", models)
		}
		if !strings.HasPrefix(m, modelIDPrefix) {
			t.Fatalf("模型名必须带 doubao/ 前缀，实际 %q", m)
		}
	}
}

// TestHandleModelsQueryRejectsUnknownAccount 校验账号不存在时明确报错。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，动态发现需要先定位账号。
func TestHandleModelsQueryRejectsUnknownAccount(t *testing.T) {
	isolateAccounts(t)
	resetModelCache()
	t.Cleanup(resetModelCache)

	res := handleModelsQuery(pluginapi.ManagementRequest{
		Query: map[string][]string{"auth_index": {"doubao-doesnotexist"}},
	})
	if res["error"] == nil {
		t.Fatalf("账号不存在时应报错，实际 %+v", res)
	}
}

// TestHandleModelsQueryRequiresAuthIndex 校验缺少 auth_index 时给出可读错误。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，入参校验。
func TestHandleModelsQueryRequiresAuthIndex(t *testing.T) {
	res := handleModelsQuery(pluginapi.ManagementRequest{})
	if res["error"] == nil {
		t.Fatalf("缺少 auth_index 时应报错，实际 %+v", res)
	}
}

// TestHandleTestActiveRequiresAuthIndex 校验缺少 auth_index 时不发上游请求。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，入参校验。
func TestHandleTestActiveRequiresAuthIndex(t *testing.T) {
	res := handleTestActive(pluginapi.ManagementRequest{Body: []byte(`{"model":"doubao/default"}`)})
	if res["error"] == nil {
		t.Fatalf("缺少 auth_index 时应报错，实际 %+v", res)
	}
}

// TestManagementRoutesExposeTestEndpoints 校验两条测试路由已注册到宿主。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，防止路由漂移导致面板 404。
func TestManagementRoutesExposeTestEndpoints(t *testing.T) {
	reg := managementRegistration()
	paths := make(map[string]string, len(reg.Routes))
	for _, r := range reg.Routes {
		paths[r.Method+" "+r.Path] = r.Path
	}
	base := "/plugins/" + providerName
	if _, ok := paths["GET "+base+"/models"]; !ok {
		t.Fatalf("缺少 GET /models 路由：%+v", reg.Routes)
	}
	if _, ok := paths["POST "+base+"/test-active"]; !ok {
		t.Fatalf("缺少 POST /test-active 路由：%+v", reg.Routes)
	}
}

// TestRunActiveTestTimeoutIsBounded 校验上游不回包时测试会被超时兜住。
//
// activeTestTimeout 是 20 秒；这里用 1 秒的短超时替换，验证超时后能返回
// 可读错误而不是无限等待。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，超时兜底验证。
func TestRunActiveTestTimeoutIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- streamCompletion(ctx, completionTestAccount(), buildCompletionRequest(
			completionTestAccount(), testPingText, textMode(), "", "", 0, true,
		), func(completionEvent) bool { return true })
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("上游不回包时应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("上下文超时后仍死等上游")
	}
}

// TestActiveTestOutputIgnoresUserEcho 校验提问回显不算输出。
//
// FULL_MSG_NOTIFY 的 user_type=1 是上游把问题原样发回；把它当输出会让
// 「只回显、没回答」的异常账号被判成活跃。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，重构后锁定帧判定语义。
func TestActiveTestOutputIgnoresUserEcho(t *testing.T) {
	echo := `{"message":{"user_type":1,"content_block":[{"block_type":10000,"content":{"text_block":{"text":"hi"}}}]}}`
	if _, ok := activeTestOutput(completionEvent{Name: "FULL_MSG_NOTIFY", Data: []byte(echo)}); ok {
		t.Fatal("提问回显不应算作输出")
	}

	reply := `{"message":{"user_type":2,"content_block":[{"block_type":10000,"content":{"text_block":{"text":"你好"}}}]}}`
	text, ok := activeTestOutput(completionEvent{Name: "FULL_MSG_NOTIFY", Data: []byte(reply)})
	if !ok || text != "你好" {
		t.Fatalf("助手消息应算输出并带回文本，实际 ok=%v text=%q", ok, text)
	}
}

// TestActiveTestOutputAcceptsReasoningOnly 校验只有思考内容也算账号可用。
//
// 思考型模型首个有效帧常常是思考片段；只看正文会把它们误判为不可用。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，思考帧判定。
func TestActiveTestOutputAcceptsReasoningOnly(t *testing.T) {
	frame := `{"content":{"content_block":[{"block_type":10040,"content":{"text_block":{"text":"先想想"}}}]}}`
	if _, ok := activeTestOutput(completionEvent{Name: "STREAM_MSG_NOTIFY", Data: []byte(frame)}); !ok {
		t.Fatal("只有思考内容时也应判为有输出")
	}
}

// TestActiveTestOutputRejectsEmptyPatch 校验空文本补丁不算输出。
//
// 上游会用空 text_block 保持块 id 存活，那不是内容。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空补丁防护。
func TestActiveTestOutputRejectsEmptyPatch(t *testing.T) {
	frame := `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":""}}}]}}]}`
	if _, ok := activeTestOutput(completionEvent{Name: "STREAM_CHUNK", Data: []byte(frame)}); ok {
		t.Fatal("空文本补丁不应算作输出")
	}
}
