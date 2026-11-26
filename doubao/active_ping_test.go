package main

import (
	"context"
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

// ackFrame 构造一帧 SSE_ACK，带上游分配的会话 id。
//
// 视频两段式的首轮唯一通路证据就是它：首轮不产生产物，只有会话建起来了，
// 下一轮才谈得上「确认后开始生成」。
// [参数] conversationID: 会话 id。
// [返回] 完整 SSE 帧文本。
// 最近修改时间：2026-11-17 23:12:00；改动原因：新增，视频首轮通路判据夹具。
func ackFrame(conversationID string) string {
	return "event: SSE_ACK\ndata: " +
		`{"ack_client_meta":{"conversation_id":"` + conversationID + `","section_id":"sec-1"},"query_list":[{"message_index":1}]}` +
		"\n\n"
}

// TestRunActiveTestReturnsAfterReplyEnd 校验读到回复结束帧就收工。
//
// 判据只能等会话 id，所以不再有「首帧即停」：上游吐完结束帧后不关流，实现必须
// 认 SSE_REPLY_END 主动收工，不能等 Keep-Alive 到期。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:05:00；改动原因：判据改为会话 id，改验证读到结束帧即收工。
func TestRunActiveTestReturnsAfterReplyEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if _, errWrite := w.Write([]byte(ackFrame("conv-ping-stop") + "event: SSE_REPLY_END\ndata: {\"end_type\":1}\n\n")); errWrite != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		// 结束帧之后不再关流：等 Keep-Alive 的实现会卡在这里。
		<-r.Context().Done()
	}))
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	sa := completionTestAccount()
	start := time.Now()
	res := runActiveTest(sa, modelIDPrefix+"video")
	elapsed := time.Since(start)

	if res["error"] != nil {
		t.Fatalf("建起会话却报错：%v", res["error"])
	}
	if res["ok"] != true {
		t.Fatalf("应判定成功，实际 %+v", res)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("读到结束帧未及时收工：耗时 %v", elapsed)
	}
}

