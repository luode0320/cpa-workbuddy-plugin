package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Provider usage endpoints and client identities, mirrored from the CPA Management Center
// (src/utils/quota/constants.ts). Requests go through the management api-call endpoint, which
// replaces $TOKEN$ with the credential's access token, so the plugin never sees a token.
const (
	claudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	codexUsageURL    = "https://chatgpt.com/backend-api/wham/usage"
	// antigravityQuotaURL is the first of the frontend's three retrieveUserQuotaSummary hosts;
	// the others are not tried, a failure here is recorded.
	antigravityQuotaURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	kimiComUsageURL     = "https://api.kimi.com/coding/v1/usages"
	kimiAIUsageURL      = "https://api.kimi.ai/coding/v1/usages"
	xaiBillingWeeklyURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	xaiBillingMonthURL  = "https://cli-chat-proxy.grok.com/v1/billing"

	claudeUserAgent      = "claude-cli/2.1.280 (external, cli)"
	codexUserAgent       = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
	antigravityUserAgent = "antigravity/hub/2.23.0 darwin/arm64"
	xaiClientVersion     = "1.0.46"
	xaiUserAgent         = "grok-pager/" + xaiClientVersion + " grok-shell/" + xaiClientVersion + " (macos; aarch64)"

	quotaFetchConcurrency = 4
	// quotaHTTPTimeout covers one management call; api-call itself gives the upstream 60s.
	quotaHTTPTimeout = 70 * time.Second
	// quotaMonth is the nominal length of a "monthly" window; providers that state the period
	// boundaries (xAI) use the real span instead.
	quotaMonth = 30 * 24 * time.Hour
)

// managementBlock stops all management calls after CPA rejected the key: every failed attempt
// counts toward CPA's IP ban, so retrying a bad key would lock the plugin out.
type managementBlock struct {
	key    string
	until  time.Time // zero: until the key changes
	reason string
}

// managementBanWindow is how long CPA bans an IP after repeated failed management logins.
const managementBanWindow = 30 * time.Minute

var (
	managementBlocked atomic.Pointer[managementBlock]
	managementBaseURL = "http://127.0.0.1:8317"
	quotaRefreshAge   = 5 * time.Minute

	quotaMu         sync.Mutex
	quotaSnapshots  = map[string]credentialQuota{}
	quotaRefreshing atomic.Bool
	quotaHTTPClient = &http.Client{Timeout: quotaHTTPTimeout}
)

// ensureQuotaFresh starts a background refresh of every given credential whose snapshot is older than
// quotaRefreshAge (or missing). It never blocks and never starts a second refresh while one runs.
func ensureQuotaFresh(key string, creds []hostAuth) {
	now := time.Now()
	var stale []hostAuth
	quotaMu.Lock()
	for _, cred := range creds {
		snapshot, ok := quotaSnapshots[cred.ID]
		if !ok || now.Sub(snapshot.FetchedAt) >= quotaRefreshAge {
			stale = append(stale, cred)
		}
	}
	quotaMu.Unlock()
	if len(stale) == 0 || !quotaRefreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer quotaRefreshing.Store(false)
		refreshQuotaNow(key, stale)
	}()
}

// managementStatus reports why management calls are blocked for this key; empty when usable.
func managementStatus(key string, now time.Time) string {
	if key == "" {
		return "management_key is not set; quota collection is off"
	}
	block := managementBlocked.Load()
	if block == nil || block.key != key || (!block.until.IsZero() && now.After(block.until)) {
		return ""
	}
	if block.until.IsZero() {
		return "management key rejected, set a new management_key: " + block.reason
	}
	return "management API banned this IP until " + block.until.Format(time.RFC3339) + ": " + block.reason
}

// refreshQuotaNow fetches the given credentials synchronously (in parallel, bounded) and stores the results.
// One authenticated call checks the key first so a bad key costs a single failed attempt.
func refreshQuotaNow(key string, creds []hostAuth) {
	if len(creds) == 0 {
		return
	}
	var providers json.RawMessage
	errCheck := cpaManagementJSON(key, http.MethodGet, "/v0/management/quota/providers", nil, &providers)
	if errCheck != nil {
		now := time.Now()
		quotaMu.Lock()
		for _, cred := range creds {
			quotaSnapshots[cred.ID] = credentialQuota{AuthID: cred.ID, AuthIndex: cred.AuthIndex, Provider: cred.Provider, FetchedAt: now, Error: errCheck.Error()}
		}
		quotaMu.Unlock()
		return
	}
	slots := make(chan struct{}, quotaFetchConcurrency)
	var wg sync.WaitGroup
	for _, cred := range creds {
		wg.Add(1)
		slots <- struct{}{}
		go func(cred hostAuth) {
			defer wg.Done()
			defer func() { <-slots }()
			snapshot := fetchCredentialQuota(key, cred)
			quotaMu.Lock()
			quotaSnapshots[cred.ID] = snapshot
			quotaMu.Unlock()
		}(cred)
	}
	wg.Wait()
}

// quotaFor returns the latest snapshot for an auth ID.
func quotaFor(authID string) (credentialQuota, bool) {
	quotaMu.Lock()
	defer quotaMu.Unlock()
	snapshot, ok := quotaSnapshots[authID]
	return snapshot, ok
}

