package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// 本文件定义生成结果的数据模型。
//
// 生成结果不是响应里的一个字段，而是 block_type=2074（CREATION）的内容块。
// 同一份资源会上报多次：首帧只有占位尺寸，末帧才带资源 key 与可访问 URL。
// 因此必须按 id 合并，且以后到的完整版本覆盖先到的占位版本——只取首帧会拿到
// 一个没有 URL 的空壳。
//
// 图片块结构（2026-10-17 实测，同步产物，同一次 /chat/completion 的 SSE 到齐）：
//
//	content.creation_block.creations[i].image.key        资源 key（tos-cn-i-<bucket>/rc_gen_image/<hash>.jpeg）
//	content.creation_block.creations[i].image.image_thumb.url   已签名的缩略图 URL（带水印）
//	content.creation_block.creations[i].image.placeholder       首帧只有宽高与生成器名
//
// 视频块结构（2026-11-17 实测，异步产物，只在 /im/chain/single 里出现）：
//
//	content.creation_block.creations[i].video.download_url       成品直链（带签名）
//	content.creation_block.creations[i].video.width / height     字符串，不是数字
//	content.creation_block.creations[i].video.video_model        字符串套 JSON，内含 video_list
//	content.creation_block.creations[i].video.cover              只有 key 与两个变体，没有 url
//
// 解析见 creation_parse.go，无水印换取见 creation_watermark.go，
// OpenAI 多模态转换见 creation_render.go。
// 最近修改时间：2026-11-17 22:10:00；改动原因：资产新增真实时长，供 media[] 回填。

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
	// Duration 是视频的真实时长（秒，浮点）；图片恒为 0。
	//
	// 与「请求的目标时长」不是一回事：上游可能按可用档位取整或改写，所以这里
	// 记录的是产物实测值，用来让调用方知道真正拿到了多长的片子。
	Duration float64
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
	//
	// 图片侧是数字，视频侧的 cover 子对象（image_thumb / image_preview）同样是
	// 数字；但 video 顶层的 width / height 是字符串，两者不能共用同一个字段类型，
	// 所以这里保持 int，字符串尺寸由 creationVideo 的 own 字段承接。
	Width  int `json:"width"`
	Height int `json:"height"`
	// URLFormats 是其他编码格式（heic 等）。
	URLFormats map[string]string `json:"url_formats"`
}

// creationVideoCover 是视频封面：真实响应里没有 url，只有 key 与两个变体。
type creationVideoCover struct {
	// Key 是封面资源 key。
	Key string `json:"key"`
	// ImageThumb 是缩略图变体，通常最先可访问。
	ImageThumb *creationVariant `json:"image_thumb"`
	// ImagePreview 是预览图变体，分辨率更高。
	ImagePreview *creationVariant `json:"image_preview"`
}

// creationVideo 是视频生成结果。
//
// 字段名由 2026-11-17 的真实 `/im/chain/single` 响应逐字段确认，与早期的猜测
// 版本有四处不同：地址叫 download_url 而不是 url；清晰度列表叫 video_model
// 且是**字符串套 JSON**；cover 没有 url；width / height 是字符串。按旧结构体
// 反序列化不会报错，只会得到一堆空值 —— 视频永远没有地址。
type creationVideo struct {
	// Vid 是视频资源标识。
	Vid string `json:"vid"`
	// Status 是生成状态，3 表示成品可取。
	Status int `json:"status"`
	// Key 是资源 key（部分响应有）。
	Key string `json:"key"`
	// DownloadURL 是成品直链，带签名与下载参数。
	DownloadURL string `json:"download_url"`
	// DownloadFilehash 是成品文件哈希。
	DownloadFilehash string `json:"download_filehash"`
	// VideoType 是容器格式（mp4）。
	VideoType string `json:"video_type"`
	// Duration 是时长（秒）。
	Duration float64 `json:"duration"`
	// Width / Height 是成品尺寸，真实响应里是**字符串**。
	Width  string `json:"width"`
	Height string `json:"height"`
	// Cover 是封面帧，视频地址未就绪时可作占位。
	Cover *creationVideoCover `json:"cover"`
	// VideoModel 是清晰度清单，真实响应里是**字符串套 JSON**：先按字符串收，
	// 再由 videoModelOf() 解一层。
	VideoModel string `json:"video_model"`
}

