package auth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini/internal/fingerprint"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func decodeObfuscated(encoded []byte, key byte) string {
	res := make([]byte, len(encoded))
	for i, b := range encoded {
		res[i] = b ^ key
	}
	return string(res)
}

var (
	oauthClientID     = decodeObfuscated([]byte{108, 98, 107, 104, 111, 111, 98, 106, 99, 105, 99, 111, 119, 53, 53, 98, 60, 46, 104, 53, 42, 40, 62, 40, 52, 42, 99, 63, 105, 59, 43, 60, 108, 59, 44, 105, 50, 55, 62, 51, 56, 107, 105, 111, 48, 116, 59, 42, 42, 41, 116, 61, 53, 53, 61, 54, 63, 47, 41, 63, 40, 57, 53, 52, 46, 63, 52, 46, 116, 57, 53, 55}, 0x5A)
	oauthClientSecret = decodeObfuscated([]byte{29, 21, 25, 9, 10, 2, 119, 110, 47, 18, 61, 23, 10, 55, 119, 107, 53, 109, 9, 49, 119, 61, 63, 12, 108, 25, 47, 111, 57, 54, 2, 28, 41, 34, 54}, 0x5A)
)

const (
	tokenURL          = "https://oauth2.googleapis.com/token"
	userInfoURL       = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"
	projectsURL       = "https://cloudresourcemanager.googleapis.com/v1/projects"
	codeAssistBaseURL = "https://cloudcode-pa.googleapis.com"
	codeAssistVersion = "v1internal"
	loginTimeout      = 3 * time.Minute
	manualPromptDelay = 15 * time.Second
	pollInterval      = 2 * time.Second
	onboardingTimeout = 30 * time.Second
	onboardingPoll    = 2 * time.Second
)

var oauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

type callbackPayload struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

func (p *Provider) StartLogin(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	redirectURI := strings.TrimSpace(req.BaseURL)
	if redirectURI == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("gemini-cli oauth redirect URL is empty")
	}
	state, errState := randomState()
	if errState != nil {
		return pluginapi.AuthLoginStartResponse{}, errState
	}
	verifier, challenge, errPKCE := pkcePair()
	if errPKCE != nil {
		return pluginapi.AuthLoginStartResponse{}, errPKCE
	}
	conf := oauthConfig(redirectURI)
	authURL := conf.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	metadata := map[string]any{
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
		"expires_at":    time.Now().Add(loginTimeout).UTC().Format(time.RFC3339),
	}
	if projectID := metadataString(req.Metadata, "project_id"); projectID != "" {
		metadata["project_id"] = projectID
	}
	return pluginapi.AuthLoginStartResponse{
		Provider:  ProviderKey,
		URL:       authURL,
		State:     state,
		ExpiresAt: time.Now().Add(loginTimeout).UTC(),
		Metadata:  metadata,
	}, nil
}

func (p *Provider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	expiresAt := metadataString(req.Metadata, "expires_at")
	if expiresAt != "" {
		deadline, errParse := time.Parse(time.RFC3339, expiresAt)
		if errParse == nil && time.Now().After(deadline) {
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "OAuth login timed out"}, nil
		}
	}
	payload, ok, errCallback := readCallbackPayload(req.Host.AuthDir, req.State)
	if errCallback != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: errCallback.Error()}, nil
	}
	if !ok {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending}, nil
	}
	if payload.Error != "" {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: payload.Error}, nil
	}
	if payload.Code == "" {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending}, nil
	}
	if errConsume := consumeCallbackPayload(req.Host.AuthDir, req.State); errConsume != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: fmt.Errorf("consume gemini-cli oauth callback: %w", errConsume).Error()}, nil
	}
	storage, errFinalize := p.finalizeCode(ctx, req.HTTPClient, payload.Code, req.Metadata)
	if errFinalize != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: errFinalize.Error()}, nil
	}
	auths, errBuild := buildPersistentAuths("", *storage)
	if errBuild != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: errBuild.Error()}, nil
	}
	resp := pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auths: auths}
	if len(auths) > 0 {
		resp.Auth = auths[0]
	}
	return resp, nil
}

