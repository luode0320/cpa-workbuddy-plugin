package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件把生成结果转成 OpenAI 响应形态（多模态 content + media 扩展 + 流式帧）。

// attachAssets 把生成结果挂到 OpenAI 响应对象上。
//
// 纯文本回复保持字符串 content（所有客户端都能处理）；只有存在媒体时才切成
// 数组形态，否则会改变普通回复的形状。视频没有对应的 OpenAI 内容类型，因此
// 以文本形式给出链接，同时在 media 扩展里保留结构化信息。
// [参数] resp: OpenAI 响应对象；assets: 生成结果。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，多模态响应转换。
func attachAssets(resp map[string]any, assets []creationAsset) {
	if len(assets) == 0 {
		return
	}
	choices, ok := resp["choices"].([]map[string]any)
	if !ok || len(choices) == 0 {
		return
	}
	message, ok := choices[0]["message"].(map[string]any)
	if !ok {
		return
	}

	parts := make([]map[string]any, 0, len(assets)+1)
	if text, isString := message["content"].(string); isString && text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, asset := range assets {
		if asset.URL == "" {
			continue
		}
		if asset.isVideo() {
			parts = append(parts, map[string]any{"type": "text", "text": "[视频] " + asset.URL})
			continue
		}
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": asset.URL},
		})
	}
	if len(parts) == 0 {
		return
	}
	message["content"] = parts

	// 结构化信息放扩展字段：OpenAI 内容数组装不下尺寸、生成器名与资源 key。
	resp["media"] = mediaExtension(assets)
}

// mediaExtension 构造 media 扩展数组。
// [参数] assets: 生成结果。
// [返回] 结构化媒体列表。
// 最近修改时间：2026-10-17；改动原因：新增，media 扩展。
func mediaExtension(assets []creationAsset) []map[string]any {
	out := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		if asset.URL == "" {
			continue
		}
		item := map[string]any{"url": asset.URL, "type": "image"}
		if asset.isVideo() {
			item["type"] = "video"
		}
		if asset.Key != "" {
			item["key"] = asset.Key
		}
		if asset.Model != "" {
			item["model"] = asset.Model
		}
		if asset.Width > 0 {
			item["width"] = asset.Width
		}
		if asset.Height > 0 {
			item["height"] = asset.Height
		}
		out = append(out, item)
	}
	return out
}

// describeAssets 生成一句人类可读的媒体摘要。
//
// 生成类回复本身没有正文，直接返回空 content 会让客户端显示空白消息，
// 因此用摘要兜底。
// [参数] assets: 生成结果。
// [返回] 摘要文本。
// 最近修改时间：2026-10-17；改动原因：新增，空正文兜底。
func describeAssets(assets []creationAsset) string {
	if len(assets) == 0 {
		return ""
	}
	parts := make([]string, 0, len(assets))
	for _, asset := range assets {
		kind := "图片"
		if asset.isVideo() {
			kind = "视频"
		}
		desc := "已生成" + kind
		if asset.Model != "" {
			desc += "（" + asset.Model + "）"
		}
		if asset.Width > 0 && asset.Height > 0 {
			desc += fmt.Sprintf(" %d×%d", asset.Width, asset.Height)
		}
		parts = append(parts, desc)
	}
	return strings.Join(parts, "；")
}

// streamAssetFrames 把生成结果转成流式帧。
//
// 生成结果无法拆成增量：它是一次性到齐的完整资源，因此在流末尾单独发帧。
// [参数] model: 模型名；assets: 生成结果。
// [返回] 帧 JSON 列表。
// 最近修改时间：2026-10-17；改动原因：新增，生成结果流式返回。
func streamAssetFrames(model string, assets []creationAsset) [][]byte {
	if len(assets) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(assets))
	for _, asset := range assets {
		if asset.URL == "" {
			continue
		}
		delta := map[string]any{"media": mediaExtension([]creationAsset{asset})}
		if asset.isVideo() {
			delta["content"] = "[视频] " + asset.URL
		} else {
			delta["content"] = ""
		}
		payload, errMarshal := json.Marshal(streamChunk(model, "", delta, ""))
		if errMarshal != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}
