package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/app"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/config"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

type HostCallFunc func(method string, payload []byte) ([]byte, error)

type Plugin struct {
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	pluginCfg      PluginConfig
	appCfg         config.Config
	dataDir        string
	runtime        *app.PluginRuntime
	hostCall       HostCallFunc
	loginStateMu   sync.Mutex
	loginStates    map[string]*loginSessionState
	chromeLogin    sync.Mutex
	serviceStopped atomic.Bool
	lastRegisterNs atomic.Int64
	rrCursor       atomic.Uint64
	// lastRenewNs 是最近一次浏览器扩展 Cookie 续期尝试的纳秒时间戳，用于节流。
	lastRenewNs atomic.Int64
}

type loginSessionState struct {
	CreatedAt       time.Time
	InitialAccounts map[string]struct{}
	Provider        string
	Done            bool
	AccountID       string
	Email           string
	ErrorMessage    string
	// Importing 表示桥接页推送的 Cookie 正在后台导入，供轮询区分「刚提交」与「还没开始」。
	Importing bool
	// CallbackID 保存宿主发起登录时给的回调标识，导入完成后用它同步 auth 文件。
	CallbackID string
}

func NewPlugin(hostCall HostCallFunc) *Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	return &Plugin{
		ctx:         ctx,
		cancel:      cancel,
		hostCall:    hostCall,
		loginStates: make(map[string]*loginSessionState),
	}
}

func (p *Plugin) SetHostCall(fn HostCallFunc) {
	p.mu.Lock()
	p.hostCall = fn
	p.mu.Unlock()
}

func (p *Plugin) Runtime() (*app.PluginRuntime, error) {
	if p.serviceStopped.Load() {
		return nil, pluginabi.NewError("service_stopped", "gemini-web2api service is currently stopped", 503)
	}
	p.mu.RLock()
	rt := p.runtime
	p.mu.RUnlock()
	if rt != nil {
		return rt, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtime != nil {
		return p.runtime, nil
	}
	pcfg, cfg, dataDir, err := ParsePluginConfig(nil)
	if err != nil {
		return nil, err
	}
	p.pluginCfg = pcfg
	p.appCfg = cfg
	p.dataDir = dataDir
	rt, err = app.NewPluginRuntime(p.ctx, cfg, dataDir, boolOrDefault(pcfg.AutoStart, true), boolOrDefault(pcfg.GuestEnabled, true))
	if err != nil {
		return nil, err
	}
	p.runtime = rt
	return rt, nil
}

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  rpcCapabilities    `json:"capabilities"`
}

type rpcCapabilities struct {
	ModelProvider          bool                         `json:"model_provider"`
	AuthProvider           bool                         `json:"auth_provider"`
	Scheduler              bool                         `json:"scheduler"`
	ModelRouter            bool                         `json:"model_router"`
	Executor               bool                         `json:"executor"`
	ExecutorModelScope     pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats   []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats  []string                     `json:"executor_output_formats,omitempty"`
	RequestLifecyclePlugin bool                         `json:"request_lifecycle_plugin"`
	CommandLinePlugin      bool                         `json:"command_line_plugin"`
	ManagementAPI          bool                         `json:"management_api"`
	QuotaProvider          bool                         `json:"quota_provider"`
}

