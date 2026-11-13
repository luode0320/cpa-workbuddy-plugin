package main

import "encoding/json"

// 本文件实现 CREATION 块的解析与同资源多次上报的合并。
//
// 合并规则：按 ID 对齐，后到的非空字段覆盖先到的空值。首帧只有 placeholder
// （宽高与生成器名），末帧才补 key 与直链；只取首帧会得到没有 URL 的空壳。

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
		if item.Image != nil {
			if asset, ok := assetFromImage(item); ok {
				out = append(out, asset)
			}
		}
		if item.Video != nil {
			if asset, ok := assetFromVideo(item); ok {
				out = append(out, asset)
			}
		}
	}
	return out
}

// assetFromImage 把图片条目转成生成结果。
// [参数] item: 生成物条目。
// [返回] 生成结果与是否可用。
// 最近修改时间：2026-10-17；改动原因：新增，图片条目转换。
func assetFromImage(item creationItem) (creationAsset, bool) {
	img := item.Image
	asset := creationAsset{
		Type:   item.Type,
		ID:     item.ID,
		Key:    img.Key,
		Status: img.Status,
	}
	// 上游偶发把图片的 type 置 0，按图片处理。
	if asset.Type == 0 {
		asset.Type = 1
	}

	// 缩略图优先：它是现成的签名地址；只有 key 时还需二次调用才能换 URL。
	if img.ImageThumb != nil {
		asset.URL = img.ImageThumb.URL
		asset.Width = img.ImageThumb.Width
		asset.Height = img.ImageThumb.Height
		if asset.Key == "" {
			asset.Key = img.ImageThumb.URI
		}
	}
	if img.ImageOri != nil && img.ImageOri.URL != "" {
		// 原图优于缩略图：分辨率与画质都更高。
		asset.URL = img.ImageOri.URL
		if img.ImageOri.Width > 0 {
			asset.Width = img.ImageOri.Width
		}
		if img.ImageOri.Height > 0 {
			asset.Height = img.ImageOri.Height
		}
	}
	if asset.URL == "" {
		asset.URL = img.URL
	}
	if img.Placeholder != nil {
		asset.Model = img.Placeholder.Description
		if asset.Width == 0 {
			asset.Width = img.Placeholder.Width
		}
		if asset.Height == 0 {
			asset.Height = img.Placeholder.Height
		}
	}

	if asset.Key == "" && asset.URL == "" {
		return asset, false
	}
	return asset, true
}

// assetFromVideo 把视频条目转成生成结果。
// [参数] item: 生成物条目。
// [返回] 生成结果与是否可用。
// 最近修改时间：2026-10-17；改动原因：新增，视频条目转换。
func assetFromVideo(item creationItem) (creationAsset, bool) {
	video := item.Video
	asset := creationAsset{
		Type:   2,
		ID:     item.ID,
		Key:    video.Key,
		URL:    video.URL,
		Status: video.Status,
	}
	if video.Cover != nil {
		// 封面兼作海报帧，也是视频地址未就绪时的回退地址。
		if asset.URL == "" {
			asset.URL = video.Cover.URL
		}
		asset.Width = video.Cover.Width
		asset.Height = video.Cover.Height
	}
	if video.VideoInfo != nil && video.VideoInfo.URL != "" {
		asset.URL = video.VideoInfo.URL
	}
	if asset.Key == "" && asset.URL == "" {
		return asset, false
	}
	return asset, true
}
