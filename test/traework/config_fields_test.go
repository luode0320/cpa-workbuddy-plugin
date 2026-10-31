package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 配置登记（2026-10-25）。
//
// 「解析了但没登记」是本仓库复发的缺陷形态：配置项在 applyConfigLines 里有 case，
// 但没进 ConfigFields，管理端就看不到、也改不了。本文件把两者绑死：
// 每个可解析的键都必须在 ConfigFields 里出现，且类型正确。

// registeredFieldTypes 把 ConfigFields 收成 name → type 的映射。
func registeredFieldTypes(t *testing.T) map[string]pluginapi.ConfigFieldType {
	t.Helper()
	reg := wbRegistration()
	out := make(map[string]pluginapi.ConfigFieldType, len(reg.Metadata.ConfigFields))
	for _, f := range reg.Metadata.ConfigFields {
		out[f.Name] = f.Type
	}
	return out
}

// configureYAML 用宿主真实线格式（config_yaml 走 []byte → base64）驱动 configure。
func configureYAML(t *testing.T, yaml string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configure(raw)
}

// TestConfigFields_AllParseableKeysRegistered 锁定「解析即登记」。
func TestConfigFields_AllParseableKeysRegistered(t *testing.T) {
	fields := registeredFieldTypes(t)

	// key → 期望类型。新增配置键时必须同步加到这里。
	want := map[string]pluginapi.ConfigFieldType{
		// 原有已登记键（抽样，防回退）。
		"api_host":        pluginapi.ConfigFieldTypeString,
		"app_id":          pluginapi.ConfigFieldTypeString,
		"scheduler_mode":  pluginapi.ConfigFieldTypeEnum,
		"checkin_auto":    pluginapi.ConfigFieldTypeBoolean,
		"retry_on_4xx":    pluginapi.ConfigFieldTypeString,
		"token_keepalive": pluginapi.ConfigFieldTypeBoolean,
		"lifecycle_auto":  pluginapi.ConfigFieldTypeBoolean,
		"auth_flag_guard": pluginapi.ConfigFieldTypeBoolean,
		// 6 个原隐藏键（0.4.0 补登记）。
		"device_model":         pluginapi.ConfigFieldTypeString,
		"os_version":           pluginapi.ConfigFieldTypeString,
		"os_name":              pluginapi.ConfigFieldTypeString,
		"ide_version":          pluginapi.ConfigFieldTypeString,
		"ide_version_code":     pluginapi.ConfigFieldTypeString,
		"min_routable_credits": pluginapi.ConfigFieldTypeInteger,
		// 5 个 0.4.0 新键。
		"low_credit_disable_below":    pluginapi.ConfigFieldTypeInteger,
		"rotation_enabled":            pluginapi.ConfigFieldTypeBoolean,
		"rotation_serve_minutes":      pluginapi.ConfigFieldTypeInteger,
		"rotation_cooldown_minutes":   pluginapi.ConfigFieldTypeInteger,
		"rotation_idle_reset_minutes": pluginapi.ConfigFieldTypeInteger,
	}
	for name, wantType := range want {
		got, ok := fields[name]
		if !ok {
			t.Fatalf("配置键 %q 未登记到 ConfigFields——管理端将看不到它", name)
		}
		if got != wantType {
			t.Fatalf("配置键 %q 类型 = %v, want %v", name, got, wantType)
		}
	}
}

// TestConfigFields_RotationKeysAreWired 锁定 5 个新键真的会被 applyConfigLines 消费。
//
// 只登记不解析等于「管理端能改但不生效」，与上面的缺陷形态互为镜像。
func TestConfigFields_RotationKeysAreWired(t *testing.T) {
	clearRotationStates()
	t.Cleanup(clearRotationStates)
	defer setLoadedRotationConfig(defaultRotationEnabled, defaultRotationServeMinutes,
		defaultRotationCooldownMinutes, defaultRotationIdleResetMinutes)

	configureYAML(t, "rotation_enabled: false\n"+
		"rotation_serve_minutes: 30\n"+
		"rotation_cooldown_minutes: 45\n"+
		"rotation_idle_reset_minutes: 5\n")

	cfg := loadedRotationConfig()
	if cfg.enabled {
		t.Fatal("rotation_enabled: false 必须生效")
	}
	if cfg.serve.Minutes() != 30 {
		t.Fatalf("rotation_serve_minutes = %v, want 30m", cfg.serve)
	}
	if cfg.cooldown.Minutes() != 45 {
		t.Fatalf("rotation_cooldown_minutes = %v, want 45m", cfg.cooldown)
	}
	if cfg.idle.Minutes() != 5 {
		t.Fatalf("rotation_idle_reset_minutes = %v, want 5m", cfg.idle)
	}
}

// TestConfigFields_RotationKeysKeepExplicitFalse 锁定 reconfigure 不冲掉显式关闭。
//
// 不含轮换键的 reconfigure（例如只改 scheduler_mode）必须保留上一次显式写入的
// rotation_enabled: false；否则开关会被默认值 true 静默冲回。
func TestConfigFields_RotationKeysKeepExplicitFalse(t *testing.T) {
	clearRotationStates()
	t.Cleanup(clearRotationStates)
	defer setLoadedRotationConfig(defaultRotationEnabled, defaultRotationServeMinutes,
		defaultRotationCooldownMinutes, defaultRotationIdleResetMinutes)

	configureYAML(t, "rotation_enabled: false\n")
	if loadedRotationConfig().enabled {
		t.Fatal("precondition: 应已关闭")
	}
	// 只改 scheduler_mode，不出现任何 rotation_* 键。
	configureYAML(t, "scheduler_mode: session\n")

	if loadedRotationConfig().enabled {
		t.Fatal("不含轮换键的 reconfigure 不得把显式 false 冲回默认 true")
	}
}

// TestConfigFields_LowCreditDisableBelowWired 锁定积分禁用线可配。
func TestConfigFields_LowCreditDisableBelowWired(t *testing.T) {
	defer setLowCreditDisableBelow(defaultLowCreditDisableBelow)

	configureYAML(t, "low_credit_disable_below: 150\n")
	if got := loadedLowCreditDisableBelow(); got != 150 {
		t.Fatalf("low_credit_disable_below = %d, want 150", got)
	}
	// 负值是笔误，必须按 0（关闭）处理，绝不能变成「全部禁用」。
	configureYAML(t, "low_credit_disable_below: -5\n")
	if got := loadedLowCreditDisableBelow(); got != defaultLowCreditDisableBelow {
		t.Fatalf("负值必须保持关闭态，got %d", got)
	}
}

// TestVersionMatchesRegistration 锁定版本三处一致：main.go 的 version 变量、
// 注册元数据与 VERSION 文件必须相同。
//
// 期望值从 VERSION 文件读取而不是写死字面量：写死会让每次发版都必须同步改测试，
// 反而更容易漏改。这里校验的是「三处彼此一致」，不是某个具体版本号。
// 最近修改时间：2026-10-31；改动原因：改为比对 VERSION 文件，发版无需改测试。
func TestVersionMatchesRegistration(t *testing.T) {
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读取 VERSION 失败: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("VERSION 文件为空")
	}
	if version != want {
		t.Fatalf("main.go version = %q, want %q（应与 VERSION 一致）", version, want)
	}
	reg := wbRegistration()
	if reg.Metadata.Version != want {
		t.Fatalf("注册元数据版本 = %q, want %q", reg.Metadata.Version, want)
	}
}
