package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

// 本文件实现 /chat/completion 的字节风控签名（msToken + a_bogus）。
//
// 协议事实（2026-11-15 生产实测，逐组对照）：
//   - 不带签名：上游秒回 {"error_code":710022002,"error_msg":"当前服务访问频繁"}；
//   - 只带 msToken、缺 a_bogus：回 710022004 rate limited；
//   - 两者齐全且 msToken 真实：SSE_ACK 正常出字；
//   - msToken 换成随机串：等同未签名，回 710022002。
//
// 签名不是可选项，而是必需参数。a_bogus 由豆包页面下发的 bdms-sdk 生成，
// 算法跑在混淆解释器里、且带环境探测位图，静态复刻不可行；因此这里直接
// 内嵌官方 SDK，用 goja 在插件进程内还原它的 XHR hook，让 SDK 自己把
// a_bogus 追加到 URL 上。msToken 首次由页面 localStorage.xmst 随授权采集，
// 之后靠每次 /chat/completion 响应头下发的 x-ms-token 自动换新，不需要用户
// 重新点书签（见 completion.go 的 adoptMsTokenFromHeader）。
//
// 最近修改时间：2026-11-21 15:10:00；改动原因：更正 msToken 续期口径，改为响应头自动换新。

//go:embed assets/bdms-sdk.js
var bdmsSDKSource string

//go:embed assets/bdms-shim.js
var bdmsShimSource string

//go:embed assets/bdms-xhr.js
var bdmsXHRSource string

const (
	// bdmsInitPageID 是 bdms.init 的 pageId，取自豆包页面真实调用点。
	bdmsInitPageID = 26930

	// bdmsInitIC 与 bdmsInitDDRT 是 bdms.init 的采集间隔参数，取自页面真实配置。
	bdmsInitIC   = 13
	bdmsInitDDRT = 13
)

// bdmsIncludePaths 是 bdms.init 的签名路径白名单，取自豆包页面真实配置。
var bdmsIncludePaths = []string{
	"/alice", "/samantha", "/passport", "/biz",
	"/chat/completion", "/chat/async/chunk_stream",
	"/creativity/canvas_workspace/tool/exec",
}

// bdmsExcludePaths 是 bdms.init 的签名豁免路径，取自豆包页面真实配置。
var bdmsExcludePaths = []string{
	"/samantha/notice/info", "/samantha/user/preference/get",
	"/samantha/user/ab/get", "/samantha/plugin/recommend/webtodesktop",
	"/samantha/guidance/get_task", "/samantha/guidance/draw_result",
}

// browserSigner 是进程内常驻的签名器：加载一次 SDK，之后反复签名。
//
// goja.Runtime 不是并发安全的，因此所有签名串行执行；SDK 加载约 0.5 秒，
// 单次签名约 200ms，摊薄后可以接受。
type browserSigner struct {
	mu   sync.Mutex
	vm   *goja.Runtime
	init bool
}

var (
	browserSignerMu     sync.Mutex
	sharedBrowserSigner *browserSigner
)

// getBrowserSigner 返回进程内共享的签名器（首次调用时构造）。
// [参数] 无。
// [返回] 签名器实例。
// 最近修改时间：2026-11-15 01:00:00；改动原因：新增，签名器单例。
func getBrowserSigner() *browserSigner {
	browserSignerMu.Lock()
	defer browserSignerMu.Unlock()
	if sharedBrowserSigner == nil {
		sharedBrowserSigner = &browserSigner{}
	}
	return sharedBrowserSigner
}

