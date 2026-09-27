package main

import (
	"errors"
	"testing"
	"time"
)

func TestParseTestFailedFromAuthJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"missing", `{"credential":"x"}`, false},
		{"false", `{"test_failed":false}`, false},
		{"true", `{"test_failed":true}`, true},
		{"malformed", `{`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTestFailedFromAuthJSON([]byte(tc.raw)); got != tc.want {
				t.Fatalf("parseTestFailedFromAuthJSON(%q)=%v; want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestActivePingTestFailedTag(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	origPingFn := sendActivePingTraeFn
	defer func() { sendActivePingTraeFn = origPingFn }()

	sa := &traeAuth{UserID: "test-failed-tag-uid"}
	storeTraeDynamicModels(nil)
	accountCache.Store("test-failed-tag-auth", &accountCacheEntry{credits: &traeCredits{TotalRemain: 5}, updated: time.Now()})
	defer accountCache.Delete("test-failed-tag-auth")

	// 1. 定时 ping 失败 + 积分 > 0：走打标签分支（host 缺省时 idxErr 非 nil，
	//    不 panic 且错误原样透传）。
	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
		return errors.New("upstream ping failed")
	}
	err := triggerActivePing("idx-1", "test-failed-tag-auth", sa)
	if err == nil || err.Error() != "upstream ping failed" {
		t.Fatalf("expected ping error, got %v", err)
	}

	// 2. 定时 ping 成功：走清标签分支（host 缺省时同样只告警，不影响返回）。
	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
		return nil
	}
	if err := triggerActivePing("idx-1", "test-failed-tag-auth", sa); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestActivePingNoTagOnExhaustedCredits(t *testing.T) {
	resetActivePingTimes()
	defer resetActivePingTimes()

	origPingFn := sendActivePingTraeFn
	defer func() { sendActivePingTraeFn = origPingFn }()

	sa := &traeAuth{UserID: "exhausted-tag-uid"}
	storeTraeDynamicModels(nil)
	accountCache.Store("exhausted-tag-auth", &accountCacheEntry{credits: &traeCredits{TotalRemain: 0, TotalUsed: 10}, updated: time.Now()})
	defer accountCache.Delete("exhausted-tag-auth")

	sendActivePingTraeFn = func(sa *traeAuth, chosenModel string) error {
		return errors.New("upstream ping failed")
	}
	if err := triggerActivePing("idx-1", "exhausted-tag-auth", sa); err == nil {
		t.Fatal("expected ping error for exhausted account")
	}
}
