package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件校验视频产物的异步回灌链路。
//
// 三个环节各自可独立失败：请求体拼错上游给空消息链、只取用户消息会漏掉产物、
// 轮询判定写错会一直等到超时。夹具取 2026-11-17 真实响应的字段形状（值脱敏）。

// chainTestVideoJSON 返回一份与真实响应同形的 video 对象。
// [参数] downloadURL: 成品地址；留空则模拟 download_url 缺失、只能退到清晰度清单。
// [返回] video 段 JSON 字符串。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，回灌链路夹具。
func chainTestVideoJSON(downloadURL string) string {
	high := base64.StdEncoding.EncodeToString([]byte("https://example.com/high.mp4"))
	low := base64.StdEncoding.EncodeToString([]byte("https://example.com/low.mp4"))
	videoModel := map[string]any{
		"status":         10,
		"message":        "success",
		"video_duration": 10.08,
		"media_type":     "video",
		"poster_url":     "https://example.com/poster.jpg",
		"url_expire":     2110202807,
		"video_list": map[string]any{
			"video_0": map[string]any{"definition": "360p", "vwidth": 640, "vheight": 360, "size": 812345, "main_url": low},
			"video_1": map[string]any{"definition": "720p", "vwidth": 1280, "vheight": 720, "size": 2728762, "main_url": high},
		},
	}
	modelJSON, _ := json.Marshal(videoModel)
	video := map[string]any{
		"vid":               "v0269cg10004dbti12i7dldbulb4k8a0",
		"status":            3,
		"width":             "384",
		"height":            "216",
		"duration":          10.08,
		"video_type":        "mp4",
		"download_filehash": "5827cf1e28434e8ccd3a0ab6a8b5fb75",
		"video_model":       string(modelJSON),
		"download_url":      downloadURL,
		"cover": map[string]any{
			"key":           "cover-key",
			"image_thumb":   map[string]any{"url": "https://example.com/thumb.png", "width": 384, "height": 216},
			"image_preview": map[string]any{"url": "https://example.com/preview.png", "width": 1280, "height": 720},
		},
	}
	raw, _ := json.Marshal(video)
	return string(raw)
}

// chainTestResponse 拼一份消息链响应。
// [参数] withVideo: 是否带创作产物消息。
// [返回] 响应 JSON 字符串。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，回灌链路夹具。
func chainTestResponse(withVideo bool) string {
	assistantBlocks := []map[string]any{
		{"block_type": blockTypeText, "content": map[string]any{"text_block": map[string]any{"text": "你的视频生成好了。"}}},
	}
	if withVideo {
		creation := map[string]any{
			"block_type": blockTypeCreation,
			"content": map[string]any{
				"creation_block": map[string]any{
					"creations": []any{
						map[string]any{
							"type":  2,
							"id":    "40579236111431999",
							"video": json.RawMessage(chainTestVideoJSON("https://example.com/real-video.mp4")),
						},
					},
				},
			},
		}
		assistantBlocks = append(assistantBlocks, creation)
	}
	resp := map[string]any{
		"cmd":         chainPullCmd,
		"sequence_id": "seq-fixture",
		"status_code": 0,
		"status_desc": "OK",
		"downlink_body": map[string]any{
			"pull_singe_chain_downlink_body": map[string]any{
				"has_more": false,
				"messages": []any{
					// 用户消息里挂一个假产物：真实响应没有，但指纹上完全可能出现。
					// 带着它才能让「只扫助手消息」这条规则被哨兵验证到。
					map[string]any{
						"user_type": 1, "section_id": "s2", "index": 2,
						"content_block": []map[string]any{
							{"block_type": blockTypeText, "content": map[string]any{"text_block": map[string]any{"text": "确认，开始生成"}}},
							chainTestDecoyCreation(),
						},
					},
					map[string]any{"user_type": 2, "section_id": "s1", "index": 1, "content_block": assistantBlocks},
				},
			},
		},
	}
	raw, _ := json.Marshal(resp)
	return string(raw)
}

