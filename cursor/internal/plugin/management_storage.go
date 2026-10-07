package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

func (handler *Handler) cursorAuthFiles(ctx context.Context) ([]hostAuthFile, error) {
	if handler.host == nil {
		return nil, errors.New("host callbacks are unavailable")
	}
	raw, err := handler.host.Call(ctx, "host.auth.list", struct{}{})
	if err != nil {
		return nil, fmt.Errorf("list auth files: %w", err)
	}
	var response hostAuthListResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode auth list: %w", err)
	}
	files := make([]hostAuthFile, 0, len(response.Files))
	seenPhysicalFiles := make(map[string]struct{}, len(response.Files))
	for _, file := range response.Files {
		isCursor := strings.EqualFold(file.Provider, providerName) || strings.EqualFold(file.Type, providerName)
		isPersistent := file.Source == "" || strings.EqualFold(file.Source, "file")
		if file.RuntimeOnly || !isCursor || !isPersistent {
			continue
		}
		key := cursorPhysicalFileIdentity(file)
		if _, duplicate := seenPhysicalFiles[key]; key != "" && duplicate {
			continue
		}
		if key != "" {
			seenPhysicalFiles[key] = struct{}{}
		}
		files = append(files, file)
	}
	return files, nil
}

func cursorPhysicalFileIdentity(file hostAuthFile) string {
	if path := strings.ToLower(strings.TrimSpace(file.Path)); path != "" {
		return "path:" + path
	}
	if name := strings.ToLower(strings.TrimSpace(file.Name)); name != "" {
		return "name:" + name
	}
	return ""
}

func (handler *Handler) getCursorCredential(ctx context.Context, authIndex string) (cursorauth.Credentials, error) {
	_, credential, err := handler.getCursorAuth(ctx, authIndex)
	return credential, err
}

func (handler *Handler) getCursorAuth(ctx context.Context, authIndex string) (hostAuthGetResponse, cursorauth.Credentials, error) {
	if handler.host == nil {
		return hostAuthGetResponse{}, cursorauth.Credentials{}, errors.New("host callbacks are unavailable")
	}
	raw, err := handler.host.Call(ctx, "host.auth.get", struct {
		AuthIndex string `json:"auth_index"`
	}{AuthIndex: authIndex})
	if err != nil {
		return hostAuthGetResponse{}, cursorauth.Credentials{}, fmt.Errorf("read Cursor auth: %w", err)
	}
	var response hostAuthGetResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return hostAuthGetResponse{}, cursorauth.Credentials{}, fmt.Errorf("decode Cursor auth: %w", err)
	}
	credential, err := cursorauth.ParseCredentials(response.JSON)
	if err != nil {
		return hostAuthGetResponse{}, cursorauth.Credentials{}, err
	}
	return response, credential, nil
}

func setDisabledModels(raw json.RawMessage, models []string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode Cursor auth JSON: %w", err)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("encode disabled models: %w", err)
	}
	if len(models) == 0 {
		delete(fields, "disabled_models")
	} else {
		fields["disabled_models"] = encoded
	}
	return json.Marshal(fields)
}

func sortedUniqueModels(models []string) []string {
	set := normalizedModelSet(models)
	result := make([]string, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	slices.Sort(result)
	return result
}

func normalizedModelSet(models []string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		if id := normalizeModelID(model); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func normalizeModelID(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "cursor/")
}

func managementJSON(status int, payload any) (managementResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return managementResponse{}, err
	}
	return managementResponse{StatusCode: status, Headers: jsonHeaders(), Body: body}, nil
}

func managementError(status int, message string) managementResponse {
	response, _ := managementJSON(status, map[string]string{"error": message})
	return response
}
