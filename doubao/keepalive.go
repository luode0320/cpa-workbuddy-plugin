package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本文件实现「空闲保活」：定时给每个账号发一次轻量业务请求，接住响应头里
// 上游刚签发的 msToken，让长期没人用的账号也不至于一直拿着几天前的令牌。
//
// 为什么必须靠真实请求：msToken 只有一个来源——上游响应头（见 completion.go
// 的 adoptMsTokenFromHeader）。`mssdk.bytedance.com/web/r/token` 需要设备侧
// 加密串 strData，缺它永远只回同一个 12 字节状态码（2026-11-21 四组对照实测）。
// 因此「不请求就没有新 token」是结构性的，只能主动发一次请求去换。
//
// 为什么选 /samantha/user/preference/get（2026-11-21 实测）：
//   - 在 sign.go 的 bdms 排除名单里，不需要 a_bogus 签名，省掉一次 goja 计算；
//   - 只读偏好查询，不消耗对话额度、不改账号状态；
//   - 每次响应都带 x-ms-token，连发 5 次拿到 5 个互不相同的值。
//
// 保活失败一律只记日志：它不属于「这次请求失败」，不能写进 failover 的冷却
// 账本，否则一次网络抖动就会把好账号踢下线。

const (
	// keepalivePath 是保活探测端点。
	keepalivePath = "/samantha/user/preference/get"

	// keepaliveBody 是探测请求体：该端点接受空对象。
	keepaliveBody = "{}"

	// defaultKeepaliveIntervalSeconds 是保活间隔的默认值（30 分钟）。
	//
	// 实测 msToken 采集后 5.6 天仍可用，本不需要高频刷新；30 分钟既能让空闲
	// 账号保持新鲜，又把对上游的额外请求压到每天 48 次。
	defaultKeepaliveIntervalSeconds = 1800

	// keepaliveStopGrace 是等保活协程退出的上限。
	//
	// 不能无限等：一次保活请求本身可能跑满 TimeoutSeconds，宿主 reconfigure
	// 时若被它拖住，配置热更新就会长时间不返回。
	keepaliveStopGrace = 5 * time.Second

	// keepaliveConfigCheckInterval 是巡检宿主配置的周期默认值。
	//
	// 宿主 PUT /config 不回调 reconfigure（见 hostconfig.go 的实测记录），只能
	// 自己按文件比对。30 秒是折中：远小于默认 30 分钟的保活周期，改小间隔最多
	// 半分钟就生效；又不会让 69KB 的宿主配置解析变成负担（每天 2880 次）。
	keepaliveConfigCheckInterval = 30 * time.Second
)

// keepaliveConfigCheckPeriod 是巡检周期的运行期值，用例可临时改小以免久等。
var keepaliveConfigCheckPeriod = keepaliveConfigCheckInterval

// 保活定时器的运行期句柄。
//
// cancel 非空表示定时器在跑；done 用于关闭时确认协程已真正退出，避免宿主
// 卸载插件后还有协程去碰已释放的宿主 API。
var (
	keepaliveMu     sync.Mutex
	keepaliveCancel context.CancelFunc
	keepaliveDone   chan struct{}
)

// restartKeepalive 按当前配置重建保活定时器。
//
// 配置关掉时只停不启，保证「关」是一次真实的状态迁移而不是留一个空转协程。
// [参数] cfg: 刚生效的运行期配置。
// [返回] 无。
// 最近修改时间：2026-11-22 02:15:00；改动原因：协程内巡检宿主配置文件，宿主改间隔后自行重建 ticker。
func restartKeepalive(cfg Config) {
	stopKeepalive()
	if !cfg.KeepaliveEnabled {
		return
	}

	interval := time.Duration(cfg.KeepaliveIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(defaultKeepaliveIntervalSeconds) * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	keepaliveMu.Lock()
	keepaliveCancel = cancel
	keepaliveDone = done
	keepaliveMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		configTicker := time.NewTicker(keepaliveConfigCheckPeriod)
		defer configTicker.Stop()
		// 1. 首轮不立刻跑：热重载可能连着来几次，配置刚落地就对上游发请求没有收益。
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runKeepaliveOnce(ctx)
			case <-configTicker.C:
				// 2. 宿主改配置不回调插件，这里自己按文件巡检，换了间隔就地重建 ticker。
				next, action := syncKeepaliveInterval(interval)
				switch action {
				case keepaliveStop:
					return
				case keepaliveReschedule:
					interval = next
					ticker.Reset(interval)
				}
			}
		}
	}()
}