func (p *Plugin) HandleMethod(method string, raw []byte) (any, error) {
	switch strings.TrimSpace(method) {
	case pluginabi.MethodPluginRegister:
		return p.handleRegister(raw)
	case pluginabi.MethodPluginReconfigure, "plugin.reload":
		return p.handleReload(raw)
	case pluginabi.MethodPluginQuiesce, "plugin.stop":
		return p.handleQuiesce()
	case pluginabi.MethodPluginShutdown:
		return p.handleShutdown()

	// Auth Provider
	case pluginabi.MethodAuthIdentifier:
		return p.handleAuthIdentifier()
	case pluginabi.MethodAuthParse:
		return p.handleAuthParse(raw)
	case pluginabi.MethodAuthLoginStart:
		return p.handleAuthLoginStart(raw)
	case pluginabi.MethodAuthLoginPoll:
		return p.handleAuthLoginPoll(raw)
	case pluginabi.MethodAuthRefresh:
		return p.handleAuthRefresh(raw)

	// Model Provider & Router
	case pluginabi.MethodModelStatic:
		return p.handleModelStatic(raw)
	case pluginabi.MethodModelForAuth:
		return p.handleModelForAuth(raw)
	case pluginabi.MethodModelRoute:
		return p.handleModelRoute(raw)

	// Executor
	case pluginabi.MethodExecutorIdentifier, "executor.prepare":
		return p.handleExecutorIdentifier()
	case pluginabi.MethodExecutorExecute:
		return p.handleExecutorExecute(raw)
	case pluginabi.MethodExecutorExecuteStream:
		return p.handleExecutorExecuteStream(raw)
	case pluginabi.MethodExecutorCountTokens:
		return p.handleExecutorCountTokens(raw)
	case pluginabi.MethodExecutorHTTPRequest, "executor.http":
		return p.handleExecutorHTTPRequest(raw)

	// Scheduler & Request Lifecycle & Quota
	case pluginabi.MethodSchedulerPick:
		return p.handleSchedulerPick(raw)
	case pluginabi.MethodRequestComplete:
		return p.handleRequestComplete(raw)
	case pluginabi.MethodQuotaIdentifier:
		return p.handleQuotaIdentifier()
	case pluginabi.MethodQuotaDescribe:
		return p.handleQuotaDescribe()
	case pluginabi.MethodQuotaFetch:
		return p.handleQuotaFetch(raw)
	case pluginabi.MethodQuotaReset:
		return p.handleQuotaReset(raw)

	// CLI & Management API
	case pluginabi.MethodCommandLineRegister, "cli.register":
		return p.handleCLIRegister()
	case pluginabi.MethodCommandLineExecute, "cli.execute":
		return p.handleCLIRun(raw)
	case pluginabi.MethodManagementRegister, "management.routes":
		return p.handleManagementRegister()
	case pluginabi.MethodManagementHandle:
		return p.handleManagementHandle(raw)

	default:
		return nil, pluginabi.NewError("unsupported_method", fmt.Sprintf("unsupported plugin method %s", method), 501)
	}
}

func (p *Plugin) handleRegister(raw []byte) (any, error) {
	var req rpcLifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			var alt struct {
				ConfigYAML string `json:"config_yaml"`
			}
			if json.Unmarshal(raw, &alt) == nil && alt.ConfigYAML != "" {
				req.ConfigYAML = []byte(alt.ConfigYAML)
			}
		}
	}
	if err := p.applyConfig(req.ConfigYAML); err != nil {
		return nil, err
	}
	return rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             PluginID,
			Version:          app.Version(),
			Author:           "luode0320 (Upstream: jianli9158 / Mag1cFall)",
			GitHubRepository: "https://github.com/luode0320/cpa-workbuddy-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "data_dir",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "~/.gemini-web2api",
					Description: "Directory storing Gemini Web accounts, SQLite request ledger, and cooldowns",
				},
				{
					Name:        "proxy",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Optional upstream HTTP/SOCKS5 proxy URL (comma-separated proxy pool supported)",
				},
				{
					Name:        "guest_enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Enable anonymous guest session fallback when no signed-in account is available",
				},
				{
					Name:        "save_history",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Save generated conversations to Google Gemini Web account history",
				},
				{
					Name:        "continue_session",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Resume persisted Gemini Web conversation sessions across subsequent requests",
				},
				{
					Name:        "per_account_concurrency",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     2,
					Description: "Maximum concurrent generation requests per Gemini Web account",
				},
				{
					Name:        "routing_strategy",
					Type:        pluginapi.ConfigFieldTypeEnum,
					Default:     "round-robin",
					EnumValues:  []string{"round-robin", "fill-first"},
					Description: "Account load balancing strategy (round-robin or fill-first)",
				},
				{
					Name:        "route_unprefixed_models",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Route Gemini Web models when accounts or guest session are available",
				},
				{
					Name:        "sync_auth_files",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Automatically sync imported Gemini Web accounts to CLIProxyAPI auths/ directory",
				},
				{
					Name:        "fake_stream",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Fake streaming: collect full response upstream before streaming events to client",
				},
				{
					Name:        "account_order",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Comma-separated account IDs ordered by priority for fill-first routing",
				},
				{
					Name:        "model_accounts",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Comma-separated model:acc1|acc2 bindings restricting specific models to specific accounts",
				},
				{
					Name:        "first_event_hedge",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "auto",
					Description: "First-event backup hedge mode (auto, off, or positive duration/seconds)",
				},
				{
					Name:        "first_event_hedge_models",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "gemini-4-carbon,gemini-4-argon",
					Description: "Comma-separated model IDs eligible for first-event backup hedging",
				},
			},
		},
		Capabilities: rpcCapabilities{
			ModelProvider:      true,
			AuthProvider:       true,
			Scheduler:          true,
			ModelRouter:        true,
			Executor:           true,
			ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats: []string{
				"chat-completions",
				"responses",
				"anthropic",
				"gemini",
			},
			ExecutorOutputFormats: []string{
				"chat-completions",
				"responses",
				"anthropic",
				"gemini",
			},
			RequestLifecyclePlugin: true,
			CommandLinePlugin:      true,
			ManagementAPI:          true,
			QuotaProvider:          true,
		},
	}, nil
}

