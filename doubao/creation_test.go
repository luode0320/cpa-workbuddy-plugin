package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// creationPlaceholderFrame 是一张图的首帧：只有占位尺寸，没有 key 与 URL。
const creationPlaceholderFrame = `{"patch_op":[{"patch_object":1,"patch_type":2,"patch_value":{"content_block":[{"block_type":2074,"block_id":"blk-1","content":{"creation_block":{"creations":[{"type":1,"id":"asset-1","image":{"status":1,"placeholder":{"width":2048,"height":2048,"description":"Seedream 5.0 Flash"}}}]}},"is_finish":false,"patch_type":2}]}}]}`

// creationFinalFrame 是同一张图的末帧：补齐 key 与可访问 URL。
const creationFinalFrame = `{"patch_op":[{"patch_object":1,"patch_type":2,"patch_value":{"content_block":[{"block_type":2074,"block_id":"blk-1","content":{"creation_block":{"creations":[{"type":1,"id":"asset-1","can_publish":true,"image":{"status":1,"key":"tos-cn-i-bucket/rc_gen_image/abc.jpeg","image_thumb":{"url":"https://example.com/thumb.png","width":2048,"height":2048},"image_ori":{"url":"https://example.com/ori.png","width":2048,"height":2048},"placeholder":{"width":2048,"height":2048,"description":"Seedream 5.0 Flash"}}}]}},"is_finish":false,"patch_type":2}]}}]}`

// TestParseCreationBlockPlaceholder 校验首帧（只有占位）不产出可用结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，占位帧不得交出空链接。
func TestParseCreationBlockPlaceholder(t *testing.T) {
	block := extractCreationBlock(t, creationPlaceholderFrame)
	assets := parseCreationBlock(block)
	if len(assets) != 0 {
		t.Fatalf("占位帧没有 key 与 URL，不应产出可用结果，实际 %+v", assets)
	}
}

// TestParseCreationBlockFinal 校验末帧解析出完整资源。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，真实结构解析锁定。
func TestParseCreationBlockFinal(t *testing.T) {
	block := extractCreationBlock(t, creationFinalFrame)
	assets := parseCreationBlock(block)
	if len(assets) != 1 {
		t.Fatalf("应解析出 1 个资源，实际 %d", len(assets))
	}
	asset := assets[0]
	if asset.Key != "tos-cn-i-bucket/rc_gen_image/abc.jpeg" {
		t.Errorf("key 解析错误：%q", asset.Key)
	}
	// 原图优于缩略图：分辨率与画质都更高。
	if asset.URL != "https://example.com/ori.png" {
		t.Errorf("应优先使用原图 URL，实际 %q", asset.URL)
	}
	if asset.Width != 2048 || asset.Height != 2048 {
		t.Errorf("尺寸解析错误：%dx%d", asset.Width, asset.Height)
	}
	if asset.Model != "Seedream 5.0 Flash" {
		t.Errorf("生成器名解析错误：%q", asset.Model)
	}
	if asset.Type != 1 {
		t.Errorf("图片类型应为 1，实际 %d", asset.Type)
	}
}

// TestMergeAssetsFillsFromLaterFrames 校验同资源多次上报的合并。
//
// 这是本轮最关键的行为：只取首帧会拿到没有 URL 的空壳。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，合并语义锁定。
func TestMergeAssetsFillsFromLaterFrames(t *testing.T) {
	merged := mergeAssets(nil, parseCreationBlock(extractCreationBlock(t, creationPlaceholderFrame)))
	if len(merged) != 0 {
		t.Fatalf("首帧不应产出结果，实际 %+v", merged)
	}

	// 首帧的占位信息也应被记住：用 parseCreationBlock 直接解析时它被过滤掉了，
	// 这里模拟“先记下占位、后补全”的真实顺序。
	placeholder := creationAsset{ID: "asset-1", Type: 1, Width: 2048, Height: 2048, Model: "Seedream 5.0 Flash"}
	merged = mergeAssets([]creationAsset{placeholder}, parseCreationBlock(extractCreationBlock(t, creationFinalFrame)))
	if len(merged) != 1 {
		t.Fatalf("同一 id 应合并为 1 条，实际 %d", len(merged))
	}
	if merged[0].URL != "https://example.com/ori.png" {
		t.Errorf("末帧应补上 URL，实际 %q", merged[0].URL)
	}
	if merged[0].Model != "Seedream 5.0 Flash" {
		t.Errorf("生成器名不应被清空，实际 %q", merged[0].Model)
	}
}

