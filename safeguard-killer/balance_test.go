package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func setQuota(t *testing.T, authID string, windows ...quotaWindow) {
	t.Helper()
	quotaMu.Lock()
	quotaSnapshots[authID] = credentialQuota{AuthID: authID, Windows: windows, FetchedAt: time.Now()}
	quotaMu.Unlock()
	t.Cleanup(func() {
		quotaMu.Lock()
		delete(quotaSnapshots, authID)
		quotaMu.Unlock()
	})
}

func weekly(remaining float64, resetIn time.Duration) quotaWindow {
	return quotaWindow{Name: "7d", Remaining: remaining, ResetAt: testNow.Add(resetIn), Period: 7 * 24 * time.Hour}
}

// fakeHost answers host.auth.list / host.auth.models for the given auth IDs (all serving models)
// and delegates every other method to other.
func fakeHost(t *testing.T, models []string, other func(method string, payload any) (json.RawMessage, error), ids ...string) {
	t.Helper()
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case methodHostAuthList:
			files := make([]hostAuth, 0, len(ids))
			for index, id := range ids {
				files = append(files, hostAuth{ID: id, AuthIndex: string(rune('a' + index)), Provider: "claude"})
			}
			return json.Marshal(hostAuthListResponse{Files: files})
		case methodHostAuthModels:
			var resp hostAuthModelsResponse
			for _, id := range ids {
				resp.Credentials = append(resp.Credentials, struct {
					ID     string   `json:"id"`
					Models []string `json:"models"`
				}{ID: id, Models: models})
			}
			return json.Marshal(resp)
		}
		return other(method, payload)
	}
	resetHostState(t)
}

func resetHostState(t *testing.T) {
	t.Helper()
	hostMu.Lock()
	hostCache = hostSnapshot{}
	hostMu.Unlock()
	stateMu.Lock()
	sessions = make(map[string]*sessionBinding)
	groupSessions = make(map[string]*sessionBinding)
	decisions = nil
	stateMu.Unlock()
	t.Cleanup(func() {
		hostCall = callHost
		hostMu.Lock()
		hostCache = hostSnapshot{}
		hostMu.Unlock()
	})
}

func noOther(t *testing.T) func(string, any) (json.RawMessage, error) {
	return func(method string, _ any) (json.RawMessage, error) {
		t.Fatalf("unexpected host call %s", method)
		return nil, nil
	}
}

func balanceConfig() pluginConfig {
	cfg := defaultPluginConfig()
	cfg.ManagementKey = "k"
	quotaRefreshAge = 1000 * time.Minute
	return cfg
}

func pickReq(session string, ids ...string) schedulerPickRequest {
	req := schedulerPickRequest{Model: "claude-opus-4-8", Options: schedulerOptions{Metadata: map[string]any{metadataRequestedModel: "claude-opus-4-8"}}}
	if session != "" {
		req.Options.Metadata[metadataCanonicalSessionID] = session
	}
	for _, id := range ids {
		req.Candidates = append(req.Candidates, schedulerCandidate{ID: id, Provider: "claude"})
	}
	return req
}

func TestPickPrefersQuotaExpiringSooner(t *testing.T) {
	fakeHost(t, []string{"claude-opus-4-8"}, noOther(t), "a", "b")
	// a: 30% left resetting in 24h (1.25%/h) beats b: 60% left resetting in 6 days (0.42%/h).
	setQuota(t, "a", weekly(0.30, 24*time.Hour))
	setQuota(t, "b", weekly(0.60, 144*time.Hour))
	resp, errPick := pick(pickReq("", "a", "b"), balanceConfig(), testNow)
	if errPick != nil || resp.AuthID != "a" || !resp.Handled {
		t.Fatalf("pick() = %+v, %v; want a", resp, errPick)
	}
}

func TestPickSessionStickyUntilIdle(t *testing.T) {
	fakeHost(t, []string{"claude-opus-4-8"}, noOther(t), "a", "b")
	setQuota(t, "a", weekly(0.30, 24*time.Hour))
	setQuota(t, "b", weekly(0.60, 144*time.Hour))
	cfg := balanceConfig()
	if resp, _ := pick(pickReq("s1", "a", "b"), cfg, testNow); resp.AuthID != "a" {
		t.Fatalf("first pick = %+v, want a", resp)
	}
	// b becomes the better credential, but the active session keeps a.
	setQuota(t, "b", weekly(0.90, 2*time.Hour))
	if resp, _ := pick(pickReq("s1", "a", "b"), cfg, testNow.Add(4*time.Minute)); resp.AuthID != "a" {
		t.Fatalf("active session pick = %+v, want a", resp)
	}
	// After more than idle_minutes without a prompt the session re-ranks.
	if resp, _ := pick(pickReq("s1", "a", "b"), cfg, testNow.Add(10*time.Minute)); resp.AuthID != "b" {
		t.Fatalf("idle session pick = %+v, want b", resp)
	}
}

