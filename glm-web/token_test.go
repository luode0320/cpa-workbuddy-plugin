package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestRequestTokenMock 验证 mock 刷新端点返回的 access_token 被正确解析。
func TestRequestTokenMock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-refresh" {
			t.Errorf("missing bearer token")
		}
		if r.Header.Get("X-Sign") == "" || r.Header.Get("X-Timestamp") == "" {
			t.Errorf("missing sign headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"access_token": "mock-access", "refresh_token": "mock-refresh"},
		})
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.RefreshEndpoint = srv.URL
	got, err := requestToken("test-refresh", cfg)
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
	key := "expiry-token"
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
	key := "concurrent-token"
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