// fetchCredentialQuota collects one credential's windows; a failure is returned as a snapshot
// with Error set and no windows.
func fetchCredentialQuota(key string, cred hostAuth) credentialQuota {
	snapshot := credentialQuota{
		AuthID:    cred.ID,
		AuthIndex: cred.AuthIndex,
		Provider:  cred.Provider,
		FetchedAt: time.Now(),
	}
	var windows []quotaWindow
	var plan string
	var errFetch error
	switch cred.Provider {
	case "claude":
		snapshot.Source = "claude-oauth-usage"
		windows, plan, errFetch = fetchClaudeQuota(key, cred)
	case "codex":
		snapshot.Source = "codex-wham-usage"
		windows, plan, errFetch = fetchCodexQuota(key, cred)
	case "antigravity":
		snapshot.Source = "antigravity-quota-summary"
		windows, errFetch = fetchAntigravityQuota(key, cred)
	case "kimi", "kimi.com", "kimi-ai", "kimi.ai":
		snapshot.Source = "kimi-usages"
		windows, errFetch = fetchKimiQuota(key, cred)
	case "xai":
		snapshot.Source = "xai-billing"
		windows, errFetch = fetchXaiQuota(key, cred)
	default:
		snapshot.Source = "cpa-quota-fetch"
		windows, plan, errFetch = fetchPluginQuota(key, cred)
	}
	if errFetch != nil {
		snapshot.Error = errFetch.Error()
		return snapshot
	}
	snapshot.Windows = windows
	snapshot.Plan = plan
	return snapshot
}

// --- management transport ---

// cpaManagementCall calls the CPA management API and returns the HTTP status and body.
func cpaManagementCall(key, method, path string, payload any) (int, []byte, error) {
	if status := managementStatus(key, time.Now()); status != "" {
		return 0, nil, fmt.Errorf("management %s: %s", path, status)
	}
	var body io.Reader
	if payload != nil {
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return 0, nil, fmt.Errorf("management %s: encode request: %w", path, errMarshal)
		}
		body = bytes.NewReader(raw)
	}
	req, errReq := http.NewRequest(method, strings.TrimRight(managementBaseURL, "/")+path, body)
	if errReq != nil {
		return 0, nil, fmt.Errorf("management %s: %w", path, errReq)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, errDo := quotaHTTPClient.Do(req)
	if errDo != nil {
		return 0, nil, fmt.Errorf("management %s: %w", path, errDo)
	}
	defer resp.Body.Close()
	raw, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return 0, nil, fmt.Errorf("management %s: read response: %w", path, errRead)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		managementBlocked.Store(&managementBlock{key: key, reason: snippet(raw)})
	case http.StatusForbidden:
		managementBlocked.Store(&managementBlock{key: key, until: time.Now().Add(managementBanWindow), reason: snippet(raw)})
	}
	return resp.StatusCode, raw, nil
}

// cpaManagementJSON calls the management API, requires HTTP 200 and decodes the body into out.
func cpaManagementJSON(key, method, path string, payload, out any) error {
	status, raw, errCall := cpaManagementCall(key, method, path, payload)
	if errCall != nil {
		return errCall
	}
	if status != http.StatusOK {
		return fmt.Errorf("management %s: HTTP %d: %s", path, status, snippet(raw))
	}
	if errDecode := json.Unmarshal(raw, out); errDecode != nil {
		return fmt.Errorf("management %s: decode response: %w", path, errDecode)
	}
	return nil
}

type apiCallRequest struct {
	AuthIndex string            `json:"auth_index"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header"`
	Data      string            `json:"data,omitempty"`
}

type apiCallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

// providerCall sends one provider request through POST /v0/management/api-call with the
// credential's token injected by CPA, and requires a 2xx upstream status.
func providerCall(key string, cred hostAuth, method, target string, header map[string]string, data string) ([]byte, error) {
	var resp apiCallResponse
	request := apiCallRequest{AuthIndex: cred.AuthIndex, Method: method, URL: target, Header: header, Data: data}
	if errCall := cpaManagementJSON(key, http.MethodPost, "/v0/management/api-call", request, &resp); errCall != nil {
		return nil, errCall
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, target, resp.StatusCode, snippet([]byte(resp.Body)))
	}
	return []byte(resp.Body), nil
}

// snippet bounds an error body for display.
func snippet(raw []byte) string {
	const limit = 300
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

// credentialModels lists the model IDs CPA serves through the credential, for scoping
// model-specific windows.
func credentialModels(key string, cred hostAuth) ([]string, error) {
	var resp struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	path := "/v0/management/auth-files/models?name=" + url.QueryEscape(cred.ID)
	if errCall := cpaManagementJSON(key, http.MethodGet, path, nil, &resp); errCall != nil {
		return nil, errCall
	}
	models := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		models = append(models, model.ID)
	}
	return models, nil
}

// --- per-credential model lists without host.auth.models ---

// Older hosts predate the host.auth.models callback but still serve the same data over the
// management API, so the router keeps a model map even on those builds. The read is asynchronous
// and cached: the scheduling path must never block on it.
const modelsFallbackTTL = 10 * time.Minute

var (
	modelsFallbackMu sync.Mutex
	modelsFallback   = map[string][]string{}
	modelsFallbackAt time.Time
	modelsRefreshing atomic.Bool

	// credentialModelsCall is the management API read; unit tests replace it because they run
	// without a host to answer on 127.0.0.1:8317.
	credentialModelsCall = credentialModels
)

// ensureFallbackModels starts a background management read of the per-credential model lists. It
// never blocks and never starts a second refresh while one runs.
func ensureFallbackModels(key string, creds []hostAuth) {
	if key == "" || len(creds) == 0 {
		return
	}
	modelsFallbackMu.Lock()
	fresh := !modelsFallbackAt.IsZero() && time.Since(modelsFallbackAt) < modelsFallbackTTL
	modelsFallbackMu.Unlock()
	if fresh || !modelsRefreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer modelsRefreshing.Store(false)
		slots := make(chan struct{}, quotaFetchConcurrency)
		var wg sync.WaitGroup
		var resultMu sync.Mutex
		fetched := make(map[string][]string, len(creds))
		for _, cred := range creds {
			wg.Add(1)
			slots <- struct{}{}
			go func(cred hostAuth) {
				defer wg.Done()
				defer func() { <-slots }()
				models, errModels := credentialModelsCall(key, cred)
				// A provider whose models CPA defines in code answers with an empty list; leaving it
				// out keeps the map meaning "known model list" instead of "serves nothing".
				if errModels != nil || len(models) == 0 {
					return
				}
				resultMu.Lock()
				fetched[cred.ID] = models
				resultMu.Unlock()
			}(cred)
		}
		wg.Wait()
		modelsFallbackMu.Lock()
		// An entirely empty round usually means the management key or IP is blocked; keeping the
		// previous map avoids blanking model detail, and the timestamp still rate-limits retries.
		if len(fetched) > 0 {
			modelsFallback = fetched
		}
		modelsFallbackAt = time.Now()
		modelsFallbackMu.Unlock()
	}()
}

