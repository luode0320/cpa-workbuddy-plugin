// config.go owns the traework plugin configuration. Values are parsed from
// config_yaml on every configure()/reconfigure (MethodPluginRegister /
// MethodPluginReconfigure) and read under RW locks so a concurrent
// reconfigure is safe. Missing keys keep the previous value (kill-switch
// safe — mirrors qoderwork/workbuddy convention).
package main

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// traeConfig holds the runtime-tunable knobs for the TraeWork upstream.
type traeConfig struct {
	// APIHost is the upstream llm_utils_chat host (CN default).
	APIHost string
	// AppID is the x-app-id header (shared Trae client id default).
	AppID string
	// DeviceModel / OSVersion / OSName populate the device fingerprint
	// headers (x-device-brand / x-os-version / x-device-type).
	DeviceModel string
	OSVersion   string
	OSName      string
	// IDEVersion overrides the auto-detected client version; empty lets
	// DetectIDEVersion read the installed manifest.
	IDEVersion string
	// IDEVersionCode overrides the daily YYYYMMDD code.
	IDEVersionCode string
	// SchedulerMode: "off" (defer to built-in) or "credits" (plugin picks).
	SchedulerMode string
	// CheckinAuto enables the daily auto check-in loop (09:00 / 21:00 local).
	CheckinAuto bool
	// ManagementKey optional defence-in-depth Bearer key for mutating routes.
	ManagementKey string
	// UsageReportURL / UsageReportKey optional CPAMP usage-import endpoint.
	UsageReportURL string
	UsageReportKey string
}

// Defaults mirror the verified gateway configuration (trae-gateway-go).
const (
	defaultAPIHost       = "https://trae-api-cn.mchost.guru"
	defaultAppID         = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
	defaultDeviceModel   = "83DG"
	defaultOSName        = "windows"
	defaultOSVersion     = "Windows 11 Pro"
	defaultCheckinAuto   = true
	schedulerModeOff     = "off"
	schedulerModeCredits = "credits"
)

var (
	cfgMu   sync.RWMutex
	traeCfg = defaultTraeConfig()
)

func defaultTraeConfig() *traeConfig {
	return &traeConfig{
		APIHost:       defaultAPIHost,
		AppID:         defaultAppID,
		DeviceModel:   defaultDeviceModel,
		OSName:        defaultOSName,
		OSVersion:     defaultOSVersion,
		SchedulerMode: schedulerModeOff,
		CheckinAuto:   defaultCheckinAuto,
	}
}

func loadedConfig() traeConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return *traeCfg
}

// configure parses the host's config_yaml payload. The wire format carries
// config_yaml as []byte (JSON-encoded as base64), so the field MUST be []byte
// — json auto-decodes it back to the raw YAML text. A string/RawMessage field
// would receive the base64 blob undecoded and silently fail every line parse.
func configure(raw []byte) {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	lines := yamlLines(req.ConfigYAML)

	cfgMu.Lock()
	defer cfgMu.Unlock()
	applyConfigLines(traeCfg, lines)
}

