package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	methodHostAuthList   = "host.auth.list"
	methodHostAuthModels = "host.auth.models"

	// Scheduler option metadata set by the host (cliproxy executor and session packages).
	metadataRequestedModel     = "requested_model"
	metadataCanonicalSessionID = "canonical_session_id"
	metadataDerivedSessionID   = "derived_session_id"

	hostCacheTTL     = 15 * time.Second
	maxDecisions     = 200
	minHoursToReset  = 0.25
	sessionRetention = 24 * time.Hour
)

type schedulerPickRequest struct {
	Model      string               `json:"Model"`
	Options    schedulerOptions     `json:"Options"`
	Candidates []schedulerCandidate `json:"Candidates"`
}

type schedulerOptions struct {
	Metadata map[string]any `json:"Metadata"`
}

type schedulerCandidate struct {
	ID       string `json:"ID"`
	Provider string `json:"Provider"`
}

type schedulerPickResponse struct {
	AuthID  string `json:"AuthID,omitempty"`
	Handled bool   `json:"Handled"`
}

type hostAuthListResponse struct {
	Files []hostAuth `json:"files"`
}

type hostAuthModelsResponse struct {
	Credentials []struct {
		ID     string   `json:"id"`
		Models []string `json:"models"`
	} `json:"credentials"`
}

type hostSnapshot struct {
	auths  []hostAuth
	models map[string][]string
	// modelsErr explains why the per-credential model lists are missing; the snapshot itself is
	// still usable, because callers treat an unknown model list as "credential may serve anything".
	modelsErr error
	fetchedAt time.Time
}

var (
	// hostCall is the host callback entry point; unit tests replace it because they run without a host.
	hostCall = callHost

	hostMu    sync.Mutex
	hostCache hostSnapshot

	// hostModelsUnsupported latches the first time the host reports host.auth.models as an unknown
	// callback: older hosts predate it, so retrying every cache window only wastes callbacks.
	hostModelsUnsupported atomic.Bool

	stateMu   sync.Mutex
	sessions  = make(map[string]*sessionBinding)
	decisions []decision
)

// isUnsupportedCallback reports whether a host callback error means the host does not implement
// that callback at all, as opposed to a transient failure.
func isUnsupportedCallback(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unsupported") || strings.Contains(message, "unknown method") ||
		strings.Contains(message, "not implemented") || strings.Contains(message, "method not found")
}

// hostState returns the credential list and per-credential models, cached for hostCacheTTL.
// host.auth.models is optional: a host that predates it still yields the credential list, with
// models left unknown and the callback error reported so the UI can explain the degradation.
func hostState(now time.Time) (hostSnapshot, error) {
	hostMu.Lock()
	defer hostMu.Unlock()
	if !hostCache.fetchedAt.IsZero() && now.Sub(hostCache.fetchedAt) < hostCacheTTL {
		return hostCache, nil
	}
	rawList, errList := hostCall(methodHostAuthList, struct{}{})
	if errList != nil {
		return hostSnapshot{}, errList
	}
	var list hostAuthListResponse
	if errUnmarshal := json.Unmarshal(rawList, &list); errUnmarshal != nil {
		return hostSnapshot{}, fmt.Errorf("decode host.auth.list: %w", errUnmarshal)
	}
	models, errModels := hostCredentialModels()
	// A single cache window keeps both halves of the snapshot in step: callers get the credential
	// list plus whatever model detail the host could provide, and modelsErr only explains the gap.
	hostCache = hostSnapshot{auths: list.Files, models: models, modelsErr: errModels, fetchedAt: now}
	return hostCache, nil
}

// hostCredentialModels fetches the per-credential model lists. A host without the callback yields an
// empty map, so every credential is treated as model-unknown rather than model-less.
func hostCredentialModels() (map[string][]string, error) {
	models := make(map[string][]string)
	if hostModelsUnsupported.Load() {
		return models, nil
	}
	rawModels, errModels := hostCall(methodHostAuthModels, struct{}{})
	if errModels != nil {
		if isUnsupportedCallback(errModels) {
			hostModelsUnsupported.Store(true)
			return models, errModels
		}
		return models, errModels
	}
	var modelsResp hostAuthModelsResponse
	if errUnmarshal := json.Unmarshal(rawModels, &modelsResp); errUnmarshal != nil {
		return models, fmt.Errorf("decode host.auth.models: %w", errUnmarshal)
	}
	for _, credential := range modelsResp.Credentials {
		models[credential.ID] = credential.Models
	}
	return models, nil
}

func authsByID(snapshot hostSnapshot, ids []string) ([]hostAuth, error) {
	byID := make(map[string]hostAuth, len(snapshot.auths))
	for _, auth := range snapshot.auths {
		byID[auth.ID] = auth
	}
	out := make([]hostAuth, 0, len(ids))
	for _, id := range ids {
		auth, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("credential %s is not in the host credential list", id)
		}
		out = append(out, auth)
	}
	return out, nil
}