// sign 在 URL 上补齐 msToken 与 a_bogus。
//
// msToken 由调用方从账号里带进来（页面 localStorage.xmst 的值）；SDK 只负责
// 生成 a_bogus。签名失败时原样返回入参 URL，由上游错误码驱动后续换号。
// [参数] rawURL: 待签名 URL；msToken: 账号令牌；body: 即将发送的请求体。
// [返回] 签名后的 URL（失败时原样返回）与错误。
// 最近修改时间：2026-11-16 03:15:00；改动原因：a_bogus 覆盖请求体，签名必须喂真实 body。
func (s *browserSigner) sign(rawURL, msToken, body string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("待签名 URL 为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if errLoad := s.ensureLoaded(); errLoad != nil {
		return rawURL, errLoad
	}

	// 1. msToken 由本函数拼进 query：SDK 只在 localStorage 里拿到 token 时才
	//    追加，且会让所有账号共用同一份缓存，因此改为在这里按账号注入。
	//    msToken 是 base64 串（含 + / =），必须做 query 转义，否则上游解析错位。
	target := rawURL
	if token := strings.TrimSpace(msToken); token != "" && !strings.Contains(target, "msToken=") {
		sep := "&"
		if !strings.Contains(target, "?") {
			sep = "?"
		}
		target += sep + "msToken=" + url.QueryEscape(token)
	}

	// 2. 触发一次被 SDK hook 的 XHR，取它改写后的最终 URL。
	//    a_bogus 把请求体一起算进签名：喂空对象会让算出的值对不上真实 body，
	//    上游直接回 -1 delegated input protocol violation（2026-11-16 三组对照：
	//    {} / 小 JSON / 真实 3.4KB body 得到三个互不相同的 a_bogus）。
	_ = s.vm.Set("__signURL", target)
	_ = s.vm.Set("__signBody", body)
	signed, errRun := s.run(signRequestScript)
	if errRun != nil {
		return rawURL, errRun
	}
	if strings.TrimSpace(signed) == "" || signed == "NO_URL" {
		return rawURL, fmt.Errorf("签名未产出 URL")
	}
	return signed, nil
}

// ensureLoaded 惰性加载 SDK 并安装签名 hook。
// [参数] 无。
// [返回] 加载错误。
// 最近修改时间：2026-11-15 01:00:00；改动原因：新增，SDK 单次装载。
func (s *browserSigner) ensureLoaded() error {
	if s.init {
		return nil
	}

	vm := goja.New()
	bindSignHelpers(vm)

	// 1. 先装 XHR 桩再装 BOM：SDK 通过替换 XMLHttpRequest.prototype.open/send
	//    安装 hook，桩的方法必须挂在 prototype 上才拦得住。
	if _, errXHR := vm.RunString(bdmsXHRSource); errXHR != nil {
		return fmt.Errorf("加载 XHR 桩失败：%w", errXHR)
	}
	if _, errShim := vm.RunString(bdmsShimSource); errShim != nil {
		return fmt.Errorf("加载浏览器环境垫片失败：%w", errShim)
	}
	for _, name := range []string{"self", "window", "globalThis", "top", "parent", "frames"} {
		_ = vm.Set(name, vm.GlobalObject())
	}

	// 2. SDK 允许抛错但不允许缺席：它的模块初始化在部分环境下会抛异常，
	//    包一层 catch 保留已完成的副作用，再用 window.bdms 是否存在判定成败。
	wrapped := "try{\n" + bdmsSDKSource + "\n}catch(e){ window.__sdkErr = String(e && e.stack || e); }"
	if _, errSDK := vm.RunString(wrapped); errSDK != nil {
		return fmt.Errorf("执行签名 SDK 失败：%w", errSDK)
	}
	if got, _ := vm.RunString("typeof window.bdms"); got == nil || got.String() != "object" {
		detail := ""
		if v := vm.Get("__sdkErr"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			detail = "：" + v.String()
		}
		return fmt.Errorf("签名 SDK 未导出 bdms%s", detail)
	}

	// 3. 安装 hook：pageId / paths 必须与页面一致，否则 /chat/completion 不在
	//    白名单里，签名不会生效且不报错。
	_ = vm.Set("__initCfg", map[string]any{
		"aid":    doubaoAID,
		"pageId": bdmsInitPageID,
		"paths": map[string]any{
			"include": bdmsIncludePaths,
			"exclude": bdmsExcludePaths,
		},
		"ic":   bdmsInitIC,
		"ddrt": bdmsInitDDRT,
	})
	if out, errInit := vm.RunString(`(function(){
  try { window.bdms.init(__initCfg); return 'OK'; } catch(e){ return 'ERR ' + (e && e.stack || e); }
})()`); errInit != nil {
		return fmt.Errorf("初始化签名 hook 失败：%w", errInit)
	} else if out != nil && out.String() != "OK" {
		return fmt.Errorf("初始化签名 hook 失败：%s", out.String())
	}

	s.vm = vm
	s.init = true
	return nil
}

