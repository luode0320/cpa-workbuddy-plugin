package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/fingerprint"
)

const (
	cloudAICompanionBaseURL = "https://cloudaicompanion.googleapis.com/v1"
	releaseChannelProduct   = "GEMINI_CODE_ASSIST"
	releaseChannelValue     = "EXPERIMENTAL"
)

// configurePreviewChannels enables the EXPERIMENTAL release channel for each project.
// Failures are returned as a multi-project error; callers may treat them as best-effort.
func configurePreviewChannels(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string, projectIDs []string) error {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return fmt.Errorf("gemini-cli access token is missing")
	}
	projects := cleanStringList(projectIDs)
	if len(projects) == 0 {
		return fmt.Errorf("gemini-cli project_id is required")
	}

	var failed []string
	for _, projectID := range projects {
		if errConfigure := configurePreviewChannel(ctx, client, accessToken, projectID); errConfigure != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", projectID, errConfigure))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("configure preview channel: %s", strings.Join(failed, "; "))
	}
	return nil
}

// configurePreviewChannel enables the EXPERIMENTAL Gemini Code Assist release channel
// for a single Google Cloud project so preview models become available.
func configurePreviewChannel(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string, projectID string) error {
	client = requireHTTPClient(client)
	accessToken = strings.TrimSpace(accessToken)
	projectID = strings.TrimSpace(projectID)
	if accessToken == "" {
		return fmt.Errorf("gemini-cli access token is missing")
	}
	if projectID == "" {
		return fmt.Errorf("gemini-cli project_id is required")
	}

	settingID, errSettingID := randomPreviewID("preview-setting")
	if errSettingID != nil {
		return errSettingID
	}
	bindingID, errBindingID := randomPreviewID("preview-binding")
	if errBindingID != nil {
		return errBindingID
	}

	baseURL := fmt.Sprintf("%s/projects/%s/locations/global", cloudAICompanionBaseURL, url.PathEscape(projectID))
	settingURL := baseURL + "/releaseChannelSettings"

	// Step 1: create Release Channel Setting with EXPERIMENTAL channel.
	settingBody, errMarshalSetting := json.Marshal(map[string]string{
		"release_channel": releaseChannelValue,
	})
	if errMarshalSetting != nil {
		return fmt.Errorf("marshal release channel setting: %w", errMarshalSetting)
	}
	settingResp, errSetting := client.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     settingURL + "?release_channel_setting_id=" + url.QueryEscape(settingID),
		Headers: previewAuthHeaders(accessToken),
		Body:    settingBody,
	})
	if errSetting != nil {
		return fmt.Errorf("create release channel setting: %w", errSetting)
	}

	switch settingResp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// Created successfully; keep generated settingID.
	case http.StatusConflict:
		// Setting already exists; resolve the real setting ID for step 2.
		existingID, errExisting := listReleaseChannelSettingID(ctx, client, accessToken, settingURL)
		if errExisting != nil {
			return errExisting
		}
		if existingID != "" {
			settingID = existingID
		}
	default:
		return fmt.Errorf("create release channel setting failed: status %d: %s", settingResp.StatusCode, strings.TrimSpace(string(settingResp.Body)))
	}

	// Step 2: bind the setting to the current project for Gemini Code Assist.
	bindingBody, errMarshalBinding := json.Marshal(map[string]string{
		"target":  "projects/" + projectID,
		"product": releaseChannelProduct,
	})
	if errMarshalBinding != nil {
		return fmt.Errorf("marshal setting binding: %w", errMarshalBinding)
	}
	bindingURL := fmt.Sprintf("%s/%s/settingBindings?setting_binding_id=%s", settingURL, url.PathEscape(settingID), url.QueryEscape(bindingID))
	bindingResp, errBinding := client.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     bindingURL,
		Headers: previewAuthHeaders(accessToken),
		Body:    bindingBody,
	})
	if errBinding != nil {
		return fmt.Errorf("create setting binding: %w", errBinding)
	}

	switch bindingResp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusConflict:
		// 409 means the binding already exists, which is treated as success.
		return nil
	default:
		return fmt.Errorf("create setting binding failed: status %d: %s", bindingResp.StatusCode, strings.TrimSpace(string(bindingResp.Body)))
	}
}

func listReleaseChannelSettingID(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string, settingURL string) (string, error) {
	listResp, errList := client.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     settingURL,
		Headers: previewAuthHeaders(accessToken),
	})
	if errList != nil {
		return "", fmt.Errorf("list release channel settings: %w", errList)
	}
	if listResp.StatusCode < 200 || listResp.StatusCode >= 300 {
		return "", fmt.Errorf("list release channel settings failed: status %d: %s", listResp.StatusCode, strings.TrimSpace(string(listResp.Body)))
	}

	var payload struct {
		ReleaseChannelSettings []struct {
			Name string `json:"name"`
		} `json:"releaseChannelSettings"`
	}
	if errUnmarshal := json.Unmarshal(listResp.Body, &payload); errUnmarshal != nil {
		return "", fmt.Errorf("decode release channel settings: %w", errUnmarshal)
	}
	if len(payload.ReleaseChannelSettings) == 0 {
		return "", nil
	}
	name := strings.TrimSpace(payload.ReleaseChannelSettings[0].Name)
	if name == "" {
		return "", nil
	}
	parts := strings.Split(name, "/")
	return strings.TrimSpace(parts[len(parts)-1]), nil
}

func previewAuthHeaders(accessToken string) http.Header {
	return http.Header{
		"Accept":            []string{"application/json"},
		"Authorization":     []string{"Bearer " + strings.TrimSpace(accessToken)},
		"Content-Type":      []string{"application/json"},
		"User-Agent":        []string{fingerprint.UserAgent("")},
		"X-Goog-Api-Client": []string{fingerprint.APIClientHeader},
	}
}

func randomPreviewID(prefix string) (string, error) {
	buf := make([]byte, 4)
	if _, errRead := rand.Read(buf); errRead != nil {
		return "", fmt.Errorf("generate preview id: %w", errRead)
	}
	return prefix + "-" + hex.EncodeToString(buf), nil
}
