package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// 本文件实现 CREATION 块的解析与同资源多次上报的合并。
//
// 合并规则：按 ID 对齐，后到的非空字段覆盖先到的空值。首帧只有尺寸，末帧才补
// key 与直链；只取首帧会得到没有 URL 的空壳。

// creationBlockPayload 是 CREATION 块的 content。
//
// 两种拼写都要认：整消息事件把负载包在 creation_block 下，而 patch_value
// 已经剥掉了这一层，直接给 creations。
type creationBlockPayload struct {
	CreationBlock struct {
		Creations []creationItem `json:"creations"`
	} `json:"creation_block"`
	Creations []creationItem `json:"creations"`
}

// allCreations 返回两种拼写下实际存在的 creations 列表。
// [参数] 无。
// [返回] 生成物条目列表。
// 最近修改时间：2026-10-17；改动原因：新增，双形态兼容。
func (p *creationBlockPayload) allCreations() []creationItem {
	if len(p.CreationBlock.Creations) > 0 {
		return p.CreationBlock.Creations
	}
	return p.Creations
}

// parseCreationBlock 从 CREATION 块体中提取全部生成结果。
//
// 尚无 URL 也没有 key 的条目会被跳过：那是纯占位帧，交出去只会得到空链接。
// [参数] raw: 块的 content 原文。
// [返回] 生成结果列表。
// 最近修改时间：2026-10-17；改动原因：新增，CREATION 块解析。
func parseCreationBlock(raw json.RawMessage) []creationAsset {
	if len(raw) == 0 {
		return nil
	}
	var payload creationBlockPayload
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}

	var out []creationAsset
	for _, item := range payload.allCreations() {
		if item.Video != nil {
			if asset, ok := assetFromVideo(item); ok {
				out = append(out, asset)
			}
		}
	}
	return out
}

// assetFromVideo 把视频条目转成生成结果。
//
// 地址只取成品：download_url → video_model 里最清晰的 main_url。封面图不是成品，
// 它是一张 JPEG，塞进 URL 会让 media 里出现 {"type":"video","url":"...jpg"} 这种
// 自相矛盾的条目，客户端按视频去播放只会失败。占位阶段就让它没有 URL，由回灌层
// 继续等（见 playable）。
// [参数] item: 生成物条目。
// [返回] 生成结果与是否可用。
// 最近修改时间：2026-11-21 13:30:00；改动原因：封面不再兜底地址与尺寸，避免冒充成品。
func assetFromVideo(item creationItem) (creationAsset, bool) {
	video := item.Video
	if video == nil {
		return creationAsset{}, false
	}
	asset := creationAsset{
		Type:   2,
		ID:     item.ID,
		Key:    video.Key,
		URL:    video.DownloadURL,
		Status: video.Status,
		// 时长是产物实测值（真实抓包 10.08），调用方据此判断拿到的片子有多长。
		Duration: video.Duration,
	}
	// 顶层 width / height 是字符串；解析失败按 0 处理，尺寸只是展示信息。
	if w, errW := strconv.Atoi(strings.TrimSpace(video.Width)); errW == nil && w > 0 {
		asset.Width = w
	}
	if h, errH := strconv.Atoi(strings.TrimSpace(video.Height)); errH == nil && h > 0 {
		asset.Height = h
	}
	// download_url 缺失时退到清晰度清单里的直链；它同样是成品，尺寸也更准。
	if strings.TrimSpace(asset.URL) == "" {
		if addr, w, h := video.bestStream(); addr != "" {
			asset.URL = addr
			if w > 0 {
				asset.Width = w
			}
			if h > 0 {
				asset.Height = h
			}
		}
	}
	// 占位帧也要交出去：回灌层靠「有没有出现过创作块」判断上游是否真的开跑，
	// 丢掉占位块会让它以为上游没启动，直接误报「上游没有开始渲染」。
	if asset.Key == "" && strings.TrimSpace(asset.URL) == "" {
		return asset, false
	}
	return asset, true
}