// pick balances models served by two or more credentials: a session keeps its credential while
// prompts keep coming, and a new or idle session goes to the credential with the highest
// remaining / hours_to_reset. Without quota data for any candidate the host's own
// selector decides.
func pick(req schedulerPickRequest, cfg pluginConfig, now time.Time) (schedulerPickResponse, error) {
	if len(req.Candidates) < 2 {
		return schedulerPickResponse{Handled: false}, nil
	}
	if cfg.ManagementKey == "" {
		return schedulerPickResponse{Handled: false}, nil
	}
	requested, okRequested := req.Options.Metadata[metadataRequestedModel].(string)
	if !okRequested {
		return schedulerPickResponse{}, fmt.Errorf("scheduler request has no %s metadata", metadataRequestedModel)
	}
	snapshot, errHost := hostState(now)
	if errHost != nil {
		return schedulerPickResponse{}, errHost
	}
	ids := make([]string, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		ids = append(ids, candidate.ID)
	}
	auths, errAuths := authsByID(snapshot, ids)
	if errAuths != nil {
		return schedulerPickResponse{}, errAuths
	}
	ensureQuotaFresh(cfg.ManagementKey, auths)

	ranking := rankCredentials(cfg, auths, req.Model, now)
	session := sessionKey(req.Options.Metadata)
	bindingKey := session + "\x00" + req.Model

	stateMu.Lock()
	defer stateMu.Unlock()
	if session != "" {
		if binding, ok := sessions[bindingKey]; ok && now.Before(binding.IdleExpiresAt) {
			if entry, found := findRank(ranking, binding.AuthID); found && entry.Excluded == "" {
				touchSession(binding, cfg, now)
				recordDecision(decision{Time: now, Kind: "balance", Model: requested, Session: session, AuthID: binding.AuthID, Reason: "session active, kept credential", Ranking: ranking})
				return schedulerPickResponse{AuthID: binding.AuthID, Handled: true}, nil
			}
		}
	}
	best, ok := bestRank(ranking)
	if !ok {
		recordDecision(decision{Time: now, Kind: "balance", Model: requested, Session: session, Reason: "no candidate with usable quota data, host selector decides", Ranking: ranking})
		return schedulerPickResponse{Handled: false}, nil
	}
	reason := "highest rate (no session identity)"
	if session != "" {
		sessions[bindingKey] = &sessionBinding{Session: session, AuthID: best.AuthID, Provider: best.Provider, Model: req.Model, BoundAt: now}
		touchSession(sessions[bindingKey], cfg, now)
		reason = "new or idle session bound to highest rate"
	}
	recordDecision(decision{Time: now, Kind: "balance", Model: requested, Session: session, AuthID: best.AuthID, Reason: reason, Ranking: ranking})
	pruneSessions(now)
	return schedulerPickResponse{AuthID: best.AuthID, Handled: true}, nil
}

// sessionKey uses the session identity the host derived for the request.
func sessionKey(metadata map[string]any) string {
	if canonical, ok := metadata[metadataCanonicalSessionID].(string); ok && canonical != "" {
		return canonical
	}
	if derived, ok := metadata[metadataDerivedSessionID].(string); ok && derived != "" {
		return "derived:" + derived
	}
	return ""
}

func touchSession(binding *sessionBinding, cfg pluginConfig, now time.Time) {
	binding.LastSeen = now
	binding.IdleExpiresAt = now.Add(time.Duration(cfg.IdleMinutes) * time.Minute)
}

func pruneSessions(now time.Time) {
	for key, binding := range sessions {
		if now.Sub(binding.LastSeen) > sessionRetention {
			delete(sessions, key)
		}
	}
	for key, binding := range groupSessions {
		if now.Sub(binding.LastSeen) > sessionRetention {
			delete(groupSessions, key)
		}
	}
}

// rankCredentials evaluates credentials for a model, best first; excluded ones go last.
func rankCredentials(cfg pluginConfig, auths []hostAuth, model string, now time.Time) []rankEntry {
	ranking := make([]rankEntry, 0, len(auths))
	for _, auth := range auths {
		entry := rankEntry{AuthID: auth.ID, Provider: auth.Provider}
		rateCredential(&entry, model, cfg.MinRemaining, now)
		ranking = append(ranking, entry)
	}
	sort.SliceStable(ranking, func(i, j int) bool {
		if (ranking[i].Excluded == "") != (ranking[j].Excluded == "") {
			return ranking[i].Excluded == ""
		}
		return ranking[i].Rate > ranking[j].Rate
	})
	return ranking
}