// fallbackModels copies the cached management-API model lists.
func fallbackModels() map[string][]string {
	modelsFallbackMu.Lock()
	defer modelsFallbackMu.Unlock()
	out := make(map[string][]string, len(modelsFallback))
	for id, models := range modelsFallback {
		out[id] = models
	}
	return out
}

// authFileEntry is the subset of a GET /v0/management/auth-files entry the collectors need.
type authFileEntry struct {
	ProjectID string `json:"project_id"`
	// IDToken holds decoded Codex id_token claims; CPA omits it for other providers.
	IDToken *struct {
		ChatgptAccountID string `json:"chatgpt_account_id"`
	} `json:"id_token"`
}

func credentialAuthFile(key string, cred hostAuth) (authFileEntry, error) {
	var resp struct {
		Files []authFileEntry `json:"files"`
	}
	path := "/v0/management/auth-files?auth_index=" + url.QueryEscape(cred.AuthIndex)
	if errCall := cpaManagementJSON(key, http.MethodGet, path, nil, &resp); errCall != nil {
		return authFileEntry{}, errCall
	}
	if len(resp.Files) != 1 {
		return authFileEntry{}, fmt.Errorf("management auth-files: %d entries for auth_index %s, want 1", len(resp.Files), cred.AuthIndex)
	}
	return resp.Files[0], nil
}

// --- shared parsing helpers ---

// flexFloat decodes a JSON number or a numeric string; Kimi and xAI send both.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if errText := json.Unmarshal(raw, &text); errText != nil {
			return errText
		}
		value, errParse := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if errParse != nil {
			return fmt.Errorf("numeric string %q: %w", text, errParse)
		}
		*f = flexFloat(value)
		return nil
	}
	var value float64
	if errNumber := json.Unmarshal(raw, &value); errNumber != nil {
		return errNumber
	}
	*f = flexFloat(value)
	return nil
}

// remainingFromUsedPercent turns a 0..100 used percentage into a 0..1 remaining fraction.
func remainingFromUsedPercent(used float64) float64 {
	return clampFraction(1 - used/100)
}

func clampFraction(value float64) float64 {
	return min(max(value, 0), 1)
}

// parseInstant parses an RFC 3339 timestamp, including the 6-9 digit fractions providers send.
func parseInstant(provider, field, value string) (time.Time, error) {
	instant, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if errParse != nil {
		return time.Time{}, fmt.Errorf("%s: %s: %w", provider, field, errParse)
	}
	return instant, nil
}

// bucketPeriod maps a quota bucket's window label (Antigravity, plugin quota providers) to its length.
func bucketPeriod(provider, window string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "5h", "five-hour", "five_hour":
		return 5 * time.Hour, nil
	case "daily", "day":
		return 24 * time.Hour, nil
	case "weekly", "week":
		return 7 * 24 * time.Hour, nil
	case "monthly", "month":
		return quotaMonth, nil
	}
	return 0, fmt.Errorf("%s: unknown bucket window %q", provider, window)
}

// modelsWithPrefix returns the models whose lowercased ID starts with any prefix.
func modelsWithPrefix(models []string, prefixes ...string) []string {
	var matched []string
	for _, model := range models {
		lower := strings.ToLower(model)
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				matched = append(matched, model)
				break
			}
		}
	}
	return matched
}

// modelsContaining returns the models whose lowercased ID contains family.
func modelsContaining(models []string, family string) []string {
	var matched []string
	for _, model := range models {
		if strings.Contains(strings.ToLower(model), family) {
			matched = append(matched, model)
		}
	}
	return matched
}

// --- claude ---

func fetchClaudeQuota(key string, cred hostAuth) ([]quotaWindow, string, error) {
	header := map[string]string{
		"User-Agent":     claudeUserAgent,
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
		"Accept":         "application/json",
	}
	usage, errUsage := providerCall(key, cred, http.MethodGet, claudeUsageURL, header, "")
	if errUsage != nil {
		return nil, "", fmt.Errorf("claude usage: %w", errUsage)
	}
	profile, errProfile := providerCall(key, cred, http.MethodGet, claudeProfileURL, header, "")
	if errProfile != nil {
		return nil, "", fmt.Errorf("claude profile: %w", errProfile)
	}
	models, errModels := credentialModels(key, cred)
	if errModels != nil {
		return nil, "", fmt.Errorf("claude: %w", errModels)
	}
	windows, errParse := parseClaudeUsage(usage, models)
	if errParse != nil {
		return nil, "", errParse
	}
	plan, errPlan := parseClaudeProfile(profile)
	if errPlan != nil {
		return nil, "", errPlan
	}
	return windows, plan, nil
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
	// Dollar fields mark a credit balance (iguana_necktie on some accounts), not a rate window.
	LimitDollars     *float64 `json:"limit_dollars"`
	UsedDollars      *float64 `json:"used_dollars"`
	RemainingDollars *float64 `json:"remaining_dollars"`
}

func (w claudeUsageWindow) dollarDenominated() bool {
	return w.LimitDollars != nil || w.UsedDollars != nil || w.RemainingDollars != nil
}

type claudeUsageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
	IsActive bool     `json:"is_active"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// claudeWindowKeys are the named windows of /api/oauth/usage, in display order. Family scopes a
// window to the credential's models whose ID contains it (claude-opus-*, claude-sonnet-*, ...).
var claudeWindowKeys = []struct {
	key, name, family string
	period            time.Duration
}{
	{"five_hour", "5h", "", 5 * time.Hour},
	{"seven_day", "7d", "", 7 * 24 * time.Hour},
	{"seven_day_oauth_apps", "7d OAuth apps", "", 7 * 24 * time.Hour},
	{"seven_day_opus", "7d Opus", "opus", 7 * 24 * time.Hour},
	{"seven_day_sonnet", "7d Sonnet", "sonnet", 7 * 24 * time.Hour},
	{"seven_day_cowork", "7d Cowork", "", 7 * 24 * time.Hour},
	// iguana_necktie is the legacy Fable window unless it carries dollar fields.
	{"iguana_necktie", "7d Fable", "fable", 7 * 24 * time.Hour},
}

// parseClaudeUsage maps /api/oauth/usage to windows. A model-specific window (Opus, Sonnet,
// Fable) is scoped to the matching models and dropped when the credential serves none of them,
// since it then constrains nothing. extra_usage (pay-as-you-go spend after the plan windows)
// and dollar credit balances are not rate windows and are not reported.
func parseClaudeUsage(body []byte, models []string) ([]quotaWindow, error) {
	var payload map[string]json.RawMessage
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, fmt.Errorf("claude: decode usage: %w", errDecode)
	}
	var limits []claudeUsageLimit
	if raw, ok := payload["limits"]; ok {
		if errLimits := json.Unmarshal(raw, &limits); errLimits != nil {
			return nil, fmt.Errorf("claude: decode limits: %w", errLimits)
		}
	}
	fable, errFable := claudeFableLimit(limits)
	if errFable != nil {
		return nil, errFable
	}

	var windows []quotaWindow
	for _, spec := range claudeWindowKeys {
		raw, ok := payload[spec.key]
		if !ok || string(raw) == "null" {
			continue
		}
		var window claudeUsageWindow
		if errWindow := json.Unmarshal(raw, &window); errWindow != nil {
			return nil, fmt.Errorf("claude: decode %s: %w", spec.key, errWindow)
		}
		if spec.key == "iguana_necktie" && (window.dollarDenominated() || fable != nil) {
			continue
		}
		if window.Utilization == nil {
			return nil, fmt.Errorf("claude: %s.utilization missing", spec.key)
		}
		parsed := quotaWindow{Name: spec.name, Remaining: remainingFromUsedPercent(*window.Utilization), Period: spec.period}
		// resets_at is null on a window that has not been used yet.
		if window.ResetsAt != nil {
			resetAt, errReset := parseInstant("claude", spec.key+".resets_at", *window.ResetsAt)
			if errReset != nil {
				return nil, errReset
			}
			parsed.ResetAt = resetAt
		}
		if spec.family != "" {
			parsed.Models = modelsContaining(models, spec.family)
			if len(parsed.Models) == 0 {
				continue
			}
		}
		windows = append(windows, parsed)
	}

	if fable != nil {
		parsed := quotaWindow{Name: "7d Fable", Remaining: remainingFromUsedPercent(*fable.Percent), Period: 7 * 24 * time.Hour}
		if fable.ResetsAt != nil {
			resetAt, errReset := parseInstant("claude", "limits[weekly_scoped Fable].resets_at", *fable.ResetsAt)
			if errReset != nil {
				return nil, errReset
			}
			parsed.ResetAt = resetAt
		}
		parsed.Models = modelsContaining(models, "fable")
		if len(parsed.Models) > 0 {
			windows = append(windows, parsed)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("claude: usage response has no windows")
	}
	return windows, nil
}

// claudeFableLimit picks the weekly_scoped Fable entry of limits, preferring the active one.
func claudeFableLimit(limits []claudeUsageLimit) (*claudeUsageLimit, error) {
	var picked *claudeUsageLimit
	for i := range limits {
		limit := &limits[i]
		if limit.Kind != "weekly_scoped" || limit.Scope == nil || limit.Scope.Model == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(limit.Scope.Model.DisplayName))
		if name != "fable" && name != "fable 5" {
			continue
		}
		if limit.Percent == nil {
			return nil, fmt.Errorf("claude: limits[weekly_scoped Fable].percent missing")
		}
		if picked == nil || (limit.IsActive && !picked.IsActive) {
			picked = limit
		}
	}
	return picked, nil
}

// parseClaudeProfile resolves the plan the way the Management Center does. Plan is optional:
// a profile that identifies none yields "".
func parseClaudeProfile(body []byte) (string, error) {
	var profile struct {
		Account *struct {
			HasClaudeMax *bool `json:"has_claude_max"`
			HasClaudePro *bool `json:"has_claude_pro"`
		} `json:"account"`
		Organization *struct {
			OrganizationType   string `json:"organization_type"`
			SubscriptionStatus string `json:"subscription_status"`
			RateLimitTier      string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if errDecode := json.Unmarshal(body, &profile); errDecode != nil {
		return "", fmt.Errorf("claude: decode profile: %w", errDecode)
	}
	if org := profile.Organization; org != nil {
		if strings.EqualFold(org.OrganizationType, "claude_team") && strings.EqualFold(org.SubscriptionStatus, "active") {
			return "team", nil
		}
		tier := strings.ToLower(org.RateLimitTier)
		if strings.HasSuffix(tier, "claude_max_20x") {
			return "max20x", nil
		}
		if strings.HasSuffix(tier, "claude_max_5x") {
			return "max5x", nil
		}
	}
	if account := profile.Account; account != nil {
		if account.HasClaudeMax != nil && *account.HasClaudeMax {
			return "max", nil
		}
		if account.HasClaudePro != nil && *account.HasClaudePro {
			return "pro", nil
		}
		if account.HasClaudeMax != nil && account.HasClaudePro != nil {
			return "free", nil
		}
	}
	return "", nil
}

// --- codex ---

func fetchCodexQuota(key string, cred hostAuth) ([]quotaWindow, string, error) {
	entry, errEntry := credentialAuthFile(key, cred)
	if errEntry != nil {
		return nil, "", fmt.Errorf("codex: %w", errEntry)
	}
	header := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    codexUserAgent,
	}
	// The account header selects a workspace; CPA exposes it only when the id_token carries the
	// claim, and personal accounts are served without it (as in the Management Center).
	if entry.IDToken != nil && entry.IDToken.ChatgptAccountID != "" {
		header["Chatgpt-Account-Id"] = entry.IDToken.ChatgptAccountID
	}
	body, errUsage := providerCall(key, cred, http.MethodGet, codexUsageURL, header, "")
	if errUsage != nil {
		return nil, "", fmt.Errorf("codex usage: %w", errUsage)
	}
	models, errModels := credentialModels(key, cred)
	if errModels != nil {
		return nil, "", fmt.Errorf("codex: %w", errModels)
	}
	return parseCodexUsage(body, time.Now(), models)
}

type codexUsageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  *int64   `json:"reset_after_seconds"`
	ResetAt            *int64   `json:"reset_at"`
}

type codexRateLimit struct {
	PrimaryWindow   *codexUsageWindow `json:"primary_window"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window"`
}