// TestBuildChainPullRequestShape 校验消息链拉取请求的固定字段。
//
// 三处任错一处就拉不到东西且不报错：cmd=3100、字段名 pull_singe（上游拼写少一个
// l）、scene 与 scenario 两个场景标识。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，锁定消息链请求契约。
func TestBuildChainPullRequestShape(t *testing.T) {
	req := buildChainPullRequest("38454121040780034", 0)

	if req.Cmd != chainPullCmd {
		t.Errorf("cmd 应为 %d，实际 %d", chainPullCmd, req.Cmd)
	}
	if req.Channel != chainPullChannel || req.Version != chainPullVersion {
		t.Errorf("channel/version 错误：%d/%q", req.Channel, req.Version)
	}
	if req.SequenceID == "" {
		t.Error("sequence_id 不能为空")
	}
	body := req.UplinkBody.PullBody
	if body.ConversationID != "38454121040780034" {
		t.Errorf("conversation_id 错误：%q", body.ConversationID)
	}
	if body.AnchorIndex != chainPullAnchorLatest {
		t.Errorf("anchor_index 应取最大值，实际 %d", body.AnchorIndex)
	}
	if body.ConversationType != chainConversationTypeTask {
		t.Errorf("conversation_type 应为 %d，实际 %d", chainConversationTypeTask, body.ConversationType)
	}
	if body.Limit != chainPullDefaultLimit {
		t.Errorf("limit 缺省应为 %d，实际 %d", chainPullDefaultLimit, body.Limit)
	}
	if body.Ext["pull_single_chain_scene"] != chainPullScene || body.Ext["sync_scenario"] != chainPullScenario {
		t.Errorf("场景标识错误：%v", body.Ext)
	}

	// 序列化后字段名必须是上游那套（pull_singe 的拼写不能"修正"）。
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), "pull_singe_chain_uplink_body") {
		t.Error("uplink_body 字段名应为 pull_singe_chain_uplink_body（上游拼写）")
	}
	if strings.Contains(string(raw), "pull_single_chain_uplink_body") {
		t.Error("不得把 pull_singe 纠正成 pull_single：上游认前者")
	}
}

// chainTestDecoyCreation 构造一个挂在用户消息里的假产物。
//
// 上游真实响应里用户消息不会有 creations，但指纹上完全可能因为客户端重放、
// 会话迁移等原因出现「用户消息里也挂着 creation_block」。把它做进夹具，是为了
// 让「只扫助手消息」这条规则真的能被哨兵验证到。
// [参数] 无。
// [返回] 假产物块。
// 最近修改时间：2026-11-17 01:44:00；改动原因：新增，让只扫助手消息的规则可被哨兵验证。
func chainTestDecoyCreation() map[string]any {
	return map[string]any{
		"block_type": blockTypeCreation,
		"content": map[string]any{
			"creation_block": map[string]any{
				"creations": []any{
					map[string]any{
						"type":  2,
						"id":    "40579236111431888",
						"video": json.RawMessage(chainTestVideoJSON("https://example.com/decoy-video.mp4")),
					},
				},
			},
		},
	}
}

// TestAssetsFromChainResponseExtractsVideo 校验从消息链里提取视频产物。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，回灌解析主用例。
func TestAssetsFromChainResponseExtractsVideo(t *testing.T) {
	assets, errParse := assetsFromChainResponse([]byte(chainTestResponse(true)))
	if errParse != nil {
		t.Fatalf("解析消息链失败：%v", errParse)
	}
	asset, ok := videoAssetAmong(assets)
	if !ok {
		t.Fatalf("没有提取到视频产物：%+v", assets)
	}
	if asset.URL != "https://example.com/real-video.mp4" {
		t.Errorf("视频地址应取 download_url，实际 %q", asset.URL)
	}
	if asset.Width != 384 || asset.Height != 216 {
		t.Errorf("宽高应取顶层字符串字段，实际 %dx%d", asset.Width, asset.Height)
	}
	if !asset.isVideo() {
		t.Error("产物类型应为视频")
	}
}

