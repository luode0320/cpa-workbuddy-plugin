package cpa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	PluginID   = "gemini-web2api"
	ProviderID = "gemini-web2api"
)

// PluginConfig defines the YAML/JSON configuration under plugins.configs.gemini-web2api in CLIProxyAPI's config.yaml.
type PluginConfig struct {
	DataDir               string            `json:"data_dir" yaml:"data_dir"`
	AccountsDir           string            `json:"accounts_dir" yaml:"accounts_dir"`
	Proxy                 string            `json:"proxy" yaml:"proxy"`
	SaveHistory           *bool             `json:"save_history" yaml:"save_history"`
	ContinueSession       *bool             `json:"continue_session" yaml:"continue_session"`
	GuestEnabled          *bool             `json:"guest_enabled" yaml:"guest_enabled"`
	ModelMapping          string            `json:"model_mapping" yaml:"model_mapping"`
	ModelAliases          map[string]string `json:"model_aliases" yaml:"model_aliases"`
	AccountCooldown       string            `json:"account_cooldown" yaml:"account_cooldown"`
	SessionTTL            string            `json:"session_ttl" yaml:"session_ttl"`
	InitTimeout           string            `json:"init_timeout" yaml:"init_timeout"`
	FirstEventHedge       string            `json:"first_event_hedge" yaml:"first_event_hedge"`
	FirstEventHedgeModels string            `json:"first_event_hedge_models" yaml:"first_event_hedge_models"`
	PerAccountConcurrency *int              `json:"per_account_concurrency" yaml:"per_account_concurrency"`
	RoutingStrategy       string            `json:"routing_strategy" yaml:"routing_strategy"`
	AccountOrder          string            `json:"account_order" yaml:"account_order"`
	ModelAccounts         string            `json:"model_accounts" yaml:"model_accounts"`
	RequestBodyLog        *bool             `json:"request_body_log" yaml:"request_body_log"`
	LogFile               *bool             `json:"log_file" yaml:"log_file"`
	LogMaxTotalMB         *int              `json:"log_max_total_mb" yaml:"log_max_total_mb"`
	LogLevel              string            `json:"log_level" yaml:"log_level"`
	AutoStart             *bool             `json:"auto_start" yaml:"auto_start"`
	RouteUnprefixedModels *bool             `json:"route_unprefixed_models" yaml:"route_unprefixed_models"`
	SyncAuthFiles         *bool             `json:"sync_auth_files" yaml:"sync_auth_files"`
	FakeStream            *bool             `json:"fake_stream" yaml:"fake_stream"`
	ModelPrefix           string            `json:"model_prefix" yaml:"model_prefix"`
	Options               *PluginConfig     `json:"options,omitempty" yaml:"options,omitempty"`
}

func defaultPluginDataDir() string {
	if envDir := strings.TrimSpace(os.Getenv("GEMINI_WEB2API_DATA_DIR")); envDir != "" {
		return envDir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".gemini-web2api")
	}
	return filepath.Join(os.TempDir(), "cpa-gemini-web2api")
}