func TestPickExcludesLowQuotaAndDefersWithoutData(t *testing.T) {
	fakeHost(t, []string{"claude-opus-4-8"}, noOther(t), "drained", "nodata")
	setQuota(t, "drained", weekly(0.9, 48*time.Hour), quotaWindow{Name: "5h", Remaining: 0.01, ResetAt: testNow.Add(time.Hour), Period: 5 * time.Hour})
	quotaMu.Lock()
	quotaSnapshots["nodata"] = credentialQuota{AuthID: "nodata", FetchedAt: time.Now()}
	quotaMu.Unlock()
	t.Cleanup(func() { quotaMu.Lock(); delete(quotaSnapshots, "nodata"); quotaMu.Unlock() })

	resp, errPick := pick(pickReq("", "drained", "nodata"), balanceConfig(), testNow)
	if errPick != nil || resp.Handled {
		t.Fatalf("pick() = %+v, %v; want unhandled", resp, errPick)
	}
	stateMu.Lock()
	last := decisions[len(decisions)-1]
	stateMu.Unlock()
	if !strings.Contains(last.Ranking[0].Excluded+last.Ranking[1].Excluded, "5h has 1% left") {
		t.Fatalf("ranking = %+v, want 5h exclusion", last.Ranking)
	}
}

func TestPickSingleCandidateOrNoKeyUnhandled(t *testing.T) {
	if resp, _ := pick(pickReq("", "a"), balanceConfig(), testNow); resp.Handled {
		t.Fatalf("single candidate handled")
	}
	if resp, _ := pick(pickReq("", "a", "b"), defaultPluginConfig(), testNow); resp.Handled {
		t.Fatalf("pick without management key handled")
	}
}

func TestPickModelScopedWindow(t *testing.T) {
	fakeHost(t, []string{"claude-opus-4-8", "claude-fable-5"}, noOther(t), "a", "b")
	setQuota(t, "a", weekly(0.5, 24*time.Hour), quotaWindow{Name: "7d Fable", Remaining: 0, ResetAt: testNow.Add(24 * time.Hour), Period: 7 * 24 * time.Hour, Models: []string{"claude-fable-5"}})
	setQuota(t, "b", weekly(0.5, 100*time.Hour))
	if resp, _ := pick(pickReq("", "a", "b"), balanceConfig(), testNow); resp.AuthID != "a" {
		t.Fatalf("opus pick = %+v, want a", resp)
	}
	fable := pickReq("", "a", "b")
	fable.Model = "claude-fable-5"
	if resp, _ := pick(fable, balanceConfig(), testNow); resp.AuthID != "b" {
		t.Fatalf("fable pick = %+v, want b", resp)
	}
}

func TestRolledWindowRefillsAfterReset(t *testing.T) {
	remaining, resetAt := rolledWindow(weekly(0, -24*time.Hour), testNow)
	if remaining != 1 || !resetAt.Equal(testNow.Add(6*24*time.Hour)) {
		t.Fatalf("rolledWindow = %v, %v; want 1, now+6d", remaining, resetAt)
	}
}

func TestValidateConfigGroups(t *testing.T) {
	cases := map[string][]modelGroup{
		"needs a name":  {{ID: "g", Models: []string{"m"}}},
		"at least one":  {{ID: "g", Name: "G"}},
		"used twice":    {{ID: "g", Name: "G", Models: []string{"m"}}, {ID: "g", Name: "H", Models: []string{"m"}}},
		"groups cannot": {{ID: "g", Name: "G", Models: []string{"h"}}, {ID: "h", Name: "H", Models: []string{"m"}}},
		"needs an id":   {{Name: "G", Models: []string{"m"}}},
	}
	for want, groups := range cases {
		cfg := defaultPluginConfig()
		cfg.Groups = groups
		if errValidate := validateConfig(cfg); errValidate == nil || !strings.Contains(errValidate.Error(), want) {
			t.Fatalf("validateConfig(%+v) = %v, want %q", groups, errValidate, want)
		}
	}
	cfg := defaultPluginConfig()
	cfg.Groups = []modelGroup{{ID: "uncensored-models", Name: "Uncensored", Models: []string{"glm-5.3-uncensored", "deepseek-v4.1-flash-uncensored"}}}
	if errValidate := validateConfig(cfg); errValidate != nil {
		t.Fatalf("validateConfig(valid) = %v", errValidate)
	}
	cfg.IdleMinutes = 0
	if errValidate := validateConfig(cfg); errValidate != nil {
		t.Fatalf("validateConfig(idle_minutes=0) = %v", errValidate)
	}
	cfg.IdleMinutes = -1
	if errValidate := validateConfig(cfg); errValidate == nil {
		t.Fatalf("validateConfig(idle_minutes=-1) = nil, want error")
	}
}
