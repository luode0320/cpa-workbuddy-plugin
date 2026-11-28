package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchCreditsParsesMemberInfo 验证 user/info 的 member_info 被正确解析成积分快照，
// 并断言请求头带上了取自令牌载荷的 X-Device-Id 与签名。
func TestFetchCreditsParsesMemberInfo(t *testing.T) {
	access := fakeToken("credit-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+access {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Device-Id"); got != "credit-device" {
			t.Errorf("X-Device-Id must come from token payload, got %q", got)
		}
		if r.Header.Get("X-Sign") == "" || r.Header.Get("X-Timestamp") == "" {
			t.Errorf("missing sign headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 0,
			"result": map[string]any{
				"member_info": map[string]any{
					"member_status":       1,
					"left_score":          "199.01",
					"token_left_percent":  "99%",
					"token_usage_percent": "1%",
					"score_rule":          "免费用户，登录赠送200积分/天",
				},
			},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.UserInfoEndpoint = srv.URL
	got, err := fetchCredits(access, cfg)
	if err != nil {
		t.Fatalf("fetchCredits failed: %v", err)
	}
	if got.LeftScore != "199.01" {
		t.Fatalf("LeftScore = %q, want 199.01", got.LeftScore)
	}
	if got.TokenLeftPercent != "99%" || got.TokenUsagePercent != "1%" {
		t.Fatalf("percent mismatch: left=%q usage=%q", got.TokenLeftPercent, got.TokenUsagePercent)
	}
	if got.MemberStatus != 1 {
		t.Fatalf("MemberStatus = %d, want 1", got.MemberStatus)
	}
	if got.ScoreRule == "" {
		t.Fatal("ScoreRule must be preserved")
	}
	if got.FetchedAt == "" {
		t.Fatal("FetchedAt must be stamped")
	}
}

// TestFetchCreditsRejectsTokenWithoutDeviceID 验证缺 device_id 的令牌在发请求前即被拒。
func TestFetchCreditsRejectsTokenWithoutDeviceID(t *testing.T) {
	cfg := defaultConfig()
	cfg.UserInfoEndpoint = "http://127.0.0.1:1/unreachable"
	if _, err := fetchCredits("not-a-jwt", cfg); err == nil {
		t.Fatal("expected error for access token without device_id")
	}
}

// TestFetchCreditsUpstreamError 验证非 200 响应被转成带状态码的错误。
func TestFetchCreditsUpstreamError(t *testing.T) {
	access := fakeToken("credit-device", "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":40101}`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.UserInfoEndpoint = srv.URL
	if _, err := fetchCredits(access, cfg); err == nil {
		t.Fatal("expected error for non-200 upstream")
	}
}
