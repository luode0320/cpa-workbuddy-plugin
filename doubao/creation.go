package main

// 本文件定义生成结果的数据模型。
//
// 生成结果不是响应里的一个字段，而是 SSE 流中一个 block_type=2074（CREATION）
// 的内容块。同一张图会上报多次：首帧只有占位尺寸，末帧才带资源 key 与可访问 URL。
// 因此必须按 id 合并，且以后到的完整版本覆盖先到的占位版本——只取首帧会拿到
// 一个没有 URL 的空壳。
//
// 块结构（2026-10-17 实测）：
//
//	content.creation_block.creations[i].image.key        资源 key（tos-cn-i-<bucket>/rc_gen_image/<hash>.jpeg）
//	content.creation_block.creations[i].image.image_thumb.url   已签名的缩略图 URL（带水印）
//	content.creation_block.creations[i].image.placeholder       首帧只有宽高与生成器名
//
// 解析见 creation_parse.go，无水印换取见 creation_watermark.go，
// OpenAI 多模态转换见 creation_render.go。

// creationAsset 是一份生成结果，已解析到可访问 URL。
type creationAsset struct {
	// Type 是资源类型：1 图片，2 视频。
	Type int
	// ID 是上游对该生成物的标识，用于合并同一资源的多次上报。
	ID string
	// Key 是内容寻址资源 key，无水印解析的入参。
	Key string
	// URL 是可直接访问的地址。
	URL string
	// Width / Height 是成品尺寸。
	Width  int
	Height int
	// Model 是生成器名称（来自首帧 placeholder.description）。
	Model string
	// Status 是上游生成状态。
	Status int
}

// isVideo 判断该资源是否为视频。
// [参数] 无。
// [返回] 视频返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，类型判定。
func (a creationAsset) isVideo() bool { return a.Type == 2 }

// creationItem 是 creations[] 中的一条。
type creationItem struct {
	// Type 是资源类型。
	Type int `json:"type"`
	// ID 是资源标识。
	ID string `json:"id"`
	// CanPublish 表示上游是否允许发布该资源。
	CanPublish bool `json:"can_publish"`
	// Image 是图片负载。
	Image *creationImage `json:"image"`
	// Video 是视频负载。
	Video *creationVideo `json:"video"`
}

// creationImage 是图片生成结果。
type creationImage struct {
	// Status 是生成状态。
	Status int `json:"status"`
	// Key 是资源 key。
	Key string `json:"key"`
	// URL 是直接地址（部分帧直接给）。
	URL string `json:"url"`
	// ImageThumb 是缩略图变体，通常最先可访问。
	ImageThumb *creationVariant `json:"image_thumb"`
	// ImageOri 是原图变体。
	ImageOri *creationVariant `json:"image_ori"`
	// Placeholder 是占位信息，首帧只有它。
	Placeholder *creationPlaceholder `json:"placeholder"`
}

// creationPlaceholder 是生成中的占位信息。
type creationPlaceholder struct {
	// Width / Height 是预期尺寸。
	Width  int `json:"width"`
	Height int `json:"height"`
	// Description 是生成器名称。
	Description string `json:"description"`
}

// creationVariant 是一种渲染形态。
type creationVariant struct {
	// URL 是签名地址。
	URL string `json:"url"`
	// URI 是资源标识。
	URI string `json:"uri"`
	// Width / Height 是该形态尺寸。
	Width  int `json:"width"`
	Height int `json:"height"`
	// URLFormats 是其他编码格式（heic 等）。
	URLFormats map[string]string `json:"url_formats"`
}

// creationVideo 是视频生成结果。
type creationVideo struct {
	// Status 是生成状态。
	Status int `json:"status"`
	// Key 是资源 key。
	Key string `json:"key"`
	// URL 是视频地址。
	URL string `json:"url"`
	// Cover 是封面帧，视频地址未就绪时可作占位。
	Cover *creationVariant `json:"cover"`
	// VideoInfo 是视频信息。
	VideoInfo *creationVariant `json:"video_info"`
	// Duration 是时长（秒）。
	Duration float64 `json:"duration"`
}