func (p *Plugin) handleReload(raw []byte) (any, error) {
	return p.handleRegister(raw)
}

func (p *Plugin) applyConfig(configYAML []byte) error {
	pcfg, cfg, dataDir, err := ParsePluginConfig(configYAML)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.pluginCfg = pcfg
	p.appCfg = cfg
	p.dataDir = dataDir
	p.lastRegisterNs.Store(time.Now().UnixNano())
	p.serviceStopped.Store(false)
	rt := p.runtime
	created := false
	if rt == nil {
		rt, err = app.NewPluginRuntime(p.ctx, cfg, dataDir, boolOrDefault(pcfg.AutoStart, true), boolOrDefault(pcfg.GuestEnabled, true))
		if err != nil {
			p.mu.Unlock()
			return err
		}
		p.runtime = rt
		created = true
	}
	p.mu.Unlock()
	if !created && rt != nil {
		rt.ApplyConfig(cfg, boolOrDefault(pcfg.GuestEnabled, true))
	}
	if boolOrDefault(pcfg.SyncAuthFiles, true) && rt != nil {
		p.setHostAuthFilesDisabled(rt, false)
	}
	return nil
}

func (p *Plugin) handleQuiesce() (any, error) {
	p.serviceStopped.Store(true)
	p.mu.RLock()
	rt := p.runtime
	syncAuth := boolOrDefault(p.pluginCfg.SyncAuthFiles, true)
	p.mu.RUnlock()
	if syncAuth {
		p.setHostAuthFilesDisabled(rt, true)
	}
	return struct{}{}, nil
}

func (p *Plugin) handleShutdown() (any, error) {
	p.serviceStopped.Store(true)
	p.mu.Lock()
	rt := p.runtime
	syncAuth := boolOrDefault(p.pluginCfg.SyncAuthFiles, true)
	p.runtime = nil
	cancel := p.cancel
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.mu.Unlock()
	p.loginStateMu.Lock()
	clear(p.loginStates)
	p.loginStateMu.Unlock()
	if syncAuth {
		p.setHostAuthFilesDisabled(rt, true)
	}
	if rt != nil {
		_ = rt.Close()
	}
	if cancel != nil {
		cancel()
	}
	return struct{}{}, nil
}

func (p *Plugin) callHost(method string, req any) (json.RawMessage, error) {
	p.mu.RLock()
	fn := p.hostCall
	p.mu.RUnlock()
	if fn == nil {
		return nil, fmt.Errorf("host callback is unavailable")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := fn(method, payload)
	if err != nil {
		return nil, err
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode host envelope for %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, env.Error
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return env.Result, nil
}

func (p *Plugin) logHost(callbackID, level, message string, fields map[string]string) {
	req := map[string]any{
		"host_callback_id": strings.TrimSpace(callbackID),
		"level":            level,
		"message":          message,
		"fields":           fields,
	}
	_, _ = p.callHost(pluginabi.MethodHostLog, req)
}
