// cpa_disable_sync.go 把「插件判定账号禁用/恢复」的权威落点交给宿主管理 API。
//
// 为什么必须有这条通道（2026-10-28 生产实证）：三类禁用（failover / low_credit /
// exhausted）原本只直写物理 auth 文件的 disabled 字段。宿主用 MySQL 存储时，物理
// 文件只是镜像，路由真相源在 DB；宿主下一次 persist 会用内存态把镜像覆盖回
// disabled:false，插件写的归属标记也会被白名单裁剪丢弃。结果是插件认为已禁用、
// 宿主照样把这个号发给插件，双源长期分叉。
//
// 宿主唯一能真正改写路由真相源的通道是 PATCH /v0/management/auth-files/status：
// 它与认证文件管理页的 toggle 走同一条链路（applyAuthDisabledState +
// authManager.Update → persist → store.Save），因此宿主内存态与 DB 同时被改。
//
// 与写盘兜底的关系：本文件只负责「尝试走 API」。调用成功即权威；配置缺失或调用
// 失败时由调用方回落到既有的 persistAuthDirect 直写路径，保证排除不会完全失效。
// 归属判断（谁的禁用谁负责恢复）仍只靠 sidecar + 物理文件标记，API 不承载归属。
//
// 最近修改时间：2026-10-28；改动原因：新增，让禁用状态能真正到达宿主路由判据。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// defaultCPAManagementURL 是宿主管理 API 的默认地址。CPA 默认监听 8317，
	// 且插件与宿主同网络命名空间（host 网络），因此 127.0.0.1 可达。
	// 非默认端口部署通过 config_yaml cpa_management_url 覆盖。
	defaultCPAManagementURL = "http://127.0.0.1:8317"

	// cpaDisableSyncTimeout 是单次管理 API 调用的超时。禁用/恢复是低频事件，
	// 但发生在请求失败路径与后台循环里，不能无限等待：超时即回落写盘。
	cpaDisableSyncTimeout = 5 * time.Second

	// cpaDisableSyncBodyLimit 限制错误响应体的保留长度，避免把整页 HTML
	// 或大 JSON 写进日志。
	cpaDisableSyncBodyLimit = 240
)

// errCPAManagementNotConfigured 表示没有配置 cpa_management_key。
// 调用方据此区分「没配（静默走兜底）」与「配了但调用失败（需要告警）」。
var errCPAManagementNotConfigured = errors.New("cpa_management_key not configured")

var (
	// cpaManagementURL/key 由 configure() 解析写入，运行期只读。
	cpaManagementURL = defaultCPAManagementURL
	cpaManagementKey = ""
	cpaManagementMu  sync.RWMutex
	// cpaDisableSyncDo 是 HTTP 调用注入点：生产走宿主 http 桥接，单测替换为
	// 本地 httptest 直接调用，避免依赖宿主 RPC。
	cpaDisableSyncDo = hostHTTPDo
)

// setCPAManagement 应用解析后的管理 API 地址与密钥。
// [参数] url：宿主管理 API 根地址，空串回落到默认值；key：Bearer 令牌，可为空。
// [返回] 无。
// 最近修改时间：2026-10-28；改动原因：新增。
func setCPAManagement(url, key string) {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		url = defaultCPAManagementURL
	}
	cpaManagementMu.Lock()
	cpaManagementURL = url
	cpaManagementKey = strings.TrimSpace(key)
	cpaManagementMu.Unlock()
}

// loadedCPAManagement 返回当前生效的管理 API 地址与密钥。
// [参数] 无。
// [返回] url 恒非空；key 未配置时为空串。
// 最近修改时间：2026-10-28；改动原因：新增。
func loadedCPAManagement() (string, string) {
	cpaManagementMu.RLock()
	defer cpaManagementMu.RUnlock()
	return cpaManagementURL, cpaManagementKey
}

// cpaDisableSyncConfigured 报告管理 API 通道是否可用（仅以密钥是否配置为准）。
// [参数] 无。
// [返回] true 表示已配置 cpa_management_key。
// 最近修改时间：2026-10-28；改动原因：新增。
func cpaDisableSyncConfigured() bool {
	_, key := loadedCPAManagement()
	return key != ""
}

// cpaAuthFileStatusRequest 是宿主 PATCH /v0/management/auth-files/status 的请求体。
// 字段名必须与宿主 handler 的 json tag 完全一致（name 必填，disabled 必填）。
type cpaAuthFileStatusRequest struct {
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index,omitempty"`
	Disabled  bool   `json:"disabled"`
}

// setHostAuthDisabled 通过宿主管理 API 设置某个 auth 文件的禁用状态。
//
// 只有 2xx 才算成功；未配置密钥返回 errCPAManagementNotConfigured（调用方据此
// 静默走写盘兜底，不当作故障）；其余情况返回带状态码与截断响应体的错误，供调用
// 方打降级日志。
//
// [参数] name：auth 文件名（如 workbuddy-<uuid>.json）；authIndex：宿主 auth_index，
// 可空但非空时一并提交，便于宿主在同名多账号时精确命中；disabled：目标状态。
// [返回] nil 表示宿主已接受；错误语义见上。
// 最近修改时间：2026-10-28；改动原因：新增。
func setHostAuthDisabled(name, authIndex string, disabled bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("cpa disable sync: empty auth file name")
	}
	url, key := loadedCPAManagement()
	if key == "" {
		return errCPAManagementNotConfigured
	}

	payload, err := json.Marshal(cpaAuthFileStatusRequest{
		Name:      name,
		AuthIndex: strings.TrimSpace(authIndex),
		Disabled:  disabled,
	})
	if err != nil {
		return fmt.Errorf("cpa disable sync: marshal body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPatch,
		url+"/v0/management/auth-files/status", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cpa disable sync: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	done := make(chan struct{})
	var (
		resp    *hostHTTPResponse
		callErr error
	)
	// 超时兜底：hostHTTPDo 的桥接路径自身没有可注入的 deadline，这里另起一个
	// 看门狗，保证禁用同步不会把失败请求的关键路径拖长。
	go func() {
		defer close(done)
		resp, callErr = cpaDisableSyncDo(req)
	}()
	select {
	case <-done:
	case <-time.After(cpaDisableSyncTimeout):
		return fmt.Errorf("cpa disable sync: timeout after %s", cpaDisableSyncTimeout)
	}
	if callErr != nil {
		return fmt.Errorf("cpa disable sync: call host api: %w", callErr)
	}
	if resp == nil {
		return fmt.Errorf("cpa disable sync: nil response")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body := strings.TrimSpace(string(resp.Body))
	if len(body) > cpaDisableSyncBodyLimit {
		body = body[:cpaDisableSyncBodyLimit] + "..."
	}
	return fmt.Errorf("cpa disable sync: host api status %d body=%s", resp.StatusCode, body)
}

// cpaManagementSecretResolver 按「config_yaml > env > secret 文件」解析管理密钥。
// 与 resolveUsageReport 保持同一套顺序，便于用容器环境变量注入而不落配置。
// [参数] cfgKey：config_yaml 中 cpa_management_key 的原始值，可为空。
// [返回] 最终生效的密钥，全无配置时为空串。
// 最近修改时间：2026-10-28；改动原因：新增。
func cpaManagementSecretResolver(cfgKey string) string {
	return firstNonEmpty(
		strings.TrimSpace(cfgKey),
		strings.TrimSpace(os.Getenv("WB_CPA_MANAGEMENT_KEY")),
		strings.TrimSpace(os.Getenv("CPA_MANAGEMENT_KEY")),
	)
}
