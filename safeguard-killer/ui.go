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

func managementRegistrationPayload() managementRegistration {
	return managementRegistration{
		Routes: []managementRoute{
			{
				Method:      http.MethodGet,
				Path:        mgmtStateRoutePath,
				Description: "Returns config, safeguard adaptation toggles, smart groups, quota windows, session bindings, and recent routing decisions of Safeguard Killer.",
			},
		},
		Resources: []managementResource{
			{
				Path:        uiResourcePath,
				Menu:        "Safeguard Killer",
				Description: "Neutralize model safeguards and auto-mode classifiers across Claude Code, Claude, OpenAI/Codex, and Gemini models, and route refusal-evasion Smart Groups with 5m cache stickiness. Set management_key in the plugin config. Made to work alongside Reorder/Rename Models (https://github.com/esauvisky/CLIProxyAPI-Model-List-Tweaker-Plugin).",
			},
		},
	}
}

func handleManagement(request []byte) ([]byte, error) {
	var req managementRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
	}
	path := strings.TrimRight(req.Path, "/")
	switch {
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
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Safeguard Killer</title>
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
    --quota-medium-color: #ffd862;
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
    --quota-medium-color: #e0aa14;
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
    --quota-medium-color: #e0aa14;
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

  .windows { display: flex; flex-direction: column; gap: 8px; min-width: 260px; }
  .win-head { display: flex; justify-content: space-between; gap: 8px; font-size: 12px; }
  .win-name { font-weight: 650; }
  .win-meta { font-size: 11px; color: var(--text-tertiary); margin-top: 2px; }
  .quota-bar { background: var(--bg-tertiary); height: 7px; border-radius: var(--radius-full); overflow: hidden; margin-top: 3px; }
  .quota-fill { height: 100%; border-radius: var(--radius-full); }
  .quota-fill.high { background: var(--viz-success); }
  .quota-fill.medium { background: var(--quota-medium-color); }
  .quota-fill.low { background: var(--viz-failure); }

  details > summary { cursor: pointer; color: var(--text-secondary); font-size: 12px; font-weight: 600; user-select: none; }
  details > summary:hover { color: var(--text-primary); }
  .kv { margin-top: 6px; display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 12px; }
  .kv .k { color: var(--text-tertiary); }
  .kv .v { word-break: break-all; }
  .rank { margin-top: 8px; }
  .rank th, .rank td { padding: 4px 8px; white-space: nowrap; }
  .rank tr.winner td { background: var(--winner-bg); font-weight: 650; }
  .banner.warn { background: var(--warn-badge-bg); color: var(--warn-badge-text); border-color: color-mix(in srgb, var(--quota-medium-color) 45%, transparent); }
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
    <p class="page-desc">Neutralize model safeguards and auto-mode classifiers across Claude Code, Claude, OpenAI/Codex, and Gemini models, route around refusals with Smart Groups (5m cache-sticky failover chains), and balance provider quota. Requires <span class="mono">management_key</span> in the plugin config. Made to work alongside <a href="https://github.com/esauvisky/CLIProxyAPI-Model-List-Tweaker-Plugin" target="_blank" rel="noopener" style="color:var(--text-primary);">Reorder/Rename Models</a>.</p>
  </header>

  <div class="status-bar">
    <div class="status-pills">
      <span class="status-pill"><span class="status-dot" id="statusDot"></span><span class="status-label">Version</span><span class="status-value mono" id="versionVal">-</span></span>
      <span class="status-pill"><span class="status-label">Credentials</span><span class="status-value" id="credCount">0</span></span>
      <span class="status-pill"><span class="status-label">Groups</span><span class="status-value" id="groupCount">0</span></span>
      <span class="status-pill"><span class="status-label">Sessions</span><span class="status-value" id="sessCount">0</span></span>
      <span class="status-pill"><span class="status-label">Last updated</span><span class="status-value" id="lastUpdated">never</span></span>
    </div>
    <div class="toolbar">
      <input type="password" id="mgmtKey" class="input" placeholder="Management Key" style="display:none;" onkeydown="if (event.key === 'Enter') refresh()">
      <button class="btn" id="refreshBtn" onclick="refresh()">Refresh</button>
      <span class="badge warn" id="dirtyBadge" style="display:none;">unsaved changes</span>
      <button class="btn" id="discardBtn" onclick="discardDraft()" disabled>Discard</button>
      <button class="btn primary" id="saveBtn" onclick="save()" disabled>Save</button>
    </div>
  </div>

  <div id="errorBanner" class="banner" style="display:none;"></div>
  <div id="saveBanner" class="banner" style="display:none;"></div>
  <div id="hostErrorBanner" class="banner" style="display:none;"></div>
  <div id="mgmtErrorBanner" class="banner" style="display:none;"></div>

  <nav class="tabs" id="tabBar">
    <button class="tab-btn active" data-tab="settings">General Settings</button>
    <button class="tab-btn" data-tab="groups">Smart Groups <span class="badge" id="tabGroupCount">0</span></button>
    <button class="tab-btn" data-tab="sessions">Sessions <span class="badge" id="tabSessCount">0</span></button>
    <button class="tab-btn" data-tab="decisions">Recent Decisions <span class="badge" id="tabDecCount">0</span></button>
    <button class="tab-btn" data-tab="credentials">Credentials <span class="badge" id="tabCredCount">0</span></button>
  </nav>

  <div class="tab-pane active" id="pane-settings">
    <section class="card">
      <div class="card-header"><h2 class="card-title">Router &amp; Quota Settings</h2></div>
      <div class="form-grid" id="settingsForm">
        <label class="field"><span class="cfg-key">Management key</span>
          <input class="input" type="password" autocomplete="new-password" placeholder="unchanged" data-field="management_key" data-setting="key">
          <span class="hint">Required for quota collection and balancing. Write-only: leave empty to keep the stored key.</span></label>
        <label class="field"><span class="cfg-key">Min remaining (%)</span>
          <input class="input" type="number" step="any" min="0" max="99.999" data-field="min_remaining" data-setting="min_remaining_pct">
          <span class="hint">Every applicable quota window must keep at least this much for a credential to be used.</span></label>
        <label class="field"><span class="cfg-key">Idle minutes</span>
          <input class="input" type="number" step="1" min="0" data-field="idle_minutes" data-setting="idle_minutes">
          <span class="hint">A session without prompts for this long may move to another credential or group model (0 disables stickiness).</span></label>
        <label class="field"><span class="cfg-key">Stream prefetch bytes</span>
          <input class="input" type="number" step="1" min="1" data-field="stream_prefetch_bytes" data-setting="stream_prefetch_bytes">
          <span class="hint">Bytes of visible text a group stream holds back and checks for refusals before forwarding to the client.</span></label>
      </div>
    </section>

    <section class="card">
      <div class="card-header"><h2 class="card-title">Safeguards &amp; Client Adaptations</h2></div>
      <p class="card-desc">Control how Safeguard Killer strips hidden safety prompts, taint headers, and tool classifiers, and how it intercepts model refusals before they reach the client.</p>
      <div class="feature-grid" id="featuresForm">
        <label class="feature-item">
          <input type="checkbox" data-feature="fallback_on_refusal">
          <span>
            <span class="feature-title">Fallback on refusal</span>
            <span class="feature-desc">Detect safeguard stops, content filters, empty responses, and plain-text or scope-gate refusals during stream prefetch and automatically switch to the next model in the group.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="client_fallback_notices">
          <span>
            <span class="feature-title">Client fallback &amp; model-switch notices</span>
            <span class="feature-desc">Emit a visible notice in the client when a model in a group refuses/fails or when the active model changes, and strip it from conversation history on later turns so models never see it.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="rewrite_cyber_system_prompt">
          <span>
            <span class="feature-title">Rewrite cyber &amp; dual-use system prompt gate</span>
            <span class="feature-desc">Replace Claude Code's built-in "require clear authorization context" and Fable/Mythos dual-use restriction blurbs in the system prompt so models answer directly without interrogating for scope.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="strip_refusal_taint_headers">
          <span>
            <span class="feature-title">Strip refusal-taint headers</span>
            <span class="feature-desc">Remove x-is-refusal-fallback, x-cc-fallback-*, x-cc-original-request-id, and x-cc-atis headers that Claude Code attaches to every request after a turn is flagged.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="disable_upstream_server_fallback">
          <span>
            <span class="feature-title">Disable upstream server-side model swap</span>
            <span class="feature-desc">Remove the fallbacks body field and server-side-fallback / fallback-credit beta headers so Anthropic's servers do not silently swap your model mid-stream.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="bypass_auto_mode_classifier">
          <span>
            <span class="feature-title">Bypass auto-mode tool classifier</span>
            <span class="feature-desc">Strip safeguards and dangerous-tool-use betas from requests, stamp not_flagged safeguard_results on group tool calls, and auto-approve local auto-mode classifier checks in 0ms.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="neutralize_classifier_stop_nudges">
          <span>
            <span class="feature-title">Neutralize classifier-stop companion nudges</span>
            <span class="feature-desc">Rewrite Claude Code's injected "stopped by a safety classifier — Do not produce that content again" messages and tool-result tombstones into neutral continuation prompts.</span>
          </span>
        </label>
        <label class="feature-item">
          <input type="checkbox" data-feature="strip_restrictive_system_reminders">
          <span>
            <span class="feature-title">Strip restrictive &lt;system-reminder&gt; blocks</span>
            <span class="feature-desc">Remove mid-conversation &lt;system-reminder&gt; or role=system refusal and dual-use warning injections from messages while preserving normal harness reminders.</span>
          </span>
        </label>
      </div>
      <div class="form-grid" style="margin-top: 14px;" id="noticePrefixForm">
        <label class="field"><span class="cfg-key">Client notice prefix</span>
          <input class="input mono" type="text" placeholder="⚡ [Safeguard Killer] " data-field="features.notice_prefix" data-feature-text="notice_prefix">
          <span class="hint">Prefix prepended to client fallback/switch notices (stripped from model context on subsequent turns).</span></label>
      </div>
    </section>
  </div>

  <div class="tab-pane" id="pane-groups">
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">Smart Groups</h2>
        <button class="btn sm" data-action="add-group" id="addGroupBtn">Add group</button>
      </div>
      <p class="card-desc">A Smart Group exposes a single virtual model ID that automatically routes around safeguard refusals, content filters, scope-gate lectures, and quota exhaustion across multiple models. Once a model answers without refusing, the session stays pinned to it for 5 minutes while the server prompt cache is warm, then resets to try the group from the start once 5 minutes pass (can also be used purely for multi-provider convenience or quota spreading).</p>
      <div class="groups" id="groupsEditor"></div>
      <datalist id="modelList"></datalist>
    </section>
  </div>

  <div class="tab-pane" id="pane-sessions">
    <section class="card">
      <div class="card-header"><h2 class="card-title">Sessions</h2></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Session</th><th>Credential / Target</th><th>Provider</th><th>Model / Group</th><th>Bound</th><th>Last seen</th><th>Idle / Cache expires</th></tr></thead>
          <tbody id="sessBody"></tbody>
        </table>
      </div>
    </section>
  </div>

  <div class="tab-pane" id="pane-decisions">
    <section class="card">
      <div class="card-header"><h2 class="card-title">Recent Decisions</h2></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Time</th><th>Kind</th><th>Model</th><th>Session</th><th>Chosen</th><th>Reason</th></tr></thead>
          <tbody id="decBody"></tbody>
        </table>
      </div>
    </section>
  </div>

  <div class="tab-pane" id="pane-credentials">
    <section class="card">
      <div class="card-header"><h2 class="card-title">Credentials</h2></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Credential</th><th>Status</th><th>Quota windows</th><th>Source</th></tr></thead>
          <tbody id="credBody"></tbody>
        </table>
      </div>
    </section>
  </div>
</main>

<script>
const STATE_API_URL = '/v0/management/plugins/safeguard-killer/state';
const CONFIG_API_URL = '/v8/management/config/plugins/configs/safeguard-killer';
const REFRESH_MS = 10000;
const APPLY_WAIT_MS = 1500;
const ZERO_TIME = '0001-01-01T00:00:00Z';

let state = null;
let lastOkAt = null;
// Offset between server clock (state.now) and browser clock, so countdowns use server time.
let clockSkewMs = 0;
// Keys of <details> blocks the user opened; kept across re-renders.
const openDetails = new Set();

// Editable copy of the config. Live refreshes never touch it while it has unsaved edits.
let draft = null;
let dirty = false;
let saving = false;
// Canonical form of the state.config the draft was last loaded from.
let draftSourceCanon = null;
// The credentials table holds weight inputs; it is not re-rendered while one has focus.
let credRenderPending = false;

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
    throw new Error('cli-proxy-auth is not in the enc::v2:: format this page understands');
  }
  const bin = atob(payload.slice(prefix.length));
  const keyBytes = new TextEncoder().encode('cli-proxy-api-webui::secure-storage|' + window.location.host);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i) ^ keyBytes[i % keyBytes.length];
  return new TextDecoder().decode(out);
}