// parseCodexUsage maps wham/usage to windows named by their length. rate_limit applies to every
// model; an additional_rate_limits entry (e.g. "GPT-5.3-Codex-Spark") applies to the model whose
// ID equals its lowercased limit_name and is dropped when the credential serves no such model.
// code_review_rate_limit governs code review only and is not reported.
func parseCodexUsage(body []byte, now time.Time, models []string) ([]quotaWindow, string, error) {
	var payload struct {
		PlanType             string          `json:"plan_type"`
		RateLimit            *codexRateLimit `json:"rate_limit"`
		AdditionalRateLimits []struct {
			LimitName string          `json:"limit_name"`
			RateLimit *codexRateLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, "", fmt.Errorf("codex: decode usage: %w", errDecode)
	}
	if payload.RateLimit == nil {
		return nil, "", fmt.Errorf("codex: rate_limit missing")
	}
	windows, errMain := codexWindows("rate_limit", "", payload.RateLimit, now, nil)
	if errMain != nil {
		return nil, "", errMain
	}
	for i, extra := range payload.AdditionalRateLimits {
		if extra.RateLimit == nil {
			continue
		}
		var scoped []string
		for _, model := range models {
			if strings.EqualFold(model, extra.LimitName) {
				scoped = append(scoped, model)
			}
		}
		if len(scoped) == 0 {
			continue
		}
		field := fmt.Sprintf("additional_rate_limits[%d]", i)
		extraWindows, errExtra := codexWindows(field, extra.LimitName+" ", extra.RateLimit, now, scoped)
		if errExtra != nil {
			return nil, "", errExtra
		}
		windows = append(windows, extraWindows...)
	}
	if len(windows) == 0 {
		return nil, "", fmt.Errorf("codex: rate_limit has no windows")
	}
	return windows, payload.PlanType, nil
}

func codexWindows(field, namePrefix string, limit *codexRateLimit, now time.Time, models []string) ([]quotaWindow, error) {
	var windows []quotaWindow
	for _, slot := range []struct {
		name   string
		window *codexUsageWindow
	}{{"primary_window", limit.PrimaryWindow}, {"secondary_window", limit.SecondaryWindow}} {
		if slot.window == nil {
			continue
		}
		path := field + "." + slot.name
		window := slot.window
		if window.UsedPercent == nil {
			return nil, fmt.Errorf("codex: %s.used_percent missing", path)
		}
		if window.LimitWindowSeconds == nil || *window.LimitWindowSeconds <= 0 {
			return nil, fmt.Errorf("codex: %s.limit_window_seconds missing", path)
		}
		period := time.Duration(*window.LimitWindowSeconds) * time.Second
		var resetAt time.Time
		switch {
		case window.ResetAt != nil:
			resetAt = time.Unix(*window.ResetAt, 0)
		case window.ResetAfterSeconds != nil:
			resetAt = now.Add(time.Duration(*window.ResetAfterSeconds) * time.Second)
		default:
			return nil, fmt.Errorf("codex: %s has neither reset_at nor reset_after_seconds", path)
		}
		windows = append(windows, quotaWindow{
			Name:      namePrefix + codexWindowName(period),
			Remaining: remainingFromUsedPercent(*window.UsedPercent),
			ResetAt:   resetAt,
			Period:    period,
			Models:    models,
		})
	}
	return windows, nil
}

func codexWindowName(period time.Duration) string {
	switch {
	case period == 5*time.Hour:
		return "5h"
	case period == 7*24*time.Hour:
		return "weekly"
	case period >= 28*24*time.Hour && period <= 31*24*time.Hour:
		return "monthly"
	}
	return period.String()
}

// --- antigravity ---

func fetchAntigravityQuota(key string, cred hostAuth) ([]quotaWindow, error) {
	entry, errEntry := credentialAuthFile(key, cred)
	if errEntry != nil {
		return nil, fmt.Errorf("antigravity: %w", errEntry)
	}
	if entry.ProjectID == "" {
		return nil, fmt.Errorf("antigravity: credential has no project_id")
	}
	data, errMarshal := json.Marshal(map[string]string{"project": entry.ProjectID})
	if errMarshal != nil {
		return nil, fmt.Errorf("antigravity: encode request: %w", errMarshal)
	}
	header := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    antigravityUserAgent,
	}
	body, errQuota := providerCall(key, cred, http.MethodPost, antigravityQuotaURL, header, string(data))
	if errQuota != nil {
		return nil, fmt.Errorf("antigravity quota: %w", errQuota)
	}
	models, errModels := credentialModels(key, cred)
	if errModels != nil {
		return nil, fmt.Errorf("antigravity: %w", errModels)
	}
	return parseAntigravityQuota(body, models)
}