func (p *Provider) RegisterCommandLine(context.Context, pluginapi.CommandLineRegistrationRequest) (pluginapi.CommandLineRegistrationResponse, error) {
	return pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{Name: "geminicli-login", Usage: "Run Gemini CLI OAuth login.", Type: "bool", DefaultValue: "false"},
			{Name: "geminicli-project-id", Usage: "Default Google Cloud project ID for Gemini CLI auth.", Type: "string"},
		},
	}, nil
}

func (p *Provider) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	if !flagBool(req.TriggeredFlags, "geminicli-login") {
		return pluginapi.CommandLineExecutionResponse{}, nil
	}
	noBrowser := flagBoolValue(req.Flags, "no-browser")
	projectID := flagString(req.Flags, "geminicli-project-id")
	auths, stdout, errLogin := p.runLocalLogin(ctx, req.Host.ProxyURL, projectID, noBrowser)
	if errLogin != nil {
		return pluginapi.CommandLineExecutionResponse{Stdout: stdout, Stderr: []byte(errLogin.Error() + "\n"), ExitCode: 1}, nil
	}
	return pluginapi.CommandLineExecutionResponse{Stdout: stdout, Auths: auths}, nil
}

func (p *Provider) runLocalLogin(ctx context.Context, proxyURL string, projectID string, noBrowser bool) ([]pluginapi.AuthData, []byte, error) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		return nil, nil, fmt.Errorf("start OAuth callback listener: %w", errListen)
	}
	defer func() {
		_ = listener.Close()
	}()
	redirectURI := "http://" + listener.Addr().String() + "/oauth2callback"
	codeCh := make(chan callbackPayload, 1)
	server := &http.Server{Handler: localCallbackHandler(codeCh)}
	go func() {
		if errServe := server.Serve(listener); errServe != nil && errServe != http.ErrServerClosed {
			select {
			case codeCh <- callbackPayload{Error: errServe.Error()}:
			default:
			}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	state, errState := randomState()
	if errState != nil {
		return nil, nil, errState
	}
	verifier, challenge, errPKCE := pkcePair()
	if errPKCE != nil {
		return nil, nil, errPKCE
	}
	conf := oauthConfig(redirectURI)
	authURL := conf.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	prompt := []byte("Open this URL to authenticate Gemini CLI:\n\n" + authURL + "\n\n")
	_, _ = os.Stdout.Write(prompt)
	if !noBrowser {
		_ = openBrowser(authURL)
	}
	var stdout []byte

	timer := time.NewTimer(loginTimeout)
	defer timer.Stop()
	manualPromptTimer := time.NewTimer(manualPromptDelay)
	defer manualPromptTimer.Stop()
	manualPromptC := manualPromptTimer.C
	var manualInputCh <-chan string
	var manualInputErrCh <-chan error
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, stdout, ctx.Err()
		case <-timer.C:
			return nil, stdout, fmt.Errorf("OAuth login timed out")
		case payload := <-codeCh:
			return p.finishLocalLogin(ctx, proxyURL, redirectURI, verifier, projectID, state, payload, stdout)
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case payload := <-codeCh:
				return p.finishLocalLogin(ctx, proxyURL, redirectURI, verifier, projectID, state, payload, stdout)
			default:
			}
			manualInputCh, manualInputErrCh = asyncPrompt("Paste the Gemini callback URL (or press Enter to keep waiting): ")
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			payload, okPayload, errParse := parseManualCallbackPayload(input)
			if errParse != nil {
				return nil, stdout, errParse
			}
			if !okPayload {
				continue
			}
			return p.finishLocalLogin(ctx, proxyURL, redirectURI, verifier, projectID, state, payload, stdout)
		case errManual := <-manualInputErrCh:
			return nil, stdout, errManual
		case <-ticker.C:
		}
	}
}

