package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// creationVideoFirstFrame 是一个视频的首帧：只有状态，没有 key 与地址。
const creationVideoFirstFrame = `{"patch_op":[{"patch_object":1,"patch_type":2,"patch_value":{"content_block":[{"block_type":2074,"block_id":"blk-1","content":{"creation_block":{"creations":[{"type":2,"id":"v-1","video":{"status":1}}]}},"is_finish":false,"patch_type":2}]}}]}`

// creationVideoFinalFrame 是同一个视频的末帧：补齐 download_url 与时长。
const creationVideoFinalFrame = `{"patch_op":[{"patch_object":1,"patch_type":2,"patch_value":{"content_block":[{"block_type":2074,"block_id":"blk-1","content":{"creation_block":{"creations":[{"type":2,"id":"v-1","video":{"vid":"v0269cg","status":3,"width":"384","height":"216","duration":10.08,"video_type":"mp4","download_url":"https://example.com/v.mp4","download_filehash":"abc123","cover":{"key":"tos-cn-p-bucket/cover","image_preview":{"url":"https://example.com/cover.png","width":1280,"height":720}}}}]}},"is_finish":false,"patch_type":2}]}}]}`

// TestParseCreationBlockVideo 校验视频资源按真实结构解析。
//
// 夹具取自 2026-11-17 的真实 `/im/chain/single` 响应：地址叫 download_url、
// 顶层 width / height 是字符串、video_model 是字符串套 JSON、cover 没有 url。
// 旧结构体对这份夹具不会报错，只会解析出全空字段。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:24:00；改动原因：夹具改为真实响应形状，断言 download_url 与清晰度挑选。
func TestParseCreationBlockVideo(t *testing.T) {
	block := json.RawMessage(`{"creation_block":{"creations":[{"type":2,"id":"v-1","video":{"vid":"v0269","status":3,"width":"384","height":"216","duration":10.08,"video_type":"mp4","download_url":"https://example.com/v.mp4","download_filehash":"abc123","cover":{"key":"tos-cn-p-bucket/cover","image_thumb":{"url":"https://example.com/cover-thumb.png","width":384,"height":216},"image_preview":{"url":"https://example.com/cover-preview.png","width":1280,"height":720}}}}]}}`)
	assets := parseCreationBlock(block)
	if len(assets) != 1 {
		t.Fatalf("应解析出 1 个视频，实际 %d", len(assets))
	}
	if !assets[0].isVideo() {
		t.Error("应判定为视频")
	}
	if assets[0].URL != "https://example.com/v.mp4" {
		t.Errorf("视频地址应取 download_url，实际 %q", assets[0].URL)
	}
	// 顶层尺寸是字符串，必须仍然解析成数字供 media 扩展使用。
	if assets[0].Width != 384 || assets[0].Height != 216 {
		t.Errorf("字符串尺寸应解析为 384×216，实际 %d×%d", assets[0].Width, assets[0].Height)
	}
	// 时长是 float 秒；调用方要靠它知道拿到的片子有多长。
	if assets[0].Duration != 10.08 {
		t.Errorf("应读出产物真实时长 10.08，实际 %v", assets[0].Duration)
	}
}

