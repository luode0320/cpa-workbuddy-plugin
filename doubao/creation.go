package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
)

// 本文件定义生成结果的数据模型。
//
// 生成结果不是响应里的一个字段，而是 block_type=2074（CREATION）的内容块。
// 同一份资源会上报多次：首帧只有占位尺寸，末帧才带资源 key 与可访问 URL。
// 因此必须按 id 合并，且以后到的完整版本覆盖先到的占位版本——只取首帧会拿到
// 一个没有 URL 的空壳。
//
// 视频块结构（2026-11-17 实测，异步产物，只在 /im/chain/single 里出现）：
//
//	content.creation_block.creations[i].video.download_url       成品直链（带签名）
//	content.creation_block.creations[i].video.width / height     字符串，不是数字
//	content.creation_block.creations[i].video.video_model        字符串套 JSON，内含 video_list
//	content.creation_block.creations[i].video.cover              只有 key 与两个变体，没有 url（不是成品，不参与取值）
//
// 解析见 creation_parse.go，OpenAI 多模态转换见 creation_render.go。
// 最近修改时间：2026-11-25 00:29:00；改动原因：playable 吸收 ChinaCPA status==3 双判据。

// creationAsset 是一份生成结果，已解析到可访问 URL。
type creationAsset struct {
	// Type 是资源类型，当前恒为 2（视频）。
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
	// Model 是生成器名称（来自首帧 video_model 或封面的生成器信息）。
	Model string
	// Status 是上游生成状态。
	Status int
	// Duration 是视频的真实时长（秒，浮点）。
	//
	// 与「请求的目标时长」不是一回事：上游可能按可用档位取整或改写，所以这里
	// 记录的是产物实测值，用来让调用方知道真正拿到了多长的片子。
	Duration float64
}

// creationStatusFinished 是上游标记生成已定稿的状态值。
const creationStatusFinished = 3

// isVideo 判断该资源是否为视频。
//
// 只留视频一条能力后它恒为 true，但仍然保留：渲染层与回灌层用它区分「成品直链」
// 与「封面兜底」两类地址，将来若重新引入非视频产物，判定点已经在这里。
// [参数] 无。
// [返回] 视频返回 true。
// 最近修改时间：2026-11-18 22:40:00；改动原因：图片能力删除，注释说明保留理由。
func (a creationAsset) isVideo() bool { return a.Type == 2 }

// playable 判断该资源是否已经有定稿的可播放直链。
//
// 不能只看 URL 非空：上游会推「生成中」的块，可能带预览直链但状态未定稿（status≠3），
// 用它当成品会让回灌拿到还没稳定的片子。status==3 且 URL 非空才算真成品。
// [参数] 无。
// [返回] 定稿且有直链返回 true。
// 最近修改时间：2026-11-25 00:29:00；改动原因：吸收 ChinaCPA status==3 双判据。
func (a creationAsset) playable() bool {
	return a.isVideo() && a.Status == creationStatusFinished && strings.TrimSpace(a.URL) != ""
}

// hasPlayableAsset 判断一批生成结果里是否已经有可播放的成品。
// [参数] assets: 生成结果列表。
// [返回] 存在可播放成品返回 true。
// 最近修改时间：2026-11-21 13:30:00；改动原因：新增，替代裸的 len(assets) 判据。
func hasPlayableAsset(assets []creationAsset) bool {
	for _, asset := range assets {
		if asset.playable() {
			return true
		}
	}
	return false
}

// creationItem 是 creations[] 中的一条。
type creationItem struct {
	// Type 是资源类型。
	Type int `json:"type"`
	// ID 是资源标识。
	ID string `json:"id"`
	// CanPublish 表示上游是否允许发布该资源。
	CanPublish bool `json:"can_publish"`
	// Video 是视频负载。
	Video *creationVideo `json:"video"`
}

// creationVariant 是一种渲染形态。
type creationVariant struct {
	// URL 是签名地址。
	URL string `json:"url"`
	// URI 是资源标识。
	URI string `json:"uri"`
	// Width / Height 是该形态尺寸。
	//
	// 视频侧的 cover 子对象（image_thumb / image_preview）是数字；但 video 顶层的
	// width / height 是字符串，两者不能共用同一个字段类型，所以这里保持 int，
	// 字符串尺寸由 creationVideo 的 own 字段承接。
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

// 流地址的水印等级，数值越小越优先。
const (
	streamRankClean       = 0
	streamRankPlain       = 1
	streamRankWatermarked = 2
)

// videoStreamRank 判定一条流地址的水印等级。
//
// 素材地址用 query 参数 lr 携带水印标识：含 unwatermark 是原片，带 lr 但不含
// unwatermark 是水印片，没有 lr 是普通片。抓包来源为 ChinaCPA pickCleanestVideoUrl。
// [参数] rawURL: 解码后的流地址。
// [返回] 水印等级，见 streamRank* 常量；解析失败按普通片处理。
// 最近修改时间：2026-11-24 23:56:00；改动原因：吸收 ChinaCPA 无水印选优判据。
func videoStreamRank(rawURL string) int {
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return streamRankPlain
	}
	lr := parsed.Query().Get("lr")
	if lr == "" {
		return streamRankPlain
	}
	if strings.Contains(strings.ToLower(lr), "unwatermark") {
		return streamRankClean
	}
	return streamRankWatermarked
}

// bestStream 挑一条最清晰且无水印的直链。
//
// 优先 main_url，其次 backup_url_1；两者都是 base64，解码失败即跳过。先按水印
// 等级（videoStreamRank）再按像素面积与文件体积取最优，避免依赖 video_1 / video_2
// 的键名顺序（同一份响应里 video_2 是 720p h265、video_1 是 720p h264，键名不表达清晰度）。
// detail 级 url_expire 已过期的清晰度整组跳过。
// [返回] 解码后的地址与尺寸；没有可用条目时返回空。
// 最近修改时间：2026-11-24 23:56:00；改动原因：吸收 ChinaCPA 无水印选优与过期排除判据。
func (v *creationVideo) bestStream() (string, int, int) {
	detail := v.videoModelOf()
	if detail == nil {
		return "", 0, 0
	}
	// url_expire 是整组清晰度的过期时间戳（秒），过期后这组地址全部不可用。
	nowUnix := time.Now().Unix()
	if detail.URLExpire > 0 && detail.URLExpire <= nowUnix {
		return "", 0, 0
	}
	// video_list 是 map，遍历顺序随机；同分辨率多码率时 score 相同，只按 score
	// 比较会让每次请求挑到不同的直链，排障时不可复现。因此同分（同水印等级同 score）
	// 时再按 key 定序，把「随机」压成「固定」。
	keys := make([]string, 0, len(detail.VideoList))
	for key := range detail.VideoList {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	bestURL, bestW, bestH := "", 0, 0
	bestRank, bestScore := streamRankWatermarked+1, int64(-1)
	for _, key := range keys {
		entry := detail.VideoList[key]
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
		rank := videoStreamRank(addr)
		score := int64(entry.VWidth) * int64(entry.VHeight)
		if score == 0 {
			score = entry.Size
		}
		if rank < bestRank || (rank == bestRank && score > bestScore) {
			bestURL, bestW, bestH = addr, entry.VWidth, entry.VHeight
			bestRank, bestScore = rank, score
		}
	}
	return bestURL, bestW, bestH
}
