package cpa

import (
	"net/http"
	"strings"
	"sync"
)

// publicOrigin 缓存最近一次观测到的外部访问 origin。
//
// 为什么要缓存而不是每次现算：宿主 internal/pluginhost/management.go 的
// ServeResourceHTTP 与 ServeManagementHTTP 都用 cloneHeader(r.Header) 转交请求头，
// 而 Go 标准库把 Host 放在 r.Host 独立字段、不进 r.Header，因此插件侧
// Headers.Get("Host") 恒为空；生产 nginx 也只设了 Host 与 X-Forwarded-For，
// 没设 X-Forwarded-Host。两头都缺的情况下，插件无法从请求头还原出
// https://cpa.luode.vip 这种真实域名，只能回落到硬编码的 127.0.0.1:8317，
// 于是控制台弹出的授权链接在用户浏览器里必然打不开。
//
// 因此真实 origin 由浏览器自己上报：面板页与桥接页都跑在用户真实域名下，
// 它们的 location.origin 就是唯一可靠来源。上报值进这里缓存，
// auth.login.start 与桥接页渲染都优先复用它。
var (
	publicOriginMu sync.RWMutex
	publicOrigin   string
)

// rememberPublicOrigin 从请求头尽力还原 origin，为空或回环时不覆盖已有值。
//
// 这是兜底通道：当前生产链路拿不到 Host，但只要哪天反代补上
// X-Forwarded-Host / X-Forwarded-Proto，这里就能自动接住，不需要再改代码。
// [参数] h 是宿主转来的请求头。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，修复授权链接回落 127.0.0.1 的问题。
func rememberPublicOrigin(h http.Header) {
	if h == nil {
		return
	}
	host := firstHeaderValue(h.Get("X-Forwarded-Host"))
	if host == "" {
		host = firstHeaderValue(h.Get("Host"))
	}
	if host == "" {
		return
	}
	scheme := firstHeaderValue(h.Get("X-Forwarded-Proto"))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
		if isLoopbackHost(host) {
			scheme = "http"
		}
	}
	reportPublicOrigin(scheme + "://" + host)
}

// reportPublicOrigin 记录浏览器上报的 origin。
//
// 回环 origin（容器内自测、健康检查、运维 curl）不允许覆盖用户真实域名，
// 否则一次本机自测就会把线上授权链接改回 127.0.0.1。
// [参数] origin 形如 https://cpa.example.com，可带尾斜杠。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增。
func reportPublicOrigin(origin string) {
	trimmed := strings.TrimSpace(origin)
	if trimmed == "" {
		return
	}
	scheme, host := splitOrigin(trimmed)
	if host == "" {
		return
	}
	publicOriginMu.Lock()
	// 回环地址不能覆盖用户真实访问的域名。
	if publicOrigin == "" || !isLoopbackHost(host) {
		publicOrigin = scheme + "://" + host
	}
	publicOriginMu.Unlock()
}

// currentPublicOrigin 读取缓存的外部 origin。
// [参数] 无。
// [返回] 形如 https://cpa.example.com 的 origin；未知时返回空字符串。
// 最近修改时间：2026-11-21；改动原因：新增。
func currentPublicOrigin() string {
	publicOriginMu.RLock()
	defer publicOriginMu.RUnlock()
	return publicOrigin
}

// splitOrigin 把 origin 拆成 scheme 与 host，非法输入返回空 host。
// [参数] origin 是待解析的 origin 字符串。
// [返回] scheme（只保留 http/https）与 host（不含尾斜杠与路径）。
// 最近修改时间：2026-11-21；改动原因：新增。
func splitOrigin(origin string) (string, string) {
	rest := strings.TrimSpace(origin)
	idx := strings.Index(rest, "://")
	if idx <= 0 {
		return "", ""
	}
	scheme := strings.ToLower(rest[:idx])
	if scheme != "http" && scheme != "https" {
		return "", ""
	}
	host := rest[idx+3:]
	if slash := strings.IndexAny(host, "/?#"); slash >= 0 {
		host = host[:slash]
	}
	return scheme, strings.TrimSpace(host)
}

// isLoopbackHost 判断 host 是否指向本机。
// [参数] host 可带端口，例如 127.0.0.1:8317、[::1]:8317，也接受裸 IPv6。
// [返回] true 表示回环地址。
// 最近修改时间：2026-11-21；改动原因：新增。剥端口时先处理方括号写法，
// 并且只对「恰好一个冒号」的 host:port 截断，避免把裸 IPv6 里的冒号当成端口分隔符。
func isLoopbackHost(host string) bool {
	name := strings.TrimSpace(host)
	if name == "" {
		return false
	}
	switch {
	case strings.HasPrefix(name, "["):
		if idx := strings.LastIndex(name, "]"); idx > 0 {
			name = name[1:idx]
		}
	case strings.Count(name, ":") == 1:
		if idx := strings.LastIndex(name, ":"); idx > 0 {
			name = name[:idx]
		}
	}
	switch name {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}

// firstHeaderValue 取逗号分隔请求头的首段。
//
// 多级反代会把 X-Forwarded-Proto 写成 "https, http"，只认最外层那段。
// [参数] raw 是原始请求头值。
// [返回] 去掉首尾空白的首段，空输入返回空字符串。
// 最近修改时间：2026-11-21；改动原因：新增。
func firstHeaderValue(raw string) string {
	if idx := strings.IndexByte(raw, ','); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}