// TestAssetsFromChainResponseIgnoresUserMessages 校验只取助手消息。
//
// 夹具在用户消息里挂了一个地址不同的假产物：一旦放开 user_type 判断，取到的
// 会是 decoy-video.mp4 而不是 real-video.mp4。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:44:00；改动原因：夹具加假产物，让规则可被哨兵验证。
func TestAssetsFromChainResponseIgnoresUserMessages(t *testing.T) {
	assets, errParse := assetsFromChainResponse([]byte(chainTestResponse(true)))
	if errParse != nil {
		t.Fatalf("解析消息链失败：%v", errParse)
	}
	for _, asset := range assets {
		if asset.URL == "https://example.com/decoy-video.mp4" {
			t.Fatal("不得解析用户消息里的创作块：那里不该有产物，取到说明扫错了消息")
		}
	}
	if _, ok := videoAssetAmong(assets); !ok {
		t.Fatal("助手消息里的产物仍应被取到")
	}
}

// TestAssetsFromChainResponseWithoutVideo 校验没有产物时不产生资产。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:44:00；改动原因：从「只扫助手消息」里拆出来独立验证。
func TestAssetsFromChainResponseWithoutVideo(t *testing.T) {
	// 只有用户消息里的假产物时，结果必须是空。
	raw := `{"cmd":3100,"status_code":0,"downlink_body":{"pull_singe_chain_downlink_body":{"messages":[` +
		`{"user_type":1,"content_block":[` +
		`{"block_type":2074,"content":{"creation_block":{"creations":[{"type":2,"id":"x","video":{"download_url":"https://example.com/decoy.mp4","status":3}}]}}}]}]}}}`
	assets, errParse := assetsFromChainResponse([]byte(raw))
	if errParse != nil {
		t.Fatalf("解析消息链失败：%v", errParse)
	}
	if len(assets) != 0 {
		t.Errorf("用户消息里的创作块不应产出资产，实际 %+v", assets)
	}
}

// TestAssetsFromChainResponseRejectsBusinessError 校验上游业务失败被当成错误。
//
// 上游用 status_code 而非 HTTP 状态码表达失败：只看 HTTP 200 会把「空消息链」
// 当成「还没生成好」，白白轮询到超时。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，锁定业务错误识别。
func TestAssetsFromChainResponseRejectsBusinessError(t *testing.T) {
	raw := `{"cmd":3100,"status_code":10001,"status_desc":"chain not found","downlink_body":{}}`
	if _, errParse := assetsFromChainResponse([]byte(raw)); errParse == nil {
		t.Fatal("status_code 非 0 时应返回错误")
	}
	if _, errEmpty := assetsFromChainResponse(nil); errEmpty == nil {
		t.Fatal("空响应应返回错误")
	}
}

// swapVideoPollTiming 临时把轮询节奏压到毫秒级，返回恢复函数。
//
// 为什么需要它：默认间隔 10 秒、总超时 6 分钟，任何「本该跳过却进了等待」的
// 缺陷都不会失败、只会挂起，用例跑满 6 分钟才暴露，验证成本高到没人愿意做。
// [参数] interval: 轮询间隔；timeout: 总时长。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-11-17 01:58:00；改动原因：新增，让「不该等待却等待」秒级暴露。
func swapVideoPollTiming(interval, timeout time.Duration) func() {
	prevInterval, prevTimeout := videoPollInterval, videoPollTimeout
	videoPollInterval, videoPollTimeout = interval, timeout
	return func() { videoPollInterval, videoPollTimeout = prevInterval, prevTimeout }
}

