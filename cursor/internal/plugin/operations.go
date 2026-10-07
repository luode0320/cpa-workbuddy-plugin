package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// decodeAuthIndex 从管理请求体中读取 auth_index 字段（无标签结构体，避免与
// 宿主 wire 契约耦合）。JSON 非法时返回 error。
func decodeAuthIndex(body []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return "", err
	}
	var value string
	if raw, ok := fields["auth_index"]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return strings.TrimSpace(value), nil
}

// exportCredentials 实现 GET /plugins/cursor-provider/export。
// 返回 {version, exported_at, plugin, count, accounts:[{name, auth_index,
// credential}]}，每条 credential 为物理认证文件的完整 JSON（含会话令牌），
// 可再通过 /import 重新导入。响应含凭据，务必仅在鉴权后暴露。
func (handler *Handler) exportCredentials(ctx context.Context) (managementResponse, error) {
	files, err := handler.cursorAuthFiles(ctx)
	if err != nil {
		return managementError(http.StatusBadGateway, err.Error()), nil
	}
	accounts := make([]map[string]any, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.AuthIndex) == "" {
			continue
		}
		get, _, getErr := handler.getCursorAuth(ctx, file.AuthIndex)
		if getErr != nil {
			accounts = append(accounts, map[string]any{
				"name": file.Name, "auth_index": file.AuthIndex, "load_error": errString(getErr),
			})
			continue
		}
		var credential any
		_ = json.Unmarshal(get.JSON, &credential)
		accounts = append(accounts, map[string]any{
			"name": file.Name, "auth_index": file.AuthIndex, "credential": credential,
		})
	}
	return managementJSON(http.StatusOK, map[string]any{
		"version":     1,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"plugin":      providerName,
		"count":       len(accounts),
		"accounts":    accounts,
	})
}

// deleteCredential 实现 POST /plugins/cursor-provider/delete。
// 严格校验：账号必须存在、属于本插件命名空间、auth_index 一致、物理路径安全，
// 删除范围限定在认证目录内。
func (handler *Handler) deleteCredential(ctx context.Context, body []byte) (managementResponse, error) {
	authIndex, err := decodeAuthIndex(body)
	if err != nil {
		return managementError(http.StatusBadRequest, "invalid JSON body"), nil
	}
	if authIndex == "" {
		return managementError(http.StatusBadRequest, "auth_index is required"), nil
	}
	files, err := handler.cursorAuthFiles(ctx)
	if err != nil {
		return managementError(http.StatusBadGateway, err.Error()), nil
	}
	for _, file := range files {
		if file.AuthIndex != authIndex {
			continue
		}
		if !isCursorAuthFileName(file.Name) {
			return managementError(http.StatusBadRequest, "不是 Cursor 认证文件"), nil
		}
		get, credential, getErr := handler.getCursorAuth(ctx, authIndex)
		if getErr != nil {
			return managementError(http.StatusBadGateway, "host.auth.get: "+getErr.Error()), nil
		}
		if strings.TrimSpace(get.AuthIndex) != authIndex {
			return managementError(http.StatusConflict, "认证索引不一致"), nil
		}
		path := strings.TrimSpace(get.Path)
		if path == "" {
			return managementError(http.StatusBadRequest, "认证文件路径缺失，无法安全删除"), nil
		}
		if !isSafeCursorAuthPath(path) {
			return managementError(http.StatusBadRequest, "认证文件路径不安全，已拒绝删除"), nil
		}
		if err := deleteCursorAuthFileInDir(path, filepath.Dir(path)); err != nil {
			return managementError(http.StatusInternalServerError, "删除认证文件失败: "+err.Error()), nil
		}
		return managementJSON(http.StatusOK, map[string]any{
			"ok": true, "auth_index": authIndex,
			"label": cursorCredentialLabel(credential), "deleted": file.Name,
		})
	}
	return managementError(http.StatusNotFound, "account not found: "+authIndex), nil
}

// toggleCredential 实现 POST /plugins/cursor-provider/{enable,disable}。
// 请求体 {auth_index} 指定单个账号；留空表示全部账号。禁用标记写入物理认证
// 文件顶层 disabled 字段（直写通道，避免 host.auth.save 丢弃未知顶层字段）。
func (handler *Handler) toggleCredential(ctx context.Context, body []byte, disable bool) (managementResponse, error) {
	authIndex, _ := decodeAuthIndex(body)
	action := "enabled"
	if disable {
		action = "disabled"
	}
	files, err := handler.cursorAuthFiles(ctx)
	if err != nil {
		return managementError(http.StatusBadGateway, err.Error()), nil
	}
	if authIndex == "" {
		count := 0
		for _, file := range files {
			if strings.TrimSpace(file.AuthIndex) == "" {
				continue
			}
			if err := handler.persistDisabledToggle(ctx, file.AuthIndex, disable); err == nil {
				count++
			}
		}
		return managementJSON(http.StatusOK, map[string]any{"ok": true, "action": action, "count": count})
	}
	for _, file := range files {
		if file.AuthIndex != authIndex {
			continue
		}
		if err := handler.persistDisabledToggle(ctx, authIndex, disable); err != nil {
			return managementError(http.StatusInternalServerError, err.Error()), nil
		}
		return managementJSON(http.StatusOK, map[string]any{"ok": true, "action": action, "auth_index": authIndex})
	}
	return managementError(http.StatusNotFound, "account not found: "+authIndex), nil
}

// persistDisabledToggle 把顶层 disabled 标记写回物理认证文件。写入走直写通道
// （persistCursorAuthDirect），保证 disabled 标记在宿主重建记录后仍然保留。
func (handler *Handler) persistDisabledToggle(ctx context.Context, authIndex string, disabled bool) error {
	get, _, err := handler.getCursorAuth(ctx, authIndex)
	if err != nil {
		return err
	}
	if len(get.JSON) == 0 {
		return nil
	}
	var doc map[string]any
	if unmarshalErr := json.Unmarshal(get.JSON, &doc); unmarshalErr != nil {
		doc = map[string]any{}
	}
	doc["disabled"] = disabled
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(get.Path)
	if path == "" {
		return nil
	}
	return persistCursorAuthDirect(path, raw)
}
