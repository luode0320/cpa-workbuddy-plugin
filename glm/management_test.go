package main

import (
	"net/url"
	"testing"
)

// TestMergeRequestQueryPrefersHostQuery 验证查询参数合并：
// 宿主单独下发的 req.Query 覆盖路径查询串，路径查询串在宿主缺省时仍然可用，
// 两者并存时非冲突键都保留。
func TestMergeRequestQueryPrefersHostQuery(t *testing.T) {
	pathOnly := url.Values{"auth_id": {"from-path"}}
	if got := mergeRequestQuery(pathOnly, nil); got.Get("auth_id") != "from-path" {
		t.Fatalf("path query must survive when host query is empty, got %q", got.Get("auth_id"))
	}

	hostQuery := map[string][]string{"auth_id": {"from-host"}}
	if got := mergeRequestQuery(pathOnly, hostQuery); got.Get("auth_id") != "from-host" {
		t.Fatalf("host query must win, got %q", got.Get("auth_id"))
	}

	both := mergeRequestQuery(
		url.Values{"auth_id": {"a"}, "keep": {"1"}},
		map[string][]string{"auth_id": {"b"}},
	)
	if both.Get("auth_id") != "b" {
		t.Fatalf("conflicting key must take host value, got %q", both.Get("auth_id"))
	}
	if both.Get("keep") != "1" {
		t.Fatalf("non-conflicting path key must be kept, got %q", both.Get("keep"))
	}
}

// TestNormalizeMgmtSubPathCredits 验证 /credits 在两种宿主前缀下都能归一化到同一子路径。
func TestNormalizeMgmtSubPathCredits(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"management 前缀", getManagementBasePath() + "/plugins/" + providerName + "/credits", "/credits"},
		{"短前缀", "/plugins/" + providerName + "/credits", "/credits"},
		{"resource 前缀", "/v0/resource/plugins/" + providerName + "/credits", "/credits"},
	}
	for _, tc := range cases {
		got, _ := normalizeMgmtSubPath(tc.path)
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