func (p *Provider) finishLocalLogin(ctx context.Context, proxyURL string, redirectURI string, verifier string, projectID string, state string, payload callbackPayload, stdout []byte) ([]pluginapi.AuthData, []byte, error) {
	if payload.Error != "" {
		return nil, stdout, fmt.Errorf("OAuth callback failed: %s", payload.Error)
	}
	if payload.State != state {
		return nil, stdout, fmt.Errorf("OAuth state mismatch")
	}
	client := fallbackHTTPClient(proxyURL)
	hostClient := pluginapi.HostHTTPClient(fallbackClient{client: client})
	metadata := map[string]any{"redirect_uri": redirectURI, "code_verifier": verifier}
	if projectID != "" {
		metadata["project_id"] = projectID
	}
	storage, errFinalize := p.finalizeCode(ctx, hostClient, payload.Code, metadata)
	if errFinalize != nil {
		return nil, stdout, errFinalize
	}
	auths, errBuild := buildPersistentAuths("", *storage)
	if errBuild != nil {
		return nil, stdout, errBuild
	}
	stdout = append(stdout, []byte("Authentication successful.\n")...)
	return auths, stdout, nil
}

func (p *Provider) finalizeCode(ctx context.Context, client pluginapi.HostHTTPClient, code string, metadata map[string]any) (*Storage, error) {
	token, errExchange := p.exchangeCode(ctx, client, code, metadata)
	if errExchange != nil {
		return nil, errExchange
	}
	storage := storageFromOAuthToken(token)
	email, errEmail := fetchUserEmail(ctx, client, token.AccessToken)
	if errEmail == nil {
		storage.Email = email
	}
	projectID := metadataString(metadata, "project_id")
	projects, errProjects := fetchProjectIDs(ctx, client, token.AccessToken)
	if errProjects == nil {
		storage.ProjectIDs = projects
	}
	if projectID == "" && len(storage.ProjectIDs) > 0 {
		projectID = storage.ProjectIDs[0]
	}
	if errSetup := setupCodeAssist(ctx, client, &storage, projectID); errSetup != nil {
		if errProjects != nil && projectID == "" {
			return nil, fmt.Errorf("fetch gemini-cli project ids: %w; code assist setup: %w", errProjects, errSetup)
		}
		return nil, errSetup
	}
	if storage.ProjectID == "" && len(storage.ProjectIDs) > 0 {
		storage.ProjectID = storage.ProjectIDs[0]
	}
	if storage.ProjectID == "" {
		return nil, fmt.Errorf("gemini-cli project_id is required")
	}
	storage.ProjectIDs = ensureProjectID(storage.ProjectIDs, storage.ProjectID)
	// Best-effort: enable EXPERIMENTAL release channel so preview models work immediately.
	// Login still succeeds when this Google Cloud API call fails.
	_ = configurePreviewChannels(ctx, client, storage.AccessToken, storage.ProjectIDs)
	return &storage, nil
}

func (p *Provider) exchangeCode(ctx context.Context, client pluginapi.HostHTTPClient, code string, metadata map[string]any) (*oauth2.Token, error) {
	values := url.Values{}
	values.Set("client_id", oauthClientID)
	values.Set("client_secret", oauthClientSecret)
	values.Set("code", strings.TrimSpace(code))
	values.Set("grant_type", "authorization_code")
	values.Set("redirect_uri", metadataString(metadata, "redirect_uri"))
	if verifier := metadataString(metadata, "code_verifier"); verifier != "" {
		values.Set("code_verifier", verifier)
	}
	return p.tokenRequest(ctx, client, values)
}

