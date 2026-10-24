// failover_persist_verify_test.go 验证「失败禁用标记能否进入宿主持久化层」。
//
// 背景（2026-10-25 生产实证）：qoderwork 在生产写盘 27 次全部成功，但宿主
// auth_store 表中 failover_disable 账号数为 0，而 workbuddy 同机制有 2 个。
// 根因候选是：宿主以 auth_store 为真相源单向回写 spool，插件直写 spool 的
// 标记在下次回写时被覆盖；唯一能进入宿主的通道是 handleParseAuth 回传的
// AuthData.Disabled / Metadata。
//
// 本测试锁死该通道的输入侧契约：只要 auth.parse 回传的 Metadata 携带
// failover_disable 三键、且 Disabled 与盘上 disabled 一致，宿主侧的
// pluginAuthDataToCoreAuth（Disabled: data.Disabled）与 mergedStorageJSON
// （Metadata 覆盖写入文件）就能把标记持久化。
//
// 宿主侧行为已由 SDK 源码静态确认（v7.2.129）：
//   internal/pluginhost/auth_provider.go:579  Disabled: data.Disabled
//   internal/pluginhost/auth_provider.go:488  metadata 覆盖 rawJSON 同名字段
//   internal/store/postgresstore.go:252       Metadata["disabled"] 落库前重写
//   internal/store/postgresstore.go:538       写库内容直接读物理文件
package main

import (
	"encoding/json"
	"testing"
)

// failoverParseAuthFixture 构造一份带本机制禁用标记的物理 auth 文档。
// 字段形状与生产 spool 文件一致（嵌套 auth/account + 顶层标记）。
func failoverParseAuthFixture() []byte {
	doc := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     "https://example.invalid/logo.png",
		"note":     "CN · 积分未知",
		"disabled": true,
		"auth": map[string]any{
			"accessToken":  "dt-fixture-token",
			"refreshToken": "drt-fixture-token",
			"domain":       "qoder.com.cn",
			"expiresAt":    1795449712,
		},
		"account": map[string]any{
			"uid":      "019fd0a6-610b-7841-9bb2-9e3cd5cdb65b",
			"nickname": "ud5cdb65b",
		},
		failoverDisableKey:      true,
		failoverDisableModelKey: "kmodel_latest",
		failoverDisableAtKey:    "2026-10-25T01:14:39Z",
	}
	raw, _ := json.Marshal(doc)
	return raw
}