// TestRunActiveTestRejectsStreamError 校验 HTTP 200 下的错误帧被判为失败。
//
// 上游用 STREAM_ERROR 表达额度耗尽/限流/人机验证，只看「有字节流出」会把死号判成活跃。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 06:10:00；改动原因：710022004 的成因改回人机验证，断言同步。
func TestRunActiveTestRejectsStreamError(t *testing.T) {
	frames := []string{
		"event: STREAM_ERROR\ndata: {\"error_code\":710022004,\"error_msg\":\"rate limited\",\"extra\":{\"decision\":\"{\\\"type\\\":\\\"verify\\\",\\\"subtype\\\":\\\"semantic_reasoning\\\",\\\"log_id\\\":\\\"20261116055224CD36C03412A894E2F020\\\"}\",\"ack\":\"1\"}}\n\n",
	}
	srv := activePingSSEServer(t, frames)
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	res := runActiveTest(completionTestAccount(), modelIDPrefix+"video")
	if res["ok"] == true {
		t.Fatalf("错误帧被误判为成功：%+v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "人机验证") {
		t.Fatalf("错误提示应说明是人机验证，实际 %q", msg)
	}
	if !strings.Contains(msg, "20261116055224CD36C03412A894E2F020") {
		t.Fatalf("错误提示应带上游 log_id 便于对账，实际 %q", msg)
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

	res := runActiveTest(completionTestAccount(), modelIDPrefix+"video")
	if res["ok"] == true {
		t.Fatalf("空回复被误判为成功：%+v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "没有返回任何输出") {
		t.Fatalf("应提示上游无输出，实际 %q", msg)
	}
}

// TestHandleModelsQueryKeepsAliasCatalogue 校验上游不可达时仍返回插件别名目录。
//
// 弹窗的可点击项必须是插件别名（与宿主 /v1/models 同源），上游内部代号无法映射到
// 任何插件别名，选中后只会被判失败，所以代号永远不构成测试列表。
// 上游不可达时只降级掉 upstream_models，别名目录必须照旧完整。
// 目录里只剩生视频：它会真正触发上游生成（耗额度、等数十秒），这是「只做视频」
// 下无法回避的代价。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 16:05:00；改动原因：列表改为插件别名目录，回落只影响上游对照。
func TestHandleModelsQueryKeepsAliasCatalogue(t *testing.T) {
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
		t.Fatalf("模型列表不应为空：%+v", res)
	}
	if res["source"] != "aliases" {
		t.Fatalf("列表来源应固定为插件别名，实际 %+v", res)
	}
	if res["upstream_error"] == nil {
		t.Fatalf("上游不可达时应记录 upstream_error 供排障，实际 %+v", res)
	}
	if notice, _ := res["notice"].(string); !strings.Contains(notice, "仅显示插件别名目录") {
		t.Fatalf("回落应说明只剩插件别名目录，实际 %q", notice)
	}
	// 目录里只剩生视频，它必须出现在可测试列表里。
	for _, m := range models {
		if !strings.HasPrefix(m, modelIDPrefix) {
			t.Fatalf("模型名必须带 doubao/ 前缀，实际 %q", m)
		}
	}
	for _, want := range []string{modelIDPrefix + "video"} {
		found := false
		for _, m := range models {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("可测试列表必须包含 %s，实际 %v", want, models)
		}
	}
}

// TestHandleModelsQueryNeverListsUpstreamCodes 校验上游代号不进入可点击列表。
//
// 代号（如 anceba、ances brs）是豆包内部模型标识，插件侧没有任何映射表能把它
// 翻译成能力别名；把它们列出来会让用户点一个必然被判失败的按钮。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 16:05:00；改动原因：新增，锁定代号不得作为可测试项。
func TestHandleModelsQueryNeverListsUpstreamCodes(t *testing.T) {
	dir := isolateAccounts(t)
	resetModelCache()
	t.Cleanup(resetModelCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"model_list":[` +
			`{"name":"anceba","model_name":"anceba"},{"name":"ances brs","model_name":"ances brs"}]}}`))
	}))
	defer srv.Close()
	defer swapLaunchEndpoint(srv.URL + modelDiscoveryPath)()

	writeLoginFixture(t, dir, "doubao-modeltest02", "模型测试号", time.Now().Format(time.RFC3339))

	res := handleModelsQuery(pluginapi.ManagementRequest{
		Query: map[string][]string{"auth_index": {"doubao-modeltest02"}},
	})
	models, _ := res["models"].([]string)
	for _, m := range models {
		if strings.Contains(m, "anceba") || strings.Contains(m, "ances") {
			t.Fatalf("上游内部代号不得进入可测试列表，实际 %v", models)
		}
	}
	upstream, _ := res["upstream_models"].([]string)
	if len(upstream) != 2 {
		t.Fatalf("上游代号应原样附在 upstream_models 供对照，实际 %+v", res)
	}
}

// TestRunActiveTestRejectsUnknownModel 校验未知模型名直接失败而非静默替换。
//
// 旧实现把不认识的名字一律回落成某个默认模型，于是点任何上游代号都显示
// 「测试成功」，结论与所选模型无关。现在必须明确报错，并说明该用别名。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 00:52:00；改动原因：默认模型概念已消失，注释改为不指名具体模型。
func TestRunActiveTestRejectsUnknownModel(t *testing.T) {
	res := runActiveTest(completionTestAccount(), "doubao/ances brs")
	if res["ok"] == true {
		t.Fatalf("未知模型不应判成功，实际 %+v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "不支持的测试模型") {
		t.Fatalf("未知模型应给出明确错误，实际 %q", msg)
	}
}

// TestRunActiveTestAcceptsWorkModels 校验工作模式模型名被面板测试接受。
//
// 工作模式模型恢复后必须能进到上游请求那一层（而不是在解析阶段就被判不支持）；
// 上游用不可达地址模拟，只验证「没有报不支持的测试模型」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:32:00；改动原因：恢复工作模式模型，断言由拒绝改为接受。
func TestRunActiveTestAcceptsWorkModels(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	defer swapCompletionEndpoint(deadURL)()

	for _, alias := range workModelAliases {
		model := modelIDPrefix + alias.ID
		res := runActiveTest(completionTestAccount(), model)
		if msg, _ := res["error"].(string); strings.Contains(msg, "不支持的测试模型") {
			t.Errorf("%s 属工作模式目录，不应报「不支持的测试模型」，实际 %q", model, msg)
		}
	}
}

// TestRunActiveTestRejectsNonCatalogModels 校验目录外模型名被拒。
//
// 目录外的名字必须直接报「不支持的测试模型」，不能因为名字眼熟就被静默接受——
// 否则面板点下去会真的发一次上游请求，白白消耗账号额度。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:32:00；改动原因：工作模式模型移入接受测试，本测试只留目录外模型。
func TestRunActiveTestRejectsNonCatalogModels(t *testing.T) {
	removed := []string{
		modelIDPrefix + "default",
		modelIDPrefix + "pro",
		modelIDPrefix + "not-exist",
	}
	for _, model := range removed {
		res := runActiveTest(completionTestAccount(), model)
		if res["ok"] == true {
			t.Errorf("%s 不在目录内，不应判成功，实际 %+v", model, res)
			continue
		}
		if msg, _ := res["error"].(string); !strings.Contains(msg, "不支持的测试模型") {
			t.Errorf("%s 应报「不支持的测试模型」，实际 %q", model, msg)
		}
	}
}

// TestRunActiveTestAcceptsVideoCapability 校验 video 别名能被认。
//
// 别名必须能进到上游请求那一层（而不是在解析阶段就被判不支持）；上游用不可达
// 地址模拟，只验证「没有报不支持的测试模型」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:05:00；改动原因：图片能力删除，只剩生视频一条。
func TestRunActiveTestAcceptsVideoCapability(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	defer swapCompletionEndpoint(deadURL)()

	for _, model := range []string{modelIDPrefix + "video"} {
		res := runActiveTest(completionTestAccount(), model)
		if msg, _ := res["error"].(string); strings.Contains(msg, "不支持的测试模型") {
			t.Errorf("%s 应被识别为合法能力别名，实际 %q", model, msg)
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
	res := handleTestActive(pluginapi.ManagementRequest{Body: []byte(`{"model":"doubao/video"}`)})
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
			completionTestAccount(), testPingPromptFor("video"), textMode(), "", "", 0, true,
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

// TestRunActiveTestReadsPastTextFrames 校验文本帧不会终止读取。
//
// 这是面板测试假阴性的成因：上游先回一段文字再建会话，见文本就关流会永远读不到
// ACK。2026-11-17 生产实测：旧实现 2.2 秒就判失败，而同账号走正式链路真的出了图。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:05:00；改动原因：判据改为会话 id，改为文本之后等 ACK。
func TestRunActiveTestReadsPastTextFrames(t *testing.T) {
	stream := "event: STREAM_MSG_NOTIFY\ndata: " +
		`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"马上生成"}}}]}}` +
		"\n\n" + ackFrame("conv-ping-late") + "event: SSE_REPLY_END\ndata: {}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	res := runActiveTest(sa, modelIDPrefix+"video")
	if res["ok"] != true {
		t.Fatalf("文本之后到达的会话 id 应判通路正常，实际 %+v", res)
	}
}

// TestRunActiveTestVideoAcceptsResumeSignal 校验视频按「会话建起来了」判通路。
//
// 视频首轮只回参数确认文本、不产生产物，用创作块判它必然失败（第二个假阴性）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 23:12:00；改动原因：新增，视频通路判据回归。
func TestRunActiveTestVideoAcceptsResumeSignal(t *testing.T) {
	stream := ackFrame("conv-ping-1") + "event: STREAM_MSG_NOTIFY\ndata: " +
		`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"请确认参数"}}}]}}` +
		"\n\n" + "event: SSE_REPLY_END\ndata: {}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	res := runActiveTest(sa, modelIDPrefix+"video")
	if res["ok"] != true {
		t.Fatalf("视频首轮建起会话应判通路正常，实际 %+v", res)
	}
	message, _ := res["message"].(string)
	if !strings.Contains(message, "首轮通路正常") {
		t.Errorf("文案必须说明这只是首轮通路，实际 %q", message)
	}
}

// TestRunActiveTestVideoRejectsMissingResume 校验视频没建起会话必须判失败。
//
// 只回文本不给会话 id，说明上游连会话都没建，发确认轮也无从谈起。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 23:12:00；改动原因：新增，视频缺会话回归。
func TestRunActiveTestVideoRejectsMissingResume(t *testing.T) {
	stream := "event: STREAM_MSG_NOTIFY\ndata: " +
		`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"请确认参数"}}}]}}` +
		"\n\n" + "event: SSE_REPLY_END\ndata: {}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	res := runActiveTest(sa, modelIDPrefix+"video")
	if res["ok"] == true {
		t.Fatalf("没建起会话不该判成功：%v", res)
	}
	message, _ := res["error"].(string)
	if !strings.Contains(message, "没有建立会话") {
		t.Errorf("失败文案应说明会话没建起来，实际 %q", message)
	}
}

// TestRunActiveTestRejectsTextOnlyReply 校验「能出字但没建会话」必须判失败。
//
// 这是本次收紧的核心：0.1.27 之前这种回复会被报成「活跃成功」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:05:00；改动原因：图片能力删除，用例改用生视频。
func TestRunActiveTestRejectsTextOnlyReply(t *testing.T) {
	stream := "event: STREAM_MSG_NOTIFY\ndata: " +
		`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"好的，已为你生成"}}}]}}` +
		"\n\nevent: SSE_REPLY_END\ndata: {}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	res := runActiveTest(sa, modelIDPrefix+"video")
	if res["ok"] == true {
		t.Fatalf("只出字不建会话不该判成功：%v", res)
	}
	message, _ := res["error"].(string)
	if !strings.Contains(message, "能正常出字") || !strings.Contains(message, "没有建立会话") {
		t.Errorf("失败文案应说明「能出字但没建会话」，实际 %q", message)
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

// TestMeaningfulPreviewDropsPunctuationOnly 校验只有标点的预览会被丢弃。
//
// 上游在产物前常先发一个句号或换行，看着像有文本却不含信息；拼进成功文案会得到
// 「…耗时 15.1s）：。」（2026-11-17 真机复验实测到）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 23:15:00；改动原因：新增，净化测试结果预览。
func TestMeaningfulPreviewDropsPunctuationOnly(t *testing.T) {
	for _, raw := range []string{"", " ", "\n", "。", "：。", " . , ", "…"} {
		if got := meaningfulPreview(raw); got != "" {
			t.Fatalf("%q 应被判为空预览，实际 %q", raw, got)
		}
	}
	for _, raw := range []string{"已生成视频", " 。已生成视频。 ", "你好，世界"} {
		if got := meaningfulPreview(raw); got == "" {
			t.Fatalf("%q 应保留非空预览", raw)
		}
	}
}
