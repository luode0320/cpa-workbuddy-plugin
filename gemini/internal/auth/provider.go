package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	ProviderKey              = "gemini-cli"
	authRefreshLead          = 5 * time.Minute
	authFallbackRefreshAfter = 30 * time.Minute
)

type Provider struct{}

type Storage struct {
	Type         string         `json:"type,omitempty"`
	Email        string         `json:"email,omitempty"`
	ProjectID    string         `json:"project_id,omitempty"`
	ProjectIDs   []string       `json:"project_ids,omitempty"`
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	TokenType    string         `json:"token_type,omitempty"`
	Expiry       string         `json:"expiry,omitempty"`
	Token        map[string]any `json:"token,omitempty"`
	Raw          map[string]any `json:"-"`
}

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Identifier() string { return ProviderKey }

func (p *Provider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	storage, errParse := ParseStorage(req.RawJSON)
	if errParse != nil {
		return pluginapi.AuthParseResponse{Handled: true}, errParse
	}
	if storage == nil {
		return pluginapi.AuthParseResponse{}, nil
	}
	auths, errBuild := BuildAuths(req.FileName, *storage)
	if errBuild != nil {
		return pluginapi.AuthParseResponse{Handled: true}, errBuild
	}
	resp := pluginapi.AuthParseResponse{Handled: true, Auths: auths}
	if len(auths) > 0 {
		resp.Auth = auths[0]
	}
	return resp, nil
}

func (p *Provider) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	storage, errParse := ParseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.AuthRefreshResponse{}, errParse
	}
	if storage == nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("gemini-cli auth storage is missing")
	}
	token, errRefresh := p.refreshToken(ctx, req.HTTPClient, *storage)
	if errRefresh != nil {
		return pluginapi.AuthRefreshResponse{}, errRefresh
	}
	applyToken(storage, token)
	if rawProject, ok := req.Metadata["project_id"].(string); ok && strings.TrimSpace(rawProject) != "" {
		storage.ProjectID = strings.TrimSpace(rawProject)
	}
	virtual := strings.EqualFold(strings.TrimSpace(req.Attributes["runtime_only"]), "true")
	if rawVirtual, ok := req.Metadata["virtual"].(bool); ok && rawVirtual {
		virtual = true
	}
	if strings.EqualFold(metadataString(req.Metadata, "virtual"), "true") {
		virtual = true
	}
	data := authDataFromStorage(req.AuthID, req.AuthID, *storage, virtual, metadataString(req.Metadata, "parent_auth_id"))
	mergeMissingMetadata(data.Metadata, req.Metadata)
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
}

func ParseStorage(raw []byte) (*Storage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		return nil, fmt.Errorf("decode gemini-cli auth: %w", errUnmarshal)
	}
	providerType := strings.ToLower(strings.TrimSpace(stringFromMap(decoded, "type")))
	if providerType != "gemini" && providerType != ProviderKey {
		return nil, nil
	}
	storage := Storage{Raw: cloneAnyMap(decoded)}
	if errMarshal := mapToStruct(decoded, &storage); errMarshal != nil {
		return nil, errMarshal
	}
	storage.Type = ProviderKey
	storage.Email = strings.TrimSpace(storage.Email)
	storage.ProjectID = strings.TrimSpace(storage.ProjectID)
	storage.ProjectIDs = cleanStringList(storage.ProjectIDs)
	normalizeProjectIDs(&storage)
	if storage.Token == nil {
		storage.Token = tokenMapFromTopLevel(decoded)
	}
	if storage.AccessToken == "" {
		storage.AccessToken = stringFromMap(storage.Token, "access_token")
	}
	if storage.RefreshToken == "" {
		storage.RefreshToken = stringFromMap(storage.Token, "refresh_token")
	}
	if storage.TokenType == "" {
		storage.TokenType = stringFromMap(storage.Token, "token_type")
	}
	if storage.Expiry == "" {
		storage.Expiry = stringFromMap(storage.Token, "expiry")
	}
	return &storage, nil
}

func normalizeProjectIDs(storage *Storage) {
	if storage == nil {
		return
	}
	projects := splitProjectIDList(storage.ProjectID)
	if len(projects) == 0 {
		storage.ProjectIDs = cleanStringList(storage.ProjectIDs)
		return
	}
	storage.ProjectID = projects[0]
	storage.ProjectIDs = cleanStringList(append(projects, storage.ProjectIDs...))
}

func BuildAuths(fileName string, storage Storage) ([]pluginapi.AuthData, error) {
	storage.Type = ProviderKey
	projects := cleanStringList(storage.ProjectIDs)
	if storage.ProjectID == "" && len(projects) > 0 {
		storage.ProjectID = projects[0]
	}
	if storage.ProjectID != "" && !containsString(projects, storage.ProjectID) {
		projects = append([]string{storage.ProjectID}, projects...)
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = defaultFileName(storage)
	}
	if fileName == "" {
		fileName = "gemini-cli.json"
	}

	primary := authDataFromStorage(fileName, fileName, storage, false, "")
	auths := []pluginapi.AuthData{primary}
	for _, projectID := range projects {
		if projectID == "" || projectID == storage.ProjectID {
			continue
		}
		virtual := storage
		virtual.ProjectID = projectID
		virtualName := virtualFileName(storage, projectID)
		auths = append(auths, authDataFromStorage(virtualName, virtualName, virtual, true, primary.ID))
	}
	return auths, nil
}