// stopKeepalive 停止保活定时器并等待协程退出。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，配合 reconfigure 与 shutdown。
func stopKeepalive() {
	keepaliveMu.Lock()
	cancel := keepaliveCancel
	done := keepaliveDone
	keepaliveCancel = nil
	keepaliveDone = nil
	keepaliveMu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(keepaliveStopGrace):
		log.Printf("[doubao] keepalive goroutine did not stop within %v", keepaliveStopGrace)
	}
}

// syncKeepaliveInterval 巡检宿主配置文件，返回本次触发后应使用的间隔与动作。
// [参数] current: 当前生效的保活间隔。
// [返回] 下次应使用的间隔与动作；读不到配置时原样返回当前间隔。
// 最近修改时间：2026-11-22 02:15:00；改动原因：新增，宿主改配置后自行对齐 ticker。
func syncKeepaliveInterval(current time.Duration) (time.Duration, keepaliveTickAction) {
	next, action := decideKeepaliveTick(current)
	if action == keepaliveReschedule {
		log.Printf("[doubao] keepalive interval changed %v -> %v, rescheduling", current, next)
	}
	return next, action
}

// runKeepaliveOnce 对所有启用中的账号各跑一次保活探测。
// [参数] ctx: 定时器上下文，取消时立即中断剩余账号。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，一轮保活。
func runKeepaliveOnce(ctx context.Context) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		log.Printf("[doubao] keepalive skipped: list accounts failed: %v", errList)
		return
	}
	for _, sa := range accounts {
		if ctx.Err() != nil {
			return
		}
		if sa == nil || sa.Disabled {
			continue
		}
		refreshMsTokenByProbe(ctx, sa)
	}
}

// refreshMsTokenByProbe 对单个账号发一次轻量探测，把响应头里的新 msToken 落盘。
//
// 落盘走 Jar 的 persist：它带着「文件被外部替换就放弃写入」的闸门，和正常请求
// 共用同一套防覆盖语义；一次 persist 同时带走滚动下发的新 Cookie 与本次 token。
// [参数] ctx: 定时器上下文；sa: 账号，MsToken 会被就地更新。
// [返回] 无。任何失败只记日志，不影响账号可用性。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，单账号保活探测。
func refreshMsTokenByProbe(ctx context.Context, sa *StoredAuth) {
	timeout := time.Duration(currentConfig().TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, jar := newUpstreamClient(sa, timeout)

	endpoint := endpointURL(sa, keepalivePath, nil)
	if businessEndpointOverride != "" {
		endpoint = businessEndpointOverride + keepalivePath
	}

	// 1. 请求体固定空对象：该端点只用来取偏好，不需要参数。
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(keepaliveBody)))
	if errReq != nil {
		log.Printf("[doubao] keepalive request build failed for %s: %v", sa.AuthID, errReq)
		return
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	applyUpstreamHeaders(req)

	resp, errDo := client.Do(req)
	if errDo != nil {
		log.Printf("[doubao] keepalive probe failed for %s: %v", sa.AuthID, errDo)
		return
	}
	defer resp.Body.Close()
	// 2. 响应体只是偏好数据，读掉并丢弃即可；限读防止异常上游拖长连接。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		log.Printf("[doubao] keepalive probe returned HTTP %d for %s", resp.StatusCode, sa.AuthID)
		return
	}

	// 3. 只有形态合法且确实变过才落盘：同值重复写只会制造无意义的文件抖动。
	token := strings.TrimSpace(resp.Header.Get(msTokenHeader))
	if !isMsTokenShaped(token) {
		return
	}
	if token == strings.TrimSpace(sa.MsToken) {
		return
	}
	sa.MsToken = token
	sa.UpdatedAt = time.Now().Format(time.RFC3339)
	if errSave := jar.persist(sa); errSave != nil {
		log.Printf("[doubao] keepalive save ms_token failed for %s: %v", sa.AuthID, errSave)
		return
	}
	log.Printf("[doubao] ms_token kept alive for %s (len=%d)", sa.AuthID, len(token))
}