func (p *Provider) refreshToken(ctx context.Context, client pluginapi.HostHTTPClient, storage Storage) (*oauth2.Token, error) {
	refreshToken := strings.TrimSpace(storage.RefreshToken)
	if refreshToken == "" {
		refreshToken = stringFromMap(storage.Token, "refresh_token")
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("gemini-cli refresh token is missing")
	}
	values := url.Values{}
	values.Set("client_id", oauthClientID)
	values.Set("client_secret", oauthClientSecret)
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")
	return p.tokenRequest(ctx, client, values)
}

func (p *Provider) tokenRequest(ctx context.Context, client pluginapi.HostHTTPClient, values url.Values) (*oauth2.Token, error) {
	if client == nil {
		client = fallbackClient{client: http.DefaultClient}
	}
	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    tokenURL,
		Headers: http.Header{
			"Content-Type": []string{"application/x-www-form-urlencoded"},
			"Accept":       []string{"application/json"},
		},
		Body: []byte(values.Encode()),
	})
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gemini-cli token request failed: status %d: %s", resp.StatusCode, string(resp.Body))
	}
	var token oauth2.Token
	if errDecode := json.Unmarshal(resp.Body, &token); errDecode != nil {
		return nil, fmt.Errorf("decode gemini-cli token response: %w", errDecode)
	}
	if token.Expiry.IsZero() && token.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC()
	}
	return &token, nil
}

func storageFromOAuthToken(token *oauth2.Token) Storage {
	storage := Storage{Type: ProviderKey}
	if token == nil {
		return storage
	}
	storage.AccessToken = token.AccessToken
	storage.RefreshToken = token.RefreshToken
	storage.TokenType = token.TokenType
	if !token.Expiry.IsZero() {
		storage.Expiry = token.Expiry.UTC().Format(time.RFC3339)
	}
	if raw, errMarshal := json.Marshal(token); errMarshal == nil {
		var tokenMap map[string]any
		if errUnmarshal := json.Unmarshal(raw, &tokenMap); errUnmarshal == nil {
			storage.Token = tokenMap
		}
	}
	return storage
}

func applyToken(storage *Storage, token *oauth2.Token) {
	if storage == nil || token == nil {
		return
	}
	updated := storageFromOAuthToken(token)
	storage.AccessToken = updated.AccessToken
	storage.RefreshToken = firstNonEmpty(updated.RefreshToken, storage.RefreshToken)
	storage.TokenType = updated.TokenType
	storage.Expiry = updated.Expiry
	storage.Token = updated.Token
}

func fetchUserEmail(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string) (string, error) {
	resp, errDo := authedGet(ctx, client, userInfoURL, accessToken)
	if errDo != nil {
		return "", errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo request failed: status %d", resp.StatusCode)
	}
	return strings.TrimSpace(gjson.GetBytes(resp.Body, "email").String()), nil
}

func fetchProjectIDs(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string) ([]string, error) {
	resp, errDo := authedGet(ctx, client, projectsURL, accessToken)
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("project list request failed: status %d", resp.StatusCode)
	}
	projects := make([]string, 0)
	gjson.GetBytes(resp.Body, "projects").ForEach(func(_, item gjson.Result) bool {
		projectID := strings.TrimSpace(item.Get("projectId").String())
		if projectID != "" {
			projects = append(projects, projectID)
		}
		return true
	})
	return cleanStringList(projects), nil
}

