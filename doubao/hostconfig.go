package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// throttleSettingTTL 是账号闸门配置的缓存有效期。
//
// 闸门每次取许可都要读这三项，逐个请求去解析 69KB 的宿主配置不可接受；而配置变更
// 本身是低频人工动作，30 秒内延迟对齐足够。
const throttleSettingTTL = 30 * time.Second

var (
	// throttleSettingMu 保护下面三个缓存字段。
	throttleSettingMu sync.Mutex
	// throttleSettingCached 是上次从宿主配置读到的闸门设置。
	throttleSettingCached throttleSetting
	// throttleSettingExpiry 是缓存到期时刻。
	throttleSettingExpiry time.Time
)

// throttleSetting 是从宿主配置文件里读出的账号闸门设置。
type throttleSetting struct {
	// maxInflight 是同一账号最大在途请求数。
	maxInflight int
	// minInterval 是同一账号两次发起的最小间隔。
	minInterval time.Duration
	// backoff 是被判访问频繁后的软退避时长。
	backoff time.Duration
}

// effectiveThrottleSetting 返回当前生效的账号闸门设置。
//
// 优先级：宿主配置文件里的显式配置 > currentConfig() 的运行期值 > 内置默认值。
// 读不到宿主配置时直接用运行期值，不让兜底失败影响节流本身。
// [参数] 无。
// [返回] 账号闸门设置。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，让闸门三项配置也能绕过宿主不回调的缺陷。
func effectiveThrottleSetting() throttleSetting {
	now := time.Now()

	throttleSettingMu.Lock()
	if override := throttleSettingOverride; override != nil {
		cached := normalizeThrottleSetting(*override)
		throttleSettingMu.Unlock()
		return cached
	}
	if now.Before(throttleSettingExpiry) {
		cached := throttleSettingCached
		throttleSettingMu.Unlock()
		return cached
	}
	throttleSettingMu.Unlock()

	cfg := currentConfig()
	setting := throttleSetting{
		maxInflight: cfg.AccountMaxInflight,
		minInterval: time.Duration(cfg.AccountMinIntervalMs) * time.Millisecond,
		backoff:     time.Duration(cfg.RateLimitBackoffSeconds) * time.Second,
	}
	// 1. 宿主配置能读到就用它覆盖，读不到保留运行期值。
	if parsed, okParsed := loadThrottleSetting(); okParsed {
		setting = parsed
	}

	// 2. 非法值统一回落默认，闸门不接受 0 并发这种会让请求永久排队的值。
	setting = normalizeThrottleSetting(setting)

	throttleSettingMu.Lock()
	throttleSettingCached = setting
	throttleSettingExpiry = now.Add(throttleSettingTTL)
	throttleSettingMu.Unlock()
	return setting
}

// normalizeThrottleSetting 把非法值统一换成安全默认值。
//
// maxInflight <= 0 会让请求永久排队，backoff <= 0 会让软退避形同虚设，
// 两者都必须挡住。
// [参数] setting: 待校验的设置。
// [返回] 校验后的设置。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，抽出非法值回落供覆盖路径复用。
func normalizeThrottleSetting(setting throttleSetting) throttleSetting {
	if setting.maxInflight <= 0 {
		setting.maxInflight = defaultAccountMaxInflight
	}
	if setting.minInterval < 0 {
		setting.minInterval = 0
	}
	if setting.backoff <= 0 {
		setting.backoff = defaultRateLimitBackoffSeconds * time.Second
	}
	return setting
}

// loadThrottleSetting 从宿主配置文件里读出本插件的账号闸门设置。
//
// 三项都缺省时返回 okThrottle=false，让调用方沿用运行期值与默认值。
// [参数] 无。
// [返回] 账号闸门设置与是否读到至少一项。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，解析宿主配置里的闸门三项。
func loadThrottleSetting() (throttleSetting, bool) {
	path := hostConfigPath()
	if path == "" {
		return throttleSetting{}, false
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return throttleSetting{}, false
	}

	var doc hostPluginDocument
	if errUnmarshal := yaml.Unmarshal(raw, &doc); errUnmarshal != nil {
		return throttleSetting{}, false
	}
	item, okItem := doc.Plugins.Configs[providerName]
	if !okItem {
		return throttleSetting{}, false
	}
	text := strings.TrimSpace(item.ConfigYAML)
	if text == "" {
		return throttleSetting{}, false
	}

	var decoded configOverride
	if errUnmarshal := yaml.Unmarshal([]byte(text), &decoded); errUnmarshal != nil {
		return throttleSetting{}, false
	}

	// 1. 缺省项从运行期配置起步，只有显式写了才替换。
	cfg := currentConfig()
	setting := throttleSetting{
		maxInflight: cfg.AccountMaxInflight,
		minInterval: time.Duration(cfg.AccountMinIntervalMs) * time.Millisecond,
		backoff:     time.Duration(cfg.RateLimitBackoffSeconds) * time.Second,
	}
	if decoded.AccountMaxInflight > 0 {
		setting.maxInflight = decoded.AccountMaxInflight
	}
	if decoded.AccountMinIntervalMs > 0 {
		setting.minInterval = time.Duration(decoded.AccountMinIntervalMs) * time.Millisecond
	}
	if decoded.RateLimitBackoffSeconds > 0 {
		setting.backoff = time.Duration(decoded.RateLimitBackoffSeconds) * time.Second
	}
	return setting, true
}

// resetThrottleSettingCache 清空闸门配置缓存（仅测试使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-22 15:10:00；改动原因：新增，避免测试间缓存串扰。
func resetThrottleSettingCache() {
	throttleSettingMu.Lock()
	defer throttleSettingMu.Unlock()
	throttleSettingCached = throttleSetting{}
	throttleSettingExpiry = time.Time{}
}
