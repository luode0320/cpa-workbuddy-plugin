package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// hostSchemaVersionUpperBound 是生产宿主实际声明的契约上限（宿主 sdk/pluginabi.SchemaVersion = 6）。
// 宿主 registerRPCPlugin 对 resp.SchemaVersion > 该值直接拒绝注册并报
// "plugin schema version %d is not supported"，因此声明值一旦超过它插件整体不可用。
const hostSchemaVersionUpperBound uint32 = 6

// TestPluginRegistrationSchemaVersionWithinHostContract 校验注册包声明的 schema 版本与常量一致且不超宿主上限。
func TestPluginRegistrationSchemaVersionWithinHostContract(t *testing.T) {
	reg := pluginRegistration()
	if reg.SchemaVersion != schemaVersion {
		t.Fatalf("registration schema %d != declared %d", reg.SchemaVersion, schemaVersion)
	}
	if reg.SchemaVersion > hostSchemaVersionUpperBound {
		t.Fatalf("declared schema %d exceeds host contract %d", reg.SchemaVersion, hostSchemaVersionUpperBound)
	}
}

// snapshotConfig 保存当前生效配置并在用例结束时恢复，避免配置类用例互相污染。
func snapshotConfig(t *testing.T) {
	t.Helper()
	saved := currentConfig()
	t.Cleanup(func() { activeConfig.Store(saved) })
}

// TestConfigurePluginAppliesValidYAML 校验热更新路径真的把 YAML 落到了生效配置上。
// 宿主每次下发 plugin.reconfigure 都走这里，此前该函数没有任何用例。
func TestConfigurePluginAppliesValidYAML(t *testing.T) {
	snapshotConfig(t)
	raw, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(`
idle_minutes: 7
stream_prefetch_bytes: 64
groups:
  - id: "g1"
    name: "Group 1"
    strategy: "random"
    models: ["m1", "m2"]
features:
  fallback_on_refusal: false
  notice_prefix: "[x] "
`)})
	if errMarshal != nil {
		t.Fatalf("marshal lifecycle request: %v", errMarshal)
	}
	if err := configurePlugin(raw); err != nil {
		t.Fatalf("configurePlugin: %v", err)
	}
	cfg := currentConfig()
	if cfg.IdleMinutes != 7 || cfg.StreamPrefetchBytes != 64 {
		t.Fatalf("idle/prefetch = %d/%d, want 7/64", cfg.IdleMinutes, cfg.StreamPrefetchBytes)
	}
	if len(cfg.Groups) != 1 || cfg.Groups[0].Strategy != "random" || len(cfg.Groups[0].Models) != 2 {
		t.Fatalf("groups = %+v, want one random group with two models", cfg.Groups)
	}
	if cfg.Features.FallbackOnRefusal {
		t.Fatal("fallback_on_refusal = true, want false from YAML")
	}
	if got := cfg.Features.effectiveNoticePrefix(); got != "[x] " {
		t.Fatalf("notice prefix = %q, want %q", got, "[x] ")
	}
}

// TestConfigurePluginEmptyPayloadUsesDefaults 校验空下发回落到默认配置而不是零值。
func TestConfigurePluginEmptyPayloadUsesDefaults(t *testing.T) {
	snapshotConfig(t)
	if err := configurePlugin(nil); err != nil {
		t.Fatalf("configurePlugin(nil): %v", err)
	}
	cfg := currentConfig()
	defaults := defaultPluginConfig()
	if cfg.IdleMinutes != defaults.IdleMinutes || cfg.StreamPrefetchBytes != defaults.StreamPrefetchBytes {
		t.Fatalf("idle/prefetch = %d/%d, want defaults %d/%d",
			cfg.IdleMinutes, cfg.StreamPrefetchBytes, defaults.IdleMinutes, defaults.StreamPrefetchBytes)
	}
	if cfg.Features.FallbackOnRefusal != defaults.Features.FallbackOnRefusal {
		t.Fatalf("features not defaulted: %+v", cfg.Features)
	}
}

// TestConfigurePluginRejectsInvalidConfig 校验每一类非法配置都被挡住，且失败时生效配置不变。
func TestConfigurePluginRejectsInvalidConfig(t *testing.T) {
	snapshotConfig(t)
	before := currentConfig()
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"负 idle_minutes", "idle_minutes: -1\n", "idle_minutes must be non-negative"},
		{"prefetch 为零", "stream_prefetch_bytes: 0\n", "stream_prefetch_bytes must be at least 1"},
		{"分组缺 id", "groups:\n  - name: n\n    models: [m1]\n", "needs an id"},
		{"分组 id 重复", "groups:\n  - {id: g, name: n, models: [m1]}\n  - {id: g, name: n2, models: [m2]}\n", "used twice"},
		{"分组缺 name", "groups:\n  - {id: g, models: [m1]}\n", "needs a name"},
		{"分组无模型", "groups:\n  - {id: g, name: n}\n", "needs at least one model"},
		{"未知策略", "groups:\n  - {id: g, name: n, strategy: bogus, models: [m1]}\n", "unknown strategy"},
		{"分组嵌套", "groups:\n  - {id: g1, name: n1, models: [g2]}\n  - {id: g2, name: n2, models: [m1]}\n", "cannot nest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(tc.yaml)})
			if errMarshal != nil {
				t.Fatalf("marshal: %v", errMarshal)
			}
			errConfig := configurePlugin(raw)
			if errConfig == nil {
				t.Fatalf("configurePlugin accepted invalid config %q", tc.yaml)
			}
			if !strings.Contains(errConfig.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", errConfig.Error(), tc.want)
			}
			if after := currentConfig(); after.IdleMinutes != before.IdleMinutes ||
				after.StreamPrefetchBytes != before.StreamPrefetchBytes ||
				len(after.Groups) != len(before.Groups) {
				t.Fatalf("config mutated on rejected input: %+v", after)
			}
		})
	}
}

// TestConfigurePluginRejectsMalformedEnvelope 校验外层信封不是合法 JSON 时直接报错。
func TestConfigurePluginRejectsMalformedEnvelope(t *testing.T) {
	snapshotConfig(t)
	if err := configurePlugin([]byte("not json")); err == nil {
		t.Fatal("configurePlugin accepted a malformed envelope")
	}
}
