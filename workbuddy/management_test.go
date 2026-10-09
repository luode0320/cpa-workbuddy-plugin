package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestManagementRegistrationModelsRoute 验证面板「测试」弹窗依赖的只读 /models
// 路由已注册为 GET，且不会被误列为需要 management key 的写路径。
func TestManagementRegistrationModelsRoute(t *testing.T) {
	reg := managementRegistration()
	found := false
	for _, r := range reg.Routes {
		if strings.HasSuffix(r.Path, "/models") {
			found = true
			if r.Method != http.MethodGet {
				t.Fatalf("/models route method = %s, want GET", r.Method)
			}
		}
	}
	if !found {
		t.Fatal("/models route missing from managementRegistration().Routes")
	}
	base := loadedManagementBasePath() + "/plugins/" + providerName
	if mutatingManagementPath(base + "/models") {
		t.Fatal("/models must stay a read-only path (not in mutatingManagementPath)")
	}
}

// TestHandleModelsQueryRequiresAuthIndex 验证缺少 auth_index 时返回结构化错误。
func TestHandleModelsQueryRequiresAuthIndex(t *testing.T) {
	res := handleModelsQuery(pluginapi.ManagementRequest{})
	if res["error"] == nil {
		t.Fatalf("expected error when auth_index missing, got %v", res)
	}
}
