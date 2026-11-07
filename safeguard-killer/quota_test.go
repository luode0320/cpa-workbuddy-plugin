package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quotaTestNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

const (
	quotaTestWeek = 7 * 24 * time.Hour
)

func mustInstant(t *testing.T, value string) time.Time {
	t.Helper()
	instant, errParse := time.Parse(time.RFC3339Nano, value)
	if errParse != nil {
		t.Fatalf("parse %q: %v", value, errParse)
	}
	return instant
}

func assertWindows(t *testing.T, got, want []quotaWindow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d windows %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Name != w.Name || !g.ResetAt.Equal(w.ResetAt) || g.Period != w.Period || !reflect.DeepEqual(g.Models, w.Models) {
			t.Errorf("window %d = %+v, want %+v", i, g, w)
		}
		if diff := g.Remaining - w.Remaining; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("window %d remaining = %v, want %v", i, g.Remaining, w.Remaining)
		}
	}
}

func assertErrorContains(t *testing.T, errGot error, want string) {
	t.Helper()
	if errGot == nil {
		t.Fatalf("error = nil, want containing %q", want)
	}
	if !strings.Contains(errGot.Error(), want) {
		t.Fatalf("error = %q, want containing %q", errGot.Error(), want)
	}
}

var claudeModels = []string{"claude-opus-4-1-20250805", "claude-sonnet-4-5-20250929", "claude-fable-5", "claude-haiku-4-5"}