// TestWaitingForVideoStopsWhenContextDone 校验上下文取消能打断轮询。
//
// 视频要等 1~5 分钟，宿主请求随时可能被客户端断开；不响应 ctx 会把 goroutine
// 钉死在轮询里，账号池被慢慢耗空。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，锁定可取消性。
func TestWaitingForVideoStopsWhenContextDone(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// 一直回「还没有产物」，逼出第二轮轮询。
		_, _ = w.Write([]byte(chainTestResponse(false)))
	}))
	defer srv.Close()

	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// 等到第一次拉取发生后再取消，避免在循环入口就退出。
		for hits == 0 {
		}
		cancel()
	}()

	sa := completionTestAccount()
	if _, errWait := waitVideoAsset(ctx, sa, "38454121040780034"); errWait == nil {
		t.Fatal("上下文取消时应返回错误")
	}
	if hits == 0 {
		t.Fatal("应至少拉取过一次消息链")
	}
}

// TestPostChainJSONSendsIMContentType 校验 /im/* 族用的是带 encoding 的 Content-Type。
//
// /im/* 与 /chat/completion 的要求正好相反：前者必须显式声明 encoding=utf-8，
// 后者必须不带。2026-11-17 用纯 application/json 拉消息链被上游回
// 712012002「不支持编码类型」，这条断言就是为了不再踩第二次。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:49:00；改动原因：新增，锁定 /im/* 的请求头。
func TestPostChainJSONSendsIMContentType(t *testing.T) {
	var gotType, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(chainTestResponse(true)))
	}))
	defer srv.Close()

	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	if _, errFetch := fetchChainAssets(context.Background(), sa, "38454121040780034"); errFetch != nil {
		t.Fatalf("拉取消息链失败：%v", errFetch)
	}
	if gotType != contentTypeIM {
		t.Errorf("/im/* 的 Content-Type 应为 %q，实际 %q", contentTypeIM, gotType)
	}
	if !strings.Contains(gotType, "encoding=utf-8") {
		t.Error("缺 encoding=utf-8 声明会被上游以 712012002 拒绝")
	}
	if gotAccept == "" {
		t.Error("Accept 不应为空")
	}
}

// videoConfirmResume 是确认轮的续接点夹具。
//
// 用真实形态的三个字段：只要有一个非空，resume.isZero() 就是 false，
// 插件据此认定「这一次是确认轮」。
//
// [返回] 续接点。
// 最近修改时间：2026-11-17 02:38:00；改动原因：新增，回灌判据从「视频模式」改成「确认轮」。
func videoConfirmResume() resumePoint {
	return resumePoint{
		ConversationID: "38454121040780034",
		SectionID:      "38454121040780290",
		MessageIndex:   5,
	}
}

// TestEnsureVideoAssetSkipsFirstRound 校验首轮不该进回灌。
//
// 这是 2026-11-17 02:33 生产事故的回归用例：首轮请求同时满足「视频模式」与
// 「有 conversation_id」，旧判据只看模式，于是首轮就进回灌、白等满 6 分钟再回 503。
// 首轮的正确行为是立刻返回、只带参数确认文本与续接点。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 02:38:00；改动原因：新增，钉死「只有确认轮才回灌」。
func TestEnsureVideoAssetSkipsFirstRound(t *testing.T) {
	restoreTiming := swapVideoPollTiming(5*time.Millisecond, 20*time.Millisecond)
	defer restoreTiming()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 永远回「还没有产物」——只要有人进回灌就必然超时失败。
		_, _ = w.Write([]byte(chainTestResponse(false)))
	}))
	defer srv.Close()
	restoreEndpoint := swapBusinessEndpoint(srv.URL)
	defer restoreEndpoint()

	sa := completionTestAccount()
	// 首轮的 res 里确实有 conversation_id（上游在 SSE_ACK 里就给了），
	// 所以「有会话 id」不能当作回灌依据。
	res := &completionResult{ConversationID: "38454121040780034"}
	if errVideo := ensureVideoAsset(sa, generationMode{Skill: skillVideoGen, Kind: "video"}, resumePoint{}, res); errVideo != nil {
		t.Fatalf("首轮不该回灌，却报了错：%v", errVideo)
	}
	if len(res.Assets) != 0 {
		t.Errorf("首轮不该被塞进资产：%+v", res.Assets)
	}
}