// TestMergeAssetsKeepsDistinctIDs 校验不同资源不被误合并。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，多图场景。
func TestMergeAssetsKeepsDistinctIDs(t *testing.T) {
	a := []creationAsset{{ID: "a", URL: "https://example.com/a.png", Type: 1}}
	b := []creationAsset{{ID: "b", URL: "https://example.com/b.png", Type: 1}}
	if got := mergeAssets(a, b); len(got) != 2 {
		t.Fatalf("不同 id 应各自保留，实际 %d 条", len(got))
	}
}

// TestParseCreationBlockVideo 校验视频资源解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，视频分支。
func TestParseCreationBlockVideo(t *testing.T) {
	block := json.RawMessage(`{"creation_block":{"creations":[{"type":2,"id":"v-1","video":{"status":1,"key":"tos-cn-i-bucket/rc_gen_video/x.mp4","url":"https://example.com/v.mp4","cover":{"url":"https://example.com/cover.png","width":1280,"height":720}}}]}}`)
	assets := parseCreationBlock(block)
	if len(assets) != 1 {
		t.Fatalf("应解析出 1 个视频，实际 %d", len(assets))
	}
	if !assets[0].isVideo() {
		t.Error("应判定为视频")
	}
	if assets[0].URL != "https://example.com/v.mp4" {
		t.Errorf("视频地址解析错误：%q", assets[0].URL)
	}
}