// yamlLines converts the config_yaml bytes into trimmed, comment-stripped
// lines.
func yamlLines(raw []byte) []string {
	var out []string
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// applyConfigLines applies one YAML-style "key: value" line at a time.
// Unknown keys are ignored (forward compatibility); a parse failure for a
// known key logs once and keeps the previous value.
func applyConfigLines(cfg *traeConfig, lines []string) {
	for i, ln := range lines {
		key, val, ok := splitYAMLKey(ln)
		if !ok {
			continue
		}
		switch key {
		case "api_host":
			if v := strings.TrimSpace(val); v != "" {
				cfg.APIHost = strings.TrimRight(v, "/")
			}
		case "app_id":
			if v := strings.TrimSpace(val); v != "" {
				cfg.AppID = v
			}
		case "device_model":
			if v := strings.TrimSpace(val); v != "" {
				cfg.DeviceModel = v
			}
		case "os_version":
			if v := strings.TrimSpace(val); v != "" {
				cfg.OSVersion = v
			}
		case "os_name":
			if v := strings.TrimSpace(val); v != "" {
				cfg.OSName = v
			}
		case "ide_version":
			if v := strings.TrimSpace(val); v != "" {
				cfg.IDEVersion = v
			}
		case "ide_version_code":
			if v := strings.TrimSpace(val); v != "" {
				cfg.IDEVersionCode = v
			}
		case "scheduler_mode":
			v := strings.TrimSpace(val)
			if v == schedulerModeOff || v == schedulerModeCredits {
				cfg.SchedulerMode = v
			} else {
				log.Printf("[traework] config: ignored invalid scheduler_mode %q", v)
			}
		case "checkin_auto":
			if b, ok := parseYAMLBool(val); ok {
				cfg.CheckinAuto = b
			}
		case "management_key":
			if v := strings.TrimSpace(val); v != "" {
				cfg.ManagementKey = v
			}
			setManagementKey(cfg.ManagementKey)
		case "usage_report_url":
			if v := strings.TrimSpace(val); v != "" {
				cfg.UsageReportURL = v
			}
			setUsageReport(cfg.UsageReportURL, cfg.UsageReportKey)
		case "usage_report_key":
			if v := strings.TrimSpace(val); v != "" {
				cfg.UsageReportKey = v
			}
			setUsageReport(cfg.UsageReportURL, cfg.UsageReportKey)
		case "models":
			// models is a YAML list; the raw value was already stripped of
			// quotes, so re-parse the whole line's JSON when possible.
			// 兼容面板/编辑器把单行 JSON 自动美化成多行 pretty-print
			// 的场景：单行解析失败时跨行收集直到括号闭合再解析。
			if j := strings.Index(ln, ":"); j >= 0 {
				if v, ok := parseModelsValue(lines, i, strings.TrimSpace(ln[j+1:])); ok {
					parseModelsConfig(v)
				}
			}
		case "retry_on_4xx":
			if n, ok := parseRetryOn4xxLine(ln); ok {
				setRetryOn4xx(clampRetryOn4xx(n))
			}
		case "anomaly_pool_threshold":
			if n, ok := parseAnomalyThresholdLine(ln); ok {
				setAnomalyConfig(clampAnomalyThreshold(n), anomalyRefreshEnabled())
			}
		case "anomaly_refresh_enabled":
			if b, ok := parseAnomalyRefreshEnabledLine(ln); ok {
				setAnomalyConfig(anomalyThreshold(), b)
			}
		}
	}
}

// parseModelsValue 解析 config_yaml 中 `models:` 的 JSON 值，兼容面板/编辑器
// 把单行 JSON 自动美化成多行 pretty-print 的场景（如
// `models: [\n  {...},\n  {...}\n]`）。单行解析失败时按括号配对收集后续行
// 直到 JSON 闭合，再整体解析。无法闭合或解析失败时返回 ok=false（调用方
// 保持现状，与"全非法条目保持现状"语义一致）。
// [参数] lines：config_yaml 全部分行；i：models: 所在行下标；
//
//	rest：该行冒号后的内容（已 TrimSpace）
//
// [返回] (解析出的 JSON 值, 是否成功)
// 最近修改时间 2026-08-28（新增多行 JSON 兼容）
func parseModelsValue(lines []string, i int, rest string) (any, bool) {
	var b strings.Builder
	b.WriteString(rest)
	depth, inStr, esc, closed := 0, false, false, false
	scan := func(s string) {
		for _, r := range s {
			if inStr {
				if esc {
					esc = false
					continue
				}
				if r == '\\' {
					esc = true
					continue
				}
				if r == '"' {
					inStr = false
				}
				continue
			}
			switch r {
			case '"':
				inStr = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					closed = true
				}
			}
		}
	}
	scan(rest)
	for j := i + 1; j < len(lines) && !closed; j++ {
		line := strings.TrimSpace(lines[j])
		if line == "" {
			continue
		}
		b.WriteString(line)
		scan(line)
	}
	if !closed {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(b.String()), &v); err != nil {
		return nil, false
	}
	return v, true
}

// splitYAMLKey splits "key: value" into (key, value). Returns ok=false for
// lines without a colon.
func splitYAMLKey(ln string) (key, val string, ok bool) {
	i := strings.Index(ln, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(strings.ToLower(ln[:i]))
	val = strings.TrimSpace(ln[i+1:])
	// Strip inline comments for scalar values.
	if j := strings.Index(val, "#"); j >= 0 && (j == 0 || val[j-1] == ' ') {
		val = strings.TrimSpace(val[:j])
	}
	val = strings.Trim(val, "\"'")
	return key, val, true
}

// parseYAMLBool accepts the common YAML boolean spellings.
func parseYAMLBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	return false, false
}