type quotaSummaryBucket struct {
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

// antigravityGroupFamilies maps the quota group names Antigravity reports (the Management
// Center's GROUP_LABEL_KEYS) to the model ID prefixes they govern. The group description lists
// display names ("Gemini 3 Pro, ..."), not IDs, so the scope is matched by ID family.
var antigravityGroupFamilies = map[string][]string{
	"gemini models":         {"gemini"},
	"claude and gpt models": {"claude", "gpt"},
}

// parseAntigravityQuota maps retrieveUserQuotaSummary groups to windows scoped to the group's
// model family. A group whose family the credential does not serve is dropped.
func parseAntigravityQuota(body []byte, models []string) ([]quotaWindow, error) {
	var payload struct {
		Groups []struct {
			DisplayName string               `json:"displayName"`
			Buckets     []quotaSummaryBucket `json:"buckets"`
		} `json:"groups"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, fmt.Errorf("antigravity: decode quota summary: %w", errDecode)
	}
	var windows []quotaWindow
	for _, group := range payload.Groups {
		families, ok := antigravityGroupFamilies[strings.ToLower(strings.TrimSpace(group.DisplayName))]
		if !ok {
			return nil, fmt.Errorf("antigravity: unknown quota group %q", group.DisplayName)
		}
		scoped := modelsWithPrefix(models, families...)
		if len(scoped) == 0 {
			continue
		}
		for _, bucket := range group.Buckets {
			// proto3 JSON omits a zero float, so an exhausted bucket arrives without remainingFraction.
			remaining := 0.0
			if bucket.RemainingFraction != nil {
				remaining = *bucket.RemainingFraction
			}
			window, errWindow := bucketWindow("antigravity", group.DisplayName, bucket.Window, remaining, bucket.ResetTime)
			if errWindow != nil {
				return nil, errWindow
			}
			window.Models = scoped
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("antigravity: quota summary has no buckets for the credential's models")
	}
	return windows, nil
}

// bucketWindow converts one groups[].buckets[] entry (shared by Antigravity and plugin quota providers).
func bucketWindow(provider, group, window string, remaining float64, resetTime string) (quotaWindow, error) {
	period, errPeriod := bucketPeriod(provider, window)
	if errPeriod != nil {
		return quotaWindow{}, errPeriod
	}
	name := window
	if group != "" {
		name = group + ": " + window
	}
	parsed := quotaWindow{Name: name, Remaining: clampFraction(remaining), Period: period}
	// resetTime is optional in pluginapi.QuotaBucket (omitempty).
	if resetTime != "" {
		resetAt, errReset := parseInstant(provider, name+" resetTime", resetTime)
		if errReset != nil {
			return quotaWindow{}, errReset
		}
		parsed.ResetAt = resetAt
	}
	return parsed, nil
}

// --- kimi ---

func fetchKimiQuota(key string, cred hostAuth) ([]quotaWindow, error) {
	// CPA routes kimi-ai/kimi.ai credentials to kimi.ai and kimi/kimi.com to kimi.com. A file that
	// overrides its domain with a domain/base_url field is not visible here and fails with the
	// upstream's auth error.
	target := kimiComUsageURL
	if cred.Provider == "kimi-ai" || cred.Provider == "kimi.ai" {
		target = kimiAIUsageURL
	}
	body, errUsage := providerCall(key, cred, http.MethodGet, target, map[string]string{"Authorization": "Bearer $TOKEN$"}, "")
	if errUsage != nil {
		return nil, fmt.Errorf("kimi usage: %w", errUsage)
	}
	return parseKimiUsage(body, time.Now())
}

// kimiUsageDetail is one counter of the Kimi usages response; key spellings follow the
// Management Center's KimiUsageDetail.
type kimiUsageDetail struct {
	Used       *flexFloat `json:"used"`
	Limit      *flexFloat `json:"limit"`
	Remaining  *flexFloat `json:"remaining"`
	Name       string     `json:"name"`
	Title      string     `json:"title"`
	ResetTime  string     `json:"resetTime"`
	ResetTimeS string     `json:"reset_time"`
	ResetAt    string     `json:"resetAt"`
	ResetAtS   string     `json:"reset_at"`
	ResetIn    *flexFloat `json:"resetIn"`
	ResetInS   *flexFloat `json:"reset_in"`
	TTL        *flexFloat `json:"ttl"`
}

type kimiLimitItem struct {
	kimiUsageDetail
	Scope  string           `json:"scope"`
	Detail *kimiUsageDetail `json:"detail"`
	Window *struct {
		Duration *flexFloat `json:"duration"`
		TimeUnit string     `json:"timeUnit"`
	} `json:"window"`
}

// parseKimiUsage maps /coding/v1/usages: limits[] (windows with explicit duration, e.g. 5h),
// usage (the weekly summary) and usages.limit_month_total (monthly ratio on plans without a
// weekly limit). All apply to every model.
func parseKimiUsage(body []byte, now time.Time) ([]quotaWindow, error) {
	var payload struct {
		Limits []kimiLimitItem  `json:"limits"`
		Usage  *kimiUsageDetail `json:"usage"`
		Usages *struct {
			LimitMonthTotal *struct {
				UsedRatio *flexFloat `json:"used_ratio"`
				ResetTime string     `json:"reset_time"`
			} `json:"limit_month_total"`
		} `json:"usages"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, fmt.Errorf("kimi: decode usages: %w", errDecode)
	}
	var windows []quotaWindow
	for i, item := range payload.Limits {
		field := fmt.Sprintf("limits[%d]", i)
		detail := item.kimiUsageDetail
		if item.Detail != nil {
			detail = *item.Detail
		}
		if item.Window == nil || item.Window.Duration == nil {
			return nil, fmt.Errorf("kimi: %s.window.duration missing", field)
		}
		period, errPeriod := kimiPeriod(float64(*item.Window.Duration), item.Window.TimeUnit)
		if errPeriod != nil {
			return nil, fmt.Errorf("kimi: %s.window: %w", field, errPeriod)
		}
		name := firstNonBlank(item.Name, item.Title, item.Scope, detail.Name, detail.Title)
		if name == "" {
			name = kimiPeriodName(period)
		}
		window, errWindow := kimiWindow(field, name, period, detail, now)
		if errWindow != nil {
			return nil, errWindow
		}
		windows = append(windows, window)
	}
	if payload.Usage != nil {
		window, errWindow := kimiWindow("usage", "weekly", 7*24*time.Hour, *payload.Usage, now)
		if errWindow != nil {
			return nil, errWindow
		}
		windows = append(windows, window)
	}
	if payload.Usages != nil && payload.Usages.LimitMonthTotal != nil {
		monthly := payload.Usages.LimitMonthTotal
		if monthly.UsedRatio == nil {
			return nil, fmt.Errorf("kimi: usages.limit_month_total.used_ratio missing")
		}
		window := quotaWindow{Name: "monthly", Remaining: clampFraction(1 - float64(*monthly.UsedRatio)), Period: quotaMonth}
		if monthly.ResetTime != "" {
			resetAt, errReset := parseInstant("kimi", "usages.limit_month_total.reset_time", monthly.ResetTime)
			if errReset != nil {
				return nil, errReset
			}
			window.ResetAt = resetAt
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("kimi: usages response has no windows")
	}
	return windows, nil
}

func kimiWindow(field, name string, period time.Duration, detail kimiUsageDetail, now time.Time) (quotaWindow, error) {
	if detail.Limit == nil || *detail.Limit <= 0 {
		return quotaWindow{}, fmt.Errorf("kimi: %s.limit missing or not positive", field)
	}
	limit := float64(*detail.Limit)
	var remaining float64
	switch {
	case detail.Remaining != nil:
		remaining = float64(*detail.Remaining) / limit
	case detail.Used != nil:
		remaining = 1 - float64(*detail.Used)/limit
	default:
		return quotaWindow{}, fmt.Errorf("kimi: %s has neither remaining nor used", field)
	}
	window := quotaWindow{Name: name, Remaining: clampFraction(remaining), Period: period}
	if absolute := firstNonBlank(detail.ResetTime, detail.ResetTimeS, detail.ResetAt, detail.ResetAtS); absolute != "" {
		resetAt, errReset := parseInstant("kimi", field+" reset time", absolute)
		if errReset != nil {
			return quotaWindow{}, errReset
		}
		window.ResetAt = resetAt
		return window, nil
	}
	for _, relative := range []*flexFloat{detail.ResetIn, detail.ResetInS, detail.TTL} {
		if relative != nil {
			window.ResetAt = now.Add(time.Duration(float64(*relative) * float64(time.Second)))
			return window, nil
		}
	}
	return window, nil
}

// kimiPeriod converts a limits[].window; Kimi sends protobuf enums such as TIME_UNIT_MINUTE.
func kimiPeriod(duration float64, unit string) (time.Duration, error) {
	if duration <= 0 {
		return 0, fmt.Errorf("duration %v not positive", duration)
	}
	var base time.Duration
	switch strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(unit)), "TIME_UNIT_") {
	case "SECOND", "SECONDS":
		base = time.Second
	case "MINUTE", "MINUTES":
		base = time.Minute
	case "HOUR", "HOURS":
		base = time.Hour
	case "DAY", "DAYS":
		base = 24 * time.Hour
	case "WEEK", "WEEKS":
		base = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown timeUnit %q", unit)
	}
	return time.Duration(duration * float64(base)), nil
}

