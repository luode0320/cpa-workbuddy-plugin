package cpa

import (
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/logging"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

// 本文件实现「浏览器扩展定时续期」的资源路由：扩展在后台按固定周期把最新的
// Google 会话 Cookie 推回插件，插件用新 Cookie 覆盖同名账号并重启它的客户端。
//
// 为什么这条路径就能补齐自动续期（2026-11-21 取证）：
//  1. accounts.Manager.ImportCookie 传空 id 时会先校验会话、再从会话自身解析出
//     邮箱算账号 ID；账号已存在时走 addFile 的「账号认证已更新」分支，本来就
//     会替换客户端并重新初始化，等价于一次续期，不需要另写账号逻辑。
//  2. Gemini Web 的 __Secure-1PSIDTS 由插件内的 CookieRotator 每 600s 自动滚动，
//     但会话彻底失效（ListAccounts 报未登录）时 rotate.go 只能返回 ErrSignedOut，
//     因为 AccountSource.Refresh 没有可等待的对象。扩展定时回推是唯一可行的补位。
//  3. 续期不能依赖配对码：登录会话 state 只有 15 分钟有效期，用户不会为了续期
//     再去开一次桥接页，因此这条路由不吃 state。

// loginRenewPath 接收扩展定时推送的会话 Cookie，不需要配对码。
const loginRenewPath = loginResourcePrefix + "/login/renew"

// loginRenewMinInterval 是两次续期之间的最小间隔。
//
// 续期会触发一次真实的上游会话校验，被高频调用等于拿插件当探测器，
// 因此做一个进程级节流；正常扩展周期是 30 分钟，10 秒下限不会误伤。
const loginRenewMinInterval = 10 * time.Second

// handleLoginRenew 接收扩展回推的会话 Cookie 并异步续期账号。
//
// 为什么异步：续期走的是完整导入链路（上游校验 + 客户端重启），最长可达
// InitTimeout，同步等会把浏览器 fetch 拖到超时；扩展本身也不需要立即结果，
// 下一轮 alarm 会继续推。
// [参数] req 携带 query：cookies（Cookie 请求头文本）。
// [返回] 202 表示已受理，400/429/503 表示请求本身不可受理。
// 最近修改时间：2026-11-21；改动原因：新增，补齐账号会话失效后的自动恢复路径。
func (p *Plugin) handleLoginRenew(req rpcManagementRequest) pluginapi.ManagementResponse {
	cookies := strings.TrimSpace(req.Query.Get("cookies"))
	if cookies == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "cookies is required"})
	}
	now := time.Now().UnixNano()
	previous := p.lastRenewNs.Load()
	if previous != 0 && now-previous < int64(loginRenewMinInterval) {
		return jsonManagementResponse(http.StatusTooManyRequests, map[string]any{"error": "renew is too frequent, retry later"})
	}
	if !p.lastRenewNs.CompareAndSwap(previous, now) {
		return jsonManagementResponse(http.StatusTooManyRequests, map[string]any{"error": "renew is too frequent, retry later"})
	}
	if _, err := p.Runtime(); err != nil {
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
	}

	go p.runLoginRenew(cookies)
	return jsonManagementResponse(http.StatusAccepted, map[string]any{"status": "renewing"})
}

// runLoginRenew 在后台完成一次续期，失败只记日志（下一轮 alarm 会重试）。
//
// [参数] cookies 是扩展推上来的 Cookie 请求头文本。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增。
func (p *Plugin) runLoginRenew(cookies string) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("renew", "Cookie 续期中断", "错误", r)
		}
	}()
	rt, err := p.Runtime()
	if err != nil {
		return
	}
	accountID, err := rt.Accounts.ImportCookie(p.ctx, cookies, "", strings.TrimSpace(p.appCfg.ProxyURL), 0)
	if err != nil {
		logging.Warn("renew", "Cookie 续期失败", "错误", err)
		return
	}
	logging.Info(accountID, "账号 Cookie 已由浏览器扩展续期")
}