// TestParseCreationBlockVideoFallsBackToVideoModel 校验 download_url 缺失时退到清晰度清单。
//
// video_model 里的 main_url 是 base64 编码的地址，且键名（video_1 / video_2）
// 不表达清晰度，必须按像素面积挑最清晰的那条。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:24:00；改动原因：新增，覆盖 video_model 解包与 base64 直链。
func TestParseCreationBlockVideoFallsBackToVideoModel(t *testing.T) {
	lowURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/low.mp4"))
	highURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/high.mp4"))
	model := `{\"status\":10,\"message\":\"success\",\"video_duration\":10.08,\"media_type\":\"video\",\"video_list\":{\"video_1\":{\"definition\":\"360p\",\"vwidth\":640,\"vheight\":360,\"main_url\":\"` + lowURL + `\"},\"video_2\":{\"definition\":\"1080p\",\"vwidth\":1920,\"vheight\":1080,\"main_url\":\"` + highURL + `\"}}}`
	block := json.RawMessage(`{"creation_block":{"creations":[{"type":2,"id":"v-2","video":{"status":3,"width":"","height":"","video_model":"` + model + `","cover":{"image_preview":{"url":"https://example.com/cover.png","width":1280,"height":720}}}}]}}`)
	assets := parseCreationBlock(block)
	if len(assets) != 1 {
		t.Fatalf("应解析出 1 个视频，实际 %d", len(assets))
	}
	if assets[0].URL != "https://example.com/high.mp4" {
		t.Errorf("应挑像素面积最大的直链，实际 %q", assets[0].URL)
	}
	if assets[0].Width != 1920 || assets[0].Height != 1080 {
		t.Errorf("尺寸应跟随所选清晰度 1920×1080，实际 %d×%d", assets[0].Width, assets[0].Height)
	}
}

// TestParseCreationBlockVideoIgnoresCover 校验封面不会被当成成品地址。
//
// 封面是一张 JPEG：把它写进 URL，下游 media 里就会出现 {"type":"video","url":"...png"}
// 这种自相矛盾的条目。占位阶段就让 URL 为空，由回灌层继续等。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：封面不再兜底地址，改为断言不外泄。
func TestParseCreationBlockVideoIgnoresCover(t *testing.T) {
	block := json.RawMessage(`{"creation_block":{"creations":[{"type":2,"id":"v-3","video":{"status":1,"cover":{"image_thumb":{"url":"https://example.com/t.png","width":384,"height":216},"image_preview":{"url":"https://example.com/p.png","width":1280,"height":720}}}}]}}`)
	if assets := parseCreationBlock(block); len(assets) != 0 {
		t.Fatalf("只有封面、没有成品直链时不应产出资产，实际 %+v", assets)
	}
}

// TestParseCreationBlockVideoSkipsEmpty 校验没有任何地址与 key 的视频条目被丢弃。
//
// 首帧可能只有 status，交出去就是一条空链接，比不交更糟。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:24:00；改动原因：新增，占位帧不外泄。
func TestParseCreationBlockVideoSkipsEmpty(t *testing.T) {
	block := json.RawMessage(`{"creation_block":{"creations":[{"type":2,"id":"v-4","video":{"status":1}}]}}`)
	if assets := parseCreationBlock(block); len(assets) != 0 {
		t.Fatalf("无地址无 key 的视频条目应被丢弃，实际 %+v", assets)
	}
}

// TestMergeAssetsFillsFromLaterFrames 校验同资源多次上报的合并。
//
// 视频是异步产物：首帧只有状态，末帧才带直链与时长。只取首帧会拿到一条空壳。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:20:00；改动原因：图片能力删除，夹具改用视频首末帧。
func TestMergeAssetsFillsFromLaterFrames(t *testing.T) {
	if got := parseCreationBlock(extractCreationBlock(t, creationVideoFirstFrame)); len(got) != 0 {
		t.Fatalf("首帧不应产出可用结果，实际 %+v", got)
	}

	// 首帧只有状态，占位信息由合并逻辑从后续帧补齐。
	placeholder := creationAsset{ID: "v-1", Type: 2, Status: 1}
	merged := mergeAssets([]creationAsset{placeholder}, parseCreationBlock(extractCreationBlock(t, creationVideoFinalFrame)))
	if len(merged) != 1 {
		t.Fatalf("同一 id 应合并为 1 条，实际 %d", len(merged))
	}
	if merged[0].URL != "https://example.com/v.mp4" {
		t.Errorf("末帧应补上直链，实际 %q", merged[0].URL)
	}
	if merged[0].Duration != 10.08 {
		t.Errorf("末帧应补上时长，实际 %v", merged[0].Duration)
	}
}

