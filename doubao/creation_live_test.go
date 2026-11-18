package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCreationLiveControlWithoutSkill 是不带 skill 的对照组，用于记录 skill 参数的真实作用。
//
// 实测结论（2026-10-17，推翻既有假设）：上游按**提示词意图**决定是否生成，不带 skill
// 的创作类提示词同样可能产出 CREATION 块；反过来带 skill 的闲聊提示词仍只回文本，且
// 上游回显 input_skill.skill_id="0"（未采纳该参数）。
//
// 因此本用例不断言「无 skill 必无视频」，而是断言解析层在两种形态下都正确：有 CREATION
// 块就解析出可用资源，没有就得到纯文本，不出现空壳资源。skill 注入仍然保留——它语义
// 正确、无副作用，且上游策略可能随时收紧。
// 默认跳过，设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:15:00；改动原因：图片能力删除，对照组改用生视频提示词。
func TestCreationLiveControlWithoutSkill(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过对照组")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, errRun := collectCompletion(ctx, sa, "生成一段海浪拍打礁石的短视频", textMode(), "", true, nil)
	if errRun != nil {
		t.Fatalf("对照组请求失败：%v", errRun)
	}

	// 关键断言：无论上游是否生成，解析结果都必须自洽——
	// 每个资源都要有可访问 URL，不允许出现「有条目没链接」的空壳。
	for _, asset := range res.Assets {
		if asset.URL == "" {
			t.Fatalf("解析出了没有 URL 的空壳资源：%+v", asset)
		}
	}
	if len(res.Assets) == 0 && strings.TrimSpace(res.Text) == "" {
		t.Fatal("既无媒体也无文本，说明解析链路有问题")
	}

	if len(res.Assets) > 0 {
		t.Logf("对照组记录：不带 skill 时上游仍按提示词意图生成，资源 %d 个（说明 skill 非必要条件）", len(res.Assets))
	} else {
		t.Logf("对照组记录：不带 skill 时上游只返回文本（长度=%d）", len(res.Text))
	}
}

// TestCreationLiveVideo 用真实账号验证生视频链路。
//
// 视频地址本身就是直链，不需要走无水印解析；结果解析成 `type=2` 的资源，OpenAI 侧以
// 文本段承载链接 + `media` 扩展保留结构化信息。
// 默认跳过，设 DOUBAO_LIVE_TEST=1 才执行。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，补齐 AC-005 视频真实证据。
func TestCreationLiveVideo(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE_TEST") != "1" {
		t.Skip("设 DOUBAO_LIVE_TEST=1 才执行真实上游探测")
	}
	sa := liveAccountForTest(t)
	if sa == nil {
		t.Skip("未提供 .local_cookie.txt（真实 Cookie），跳过生视频验证")
	}

	// 视频生成耗时远高于文本：给足超时，避免把慢生成误判为失败。
	ctx, cancel := context.WithTimeout(context.Background(), 540*time.Second)
	defer cancel()

	res, errRun := collectCompletion(ctx, sa, "生成一段海浪拍打礁石的短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", true, nil)
	if errRun != nil {
		t.Fatalf("真实生视频失败：%v", errRun)
	}
	if len(res.Assets) == 0 {
		t.Fatalf("应返回至少一个视频资源，实际 0 个（正文长度=%d）", len(res.Text))
	}

	asset := res.Assets[0]
	if asset.URL == "" {
		t.Fatal("视频结果应有可访问 URL")
	}
	if !asset.isVideo() {
		t.Errorf("skill=5 应产出视频，实际 type=%d", asset.Type)
	}

	// 视频在 OpenAI 侧以文本段承载链接，media 扩展保留 type=video。
	resp := buildChatCompletion("doubao/video", "", "", res.ConversationID, "生成一段海浪拍打礁石的短视频")
	attachAssets(resp, res.Assets)
	media, ok := resp["media"].([]map[string]any)
	if !ok || len(media) == 0 {
		t.Fatal("应带 media 扩展")
	}
	if media[0]["type"] != "video" {
		t.Errorf("media 扩展应标记为 video，实际 %v", media[0]["type"])
	}

	t.Logf("真实生视频成功：资源 %d 个，尺寸 %dx%d，URL 长度=%d",
		len(res.Assets), asset.Width, asset.Height, len(asset.URL))
}
