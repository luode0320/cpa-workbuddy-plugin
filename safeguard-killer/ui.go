package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	uiResourcePath     = "/ui"
	mgmtStateRoutePath = "/plugins/safeguard-killer/state"
	// 面板与状态接口都挂在无鉴权的 resource 路由下：宿主只对 /v0/management 校验管理密钥，
	// /v0/resource/plugins/<id>/ 完全不校验，因此面板不需要、也不应该让用户手填密钥。
	uiStateResourcePath = "/state"
)

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// managementRegistrationPayload 声明插件的管理入口。
// [返回] 带鉴权的 management 路由供脚本/运维读取状态；面板自身改走无鉴权 resource 路由，
// 因此面板不需要用户手填管理密钥。
// 最近修改时间 2026-11-14 00:05:00, 修改, 新增无鉴权状态 resource 路由，面板不再需要用户手填管理密钥。
func managementRegistrationPayload() managementRegistration {
	return managementRegistration{
		Routes: []managementRoute{
			{
				Method:      http.MethodGet,
				Path:        mgmtStateRoutePath,
				Description: "返回 Safeguard Killer 的配置、护栏适配开关、智能分组、会话绑定以及近期路由决策。需要管理密钥。",
			},
		},
		Resources: []managementResource{
			{
				Path:        uiResourcePath,
				Menu:        "Safeguard Killer",
				Description: "在 Claude Code、Claude、OpenAI/Codex 与 Gemini 模型上清除模型侧安全护栏与 auto-mode 分类器，用智能分组（5 分钟缓存粘性的故障转移链）绕开拒答。可与模型改名/重排插件 CLIProxyAPI-Model-List-Tweaker-Plugin 配合使用。",
			},
			{
				// Menu 留空：这是面板自己 fetch 的数据接口，不该在侧边栏多出一个菜单项。
				Path:        uiStateResourcePath,
				Description: "面板自用的只读状态快照，免管理密钥。",
			},
		},
	}
}

// handleManagement 处理插件自己的 management / resource 路由请求。
// [参数] request：宿主编排层透传的管理请求信封
// [返回] 含 managementResponse 的信封，或无法识别路径时的错误
// 最近修改时间 2026-11-14 00:05:00, 修改, 状态接口同时挂在无鉴权 resource 路由下，与面板同源且无需管理密钥。
func handleManagement(request []byte) ([]byte, error) {
	var req managementRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
	}
	path := strings.TrimRight(req.Path, "/")
	switch {
	// 管理路由是 /v0/management/plugins/safeguard-killer/state，
	// resource 路由是 /v0/resource/plugins/safeguard-killer/state，两者都以该后缀结尾，因此共用这一支。
	case strings.HasSuffix(path, mgmtStateRoutePath):
		if req.Method != http.MethodGet {
			return nil, fmt.Errorf("management route %s: unsupported method %q", path, req.Method)
		}
		body, errMarshal := json.Marshal(buildUIState())
		if errMarshal != nil {
			return nil, fmt.Errorf("marshal ui state: %w", errMarshal)
		}
		return okEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"application/json; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: body,
		})
	case strings.HasSuffix(path, uiResourcePath):
		return okEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: []byte(uiHTML),
		})
	default:
		return nil, fmt.Errorf("unknown management path %q", req.Path)
	}
}