// TestMergeAssetsKeepsDistinctIDs 校验不同资源不被误合并。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:20:00；改动原因：图片能力删除，夹具改用视频。
func TestMergeAssetsKeepsDistinctIDs(t *testing.T) {
	a := []creationAsset{{ID: "a", URL: "https://example.com/a.mp4", Type: 2}}
	b := []creationAsset{{ID: "b", URL: "https://example.com/b.mp4", Type: 2}}
	if got := mergeAssets(a, b); len(got) != 2 {
		t.Fatalf("不同 id 应各自保留，实际 %d 条", len(got))
	}
}

// TestAttachAssetsVideoUsesTextPart 校验视频以文本形式给出链接。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，OpenAI 无视频内容类型。
func TestAttachAssetsVideoUsesTextPart(t *testing.T) {
	resp := buildChatCompletion("doubao/video", "", "", "conv-1", "生成视频")
	attachAssets(resp, []creationAsset{{ID: "v", Type: 2, Status: 3, URL: "https://example.com/v.mp4"}})

	message := resp["choices"].([]map[string]any)[0]["message"].(map[string]any)
	parts := message["content"].([]map[string]any)
	if len(parts) != 1 || parts[0]["type"] != "text" {
		t.Fatalf("视频应作为文本段返回，实际 %+v", parts)
	}
	if !strings.Contains(parts[0]["text"].(string), "https://example.com/v.mp4") {
		t.Errorf("文本段应包含视频地址，实际 %q", parts[0]["text"])
	}
	media := resp["media"].([]map[string]any)
	if media[0]["type"] != "video" {
		t.Errorf("media 中应标记为 video，实际 %+v", media[0])
	}
}

// TestAttachAssetsNoopForTextReply 校验纯文本回复保持字符串 content。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:20:00；改动原因：图片能力删除，夹具模型名改用 doubao/video。
func TestAttachAssetsNoopForTextReply(t *testing.T) {
	resp := buildChatCompletion("doubao/video", "答案是 2", "", "", "1+1=?")
	attachAssets(resp, nil)

	message := resp["choices"].([]map[string]any)[0]["message"].(map[string]any)
	if _, isString := message["content"].(string); !isString {
		t.Errorf("无媒体时 content 应保持字符串，实际 %T", message["content"])
	}
	if _, exists := resp["media"]; exists {
		t.Error("无媒体时不应出现 media 字段")
	}
}