// rateCredential scores a credential from its quota windows that apply to the model. The longest
// window governs the rate (quota that resets later can wait); every applicable window must keep
// at least minRemaining.
func rateCredential(entry *rankEntry, model string, minRemaining float64, now time.Time) {
	quota, ok := quotaFor(entry.AuthID)
	if !ok {
		entry.Excluded = "quota not fetched yet"
		return
	}
	if quota.Error != "" {
		entry.Excluded = "quota error: " + quota.Error
		return
	}
	var governing *quotaWindow
	for index := range quota.Windows {
		window := &quota.Windows[index]
		if len(window.Models) > 0 && !containsString(window.Models, model) {
			continue
		}
		remaining, resetAt := rolledWindow(*window, now)
		if remaining < minRemaining {
			entry.Excluded = fmt.Sprintf("%s has %.0f%% left", window.Name, remaining*100)
			return
		}
		if governing == nil || resetAt.After(governingReset(*governing, now)) {
			governing = window
		}
	}
	if governing == nil {
		entry.Excluded = "no quota window applies to " + model
		return
	}
	remaining, resetAt := rolledWindow(*governing, now)
	if resetAt.IsZero() {
		entry.Excluded = governing.Name + " has no reset time"
		return
	}
	hours := math.Max(resetAt.Sub(now).Hours(), minHoursToReset)
	entry.Window = governing.Name
	entry.Remaining = remaining
	entry.HoursToReset = hours
	entry.Rate = remaining / hours
}

// rolledWindow applies resets that happened since the snapshot: a passed reset with a known
// period means the window is full again until the next period boundary.
func rolledWindow(window quotaWindow, now time.Time) (float64, time.Time) {
	if window.ResetAt.IsZero() || window.ResetAt.After(now) || window.Period <= 0 {
		return window.Remaining, window.ResetAt
	}
	periods := math.Floor(float64(now.Sub(window.ResetAt))/float64(window.Period)) + 1
	return 1, window.ResetAt.Add(time.Duration(periods) * window.Period)
}

func governingReset(window quotaWindow, now time.Time) time.Time {
	_, resetAt := rolledWindow(window, now)
	return resetAt
}

func bestRank(ranking []rankEntry) (rankEntry, bool) {
	if len(ranking) == 0 || ranking[0].Excluded != "" {
		return rankEntry{}, false
	}
	return ranking[0], true
}

func findRank(ranking []rankEntry, authID string) (rankEntry, bool) {
	for _, entry := range ranking {
		if entry.AuthID == authID {
			return entry, true
		}
	}
	return rankEntry{}, false
}

// recordDecision appends to the ring buffer; callers hold stateMu.
func recordDecision(entry decision) {
	decisions = append(decisions, entry)
	if len(decisions) > maxDecisions {
		decisions = decisions[len(decisions)-maxDecisions:]
	}
}

// mergedModels returns the host's per-credential model map with the management-API cache filling in
// credentials the host could not describe. Hosts that predate host.auth.models report nothing, so
// the fallback is what keeps model-scoped routing working there.
func mergedModels(snapshot hostSnapshot) map[string][]string {
	fallback := fallbackModels()
	if len(fallback) == 0 {
		return snapshot.models
	}
	merged := make(map[string][]string, len(snapshot.models)+len(fallback))
	for id, models := range snapshot.models {
		merged[id] = models
	}
	for id, models := range fallback {
		if len(merged[id]) == 0 {
			merged[id] = models
		}
	}
	return merged
}

// servesKnownModel reports whether a credential serves a model. An empty model list means the host
// could not describe the credential, which counts as "may serve anything": skipping on missing
// evidence would drop every credential on a host without the host.auth.models callback.
func servesKnownModel(models map[string][]string, authID, model string) bool {
	known := models[authID]
	return len(known) == 0 || containsString(known, model)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildUIState() uiState {
	now := time.Now()
	cfg := currentConfig()
	state := uiState{Version: pluginVersion, Now: now, Config: cfg, ManagementError: managementStatus(cfg.ManagementKey, now)}
	snapshot, errHost := hostState(now)
	if errHost != nil {
		state.Error = errHost.Error()
	}
	if state.ManagementError == "" {
		ensureQuotaFresh(cfg.ManagementKey, snapshot.auths)
	}
	// Hosts without the host.auth.models callback get their model lists from the management API,
	// so model-scoped groups keep working; models_error only reports which source answered.
	if snapshot.modelsErr != nil {
		ensureFallbackModels(cfg.ManagementKey, snapshot.auths)
	}
	modelMap := mergedModels(snapshot)
	if snapshot.modelsErr != nil {
		// Routing stays usable: model lists come from the management API where available, and a
		// credential with no list is treated as serving any model.
		state.ModelsError = snapshot.modelsErr.Error()
	}
	models := make(map[string]bool)
	stateMu.Lock()
	defer stateMu.Unlock()
	for _, auth := range snapshot.auths {
		quota, hasQuota := quotaFor(auth.ID)
		state.Credentials = append(state.Credentials, uiCredential{Auth: auth, Quota: quota, HasQuota: hasQuota, Models: modelMap[auth.ID]})
		for _, model := range modelMap[auth.ID] {
			models[model] = true
		}
	}
	for model := range models {
		state.Models = append(state.Models, model)
	}
	sort.Strings(state.Models)
	for _, binding := range sessions {
		state.Sessions = append(state.Sessions, *binding)
	}
	for _, binding := range groupSessions {
		state.Sessions = append(state.Sessions, *binding)
	}
	sort.Slice(state.Sessions, func(i, j int) bool { return state.Sessions[i].LastSeen.After(state.Sessions[j].LastSeen) })
	for index := len(decisions) - 1; index >= 0; index-- {
		state.Decisions = append(state.Decisions, decisions[index])
	}
	return state
}
