package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

var (
	managementBasePathMu sync.RWMutex
	managementBasePath   = "/v0/management"
)

func setManagementBasePath(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	managementBasePathMu.Lock()
	defer managementBasePathMu.Unlock()
	managementBasePath = base
}

func getManagementBasePath() string {
	managementBasePathMu.RLock()
	defer managementBasePathMu.RUnlock()
	return managementBasePath
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + zcodeProviderID
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List ZCode accounts."},
			{Method: http.MethodGet, Path: base + "/status", Description: "List ZCode accounts status."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import ZCode API keys."},
			{Method: http.MethodGet, Path: base + "/export", Description: "Export all ZCode credentials."},
			{Method: http.MethodPost, Path: base + "/export", Description: "Export all ZCode credentials."},
			{Method: http.MethodPost, Path: base + "/delete", Description: "Delete one ZCode account."},
			{Method: http.MethodPost, Path: base + "/enable", Description: "Enable one ZCode account."},
			{Method: http.MethodPost, Path: base + "/disable", Description: "Disable one ZCode account."},
			{Method: http.MethodPost, Path: base + "/enable-all", Description: "Enable all ZCode accounts."},
			{Method: http.MethodPost, Path: base + "/disable-all", Description: "Disable all ZCode accounts."},
			{Method: http.MethodGet, Path: base + "/models", Description: "List models available for one account."},
			{Method: http.MethodPost, Path: base + "/test-active", Description: "Send an active ping chat inference request for one account."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "ZCode", Description: "ZCode Provider dashboard: accounts, models, import, active test."},
		},
	}
}