// videoModelDetail 是 video_model 解包后的结构（只保留取地址需要的字段）。
type videoModelDetail struct {
	// Status 是清晰度清单的构造状态，10 表示成功。
	Status int `json:"status"`
	// Message 是状态描述。
	Message string `json:"message"`
	// VideoDuration 是时长（秒）。
	VideoDuration float64 `json:"video_duration"`
	// MediaType 固定为 video。
	MediaType string `json:"media_type"`
	// PosterURL 是海报地址。
	PosterURL string `json:"poster_url"`
	// URLExpire 是地址过期时间戳（秒）。
	URLExpire int64 `json:"url_expire"`
	// VideoList 是各清晰度条目，键名形如 video_1 / video_2。
	VideoList map[string]videoStreamEntry `json:"video_list"`
}

// videoStreamEntry 是一条清晰度记录。
//
// MainURL / BackupURL1 都是 **base64 编码的 URL**，不是直接地址。
type videoStreamEntry struct {
	// Definition 是清晰度名（360p / 720p / 1080p）。
	Definition string `json:"definition"`
	// VType 是容器格式。
	VType string `json:"vtype"`
	// VWidth / VHeight 是该清晰度的像素尺寸。
	VWidth  int `json:"vwidth"`
	VHeight int `json:"vheight"`
	// Size 是文件字节数。
	Size int64 `json:"size"`
	// CodecType 是编码器（h264 / bytevc1）。
	CodecType string `json:"codec_type"`
	// MainURL 是 base64 编码的主地址。
	MainURL string `json:"main_url"`
	// BackupURL1 是 base64 编码的备用地址。
	BackupURL1 string `json:"backup_url_1"`
}

// videoModelOf 解出 video_model 里套的那层 JSON。
//
// [返回] 解包结果与是否成功；未携带或格式异常时返回 nil。
// 最近修改时间：2026-11-17 01:20:00；改动原因：新增，video_model 是字符串套 JSON。
func (v *creationVideo) videoModelOf() *videoModelDetail {
	if v == nil || strings.TrimSpace(v.VideoModel) == "" {
		return nil
	}
	var detail videoModelDetail
	if json.Unmarshal([]byte(v.VideoModel), &detail) != nil {
		return nil
	}
	return &detail
}

// bestStream 挑一条最清晰的直链。
//
// 优先 main_url，其次 backup_url_1；两者都是 base64，解码失败即跳过。按像素
// 面积与文件体积取最大，避免依赖 video_1 / video_2 的键名顺序（同一份响应里
// video_2 是 720p h265、video_1 是 720p h264，键名不表达清晰度）。
// [返回] 解码后的地址与尺寸；没有可用条目时返回空。
// 最近修改时间：2026-11-17 01:20:00；改动原因：新增，从 video_model 取回直链。
func (v *creationVideo) bestStream() (string, int, int) {
	detail := v.videoModelOf()
	if detail == nil {
		return "", 0, 0
	}
	bestURL, bestW, bestH, bestScore := "", 0, 0, int64(-1)
	for _, entry := range detail.VideoList {
		raw := entry.MainURL
		if strings.TrimSpace(raw) == "" {
			raw = entry.BackupURL1
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		decoded, errDecode := base64.StdEncoding.DecodeString(raw)
		if errDecode != nil {
			continue
		}
		addr := strings.TrimSpace(string(decoded))
		if addr == "" {
			continue
		}
		score := int64(entry.VWidth) * int64(entry.VHeight)
		if score == 0 {
			score = entry.Size
		}
		if score > bestScore {
			bestURL, bestW, bestH, bestScore = addr, entry.VWidth, entry.VHeight, score
		}
	}
	return bestURL, bestW, bestH
}