// TestCollectCompletionExtractsAssets 校验 SSE 流中的生成结果被收集并合并。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:20:00；改动原因：图片能力删除，流夹具改用视频首末帧。
func TestCollectCompletionExtractsAssets(t *testing.T) {
	stream := strings.Join([]string{
		"event: STREAM_CHUNK",
		"data: " + creationVideoFirstFrame,
		"",
		"event: STREAM_CHUNK",
		"data: " + creationVideoFinalFrame,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")

	srv, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	res, err := collectCompletion(t.Context(), sa, "生成一段海浪短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, srv.URL, true, nil)
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if len(res.Assets) != 1 {
		t.Fatalf("应收集到 1 个生成结果，实际 %d", len(res.Assets))
	}
	if res.Assets[0].URL != "https://example.com/v.mp4" {
		t.Errorf("生成结果应有可用直链，实际 %q", res.Assets[0].URL)
	}
}

// TestMediaExtensionCarriesVideoDuration 校验 media[] 回填视频真实时长。
//
// 上游可能按可用档位调整时长，所以「请求 5 秒」不等于「拿到 5 秒」；不把这个
// 实测值交出去，调用方只能猜。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:20:00；改动原因：图片能力删除，夹具只剩视频条目。
func TestMediaExtensionCarriesVideoDuration(t *testing.T) {
	media := mediaExtension([]creationAsset{
		{ID: "v", Type: 2, Status: 3, URL: "https://example.com/v.mp4", Duration: 5.04},
	})
	if len(media) != 1 {
		t.Fatalf("应输出 1 条媒体，实际 %d", len(media))
	}
	if got, ok := media[0]["duration"]; !ok || got != 5.04 {
		t.Errorf("视频条目应带 duration=5.04，实际 %v（存在=%v）", got, ok)
	}
	if media[0]["type"] != "video" {
		t.Errorf("媒体条目应标记为 video，实际 %v", media[0]["type"])
	}
}

// TestMediaExtensionOmitsUnknownDuration 校验上游没给时长时省略该键。
//
// 写 0 会被读成「拿到了 0 秒的片子」，比缺字段更误导。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:16:00；改动原因：新增，缺值不得写成 0。
func TestMediaExtensionOmitsUnknownDuration(t *testing.T) {
	media := mediaExtension([]creationAsset{{ID: "v", Type: 2, Status: 3, URL: "https://example.com/v.mp4"}})
	if _, ok := media[0]["duration"]; ok {
		t.Errorf("时长未知时不应出现 duration，实际 %v", media[0]["duration"])
	}
}

// TestMergeAssetsKeepsDuration 校验时长在多次上报合并时不被清空。
//
// 真实的视频是异步产物：首帧只有状态，末帧才带时长与直链。合并逻辑若只覆盖
// URL 而漏掉时长，最终结果就会缺这一项。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:16:00；改动原因：新增，补时长合并分支。
func TestMergeAssetsKeepsDuration(t *testing.T) {
	merged := mergeAssets(
		[]creationAsset{{ID: "v", Type: 2, Status: 1}},
		[]creationAsset{{ID: "v", Type: 2, Status: 3, URL: "https://example.com/v.mp4", Duration: 10.08}},
	)
	if len(merged) != 1 {
		t.Fatalf("同一 id 应合并为 1 条，实际 %d", len(merged))
	}
	if merged[0].Duration != 10.08 {
		t.Errorf("末帧的时长应被合并进来，实际 %v", merged[0].Duration)
	}
}

// TestDescribeAssets 校验媒体摘要文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：摘要只统计有直链的成品，与 media 对齐。
func TestDescribeAssets(t *testing.T) {
	got := describeAssets([]creationAsset{{Type: 2, Status: 3, URL: "https://example.com/v.mp4", Model: "Seedance", Width: 384, Height: 216}})
	if !strings.Contains(got, "已生成视频") || !strings.Contains(got, "Seedance") || !strings.Contains(got, "384×216") {
		t.Errorf("摘要应包含类型、生成器与尺寸，实际 %q", got)
	}
	if got := describeAssets(nil); got != "" {
		t.Errorf("无资源时应返回空串，实际 %q", got)
	}
	// 占位块没有直链：说「已生成」会与空的 media 矛盾。
	if got := describeAssets([]creationAsset{{Type: 2, Model: "Seedance", Width: 384, Height: 216}}); got != "" {
		t.Errorf("只有占位块时应返回空串，实际 %q", got)
	}
}

// extractCreationBlock 从一帧 patch_op 中取出 CREATION 块的 content。
// [参数] t: 测试上下文；frame: 事件 data 原文。
// [返回] 块的 content 原文。
// 最近修改时间：2026-10-17；改动原因：新增，测试辅助。
func extractCreationBlock(t *testing.T, frame string) json.RawMessage {
	t.Helper()
	var payload struct {
		PatchOp []patchOp `json:"patch_op"`
	}
	if err := json.Unmarshal([]byte(frame), &payload); err != nil {
		t.Fatalf("解析测试帧失败：%v", err)
	}
	for _, op := range payload.PatchOp {
		for _, block := range op.PatchValue.blocks() {
			if block.BlockType != blockTypeCreation {
				continue
			}
			var content blockContent
			if err := json.Unmarshal(block.Content, &content); err != nil {
				t.Fatalf("解析块内容失败：%v", err)
			}
			return content.CreationBlock
		}
	}
	t.Fatal("测试帧中未找到 CREATION 块")
	return nil
}

// TestCreationAssetPlayable 校验可播放判据只看是否有成品直链。
//
// 这条判据是回灌与渲染共用的分界线：占位块有 key、有状态，但没有 URL，
// 一旦被判成「已出片」，客户端会拿到一条打不开的链接，正文却写着「已生成」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-25 00:29:00；改动原因：补 status==3 双判据用例。
func TestCreationAssetPlayable(t *testing.T) {
	cases := []struct {
		name  string
		asset creationAsset
		want  bool
	}{
		{"成品", creationAsset{Type: 2, Status: 3, URL: "https://example.com/v.mp4"}, true},
		{"状态未定稿", creationAsset{Type: 2, Status: 0, URL: "https://example.com/v.mp4"}, false},
		{"只有空白直链", creationAsset{Type: 2, Status: 3, URL: "   "}, false},
		{"占位块只有 key", creationAsset{Type: 2, Status: 3, Key: "tos-cn-p-bucket/v"}, false},
		{"非视频类型", creationAsset{Type: 1, Status: 3, URL: "https://example.com/v.mp4"}, false},
	}
	for _, c := range cases {
		if got := c.asset.playable(); got != c.want {
			t.Errorf("%s：playable 应为 %v，实际 %v", c.name, c.want, got)
		}
	}

	if !hasPlayableAsset([]creationAsset{{Type: 2, Status: 3, Key: "k"}, {Type: 2, Status: 3, URL: "https://example.com/v.mp4"}}) {
		t.Error("混合列表里只要有一条可播放就应判 true")
	}
	if hasPlayableAsset([]creationAsset{{Type: 2, Status: 3, Key: "k"}, {Type: 2, Status: 3}}) {
		t.Error("全是没有直链的占位块时应判 false")
	}
}

// TestAttachAssetsSkipsPlaceholder 校验占位块不会写进 content 与 media。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：新增，占位块不得冒充成品。
func TestAttachAssetsSkipsPlaceholder(t *testing.T) {
	resp := buildChatCompletion("doubao/video", "正在生成", "", "conv-1", "生成视频")
	attachAssets(resp, []creationAsset{{ID: "v", Type: 2, Key: "tos-cn-p-bucket/v"}})

	message := resp["choices"].([]map[string]any)[0]["message"].(map[string]any)
	parts, ok := message["content"].([]map[string]any)
	if !ok || len(parts) != 1 || parts[0]["text"] != "正在生成" {
		t.Fatalf("没有成品直链时应只剩原文一段，实际 %+v", message["content"])
	}
	if media, _ := resp["media"].([]map[string]any); len(media) != 0 {
		t.Errorf("没有成品直链时 media 应为空，实际 %+v", media)
	}
}

// TestAttachAssetsAcceptsGenericChoices 校验 choices 经过 JSON 往返后仍能挂上附件。
//
// 直接断言 []map[string]any 只在「同进程刚构造完」时成立；中间过一次序列化，
// choices 会退化成 []any，旧实现会静默 return，附件全丢且不报错。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：新增，覆盖 firstChoiceMessage 的泛型分支。
func TestAttachAssetsAcceptsGenericChoices(t *testing.T) {
	resp := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "生成好了"}}}}
	attachAssets(resp, []creationAsset{{ID: "v", Type: 2, Status: 3, URL: "https://example.com/v.mp4"}})

	message := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	parts, ok := message["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("JSON 往返后应挂上 [原文 + 链接] 两段，实际 %+v", message["content"])
	}
	if _, has := resp["media"]; !has {
		t.Error("JSON 往返后仍应写入 media")
	}
}