function detectStoredManagementKey() {
  const authRaw = localStorage.getItem('cli-proxy-auth');
  if (authRaw === null) return '';
  // managementKey is only persisted when "remember password" is on; without it the key input is shown.
  const k = JSON.parse(deobfuscateCPAStore(authRaw)).state.managementKey;
  return typeof k === 'string' ? k.trim() : '';
}

function authHeaders() {
  const typed = document.getElementById('mgmtKey').value.trim();
  const key = typed !== '' ? typed : detectStoredManagementKey();
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
  if (d > 0) out = d + 'd ' + h + 'h';
  else if (h > 0) out = h + 'h ' + m + 'm';
  else if (m > 0) out = m + 'm ' + s + 's';
  else out = s + 's';
  return neg ? '-' + out : out;
}

function relText(t) {
  const diff = new Date(t).getTime() - serverNow();
  if (Math.abs(diff) < 1000) return 'now';
  return diff > 0 ? 'in ' + fmtDuration(diff) : fmtDuration(-diff) + ' ago';
}

function absText(t) {
  return new Date(t).toLocaleString();
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
  if (diff <= 0) return '<span class="badge warn">idle, re-picks on next prompt</span>';
  return esc(fmtDuration(diff));
}

function tick() {
  for (const el of document.querySelectorAll('.live-rel')) el.textContent = relText(el.dataset.t);
  for (const el of document.querySelectorAll('.live-cd')) el.innerHTML = countdownText(el.dataset.t);
  if (lastOkAt !== null) {
    document.getElementById('lastUpdated').textContent = fmtDuration(Date.now() - lastOkAt) + ' ago';
  }
}