func TestQuotaParseClaudeUsage(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		models  []string
		want    []quotaWindow
		wantErr string
	}{
		{
			name: "plan windows, dollar balances and extra usage skipped",
			body: `{
				"five_hour": {"utilization": 42.5, "resets_at": "2026-10-09T18:00:00Z"},
				"seven_day": {"utilization": 16, "resets_at": "2026-10-15T09:00:00Z"},
				"seven_day_oauth_apps": null,
				"seven_day_opus": {"utilization": 0, "resets_at": null},
				"seven_day_sonnet": {"utilization": 10, "resets_at": "2026-10-15T09:00:00.000000+00:00"},
				"extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 1250, "utilization": 25, "currency": "USD"},
				"iguana_necktie": {"limit_dollars": 100, "used_dollars": 5.04, "remaining_dollars": 94.96, "utilization": 5.04, "resets_at": "2026-11-01T00:00:00Z"},
				"additional_credit_balance": {"limit_dollars": 20, "used_dollars": 0, "remaining_dollars": 20, "utilization": 0, "resets_at": null}
			}`,
			models: claudeModels,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.575, ResetAt: mustInstant(t, "2026-10-09T18:00:00Z"), Period: 5 * time.Hour},
				{Name: "7d", Remaining: 0.84, ResetAt: mustInstant(t, "2026-10-15T09:00:00Z"), Period: quotaTestWeek},
				{Name: "7d Opus", Remaining: 1, Period: quotaTestWeek, Models: []string{"claude-opus-4-1-20250805"}},
				{Name: "7d Sonnet", Remaining: 0.9, ResetAt: mustInstant(t, "2026-10-15T09:00:00Z"), Period: quotaTestWeek, Models: []string{"claude-sonnet-4-5-20250929"}},
			},
		},
		{
			name: "scoped Fable limit wins over legacy iguana_necktie",
			body: `{
				"five_hour": {"utilization": 10, "resets_at": "2026-10-09T18:00:00Z"},
				"iguana_necktie": {"utilization": 99, "resets_at": "2026-10-20T10:00:00Z"},
				"limits": [
					{"kind": "session", "percent": 10, "resets_at": "2026-10-09T18:00:00Z", "is_active": true, "scope": null},
					{"kind": "weekly_scoped", "group": "weekly", "percent": 30, "resets_at": "2026-10-14T10:00:00Z", "is_active": false, "scope": {"model": {"id": null, "display_name": "Fable 5"}}},
					{"kind": "weekly_scoped", "group": "weekly", "percent": 64, "resets_at": "2026-10-13T10:00:00.000000+00:00", "is_active": true, "scope": {"model": {"id": null, "display_name": "Fable"}}}
				]
			}`,
			models: claudeModels,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.9, ResetAt: mustInstant(t, "2026-10-09T18:00:00Z"), Period: 5 * time.Hour},
				{Name: "7d Fable", Remaining: 0.36, ResetAt: mustInstant(t, "2026-10-13T10:00:00Z"), Period: quotaTestWeek, Models: []string{"claude-fable-5"}},
			},
		},
		{
			name:   "legacy iguana_necktie is Fable without a scoped limit",
			body:   `{"seven_day": {"utilization": 50, "resets_at": "2026-10-15T09:00:00Z"}, "iguana_necktie": {"utilization": 20, "resets_at": "2026-10-20T10:00:00Z"}}`,
			models: claudeModels,
			want: []quotaWindow{
				{Name: "7d", Remaining: 0.5, ResetAt: mustInstant(t, "2026-10-15T09:00:00Z"), Period: quotaTestWeek},
				{Name: "7d Fable", Remaining: 0.8, ResetAt: mustInstant(t, "2026-10-20T10:00:00Z"), Period: quotaTestWeek, Models: []string{"claude-fable-5"}},
			},
		},
		{
			name:   "model window dropped when the credential serves no matching model",
			body:   `{"seven_day": {"utilization": 100, "resets_at": "2026-10-15T09:00:00Z"}, "seven_day_opus": {"utilization": 100, "resets_at": "2026-10-15T09:00:00Z"}}`,
			models: []string{"claude-sonnet-4-5-20250929"},
			want: []quotaWindow{
				{Name: "7d", Remaining: 0, ResetAt: mustInstant(t, "2026-10-15T09:00:00Z"), Period: quotaTestWeek},
			},
		},
		{name: "window without utilization", body: `{"five_hour": {"resets_at": "2026-10-09T18:00:00Z"}}`, wantErr: "five_hour.utilization missing"},
		{name: "bad reset", body: `{"five_hour": {"utilization": 1, "resets_at": "soon"}}`, wantErr: "five_hour.resets_at"},
		{name: "no windows", body: `{"extra_usage": {"is_enabled": false}}`, wantErr: "no windows"},
		{name: "not json", body: `<html>`, wantErr: "claude: decode usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errParse := parseClaudeUsage([]byte(tt.body), tt.models)
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parseClaudeUsage: %v", errParse)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestQuotaParseClaudeProfile(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"max 20x tier", `{"account": {"has_claude_max": true, "has_claude_pro": false}, "organization": {"organization_type": "claude_max", "rate_limit_tier": "default_claude_max_20x", "subscription_status": "active"}}`, "max20x"},
		{"max 5x tier", `{"account": {"has_claude_max": true}, "organization": {"rate_limit_tier": "default_claude_max_5x"}}`, "max5x"},
		{"team", `{"account": {"has_claude_max": true}, "organization": {"organization_type": "claude_team", "subscription_status": "active", "rate_limit_tier": "default_claude_max_5x"}}`, "team"},
		{"pro", `{"account": {"has_claude_max": false, "has_claude_pro": true}, "organization": {"rate_limit_tier": "default_claude_ai"}}`, "pro"},
		{"free", `{"account": {"has_claude_max": false, "has_claude_pro": false}}`, "free"},
		{"unknown", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errParse := parseClaudeProfile([]byte(tt.body))
			if errParse != nil {
				t.Fatalf("parseClaudeProfile: %v", errParse)
			}
			if got != tt.want {
				t.Fatalf("plan = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQuotaParseCodexUsage(t *testing.T) {
	codexModels := []string{"gpt-5.3-codex", "gpt-5.3-codex-spark"}
	tests := []struct {
		name     string
		body     string
		want     []quotaWindow
		wantPlan string
		wantErr  string
	}{
		{
			name: "pro weekly primary and Spark additional limit",
			body: `{
				"plan_type": "pro",
				"rate_limit": {"allowed": true, "limit_reached": false,
					"primary_window": {"used_percent": 1, "limit_window_seconds": 604800, "reset_after_seconds": 601888, "reset_at": 1785902974},
					"secondary_window": null},
				"code_review_rate_limit": null,
				"additional_rate_limits": [{"limit_name": "GPT-5.3-Codex-Spark", "metered_feature": "codex_bengalfox",
					"rate_limit": {"allowed": true, "limit_reached": false,
						"primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 602111, "reset_at": 1785903197},
						"secondary_window": null}}],
				"rate_limit_reset_credits": {"available_count": 1, "applicable_available_count": 0}
			}`,
			want: []quotaWindow{
				{Name: "weekly", Remaining: 0.99, ResetAt: time.Unix(1785902974, 0), Period: quotaTestWeek},
				{Name: "GPT-5.3-Codex-Spark weekly", Remaining: 1, ResetAt: time.Unix(1785903197, 0), Period: quotaTestWeek, Models: []string{"gpt-5.3-codex-spark"}},
			},
			wantPlan: "pro",
		},
		{
			name: "plus 5h and weekly, relative reset, unmatched additional limit dropped",
			body: `{
				"plan_type": "plus",
				"rate_limit": {
					"primary_window": {"used_percent": 75, "limit_window_seconds": 18000, "reset_after_seconds": 3600},
					"secondary_window": {"used_percent": 20, "limit_window_seconds": 604800, "reset_at": 1760400000}},
				"additional_rate_limits": [{"limit_name": "Some Other Model", "rate_limit": {"primary_window": {"used_percent": 100, "limit_window_seconds": 604800, "reset_at": 1760400000}}}]
			}`,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.25, ResetAt: quotaTestNow.Add(time.Hour), Period: 5 * time.Hour},
				{Name: "weekly", Remaining: 0.8, ResetAt: time.Unix(1760400000, 0), Period: quotaTestWeek},
			},
			wantPlan: "plus",
		},
		{
			name: "team monthly secondary",
			body: `{"plan_type": "team", "rate_limit": {"primary_window": null, "secondary_window": {"used_percent": 40, "limit_window_seconds": 2592000, "reset_at": 1760400000}}}`,
			want: []quotaWindow{
				{Name: "monthly", Remaining: 0.6, ResetAt: time.Unix(1760400000, 0), Period: 30 * 24 * time.Hour},
			},
			wantPlan: "team",
		},
		{name: "missing rate_limit", body: `{"plan_type": "free"}`, wantErr: "rate_limit missing"},
		{name: "missing used_percent", body: `{"rate_limit": {"primary_window": {"limit_window_seconds": 18000, "reset_at": 1}}}`, wantErr: "primary_window.used_percent missing"},
		{name: "missing window length", body: `{"rate_limit": {"primary_window": {"used_percent": 1, "reset_at": 1}}}`, wantErr: "limit_window_seconds missing"},
		{name: "missing reset", body: `{"rate_limit": {"primary_window": {"used_percent": 1, "limit_window_seconds": 18000}}}`, wantErr: "neither reset_at nor reset_after_seconds"},
		{name: "no windows", body: `{"rate_limit": {"primary_window": null, "secondary_window": null}}`, wantErr: "no windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, plan, errParse := parseCodexUsage([]byte(tt.body), quotaTestNow, codexModels)
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parseCodexUsage: %v", errParse)
			}
			if plan != tt.wantPlan {
				t.Errorf("plan = %q, want %q", plan, tt.wantPlan)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestQuotaParseAntigravityQuota(t *testing.T) {
	agModels := []string{"gemini-3-pro-high", "gemini-3-flash", "claude-sonnet-4-6-thinking", "gpt-oss-120b-medium"}
	gemini := []string{"gemini-3-pro-high", "gemini-3-flash"}
	claudeGPT := []string{"claude-sonnet-4-6-thinking", "gpt-oss-120b-medium"}
	tests := []struct {
		name    string
		body    string
		models  []string
		want    []quotaWindow
		wantErr string
	}{
		{
			name: "two groups with 5h and weekly buckets, exhausted bucket omits remainingFraction",
			body: `{"groups": [
				{"displayName": "Gemini models", "description": "Models within this group: Gemini 3 Pro, Gemini 3 Flash",
					"buckets": [
						{"bucketId": "g5", "displayName": "Five Hour Limit Remaining", "window": "5h", "remainingFraction": 0.8, "resetTime": "2026-10-09T15:00:00.123456789Z"},
						{"bucketId": "gw", "displayName": "Weekly Limit Remaining", "window": "weekly", "remainingFraction": 0.5, "resetTime": "2026-10-14T00:00:00Z"}]},
				{"displayName": "Claude and GPT models", "description": "Models within this group: Claude Sonnet 4.6, GPT-OSS 120B",
					"buckets": [
						{"window": "5h", "remainingFraction": 1, "resetTime": "2026-10-09T17:00:00Z"},
						{"window": "weekly", "resetTime": "2026-10-12T00:00:00Z"}]}
			]}`,
			models: agModels,
			want: []quotaWindow{
				{Name: "Gemini models: 5h", Remaining: 0.8, ResetAt: mustInstant(t, "2026-10-09T15:00:00.123456789Z"), Period: 5 * time.Hour, Models: gemini},
				{Name: "Gemini models: weekly", Remaining: 0.5, ResetAt: mustInstant(t, "2026-10-14T00:00:00Z"), Period: quotaTestWeek, Models: gemini},
				{Name: "Claude and GPT models: 5h", Remaining: 1, ResetAt: mustInstant(t, "2026-10-09T17:00:00Z"), Period: 5 * time.Hour, Models: claudeGPT},
				{Name: "Claude and GPT models: weekly", Remaining: 0, ResetAt: mustInstant(t, "2026-10-12T00:00:00Z"), Period: quotaTestWeek, Models: claudeGPT},
			},
		},
		{
			name:   "group without served models dropped",
			body:   `{"groups": [{"displayName": "Gemini models", "buckets": [{"window": "weekly", "remainingFraction": 0.2, "resetTime": "2026-10-14T00:00:00Z"}]}, {"displayName": "Claude and GPT models", "buckets": [{"window": "weekly", "remainingFraction": 0.9, "resetTime": "2026-10-14T00:00:00Z"}]}]}`,
			models: gemini,
			want: []quotaWindow{
				{Name: "Gemini models: weekly", Remaining: 0.2, ResetAt: mustInstant(t, "2026-10-14T00:00:00Z"), Period: quotaTestWeek, Models: gemini},
			},
		},
		{name: "unknown group", body: `{"groups": [{"displayName": "Image models", "buckets": []}]}`, models: agModels, wantErr: `unknown quota group "Image models"`},
		{name: "unknown window", body: `{"groups": [{"displayName": "Gemini models", "buckets": [{"window": "fortnight", "remainingFraction": 1}]}]}`, models: agModels, wantErr: `unknown bucket window "fortnight"`},
		{name: "no groups", body: `{}`, models: agModels, wantErr: "no buckets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errParse := parseAntigravityQuota([]byte(tt.body), tt.models)
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parseAntigravityQuota: %v", errParse)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestQuotaParseKimiUsage(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []quotaWindow
		wantErr string
	}{
		{
			name: "5h limit and weekly summary",
			body: `{
				"usage": {"used": "1", "limit": "100", "remaining": "99", "resetTime": "2026-10-13T13:59:23.136523Z"},
				"limits": [{"detail": {"used": "2", "limit": "100", "remaining": "98", "resetTime": "2026-10-09T14:59:23.136523Z"}, "window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}}]
			}`,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.98, ResetAt: mustInstant(t, "2026-10-09T14:59:23.136523Z"), Period: 5 * time.Hour},
				{Name: "weekly", Remaining: 0.99, ResetAt: mustInstant(t, "2026-10-13T13:59:23.136523Z"), Period: quotaTestWeek},
			},
		},
		{
			name: "monthly ratio, used without remaining, relative reset",
			body: `{
				"limits": [{"detail": {"limit": 100, "used": 25, "reset_in": 7200}, "window": {"duration": 5, "timeUnit": "TIME_UNIT_HOUR"}}],
				"usages": {"limit_month_total": {"used_ratio": 0.2529, "reset_time": "2026-10-22T00:00:00Z"}}
			}`,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.75, ResetAt: quotaTestNow.Add(2 * time.Hour), Period: 5 * time.Hour},
				{Name: "monthly", Remaining: 0.7471, ResetAt: mustInstant(t, "2026-10-22T00:00:00Z"), Period: 30 * 24 * time.Hour},
			},
		},
		{name: "limit without window", body: `{"limits": [{"detail": {"limit": 100, "used": 1}}]}`, wantErr: "limits[0].window.duration missing"},
		{name: "unknown time unit", body: `{"limits": [{"detail": {"limit": 100, "used": 1}, "window": {"duration": 1, "timeUnit": "TIME_UNIT_FORTNIGHT"}}]}`, wantErr: "unknown timeUnit"},
		{name: "zero limit", body: `{"usage": {"limit": "0", "used": "0"}}`, wantErr: "usage.limit missing or not positive"},
		{name: "non numeric", body: `{"usage": {"limit": "lots", "used": "0"}}`, wantErr: "kimi: decode usages"},
		{name: "empty", body: `{"limits": []}`, wantErr: "no windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errParse := parseKimiUsage([]byte(tt.body), quotaTestNow)
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parseKimiUsage: %v", errParse)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestQuotaParseXaiBilling(t *testing.T) {
	const weeklyPeriod = `"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "start": "2026-10-03T13:32:42.093205+00:00", "end": "2026-10-10T13:32:42.093205+00:00"},
		"billingPeriodStart": "2026-10-03T13:32:42.093205+00:00", "billingPeriodEnd": "2026-10-10T13:32:42.093205+00:00", "onDemandCap": {"val": 0}`
	const zeroMonthly = `{"config": {"monthlyLimit": {"val": 0}, "used": {"val": 0}, "onDemandCap": {"val": 0}, "billingPeriodStart": "2026-10-01T00:00:00+00:00", "billingPeriodEnd": "2026-11-01T00:00:00+00:00"}}`
	tests := []struct {
		name    string
		weekly  string
		monthly string
		want    []quotaWindow
		wantErr string
	}{
		{
			name:    "weekly credits only",
			weekly:  `{"config": {` + weeklyPeriod + `, "creditUsagePercent": 37.5, "productUsage": [{"product": "Grok", "usagePercent": 37.5}]}}`,
			monthly: zeroMonthly,
			want: []quotaWindow{
				{Name: "weekly credits", Remaining: 0.625, ResetAt: mustInstant(t, "2026-10-10T13:32:42.093205+00:00"), Period: quotaTestWeek},
			},
		},
		{
			name:    "monthly spend with string cents, weekly unavailable",
			weekly:  `{"config": {` + weeklyPeriod + `}}`,
			monthly: `{"config": {"monthlyLimit": {"val": "5000"}, "used": {"val": "6000"}, "billingPeriodStart": "2026-10-01T00:00:00+00:00", "billingPeriodEnd": "2026-10-31T00:00:00+00:00"}}`,
			want: []quotaWindow{
				{Name: "monthly", Remaining: 0, ResetAt: mustInstant(t, "2026-10-31T00:00:00Z"), Period: 30 * 24 * time.Hour},
			},
		},
		{name: "usage unavailable", weekly: `{"config": {` + weeklyPeriod + `}}`, monthly: zeroMonthly, wantErr: "xai: usage unavailable"},
		{name: "weekly config missing", weekly: `{}`, monthly: zeroMonthly, wantErr: "weekly billing config missing"},
		{name: "weekly without period end", weekly: `{"config": {"creditUsagePercent": 10}}`, monthly: zeroMonthly, wantErr: "weekly billing period end missing"},
		{name: "money without val", weekly: `{"config": {}}`, monthly: `{"config": {"monthlyLimit": {}}}`, wantErr: "money value without val"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errParse := parseXaiBilling([]byte(tt.weekly), []byte(tt.monthly))
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parseXaiBilling: %v", errParse)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestQuotaParsePluginQuota(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     []quotaWindow
		wantPlan string
		wantErr  string
	}{
		{
			name: "opencode-go single group",
			body: `{"subscription": {"plan": "go"}, "groups": [{"displayName": "OpenCode Go Shared Allowance (Tracked by CPA)", "buckets": [
				{"window": "5h", "remainingFraction": 0.9, "resetTime": "2026-10-09T16:00:00Z", "description": "$1.2000 / $12.00 allowance used (90.0% remaining)"},
				{"window": "weekly", "remainingFraction": 0.5, "resetTime": "2026-10-14T00:00:00Z"},
				{"window": "monthly", "remainingFraction": 0.25, "resetTime": "2026-10-30T00:00:00Z"}]}]}`,
			want: []quotaWindow{
				{Name: "5h", Remaining: 0.9, ResetAt: mustInstant(t, "2026-10-09T16:00:00Z"), Period: 5 * time.Hour},
				{Name: "weekly", Remaining: 0.5, ResetAt: mustInstant(t, "2026-10-14T00:00:00Z"), Period: quotaTestWeek},
				{Name: "monthly", Remaining: 0.25, ResetAt: mustInstant(t, "2026-10-30T00:00:00Z"), Period: 30 * 24 * time.Hour},
			},
			wantPlan: "go",
		},
		{
			name: "several groups are named, reset is optional",
			body: `{"groups": [{"displayName": "A", "buckets": [{"window": "daily", "remainingFraction": 1}]}, {"displayName": "B", "buckets": [{"window": "weekly", "remainingFraction": 0}]}]}`,
			want: []quotaWindow{
				{Name: "A: daily", Remaining: 1, Period: 24 * time.Hour},
				{Name: "B: weekly", Remaining: 0, Period: quotaTestWeek},
			},
		},
		{name: "missing remainingFraction", body: `{"groups": [{"displayName": "A", "buckets": [{"window": "5h"}]}]}`, wantErr: "remainingFraction missing"},
		{name: "no buckets", body: `{"summary": [{"key": "balance", "label": "Balance", "value": 3}]}`, wantErr: "no buckets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, plan, errParse := parsePluginQuota("opencode-go", []byte(tt.body))
			if tt.wantErr != "" {
				assertErrorContains(t, errParse, tt.wantErr)
				return
			}
			if errParse != nil {
				t.Fatalf("parsePluginQuota: %v", errParse)
			}
			if plan != tt.wantPlan {
				t.Errorf("plan = %q, want %q", plan, tt.wantPlan)
			}
			assertWindows(t, got, tt.want)
		})
	}
}

func TestRejectedManagementKeyStopsCalls(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	origURL := managementBaseURL
	managementBaseURL = server.URL
	t.Cleanup(func() {
		managementBaseURL = origURL
		managementBlocked.Store(nil)
	})
	creds := []hostAuth{{ID: "block-a", AuthIndex: "1", Provider: "claude"}, {ID: "block-b", AuthIndex: "2", Provider: "claude"}}

	refreshQuotaNow("bad", creds)
	refreshQuotaNow("bad", creds)
	if got := hits.Load(); got != 1 {
		t.Fatalf("management hits with a rejected key = %d, want 1", got)
	}
	if snapshot, _ := quotaFor("block-a"); !strings.Contains(snapshot.Error, "invalid management key") {
		t.Fatalf("snapshot error = %q, want rejection reason", snapshot.Error)
	}
	if status := managementStatus("bad", time.Now()); !strings.Contains(status, "rejected") {
		t.Fatalf("managementStatus = %q, want rejected", status)
	}
	if status := managementStatus("good", time.Now()); status != "" {
		t.Fatalf("managementStatus for a new key = %q, want usable", status)
	}
	if status := managementStatus("", time.Now()); !strings.Contains(status, "not set") {
		t.Fatalf("managementStatus without key = %q, want not set", status)
	}
}
