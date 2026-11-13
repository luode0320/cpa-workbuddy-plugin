package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// 本文件实现无水印直链换取。
//
// 无水印直链必须用**完整 key**（含 bucket 前缀）调用
// /creativity/resource/get_without_watermark；实测传去掉 bucket 的裸 key 会返回
// without_watermark:false，拿不到无水印版本。

// watermarkRequest 是无水印解析请求体。
type watermarkRequest struct {
	// URI 是资源 key 列表（必须含 bucket 前缀）。
	URI []string `json:"uri"`
}

// watermarkResponse 是无水印解析响应。
type watermarkResponse struct {
	// Code 是业务码。
	Code int `json:"code"`
	// Msg 是错误说明。
	Msg string `json:"msg"`
	// Data 是解析结果。
	Data struct {
		// WithoutWatermark 表示上游是否给出了无水印版本。
		WithoutWatermark bool `json:"without_watermark"`
		// DownloadImage 是下载用变体。
		DownloadImage map[string]watermarkVariant `json:"download_image"`
		// PreviewImage 是预览用变体。
		PreviewImage map[string]watermarkVariant `json:"preview_image"`
	} `json:"data"`
}

// watermarkVariant 是一种无水印形态。
type watermarkVariant struct {
	// URL 是签名地址。
	URL string `json:"url"`
	// URI 是资源标识。
	URI string `json:"uri"`
	// Width / Height 是尺寸。
	Width  int `json:"width"`
	Height int `json:"height"`
}

// hydrateAssets 把生成结果升级为无水印直链。
//
// 失败不视为请求失败：流里自带的缩略图 URL 仍然可用，只是画质降级。
// 视频不需要解析——它的地址本身就是直链。
// [参数] ctx: 上下文；sa: 账号；assets: 生成结果。
// [返回] 升级后的生成结果（可能与入参同一底层数组）。
// 最近修改时间：2026-10-17；改动原因：新增，无水印直链换取。
func hydrateAssets(ctx context.Context, sa *StoredAuth, assets []creationAsset) []creationAsset {
	var keys []string
	seen := make(map[string]bool, len(assets))
	for _, asset := range assets {
		if asset.Key == "" || asset.isVideo() || seen[asset.Key] {
			continue
		}
		seen[asset.Key] = true
		keys = append(keys, asset.Key)
	}
	if len(keys) == 0 {
		return assets
	}

	resolved, errResolve := resolveNoWatermark(ctx, sa, keys)
	if errResolve != nil || len(resolved) == 0 {
		return assets
	}

	for i := range assets {
		variant, ok := resolved[assets[i].Key]
		if !ok || variant.URL == "" {
			continue
		}
		assets[i].URL = variant.URL
		if variant.Width > 0 {
			assets[i].Width = variant.Width
		}
		if variant.Height > 0 {
			assets[i].Height = variant.Height
		}
	}
	return assets
}

// resolveNoWatermark 调用上游把资源 key 换成无水印直链。
//
// key 必须保留 bucket 前缀（形如 tos-cn-i-<bucket>/rc_gen_image/<hash>.jpeg）：
// 实测去掉前缀后上游返回 without_watermark:false，等于没解析。
// [参数] ctx: 上下文；sa: 账号；keys: 资源 key 列表。
// [返回] key → 变体的映射；上游未解析的 key 不会出现在结果中。
// 最近修改时间：2026-10-17；改动原因：新增，无水印端点调用。
func resolveNoWatermark(ctx context.Context, sa *StoredAuth, keys []string) (map[string]watermarkVariant, error) {
	out := make(map[string]watermarkVariant, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	raw, errMarshal := json.Marshal(watermarkRequest{URI: keys})
	if errMarshal != nil {
		return out, fmt.Errorf("编码无水印请求失败：%w", errMarshal)
	}

	body, errPost := postBusinessJSON(ctx, sa, "/creativity/resource/get_without_watermark", raw)
	if errPost != nil {
		return out, errPost
	}

	var resp watermarkResponse
	if errUnmarshal := json.Unmarshal(body, &resp); errUnmarshal != nil {
		return out, fmt.Errorf("解析无水印响应失败：%w", errUnmarshal)
	}
	if resp.Code != 0 {
		return out, &upstreamError{Code: resp.Code, Message: statusCodeHint(resp.Code, resp.Msg)}
	}
	if !resp.Data.WithoutWatermark {
		return out, nil
	}

	for key, variant := range resp.Data.DownloadImage {
		if variant.URL != "" {
			out[key] = variant
		}
	}
	for key, variant := range resp.Data.PreviewImage {
		if _, exists := out[key]; !exists && variant.URL != "" {
			out[key] = variant
		}
	}
	return out, nil
}
