package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件把生成结果转成 OpenAI 响应形态（多模态 content + media 扩展 + 流式帧）。

// firstChoiceMessage 取出 OpenAI 响应里第一条 choice 的 message 对象。
//
// 兼容两种形态：构造时用的 []map[string]any，以及任何一次 JSON 往返后的 []any。
// 直接断言 []map[string]any 只在「同一进程内刚构造完」时成立，中间过一次序列化
// 就会静默失败——附件全丢却不报错，那种 bug 只能靠读代码发现。
// [参数] resp: OpenAI 响应对象。
// [返回] message 对象与是否取到。
// 最近修改时间：2026-11-21 13:30:00；改动原因：新增，替代脆弱的单类型断言。
func firstChoiceMessage(resp map[string]any) (map[string]any, bool) {
	switch choices := resp["choices"].(type) {
	case []map[string]any:
		if len(choices) == 0 {
			return nil, false
		}
		message, ok := choices[0]["message"].(map[string]any)
		return message, ok
	case []any:
		if len(choices) == 0 {
			return nil, false
		}
		first, ok := choices[0].(map[string]any)
		if !ok {
			return nil, false
		}
		message, ok := first["message"].(map[string]any)
		return message, ok
	default:
		return nil, false
	}
}

// attachAssets 把生成结果挂到 OpenAI 响应对象上。
//
// 纯文本回复保持字符串 content（所有客户端都能处理）；只有存在媒体时才切成
// 数组形态，否则会改变普通回复的形状。视频没有对应的 OpenAI 内容类型，因此
// 以文本形式给出链接，同时在 media 扩展里保留结构化信息。
// [参数] resp: OpenAI 响应对象；assets: 生成结果。
// [返回] 无。
// 最近修改时间：2026-11-21 13:30:00；改动原因：改用 playable 判据，断言改走 firstChoiceMessage。
func attachAssets(resp map[string]any, assets []creationAsset) {
	if len(assets) == 0 {
		return
	}
	message, ok := firstChoiceMessage(resp)
	if !ok {
		return
	}

	parts := make([]map[string]any, 0, len(assets)+1)
	if text, isString := message["content"].(string); isString && text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, asset := range assets {
		if !asset.playable() {
			continue
		}
		parts = append(parts, map[string]any{"type": "text", "text": "[视频] " + asset.URL})
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
// 最近修改时间：2026-11-21 13:30:00；改动原因：改用 playable 判据，占位块不进 media。
func mediaExtension(assets []creationAsset) []map[string]any {
	out := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		if !asset.playable() {
			continue
		}
		item := map[string]any{"url": asset.URL, "type": "video"}
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
		// 时长上游没给时省略而不是写 0：0 秒是非法值，写成 0 会让调用方
		// 以为拿到了空片子。
		if asset.Duration > 0 {
			item["duration"] = asset.Duration
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
// 最近修改时间：2026-11-21 13:30:00；改动原因：只统计有直链的成品，避免与空 media 矛盾。
func describeAssets(assets []creationAsset) string {
	parts := make([]string, 0, len(assets))
	for _, asset := range assets {
		// 只说「已生成」必须与 media 里真的有条目一致，否则正文报成功、
		// 结构化字段却是空的，调用方两边对不上。
		if !asset.playable() {
			continue
		}
		desc := "已生成视频"
		if asset.Model != "" {
			desc += "（" + asset.Model + "）"
		}
		if asset.Width > 0 && asset.Height > 0 {
			desc += fmt.Sprintf(" %d×%d", asset.Width, asset.Height)
		}
		parts = append(parts, desc)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
}

// streamAssetFrames 把生成结果转成流式帧。
//
// 生成结果无法拆成增量：它是一次性到齐的完整资源，因此在流末尾单独发帧。
// [参数] model: 模型名；assets: 生成结果。
// [返回] 帧 JSON 列表。
// 最近修改时间：2026-11-21 13:30:00；改动原因：改用 playable 判据，占位块不发帧。
func streamAssetFrames(model string, assets []creationAsset) [][]byte {
	if len(assets) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(assets))
	for _, asset := range assets {
		if !asset.playable() {
			continue
		}
		delta := map[string]any{"media": mediaExtension([]creationAsset{asset}), "content": "[视频] " + asset.URL}
		payload, errMarshal := json.Marshal(streamChunk(model, "", delta, ""))
		if errMarshal != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}
