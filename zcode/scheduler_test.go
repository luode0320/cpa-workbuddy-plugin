package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScheduler_RoutableAndPick(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_sched_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	// 账号 1: 正常
	sa1 := &StoredAuth{
		AuthID:     "zcode-acc1",
		APIKey:     "key1.secret",
		Disabled:   false,
		TestFailed: false,
	}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-acc1.json"), sa1)

	// 账号 2: 禁用
	sa2 := &StoredAuth{
		AuthID:     "zcode-acc2",
		APIKey:     "key2.secret",
		Disabled:   true,
		TestFailed: false,
	}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-acc2.json"), sa2)

	// 账号 3: 测试失败
	sa3 := &StoredAuth{
		AuthID:     "zcode-acc3",
		APIKey:     "key3.secret",
		Disabled:   false,
		TestFailed: true,
	}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-acc3.json"), sa3)

	if !accountRoutable(sa1) {
		t.Fatalf("sa1 should be routable")
	}
	if accountRoutable(sa2) {
		t.Fatalf("sa2 (disabled) should not be routable")
	}
	if accountRoutable(sa3) {
		t.Fatalf("sa3 (test failed) should not be routable")
	}

	picked := pickActiveAuth()
	if picked == nil || picked.AuthID != "zcode-acc1" {
		t.Fatalf("expected sa1 to be picked, got %+v", picked)
	}

	// 测试冷却状态排除
	noteAccountFailure(sa1.AuthID, 429, "rate limited")
	if accountRoutable(sa1) {
		t.Fatalf("sa1 should be cooling down after failure")
	}
	pickedCooling := pickActiveAuth()
	if pickedCooling != nil {
		t.Fatalf("expected no active account when cooling down, got %+v", pickedCooling)
	}

	// 成功重置
	resetAccountFailover(sa1.AuthID)
	if !accountRoutable(sa1) {
		t.Fatalf("sa1 should be routable after reset")
	}
}

func TestScheduler_PickNextAuth(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "zcode_next_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	setAccountsDir(tempDir)

	sa1 := &StoredAuth{AuthID: "zcode-acc1", APIKey: "key1.secret", Disabled: false}
	sa2 := &StoredAuth{AuthID: "zcode-acc2", APIKey: "key2.secret", Disabled: false}
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-acc1.json"), sa1)
	_ = writeAuthFileDirect(filepath.Join(tempDir, "zcode-acc2.json"), sa2)

	next, ok := pickNextAuth("zcode-acc1")
	if !ok || next == nil || next.AuthID != "zcode-acc2" {
		t.Fatalf("expected zcode-acc2, got %+v, ok=%v", next, ok)
	}
}

func TestFailover_ShouldRotate(t *testing.T) {
	if !shouldRotateOnUpstreamErr(401, "unauthorized") {
		t.Fatalf("401 should rotate")
	}
	if !shouldRotateOnUpstreamErr(429, "rate limit exceeded") {
		t.Fatalf("429 should rotate")
	}
	if shouldRotateOnUpstreamErr(500, "internal server error") {
		t.Fatalf("plain 500 should not rotate")
	}
}