func kimiPeriodName(period time.Duration) string {
	switch {
	case period == 7*24*time.Hour:
		return "weekly"
	case period%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", period/(24*time.Hour))
	case period%time.Hour == 0:
		return fmt.Sprintf("%dh", period/time.Hour)
	}
	return period.String()
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// --- xai ---

func fetchXaiQuota(key string, cred hostAuth) ([]quotaWindow, error) {
	header := map[string]string{
		"Authorization":         "Bearer $TOKEN$",
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": xaiClientVersion,
		"accept":                "*/*",
		"user-agent":            xaiUserAgent,
	}
	weekly, errWeekly := providerCall(key, cred, http.MethodGet, xaiBillingWeeklyURL, header, "")
	if errWeekly != nil {
		return nil, fmt.Errorf("xai weekly billing: %w", errWeekly)
	}
	monthly, errMonthly := providerCall(key, cred, http.MethodGet, xaiBillingMonthURL, header, "")
	if errMonthly != nil {
		return nil, fmt.Errorf("xai monthly billing: %w", errMonthly)
	}
	return parseXaiBilling(weekly, monthly)
}

// xaiCents decodes a Grok money value: {"val": n}, a number, or a numeric string.
type xaiCents float64

func (c *xaiCents) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '{' {
		var wrapped struct {
			Val *flexFloat `json:"val"`
		}
		if errWrapped := json.Unmarshal(raw, &wrapped); errWrapped != nil {
			return errWrapped
		}
		if wrapped.Val == nil {
			return fmt.Errorf("money value without val: %s", raw)
		}
		*c = xaiCents(*wrapped.Val)
		return nil
	}
	var value flexFloat
	if errValue := json.Unmarshal(raw, &value); errValue != nil {
		return errValue
	}
	*c = xaiCents(value)
	return nil
}