// TestBestStreamStableOnTie 校验同分辨率多码率时取到的是固定那条。
//
// video_list 是 map，遍历顺序随机；只按像素面积比较，同分时会随机挑一条直链，
// 排障时同一份响应换个进程就复现不了。定序后必须稳定取 key 最小的那条。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 13:32:00；改动原因：新增，锁定同分时的确定性。
func TestBestStreamStableOnTie(t *testing.T) {
	aURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/a.mp4"))
	bURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/b.mp4"))
	model := fmt.Sprintf(`{"status":10,"video_list":{"video_b":{"vwidth":1280,"vheight":720,"main_url":"%s"},"video_a":{"vwidth":1280,"vheight":720,"main_url":"%s"}}}`,
		bURL, aURL)
	video := &creationVideo{VideoModel: model}

	first, width, height := video.bestStream()
	if first == "" {
		t.Fatal("同分时应至少挑出一条直链")
	}
	for i := 0; i < 20; i++ {
		got, _, _ := video.bestStream()
		if got != first {
			t.Fatalf("同分辨率多码率应稳定取同一条，第 %d 次得到 %q，首次是 %q", i, got, first)
		}
	}
	if first != "https://example.com/a.mp4" {
		t.Errorf("同分时应按 key 定序取 video_a，实际 %q", first)
	}
	if width != 1280 || height != 720 {
		t.Errorf("尺寸应跟随所选清晰度 1280×720，实际 %d×%d", width, height)
	}
}