func buildPersistentAuths(fileName string, storage Storage) ([]pluginapi.AuthData, error) {
	auths, errBuild := BuildAuths(fileName, storage)
	if errBuild != nil {
		return nil, errBuild
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return []pluginapi.AuthData{auths[0]}, nil
}

func authDataFromStorage(id string, fileName string, storage Storage, virtual bool, parentID string) pluginapi.AuthData {
	raw := storage.RawJSON()
	metadata := cloneAnyMap(storage.Raw)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["type"] = ProviderKey
	if storage.Email != "" {
		metadata["email"] = storage.Email
	}
	if storage.ProjectID != "" {
		metadata["project_id"] = storage.ProjectID
	}
	if len(storage.ProjectIDs) > 0 {
		metadata["project_ids"] = append([]string(nil), storage.ProjectIDs...)
	}
	if storage.AccessToken != "" {
		metadata["access_token"] = storage.AccessToken
	}
	if storage.RefreshToken != "" {
		metadata["refresh_token"] = storage.RefreshToken
	}
	if storage.TokenType != "" {
		metadata["token_type"] = storage.TokenType
	}
	if storage.Expiry != "" {
		metadata["expiry"] = storage.Expiry
	}
	if len(storage.Token) > 0 {
		metadata["token"] = cloneAnyMap(storage.Token)
	}
	if virtual {
		metadata["virtual"] = true
		metadata["parent_auth_id"] = parentID
	}
	label := storage.Email
	if storage.ProjectID != "" {
		if label == "" {
			label = storage.ProjectID
		} else {
			label = label + " / " + storage.ProjectID
		}
	}
	attributes := map[string]string{
		"project_id": storage.ProjectID,
	}
	if virtual {
		attributes["runtime_only"] = "true"
	}
	return pluginapi.AuthData{
		Provider:         ProviderKey,
		ID:               strings.TrimSpace(id),
		FileName:         filepath.Base(strings.TrimSpace(fileName)),
		Label:            label,
		StorageJSON:      raw,
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: nextRefreshAfter(storage, time.Now()),
	}
}

func nextRefreshAfter(storage Storage, now time.Time) time.Time {
	if !hasRefreshToken(storage) {
		return time.Time{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	if expiry, ok := storageExpiryTime(storage); ok {
		refreshAt := expiry.Add(-authRefreshLead)
		if !refreshAt.After(now) {
			return now
		}
		return refreshAt
	}
	return now.Add(authFallbackRefreshAfter)
}

func ShouldRefreshStorage(storage Storage, now time.Time) bool {
	if !hasRefreshToken(storage) {
		return false
	}
	if strings.TrimSpace(storage.AccessToken) == "" && strings.TrimSpace(stringFromMap(storage.Token, "access_token")) == "" {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}
	if expiry, ok := storageExpiryTime(storage); ok {
		return !expiry.Add(-authRefreshLead).After(now)
	}
	return false
}

func hasRefreshToken(storage Storage) bool {
	return strings.TrimSpace(storage.RefreshToken) != "" || strings.TrimSpace(stringFromMap(storage.Token, "refresh_token")) != ""
}

func storageExpiryTime(storage Storage) (time.Time, bool) {
	if ts, ok := parseRFC3339Time(storage.Expiry); ok {
		return ts, true
	}
	if ts, ok := parseRFC3339Time(stringFromMap(storage.Token, "expiry")); ok {
		return ts, true
	}
	return time.Time{}, false
}

func parseRFC3339Time(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	ts, errParse := time.Parse(time.RFC3339, trimmed)
	if errParse != nil {
		return time.Time{}, false
	}
	return ts, true
}

func mergeMissingMetadata(dst map[string]any, src map[string]any) {
	if dst == nil || len(src) == 0 {
		return
	}
	for key, value := range src {
		if _, exists := dst[key]; !exists {
			dst[key] = value
		}
	}
}

func (s Storage) RawJSON() []byte {
	out := cloneAnyMap(s.Raw)
	if out == nil {
		out = make(map[string]any)
	}
	out["type"] = ProviderKey
	if s.Email != "" {
		out["email"] = s.Email
	}
	if s.ProjectID != "" {
		out["project_id"] = s.ProjectID
	}
	if len(s.ProjectIDs) > 0 {
		out["project_ids"] = append([]string(nil), s.ProjectIDs...)
	}
	if s.AccessToken != "" {
		out["access_token"] = s.AccessToken
	}
	if s.RefreshToken != "" {
		out["refresh_token"] = s.RefreshToken
	}
	if s.TokenType != "" {
		out["token_type"] = s.TokenType
	}
	if s.Expiry != "" {
		out["expiry"] = s.Expiry
	}
	if len(s.Token) > 0 {
		out["token"] = cloneAnyMap(s.Token)
	}
	raw, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		return nil
	}
	return raw
}

func defaultFileName(storage Storage) string {
	base := sanitizeFilePart(storage.Email)
	if base == "" {
		base = "gemini-cli"
	}
	return base + "-gemini-cli.json"
}

func virtualFileName(storage Storage, projectID string) string {
	email := sanitizeFilePart(storage.Email)
	project := sanitizeFilePart(projectID)
	if email == "" {
		email = "gemini-cli"
	}
	if project == "" {
		project = "project"
	}
	return email + "-" + project + ".json"
}

func cleanStringList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func splitProjectIDList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return cleanStringList(strings.Split(value, ","))
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func sanitizeFilePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "@", "-", " ", "-", "\t", "-")
	value = replacer.Replace(value)
	value = strings.Trim(value, ".-")
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".-")
}
