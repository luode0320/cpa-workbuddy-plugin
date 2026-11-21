package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadKeepaliveSettingReadsRealHostLayout 校验能解析宿主真实配置层级。
//
// 宿主的 config.yaml 顶层键带引号（"plugins" / "configs"），本插件条目还混在
// 十几个其它插件之间；这条用例用生产配置的真实形态固定解析路径。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:30:00；改动原因：新增，用生产层级样本固定解析行为。
func TestLoadKeepaliveSettingReadsRealHostLayout(t *testing.T) {
	const sample = `"config-version": 8
"plugins":
  "configs":
    copilot-provider:
      "enabled": false
      store:
        version: 0.1.12
    doubao-provider:
      config_yaml: |
        keepalive_enabled: true
        keepalive_interval_seconds: 120
      enabled: true
      priority: 20
    qoder-ai-provider:
      config_yaml: ""
      enabled: false
`
	path := filepath.Join(t.TempDir(), hostConfigFileName)
	if errWrite := os.WriteFile(path, []byte(sample), 0o600); errWrite != nil {
		t.Fatalf("写样本配置失败：%v", errWrite)
	}
	prev := hostConfigPathOverride
	hostConfigPathOverride = path
	t.Cleanup(func() { hostConfigPathOverride = prev })

	setting, ok := loadKeepaliveSetting()
	if !ok {
		t.Fatal("生产层级样本应解析成功")
	}
	if !setting.enabled {
		t.Error("样本里 keepalive_enabled 为 true")
	}
	if setting.interval != 120*time.Second {
		t.Errorf("间隔应为 120s，实际 %v", setting.interval)
	}
}

// TestHostConfigPathPrefersCwd 校验默认定位会先看当前工作目录下的 config.yaml。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-22 02:30:00；改动原因：新增，容器 cwd 为 /CLIProxyAPI 的定位依据。
func TestHostConfigPathPrefersCwd(t *testing.T) {
	prevOverride := hostConfigPathOverride
	hostConfigPathOverride = ""
	t.Cleanup(func() { hostConfigPathOverride = prevOverride })

	dir := t.TempDir()
	prevWd, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("读取工作目录失败：%v", errGetwd)
	}
	if errChdir := os.Chdir(dir); errChdir != nil {
		t.Fatalf("切换工作目录失败：%v", errChdir)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	if got := hostConfigPath(); got != "" {
		t.Fatalf("空目录下应返回空串，实际 %q", got)
	}

	target := filepath.Join(dir, hostConfigFileName)
	if errWrite := os.WriteFile(target, []byte("plugins:\n  configs: {}\n"), 0o600); errWrite != nil {
		t.Fatalf("写配置失败：%v", errWrite)
	}
	if got := hostConfigPath(); got != target {
		t.Fatalf("应命中 cwd 下的 config.yaml，实际 %q", got)
	}
}