// ParsePluginConfig decodes the plugin YAML or JSON config and builds an internal config.Config.
func ParsePluginConfig(raw []byte) (PluginConfig, config.Config, string, error) {
	var pcfg PluginConfig
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "null" && trimmed != "{}" {
		if err := yaml.Unmarshal(raw, &pcfg); err != nil {
			_ = json.Unmarshal(raw, &pcfg)
		}
		if pcfg.Options != nil {
			opt := *pcfg.Options
			if pcfg.DataDir == "" {
				pcfg.DataDir = opt.DataDir
			}
			if pcfg.AccountsDir == "" {
				pcfg.AccountsDir = opt.AccountsDir
			}
			if pcfg.Proxy == "" {
				pcfg.Proxy = opt.Proxy
			}
			if pcfg.SaveHistory == nil {
				pcfg.SaveHistory = opt.SaveHistory
			}
			if pcfg.ContinueSession == nil {
				pcfg.ContinueSession = opt.ContinueSession
			}
			if pcfg.GuestEnabled == nil {
				pcfg.GuestEnabled = opt.GuestEnabled
			}
			if pcfg.ModelMapping == "" {
				pcfg.ModelMapping = opt.ModelMapping
			}
			if len(pcfg.ModelAliases) == 0 {
				pcfg.ModelAliases = opt.ModelAliases
			}
			if pcfg.AccountCooldown == "" {
				pcfg.AccountCooldown = opt.AccountCooldown
			}
			if pcfg.SessionTTL == "" {
				pcfg.SessionTTL = opt.SessionTTL
			}
			if pcfg.InitTimeout == "" {
				pcfg.InitTimeout = opt.InitTimeout
			}
			if pcfg.FirstEventHedge == "" {
				pcfg.FirstEventHedge = opt.FirstEventHedge
			}
			if pcfg.FirstEventHedgeModels == "" {
				pcfg.FirstEventHedgeModels = opt.FirstEventHedgeModels
			}
			if pcfg.PerAccountConcurrency == nil {
				pcfg.PerAccountConcurrency = opt.PerAccountConcurrency
			}
			if pcfg.RoutingStrategy == "" {
				pcfg.RoutingStrategy = opt.RoutingStrategy
			}
			if pcfg.AccountOrder == "" {
				pcfg.AccountOrder = opt.AccountOrder
			}
			if pcfg.ModelAccounts == "" {
				pcfg.ModelAccounts = opt.ModelAccounts
			}
			if pcfg.RequestBodyLog == nil {
				pcfg.RequestBodyLog = opt.RequestBodyLog
			}
			if pcfg.LogFile == nil {
				pcfg.LogFile = opt.LogFile
			}
			if pcfg.LogMaxTotalMB == nil {
				pcfg.LogMaxTotalMB = opt.LogMaxTotalMB
			}
			if pcfg.LogLevel == "" {
				pcfg.LogLevel = opt.LogLevel
			}
			if pcfg.AutoStart == nil {
				pcfg.AutoStart = opt.AutoStart
			}
			if pcfg.RouteUnprefixedModels == nil {
				pcfg.RouteUnprefixedModels = opt.RouteUnprefixedModels
			}
			if pcfg.SyncAuthFiles == nil {
				pcfg.SyncAuthFiles = opt.SyncAuthFiles
			}
			if pcfg.FakeStream == nil {
				pcfg.FakeStream = opt.FakeStream
			}
			if pcfg.ModelPrefix == "" {
				pcfg.ModelPrefix = opt.ModelPrefix
			}
		}
	}

	dataDir := strings.TrimSpace(pcfg.DataDir)
	if dataDir == "" {
		dataDir = defaultPluginDataDir()
	}
	if strings.HasPrefix(dataDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dataDir = filepath.Join(home, dataDir[2:])
		}
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}

	accountsDir := strings.TrimSpace(pcfg.AccountsDir)
	if accountsDir == "" {
		accountsDir = filepath.Join(dataDir, "accounts")
	} else if strings.HasPrefix(accountsDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			accountsDir = filepath.Join(home, accountsDir[2:])
		}
	}
	if abs, err := filepath.Abs(accountsDir); err == nil {
		accountsDir = abs
	}

	proxyURL := strings.TrimSpace(pcfg.Proxy)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(os.Getenv("GEMINI_WEB2API_PROXY"))
	}

	accountCooldown := 2 * time.Minute
	if s := strings.TrimSpace(pcfg.AccountCooldown); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			accountCooldown = d
		}
	}

	sessionTTL := 30 * time.Minute
	if s := strings.TrimSpace(pcfg.SessionTTL); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			sessionTTL = d
		}
	}

	initTimeout := 20 * time.Second
	if s := strings.TrimSpace(pcfg.InitTimeout); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			initTimeout = d
		}
	}

	firstEventHedge := strings.ToLower(strings.TrimSpace(pcfg.FirstEventHedge))
	if firstEventHedge == "" {
		firstEventHedge = "auto"
	}
	firstEventHedgeModels := config.SplitList(config.Defaults["FIRST_EVENT_HEDGE_MODELS"])
	if s := strings.TrimSpace(pcfg.FirstEventHedgeModels); s != "" {
		firstEventHedgeModels = config.SplitList(s)
	}
	concurrency := 2
	if pcfg.PerAccountConcurrency != nil && *pcfg.PerAccountConcurrency > 0 {
		concurrency = *pcfg.PerAccountConcurrency
	}

	routingStrategy := strings.ToLower(strings.TrimSpace(pcfg.RoutingStrategy))
	if routingStrategy != "fill-first" {
		routingStrategy = "round-robin"
	}

	logMaxMB := 100
	if pcfg.LogMaxTotalMB != nil && *pcfg.LogMaxTotalMB >= config.MinLogTotalMB {
		logMaxMB = *pcfg.LogMaxTotalMB
	}

	logLevel := strings.ToLower(strings.TrimSpace(pcfg.LogLevel))
	switch logLevel {
	case "debug", "info", "warn", "error":
	default:
		logLevel = "info"
	}

	modelMapping := make(map[string]string)
	if s := strings.TrimSpace(pcfg.ModelMapping); s != "" {
		if parsed, err := config.ParseModelMapping(s); err == nil {
			for k, v := range parsed {
				modelMapping[k] = v
			}
		}
	}
	for k, v := range pcfg.ModelAliases {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			modelMapping[k] = v
		}
	}

	accountOrder := config.SplitList(pcfg.AccountOrder)
	var modelAccounts map[string][]string
	if s := strings.TrimSpace(pcfg.ModelAccounts); s != "" {
		if parsed, err := config.ParseModelAccounts(s); err == nil {
			modelAccounts = parsed
		}
	}

	cfg := config.Config{
		AuthStatePaths:        []string{accountsDir},
		ProxyURL:              proxyURL,
		AccountCooldown:       accountCooldown,
		SessionTTL:            sessionTTL,
		InitTimeout:           initTimeout,
		FirstEventHedge:       firstEventHedge,
		FirstEventHedgeModels: firstEventHedgeModels,
		AccountOrder:          accountOrder,
		ModelAccounts:         modelAccounts,
		ModelMapping:          modelMapping,
		SaveHistory:           boolOrDefault(pcfg.SaveHistory, false),
		ContinueSession:       boolOrDefault(pcfg.ContinueSession, false),
		NonstreamKeepalive:    true,
		FakeStream:            boolOrDefault(pcfg.FakeStream, false),
		LogLevel:              logLevel,
		RequestBodyLog:        boolOrDefault(pcfg.RequestBodyLog, false),
		LogFile:               boolOrDefault(pcfg.LogFile, false),
		LogMaxTotalMB:         logMaxMB,
		PerAccountConcurrency: concurrency,
		RoutingStrategy:       routingStrategy,
	}

	pcfg.DataDir = dataDir
	pcfg.AccountsDir = accountsDir
	pcfg.ModelPrefix = strings.TrimSpace(pcfg.ModelPrefix)
	return pcfg, cfg, dataDir, nil
}

func boolOrDefault(ptr *bool, def bool) bool {
	if ptr == nil {
		return def
	}
	return *ptr
}
