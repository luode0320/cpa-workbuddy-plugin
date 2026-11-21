package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 本文件覆盖 CDP 底座与页面内换发（TASK-06）。
//
// 不用真浏览器：CI 与本地都没有稳定可用的 Chrome，而起不起得来浏览器
// 与「CDP 封装是否按协议正确配对响应」是两件事。这里用假 CDP 服务端
// 把这个协议层钉住，真浏览器只在集成验证时跑一次。

// fakeCDP 是一个假 CDP 服务端。
type fakeCDP struct {
	// mu 保护字段。
	mu sync.Mutex
	// evalResults 记录收到的脚本表达式。
	evalResults []string
	// failMethod 指定某方法返回 CDP 错误。
	failMethod string
	// evalOverride 非空时 evaluate 一律返回该值。
	//
	// 用于钉住「页面返回某段 JSON 时判据怎么走」，不关心假服务端语义。
	evalOverride string
}

// newFakeCDP 起假 CDP 服务端并返回客户端。
//
// [参数] t: 测试上下文。
// [返回] 已连接的 CDP 客户端与假服务端句柄。
// 最近修改时间：2026-11-21；改动原因：新增，协议层用例共用前置。
func newFakeCDP(t *testing.T) (*cdpClient, *fakeCDP) {
	t.Helper()
	fake := &fakeCDP{}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		// 带一个非 page 目标：dialCDPPage 必须挑出 page 类型，
		// 真实浏览器里 service worker 目标经常排在前面。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"sw","type":"service_worker","webSocketDebuggerUrl":"ws://127.0.0.1:1/devtools/page/sw"},
			{"id":"page1","type":"page","url":"about:blank","webSocketDebuggerUrl":"WS_PLACEHOLDER"}
		]`))
	})
	mux.HandleFunc("/devtools/page/page1", func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var req cdpRequest
			if errRead := conn.ReadJSON(&req); errRead != nil {
				return
			}
			fake.mu.Lock()
			failMethod := fake.failMethod
			fake.mu.Unlock()
			if failMethod != "" && req.Method == failMethod {
				_ = conn.WriteJSON(map[string]any{
					"id":    req.ID,
					"error": map[string]any{"code": -32000, "message": "假服务端拒绝"},
				})
				continue
			}
			switch req.Method {
			case "Runtime.evaluate":
				expr := ""
				if params, ok := req.Params.(map[string]any); ok {
					expr, _ = params["expression"].(string)
				}
				fake.mu.Lock()
				fake.evalResults = append(fake.evalResults, expr)
				fake.mu.Unlock()
				value := fake.evalValue(expr)
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"result": map[string]any{"type": "string", "value": value},
					},
				})
			case "Network.getAllCookies":
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"cookies": []map[string]any{
							{"name": "sessionid", "value": "fresh-session", "domain": ".coze.cn", "path": "/"},
							{"name": "ttwid", "value": "tracking", "domain": ".coze.cn", "path": "/"},
						},
					},
				})
			default:
				_ = conn.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{}})
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	// 把占位符换成真实地址：先探测端口，再拼出 ws URL。
	addr := strings.TrimPrefix(server.URL, "http://")
	wsURL := "ws://" + addr + "/devtools/page/page1"

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, errDial := websocket.DefaultDialer.DialContext(dialCtx, wsURL, nil)
	if errDial != nil {
		t.Fatalf("连接假 CDP 失败：%v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &cdpClient{conn: conn}, fake
}

// evalValue 按表达式内容生成脚本返回值。
//
// [参数] expr: 收到的脚本。
// [返回] 模拟的页面返回值。
// 最近修改时间：2026-11-21；改动原因：新增，模拟页面语义。
func (f *fakeCDP) evalValue(expr string) string {
	f.mu.Lock()
	override := f.evalOverride
	f.mu.Unlock()
	if override != "" {
		return override
	}
	if strings.Contains(expr, "document.readyState") {
		return "complete"
	}
	if strings.Contains(expr, "passport/token/beat") {
		// 判据必须看「请求头里有没有注入 x-tt-token」这一具体形态：
		// 两步脚本都含 r.headers.get('x-tt-token')，按裸名字判会把
		// 第一步也当成第二步，结果第一步永远拿不到令牌。
		if strings.Contains(expr, "x-tt-token':'") {
			return `{"token":"","status":200,"message":"success","error_code":0,"description":""}`
		}
		return `{"token":"tok-123","status":200,"message":"success","error_code":0,"description":""}`
	}
	return ""
}

// hasEval 判断是否收到过包含某片段的脚本。
// [参数] needle: 片段。
// [返回] 命中返回 true。
// 最近修改时间：2026-11-21；改动原因：新增，断言辅助。
func (f *fakeCDP) hasEval(needle string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.evalResults {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

// TestCDPCall_PairsResponseByID 验证按 ID 配对响应。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，协议配对主行为。
func TestCDPCall_PairsResponseByID(t *testing.T) {
	client, _ := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]any
	if errCall := client.call(ctx, "Page.enable", nil, &out); errCall != nil {
		t.Fatalf("CDP 调用失败：%v", errCall)
	}
}

// TestCDPCall_PropagatesError 验证 CDP 错误上抛。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，错误分支。
func TestCDPCall_PropagatesError(t *testing.T) {
	client, fake := newFakeCDP(t)
	fake.mu.Lock()
	fake.failMethod = "Page.enable"
	fake.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]any
	errCall := client.call(ctx, "Page.enable", nil, &out)
	if errCall == nil {
		t.Fatal("CDP 错误必须上抛")
	}
	if !strings.Contains(errCall.Error(), "假服务端拒绝") {
		t.Fatalf("错误文案应带上游 message，实际 %q", errCall.Error())
	}
}

// TestCDPEvaluate_ReturnsPageValue 验证取回页面字符串结果。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，页面取数。
func TestCDPEvaluate_ReturnsPageValue(t *testing.T) {
	client, _ := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, errEval := client.evaluate(ctx, "document.readyState")
	if errEval != nil {
		t.Fatalf("取页面值失败：%v", errEval)
	}
	if got != "complete" {
		t.Fatalf("页面返回值 = %q，期望 complete", got)
	}
}

// TestCDPNavigate_WaitsForComplete 验证跳转后等文档就绪。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，跳转语义。
func TestCDPNavigate_WaitsForComplete(t *testing.T) {
	client, fake := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if errNav := client.navigate(ctx, cozeSiteHost+"/"); errNav != nil {
		t.Fatalf("跳转失败：%v", errNav)
	}
	if !fake.hasEval("document.readyState") {
		t.Fatal("跳转后必须探测文档就绪状态")
	}
}

// TestCDPGetAllCookies_ConvertsFields 验证 Cookie 结构转换。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，凭据回收。
func TestCDPGetAllCookies_ConvertsFields(t *testing.T) {
	client, _ := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cookies, errCookies := client.getAllCookies(ctx)
	if errCookies != nil {
		t.Fatalf("取 Cookie 失败：%v", errCookies)
	}
	if len(cookies) != 2 {
		t.Fatalf("Cookie 条数 = %d，期望 2", len(cookies))
	}
	if cookies[0].Name != "sessionid" || cookies[0].Value != "fresh-session" {
		t.Fatalf("Cookie 转换错误：%+v", cookies[0])
	}
}

// TestBeatInPage_FirstStepReturnsToken 验证第一步取回令牌。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，页面内换发。
func TestBeatInPage_FirstStepReturnsToken(t *testing.T) {
	client, _ := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token, describe, errBeat := beatInPage(ctx, client, "")
	if errBeat != nil {
		t.Fatalf("页面内换发失败：%v", errBeat)
	}
	if token != "tok-123" {
		t.Fatalf("令牌 = %q，期望 tok-123", token)
	}
	if describe != "" {
		t.Fatalf("成功时不应有失败说明，实际 %q", describe)
	}
}

// TestBeatInPage_SecondStepUsesToken 验证第二步带上令牌。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，两步换发。
func TestBeatInPage_SecondStepUsesToken(t *testing.T) {
	client, fake := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, errBeat := beatInPage(ctx, client, "tok-abc"); errBeat != nil {
		t.Fatalf("第二步换发失败：%v", errBeat)
	}
	if !fake.hasEval("tok-abc") {
		t.Fatal("第二步必须把令牌带进请求头")
	}
}

// TestBeatInPage_DetectsSessionExpiredOnHTTP200 验证 HTTP 200 下的失效判定。
//
// 这是整条链路最容易踩的坑：端点恒返回 200，只看状态码会把死会话判成
// 成功，进而刷新 LastRenewAt 让面板一路显示正常。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，判据回归。
func TestBeatInPage_DetectsSessionExpiredOnHTTP200(t *testing.T) {
	client, fake := newFakeCDP(t)
	fake.mu.Lock()
	fake.evalOverride = `{"token":"tok-123","status":200,"message":"error","error_code":401,"description":"会话过期，请重新登录"}`
	fake.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token, describe, errBeat := beatInPage(ctx, client, "")
	if errBeat != nil {
		t.Fatalf("换发不应报执行错误：%v", errBeat)
	}
	if describe == "" {
		t.Fatal("HTTP 200 + error_code 401 必须判为失败")
	}
	if !strings.Contains(describe, "会话过期") {
		t.Fatalf("失败说明应带上游原文，实际 %q", describe)
	}
	// 失败时仍要把令牌带出去：上游有时先给令牌再拒，保留便于排查。
	if token != "tok-123" {
		t.Fatalf("令牌 = %q，期望原样带出", token)
	}
}
