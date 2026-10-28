// cpa_disable_sync.go 把「插件判定账号禁用/恢复」的权威落点交给宿主管理 API。
//
// 为什么必须有这条通道（2026-10-28 生产实证）：低额禁用原本只直写物理 auth
// 文件的 disabled 字段。宿主用 MySQL 存储时，物理文件只是镜像，路由真相源在
// DB；宿主下一次 persist 会用内存态把镜像覆盖回 disabled:false，插件写的归属
// 标记也会被白名单裁剪丢弃。结果是插件认为已禁用、宿主照样把这个号发给插件。
//
// 宿主唯一能真正改写路由真相源的通道是 PATCH /v0/management/auth-files/status：
// 它与认证文件管理页的 toggle 走同一条链路（applyAuthDisabledState +
// authManager.Update → persist → store.Save），因此宿主内存态与 DB 同时被改。
//
// 本文件与其它插件的 cpa_disable_sync.go 语义一致，唯一差异是发请求的方式：
// coze 没有 hostHTTPDo 封装，直接拼 host.http.do 的 wire 走通用 hostCall。
//
// 最近修改时间：2026-10-29；改动原因：新增，让禁用状态能真正到达宿主路由判据。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const (
	// defaultCPAManagementURL 是宿主管理 API 的默认地址。CPA 默认监听 8317，
	// 且插件与宿主同网络命名空间（host 网络），因此 127.0.0.1 可达。
	defaultCPAManagementURL = "http://127.0.0.1:8317"

	// cpaDisableSyncTimeout 是单次管理 API 调用的超时。禁用/恢复是低频事件，
	// 但不能无限等待：超时即回落写盘。
	cpaDisableSyncTimeout = 5 * time.Second

	// cpaDisableSyncBodyLimit 限制错误响应体的保留长度，避免把整页 HTML
	// 或大 JSON 写进日志。
	cpaDisableSyncBodyLimit = 240
)

// errCPAManagementNotConfigured 表示没有配置 cpa_management_key。
// 调用方据此区分「没配（静默走兜底）」与「配了但调用失败（需要告警）」。
var errCPAManagementNotConfigured = errors.New("cpa_management_key not configured")

var (
	// cpaManagementURL/key 由 configurePlugin 解析写入，运行期只读。
	cpaManagementURL = defaultCPAManagementURL
	cpaManagementKey = ""
	cpaManagementMu  sync.RWMutex
)

// cpaHTTPRequestWire 是 host.http.do 的请求外壳。宿主优先读 request 子对象。
type cpaHTTPRequestWire struct {
	Request *cpaHTTPInner `json:"request,omitempty"`
}

// cpaHTTPInner 描述真正要发出的 HTTP 调用。
type cpaHTTPInner struct {
	Method  string              `json:"method,omitempty"`
	URL     string              `json:"url,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

// cpaAuthFileStatusRequest 是宿主 PATCH /v0/management/auth-files/status 的请求体。
// 字段名必须与宿主 handler 的 json tag 完全一致（name 必填，disabled 必填）。
type cpaAuthFileStatusRequest struct {
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index,omitempty"`
	Disabled  bool   `json:"disabled"`
}

// setCPAManagement 应用解析后的管理 API 地址与密钥。
// [参数] url：宿主管理 API 根地址，空串回落到默认值；key：Bearer 令牌，可为空。
// [返回] 无。
// 最近修改时间：2026-10-29；改动原因：新增。
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
// 最近修改时间：2026-10-29；改动原因：新增。
func loadedCPAManagement() (string, string) {
	cpaManagementMu.RLock()
	defer cpaManagementMu.RUnlock()
	return cpaManagementURL, cpaManagementKey
}

// cpaDisableSyncConfigured 报告管理 API 通道是否可用（仅以密钥是否配置为准）。
// [参数] 无。
// [返回] true 表示已配置 cpa_management_key。
// 最近修改时间：2026-10-29；改动原因：新增。
func cpaDisableSyncConfigured() bool {
	_, key := loadedCPAManagement()
	return key != ""
}