function pct(fraction) {
  return (fraction * 100).toFixed(1) + '%';
}

function tierOf(fraction) {
  if (fraction >= 0.5) return 'high';
  if (fraction >= 0.2) return 'medium';
  return 'low';
}

function truncate(s, n) {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

function details(key, summary, body) {
  const open = openDetails.has(key) ? ' open' : '';
  return '<details data-key="' + esc(key) + '"' + open + '><summary>' + summary + '</summary>' + body + '</details>';
}

function credName(a) {
  // Label, email and name are each optional on host credentials; show the most specific one present.
  for (const v of [a.label, a.email, a.name]) if (v) return v;
  return a.id;
}

function credNameByID(id) {
  for (const c of state.credentials) if (c.auth.id === id) return credName(c.auth);
  return id;
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
  return s === 'random' || s === 'quota' ? s : 'order';
}

function canonConfig(cfg) {
  return JSON.stringify({
    min_remaining: cfg.min_remaining,
    idle_minutes: cfg.idle_minutes,
    stream_prefetch_bytes: cfg.stream_prefetch_bytes,
    groups: groupsOf(cfg).map(g => ({ id: g.id, name: g.name, strategy: normStrategy(g.strategy), models: g.models })),
    features: featuresOf(cfg),
  });
}

function fracToPct(f) {
  return String(Number((f * 100).toPrecision(12)));
}

function loadDraft(cfg) {
  draft = {
    settings: {
      min_remaining_pct: fracToPct(cfg.min_remaining),
      idle_minutes: String(cfg.idle_minutes),
      stream_prefetch_bytes: String(cfg.stream_prefetch_bytes),
      key: '',
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
  renderCredentials();
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
  const minPct = parseNum(s.min_remaining_pct);
  const minFrac = Number((minPct / 100).toPrecision(12));
  if (!Number.isFinite(minPct) || minFrac < 0 || minFrac >= 1) errors.push(['min_remaining', 'Min remaining must be a percentage in [0, 100), got "' + s.min_remaining_pct + '"']);
  const idle = parseNum(s.idle_minutes);
  if (!Number.isInteger(idle) || idle < 0) errors.push(['idle_minutes', 'Idle minutes must be a non-negative whole number, got "' + s.idle_minutes + '"']);
  const prefetch = parseNum(s.stream_prefetch_bytes);
  if (!Number.isInteger(prefetch) || prefetch < 1) errors.push(['stream_prefetch_bytes', 'Stream prefetch bytes must be a whole number of at least 1, got "' + s.stream_prefetch_bytes + '"']);

  const groups = draft.groups.map(g => ({ id: g.id.trim(), name: g.name.trim(), strategy: normStrategy(g.strategy), models: g.models.map(m => m.trim()) }));
  const ids = new Set();
  groups.forEach((g, i) => {
    const label = 'Group #' + (i + 1) + (g.id ? ' (' + g.id + ')' : '');
    if (g.id === '') errors.push(['groups.' + i + '.id', label + ' needs a codename']);
    else if (ids.has(g.id)) errors.push(['groups.' + i + '.id', label + ': codename ' + g.id + ' is used twice']);
    ids.add(g.id);
    if (g.name === '') errors.push(['groups.' + i + '.name', label + ' needs a name']);
    if (g.models.length === 0) errors.push(['groups.' + i + '.add', label + ' needs at least one model']);
    g.models.forEach((m, j) => {
      if (m === '') errors.push(['groups.' + i + '.models.' + j, label + ': model #' + (j + 1) + ' is empty']);
    });
  });
  groups.forEach((g, i) => {
    g.models.forEach((m, j) => {
      if (ids.has(m)) errors.push(['groups.' + i + '.models.' + j, 'Group ' + g.id + ' lists group ' + m + ' as a target; groups cannot nest']);
    });
  });

  const features = Object.assign({}, draft.features, {
    notice_prefix: draft.features.notice_prefix !== '' ? draft.features.notice_prefix : DEFAULT_NOTICE_PREFIX,
  });

  return {
    errors: errors,
    cfg: {
      min_remaining: minFrac,
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
    showSaveBanner('error', 'Not saved: fix ' + v.errors.length + ' invalid field' + (v.errors.length > 1 ? 's' : ''), v.errors.map(e => '- ' + e[1]).join('\n'));
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

async function saveValidated(cfg) {
  const getResp = await fetch(CONFIG_API_URL, { method: 'GET', headers: authHeaders(), cache: 'no-store' });
  const getText = await getResp.text();
  let node;
  let etag = null;
  if (getResp.status === 404) {
    // No plugin config node yet; fetch the parent map ETag so the PUT creates it.
    node = {};
    const parentResp = await fetch('/v8/management/config/plugins/configs', { method: 'GET', headers: authHeaders(), cache: 'no-store' });
    if (parentResp.ok) etag = parentResp.headers.get('ETag');
  } else if (!getResp.ok) {
    if (getResp.status === 401 || getResp.status === 403) document.getElementById('mgmtKey').style.display = 'inline-block';
    showSaveBanner('error', 'Not saved: HTTP ' + getResp.status + ' reading ' + CONFIG_API_URL, getText);
    return;
  } else {
    node = JSON.parse(getText);
    if (!isPlainObject(node)) throw new Error('config node at ' + CONFIG_API_URL + ' is not an object: ' + truncate(getText, 500));
    etag = getResp.headers.get('ETag');
  }
  if (etag !== null) etag = etag.replace(/^W\//, '');

  const merged = Object.assign({}, node);
  merged.min_remaining = cfg.min_remaining;
  merged.idle_minutes = cfg.idle_minutes;
  merged.stream_prefetch_bytes = cfg.stream_prefetch_bytes;
  if (draft.settings.key.trim() !== '') merged.management_key = draft.settings.key.trim();
  else if (!merged.management_key && isPlainObject(merged.management) && typeof merged.management.key === 'string' && merged.management.key !== '') {
    merged.management_key = merged.management.key;
  }
  delete merged.management;
  delete merged.weights;
  if (cfg.groups.length > 0) merged.groups = cfg.groups;
  else delete merged.groups;
  merged.features = cfg.features;

  const headers = authHeaders();
  headers['Content-Type'] = 'application/json';
  if (etag !== null) headers['If-Match'] = etag;
  const putResp = await fetch(CONFIG_API_URL, { method: 'PUT', headers: headers, body: JSON.stringify(merged), cache: 'no-store' });
  const putText = await putResp.text();
  if (putResp.status === 412) {
    showSaveBanner('error', 'Not saved: config changed elsewhere, reloaded', putText);
    if (await refresh()) loadDraft(state.config);
    return;
  }
  if (!putResp.ok) {
    if (putResp.status === 401 || putResp.status === 403) document.getElementById('mgmtKey').style.display = 'inline-block';
    showSaveBanner('error', 'Not saved: HTTP ' + putResp.status + ' writing ' + CONFIG_API_URL, putText);
    return;
  }

  // The host now calls plugin.reconfigure and re-registers the group models.
  draft.settings.key = '';
  document.querySelector('[data-setting="key"]').value = '';
  setDirty(false);
  showSaveBanner('ok', 'Saved', 'Waiting for the plugin to apply the new config...');
  await sleep(APPLY_WAIT_MS);
  if (!(await refresh())) {
    showSaveBanner('warn', 'Saved, but the state reload failed', 'See the error above; the plugin may or may not have applied the config.');
    return;
  }
  if (canonConfig(state.config) === canonConfig(cfg)) {
    showSaveBanner('ok', 'Saved and applied', '');
    loadDraft(state.config);
    return;
  }
  // The plugin rejected the config (validateConfig failed) and kept the old one; keep the draft as saved.
  showSaveBanner('warn', 'Saved but the plugin did not apply it; check CPA logs', 'The config node was written, but the running plugin still reports the previous config.');
}

// ---- groups editor ----

function groupFieldAttr(i, rest) {
  return ' data-field="groups.' + i + '.' + rest + '"';
}

const STRATEGY_HINTS = {
  order: 'In order: tries models top to bottom and only advances when a model refuses, errors, or runs out of quota. Pins to the first model that answers for 5m (warm cache), then retries from #1 after 5m.',
  quota: 'Quota spread: picks a starting model weighted-randomly by each provider\'s available quota rate (remaining / hours to reset) to spread token spend efficiently, falls through on refusal, and pins to the answering model for 5m.',
  random: 'Random: picks a starting model uniformly at random for new or cache-expired (5m+) sessions, falls through on refusal, and pins to the answering model for 5m.',
};

function renderGroups() {
  const root = document.getElementById('groupsEditor');
  document.getElementById('groupCount').textContent = String(draft.groups.length);
  document.getElementById('tabGroupCount').textContent = String(draft.groups.length);
  if (draft.groups.length === 0) {
    root.innerHTML = '<div class="empty">No smart groups configured. Add one to expose a virtual model that automatically routes around safeguard refusals across models.</div>';
    return;
  }
  root.innerHTML = draft.groups.map((g, i) => {
    const strat = normStrategy(g.strategy);
    const models = g.models.map((m, j) =>
      '<li class="model-row">' +
        '<span class="model-idx mono">' + (j + 1) + '.</span>' +
        '<input class="input grow mono" list="modelList" value="' + esc(m) + '" data-group="' + i + '" data-model="' + j + '"' + groupFieldAttr(i, 'models.' + j) + '>' +
        '<button class="btn sm" data-action="model-up" data-group="' + i + '" data-model="' + j + '"' + (j === 0 ? ' disabled' : '') + ' title="Move up">&uarr;</button>' +
        '<button class="btn sm" data-action="model-down" data-group="' + i + '" data-model="' + j + '"' + (j === g.models.length - 1 ? ' disabled' : '') + ' title="Move down">&darr;</button>' +
        '<button class="btn sm danger" data-action="model-remove" data-group="' + i + '" data-model="' + j + '" title="Remove">&times;</button>' +
      '</li>'
    ).join('');
    return '<div class="group-card">' +
      '<div class="group-head">' +
        '<label class="field"><span class="cfg-key">Codename (what clients request)</span>' +
          '<input class="input mono" value="' + esc(g.id) + '" data-group="' + i + '" data-gfield="id"' + groupFieldAttr(i, 'id') + '></label>' +
        '<label class="field"><span class="cfg-key">Visible name</span>' +
          '<input class="input" value="' + esc(g.name) + '" data-group="' + i + '" data-gfield="name"' + groupFieldAttr(i, 'name') + '></label>' +
        '<label class="field"><span class="cfg-key">Routing strategy</span>' +
          '<select class="input" data-group="' + i + '" data-gfield="strategy"' + groupFieldAttr(i, 'strategy') + '>' +
            '<option value="order"' + (strat === 'order' ? ' selected' : '') + '>In Order (Fallback Only, 5m Sticky)</option>' +
            '<option value="quota"' + (strat === 'quota' ? ' selected' : '') + '>Quota Spread (Weighted Random, 5m Sticky)</option>' +
            '<option value="random"' + (strat === 'random' ? ' selected' : '') + '>Random (Uniform Random, 5m Sticky)</option>' +
          '</select></label>' +
        '<button class="btn sm danger" data-action="group-remove" data-group="' + i + '">Delete group</button>' +
      '</div>' +
      '<div class="hint" style="margin-top:6px" data-group-hint="' + i + '">' + esc(STRATEGY_HINTS[strat]) + '</div>' +
      '<ol class="model-list">' + models + '</ol>' +
      '<div class="add-row">' +
        '<input class="input grow mono" list="modelList" placeholder="Add model (pick or type)" value="' + esc(g.pending) + '" data-group="' + i + '" data-gfield="pending"' + groupFieldAttr(i, 'add') + '>' +
        '<button class="btn sm" data-action="model-add" data-group="' + i + '">Add model</button>' +
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
    else throw new Error('unknown editor action ' + action);
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

function renderWindow(w) {
  const r = Math.max(0, Math.min(1, w.remaining));
  const periodH = w.period / 3.6e12;
  // models is omitempty: absent means the window covers every model of the credential.
  const models = w.models && w.models.length > 0 ? w.models.join(', ') : 'all models';
  return '<div>' +
    '<div class="win-head"><span class="win-name">' + esc(w.name) +
      (periodH > 0 ? ' <span class="subtle">(' + esc(periodH >= 24 ? (periodH / 24).toFixed(periodH % 24 ? 1 : 0) + 'd' : periodH.toFixed(periodH % 1 ? 1 : 0) + 'h') + ')</span>' : '') +
    '</span><span class="mono">' + esc(pct(w.remaining)) + '</span></div>' +
    '<div class="quota-bar"><div class="quota-fill ' + tierOf(r) + '" style="width:' + (r * 100).toFixed(1) + '%"></div></div>' +
    '<div class="win-meta">resets ' + (isZero(w.reset_at) ? 'unknown' : rel(w.reset_at) + ' · ' + esc(absText(w.reset_at))) + '</div>' +
    '<div class="win-meta">' + esc(models) + '</div>' +
  '</div>';
}

function renderCredentials() {
  const body = document.getElementById('credBody');
  if (state.credentials.length === 0) {
    body.innerHTML = '<tr><td colspan="4" class="empty">No credentials reported by the host.</td></tr>';
    return;
  }
  body.innerHTML = state.credentials.map(c => {
    const a = c.auth;
    const q = c.quota;
    const name = credName(a);
    const secondary = [a.email, a.name, a.id].filter(v => v && v !== name);
    const models = c.models === null ? [] : c.models;

    const status = [];
    if (a.disabled) status.push('<span class="badge bad">disabled</span>');
    if (a.unavailable) status.push('<span class="badge bad">unavailable</span>');
    if (!a.disabled && !a.unavailable) status.push('<span class="badge ok">' + esc(a.status === '' ? 'active' : a.status) + '</span>');
    else if (a.status !== '') status.push('<span class="badge">' + esc(a.status) + '</span>');
    if (!isZero(a.next_retry_after)) status.push('<div class="win-meta">retry ' + rel(a.next_retry_after) + '</div>');

    let windows;
    if (!c.has_quota) windows = '<span class="subtle">no snapshot yet</span>';
    else if (q.windows === null || q.windows.length === 0) windows = '<span class="subtle">no windows reported</span>';
    else windows = '<div class="windows">' + q.windows.map(renderWindow).join('') + '</div>';

    let source = '<span class="subtle">-</span>';
    if (c.has_quota) {
      source = '<span class="badge">' + esc(q.source) + '</span>' +
        (q.plan ? ' <span class="badge">' + esc(q.plan) + '</span>' : '') +
        '<div class="win-meta">fetched ' + rel(q.fetched_at) + '</div>';
    }

    return '<tr>' +
      '<td><div class="cred-name">' + esc(name) + '</div>' +
        '<div class="win-meta"><span class="badge">' + esc(a.provider) + '</span> ' + secondary.map(esc).join(' · ') + '</div>' +
        (q.error ? '<div class="err-box">Quota error: ' + esc(q.error) + '</div>' : '') +
        (models.length > 0 ? '<div style="margin-top:6px">' + details('models:' + a.id, 'Models (' + models.length + ')', '<div class="small mono">' + models.map(esc).join('<br>') + '</div>') + '</div>' : '') +
      '</td>' +
      '<td class="nowrap">' + status.join(' ') + '</td>' +
      '<td>' + windows + '</td>' +
      '<td class="nowrap">' + source + '</td>' +
    '</tr>';
  }).join('');
}

function renderSessions() {
  const body = document.getElementById('sessBody');
  if (state.sessions.length === 0) {
    body.innerHTML = '<tr><td colspan="7" class="empty">No bound sessions.</td></tr>';
    return;
  }
  body.innerHTML = state.sessions.map(s =>
    '<tr>' +
      '<td class="mono nowrap" title="' + esc(s.session) + '">' + esc(truncate(s.session, 24)) + '</td>' +
      '<td>' + esc(credNameByID(s.auth_id)) + '</td>' +
      '<td><span class="badge">' + esc(s.provider) + '</span></td>' +
      '<td class="mono">' + esc(s.model) + '</td>' +
      '<td class="nowrap">' + rel(s.bound_at) + '</td>' +
      '<td class="nowrap">' + rel(s.last_seen) + '</td>' +
      '<td class="nowrap">' + countdown(s.idle_expires_at) + '</td>' +
    '</tr>'
  ).join('');
}

function num(v, digits) {
  return Number.isFinite(v) ? v.toFixed(digits) : String(v);
}

function renderRanking(d) {
  const rows = d.ranking.map(r =>
    '<tr class="' + (r.auth_id === d.auth_id ? 'winner' : '') + '">' +
      '<td>' + esc(credNameByID(r.auth_id)) + '</td>' +
      '<td>' + esc(r.provider) + '</td>' +
      '<td>' + esc(r.window ? r.window : '-') + '</td>' +
      '<td class="mono">' + esc(pct(r.remaining)) + '</td>' +
      '<td class="mono">' + esc(num(r.hours_to_reset, 2)) + '</td>' +
      '<td class="mono">' + esc(num(r.rate, 4)) + '</td>' +
      '<td>' + (r.excluded ? '<span class="badge bad">' + esc(r.excluded) + '</span>' : '') + '</td>' +
    '</tr>'
  ).join('');
  return '<table class="rank"><thead><tr><th>Auth</th><th>Provider</th><th>Window</th><th>Remaining</th><th>Hours to reset</th><th>Rate</th><th>Excluded</th></tr></thead><tbody>' + rows + '</tbody></table>';
}

function renderAttempts(d) {
  return '<ol class="attempts">' + d.attempts.map(at =>
    '<li class="' + (at.result === 'served' && at.model === d.auth_id ? 'served' : '') + '">' +
      '<span class="mono">' + esc(at.model) + '</span> &rarr; ' + esc(at.result) +
    '</li>'
  ).join('') + '</ol>';
}

function renderDecisions() {
  const body = document.getElementById('decBody');
  if (state.decisions.length === 0) {
    body.innerHTML = '<tr><td colspan="6" class="empty">No decisions recorded yet.</td></tr>';
    return;
  }
  const sorted = state.decisions.slice().sort((a, b) => new Date(b.time) - new Date(a.time));
  body.innerHTML = sorted.map(d => {
    const key = 'dec:' + d.time + ':' + d.kind + ':' + d.session + ':' + d.model;
    let extra = '';
    let chosen;
    // ranking and attempts are omitempty.
    if (d.kind === 'balance') {
      if (d.ranking && d.ranking.length > 0) extra = details(key, 'Ranking (' + d.ranking.length + ')', renderRanking(d));
      chosen = d.auth_id ? esc(credNameByID(d.auth_id)) : '<span class="badge bad">none</span>';
    } else if (d.kind === 'group') {
      if (d.attempts && d.attempts.length > 0) extra = renderAttempts(d);
      chosen = d.auth_id ? '<span class="mono">' + esc(d.auth_id) + '</span>' : '<span class="badge bad">none</span>';
    } else {
      throw new Error('unknown decision kind ' + d.kind);
    }
    return '<tr>' +
      '<td class="nowrap">' + rel(d.time) + '<div class="win-meta">' + esc(absText(d.time)) + '</div></td>' +
      '<td><span class="badge">' + esc(d.kind) + '</span></td>' +
      '<td class="mono">' + esc(d.model) + '</td>' +
      '<td class="mono nowrap" title="' + esc(d.session ? d.session : '') + '">' + (d.session ? esc(truncate(d.session, 16)) : '<span class="subtle">-</span>') + '</td>' +
      '<td>' + chosen + '</td>' +
      '<td>' + esc(d.reason) + extra + '</td>' +
    '</tr>';
  }).join('');
}

function render() {
  const hostBanner = document.getElementById('hostErrorBanner');
  if (state.error) {
    hostBanner.innerHTML = '<strong>Router could not read the host credential list</strong>' + esc(state.error);
    hostBanner.style.display = 'block';
  } else {
    hostBanner.style.display = 'none';
  }
  const mgmtBanner = document.getElementById('mgmtErrorBanner');
  if (state.management_error) {
    mgmtBanner.innerHTML = '<strong>Quota collection is not running: ' + esc(state.management_error) + '</strong>';
    mgmtBanner.style.display = 'block';
  } else {
    mgmtBanner.style.display = 'none';
  }
  document.getElementById('versionVal').textContent = state.version;
  document.getElementById('credCount').textContent = String(state.credentials.length);
  document.getElementById('sessCount').textContent = String(state.sessions.length);
  document.getElementById('tabCredCount').textContent = String(state.credentials.length);
  document.getElementById('tabSessCount').textContent = String(state.sessions.length);
  document.getElementById('tabDecCount').textContent = String(state.decisions.length);
  document.getElementById('modelList').innerHTML = state.models.map(m => '<option value="' + esc(m) + '"></option>').join('');
  // Follow config changes made elsewhere only while there is nothing unsaved here.
  if (draft === null || (!dirty && !saving && canonConfig(state.config) !== draftSourceCanon)) loadDraft(state.config);
  renderCredentials();
  renderSessions();
  renderDecisions();
}

function showError(title, detail) {
  const banner = document.getElementById('errorBanner');
  const stale = lastOkAt === null
    ? 'No data loaded yet.'
    : 'Showing STALE data from ' + new Date(lastOkAt).toLocaleTimeString() + '.';
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
async function doRefresh() {
  refreshBusy = true;
  const btn = document.getElementById('refreshBtn');
  btn.disabled = true;
  try {
    let resp;
    try {
      resp = await fetch(STATE_API_URL, { method: 'GET', headers: authHeaders(), cache: 'no-store' });
    } catch (err) {
      showError('Network error fetching ' + STATE_API_URL, String(err));
      return false;
    }
    const text = await resp.text();
    if (resp.status === 401 || resp.status === 403) {
      document.getElementById('mgmtKey').style.display = 'inline-block';
      showError('HTTP ' + resp.status + ': management key required or rejected. Enter it above and press Refresh.', text);
      return false;
    }
    if (!resp.ok) {
      showError('HTTP ' + resp.status + ' ' + resp.statusText + ' from ' + STATE_API_URL, text);
      return false;
    }
    let next;
    try {
      next = JSON.parse(text);
    } catch (err) {
      showError('Invalid JSON from ' + STATE_API_URL + ': ' + err.message, truncate(text, 2000));
      return false;
    }
    // Go encodes empty slices as null; normalize so renderers can rely on arrays.
    for (const k of ['credentials', 'sessions', 'decisions', 'models']) if (next[k] === null) next[k] = [];
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

document.addEventListener('toggle', (ev) => {
  const el = ev.target;
  if (el.tagName !== 'DETAILS') return;
  if (el.open) openDetails.add(el.dataset.key);
  else openDetails.delete(el.dataset.key);
}, true);

window.addEventListener('error', (ev) => showError('Page script error', String(ev.message)));
window.addEventListener('unhandledrejection', (ev) => showError('Page script error', String(ev.reason)));
window.addEventListener('beforeunload', (ev) => { if (dirty) ev.preventDefault(); });

syncThemeWithCPA();
if (detectStoredManagementKey() === '') document.getElementById('mgmtKey').style.display = 'inline-block';
void refresh();
setInterval(() => { if (!refreshBusy) void refresh(); }, REFRESH_MS);
setInterval(tick, 1000);
</script>
</body>
</html>
`