const uiHTML = `<!doctype html>
<html lang="zh-CN" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Safeguard Killer 管理面板</title>
<style>
  :root, [data-theme='dark'] {
    --bg-secondary: #151412;
    --bg-primary: #1d1b18;
    --bg-tertiary: #262320;
    --bg-hover: #2e2a26;
    --text-primary: #f6f4f1;
    --text-secondary: #c9c3bb;
    --text-tertiary: #9c958d;
    --border-color: #3a3530;
    --border-hover: #5a544d;
    --primary-color: #8b8680;
    --success-color: #10b981;
    --error-color: #c65746;
    --viz-success: #0fae78;
    --viz-failure: #cf4b3a;
    --success-badge-bg: rgba(6, 78, 59, 0.3);
    --success-badge-text: #6ee7b7;
    --success-badge-border: #059669;
    --failure-badge-bg: rgba(198, 87, 70, 0.24);
    --failure-badge-text: #f1b0a6;
    --failure-badge-border: rgba(198, 87, 70, 0.5);
    --warn-badge-bg: rgba(224, 170, 20, 0.18);
    --warn-badge-text: #ffd862;
    --count-badge-bg: rgba(139, 134, 128, 0.28);
    --winner-bg: rgba(16, 185, 129, 0.14);
    --shadow-sm: 0 1px 3px 0 rgb(0 0 0 / 0.3);
    --radius-md: 8px;
    --radius-lg: 12px;
    --radius-full: 9999px;
  }

  [data-theme='light'] {
    --bg-secondary: #faf9f5;
    --bg-primary: #f0eee8;
    --bg-tertiary: #e9e6df;
    --bg-hover: #e9e6df;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --border-color: #e3e1db;
    --border-hover: #cecac4;
    --primary-color: #8b8680;
    --viz-success: #10b981;
    --viz-failure: #c65746;
    --success-badge-bg: #d1fae5;
    --success-badge-text: #065f46;
    --success-badge-border: #6ee7b7;
    --failure-badge-bg: rgba(198, 87, 70, 0.14);
    --failure-badge-text: #8a3a30;
    --failure-badge-border: rgba(198, 87, 70, 0.35);
    --warn-badge-bg: rgba(224, 170, 20, 0.16);
    --warn-badge-text: #7a5a00;
    --count-badge-bg: rgba(139, 134, 128, 0.18);
    --winner-bg: rgba(16, 185, 129, 0.12);
    --shadow-sm: 0 1px 2px 0 rgb(0 0 0 / 0.05);
  }

  [data-theme='white'] {
    --bg-secondary: #ffffff;
    --bg-primary: #ffffff;
    --bg-tertiary: #f6f6f6;
    --bg-hover: #f6f6f6;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --border-color: #e5e5e5;
    --border-hover: #cccccc;
    --primary-color: #8b8680;
    --viz-success: #10b981;
    --viz-failure: #c65746;
    --success-badge-bg: #d1fae5;
    --success-badge-text: #065f46;
    --success-badge-border: #6ee7b7;
    --failure-badge-bg: rgba(198, 87, 70, 0.14);
    --failure-badge-text: #8a3a30;
    --failure-badge-border: rgba(198, 87, 70, 0.35);
    --warn-badge-bg: rgba(224, 170, 20, 0.16);
    --warn-badge-text: #7a5a00;
    --count-badge-bg: rgba(139, 134, 128, 0.18);
    --winner-bg: rgba(16, 185, 129, 0.12);
    --shadow-sm: 0 1px 2px 0 rgb(0 0 0 / 0.05);
  }

  * { box-sizing: border-box; }
  html, body {
    margin: 0;
    padding: 0;
    background-color: var(--bg-secondary);
    color: var(--text-primary);
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Oxygen', 'Ubuntu', 'Cantarell', sans-serif;
    font-size: 14px;
    line-height: 1.5;
    -webkit-font-smoothing: antialiased;
  }
  ::-webkit-scrollbar { width: 8px; height: 8px; }
  ::-webkit-scrollbar-track { background: var(--bg-secondary); }
  ::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 9999px; }

  .main-content {
    padding: 70px clamp(20px, 3vw, 48px) 40px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    max-width: 100%;
  }
  .page-header { display: flex; flex-direction: column; gap: 6px; padding-right: 180px; }
  .page-title { margin: 0; font-size: 28px; font-weight: 700; line-height: 1.2; letter-spacing: -0.01em; }
  .page-desc { margin: 0; color: var(--text-secondary); }

  .status-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border-radius: 10px;
    border: 1px solid color-mix(in srgb, var(--border-color) 75%, transparent);
    background: var(--bg-primary);
    flex-wrap: wrap;
  }
  .status-pills { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 12px;
    border-radius: var(--radius-full);
    border: 1px solid color-mix(in srgb, var(--border-color) 75%, transparent);
    background: var(--bg-secondary);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }
  .status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--success-color); box-shadow: 0 0 6px rgba(16, 185, 129, 0.5); }
  .status-dot.err { background: var(--error-color); box-shadow: 0 0 6px rgba(198, 87, 70, 0.6); }
  .status-label { color: var(--text-secondary); }
  .status-value { color: var(--text-primary); font-weight: 700; }

  .toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border: 1px solid var(--border-color);
    border-radius: var(--radius-md);
    padding: 7px 14px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    background-color: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .btn:hover { background-color: var(--bg-hover); border-color: var(--border-hover); }
  .btn:disabled { opacity: 0.6; cursor: progress; }
  .input {
    border: 1px solid var(--border-color);
    border-radius: var(--radius-md);
    padding: 7px 11px;
    font-size: 13px;
    background-color: var(--bg-secondary);
    color: var(--text-primary);
    width: 220px;
  }

  .banner {
    padding: 10px 14px;
    border-radius: var(--radius-md);
    font-size: 13px;
    font-weight: 500;
    background: var(--failure-badge-bg);
    color: var(--failure-badge-text);
    border: 1px solid var(--failure-badge-border);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .banner strong { display: block; margin-bottom: 4px; }

  .card {
    background: var(--bg-primary);
    border: 1px solid var(--border-color);
    border-radius: var(--radius-lg);
    padding: 16px;
    box-shadow: var(--shadow-sm);
  }
  .card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
    padding-bottom: 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border-color) 70%, transparent);
  }
  .card-title { margin: 0; font-size: 15px; font-weight: 700; }

  .cfg-key { color: var(--text-tertiary); font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; }

  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td {
    padding: 8px 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border-color) 65%, transparent);
    text-align: left;
    vertical-align: top;
  }
  th { color: var(--text-tertiary); font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; white-space: nowrap; }
  tbody tr:hover { background-color: color-mix(in srgb, var(--bg-tertiary) 65%, transparent); }
  tbody tr:last-child td { border-bottom: 0; }
  td.nowrap { white-space: nowrap; }
  .empty { text-align: center; padding: 24px; color: var(--text-tertiary); }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 1px 8px;
    font-size: 11px;
    font-weight: 600;
    border-radius: var(--radius-full);
    background: var(--count-badge-bg);
    color: var(--text-secondary);
    white-space: nowrap;
  }
  .badge.ok { background: var(--success-badge-bg); color: var(--success-badge-text); }
  .badge.bad { background: var(--failure-badge-bg); color: var(--failure-badge-text); }
  .badge.warn { background: var(--warn-badge-bg); color: var(--warn-badge-text); }
  .mono { font-family: ui-monospace, 'SF Mono', 'Cascadia Mono', 'JetBrains Mono', Menlo, Consolas, monospace; font-size: 12px; font-variant-numeric: tabular-nums; }
  .subtle { color: var(--text-tertiary); }
  .small { font-size: 12px; }
  .cred-name { font-weight: 650; }
  .err-box {
    margin-top: 6px;
    padding: 6px 10px;
    border-radius: var(--radius-md);
    background: var(--failure-badge-bg);
    color: var(--failure-badge-text);
    border: 1px solid var(--failure-badge-border);
    font-size: 12px;
    font-weight: 600;
    white-space: pre-wrap;
    word-break: break-word;
  }

  /* win-meta 仍在决策表里作为时间副标使用，保留最小定义。 */
  .win-meta { font-size: 11px; color: var(--text-tertiary); margin-top: 2px; }

  details > summary { cursor: pointer; color: var(--text-secondary); font-size: 12px; font-weight: 600; user-select: none; }
  details > summary:hover { color: var(--text-primary); }
  .kv { margin-top: 6px; display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 12px; }
  .kv .k { color: var(--text-tertiary); }
  .kv .v { word-break: break-all; }
  .banner.warn { background: var(--warn-badge-bg); color: var(--warn-badge-text); border-color: color-mix(in srgb, var(--warn-badge-text) 45%, transparent); }
  .banner.ok { background: var(--success-badge-bg); color: var(--success-badge-text); border-color: var(--success-badge-border); }
  .btn.sm { padding: 3px 8px; font-size: 12px; }
  .btn.primary { background: var(--success-badge-bg); color: var(--success-badge-text); border-color: var(--success-badge-border); }
  .btn.danger { color: var(--failure-badge-text); }
  .input.num { width: 100px; }
  .input.grow { flex: 1; min-width: 160px; width: auto; }
  .input.invalid { border-color: var(--error-color); box-shadow: 0 0 0 1px var(--error-color); }
  .card-desc { margin: 0 0 12px; color: var(--text-secondary); font-size: 13px; }
  .form-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 12px 20px; }
  .field { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .field .input { width: 100%; }
  .hint { font-size: 11px; color: var(--text-tertiary); }
  .groups { display: flex; flex-direction: column; gap: 12px; }
  .group-card { border: 1px solid var(--border-color); border-radius: var(--radius-md); padding: 12px; background: var(--bg-secondary); }
  .group-head { display: flex; gap: 10px; flex-wrap: wrap; align-items: flex-end; }
  .group-head .field { flex: 1; min-width: 180px; }
  .model-list { list-style: none; margin: 10px 0 8px; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  .model-row { display: flex; align-items: center; gap: 6px; }
  .model-idx { width: 22px; text-align: right; color: var(--text-tertiary); }
  .add-row { display: flex; gap: 6px; align-items: center; }
  .attempts { margin: 6px 0 0; padding-left: 20px; font-size: 12px; }
  .attempts li { padding: 1px 4px; border-radius: 4px; }
  .attempts li.served { background: var(--winner-bg); font-weight: 650; }

  .tabs {
    display: flex;
    gap: 6px;
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 8px;
    flex-wrap: wrap;
  }
  .tab-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border: 1px solid transparent;
    border-radius: var(--radius-md);
    padding: 7px 14px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    background: transparent;
    color: var(--text-secondary);
  }
  .tab-btn:hover { background: var(--bg-tertiary); color: var(--text-primary); }
  .tab-btn.active {
    background: var(--bg-primary);
    color: var(--text-primary);
    border-color: var(--border-color);
    box-shadow: var(--shadow-sm);
  }
  .tab-pane { display: none; flex-direction: column; gap: 20px; }
  .tab-pane.active { display: flex; }

  .feature-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px 18px; }
  .feature-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--border-color) 75%, transparent);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
    cursor: pointer;
  }
  .feature-item input[type="checkbox"] { margin-top: 3px; accent-color: var(--success-color); cursor: pointer; }
  .feature-title { font-weight: 650; font-size: 13px; color: var(--text-primary); display: block; }
  .feature-desc { font-size: 12px; color: var(--text-secondary); display: block; margin-top: 2px; }
</style>
</head>
<body>
<main class="main-content">
  <header class="page-header">
    <h1 class="page-title">Safeguard Killer</h1>
    <p class="page-desc">在 Claude Code、Claude、OpenAI/Codex 与 Gemini 模型上清除模型侧安全护栏与 auto-mode 分类器，用智能分组（5 分钟缓存粘性的故障转移链）绕开拒答。可与 <a href="https://github.com/esauvisky/CLIProxyAPI-Model-List-Tweaker-Plugin" target="_blank" rel="noopener" style="color:var(--text-primary);">模型重排/重命名</a> 配合使用。</p>
  </header>

  <div class="status-bar">
    <div class="status-pills">
      <span class="status-pill"><span class="status-dot" id="statusDot"></span><span class="status-label">版本</span><span class="status-value mono" id="versionVal">-</span></span>
      <span class="status-pill"><span class="status-label">分组</span><span class="status-value" id="groupCount">0</span></span>
      <span class="status-pill"><span class="status-label">会话</span><span class="status-value" id="sessCount">0</span></span>
      <span class="status-pill"><span class="status-label">最后更新</span><span class="status-value" id="lastUpdated">从未</span></span>
    </div>
    <div class="toolbar">
      <button class="btn" id="refreshBtn" onclick="refresh()">刷新</button>
      <span class="badge warn" id="dirtyBadge" style="display:none;">有未保存的修改</span>
      <button class="btn" id="discardBtn" onclick="discardDraft()" disabled>放弃修改</button>
      <button class="btn primary" id="saveBtn" onclick="save()" disabled>保存</button>
    </div>
  </div>

  <div id="errorBanner" class="banner" style="display:none;"></div>
  <div id="saveBanner" class="banner" style="display:none;"></div>

  <nav class="tabs" id="tabBar">
    <button class="tab-btn active" data-tab="settings">通用设置</button>
    <button class="tab-btn" data-tab="groups">智能分组 <span class="badge" id="tabGroupCount">0</span></button>
    <button class="tab-btn" data-tab="sessions">会话 <span class="badge" id="tabSessCount">0</span></button>
    <button class="tab-btn" data-tab="decisions">最近决策 <span class="badge" id="tabDecCount">0</span></button>
  </nav>

  <div class="tab-pane active" id="pane-settings">
    <section class="card">
      <div class="card-header"><h2 class="card-title">分组与会话设置</h2></div>
      <div class="form-grid" id="settingsForm">
        <label class="field"><span class="cfg-key">空闲分钟数</span>
          <input class="input" type="number" step="1" min="0" data-field="idle_minutes" data-setting="idle_minutes">
          <span class="hint">会话超过该时长没有新提示后，可换到分组里的其它模型（填 0 表示关闭粘性）。</span></label>
        <label class="field"><span class="cfg-key">流预取字节数</span>
          <input class="input" type="number" step="1" min="1" data-field="stream_prefetch_bytes" data-setting="stream_prefetch_bytes">
          <span class="hint">分组流在转发给客户端之前先缓冲并检查拒答的可见文本字节数。</span></label>
      </div>
    </section>

    <section class="card">
      <div class="card-header"><h2 class="card-title">安全护栏与客户端适配</h2></div>
      <p class="card-desc">控制 Safeguard Killer 如何剥离隐藏的安全提示、拒答污染头与工具分类器，以及如何在模型拒答到达客户端之前拦截它。</p>
      <div class="feature-grid" id="featuresForm">
        <label class="feature-item">
          <input type="checkbox" data-feature="fallback_on_refusal">
          <span>
            <span class="feature-title">遇拒答自动切换</span>
            <span class="feature-desc">在流预取阶段识别安全护栏中止、内容过滤、空响应、纯文本拒答与授权范围推诿，并自动切换到分组里的下一个模型。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="client_fallback_notices">
          <span>
            <span class="feature-title">客户端回退与换模型提示</span>
            <span class="feature-desc">分组内模型拒答/失败或当前模型发生变化时，向客户端输出一条可见提示，并在后续轮次把它从会话历史中剥离，避免模型看到。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="rewrite_cyber_system_prompt">
          <span>
            <span class="feature-title">改写网络安全/两用能力系统提示门禁</span>
            <span class="feature-desc">替换 Claude Code 系统提示里内置的“要求明确的授权背景”与 Fable/Mythos 两用限制段落，让模型直接作答而不再追问使用范围。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="strip_refusal_taint_headers">
          <span>
            <span class="feature-title">剥离拒答污染头</span>
            <span class="feature-desc">移除 Claude Code 在一轮被标记之后附加到每个请求上的 x-is-refusal-fallback、x-cc-fallback-*、x-cc-original-request-id 与 x-cc-atis 请求头。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="disable_upstream_server_fallback">
          <span>
            <span class="feature-title">禁用上游服务端换模型</span>
            <span class="feature-desc">移除请求体里的 fallbacks 字段与 server-side-fallback / fallback-credit beta 头，避免 Anthropic 服务端在流式输出中途悄悄换掉你的模型。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="bypass_auto_mode_classifier">
          <span>
            <span class="feature-title">绕过 auto-mode 工具分类器</span>
            <span class="feature-desc">从请求中剥离 safeguards 与 dangerous-tool-use beta，给分组工具调用打上 not_flagged 的 safeguard_results，并让本地 auto-mode 分类器检查在 0 毫秒内直接放行。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="neutralize_classifier_stop_nudges">
          <span>
            <span class="feature-title">中和分类器中止附带的催促语</span>
            <span class="feature-desc">把 Claude Code 注入的“已被安全分类器中止——不要再生成该内容”类提示与工具结果墓碑，改写为中性的续写提示。</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="strip_restrictive_system_reminders">
          <span>
            <span class="feature-title">剥离限制性 &lt;system-reminder&gt; 块</span>
            <span class="feature-desc">从消息中移除对话中途注入的 &lt;system-reminder&gt; 或 role=system 拒答与两用警告，同时保留宿主正常的提醒。</span>
          </span>
        </label>
      </div>
      <div class="form-grid" style="margin-top: 14px;" id="noticePrefixForm">
        <label class="field"><span class="cfg-key">客户端提示前缀</span>
          <input class="input mono" type="text" placeholder="⚡ [Safeguard Killer] " data-field="features.notice_prefix" data-feature-text="notice_prefix">
          <span class="hint">加在客户端回退/换模型提示前面的前缀（后续轮次会从模型上下文里剥离）。</span></label>
      </div>
    </section>
  </div>

  <div class="tab-pane" id="pane-groups">
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">智能分组</h2>
        <button class="btn sm" data-action="add-group" id="addGroupBtn">新增分组</button>
      </div>
      <p class="card-desc">智能分组对外暴露一个虚拟模型 ID，同时也可以用成员模型的真实名称直接进入：客户端请求任一成员模型时，请求会自动接管并从这个模型开始尝试，被拒答再往分组里的下一个模型滚动，客户端无需改配置。成员模型在多个模型之间自动绕开安全护栏拒答、内容过滤与授权范围说教。某个模型不拒答之后，会话会在服务端 prompt 缓存仍热的 5 分钟内固定使用它；超过 5 分钟后重置，重新从分组头部开始尝试。</p>
      <div class="groups" id="groupsEditor"></div>
      <datalist id="modelList"></datalist>
    </section>
  </div>

  <div class="tab-pane" id="pane-sessions">
    <section class="card">
      <div class="card-header"><h2 class="card-title">会话</h2></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>会话</th><th>命中模型</th><th>分组</th><th>绑定时间</th><th>最后访问</th><th>空闲 / 缓存到期</th></tr></thead>
          <tbody id="sessBody"></tbody>
        </table>
      </div>
    </section>
  </div>

  <div class="tab-pane" id="pane-decisions">
    <section class="card">
      <div class="card-header"><h2 class="card-title">最近决策</h2></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>时间</th><th>类型</th><th>模型</th><th>会话</th><th>选中</th><th>原因</th></tr></thead>
          <tbody id="decBody"></tbody>
        </table>
      </div>
    </section>
  </div>
</main>

<script>
// 状态走无鉴权的 resource 路由：宿主只对 /v0/management 校验管理密钥，
// 因此面板打开即可读到状态，不再出现 401 与密钥输入框。
const STATE_API_URL = '/v0/resource/plugins/safeguard-killer/state';
// 写入仍必须走需要密钥的 v8 管理配置接口，密钥只从宿主已持久化的存储里取。
const CONFIG_API_URL = '/v8/management/config/plugins/configs/safeguard-killer';
const REFRESH_MS = 10000;
const APPLY_WAIT_MS = 1500;
const ZERO_TIME = '0001-01-01T00:00:00Z';

let state = null;
let lastOkAt = null;
// Offset between server clock (state.now) and browser clock, so countdowns use server time.
let clockSkewMs = 0;

// Editable copy of the config. Live refreshes never touch it while it has unsaved edits.
let draft = null;
let dirty = false;
let saving = false;
// Canonical form of the state.config the draft was last loaded from.
let draftSourceCanon = null;

function syncThemeWithCPA() {
  const root = document.documentElement;
  const apply = (mode) => root.setAttribute('data-theme', mode === 'white' || mode === 'light' ? mode : 'dark');
  // Embedded in the CPA iframe (same origin): follow the parent's theme attribute.
  if (window.parent !== window) {
    const parentDoc = window.parent.document.documentElement;
    const syncFromParent = () => {
      const attr = parentDoc.getAttribute('data-theme');
      apply(attr === 'dark' || attr === 'white' ? attr : 'light');
    };
    syncFromParent();
    new MutationObserver(syncFromParent).observe(parentDoc, { attributes: true, attributeFilter: ['data-theme'] });
    return;
  }
  // Opened standalone: use the CPA theme stored in localStorage, if the user ever set one.
  const raw = localStorage.getItem('cli-proxy-theme');
  if (raw !== null) {
    const s = JSON.parse(raw).state;
    if (s.theme === 'dark' || (s.theme === 'auto' && s.resolvedTheme === 'dark')) { apply('dark'); return; }
    if (s.theme === 'white' || s.theme === 'auto') { apply('white'); return; }
    if (s.theme === 'light') { apply('light'); return; }
  }
  apply(window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
}

// CPA Management Center persists its auth store as 'enc::v2::' + base64(JSON XOR
// 'cli-proxy-api-webui::secure-storage|' + host). See its src/utils/encryption.ts.
function deobfuscateCPAStore(payload) {
  const prefix = 'enc::v2::';
  if (!payload.startsWith(prefix)) {
    throw new Error('cli-proxy-auth 不是本页面能识别的 enc::v2:: 格式');
  }
  const bin = atob(payload.slice(prefix.length));
  const keyBytes = new TextEncoder().encode('cli-proxy-api-webui::secure-storage|' + window.location.host);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i) ^ keyBytes[i % keyBytes.length];
  return new TextDecoder().decode(out);
}

// 从 CPA 主面板的同源 localStorage 里取管理密钥。
// 主面板只在用户勾选「记住密码」时才持久化它；没勾选时这里返回空串，
// 此时保存会引导用户去主面板改配置，而不是弹一个让人手填密钥的输入框。
// [返回] 已 trim 的密钥，或空串
// 最近修改时间 2026-11-14 00:08:00, 修改, 去掉面板内手填密钥的回退路径，只保留宿主已持久化的密钥。
function detectStoredManagementKey() {
  let authRaw;
  try {
    authRaw = localStorage.getItem('cli-proxy-auth');
  } catch (err) {
    return '';
  }
  if (authRaw === null || authRaw === '') return '';
  try {
    const k = JSON.parse(deobfuscateCPAStore(authRaw)).state.managementKey;
    return typeof k === 'string' ? k.trim() : '';
  } catch (err) {
    return '';
  }
}

// 组装管理接口写请求所需的鉴权头。
// [返回] 有宿主已持久化的密钥时返回鉴权头，否则返回空对象
function mgmtAuthHeaders() {
  const key = detectStoredManagementKey();
  const headers = {};
  if (key !== '') {
    headers['Authorization'] = 'Bearer ' + key;
    headers['X-Management-Key'] = key;
  }
  return headers;
}

function esc(v) {
  return String(v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function isZero(t) {
  return !t || t === ZERO_TIME || t.startsWith('0001-01-01');
}

function serverNow() {
  return Date.now() + clockSkewMs;
}

function fmtDuration(ms) {
  const neg = ms < 0;
  let s = Math.floor(Math.abs(ms) / 1000);
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); s -= m * 60;
  let out;
  if (d > 0) out = d + '天 ' + h + '小时';
  else if (h > 0) out = h + '小时 ' + m + '分';
  else if (m > 0) out = m + '分 ' + s + '秒';
  else out = s + '秒';
  return neg ? '-' + out : out;
}

function relText(t) {
  const diff = new Date(t).getTime() - serverNow();
  if (Math.abs(diff) < 1000) return '刚刚';
  return diff > 0 ? fmtDuration(diff) + '后' : fmtDuration(-diff) + '前';
}

function absText(t) {
  return new Date(t).toLocaleString('zh-CN', { hour12: false });
}

// Live-updating relative time; refreshed every second by tick().
function rel(t) {
  if (isZero(t)) return '<span class="subtle">-</span>';
  return '<span class="live-rel" data-t="' + esc(t) + '" title="' + esc(absText(t)) + '">' + esc(relText(t)) + '</span>';
}

function countdown(t) {
  if (isZero(t)) return '<span class="subtle">-</span>';
  return '<span class="live-cd mono" data-t="' + esc(t) + '" title="' + esc(absText(t)) + '">' + countdownText(t) + '</span>';
}

function countdownText(t) {
  const diff = new Date(t).getTime() - serverNow();
  if (diff <= 0) return '<span class="badge warn">空闲中，下次提示时重新选择</span>';
  return esc(fmtDuration(diff));
}

function tick() {
  for (const el of document.querySelectorAll('.live-rel')) el.textContent = relText(el.dataset.t);
  for (const el of document.querySelectorAll('.live-cd')) el.innerHTML = countdownText(el.dataset.t);
  if (lastOkAt !== null) {
    document.getElementById('lastUpdated').textContent = fmtDuration(Date.now() - lastOkAt) + '前';
  }
}

function truncate(s, n) {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

// ---- draft (editable config) ----

// Go encodes nil slices as null.
function groupsOf(cfg) {
  return cfg.groups === null ? [] : cfg.groups;
}

const DEFAULT_NOTICE_PREFIX = '⚡ [Safeguard Killer] ';
const FEATURE_KEYS = [
  'fallback_on_refusal',
  'client_fallback_notices',
  'rewrite_cyber_system_prompt',
  'strip_refusal_taint_headers',
  'disable_upstream_server_fallback',
  'bypass_auto_mode_classifier',
  'neutralize_classifier_stop_nudges',
  'strip_restrictive_system_reminders',
];

function featuresOf(cfg) {
  const raw = cfg.features || {};
  const out = {};
  for (const k of FEATURE_KEYS) {
    out[k] = typeof raw[k] === 'boolean' ? raw[k] : true;
  }
  out.notice_prefix = typeof raw.notice_prefix === 'string' && raw.notice_prefix !== '' ? raw.notice_prefix : DEFAULT_NOTICE_PREFIX;
  return out;
}

function normStrategy(s) {
  return s === 'random' ? s : 'order';
}

function canonConfig(cfg) {
  return JSON.stringify({
    idle_minutes: cfg.idle_minutes,
    stream_prefetch_bytes: cfg.stream_prefetch_bytes,
    groups: groupsOf(cfg).map(g => ({ id: g.id, name: g.name, strategy: normStrategy(g.strategy), models: g.models })),
    features: featuresOf(cfg),
  });
}

function loadDraft(cfg) {
  draft = {
    settings: {
      idle_minutes: String(cfg.idle_minutes),
      stream_prefetch_bytes: String(cfg.stream_prefetch_bytes),
    },
    features: featuresOf(cfg),
    groups: groupsOf(cfg).map(g => ({ id: g.id, name: g.name, strategy: normStrategy(g.strategy), models: g.models.slice(), pending: '' })),
  };
  draftSourceCanon = canonConfig(cfg);
  for (const el of document.querySelectorAll('[data-setting]')) el.value = draft.settings[el.dataset.setting];
  for (const el of document.querySelectorAll('[data-feature]')) el.checked = Boolean(draft.features[el.dataset.feature]);
  for (const el of document.querySelectorAll('[data-feature-text]')) el.value = draft.features[el.dataset.featureText];
  setDirty(false);
  markInvalid(new Set());
  renderGroups();
}

function setDirty(v) {
  dirty = v;
  document.getElementById('dirtyBadge').style.display = v ? 'inline-flex' : 'none';
  document.getElementById('saveBtn').disabled = !v || saving;
  document.getElementById('discardBtn').disabled = !v || saving;
}

function discardDraft() {
  hideSaveBanner();
  loadDraft(state.config);
}

function parseNum(s) {
  return s.trim() === '' ? NaN : Number(s);
}

// Mirrors validateConfig in config.go. Returns {errors: [[field, message]], cfg}.
function validateDraft() {
  const errors = [];
  const s = draft.settings;
  const idle = parseNum(s.idle_minutes);
  if (!Number.isInteger(idle) || idle < 0) errors.push(['idle_minutes', '空闲分钟数必须是非负整数，当前为 "' + s.idle_minutes + '"']);
  const prefetch = parseNum(s.stream_prefetch_bytes);
  if (!Number.isInteger(prefetch) || prefetch < 1) errors.push(['stream_prefetch_bytes', '流预取字节数必须是不小于 1 的整数，当前为 "' + s.stream_prefetch_bytes + '"']);

  const groups = draft.groups.map(g => ({ id: g.id.trim(), name: g.name.trim(), strategy: normStrategy(g.strategy), models: g.models.map(m => m.trim()) }));
  const ids = new Set();
  groups.forEach((g, i) => {
    const label = '分组 #' + (i + 1) + (g.id ? ' (' + g.id + ')' : '');
    if (g.id === '') errors.push(['groups.' + i + '.id', label + ' 需要填写分组代号']);
    else if (ids.has(g.id)) errors.push(['groups.' + i + '.id', label + '：分组代号 ' + g.id + ' 被重复使用']);
    ids.add(g.id);
    if (g.name === '') errors.push(['groups.' + i + '.name', label + ' 需要填写显示名称']);
    if (g.models.length === 0) errors.push(['groups.' + i + '.add', label + ' 至少需要一个模型']);
    g.models.forEach((m, j) => {
      if (m === '') errors.push(['groups.' + i + '.models.' + j, label + '：第 ' + (j + 1) + ' 个模型为空']);
    });
  });
  groups.forEach((g, i) => {
    g.models.forEach((m, j) => {
      if (ids.has(m)) errors.push(['groups.' + i + '.models.' + j, '分组 ' + g.id + ' 把分组 ' + m + ' 当作目标模型；分组不能嵌套']);
    });
  });

  const features = Object.assign({}, draft.features, {
    notice_prefix: draft.features.notice_prefix !== '' ? draft.features.notice_prefix : DEFAULT_NOTICE_PREFIX,
  });

  return {
    errors: errors,
    cfg: {
      idle_minutes: idle,
      stream_prefetch_bytes: prefetch,
      groups: groups,
      features: features,
    },
  };
}

function markInvalid(fields) {
  for (const el of document.querySelectorAll('[data-field]')) el.classList.toggle('invalid', fields.has(el.dataset.field));
}

// ---- save ----

function showSaveBanner(kind, title, detail) {
  const banner = document.getElementById('saveBanner');
  banner.className = 'banner' + (kind === 'error' ? '' : ' ' + kind);
  banner.innerHTML = '<strong>' + esc(title) + '</strong>' + esc(detail);
  banner.style.display = 'block';
}

function hideSaveBanner() {
  document.getElementById('saveBanner').style.display = 'none';
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

function isPlainObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

async function save() {
  hideSaveBanner();
  const v = validateDraft();
  markInvalid(new Set(v.errors.map(e => e[0])));
  if (v.errors.length > 0) {
    showSaveBanner('error', '未保存：请先修正 ' + v.errors.length + ' 个不合法字段' , v.errors.map(e => '- ' + e[1]).join('\n'));
    return;
  }
  // 没有宿主已持久化的密钥就不写配置：不弹输入框，直接给出可执行的替代路径。
  if (detectStoredManagementKey() === '') {
    showSaveBanner('error', '未保存：本页面拿不到管理密钥',
      '面板不会向你索要密钥。请在 CPA 主面板的「插件管理 → Safeguard Killer → 编辑配置」里改同样的字段，' +
      '或先在主面板勾选「记住密码」后再回来保存。');
    return;
  }
  saving = true;
  setDirty(dirty);
  try {
    await saveValidated(v.cfg);
  } finally {
    saving = false;
    setDirty(dirty);
  }
}

// saveValidated 把面板草稿写回 CPA 的插件配置节点。
// [参数] cfg：已通过 validateDraft 校验的配置对象
// [返回] 无；成功与失败都通过 saveBanner 呈现
// 最近修改时间 2026-11-14 00:08:00, 修改, 移除手填密钥回退，鉴权只依赖宿主已持久化的密钥。
async function saveValidated(cfg) {
  const getResp = await fetch(CONFIG_API_URL, { method: 'GET', headers: mgmtAuthHeaders(), cache: 'no-store' });
  const getText = await getResp.text();
  let node;
  let etag = null;
  if (getResp.status === 404) {
    // No plugin config node yet; fetch the parent map ETag so the PUT creates it.
    node = {};
    const parentResp = await fetch('/v8/management/config/plugins/configs', { method: 'GET', headers: mgmtAuthHeaders(), cache: 'no-store' });
    if (parentResp.ok) etag = parentResp.headers.get('ETag');
  } else if (!getResp.ok) {
    showSaveBanner('error', '未保存：读取 ' + CONFIG_API_URL + ' 返回 HTTP ' + getResp.status, getText);
    return;
  } else {
    node = JSON.parse(getText);
    if (!isPlainObject(node)) throw new Error(CONFIG_API_URL + ' 上的配置节点不是 JSON 对象：' + truncate(getText, 500));
    etag = getResp.headers.get('ETag');
  }
  if (etag !== null) etag = etag.replace(/^W\//, '');

  const merged = Object.assign({}, node);
  merged.idle_minutes = cfg.idle_minutes;
  merged.stream_prefetch_bytes = cfg.stream_prefetch_bytes;
  delete merged.weights;
  if (cfg.groups.length > 0) merged.groups = cfg.groups;
  else delete merged.groups;
  merged.features = cfg.features;

  const headers = mgmtAuthHeaders();
  headers['Content-Type'] = 'application/json';
  if (etag !== null) headers['If-Match'] = etag;
  const putResp = await fetch(CONFIG_API_URL, { method: 'PUT', headers: headers, body: JSON.stringify(merged), cache: 'no-store' });
  const putText = await putResp.text();
  if (putResp.status === 412) {
    showSaveBanner('error', '未保存：配置已在别处变更，已重新载入', putText);
    if (await refresh()) loadDraft(state.config);
    return;
  }
  if (!putResp.ok) {
    showSaveBanner('error', '未保存：写入 ' + CONFIG_API_URL + ' 返回 HTTP ' + putResp.status, putText);
    return;
  }

  // The host now calls plugin.reconfigure and re-registers the group models.
  setDirty(false);
  showSaveBanner('ok', '已保存', '正在等待插件应用新配置…');
  await sleep(APPLY_WAIT_MS);
  if (!(await refresh())) {
    showSaveBanner('warn', '已保存，但状态刷新失败', '请查看上方错误；插件可能已应用该配置，也可能没有。');
    return;
  }
  if (canonConfig(state.config) === canonConfig(cfg)) {
    showSaveBanner('ok', '已保存并生效', '');
    loadDraft(state.config);
    return;
  }
  // The plugin rejected the config (validateConfig failed) and kept the old one; keep the draft as saved.
  showSaveBanner('warn', '已写入但插件未应用，请查看 CPA 日志', '配置节点已写入，但运行中的插件仍然报告旧配置。');
}

// ---- groups editor ----

function groupFieldAttr(i, rest) {
  return ' data-field="groups.' + i + '.' + rest + '"';
}

const STRATEGY_HINTS = {
  order: '按顺序：自上而下依次尝试模型，只有当前模型拒答或报错时才前进。命中第一个正常作答的模型后固定 5 分钟（缓存保持温热），5 分钟后重新从第 1 个开始。',
  random: '随机：新会话或缓存已过期（5 分钟以上）的会话在分组内均匀随机挑选起始模型；遇拒答继续回退，命中后固定 5 分钟。',
};

function renderGroups() {
  const root = document.getElementById('groupsEditor');
  document.getElementById('groupCount').textContent = String(draft.groups.length);
  document.getElementById('tabGroupCount').textContent = String(draft.groups.length);
  if (draft.groups.length === 0) {
    root.innerHTML = '<div class="empty">还没有配置智能分组。新增一个即可对外暴露一个虚拟模型，在多个模型之间自动绕开安全护栏拒答。</div>';
    return;
  }
  root.innerHTML = draft.groups.map((g, i) => {
    const strat = normStrategy(g.strategy);
    const models = g.models.map((m, j) =>
      '<li class="model-row">' +
        '<span class="model-idx mono">' + (j + 1) + '.</span>' +
        '<input class="input grow mono" list="modelList" value="' + esc(m) + '" data-group="' + i + '" data-model="' + j + '"' + groupFieldAttr(i, 'models.' + j) + '>' +
        '<button class="btn sm" data-action="model-up" data-group="' + i + '" data-model="' + j + '"' + (j === 0 ? ' disabled' : '') + ' title="上移">&uarr;</button>' +
        '<button class="btn sm" data-action="model-down" data-group="' + i + '" data-model="' + j + '"' + (j === g.models.length - 1 ? ' disabled' : '') + ' title="下移">&darr;</button>' +
        '<button class="btn sm danger" data-action="model-remove" data-group="' + i + '" data-model="' + j + '" title="移除">&times;</button>' +
      '</li>'
    ).join('');
    return '<div class="group-card">' +
      '<div class="group-head">' +
        '<label class="field"><span class="cfg-key">分组代号（客户端请求用）</span>' +
          '<input class="input mono" value="' + esc(g.id) + '" data-group="' + i + '" data-gfield="id"' + groupFieldAttr(i, 'id') + '></label>' +
        '<label class="field"><span class="cfg-key">显示名称</span>' +
          '<input class="input" value="' + esc(g.name) + '" data-group="' + i + '" data-gfield="name"' + groupFieldAttr(i, 'name') + '></label>' +
        '<label class="field"><span class="cfg-key">路由策略</span>' +
          '<select class="input" data-group="' + i + '" data-gfield="strategy"' + groupFieldAttr(i, 'strategy') + '>' +
            '<option value="order"' + (strat === 'order' ? ' selected' : '') + '>按顺序（仅作回退，粘性 5 分钟）</option>' +
            '<option value="random"' + (strat === 'random' ? ' selected' : '') + '>随机（均匀随机，粘性 5 分钟）</option>' +
          '</select></label>' +
        '<button class="btn sm danger" data-action="group-remove" data-group="' + i + '">删除分组</button>' +
      '</div>' +
      '<div class="hint" style="margin-top:6px" data-group-hint="' + i + '">' + esc(STRATEGY_HINTS[strat]) + '</div>' +
      '<ol class="model-list">' + models + '</ol>' +
      '<div class="add-row">' +
        '<input class="input grow mono" list="modelList" placeholder="添加模型（可选择或直接输入）" value="' + esc(g.pending) + '" data-group="' + i + '" data-gfield="pending"' + groupFieldAttr(i, 'add') + '>' +
        '<button class="btn sm" data-action="model-add" data-group="' + i + '">添加模型</button>' +
      '</div>' +
    '</div>';
  }).join('');
}

function swap(list, a, b) {
  const t = list[a]; list[a] = list[b]; list[b] = t;
}

function addPendingModel(gi) {
  const g = draft.groups[gi];
  const m = g.pending.trim();
  if (m === '') return;
  g.models.push(m);
  g.pending = '';
  setDirty(true);
  renderGroups();
  document.querySelector('[data-group="' + gi + '"][data-gfield="pending"]').focus();
}

function onEditorClick(ev) {
  const btn = ev.target.closest('[data-action]');
  if (btn === null) return;
  const action = btn.dataset.action;
  if (action === 'add-group') {
    draft.groups.push({ id: '', name: '', strategy: 'order', models: [], pending: '' });
  } else {
    const gi = Number(btn.dataset.group);
    const g = draft.groups[gi];
    const mi = Number(btn.dataset.model);
    if (action === 'group-remove') draft.groups.splice(gi, 1);
    else if (action === 'model-add') { addPendingModel(gi); return; }
    else if (action === 'model-up') swap(g.models, mi, mi - 1);
    else if (action === 'model-down') swap(g.models, mi, mi + 1);
    else if (action === 'model-remove') g.models.splice(mi, 1);
    else throw new Error('未知编辑器操作 ' + action);
  }
  setDirty(true);
  renderGroups();
}

function onEditorInput(ev) {
  const el = ev.target;
  if (el.dataset.group === undefined) return;
  const g = draft.groups[Number(el.dataset.group)];
  if (el.dataset.model !== undefined) {
    g.models[Number(el.dataset.model)] = el.value;
    setDirty(true);
  } else if (el.dataset.gfield === 'pending') {
    // Text in the add box is not part of the config until "Add model".
    g.pending = el.value;
  } else {
    g[el.dataset.gfield] = el.value;
    if (el.dataset.gfield === 'strategy') {
      const hint = document.querySelector('[data-group-hint="' + el.dataset.group + '"]');
      if (hint !== null) hint.textContent = STRATEGY_HINTS[normStrategy(el.value)];
    }
    setDirty(true);
  }
}

// ---- live state ----

// 后端的 reason 与 attempt 结果目前是英文常量，这里只在展示层翻译，不改动后端逻辑与既有断言。
const REASON_TEXT = {
  'served': '已应答',
  'every model failed': '所有模型都失败',
};

function zhReason(text) {
  if (!text) return '';
  const mapped = REASON_TEXT[text];
  if (mapped !== undefined) return mapped;
  const served = 'served by ';
  if (text.startsWith(served)) return '由 ' + text.slice(served.length) + ' 应答';
  return text;
}

function zhAttempt(result) {
  if (result === 'served') return '已应答';
  const failed = 'failed: ';
  if (result.startsWith(failed)) return '失败：' + result.slice(failed.length);
  return result;
}

function zhKind(kind) {
  if (kind === 'group') return '分组路由';
  return kind;
}

function renderSessions() {
  const body = document.getElementById('sessBody');
  if (state.sessions.length === 0) {
    body.innerHTML = '<tr><td colspan="6" class="empty">暂无绑定的会话。</td></tr>';
    return;
  }
  body.innerHTML = state.sessions.map(s =>
    '<tr>' +
      '<td class="mono nowrap" title="' + esc(s.session) + '">' + esc(truncate(s.session, 24)) + '</td>' +
      '<td class="mono">' + esc(s.auth_id) + '</td>' +
      '<td class="mono">' + esc(s.model) + '</td>' +
      '<td class="nowrap">' + rel(s.bound_at) + '</td>' +
      '<td class="nowrap">' + rel(s.last_seen) + '</td>' +
      '<td class="nowrap">' + countdown(s.idle_expires_at) + '</td>' +
    '</tr>'
  ).join('');
}

function renderAttempts(d) {
  return '<ol class="attempts">' + d.attempts.map(at =>
    '<li class="' + (at.result === 'served' && at.model === d.auth_id ? 'served' : '') + '">' +
      '<span class="mono">' + esc(at.model) + '</span> &rarr; ' + esc(zhAttempt(at.result)) +
    '</li>'
  ).join('') + '</ol>';
}

function renderDecisions() {
  const body = document.getElementById('decBody');
  if (state.decisions.length === 0) {
    body.innerHTML = '<tr><td colspan="6" class="empty">暂无决策记录。</td></tr>';
    return;
  }
  const sorted = state.decisions.slice().sort((a, b) => new Date(b.time) - new Date(a.time));
  body.innerHTML = sorted.map(d => {
    // 删掉配额调度后只剩分组决策；attempts 是 omitempty。
    let extra = '';
    if (d.attempts && d.attempts.length > 0) extra = renderAttempts(d);
    const chosen = d.auth_id ? '<span class="mono">' + esc(d.auth_id) + '</span>' : '<span class="badge bad">无</span>';
    return '<tr>' +
      '<td class="nowrap">' + rel(d.time) + '<div class="win-meta">' + esc(absText(d.time)) + '</div></td>' +
      '<td><span class="badge">' + esc(zhKind(d.kind)) + '</span></td>' +
      '<td class="mono">' + esc(d.model) + '</td>' +
      '<td class="mono nowrap" title="' + esc(d.session ? d.session : '') + '">' + (d.session ? esc(truncate(d.session, 16)) : '<span class="subtle">-</span>') + '</td>' +
      '<td>' + chosen + '</td>' +
      '<td>' + esc(zhReason(d.reason)) + extra + '</td>' +
    '</tr>';
  }).join('');
}

function render() {
  document.getElementById('versionVal').textContent = state.version;
  document.getElementById('sessCount').textContent = String(state.sessions.length);
  document.getElementById('tabSessCount').textContent = String(state.sessions.length);
  document.getElementById('tabDecCount').textContent = String(state.decisions.length);
  document.getElementById('modelList').innerHTML = state.models.map(m => '<option value="' + esc(m) + '"></option>').join('');
  // Follow config changes made elsewhere only while there is nothing unsaved here.
  if (draft === null || (!dirty && !saving && canonConfig(state.config) !== draftSourceCanon)) loadDraft(state.config);
  renderSessions();
  renderDecisions();
}

function showError(title, detail) {
  const banner = document.getElementById('errorBanner');
  const stale = lastOkAt === null
    ? '尚未成功加载过数据。'
    : '当前显示的是 ' + new Date(lastOkAt).toLocaleTimeString('zh-CN', { hour12: false }) + ' 的旧数据。';
  banner.innerHTML = '<strong>' + esc(title) + '</strong>' + esc(detail) + '\n' + esc(stale);
  banner.style.display = 'block';
  document.getElementById('statusDot').classList.add('err');
}

function clearError() {
  document.getElementById('errorBanner').style.display = 'none';
  document.getElementById('statusDot').classList.remove('err');
}

// Refreshes are serialized so a save can wait for a state that reflects it.
let refreshChain = Promise.resolve(true);
let refreshBusy = false;
function refresh() {
  refreshChain = refreshChain.then(doRefresh);
  return refreshChain;
}

// Returns whether a new state was loaded.
// 状态接口是无鉴权的 resource 路由，因此这里既不带鉴权头，也不再处理 401/403。
// 最近修改时间 2026-11-14 00:10:00, 修改, 状态改走无鉴权 resource 路由，去掉密钥输入框提示。
async function doRefresh() {
  refreshBusy = true;
  const btn = document.getElementById('refreshBtn');
  btn.disabled = true;
  try {
    let resp;
    try {
      resp = await fetch(STATE_API_URL, { method: 'GET', cache: 'no-store' });
    } catch (err) {
      showError('网络错误，请求 ' + STATE_API_URL + ' 失败', String(err));
      return false;
    }
    const text = await resp.text();
    if (!resp.ok) {
      showError('HTTP ' + resp.status + ' ' + resp.statusText + '，来自 ' + STATE_API_URL, text);
      return false;
    }
    let next;
    try {
      next = JSON.parse(text);
    } catch (err) {
      showError('来自 ' + STATE_API_URL + ' 的响应不是合法 JSON：' + err.message, truncate(text, 2000));
      return false;
    }
    // Go encodes empty slices as null; normalize so renderers can rely on arrays.
    for (const k of ['sessions', 'decisions', 'models']) if (next[k] === null) next[k] = [];
    state = next;
    clockSkewMs = new Date(state.now).getTime() - Date.now();
    render();
    lastOkAt = Date.now();
    clearError();
    tick();
    return true;
  } finally {
    refreshBusy = false;
    btn.disabled = false;
  }
}

const groupsCard = document.getElementById('groupsEditor').closest('.card');
groupsCard.addEventListener('click', onEditorClick);
groupsCard.addEventListener('input', onEditorInput);
groupsCard.addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && ev.target.dataset.gfield === 'pending') {
    ev.preventDefault();
    addPendingModel(Number(ev.target.dataset.group));
  }
});

document.getElementById('settingsForm').addEventListener('input', (ev) => {
  draft.settings[ev.target.dataset.setting] = ev.target.value;
  setDirty(true);
});

document.getElementById('featuresForm').addEventListener('change', (ev) => {
  if (ev.target.dataset.feature === undefined) return;
  draft.features[ev.target.dataset.feature] = ev.target.checked;
  setDirty(true);
});

document.getElementById('noticePrefixForm').addEventListener('input', (ev) => {
  if (ev.target.dataset.featureText === undefined) return;
  draft.features[ev.target.dataset.featureText] = ev.target.value;
  setDirty(true);
});

function selectTab(tab) {
  for (const btn of document.querySelectorAll('#tabBar [data-tab]')) {
    btn.classList.toggle('active', btn.dataset.tab === tab);
  }
  for (const pane of document.querySelectorAll('.tab-pane')) {
    pane.classList.toggle('active', pane.id === 'pane-' + tab);
  }
  localStorage.setItem('qsr-active-tab', tab);
}

document.getElementById('tabBar').addEventListener('click', (ev) => {
  const btn = ev.target.closest('[data-tab]');
  if (btn !== null) selectTab(btn.dataset.tab);
});

const savedTab = localStorage.getItem('qsr-active-tab');
if (savedTab && document.getElementById('pane-' + savedTab) !== null) {
  selectTab(savedTab);
}

window.addEventListener('error', (ev) => showError('页面脚本异常', String(ev.message)));
window.addEventListener('unhandledrejection', (ev) => showError('页面脚本异常（未处理的 Promise 拒绝）', String(ev.reason)));
window.addEventListener('beforeunload', (ev) => { if (dirty) ev.preventDefault(); });

syncThemeWithCPA();
void refresh();
setInterval(() => { if (!refreshBusy) void refresh(); }, REFRESH_MS);
setInterval(tick, 1000);
</script>
</body>
</html>
`
