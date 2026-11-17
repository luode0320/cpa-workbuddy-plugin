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

// creationChunkFrame 构造一帧携带创作块（block_type=2074）的 STREAM_CHUNK。
//
// 面板测试的成功判据是「解析出产物」，只有文字帧会被判成「能出字但没能出图」，
// 因此验证首帧即停的用例必须发创作块帧，否则测的就不再是成功路径。
// [参数] 无。
// [返回] 完整 SSE 帧文本。
// 最近修改时间：2026-11-17 22:52:00；改动原因：判据收紧到创作块后，改用创作块帧。
func creationChunkFrame() string {
	const payload = `{"content":{"content_block":[{"block_type":2074,"content":{"creation_block":{"creations":[{"type":1,"id":"ping-1","image":{"status":1,"key":"tos-cn-i-b/rc_gen_image/ping.jpeg"}}]}}}]}}`
	return "event: STREAM_CHUNK\ndata: " + payload + "\n\n"
}

// TestRunActiveTestStopsOnFirstFrame 校验测试链路在首个有效输出帧即返回。
//
// 上游在首帧之后挂起 3 秒再结束；若实现等全文，本次调用必然超过 1 秒。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，锁定首帧即停契约。
func TestRunActiveTestStopsOnFirstFrame(t *testing.T) {
	frames := []string{
		creationChunkFrame(),
		"event: SSE_REPLY_END\ndata: {\"end_type\":1}\n\n",
	}
	srv := activePingSSEServer(t, frames)
	defer srv.Close()
	defer swapCompletionEndpoint(srv.URL + "/chat/completion")()

	sa := completionTestAccount()
	start := time.Now()
	res := runActiveTest(sa, "doubao/image")
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

	res := runActiveTest(completionTestAccount(), "doubao/image")
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

	res := runActiveTest(completionTestAccount(), "doubao/image")
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
// 目录里只剩生图与生视频：两者都会真正触发上游生成（耗额度、等数十秒），这是
// 「只做图片和视频」下无法回避的代价。
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
	// 目录里只剩生图与生视频，两者都必须出现在可测试列表里。
	for _, m := range models {
		if !strings.HasPrefix(m, modelIDPrefix) {
			t.Fatalf("模型名必须带 doubao/ 前缀，实际 %q", m)
		}
	}
	for _, want := range []string{modelIDPrefix + "image", modelIDPrefix + "video"} {
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

// TestRunActiveTestRejectsRemovedWorkModels 校验已删的工作模式模型名被拒。
//
// 0.1.26 起插件只做图片与视频，deepseek / kimi / glm 这些工作模式模型已从目录
// 移除。它们必须直接报「不支持的测试模型」，不能因为名字眼熟就被静默接受——
// 否则面板点下去会真的发一次上游请求，白白消耗账号额度。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:08:00；改动原因：新增，锁定工作模式模型已不可测。
func TestRunActiveTestRejectsRemovedWorkModels(t *testing.T) {
	removed := []string{
		modelIDPrefix + "deepseek-v4-flash",
		modelIDPrefix + "deepseek-v4-pro",
		modelIDPrefix + "kimi-k3",
		modelIDPrefix + "glm-5.3",
		modelIDPrefix + "default",
		modelIDPrefix + "pro",
	}
	for _, model := range removed {
		res := runActiveTest(completionTestAccount(), model)
		if res["ok"] == true {
			t.Errorf("%s 已从目录删除，不应判成功，实际 %+v", model, res)
			continue
		}
		if msg, _ := res["error"].(string); !strings.Contains(msg, "不支持的测试模型") {
			t.Errorf("%s 应报「不支持的测试模型」，实际 %q", model, msg)
		}
	}
}

// TestRunActiveTestAcceptsBothCapabilities 校验 image 与 video 都被认。
//
// 两个别名都必须能进到上游请求那一层（而不是在解析阶段就被判不支持）；
// 上游用不可达地址模拟，只验证「没有报不支持的测试模型」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:08:00；改动原因：新增，锁定两条能力别名都能测。
func TestRunActiveTestAcceptsBothCapabilities(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	defer swapCompletionEndpoint(deadURL)()

	for _, model := range []string{modelIDPrefix + "image", modelIDPrefix + "video"} {
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
	res := handleTestActive(pluginapi.ManagementRequest{Body: []byte(`{"model":"doubao/image"}`)})
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
			completionTestAccount(), testPingPromptFor("image"), textMode(), "", "", 0, true,
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

// TestActiveTestSawCreationDetectsCreationBlock 校验创作块是唯一的产物证据。
//
// 收紧判据的靶点：文字帧只能证明「账号能聊天」，一张图也没出。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:36:00；改动原因：新增，锁住创作块判据。
func TestActiveTestSawCreationDetectsCreationBlock(t *testing.T) {
	// 1. 只有文字：不是产物证据。
	textOnly := `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"已为你生成图片"}}}]}}`
	if activeTestSawCreation(completionEvent{Name: "STREAM_MSG_NOTIFY", Data: []byte(textOnly)}) {
		t.Error("纯文字帧不该被当作产物证据")
	}

	// 2. 创作块带完整图片：是产物证据。
	creation := `{"content":{"content_block":[{"block_type":2074,"content":{"creation_block":{"creations":[{"type":1,"id":"a-1","image":{"status":1,"key":"tos-cn-i-b/rc_gen_image/x.jpeg"}}]}}}]}}`
	if !activeTestSawCreation(completionEvent{Name: "STREAM_MSG_NOTIFY", Data: []byte(creation)}) {
		t.Error("带 key 的创作块应被当作产物证据")
	}

	// 3. 提问回显里的创作块不算：那是上游把请求原样发回来。
	echo := `{"message":{"user_type":1,"content_block":[{"block_type":2074,"content":{"creation_block":{"creations":[{"type":1,"id":"a-2","image":{"status":1,"key":"tos-cn-i-b/rc_gen_image/y.jpeg"}}]}}}]}}`
	if activeTestSawCreation(completionEvent{Name: "FULL_MSG_NOTIFY", Data: []byte(echo)}) {
		t.Error("提问回显里的创作块不该算产物")
	}
}

// TestCapabilityVerb 校验失败文案用的能力动作词。
//
// 文案必须说「没能出图 / 没能出片」而不是「没能生成图片」：前者是用户视角的
// 动作，后者读起来像在描述缺失的功能模块。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:52:00；改动原因：只留 capabilityVerb 断言，用例名同步。
func TestCapabilityVerb(t *testing.T) {
	if capabilityVerb("image") != "出图" {
		t.Errorf("生图动作词错误：%s", capabilityVerb("image"))
	}
	if capabilityVerb("video") != "出片" {
		t.Errorf("生视频动作词错误：%s", capabilityVerb("video"))
	}
}

// TestRunActiveTestRejectsTextOnlyReply 校验「能出字但没出图」必须判失败。
//
// 这是本次收紧的核心：0.1.27 之前这种回复会被报成「活跃成功」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:36:00；改动原因：新增，假阳性回归。
func TestRunActiveTestRejectsTextOnlyReply(t *testing.T) {
	stream := "event: STREAM_MSG_NOTIFY\ndata: " +
		`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"好的，已为你生成"}}}]}}` +
		"\n\nevent: SSE_REPLY_END\ndata: {}\n\n"
	_, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()

	res := runActiveTest(sa, modelIDPrefix+"image")
	if res["ok"] == true {
		t.Fatalf("只出字不出图不该判成功：%v", res)
	}
	message, _ := res["error"].(string)
	if !strings.Contains(message, "能正常出字") || !strings.Contains(message, "出图") {
		t.Errorf("失败文案应说明「能出字但没能出图」，实际 %q", message)
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
