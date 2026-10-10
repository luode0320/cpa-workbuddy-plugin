package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	managementStatusPath     = "/plugins/" + providerName + "/status"
	managementAccountsPath   = "/plugins/" + providerName + "/accounts"
	managementModelsPath     = "/plugins/" + providerName + "/models"
	managementTestActivePath = "/plugins/" + providerName + "/test-active"
	managementDisabledPath   = "/plugins/" + providerName + "/disabled-models"
	managementImportPath     = "/plugins/" + providerName + "/import"
	managementExportPath     = "/plugins/" + providerName + "/export"
	managementDeletePath     = "/plugins/" + providerName + "/delete"
	managementEnablePath     = "/plugins/" + providerName + "/enable"
	managementDisablePath    = "/plugins/" + providerName + "/disable"
)

type managementRegistrationResponse struct {
	Routes    []managementRoute    `json:"routes"`
	Resources []managementResource `json:"resources"`
}

type managementRoute struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method string      `json:"Method"`
	Path   string      `json:"Path"`
	Body   []byte      `json:"Body"`
	Query  http.Header `json:"-"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// managementRegistration 声明 Cursor 插件导出的管理 API 路由与前端页面资源。
// [返回] 路由与资源列表，对外暴露 /panel 与 /status 双资源路由，以及完整的账号管理接口。
// 最近修改时间 2026-10-11（对齐全仓库 /panel 路由规范与 /accounts、/models、/test-active 接口）
func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: managementStatusPath},
			{Method: http.MethodGet, Path: managementAccountsPath},
			{Method: http.MethodGet, Path: managementModelsPath},
			{Method: http.MethodPost, Path: managementTestActivePath},
			{Method: http.MethodPut, Path: managementDisabledPath},
			{Method: http.MethodPost, Path: managementImportPath},
			{Method: http.MethodGet, Path: managementExportPath},
			{Method: http.MethodPost, Path: managementDeletePath},
			{Method: http.MethodPost, Path: managementEnablePath},
			{Method: http.MethodPost, Path: managementDisablePath},
		},
		Resources: []managementResource{
			{
				Path:        "/panel",
				Menu:        "Cursor",
				Description: "Cursor dashboard: status, usage, token import and model controls / Cursor 控制面板：状态、用量、Token 导入与模型管理。",
			},
			{
				Path:        "/status",
				Menu:        "Cursor",
				Description: "Cursor status (compatible alias) / Cursor 状态页（兼容别名）。",
			},
		},
	}
}

// handleManagement 分发管理 API 请求与前端资源请求。
// [参数] ctx: 上下文；raw: 原始 JSON 序列化的 managementRequest。
// [返回] 接口响应体或错误。
// 最近修改时间 2026-10-11（支持 /panel、/accounts、/models 与 /test-active 请求分发）
func (handler *Handler) handleManagement(ctx context.Context, raw []byte) (any, error) {
	var request managementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(request.Path)
	cleanPath := path
	if idx := strings.Index(cleanPath, "?"); idx >= 0 {
		cleanPath = cleanPath[:idx]
	}
	switch {
	case strings.Contains(path, "/v0/resource/plugins/") && (strings.HasSuffix(cleanPath, "/status") || strings.HasSuffix(cleanPath, "/panel")):
		return managementPageResponse(), nil
	case (strings.HasSuffix(cleanPath, "/status") || strings.HasSuffix(cleanPath, "/accounts")) && strings.EqualFold(request.Method, http.MethodGet):
		return handler.managementStatus(ctx)
	case strings.HasSuffix(cleanPath, "/models") && strings.EqualFold(request.Method, http.MethodGet):
		return handler.handleModelsQuery(ctx, request)
	case strings.HasSuffix(cleanPath, "/test-active") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.handleTestActive(ctx, request.Body)
	case strings.HasSuffix(cleanPath, "/disabled-models") && strings.EqualFold(request.Method, http.MethodPut):
		return handler.updateDisabledModels(ctx, request.Body)
	case strings.HasSuffix(cleanPath, "/import") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.importCredential(ctx, request.Body)
	case strings.HasSuffix(cleanPath, "/export") && strings.EqualFold(request.Method, http.MethodGet):
		return handler.exportCredentials(ctx)
	case strings.HasSuffix(cleanPath, "/delete") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.deleteCredential(ctx, request.Body)
	case strings.HasSuffix(cleanPath, "/enable") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.toggleCredential(ctx, request.Body, false)
	case strings.HasSuffix(cleanPath, "/disable") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.toggleCredential(ctx, request.Body, true)
	default:
		return managementResponse{StatusCode: http.StatusNotFound, Headers: jsonHeaders(), Body: []byte(`{"error":"not found"}`)}, nil
	}
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}
}