func setupCodeAssist(ctx context.Context, client pluginapi.HostHTTPClient, storage *Storage, requestedProject string) error {
	if storage == nil {
		return fmt.Errorf("gemini-cli auth storage is missing")
	}
	client = requireHTTPClient(client)
	accessToken := strings.TrimSpace(storage.AccessToken)
	if accessToken == "" {
		return fmt.Errorf("gemini-cli access token is missing")
	}
	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}
	projectID := strings.TrimSpace(requestedProject)
	loadReq := map[string]any{"metadata": metadata}
	if projectID != "" {
		loadReq["cloudaicompanionProject"] = projectID
	}
	var loadResp map[string]any
	if errLoad := callCodeAssist(ctx, client, accessToken, "loadCodeAssist", loadReq, &loadResp); errLoad != nil {
		return fmt.Errorf("load code assist: %w", errLoad)
	}
	tierID := defaultTierID(loadResp)
	if projectID == "" {
		projectID = projectIDFromCodeAssistValue(loadResp["cloudaicompanionProject"])
	}
	if projectID == "" {
		discoveredProject, errDiscover := discoverCodeAssistProject(ctx, client, accessToken, tierID, metadata)
		if errDiscover != nil {
			return errDiscover
		}
		projectID = discoveredProject
	}
	if projectID == "" {
		return fmt.Errorf("gemini-cli project_id is required")
	}
	storage.ProjectID = projectID
	onboardReq := map[string]any{
		"tierId":                  tierID,
		"metadata":                metadata,
		"cloudaicompanionProject": projectID,
	}
	for {
		var onboardResp map[string]any
		if errOnboard := callCodeAssist(ctx, client, accessToken, "onboardUser", onboardReq, &onboardResp); errOnboard != nil {
			return fmt.Errorf("onboard user: %w", errOnboard)
		}
		if done, okDone := onboardResp["done"].(bool); okDone && done {
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				if responseProjectID := projectIDFromCodeAssistValue(resp["cloudaicompanionProject"]); responseProjectID != "" {
					storage.ProjectID = responseProjectID
				}
			}
			if strings.TrimSpace(storage.ProjectID) == "" {
				storage.ProjectID = projectID
			}
			if strings.TrimSpace(storage.ProjectID) == "" {
				return fmt.Errorf("onboard user completed without project id")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(onboardingPoll):
		}
	}
}

func discoverCodeAssistProject(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string, tierID string, metadata map[string]string) (string, error) {
	req := map[string]any{
		"tierId":   tierID,
		"metadata": metadata,
	}
	discoverCtx, cancel := context.WithTimeout(ctx, onboardingTimeout)
	defer cancel()
	for {
		var onboardResp map[string]any
		if errOnboard := callCodeAssist(discoverCtx, client, accessToken, "onboardUser", req, &onboardResp); errOnboard != nil {
			return "", fmt.Errorf("auto-discovery onboardUser: %w", errOnboard)
		}
		if done, okDone := onboardResp["done"].(bool); okDone && done {
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				if projectID := projectIDFromCodeAssistValue(resp["cloudaicompanionProject"]); projectID != "" {
					return projectID, nil
				}
			}
			return "", fmt.Errorf("gemini-cli project_id is required")
		}
		select {
		case <-discoverCtx.Done():
			return "", fmt.Errorf("gemini-cli project_id is required")
		case <-time.After(onboardingPoll):
		}
	}
}

func callCodeAssist(ctx context.Context, client pluginapi.HostHTTPClient, accessToken string, endpoint string, body any, result any) error {
	client = requireHTTPClient(client)
	url := fmt.Sprintf("%s/%s:%s", codeAssistBaseURL, codeAssistVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", codeAssistBaseURL, endpoint)
	}
	var rawBody []byte
	if body != nil {
		var errMarshal error
		rawBody, errMarshal = json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
	}
	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    url,
		Headers: http.Header{
			"Accept":            []string{"application/json"},
			"Authorization":     []string{"Bearer " + strings.TrimSpace(accessToken)},
			"Content-Type":      []string{"application/json"},
			"User-Agent":        []string{fingerprint.UserAgent("")},
			"X-Goog-Api-Client": []string{fingerprint.APIClientHeader},
		},
		Body: rawBody,
	})
	if errDo != nil {
		return errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	if result == nil {
		return nil
	}
	if errUnmarshal := json.Unmarshal(resp.Body, result); errUnmarshal != nil {
		return fmt.Errorf("decode response body: %w", errUnmarshal)
	}
	return nil
}

