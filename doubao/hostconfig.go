package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 本文件实现「宿主配置文件兜底读」：直接从宿主落盘的 config.yaml 里取本插件的
// config_yaml，用来补上宿主不回调 reconfigure 的那一步。
//
// 为什么需要它（2026-11-22 生产实测）：PUT /v0/management/plugins/<id>/config
// 之后宿主确实把新值写进了 config.yaml，watcher 也打了 config file changed，但
// 插件收不到 MethodPluginReconfigure —— 三次把 keepalive_interval_seconds 改成
// 120/90/60 秒，保活仍然严格每 30 分 00 秒跑一次，说明 ticker 从未重建。宿主的
// 重载回调链（management hook → reloadConfigFromWatcher → ApplyConfig）在成功
// 路径上不打日志，插件侧无法判定它走到哪一步。
//
// 只兜底保活两项，不碰其余五项：保活是唯一「读不到新值就会留下陈旧行为」的配置
// （ticker 一旦按旧间隔建起来就永不变），而超时/缓存窗口每次请求都会重读
// currentConfig()，重装或重启后自然对齐。

const (
	// hostConfigFileName 是宿主配置文件名。
	//
	// 容器里 PID1 的 cwd 是 /CLIProxyAPI，配置文件映射到 /CLIProxyAPI/config.yaml
	// （宿主侧 /usr/local/src/cli-proxy-api/config/config.yaml），因此相对 cwd 的
	// 同名文件就是首选候选。
	hostConfigFileName = "config.yaml"

	// hostConfigDirName 是「配置放在子目录」布局下的子目录名。
	hostConfigDirName = "config"
)

// hostConfigPathOverride 让用例把配置文件路径指到临时目录。
var hostConfigPathOverride string

// hostPluginDocument 只取兜底需要的那一层结构。
//
// 宿主配置里键名带引号（"plugins" / "configs"），yaml 解析后与无引号等价，
// 因此不需要为引号做特殊处理。
type hostPluginDocument struct {
	Plugins struct {
		Configs map[string]struct {
			ConfigYAML string `yaml:"config_yaml"`
		} `yaml:"configs"`
	} `yaml:"plugins"`
}

// keepaliveSetting 是从宿主配置文件里读出的保活设置。
type keepaliveSetting struct {
	enabled  bool
	interval time.Duration
}

// keepaliveTickAction 是本次触发前要对定时器执行的动作。
type keepaliveTickAction int

const (
	// keepaliveProceed 表示宿主配置读不到或与当前一致，照常跑一轮。
	keepaliveProceed keepaliveTickAction = iota
	// keepaliveStop 表示宿主配置已把保活关掉，协程应当退出。
	keepaliveStop
	// keepaliveReschedule 表示宿主配置换了间隔，需要重建 ticker。
	keepaliveReschedule
)

// hostConfigPath 定位宿主配置文件，找不到返回空串。
// [参数] 无。
// [返回] 存在的配置文件路径；都不存在时为空串。
// 最近修改时间：2026-11-22 02:10:00；改动原因：新增，为保活兜底提供文件路径。
func hostConfigPath() string {
	if path := strings.TrimSpace(hostConfigPathOverride); path != "" {
		return path
	}

	candidates := make([]string, 0, 3)
	if wd, errWd := os.Getwd(); errWd == nil {
		candidates = append(candidates, filepath.Join(wd, hostConfigFileName))
		candidates = append(candidates, filepath.Join(wd, hostConfigDirName, hostConfigFileName))
	}
	candidates = append(candidates, filepath.Join(string(os.PathSeparator), "CLIProxyAPI", hostConfigFileName))

	for _, path := range candidates {
		if info, errStat := os.Stat(path); errStat == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// loadKeepaliveSetting 从宿主配置文件里读出本插件的保活设置。
//
// 三层都可能失败：文件读不到、整份 YAML 解析失败、本插件条目不存在。任一失败都
// 返回 false，由调用方沿用当前运行值 —— 兜底读失败绝不能让保活停摆。
// [参数] 无。
// [返回] 保活设置与是否读成功。
// 最近修改时间：2026-11-22 02:10:00；改动原因：新增，解析宿主配置里的保活两项。
func loadKeepaliveSetting() (keepaliveSetting, bool) {
	path := hostConfigPath()
	if path == "" {
		return keepaliveSetting{}, false
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return keepaliveSetting{}, false
	}

	var doc hostPluginDocument
	if errUnmarshal := yaml.Unmarshal(raw, &doc); errUnmarshal != nil {
		return keepaliveSetting{}, false
	}
	item, okItem := doc.Plugins.Configs[providerName]
	if !okItem {
		return keepaliveSetting{}, false
	}

	// 1. 缺省值先按「默认开启 + 默认间隔」填好，与 configurePlugin 的口径一致。
	setting := keepaliveSetting{
		enabled:  true,
		interval: time.Duration(defaultKeepaliveIntervalSeconds) * time.Second,
	}
	text := strings.TrimSpace(item.ConfigYAML)
	if text == "" {
		return setting, true
	}

	// 2. config_yaml 是嵌套的一层 YAML，复用 configurePlugin 的解码结构，指针语义一致。
	var decoded configOverride
	if errUnmarshal := yaml.Unmarshal([]byte(text), &decoded); errUnmarshal != nil {
		return keepaliveSetting{}, false
	}
	if decoded.KeepaliveEnabled != nil {
		setting.enabled = *decoded.KeepaliveEnabled
	}
	if decoded.KeepaliveIntervalSeconds > 0 {
		setting.interval = time.Duration(decoded.KeepaliveIntervalSeconds) * time.Second
	}
	return setting, true
}

// decideKeepaliveTick 比对宿主配置文件与当前运行间隔，决定本次触发前的动作。
// [参数] current: 当前生效的保活间隔。
// [返回] 新的间隔（仅 keepaliveReschedule 时有意义）与动作。
// 最近修改时间：2026-11-22 02:10:00；改动原因：新增，把「读配置 + 比对」抽成可测的纯决策。
func decideKeepaliveTick(current time.Duration) (time.Duration, keepaliveTickAction) {
	setting, okSetting := loadKeepaliveSetting()
	if !okSetting {
		return current, keepaliveProceed
	}
	if !setting.enabled {
		return current, keepaliveStop
	}
	if setting.interval != current {
		return setting.interval, keepaliveReschedule
	}
	return current, keepaliveProceed
}
