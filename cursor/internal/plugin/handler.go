package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorsession"
)

const (
	// providerName 必须与插件 id（二进制文件名 cursor-provider.<ext>）一致：
	// 宿主按插件 id 派生管理路由前缀、executor 标识符，并要求认证文件顶层
	// type 与之匹配，否则凭据无法路由到本执行器。
	providerName  = "cursor-provider"
	pluginAuthor  = "luode0320 (ported from yobo2u/omsub cursor-plugin)"
	pluginRepoURL = "https://github.com/luode0320/cpa-workbuddy-plugin"
	// Cursor 官方图标未随移植提供，留空以避免面板加载死链；如需品牌图标再补
	// assets/icons/Cursor.png 并回填此常量。
	pluginLogoURL = ""
)

// pluginVersion 是插件对外声明的版本，默认值可被 CI 注入的真实版本覆盖。
var pluginVersion = "0.1.0"

// SetVersion 由 main 包在启动时用构建期注入的版本调用，保证 registration 与
// 二进制版本一致。
func SetVersion(value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		pluginVersion = trimmed
	}
}

type Handler struct {
	auth     *cursorauth.Service
	cursor   CursorClient
	emitter  StreamEmitter
	host     HostCaller
	usage    *usageStore
	sessions *cursorsession.Store
	turns    *sessionTurnLocks
}

func NewHandler(dependencies Dependencies) *Handler {
	if dependencies.Auth == nil {
		dependencies.Auth = cursorauth.NewService(http.DefaultClient, cursorauth.DefaultEndpoints())
	}
	if dependencies.Cursor == nil {
		client, err := cursorapi.NewClient(cursorapi.Config{BaseURL: "https://api2.cursor.sh"})
		if err == nil {
			dependencies.Cursor = client
		}
	}
	return &Handler{
		auth: dependencies.Auth, cursor: dependencies.Cursor, emitter: dependencies.Emitter,
		host: dependencies.Host, usage: newUsageStore(), sessions: cursorsession.NewStore(cursorsession.Options{}),
		turns: newSessionTurnLocks(),
	}
}

func (handler *Handler) Call(ctx context.Context, method string, request []byte) []byte {
	raw, _ := handler.CallWithStatus(ctx, method, request)
	return raw
}

func (handler *Handler) CallWithStatus(ctx context.Context, method string, request []byte) ([]byte, bool) {
	result, err := handler.dispatch(ctx, method, request)
	if err != nil {
		return marshalEnvelope(envelope{OK: false, Error: describeExecutionError(err)}), false
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return marshalEnvelope(envelope{OK: false, Error: &envelopeError{Code: "cursor_plugin_error", Message: "encode plugin result"}}), false
	}
	return marshalEnvelope(envelope{OK: true, Result: raw}), true
}

func (handler *Handler) dispatch(ctx context.Context, method string, request []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return registration(), nil
	case "management.register":
		return managementRegistration(), nil
	case "management.handle":
		return handler.handleManagement(ctx, request)
	case "request.intercept_before":
		return handler.passRequest(request)
	case "request.intercept_after":
		return handler.observeRequestAuth(request)
	case "request.complete":
		return handler.completeRequest(request)
	case "auth.identifier", "executor.identifier":
		return map[string]string{"identifier": providerName}, nil
	case "auth.parse":
		return handler.parseAuth(request)
	case "auth.login.start":
		return handler.startLogin(ctx)
	case "auth.login.poll":
		return handler.pollLogin(ctx, request)
	case "auth.refresh":
		return handler.refreshAuth(ctx, request)
	case "model.static":
		return modelResponse([]string{"auto"}, nil), nil
	case "model.for_auth":
		return handler.modelsForAuth(ctx, request)
	case "executor.execute":
		return handler.execute(ctx, request)
	case "executor.execute_stream":
		return handler.executeStream(ctx, request)
	case "executor.count_tokens":
		return countTokens(request)
	case "executor.http_request":
		return nil, errors.New("Cursor plugin HTTP passthrough is unsupported")
	default:
		return nil, errors.New("unknown plugin method")
	}
}

func registration() map[string]any {
	return map[string]any{
		"schema_version": 3,
		"metadata": map[string]any{
			"Name":             providerName,
			"Version":          pluginVersion,
			"Author":           pluginAuthor,
			"GitHubRepository": pluginRepoURL,
			"Logo":             pluginLogoURL,
			"ConfigFields":     []string{},
		},
		"capabilities": map[string]any{
			"auth_provider":            true,
			"model_provider":           true,
			"management_api":           true,
			"request_interceptor":      true,
			"request_lifecycle_plugin": true,
			"usage_plugin":             false,
			"executor":                 true,
			"executor_model_scope":     "oauth",
			"executor_input_formats":   []string{"chat-completions"},
			"executor_output_formats":  []string{"chat-completions"},
		},
	}
}

func marshalEnvelope(response envelope) []byte {
	raw, err := json.Marshal(response)
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"cursor_plugin_error","message":"encode envelope"}}`)
	}
	return raw
}