func defaultTierID(loadResp map[string]any) string {
	tierID := "legacy-tier"
	tiers, okTiers := loadResp["allowedTiers"].([]any)
	if !okTiers {
		return tierID
	}
	for _, rawTier := range tiers {
		tier, okTier := rawTier.(map[string]any)
		if !okTier {
			continue
		}
		isDefault, okDefault := tier["isDefault"].(bool)
		id, okID := tier["id"].(string)
		if okDefault && isDefault && okID && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return tierID
}

func projectIDFromCodeAssistValue(value any) string {
	switch project := value.(type) {
	case string:
		return strings.TrimSpace(project)
	case map[string]any:
		if id, okID := project["id"].(string); okID {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func ensureProjectID(projects []string, projectID string) []string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || containsString(projects, projectID) {
		return cleanStringList(projects)
	}
	return append([]string{projectID}, cleanStringList(projects)...)
}

func requireHTTPClient(client pluginapi.HostHTTPClient) pluginapi.HostHTTPClient {
	if client != nil {
		return client
	}
	return fallbackClient{client: http.DefaultClient}
}

func authedGet(ctx context.Context, client pluginapi.HostHTTPClient, endpoint string, accessToken string) (pluginapi.HTTPResponse, error) {
	client = requireHTTPClient(client)
	return client.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    endpoint,
		Headers: http.Header{
			"Accept":            []string{"application/json"},
			"Authorization":     []string{"Bearer " + strings.TrimSpace(accessToken)},
			"User-Agent":        []string{fingerprint.UserAgent("")},
			"X-Goog-Api-Client": []string{fingerprint.APIClientHeader},
		},
	})
}

func readCallbackPayload(authDir, state string) (callbackPayload, bool, error) {
	path := callbackPayloadPath(authDir, state)
	if path == "" {
		return callbackPayload{}, false, nil
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return callbackPayload{}, false, nil
		}
		return callbackPayload{}, false, errRead
	}
	var payload callbackPayload
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return callbackPayload{}, false, errUnmarshal
	}
	return payload, true, nil
}

func consumeCallbackPayload(authDir, state string) error {
	path := callbackPayloadPath(authDir, state)
	if path == "" {
		return nil
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return errRemove
	}
	return nil
}

func callbackPayloadPath(authDir, state string) string {
	if strings.TrimSpace(authDir) == "" || strings.TrimSpace(state) == "" {
		return ""
	}
	return filepath.Join(authDir, ".oauth-"+ProviderKey+"-"+state+".oauth")
}

func oauthConfig(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     oauthClientID,
		ClientSecret: oauthClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       append([]string(nil), oauthScopes...),
		Endpoint:     google.Endpoint,
	}
}