// TestParseAuthCarriesFailoverMarkers 锁死 auth.parse 回传必须携带本机制三键。
//
// 这是标记进入宿主的唯一通道：宿主 pluginAuthDataToCoreAuth 用 Metadata 构造
// coreauth.Auth，后续 mergedStorageJSON 把 Metadata 覆盖写进文件、postgresstore
// 再把文件内容整份 upsert 进 auth_store。少一个键，探活循环就找不到候选。
func TestParseAuthCarriesFailoverMarkers(t *testing.T) {
	raw := failoverParseAuthFixture()
	req, err := json.Marshal(map[string]any{
		"RawJSON":  raw,
		"FileName": "qoderwork-019fd0a6-610b-7841-9bb2-9e3cd5cdb65b.json",
		"Provider": providerName,
	})
	if err != nil {
		t.Fatalf("marshal parse request: %v", err)
	}

	resp, err := handleParseAuth(req)
	if err != nil {
		t.Fatalf("handleParseAuth: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(resp, &env); err != nil || !env.OK {
		t.Fatalf("parse envelope not ok: err=%v resp=%s", err, truncateRedacted(string(resp), 200))
	}
	var parsed struct {
		Handled bool `json:"Handled"`
		Auth    struct {
			Disabled bool           `json:"Disabled"`
			Metadata map[string]any `json:"Metadata"`
		} `json:"Auth"`
	}
	if err := json.Unmarshal(env.Result, &parsed); err != nil {
		t.Fatalf("decode parse result: %v", err)
	}
	if !parsed.Handled {
		t.Fatalf("parse not handled: %s", truncateRedacted(string(env.Result), 200))
	}

	// 1. Disabled 必须与盘上一致：宿主 pluginAuthDataToCoreAuth 直接取该值，
	//    它是路由判据（conductor_selection 多处 candidate.Disabled）。
	if !parsed.Auth.Disabled {
		t.Fatalf("Auth.Disabled = false, want true (宿主会当作启用账号继续路由)")
	}

	// 2. 三键必须原样出现在 Metadata 里：mergedStorageJSON 用 Metadata 覆盖写文件。
	meta := parsed.Auth.Metadata
	if v, ok := meta[failoverDisableKey].(bool); !ok || !v {
		t.Fatalf("metadata[%s] = %#v, want true", failoverDisableKey, meta[failoverDisableKey])
	}
	if got, _ := meta[failoverDisableModelKey].(string); got != "kmodel_latest" {
		t.Fatalf("metadata[%s] = %q, want kmodel_latest", failoverDisableModelKey, got)
	}
	if got, _ := meta[failoverDisableAtKey].(string); got == "" {
		t.Fatalf("metadata[%s] 为空，探活将无法判断禁用时刻", failoverDisableAtKey)
	}

	// 3. Metadata.disabled 必须为 true：postgresstore 落库前会用 auth.Disabled
	//    重写该键，但宿主内存态与文件内容都以 Metadata 为准，两者不得打架。
	if v, ok := meta["disabled"].(bool); !ok || !v {
		t.Fatalf("metadata[disabled] = %#v, want true", meta["disabled"])
	}
}

// TestParseAuthDropsFailoverMarkersWhenAbsent 反向守卫：盘上没有标记时不得凭空造。
//
// 若插件对未禁用账号也输出 failover_disable，宿主会把健康号判成待探活候选，
// 探活循环每 10 分钟对健康号打一次上游（Qoder 日配额宝贵），必须禁止。
func TestParseAuthDropsFailoverMarkersWhenAbsent(t *testing.T) {
	doc := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     "https://example.invalid/logo.png",
		"note":     "CN · 积分未知",
		"disabled": false,
		"auth": map[string]any{
			"accessToken":  "dt-fixture-token",
			"refreshToken": "drt-fixture-token",
			"domain":       "qoder.com.cn",
			"expiresAt":    1795449712,
		},
		"account": map[string]any{
			"uid":      "01a01535-c93d-7263-9548-7b53dbee4bb0",
			"nickname": "udbee4bb0",
		},
	}
	raw, _ := json.Marshal(doc)
	req, _ := json.Marshal(map[string]any{
		"RawJSON":  raw,
		"FileName": "qoderwork-01a01535-c93d-7263-9548-7b53dbee4bb0.json",
		"Provider": providerName,
	})

	resp, err := handleParseAuth(req)
	if err != nil {
		t.Fatalf("handleParseAuth: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(resp, &env); err != nil || !env.OK {
		t.Fatalf("parse envelope not ok: %v", err)
	}
	var parsed struct {
		Auth struct {
			Disabled bool           `json:"Disabled"`
			Metadata map[string]any `json:"Metadata"`
		} `json:"Auth"`
	}
	if err := json.Unmarshal(env.Result, &parsed); err != nil {
		t.Fatalf("decode parse result: %v", err)
	}

	if parsed.Auth.Disabled {
		t.Fatalf("Auth.Disabled = true, want false")
	}
	for _, key := range []string{failoverDisableKey, failoverDisableModelKey, failoverDisableAtKey} {
		if _, exists := parsed.Auth.Metadata[key]; exists {
			t.Fatalf("metadata 不应凭空出现 %s（会造成健康号被反复探活）", key)
		}
	}
}

// TestFailoverMarkersSurviveHostMetadataMerge 复刻宿主 mergedStorageJSON 语义。
//
// 宿主落盘路径（auth_provider.go:488-498）是「rawJSON 打底 + Metadata 整体覆盖」，
// 本测试用同一规则模拟一次回传落盘，确认三键在合并后仍然存在且值不变——
// 这是标记能进入 auth_store 的直接前提。
func TestFailoverMarkersSurviveHostMetadataMerge(t *testing.T) {
	storageJSON := failoverParseAuthFixture() // 插件 StorageJSON（不含 Metadata 三键时仍需存活）

	// 插件回传的 Metadata（模拟 handleParseAuth 的产出）。
	metadata := map[string]any{
		"type":                  providerName,
		"provider":              providerName,
		"disabled":              true,
		failoverDisableKey:      true,
		failoverDisableModelKey: "kmodel_latest",
		failoverDisableAtKey:    "2026-10-25T01:14:39Z",
	}

	// 复刻 mergedStorageJSON：rawJSON 打底，Metadata 逐键覆盖，最后强制 type。
	var merged map[string]any
	if err := json.Unmarshal(storageJSON, &merged); err != nil {
		t.Fatalf("decode storage json: %v", err)
	}
	for k, v := range metadata {
		merged[k] = v
	}
	merged["type"] = providerName

	if v, ok := merged[failoverDisableKey].(bool); !ok || !v {
		t.Fatalf("合并后 %s 丢失 = %#v", failoverDisableKey, merged[failoverDisableKey])
	}
	if v, ok := merged["disabled"].(bool); !ok || !v {
		t.Fatalf("合并后 disabled 丢失 = %#v", merged["disabled"])
	}
	if got, _ := merged[failoverDisableModelKey].(string); got != "kmodel_latest" {
		t.Fatalf("合并后探活模型 = %q, want kmodel_latest", got)
	}
}