// run 在 vm 上执行脚本并返回结果字符串。
// [参数] script: 待执行脚本。
// [返回] 结果字符串与错误。
// 最近修改时间：2026-11-15 01:00:00；改动原因：新增，脚本执行收敛。
func (s *browserSigner) run(script string) (string, error) {
	if s.vm == nil {
		return "", fmt.Errorf("签名器未初始化")
	}
	v, errRun := s.vm.RunString(script)
	if errRun != nil {
		return "", fmt.Errorf("执行签名脚本失败：%w", errRun)
	}
	if v == nil {
		return "", nil
	}
	return v.String(), nil
}

// signRequestScript 触发一次签名 XHR 并取回改写后的 URL。
//
// body 必须是即将真实发送的请求体：a_bogus 覆盖 body 内容，喂空对象算出的签名
// 与上游按真实 body 校验的结果不一致，会稳定触发 -1。
const signRequestScript = `(function(){
  try {
    var x = new XMLHttpRequest();
    x.open('POST', __signURL, true);
    x.setRequestHeader('Content-Type','application/json');
    x.setRequestHeader('Accept','text/event-stream');
    x.send(__signBody || '');
  } catch(e) { return 'ERR ' + (e && e.stack || e); }
  return (window.__lastXhr && window.__lastXhr.url) || 'NO_URL';
})()`

// bindSignHelpers 把 SDK 依赖的二进制与随机数能力绑定到 JS 侧。
//
// btoa/atob 必须按 charCode 逐字节往返：直接让 Go 的 string 与 JS 字符串
// 互换会按 UTF-8 处理，二进制字节被破坏后签名会算出错误结果。
// [参数] vm: 目标 Runtime。
// [返回] 无。
// 最近修改时间：2026-11-15 01:00:00；改动原因：新增，SDK 运行时依赖注入。
func bindSignHelpers(vm *goja.Runtime) {
	_ = vm.Set("goBtoaCodes", func(codes []int64) string {
		b := make([]byte, len(codes))
		for i, c := range codes {
			b[i] = byte(c)
		}
		return base64.StdEncoding.EncodeToString(b)
	})
	_ = vm.Set("goAtobCodes", func(s string) []int64 {
		b, errDecode := base64.StdEncoding.DecodeString(s)
		if errDecode != nil {
			b, errDecode = base64.RawStdEncoding.DecodeString(s)
		}
		if errDecode != nil {
			b, errDecode = base64.URLEncoding.DecodeString(s)
		}
		if errDecode != nil {
			return nil
		}
		out := make([]int64, len(b))
		for i, v := range b {
			out[i] = int64(v)
		}
		return out
	})
	_ = vm.Set("goUtf8Encode", func(s string) []byte { return []byte(s) })
	_ = vm.Set("goUtf8Decode", func(b []byte) string { return string(b) })
	_ = vm.Set("goRandomBytes", func(n int) []byte {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return b
	})
	_ = vm.Set("goRandomUUID", func() string {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	})
}

// signChatCompletionURL 给 /chat/completion 地址补签名。
//
// 只对真实上游地址签名：测试覆盖点（httptest）直接跳过，避免测试依赖 SDK。
// 签名失败不阻断请求——返回原 URL，让上游错误码驱动换号与冷却判定。
// [参数] sa: 账号（提供 msToken）；rawURL: 待签名 URL；body: 即将发送的请求体。
// [返回] 签名后的 URL。
// 最近修改时间：2026-11-16 03:15:00；改动原因：a_bogus 把请求体算进签名，必须把真实 body 交给签名器。
func signChatCompletionURL(sa *StoredAuth, rawURL string, body []byte) string {
	if sa == nil || strings.TrimSpace(rawURL) == "" {
		return rawURL
	}
	// 1. 只签真实上游地址：httptest 覆盖点与本地地址一律跳过。
	if !strings.HasPrefix(rawURL, doubaoSiteHost) {
		return rawURL
	}

	signed, errSign := getBrowserSigner().sign(rawURL, sa.MsToken, string(body))
	if errSign != nil || strings.TrimSpace(signed) == "" {
		return rawURL
	}
	return signed
}
