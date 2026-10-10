package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

// parseAuthIndexFromPath 从请求路径或 URL 查询参数中解析 auth_index。
// [参数] rawPath: 包含路径与可选 query string 的字符串。
// [返回] 解析出的 auth_index 字符串，若无则返回空字符串。
// 最近修改时间 2026-10-11（新增管理接口查询参数解析支持）
func parseAuthIndexFromPath(rawPath string) string {
	if idx := strings.Index(rawPath, "?"); idx >= 0 {
		query, err := url.ParseQuery(rawPath[idx+1:])
		if err == nil {
			return strings.TrimSpace(query.Get("auth_index"))
		}
	}
	return ""
}

// handleModelsQuery 处理 GET /plugins/cursor-provider/models 请求，返回指定账号或全局可用模型。
// [参数] ctx: 上下文；request: 管理请求对象，路径中可包含 auth_index 查询参数。
// [返回] 统一响应对象，包含 models 列表 JSON；若上游获取失败则优雅降级为默认常用模型。
// 最近修改时间 2026-10-11（新增面板测试弹窗模型选择列表查询支持）
func (handler *Handler) handleModelsQuery(ctx context.Context, request managementRequest) (managementResponse, error) {
	authIndex := parseAuthIndexFromPath(request.Path)
	if authIndex == "" {
		return managementJSON(http.StatusOK, map[string]any{"models": defaultCursorModels})
	}
	credential, err := handler.getCursorCredential(ctx, authIndex)
	if err != nil {
		return managementJSON(http.StatusOK, map[string]any{"models": defaultCursorModels})
	}
	var models []string
	if handler.cursor != nil {
		models, _ = handler.cursor.DiscoverModels(ctx, credential.AccessToken)
	}
	if len(models) == 0 {
		models = append([]string(nil), defaultCursorModels...)
	}
	filtered := filterDisabledModels(models, credential.DisabledModels)
	if len(filtered) == 0 {
		filtered = append([]string(nil), defaultCursorModels...)
	}
	return managementJSON(http.StatusOK, map[string]any{"models": filtered})
}

// handleTestActive 处理 POST /plugins/cursor-provider/test-active 请求，对指定账号发起手动活跃推理测试。
// [参数] ctx: 上下文；body: JSON 请求体，必须包含 auth_index，可选 model。
// [返回] 测试结果响应，成功时包含 ok: true、model、elapsed 及中文描述，失败时包含错误信息。
// 最近修改时间 2026-10-11（新增面板卡片测试按钮手动探活接口支持）
func (handler *Handler) handleTestActive(ctx context.Context, body []byte) (managementResponse, error) {
	var req struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return managementError(http.StatusBadRequest, "invalid JSON body"), nil
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return managementError(http.StatusBadRequest, "auth_index is required"), nil
	}
	_, credential, err := handler.getCursorAuth(ctx, authIndex)
	if err != nil {
		return managementError(http.StatusNotFound, fmt.Sprintf("获取凭据失败: %v", err)), nil
	}
	return handler.handleTestActiveWithAuth(ctx, credential, authIndex, req.Model)
}

// handleTestActiveWithAuth 使用指定凭据对 Cursor 上游执行真实轻量推理探活。
// [参数] ctx: 上下文；credential: Cursor 认证凭据；authIndex: 账号索引；model: 指定模型名称（可为空）。
// [返回] 管理接口响应体，包含测试往返结果与耗时。
// 最近修改时间 2026-10-11（实现测试推理与 401 自动令牌刷新重试闭环）
func (handler *Handler) handleTestActiveWithAuth(ctx context.Context, credential cursorauth.Credentials, authIndex, model string) (managementResponse, error) {
	if handler.cursor == nil {
		return managementJSON(http.StatusOK, map[string]any{
			"ok":    false,
			"error": "Cursor 客户端未就绪",
		})
	}
	chosenModel := strings.TrimSpace(model)
	if chosenModel == "" {
		chosenModel = pickDefaultCursorModel(credential)
	}

	start := time.Now()
	testCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	input := cursorapi.RunInput{
		AccessToken: credential.AccessToken,
		Model:       chosenModel,
		Prompt:      "User: ping",
		Mode:        cursorproto.FullReplay,
	}

	var responseText strings.Builder
	_, err := handler.cursor.Run(testCtx, input, func(event cursorproto.ServerEvent) error {
		if event.Kind == cursorproto.EventText {
			responseText.WriteString(event.Text)
		}
		return nil
	})

	elapsed := time.Since(start).Round(time.Millisecond)

	// 1. 若遇到未授权错误且存在 RefreshToken，尝试自动刷新一次并重试
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "unauthorized")) && credential.RefreshToken != "" && handler.auth != nil {
		if refreshed, refreshErr := handler.auth.Refresh(ctx, credential); refreshErr == nil {
			credential = refreshed
			_ = handler.saveRefreshedCredential(ctx, authIndex, refreshed)
			input.AccessToken = refreshed.AccessToken
			startRetry := time.Now()
			_, err = handler.cursor.Run(testCtx, input, func(event cursorproto.ServerEvent) error {
				if event.Kind == cursorproto.EventText {
					responseText.WriteString(event.Text)
				}
				return nil
			})
			elapsed = time.Since(startRetry).Round(time.Millisecond)
		}
	}

	// 2. 检查最终执行结果
	if err != nil {
		return managementJSON(http.StatusOK, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("活跃测试失败 (模型: %s): %v", chosenModel, err),
			"model": chosenModel,
		})
	}

	return managementJSON(http.StatusOK, map[string]any{
		"ok":      true,
		"model":   chosenModel,
		"elapsed": elapsed.String(),
		"message": fmt.Sprintf("活跃成功 (模型: %s, 耗时: %v)", chosenModel, elapsed),
	})
}

// saveRefreshedCredential 尝试把刷新后的凭据持久化写回宿主。
// [参数] ctx: 上下文；authIndex: 账号索引；refreshed: 刷新后的凭据。
// [返回] 错误信息（若有）。
// 最近修改时间 2026-10-11（测试探活自动刷新凭据持久化）
func (handler *Handler) saveRefreshedCredential(ctx context.Context, authIndex string, refreshed cursorauth.Credentials) error {
	if handler.host == nil {
		return nil
	}
	file, _, err := handler.getCursorAuth(ctx, authIndex)
	if err != nil {
		return err
	}
	name := file.Name
	if name == "" {
		name = authFileNameFor(refreshed)
	}
	raw, err := buildCursorAuthFileJSON(refreshed, false, "refreshed via active ping test")
	if err != nil {
		return err
	}
	_, err = handler.host.Call(ctx, "host.auth.save", map[string]any{
		"name": name, "json": json.RawMessage(raw),
	})
	return err
}

// pickDefaultCursorModel 从账号凭据中选取一个可用的默认模型（优先 auto）。
// [参数] credential: 账号凭据，包含已禁用的模型列表。
// [返回] 选取的默认模型名称。
// 最近修改时间 2026-10-11（新增默认测试模型选取逻辑）
func pickDefaultCursorModel(credential cursorauth.Credentials) string {
	disabled := normalizedModelSet(credential.DisabledModels)
	if _, isBlocked := disabled["auto"]; !isBlocked {
		return "auto"
	}
	for _, m := range defaultCursorModels {
		if _, isBlocked := disabled[m]; !isBlocked {
			return m
		}
	}
	return "auto"
}