// TestAttachAssetsBuildsMultimodalContent 校验图片转成 OpenAI 多模态格式。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，AC-005 返回格式锁定。
func TestAttachAssetsBuildsMultimodalContent(t *testing.T) {
	resp := buildChatCompletion("doubao/image", "已生成图片", "", "conv-1", "画一只猫")
	attachAssets(resp, []creationAsset{
		{ID: "a", Type: 1, URL: "https://example.com/a.png", Width: 1024, Height: 1024, Model: "Seedream", Key: "k1"},
	})

	choices := resp["choices"].([]map[string]any)
	message := choices[0]["message"].(map[string]any)
	parts, ok := message["content"].([]map[string]any)
	if !ok {
		t.Fatalf("有媒体时 content 应为数组，实际 %T", message["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("应含 1 段文本 + 1 个图片，实际 %d", len(parts))
	}
	if parts[0]["type"] != "text" {
		t.Errorf("首段应为文本，实际 %+v", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("第二段应为 image_url，实际 %+v", parts[1])
	}
	imageURL := parts[1]["image_url"].(map[string]any)
	if imageURL["url"] != "https://example.com/a.png" {
		t.Errorf("图片地址错误：%+v", imageURL)
	}

	media, ok := resp["media"].([]map[string]any)
	if !ok || len(media) != 1 {
		t.Fatalf("应带 media 扩展，实际 %+v", resp["media"])
	}
	if media[0]["width"] != 1024 || media[0]["key"] != "k1" || media[0]["model"] != "Seedream" {
		t.Errorf("media 结构化信息不完整：%+v", media[0])
	}
}

// TestAttachAssetsVideoUsesTextPart 校验视频以文本形式给出链接。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，OpenAI 无视频内容类型。
func TestAttachAssetsVideoUsesTextPart(t *testing.T) {
	resp := buildChatCompletion("doubao/video", "", "", "conv-1", "生成视频")
	attachAssets(resp, []creationAsset{{ID: "v", Type: 2, URL: "https://example.com/v.mp4"}})

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
// 最近修改时间：2026-10-17；改动原因：新增，不改变普通回复形状。
func TestAttachAssetsNoopForTextReply(t *testing.T) {
	resp := buildChatCompletion("doubao/default", "答案是 2", "", "", "1+1=?")
	attachAssets(resp, nil)

	message := resp["choices"].([]map[string]any)[0]["message"].(map[string]any)
	if _, isString := message["content"].(string); !isString {
		t.Errorf("无媒体时 content 应保持字符串，实际 %T", message["content"])
	}
	if _, exists := resp["media"]; exists {
		t.Error("无媒体时不应出现 media 字段")
	}
}

// TestResolveNoWatermarkRequiresFullKey 校验无水印解析必须传完整 key。
//
// 实测：传去掉 bucket 前缀的裸 key 会返回 without_watermark:false。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，key 形态契约。
func TestResolveNoWatermarkRequiresFullKey(t *testing.T) {
	var gotBody watermarkRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"code":0,"data":{"without_watermark":true,"download_image":{"tos-cn-i-bucket/rc_gen_image/abc.jpeg":{"url":"https://example.com/clean.png","width":2048,"height":2048}}}}`))
	}))
	defer srv.Close()
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""
	resolved, err := resolveNoWatermark(t.Context(), sa, []string{"tos-cn-i-bucket/rc_gen_image/abc.jpeg"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(gotBody.URI) != 1 || !strings.HasPrefix(gotBody.URI[0], "tos-cn-i-") {
		t.Errorf("必须传完整 key（含 bucket 前缀），实际 %+v", gotBody.URI)
	}
	variant, ok := resolved["tos-cn-i-bucket/rc_gen_image/abc.jpeg"]
	if !ok || variant.URL != "https://example.com/clean.png" {
		t.Errorf("应返回无水印地址，实际 %+v", resolved)
	}
}

// TestHydrateAssetsDegradesOnFailure 校验无水印失败时保留原 URL 而非整体失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，降级而非失败。
func TestHydrateAssetsDegradesOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""
	assets := []creationAsset{{ID: "a", Type: 1, URL: "https://example.com/thumb.png", Key: "tos-cn-i-b/rc_gen_image/a.jpeg"}}
	got := hydrateAssets(t.Context(), sa, assets)
	if got[0].URL != "https://example.com/thumb.png" {
		t.Errorf("解析失败时应保留流内 URL，实际 %q", got[0].URL)
	}
}

// TestHydrateAssetsSkipsVideo 校验视频不参与无水印解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，视频地址本身就是直链。
func TestHydrateAssetsSkipsVideo(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	sa := completionTestAccount()
	sa.PhysicalPath = ""
	assets := []creationAsset{{ID: "v", Type: 2, URL: "https://example.com/v.mp4", Key: "tos-cn-i-b/rc_gen_video/v.mp4"}}
	got := hydrateAssets(t.Context(), sa, assets)
	if called {
		t.Error("视频不应触发无水印解析调用")
	}
	if got[0].URL != "https://example.com/v.mp4" {
		t.Errorf("视频地址不应被改动，实际 %q", got[0].URL)
	}
}

// TestCollectCompletionExtractsAssets 校验 SSE 流中的生成结果被收集并合并。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，流内生成结果收集。
func TestCollectCompletionExtractsAssets(t *testing.T) {
	stream := strings.Join([]string{
		"event: STREAM_CHUNK",
		"data: " + creationPlaceholderFrame,
		"",
		"event: STREAM_CHUNK",
		"data: " + creationFinalFrame,
		"",
		"event: SSE_REPLY_END",
		"data: {}",
		"",
	}, "\n")

	srv, sa, cleanup := newCompletionTestServer(t, stream)
	defer cleanup()
	sa.PhysicalPath = ""

	// 无水印端点不可达（真实域名），走降级路径，保留流内 URL。
	res, err := collectCompletion(t.Context(), sa, "画一只猫", generationMode{Skill: skillImageGen, Kind: "image"}, srv.URL, true, nil)
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if len(res.Assets) != 1 {
		t.Fatalf("应收集到 1 个生成结果，实际 %d", len(res.Assets))
	}
	if res.Assets[0].URL == "" {
		t.Error("生成结果应有可用 URL")
	}
}

// TestDescribeAssets 校验媒体摘要文案。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，空正文兜底。
func TestDescribeAssets(t *testing.T) {
	got := describeAssets([]creationAsset{{Type: 1, Model: "Seedream", Width: 1024, Height: 1024}})
	if !strings.Contains(got, "已生成图片") || !strings.Contains(got, "Seedream") || !strings.Contains(got, "1024×1024") {
		t.Errorf("摘要应包含类型、生成器与尺寸，实际 %q", got)
	}
	if got := describeAssets([]creationAsset{{Type: 2}}); !strings.Contains(got, "已生成视频") {
		t.Errorf("视频摘要错误：%q", got)
	}
	if got := describeAssets(nil); got != "" {
		t.Errorf("无资源时应返回空串，实际 %q", got)
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
