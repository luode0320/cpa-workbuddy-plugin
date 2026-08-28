// config.go owns the traework plugin configuration. Values are parsed from
// config_yaml on every configure()/reconfigure (MethodPluginRegister /
// MethodPluginReconfigure) and read under RW locks so a concurrent
// reconfigure is safe. Missing keys keep the previous value (kill-switch
// safe — mirrors qoderwork/workbuddy convention).
package main

import (
	"encoding/json"
	"log"
	"strconv"
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

	// 宿主管理面板把面板数组字段序列化回 YAML block sequence 的 models
	// 形态（`models:` 换行逐行 `- key: value`）在 yamlLines 去缩进后无法
	// 识别，这里用原始行先行应用；单行/多行 JSON 形态仍由 applyConfigLines
	// 的 case "models" 解析（block 形态 rest 为空，JSON 路径自然跳过，二者
	// 互补不重复）。
	parseModelsYAMLConfig(req.ConfigYAML)

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

// parseModelsYAMLConfig 在 configure 早期用原始 YAML 行识别宿主把 JSON
// 数组序列化回 YAML block sequence 的 models 形态并应用。该形态形如：
//
//	models:
//	  - context: 2000000
//	    id: hy4-preview
//	    max_tokens: 20000
//	    name: Hy4 preview
//
// 与 applyConfigLines 的 JSON 路径互补：只处理 models: 行 rest 为空的
// block 形态，JSON 形态（单行/多行）交给 applyConfigLines 解析。
func parseModelsYAMLConfig(raw []byte) {
	rawLines := strings.Split(string(raw), "\n")
	for i, line := range rawLines {
		if strings.TrimSpace(line) == "models:" {
			if v, ok := parseModelsYAMLBlock(rawLines, i+1, indentOf(line)); ok {
				parseModelsConfig(v)
			}
			return
		}
	}
}

// parseModelsYAMLBlock 解析宿主管理面板把 JSON 数组序列化回 YAML 的 block
// sequence 形态（models: 换行逐行 `- key: value`）。输出与 json.Unmarshal
// 到 []any 的产物同构（元素为 map[string]any，标量字段转
// int64/float64/bool/string/nil），可直接交给 parseModelsConfig。无任何
// 可识别条目时返回 ok=false（调用方保持现状）。
// [参数] lines：config_yaml 原始分行（保留缩进）；start：models: 下一行下标；
//
//	baseIndent：models: 行的空格缩进数（block 内容必须更深）
//
// [返回] (条目列表, 是否成功)
// 最近修改时间 2026-08-29（新增 YAML block sequence 兼容，实证宿主
// config_store 把面板 JSON 数组序列化为该形态）
func parseModelsYAMLBlock(lines []string, start, baseIndent int) (any, bool) {
	var out []any
	first := -1
	for j := start; j < len(lines); j++ {
		ind := indentOf(lines[j])
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if ind <= baseIndent {
			return nil, false
		}
		if strings.HasPrefix(trimmed, "- ") {
			first = j
			break
		}
		return nil, false
	}
	if first < 0 {
		return nil, false
	}
	dashIndent := indentOf(lines[first])
	cur := map[string]any{}
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
	}
	for j := first; j < len(lines); j++ {
		ind := indentOf(lines[j])
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && ind == dashIndent {
			flush()
			cur = map[string]any{}
			if k, v, ok := splitYAMLPair(trimmed[2:]); ok {
				cur[k] = parseYAMLScalar(v)
			}
			continue
		}
		if ind <= baseIndent || ind <= dashIndent {
			break
		}
		if k, v, ok := splitYAMLPair(trimmed); ok {
			cur[k] = parseYAMLScalar(v)
		}
	}
	flush()
	return out, len(out) > 0
}

// indentOf 返回行首空格数（YAML block 缩进，tab 不计入）。
func indentOf(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// splitYAMLPair 按第一个冒号切分 "key: value"，返回 (key, value)。
func splitYAMLPair(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	j := strings.Index(s, ":")
	if j <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:j]), strings.TrimSpace(s[j+1:]), true
}

// parseYAMLScalar 把 YAML 标量值转为与 json.Unmarshal 同构的类型
// （int64/float64/bool/string/nil），保证 parseModelsConfig 的 JSON 通道
// 后续处理一致。
func parseYAMLScalar(v string) any {
	v = strings.TrimSpace(v)
	if v == "" || v == "null" || v == "~" {
		return nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	return strings.Trim(v, "\"'")
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
