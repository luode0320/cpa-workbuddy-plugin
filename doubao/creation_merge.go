package main

import "encoding/json"

// 本文件实现同一生成资源的多次上报合并。
//
// 视频是异步产物，同一条会分多帧上报：首帧只有 status，末帧才补 download_url、
// 时长与尺寸。按 ID 对齐、后到非空覆盖先到空值，才能拿到完整结果。

// mergeAssets 把新到的生成结果并入已有列表。
//
// 同一条视频会被反复上报：首帧只有 status，末帧才带直链、时长与尺寸。按 ID 合并
// 并让后到的非空字段覆盖先到的空值，才能拿到完整结果。没有 ID 的条目直接追加。
// [参数] existing: 已累计的结果；incoming: 本次解析出的结果。
// [返回] 合并后的列表。
// 最近修改时间：2026-11-18 23:34:00；改动原因：图片能力删除，注释改为视频措辞。
func mergeAssets(existing, incoming []creationAsset) []creationAsset {
	index := make(map[string]int, len(existing))
	for i, asset := range existing {
		if asset.ID != "" {
			index[asset.ID] = i
		}
	}

	for _, asset := range incoming {
		if asset.ID == "" {
			existing = append(existing, asset)
			continue
		}
		if i, ok := index[asset.ID]; ok {
			merged := existing[i]
			// 每个字段单独取非空值：末帧可能只补 URL 而不重复生成器名。
			if asset.Key != "" {
				merged.Key = asset.Key
			}
			if asset.URL != "" {
				merged.URL = asset.URL
			}
			if asset.Model != "" {
				merged.Model = asset.Model
			}
			if asset.Width > 0 {
				merged.Width = asset.Width
			}
			if asset.Height > 0 {
				merged.Height = asset.Height
			}
			if asset.Type != 0 {
				merged.Type = asset.Type
			}
			if asset.Status != 0 {
				merged.Status = asset.Status
			}
			// 时长只在末帧才有，同样按非零覆盖，避免首帧的空值盖掉真实值。
			if asset.Duration > 0 {
				merged.Duration = asset.Duration
			}
			existing[i] = merged
			continue
		}
		index[asset.ID] = len(existing)
		existing = append(existing, asset)
	}
	return existing
}

// collectCreationAssets 从一个内容块集合中收集生成结果。
// [参数] blocks: 内容块列表；existing: 已累计结果。
// [返回] 合并后的结果列表。
// 最近修改时间：2026-10-17；改动原因：新增，流事件中的生成结果收集。
func collectCreationAssets(blocks []responseBlock, existing []creationAsset) []creationAsset {
	for _, block := range blocks {
		if block.BlockType != blockTypeCreation {
			continue
		}
		var content blockContent
		if json.Unmarshal(block.Content, &content) != nil || len(content.CreationBlock) == 0 {
			continue
		}
		existing = mergeAssets(existing, parseCreationBlock(content.CreationBlock))
	}
	return existing
}
