package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeRefreshToken 构造带 device_id 的 JWT 形 refresh_token 测试夹具。
// [参数] deviceID: 写入载荷的设备标识。
// [返回] 三段式 JWT 字符串。
// 最近修改时间：2026-11-28 新增，上游要求 X-Device-Id 与载荷一致，夹具需可解析。
func fakeRefreshToken(deviceID string) string {
	payload, _ := json.Marshal(map[string]string{"device_id": deviceID, "type": "refresh"})
	return "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// TestRequestTokenMock 验证 mock 刷新端点返回的 access_token 被正确解析，
// 并断言 X-Device-Id 取自令牌载荷而非随机值。
func TestRequestTokenMock(t *testing.T) {
	refresh := fakeRefreshToken("test-device")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+refresh {
			t.Errorf("missing bearer token")
		}
		if r.Header.Get("X-Sign") == "" || r.Header.Get("X-Timestamp") == "" {
			t.Errorf("missing sign headers")
		}
		if got := r.Header.Get("X-Device-Id"); got != "test-device" {
			t.Errorf("X-Device-Id must come from token payload, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"access_token": "mock-access", "refresh_token": "mock-refresh"},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.RefreshEndpoint = srv.URL
	got, err := requestToken(refresh, cfg)
	if err != nil {
		t.Fatalf("requestToken failed: %v", err)
	}
	if got.AccessToken != "mock-access" || got.RefreshToken != "mock-refresh" {
		t.Fatalf("unexpected token result: %+v", got)
	}
}

// TestAcquireTokenCacheExpiry 验证缓存命中与过期后刷新。
func TestAcquireTokenCacheExpiry(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"access_token": "access-" + string(rune('0'+n)), "refresh_token": "r"},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.RefreshEndpoint = srv.URL
	key := fakeRefreshToken("expiry-device")
	invalidateToken(key)

	first, err := acquireToken(key, cfg)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	second, err := acquireToken(key, cfg)
	if err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	if first != second {
		t.Fatal("cache hit must return same token")
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("expected 1 refresh, got %d", calls)
	}
	mu.Unlock()

	globalTokenCache.mu.Lock()
	globalTokenCache.entries[key].RefreshTime = time.Now().Add(-time.Second)
	globalTokenCache.mu.Unlock()
	third, err := acquireToken(key, cfg)
	if err != nil {
		t.Fatalf("third acquire failed: %v", err)
	}
	if third == first {
		t.Fatal("expired cache must trigger refresh")
	}
}

// TestAcquireTokenConcurrentSingleRefresh 验证并发请求只刷新一次。
func TestAcquireTokenConcurrentSingleRefresh(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"access_token": "shared-access", "refresh_token": "r"},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.RefreshEndpoint = srv.URL
	key := fakeRefreshToken("concurrent-device")
	invalidateToken(key)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := acquireToken(key, cfg)
			if err != nil {
				errs <- err
				return
			}
			if tok != "shared-access" {
				errs <- fmt.Errorf("unexpected token: %s", tok)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent acquire error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 refresh, got %d", calls)
	}
}

// TestDeviceIDFromRefreshToken 验证从令牌载荷读取 device_id 的四种边界。
func TestDeviceIDFromRefreshToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"合法载荷", fakeRefreshToken("dev-abc"), "dev-abc"},
		{"非三段式", "not-a-jwt", ""},
		{"载荷无 device_id", "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"type":"refresh"}`)) + ".sig", ""},
		{"载荷非法 JSON", "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte("{{{")) + ".sig", ""},
	}
	for _, tc := range cases {
		if got := deviceIDFromRefreshToken(tc.token); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestRequestTokenRejectsMissingDeviceID 验证无 device_id 的令牌在请求上游前即被拒。
func TestRequestTokenRejectsMissingDeviceID(t *testing.T) {
	cfg := defaultConfig()
	cfg.RefreshEndpoint = "http://127.0.0.1:1/unreachable"
	if _, err := requestToken("not-a-jwt", cfg); err == nil {
		t.Fatal("expected error for token without device_id")
	}
}
