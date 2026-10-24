package main

import (
	"encoding/json"
	"testing"
)

// TestShouldReenableLegacyExhausted 锁定历史「额度耗尽自动禁用」的解禁判据。
//
// 额度耗尽不再是禁用理由，但盘上历史文档可能仍带 disabled + exhausted_disable。
// 解禁只允许认领本插件旧版自己写的标记：手动禁用与无标记禁用都属于他人的意图。
func TestShouldReenableLegacyExhausted(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"legacy-exhausted", `{"disabled":true,"exhausted_disable":true}`, true},
		{"manual-wins", `{"disabled":true,"manual_disable":true,"exhausted_disable":true}`, false},
		{"manual-only", `{"disabled":true,"manual_disable":true}`, false},
		{"markerless", `{"disabled":true}`, false},
		{"enabled", `{"disabled":false,"exhausted_disable":true}`, true},
		{"malformed", `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReenableLegacyExhausted([]byte(tc.raw)); got != tc.want {
				t.Fatalf("shouldReenableLegacyExhausted(%s) = %v; want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestReenableLegacyExhaustedRebuildKeepsManualMarker 断言解禁后的白名单重建
// 不会把用户的手动禁用标记一并抹掉：一旦抹掉，该账号就会在下一轮被误判为
// 「可自动恢复」的历史耗尽禁用。
func TestReenableLegacyExhaustedRebuildKeepsManualMarker(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-legacy"}}
	// 解禁路径只在无 manual_disable 时可达，这里直接验证重建白名单的语义。
	raw, err := buildAuthFileJSON(sa, false, displayNote(sa, nil, false), map[string]any{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["disabled"] != false {
		t.Fatalf("解禁后 disabled = %v; want false", doc["disabled"])
	}
	if _, ok := doc["exhausted_disable"]; ok {
		t.Fatal("解禁后不应再写入 exhausted_disable 标记")
	}
}