type accountSummary struct {
	AuthID       string   `json:"auth_id"`
	AuthIndex    string   `json:"auth_index"`
	Label        string   `json:"label"`
	APIKeyMasked string   `json:"api_key_masked"`
	Disabled     bool     `json:"disabled"`
	TestFailed   bool     `json:"test_failed"`
	CoolingDown  bool     `json:"cooling_down"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Models       []string `json:"models"`
}

func normalizeMgmtSubPath(rawPath string) (string, url.Values) {
	u, err := url.Parse(rawPath)
	path := rawPath
	var query url.Values
	if err == nil {
		path = u.Path
		query = u.Query()
	}
	path = strings.TrimRight(path, "/")

	// 兼容 /v0/resource/plugins/zcode-provider/...
	resPrefix := "/v0/resource/plugins/" + zcodeProviderID
	if strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub == "" {
			return "/panel", query
		}
		return sub, query
	}

	// 兼容 /v0/management/plugins/zcode-provider/... 与 /plugins/zcode-provider/...
	fullPrefix := getManagementBasePath() + "/plugins/" + zcodeProviderID
	shortPrefix := "/plugins/" + zcodeProviderID
	switch {
	case strings.HasPrefix(path, fullPrefix):
		path = strings.TrimPrefix(path, fullPrefix)
	case strings.HasPrefix(path, shortPrefix):
		path = strings.TrimPrefix(path, shortPrefix)
	}
	if path == "" {
		path = "/"
	}
	return path, query
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	subPath, urlQuery := normalizeMgmtSubPath(req.Path)
	method := strings.ToUpper(req.Method)

	if method == http.MethodGet && (subPath == "/" || subPath == "/panel") {
		return handlePanelPage(), nil
	}

	getQueryParam := func(key string) string {
		if vals := req.Query[key]; len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			return strings.TrimSpace(vals[0])
		}
		if urlQuery != nil {
			if v := strings.TrimSpace(urlQuery.Get(key)); v != "" {
				return v
			}
		}
		return ""
	}

	switch {
	case method == http.MethodGet && (subPath == "/accounts" || subPath == "/status"):
		return handleListAccounts()

	case method == http.MethodPost && subPath == "/import":
		return handleImportAccounts(req.Body)

	case (method == http.MethodPost || method == http.MethodGet) && subPath == "/export":
		return handleExportAccounts()

	case method == http.MethodPost && subPath == "/delete":
		return handleDeleteAccount(req.Body)

	case method == http.MethodPost && subPath == "/enable":
		return handleSetAccountStatus(req.Body, false)

	case method == http.MethodPost && subPath == "/disable":
		return handleSetAccountStatus(req.Body, true)

	case method == http.MethodPost && subPath == "/enable-all":
		return handleSetAllAccountStatus(false)

	case method == http.MethodPost && subPath == "/disable-all":
		return handleSetAllAccountStatus(true)

	case method == http.MethodGet && subPath == "/models":
		authID := getQueryParam("auth_index")
		if authID == "" {
			authID = getQueryParam("auth_id")
		}
		if authID == "" {
			return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_index is required"}`))
		}
		data, err := handleModelsQuery(authID)
		if err != nil {
			return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		}
		return makeHTTPResponse(http.StatusOK, data)

	case method == http.MethodPost && subPath == "/test-active":
		data, err := handleTestActive(req.Body)
		if err != nil {
			return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		}
		return makeHTTPResponse(http.StatusOK, data)

	default:
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"not_found"}`))
	}
}

func makeHTTPResponse(status int, body []byte) ([]byte, error) {
	resp := pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: body,
	}
	return okEnvelope(resp)
}

func handleListAccounts() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	list := make([]accountSummary, 0, len(accounts))
	for _, acc := range accounts {
		models := acc.Models
		if len(models) == 0 {
			models = defaultModels
		}
		list = append(list, accountSummary{
			AuthID:       acc.AuthID,
			AuthIndex:    acc.AuthID,
			Label:        acc.Label,
			APIKeyMasked: maskAPIKey(acc.APIKey),
			Disabled:     acc.Disabled,
			TestFailed:   acc.TestFailed,
			CoolingDown:  isAccountCoolingDown(acc.AuthID),
			CreatedAt:    acc.CreatedAt,
			UpdatedAt:    acc.UpdatedAt,
			Models:       models,
		})
	}

	body, _ := json.Marshal(map[string]any{
		"accounts": list,
		"count":    len(list),
	})
	return makeHTTPResponse(http.StatusOK, body)
}

func handleImportAccounts(body []byte) ([]byte, error) {
	content := gjson.GetBytes(body, "content").String()
	if content == "" {
		content = string(body)
	}

	lines := strings.Split(content, "\n")
	imported := 0
	skipped := 0
	var errs []string

	now := time.Now().Format(time.RFC3339)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var apiKey, label string
		if strings.Contains(line, "----") {
			parts := strings.SplitN(line, "----", 2)
			apiKey = strings.TrimSpace(parts[0])
			label = strings.TrimSpace(parts[1])
		} else if strings.HasPrefix(line, "{") {
			apiKey = gjson.Get(line, "api_key").String()
			label = gjson.Get(line, "label").String()
		} else {
			apiKey = line
		}

		if apiKey == "" {
			skipped++
			continue
		}

		authID := authIDFor(apiKey)
		fileName := authFileNameFor(apiKey)
		fullPath := filepath.Join(getAccountsDir(), fileName)

		if label == "" {
			label = "ZCode " + maskAPIKey(apiKey)
		}

		sa := &StoredAuth{
			Type:       zcodeProviderID,
			Provider:   zcodeProviderID,
			AuthID:     authID,
			APIKey:     apiKey,
			Label:      label,
			Disabled:   false,
			CreatedAt:  now,
			UpdatedAt:  now,
			Models:     defaultModels,
			TestFailed: false,
		}

		if err := writeAuthFileDirect(fullPath, sa); err != nil {
			errs = append(errs, fmt.Sprintf("failed to save %s: %v", authID, err))
		} else {
			imported++
		}
	}

	res := map[string]any{
		"ok":       true,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
	}
	resBytes, _ := json.Marshal(res)
	return makeHTTPResponse(http.StatusOK, resBytes)
}

func handleExportAccounts() ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	data, _ := json.MarshalIndent(accounts, "", "  ")
	return makeHTTPResponse(http.StatusOK, data)
}

func extractAuthIDFromBody(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "auth_id").String())
	if id == "" {
		id = strings.TrimSpace(gjson.GetBytes(body, "auth_index").String())
	}
	return id
}

func handleDeleteAccount(body []byte) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id is required"}`))
	}

	if err := deleteAuthFileDirect(authID); err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

func handleSetAccountStatus(body []byte, disabled bool) ([]byte, error) {
	authID := extractAuthIDFromBody(body)
	if authID == "" {
		return makeHTTPResponse(http.StatusBadRequest, []byte(`{"error":"auth_id is required"}`))
	}

	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	var found *StoredAuth
	for _, acc := range accounts {
		if acc.AuthID == authID {
			found = acc
			break
		}
	}

	if found == nil {
		return makeHTTPResponse(http.StatusNotFound, []byte(`{"error":"account not found"}`))
	}

	found.Disabled = disabled
	found.UpdatedAt = time.Now().Format(time.RFC3339)
	fullPath := filepath.Join(getAccountsDir(), authFileNameFor(found.APIKey))

	if err := writeAuthFileDirect(fullPath, found); err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	return makeHTTPResponse(http.StatusOK, []byte(`{"ok":true}`))
}

func handleSetAllAccountStatus(disabled bool) ([]byte, error) {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return makeHTTPResponse(http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
	}

	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, acc := range accounts {
		acc.Disabled = disabled
		acc.UpdatedAt = now
		fullPath := filepath.Join(getAccountsDir(), authFileNameFor(acc.APIKey))
		if err := writeAuthFileDirect(fullPath, acc); err == nil {
			count++
		}
	}

	return makeHTTPResponse(http.StatusOK, []byte(fmt.Sprintf(`{"ok":true,"updated":%d}`, count)))
}