// TestEnsureVideoAssetSkipsOtherModes 校验非视频模式、已有产物、无会话 id 都不回灌。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 02:38:00；改动原因：改用 resume 入参并合并三条短路条件。
func TestEnsureVideoAssetSkipsOtherModes(t *testing.T) {
	restoreTiming := swapVideoPollTiming(5*time.Millisecond, 20*time.Millisecond)
	defer restoreTiming()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chainTestResponse(false)))
	}))
	defer srv.Close()
	restoreEndpoint := swapBusinessEndpoint(srv.URL)
	defer restoreEndpoint()

	sa := completionTestAccount()
	resume := videoConfirmResume()

	imageRes := &completionResult{ConversationID: "38454121040780034"}
	if errVideo := ensureVideoAsset(sa, generationMode{Skill: skillImageGen, Kind: "image"}, resume, imageRes); errVideo != nil {
		t.Fatalf("生图模式不该回灌：%v", errVideo)
	}
	if len(imageRes.Assets) != 0 {
		t.Errorf("生图模式不该被塞进资产：%+v", imageRes.Assets)
	}

	// 视频确认轮但已经拿到产物（短时长视频会在 SSE 里直接带出来）。
	withAsset := &completionResult{ConversationID: "38454121040780034", Assets: []creationAsset{{Type: 2, URL: "https://example.com/already.mp4"}}}
	if errVideo := ensureVideoAsset(sa, generationMode{Skill: skillVideoGen, Kind: "video"}, resume, withAsset); errVideo != nil {
		t.Fatalf("已有产物时不该回灌：%v", errVideo)
	}
	if len(withAsset.Assets) != 1 {
		t.Errorf("已有产物时不该再加一个：%+v", withAsset.Assets)
	}

	// 视频确认轮但没有会话 id：无从回灌，直接放过（错误在 waitVideoAsset 里报）。
	noConv := &completionResult{}
	if errVideo := ensureVideoAsset(sa, generationMode{Skill: skillVideoGen, Kind: "video"}, resume, noConv); errVideo != nil {
		t.Fatalf("没有会话 id 时应直接放过：%v", errVideo)
	}
}

// TestEnsureVideoAssetAppendsFetchedAsset 校验确认轮会把回灌到的产物补进结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 02:38:00；改动原因：改用 resume 入参。
func TestEnsureVideoAssetAppendsFetchedAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chainTestResponse(true)))
	}))
	defer srv.Close()

	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	res := &completionResult{ConversationID: "38454121040780034"}
	if errVideo := ensureVideoAsset(sa, generationMode{Skill: skillVideoGen, Kind: "video"}, videoConfirmResume(), res); errVideo != nil {
		t.Fatalf("回灌失败：%v", errVideo)
	}
	if len(res.Assets) != 1 || res.Assets[0].URL != "https://example.com/real-video.mp4" {
		t.Fatalf("回灌结果错误：%+v", res.Assets)
	}
}

// TestWaitingForVideoRejectsEmptyConversation 校验会话 id 为空时立刻失败。
//
// 没有 conversation_id 就无从回灌，等下去只是把超时从 6 分钟拖到更久。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:42:00；改动原因：新增，快速失败。
func TestWaitingForVideoRejectsEmptyConversation(t *testing.T) {
	sa := completionTestAccount()
	if _, errWait := waitVideoAsset(context.Background(), sa, ""); errWait == nil {
		t.Fatal("会话 id 为空时应立刻返回错误")
	}
}