// cpaManagementSecretResolver 按「config_yaml > env」解析管理密钥。
// [参数] cfgKey：config_yaml 中 cpa_management_key 的原始值，可为空。
// [返回] 最终生效的密钥，全无配置时为空串。
// 最近修改时间：2026-10-29；改动原因：新增。
func cpaManagementSecretResolver(cfgKey string) string {
	for _, v := range []string{
		cfgKey,
		os.Getenv("WB_CPA_MANAGEMENT_KEY"),
		os.Getenv("CPA_MANAGEMENT_KEY"),
	} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// cpaHostHTTPDo 经宿主 host.http.do 发一次非流式请求。
//
// coze 没有 hostHTTPDo 封装，这里按 wire 契约直接拼：request 子对象携带
// method/url/headers/body，宿主执行后把 HTTPResponse 放进 envelope.Result。
// 宿主响应的字段是 PascalCase（未打 json tag），因此解析时按 PascalCase 取。
//
// [参数] req：要发出的 HTTP 请求。
// [返回] 状态码与响应体；宿主 RPC 失败时返回错误。
// 最近修改时间：2026-10-29；改动原因：新增。
func cpaHostHTTPDo(req *http.Request) (int, []byte, error) {
	if req == nil {
		return 0, nil, fmt.Errorf("cpa disable sync: nil request")
	}
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return 0, nil, fmt.Errorf("cpa disable sync: read request body: %w", err)
		}
		body = b
	}
	wire := cpaHTTPRequestWire{
		Request: &cpaHTTPInner{
			Method:  req.Method,
			URL:     req.URL.String(),
			Headers: map[string][]string(req.Header),
			Body:    body,
		},
	}
	rawWire, err := json.Marshal(wire)
	if err != nil {
		return 0, nil, fmt.Errorf("cpa disable sync: marshal wire: %w", err)
	}
	raw, err := hostCall(pluginabi.MethodHostHTTPDo, rawWire)
	if err != nil {
		return 0, nil, fmt.Errorf("cpa disable sync: host.http.do: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, nil, fmt.Errorf("cpa disable sync: decode envelope: %w", err)
	}
	if !env.OK {
		if env.Error != nil {
			return 0, nil, fmt.Errorf("cpa disable sync: host error %s: %s", env.Error.Code, env.Error.Message)
		}
		return 0, nil, fmt.Errorf("cpa disable sync: host returned not-ok")
	}
	// 宿主序列化 pluginapi.HTTPResponse 时未打 json tag，键为 PascalCase。
	var resp struct {
		StatusCode      int
		StatusCodeSnake int    `json:"status_code"`
		Body            []byte `json:"body,omitempty"`
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return 0, nil, fmt.Errorf("cpa disable sync: decode response: %w", err)
	}
	sc := resp.StatusCode
	if sc == 0 {
		sc = resp.StatusCodeSnake
	}
	return sc, resp.Body, nil
}

// setHostAuthDisabled 通过宿主管理 API 设置某个 auth 文件的禁用状态。
//
// 只有 2xx 才算成功；未配置密钥返回 errCPAManagementNotConfigured（调用方据此
// 静默走写盘兜底，不当作故障）；其余情况返回带状态码与截断响应体的错误。
//
// [参数] name：auth 文件名（如 coze-<uid>.json）；authIndex：宿主 auth_index，
// 可空但非空时一并提交；disabled：目标状态。
// [返回] nil 表示宿主已接受；错误语义见上。
// 最近修改时间：2026-10-29；改动原因：新增。
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
		url+"/v0/management/auth-files/status", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("cpa disable sync: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	type callResult struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan callResult, 1)
	go func() {
		st, bd, e := cpaHostHTTPDo(req)
		done <- callResult{status: st, body: bd, err: e}
	}()
	var res callResult
	select {
	case res = <-done:
	case <-time.After(cpaDisableSyncTimeout):
		return fmt.Errorf("cpa disable sync: timeout after %s", cpaDisableSyncTimeout)
	}
	if res.err != nil {
		return res.err
	}
	if res.status >= 200 && res.status < 300 {
		return nil
	}
	body := strings.TrimSpace(string(res.body))
	if len(body) > cpaDisableSyncBodyLimit {
		body = body[:cpaDisableSyncBodyLimit] + "..."
	}
	return fmt.Errorf("cpa disable sync: host api status %d body=%s", res.status, body)
}

// syncCozeAuthDisabled 把 coze 账号的禁用状态同步到宿主管理接口。
//
// 归属判断仍由调用方完成（只有本机制写入的禁用才会走到这里）；本函数只负责
// 从 StoredAuth 推出接口需要的 auth 文件名，并调用 setHostAuthDisabled。
//
// [参数] sa：coze 账号（必须有 PhysicalPath）；disabled：目标状态。
// [返回] nil 表示宿主已接受；未配密钥返回 errCPAManagementNotConfigured。
// 最近修改时间：2026-10-29；改动原因：新增，供低额禁用/恢复共用。
func syncCozeAuthDisabled(sa *StoredAuth, disabled bool) error {
	if sa == nil || sa.PhysicalPath == "" {
		return fmt.Errorf("cpa disable sync: no physical path for auth=%s", sa.AuthID)
	}
	return setHostAuthDisabled(filepath.Base(sa.PhysicalPath), "", disabled)
}
