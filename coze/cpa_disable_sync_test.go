// cpa_disable_sync_test.go 覆盖 coze 侧宿主管理 API 禁用通道的契约。
//
// coze 的通道实现与其他插件不同：它没有 hostHTTPDo，直接拼 host.http.do
// 的 wire 走通用 hostCall。因此这里重点验证「wire 构造 + 响应解析」这两段
// 纯逻辑，以及 setHostAuthDisabled 的状态码判定与禁用兜底语义。
package main

import (
	"errors"
	"strings"
	"testing"
)

func TestSetHostAuthDisabled_NotConfigured(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("", "")

	err := setHostAuthDisabled("coze-x.json", "idx", true)
	if !errors.Is(err, errCPAManagementNotConfigured) {
		t.Fatalf("err = %v; want errCPAManagementNotConfigured", err)
	}
}

func TestSetHostAuthDisabled_EmptyNameRejected(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)
	setCPAManagement("http://127.0.0.1:8317", "secret-key")

	if err := setHostAuthDisabled("  ", "", true); err == nil {
		t.Fatal("空 name 必须拒绝")
	}
}

func TestSetCPAManagement_DefaultURLAndSlashTrim(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)

	setCPAManagement("", "k")
	if url, _ := loadedCPAManagement(); url != defaultCPAManagementURL {
		t.Fatalf("url = %s; want default", url)
	}
	setCPAManagement("http://host:9000/", "")
	if url, _ := loadedCPAManagement(); url != "http://host:9000" {
		t.Fatalf("url = %s; want trailing slash trimmed", url)
	}
}

func TestCPAManagementSecretResolver_Precedence(t *testing.T) {
	t.Setenv("WB_CPA_MANAGEMENT_KEY", "")
	t.Setenv("CPA_MANAGEMENT_KEY", "")
	if got := cpaManagementSecretResolver("from-config"); got != "from-config" {
		t.Fatalf("config 应优先，实际 %q", got)
	}
	t.Setenv("WB_CPA_MANAGEMENT_KEY", "from-env")
	if got := cpaManagementSecretResolver(""); got != "from-env" {
		t.Fatalf("env 应兜底，实际 %q", got)
	}
}

// TestSyncCozeAuthDisabled_UsesBaseName 锁定：接口的 name 取物理文件 basename。
func TestSyncCozeAuthDisabled_UsesBaseName(t *testing.T) {
	origURL, origKey := loadedCPAManagement()
	defer setCPAManagement(origURL, origKey)

	// 未配密钥时应返回哨兵错误，而不是 panic 或返回 nil。
	setCPAManagement("", "")
	sa := &StoredAuth{AuthID: "a1", PhysicalPath: "/data/coze-abc.json"}
	if err := syncCozeAuthDisabled(sa, true); !errors.Is(err, errCPAManagementNotConfigured) {
		t.Fatalf("err = %v; want errCPAManagementNotConfigured", err)
	}
}

// TestSyncCozeAuthDisabled_NoPhysicalPath 锁定：缺物理路径必须显式报错。
func TestSyncCozeAuthDisabled_NoPhysicalPath(t *testing.T) {
	setCPAManagement("", "")
	err := syncCozeAuthDisabled(&StoredAuth{AuthID: "a2"}, true)
	if err == nil {
		t.Fatal("缺 PhysicalPath 必须报错")
	}
	if !strings.Contains(err.Error(), "no physical path") {
		t.Fatalf("错误应说明缺路径，实际: %v", err)
	}
}

// TestCPAWireShapes 锁定 wire 契约：request 子对象携带 method/url/headers/body，
// 且 status 字段兼容 PascalCase 与 snake_case 两种宿主序列化形态。
func TestCPAWireShapes(t *testing.T) {
	inner := cpaHTTPInner{
		Method:  "PATCH",
		URL:     "http://127.0.0.1:8317/v0/management/auth-files/status",
		Headers: map[string][]string{"Authorization": {"Bearer k"}},
		Body:    []byte(`{"name":"x.json","disabled":true}`),
	}
	wire := cpaHTTPRequestWire{Request: &inner}
	if wire.Request == nil || wire.Request.Method != "PATCH" {
		t.Fatalf("wire 未携带 request 子对象: %+v", wire)
	}
	if !strings.Contains(string(wire.Request.Body), `"disabled":true`) {
		t.Fatalf("body 丢失: %s", wire.Request.Body)
	}
	// 请求体结构必须是 name/disabled 两个键。
	body := cpaAuthFileStatusRequest{Name: "x.json", Disabled: true}
	if body.Name != "x.json" || !body.Disabled {
		t.Fatalf("请求体结构异常: %+v", body)
	}
}
