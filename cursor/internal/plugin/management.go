package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	managementStatusPath   = "/plugins/" + providerName + "/status"
	managementDisabledPath = "/plugins/" + providerName + "/disabled-models"
	managementImportPath   = "/plugins/" + providerName + "/import"
	managementExportPath   = "/plugins/" + providerName + "/export"
	managementDeletePath   = "/plugins/" + providerName + "/delete"
	managementEnablePath   = "/plugins/" + providerName + "/enable"
	managementDisablePath  = "/plugins/" + providerName + "/disable"
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

func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: managementStatusPath},
			{Method: http.MethodPut, Path: managementDisabledPath},
			{Method: http.MethodPost, Path: managementImportPath},
			{Method: http.MethodGet, Path: managementExportPath},
			{Method: http.MethodPost, Path: managementDeletePath},
			{Method: http.MethodPost, Path: managementEnablePath},
			{Method: http.MethodPost, Path: managementDisablePath},
		},
		Resources: []managementResource{{
			Path:        "/status",
			Menu:        "Cursor",
			Description: "Cursor status, usage, token import and model controls / Cursor 状态、用量、Token 导入与模型管理。",
		}},
	}
}

func (handler *Handler) handleManagement(ctx context.Context, raw []byte) (any, error) {
	var request managementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(request.Path)
	switch {
	case strings.Contains(path, "/v0/resource/plugins/") && strings.HasSuffix(path, "/status"):
		return managementPageResponse(), nil
	case strings.HasSuffix(path, "/status") && strings.EqualFold(request.Method, http.MethodGet):
		return handler.managementStatus(ctx)
	case strings.HasSuffix(path, "/disabled-models") && strings.EqualFold(request.Method, http.MethodPut):
		return handler.updateDisabledModels(ctx, request.Body)
	case strings.HasSuffix(path, "/import") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.importCredential(ctx, request.Body)
	case strings.HasSuffix(path, "/export") && strings.EqualFold(request.Method, http.MethodGet):
		return handler.exportCredentials(ctx)
	case strings.HasSuffix(path, "/delete") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.deleteCredential(ctx, request.Body)
	case strings.HasSuffix(path, "/enable") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.toggleCredential(ctx, request.Body, false)
	case strings.HasSuffix(path, "/disable") && strings.EqualFold(request.Method, http.MethodPost):
		return handler.toggleCredential(ctx, request.Body, true)
	default:
		return managementResponse{StatusCode: http.StatusNotFound, Headers: jsonHeaders(), Body: []byte(`{"error":"not found"}`)}, nil
	}
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}
}
