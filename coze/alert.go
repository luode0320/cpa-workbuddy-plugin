package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现账号状态迁移告警（TASK-05）。
//
// 为什么只在「迁移」时推：定时续期每 4 小时一轮，一个失效账号会被反复
// 判定为失效。若每次判定都推一条，12 个账号的池子里 5 个死号就能把告警
// 通道刷成噪音，真正需要人处理的事件反而被埋掉。
//
// 推什么：只推 AuthInvalid 的 false→true（失效）与 true→false（恢复），
// 同一账号在连续标记中只会看到一条。正文不带 Cookie 原值。

const (
	// alertEventInvalid 表示账号从可用转为「待重新授权」。
	alertEventInvalid = "coze_auth_invalid"
	// alertEventRecovered 表示账号从「待重新授权」回到可用。
	alertEventRecovered = "coze_auth_recovered"
	// alertTimeout 是单条告警的推送超时。
	//
	// 告警是旁路动作：上游慢不能拖住续期写盘，5 秒拿不到响应就放弃，
	// 只留一条 warn。失败不重试——重试会把一次状态迁移放大成多条告警。
	alertTimeout = 5 * time.Second
)

var (
	// alertEndpointFn 返回当前生效的告警地址，供单测替换。
	alertEndpointFn = func() string { return currentConfig().RenewAlertWebhook }
	// alertClient 是告警推送专用客户端，独立于上游客户端以免共享连接池与超时。
	alertClient = &http.Client{Timeout: alertTimeout}
	// alertNowFn 供单测固定时间戳。
	alertNowFn = time.Now

	// alertSentMu 保护 alertSent，避免并发续期线程同时判定迁移后重复推送。
	alertSentMu sync.Mutex
	// alertSent 记录每个账号最近一次已推送的状态，作为去重依据。
	//
	// 只在内存里：热重载后重新学习一遍当前状态，最坏结果是重载后对已失效
	// 账号再推一条失效告警。相比把告警状态持久化到账号文件的代价，这个
	// 偏差可以接受。
	alertSent = map[string]bool{}
)

// renewAlert 是一条账号状态迁移告警的正文。
type renewAlert struct {
	Event      string `json:"event"`
	AuthID     string `json:"auth_id"`
	Label      string `json:"label,omitempty"`
	ScreenName string `json:"screen_name,omitempty"`
	Mobile     string `json:"mobile,omitempty"`
	At         string `json:"at"`
}

// notifyAuthStateChange 在账号失效状态发生迁移时推送一条告警。
//
// 调用点必须在真正改字段之前，因为判定要用到旧值。同一账号连续两次
// 传入相同的 next 只会在第一次推送；prev 与 next 相同（无迁移）直接返回。
// [参数] prev: 变更前的失效状态；next: 变更后的失效状态；sa: 账号。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，失效与恢复各推一次。
func notifyAuthStateChange(prev, next bool, sa *StoredAuth) {
	if prev == next || sa == nil {
		return
	}
	event := alertEventInvalid
	if !next {
		event = alertEventRecovered
	}
	// 去重判据放在状态迁移之后：同一账号上一轮已经推过「失效」，本轮
	// 即使因为重启丢了 prev 也不会重复推。AuthID 为空时用 Label 兜底，
	// 两者都空说明账号记录本身不完整，宁可不推也不推一条认不出人的告警。
	key := alertKeyOf(sa)
	if key == "" {
		return
	}
	alertSentMu.Lock()
	if last, ok := alertSent[key]; ok && last == next {
		alertSentMu.Unlock()
		return
	}
	alertSent[key] = next
	alertSentMu.Unlock()

	endpoint := strings.TrimSpace(alertEndpointFn())
	if endpoint == "" {
		// 没配 webhook 时仍然维护去重表：后续补配 webhook 后不会因为
		// 记不住历史状态而把一批旧失效重新推一遍。
		return
	}
	payload := renewAlert{
		Event:      event,
		AuthID:     sa.AuthID,
		Label:      sa.Label,
		ScreenName: sa.ScreenName,
		Mobile:     maskMobile(sa.Mobile),
		At:         alertNowFn().Format(time.RFC3339),
	}
	if err := postRenewAlert(endpoint, payload); err != nil {
		hostLogf("warn", "账号状态告警推送失败", map[string]any{
			"event":   event,
			"auth_id": sa.AuthID,
			"error":   err.Error(),
		})
		return
	}
	hostLogf("info", "账号状态告警已推送", map[string]any{
		"event":   event,
		"auth_id": sa.AuthID,
	})
}

// postRenewAlert 把告警正文 POST 到 webhook 地址。
//
// 只把非 2xx 当失败：多数 webhook 服务（企业微信、钉钉、自建转发）
// 用 200 表达「已接收」，正文里的业务码不在本插件职责范围内解析。
// [参数] endpoint: webhook 地址；payload: 告警正文。
// [返回] 网络错误或非 2xx 状态码。
// 最近修改时间：2026-11-21；改动原因：新增，告警投递。
func postRenewAlert(endpoint string, payload renewAlert) error {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errMarshal
	}
	req, errNew := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if errNew != nil {
		return errNew
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, errDo := alertClient.Do(req)
	if errDo != nil {
		return errDo
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &alertStatusError{Status: resp.StatusCode}
	}
	return nil
}

// alertStatusError 表示 webhook 返回了非 2xx 状态码。
type alertStatusError struct {
	// Status 是上游返回的 HTTP 状态码。
	Status int
}

// Error 实现 error 接口。
// [参数] 无。
// [返回] 带状态码的错误文案。
// 最近修改时间：2026-11-21；改动原因：新增，告警失败可读。
func (e *alertStatusError) Error() string {
	return "webhook 返回状态码 " + strconv.Itoa(e.Status)
}

// alertKeyOf 返回账号在告警去重表里的键。
// [参数] sa: 账号。
// [返回] 优先 AuthID，缺失时退回 Label；都为空时返回空串。
// 最近修改时间：2026-11-21；改动原因：新增，去重键口径。
func alertKeyOf(sa *StoredAuth) string {
	if id := strings.TrimSpace(sa.AuthID); id != "" {
		return id
	}
	return strings.TrimSpace(sa.Label)
}

// resetAlertState 清空去重表，仅测试与重配置使用。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，用例隔离。
func resetAlertState() {
	alertSentMu.Lock()
	alertSent = map[string]bool{}
	alertSentMu.Unlock()
}

// maskMobile 对手机号做中间四位遮蔽。
//
// 告警会发到任意外部 webhook，正文里不该出现完整手机号。
// [参数] mobile: 原始手机号。
// [返回] 遮蔽后的号码；长度不足时原样返回。
// 最近修改时间：2026-11-21；改动原因：新增，避免外部通道泄露完整号码。
func maskMobile(mobile string) string {
	m := strings.TrimSpace(mobile)
	if len(m) < 7 {
		return m
	}
	return m[:3] + "****" + m[len(m)-4:]
}