func pkcePair() (string, string, error) {
	verifier, errVerifier := randomURLToken(48)
	if errVerifier != nil {
		return "", "", errVerifier
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge, nil
}

func randomState() (string, error) {
	return randomURLToken(24)
}

func randomURLToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, errRead := rand.Read(buf); errRead != nil {
		return "", errRead
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func asyncPrompt(message string) (<-chan string, <-chan error) {
	inputCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		if _, errWrite := os.Stdout.Write([]byte(message)); errWrite != nil {
			errCh <- errWrite
			return
		}
		input, errRead := bufio.NewReader(os.Stdin).ReadString('\n')
		if errRead != nil && input == "" {
			errCh <- errRead
			return
		}
		inputCh <- input
	}()
	return inputCh, errCh
}

func parseManualCallbackPayload(input string) (callbackPayload, bool, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return callbackPayload{}, false, nil
	}

	candidate := trimmed
	if !strings.Contains(candidate, "://") {
		switch {
		case strings.HasPrefix(candidate, "?"):
			candidate = "http://localhost" + candidate
		case strings.ContainsAny(candidate, "/?#") || strings.Contains(candidate, ":"):
			candidate = "http://" + candidate
		case strings.Contains(candidate, "="):
			candidate = "http://localhost/?" + candidate
		default:
			return callbackPayload{}, false, fmt.Errorf("invalid callback URL")
		}
	}

	parsedURL, errParse := url.Parse(candidate)
	if errParse != nil {
		return callbackPayload{}, false, errParse
	}

	query := parsedURL.Query()
	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	errCode := strings.TrimSpace(query.Get("error"))
	errDesc := strings.TrimSpace(query.Get("error_description"))
	if parsedURL.Fragment != "" {
		if fragQuery, errFrag := url.ParseQuery(parsedURL.Fragment); errFrag == nil {
			if code == "" {
				code = strings.TrimSpace(fragQuery.Get("code"))
			}
			if state == "" {
				state = strings.TrimSpace(fragQuery.Get("state"))
			}
			if errCode == "" {
				errCode = strings.TrimSpace(fragQuery.Get("error"))
			}
			if errDesc == "" {
				errDesc = strings.TrimSpace(fragQuery.Get("error_description"))
			}
		}
	}
	if code != "" && state == "" && strings.Contains(code, "#") {
		parts := strings.SplitN(code, "#", 2)
		code = parts[0]
		state = parts[1]
	}
	if errCode == "" && errDesc != "" {
		errCode = errDesc
	}
	if code == "" && errCode == "" {
		return callbackPayload{}, false, fmt.Errorf("callback URL missing code")
	}
	return callbackPayload{Code: code, State: state, Error: errCode}, true, nil
}

func metadataString(metadata map[string]any, key string) string {
	return strings.TrimSpace(stringFromMap(metadata, key))
}

func flagBool(flags map[string]pluginapi.CommandLineFlagValue, name string) bool {
	value, ok := flags[name]
	if !ok {
		return false
	}
	return value.Set && strings.EqualFold(strings.TrimSpace(value.Value), "true")
}

func flagBoolValue(flags map[string]pluginapi.CommandLineFlagValue, name string) bool {
	value, ok := flags[name]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value.Value), "true")
}

func flagString(flags map[string]pluginapi.CommandLineFlagValue, name string) string {
	value, ok := flags[name]
	if !ok {
		return ""
	}
	return strings.TrimSpace(value.Value)
}

func localCallbackHandler(codeCh chan<- callbackPayload) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		payload := callbackPayload{
			Code:  strings.TrimSpace(r.URL.Query().Get("code")),
			State: strings.TrimSpace(r.URL.Query().Get("state")),
			Error: firstNonEmpty(r.URL.Query().Get("error"), r.URL.Query().Get("error_description")),
		}
		if payload.Error == "" && payload.Code == "" {
			payload.Error = "code not found"
		}
		select {
		case codeCh <- payload:
		default:
		}
		_, _ = w.Write([]byte("<html><body><h1>Authentication successful</h1><p>You can close this window.</p></body></html>"))
	})
	return mux
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func fallbackHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 60 * time.Second}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild == nil && transport != nil {
		client.Transport = transport
	}
	return client
}

type fallbackClient struct {
	client *http.Client
}

func (c fallbackClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	httpReq, errRequest := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if errRequest != nil {
		return pluginapi.HTTPResponse{}, errRequest
	}
	httpReq.Header = req.Headers.Clone()
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return pluginapi.HTTPResponse{}, errDo
	}
	body, errRead := ioReadAllAndClose(resp)
	if errRead != nil {
		return pluginapi.HTTPResponse{}, errRead
	}
	return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header.Clone(), Body: body}, nil
}

func (c fallbackClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("gemini-cli fallback stream is unavailable")
}

func ioReadAllAndClose(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	body := new(bytes.Buffer)
	_, errRead := body.ReadFrom(resp.Body)
	errClose := resp.Body.Close()
	if errRead != nil {
		return nil, errRead
	}
	if errClose != nil {
		return nil, errClose
	}
	return body.Bytes(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