type xaiBillingConfig struct {
	CurrentPeriod *struct {
		Type  string `json:"type"`
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"currentPeriod"`
	CreditUsagePercent *flexFloat `json:"creditUsagePercent"`
	MonthlyLimit       *xaiCents  `json:"monthlyLimit"`
	Used               *xaiCents  `json:"used"`
	BillingPeriodStart string     `json:"billingPeriodStart"`
	BillingPeriodEnd   string     `json:"billingPeriodEnd"`
}

// parseXaiBilling maps the two cli-chat-proxy billing responses. The weekly (format=credits)
// response yields "weekly credits" when it states creditUsagePercent; the monthly response
// yields "monthly" when the plan has a positive monthly limit. Neither -> usage unavailable.
func parseXaiBilling(weeklyBody, monthlyBody []byte) ([]quotaWindow, error) {
	var weekly, monthly struct {
		Config *xaiBillingConfig `json:"config"`
	}
	if errWeekly := json.Unmarshal(weeklyBody, &weekly); errWeekly != nil {
		return nil, fmt.Errorf("xai: decode weekly billing: %w", errWeekly)
	}
	if errMonthly := json.Unmarshal(monthlyBody, &monthly); errMonthly != nil {
		return nil, fmt.Errorf("xai: decode monthly billing: %w", errMonthly)
	}
	if weekly.Config == nil {
		return nil, fmt.Errorf("xai: weekly billing config missing")
	}
	if monthly.Config == nil {
		return nil, fmt.Errorf("xai: monthly billing config missing")
	}

	var windows []quotaWindow
	if usage := weekly.Config.CreditUsagePercent; usage != nil {
		window := quotaWindow{Name: "weekly credits", Remaining: remainingFromUsedPercent(float64(*usage))}
		start, end := weekly.Config.BillingPeriodStart, weekly.Config.BillingPeriodEnd
		if period := weekly.Config.CurrentPeriod; period != nil {
			start, end = period.Start, period.End
		}
		if errSpan := xaiSpan(&window, "weekly", start, end); errSpan != nil {
			return nil, errSpan
		}
		windows = append(windows, window)
	}
	if limit := monthly.Config.MonthlyLimit; limit != nil && *limit > 0 {
		if monthly.Config.Used == nil {
			return nil, fmt.Errorf("xai: monthly billing used missing")
		}
		used := min(float64(*monthly.Config.Used), float64(*limit))
		window := quotaWindow{Name: "monthly", Remaining: clampFraction(1 - used/float64(*limit))}
		if errSpan := xaiSpan(&window, "monthly", monthly.Config.BillingPeriodStart, monthly.Config.BillingPeriodEnd); errSpan != nil {
			return nil, errSpan
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("xai: usage unavailable (no weekly creditUsagePercent and no monthly limit)")
	}
	return windows, nil
}

// xaiSpan sets ResetAt from the period end and Period from start..end.
func xaiSpan(window *quotaWindow, which, start, end string) error {
	if end == "" {
		return fmt.Errorf("xai: %s billing period end missing", which)
	}
	resetAt, errEnd := parseInstant("xai", which+" period end", end)
	if errEnd != nil {
		return errEnd
	}
	window.ResetAt = resetAt
	if start == "" {
		return fmt.Errorf("xai: %s billing period start missing", which)
	}
	startAt, errStart := parseInstant("xai", which+" period start", start)
	if errStart != nil {
		return errStart
	}
	if !resetAt.After(startAt) {
		return fmt.Errorf("xai: %s billing period ends %s before it starts %s", which, end, start)
	}
	window.Period = resetAt.Sub(startAt)
	return nil
}

// --- plugin quota providers ---

// fetchPluginQuota asks CPA's quota/fetch, which serves plugin quota providers (e.g. opencode-go)
// and declarative quota probes. HTTP 501 means the credential has no quota source.
func fetchPluginQuota(key string, cred hostAuth) ([]quotaWindow, string, error) {
	status, body, errCall := cpaManagementCall(key, http.MethodPost, "/v0/management/quota/fetch", map[string]string{"auth_index": cred.AuthIndex})
	if errCall != nil {
		return nil, "", fmt.Errorf("%s: %w", cred.Provider, errCall)
	}
	if status == http.StatusNotImplemented {
		return nil, "", fmt.Errorf("%s: no quota source", cred.Provider)
	}
	if status != http.StatusOK {
		return nil, "", fmt.Errorf("%s: management quota/fetch: HTTP %d: %s", cred.Provider, status, snippet(body))
	}
	return parsePluginQuota(cred.Provider, body)
}

// parsePluginQuota maps a pluginapi.QuotaFetchResponse. Plugin groups carry no model scope, so
// every window applies to all of the credential's models.
func parsePluginQuota(provider string, body []byte) ([]quotaWindow, string, error) {
	var payload struct {
		Subscription *struct {
			Plan string `json:"plan"`
		} `json:"subscription"`
		Groups []struct {
			DisplayName string               `json:"displayName"`
			Buckets     []quotaSummaryBucket `json:"buckets"`
		} `json:"groups"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, "", fmt.Errorf("%s: decode quota/fetch: %w", provider, errDecode)
	}
	var windows []quotaWindow
	for _, group := range payload.Groups {
		groupName := group.DisplayName
		if len(payload.Groups) == 1 {
			groupName = ""
		}
		for i, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				return nil, "", fmt.Errorf("%s: group %q bucket %d remainingFraction missing", provider, group.DisplayName, i)
			}
			window, errWindow := bucketWindow(provider, groupName, bucket.Window, *bucket.RemainingFraction, bucket.ResetTime)
			if errWindow != nil {
				return nil, "", errWindow
			}
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, "", fmt.Errorf("%s: quota/fetch returned no buckets", provider)
	}
	var plan string
	if payload.Subscription != nil {
		plan = payload.Subscription.Plan
	}
	return windows, plan, nil
}