// TestVideoStreamRank 校验流地址水印等级判据。
//
// 素材地址用 query 参数 lr 携带水印标识；判据来自 ChinaCPA pickCleanestVideoUrl，
// 后续 bestStream 依此排序，判错会把水印片当原片返回。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-24 23:56:00；改动原因：新增，锁定水印等级判据。
func TestVideoStreamRank(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"无 query", "https://example.com/v.mp4", streamRankPlain},
		{"无水印", "https://example.com/v.mp4?lr=unwatermark_1080p", streamRankClean},
		{"带水印", "https://example.com/v.mp4?lr=watermark", streamRankWatermarked},
		{"其他 query", "https://example.com/v.mp4?foo=bar", streamRankPlain},
		{"解析失败", "://bad url", streamRankPlain},
	}
	for _, c := range cases {
		if got := videoStreamRank(c.raw); got != c.want {
			t.Errorf("%s：水印等级应为 %d，实际 %d", c.name, c.want, got)
		}
	}
}

// TestBestStreamPrefersUnwatermark 校验无水印优先于清晰度。
//
// 水印流是 1080p、无水印流是 720p；按新判据先比水印等级再比清晰度，
// 应取无水印的 720p，否则用户拿到的是带平台水印的片子。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-24 23:56:00；改动原因：新增，锁定无水印选优。
func TestBestStreamPrefersUnwatermark(t *testing.T) {
	wmURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/wm.mp4?lr=watermark_hd"))
	cleanURL := base64.StdEncoding.EncodeToString([]byte("https://example.com/clean.mp4?lr=unwatermark_sd"))
	model := fmt.Sprintf(`{"status":10,"video_list":{"video_hd":{"vwidth":1920,"vheight":1080,"main_url":"%s"},"video_sd":{"vwidth":1280,"vheight":720,"main_url":"%s"}}}`,
		wmURL, cleanURL)
	video := &creationVideo{VideoModel: model}

	got, width, height := video.bestStream()
	if got != "https://example.com/clean.mp4?lr=unwatermark_sd" {
		t.Errorf("无水印优先：应取无水印流，实际 %q", got)
	}
	if width != 1280 || height != 720 {
		t.Errorf("尺寸应跟随无水印流 1280×720，实际 %d×%d", width, height)
	}
}

// TestBestStreamSkipsExpiredURL 校验整组地址过期时不返回任何直链。
//
// url_expire 是秒级时间戳，过期后 video_model 内所有清晰度地址都失效；
// 仍返回旧地址会让客户端拿到 403。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-24 23:56:00；改动原因：新增，锁定过期排除。
func TestBestStreamSkipsExpiredURL(t *testing.T) {
	addr := base64.StdEncoding.EncodeToString([]byte("https://example.com/v.mp4"))
	model := fmt.Sprintf(`{"status":10,"url_expire":1,"video_list":{"video_a":{"vwidth":1280,"vheight":720,"main_url":"%s"}}}`,
		addr)
	video := &creationVideo{VideoModel: model}
	if got, _, _ := video.bestStream(); got != "" {
		t.Errorf("url_expire 已过期时应返回空地址，实际 %q", got)
	}
}
