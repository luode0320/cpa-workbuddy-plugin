// client_version.go 维护上报给上游的客户端版本号。
//
// 上游 CodeBuddy / WorkBuddy 的 User-Agent 形态是 `${X-IDE-Name}/${版本}`。
// 该版本号每天从官方发布源拉取一次，失败时保留上一次已知值，最终兜底为
// 编译期常量 builtinClientVersion。
//
// 注意：X-IDE-Version 头与 UA 版本号是两套语义，不可混用。官方 Electron
// 客户端（独立应用、无宿主 IDE）与 CLI bundle 的 X-IDE-Version 缺省值都是
// 固定 "0.0.0"（见 ideVersionHeaderValue），只有作为 IDE 插件运行时才填
// 宿主 IDE 版本。因此这里拉取的版本号仅用于 UA。
//
// 版本源优先级：
//  1. config_yaml 的 client_version（人工钉死，非空即用，不再自动更新）
//  2. 官方 COS 发布源 releases/latest（返回纯文本版本号）
//  3. npm registry 的 latest（备用源，结构为 JSON）
//  4. builtinClientVersion（编译期兜底）
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// builtinClientVersion 是拉取失败时的兜底版本，同时作为「只升不降」的比较基准。
// 发版时同步刷新它，避免首次启动就落后于上游太多。
const builtinClientVersion = "2.174.0"

// ideVersionHeaderValue 是 X-IDE-Version 头的固定取值。
//
// 官方独立桌面客户端与 CLI 的该头缺省值都是 "0.0.0"（bundle 中写作
// `IDE_VERSION_HEADER] = ideVersion || "0.0.0"`），语义是「宿主 IDE 版本」：
// 独立应用没有宿主 IDE，故发 0.0.0；作为 VS Code 等插件运行时才填宿主版本。
// 本插件扮演独立客户端，因此固定发 0.0.0，与真实客户端一致。
//
// 该值与 UA 里的产品版本号是两套语义，不得互相替代。
const ideVersionHeaderValue = "0.0.0"

const (
	// clientVersionPrimaryURL 是官方 CLI 自己的更新检查地址（腾讯云 COS）。
	// 响应体为纯文本版本号，例如 "2.174.0"，约 8 字节。
	clientVersionPrimaryURL = "https://acc-1258344699.cos.accelerate.myqcloud.com/@tencent-ai/codebuddy-code/releases/latest"
	// clientVersionBackupURL 是公共 npm registry，返回完整 package.json。
	clientVersionBackupURL = "https://registry.npmjs.org/@tencent-ai/codebuddy-code/latest"

	// clientVersionCheckInterval 是自动检查周期：上游几乎每天发版，24 小时足够。
	clientVersionCheckInterval = 24 * time.Hour
	// clientVersionJitterMax 摊平多实例同时启动造成的瞬时并发。
	clientVersionJitterMax = 30 * time.Minute
	// clientVersionHTTPTimeout 单次拉取超时，避免拖住启动。
	clientVersionHTTPTimeout = 10 * time.Second
	// clientVersionBodyLimit 限制响应体读取量，防止异常超大响应。
	clientVersionBodyLimit = 4096
)

// semverRe 只接受三段纯数字，拒绝预发布后缀（-beta / -next.xxx）与 v 前缀：
// 上游的预发布版本不应被当作正式客户端版本上报。
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

var (
	// clientVersion 保存当前生效版本，经 atomic.Value 并发读写。
	clientVersion atomic.Value
	// clientVersionPinned 为 true 时表示人工钉死了版本，自动更新整体让位。
	clientVersionPinned   bool
	clientVersionPinnedMu sync.RWMutex
	// clientVersionStop 关闭后台检查协程（仅测试使用）。
	clientVersionStop chan struct{}
	clientVersionOnce sync.Once

	// clientVersionFetchFn 是拉取接缝，便于单测注入假响应而不触网。
	clientVersionFetchFn = fetchClientVersion
)

func init() {
	clientVersion.Store(builtinClientVersion)
}

// currentClientVersion 返回当前生效的客户端版本号，未初始化时回落到内置值。
//
// [返回] 形如 "2.174.0" 的版本字符串，保证非空。
func currentClientVersion() string {
	if v, ok := clientVersion.Load().(string); ok && v != "" {
		return v
	}
	return builtinClientVersion
}

// currentIDEVersionHeader 返回 X-IDE-Version 头的取值。
//
// 恒为固定值 "0.0.0"，与 UA 的产品版本号无关——两者语义不同，详见
// ideVersionHeaderValue 的说明。
//
// [返回] 固定字符串 "0.0.0"。
func currentIDEVersionHeader() string {
	return ideVersionHeaderValue
}

// currentUserAgent 按上游真实形态拼出 User-Agent：`${品牌名}/${版本}`。
//
// 品牌名直接复用 X-IDE-Name 的同一个常量（ideNameHeaderValue），使 UA 与
// 头部永远同源，不会出现「UA 说 CodeBuddy、头部说 WorkBuddy」的自相矛盾。
//
// [返回] 形如 "WorkBuddy/2.174.0" 的 User-Agent。
func currentUserAgent() string {
	return ideNameHeaderValue + "/" + currentClientVersion()
}

// clientVersionFromConfig 解析 config_yaml 的 client_version 键。
//
// 值为空表示交给自动更新；非空且符合三段纯数字时钉死该版本。
//
// [参数] raw：config_yaml 中的原始值（已去除引号）。
// [返回] 解析出的版本号；空串表示未钉死。
func clientVersionFromConfig(raw string) string {
	v := strings.TrimSpace(raw)
	if !semverRe.MatchString(v) {
		return ""
	}
	return v
}

// setPinnedClientVersion 应用人工钉死的版本。
//
// [参数] pinned：非空版本号表示钉死；空串表示恢复自动更新。
// [返回] 无。
func setPinnedClientVersion(pinned string) {
	clientVersionPinnedMu.Lock()
	clientVersionPinned = pinned != ""
	clientVersionPinnedMu.Unlock()
	if pinned != "" {
		clientVersion.Store(pinned)
	}
}

func isClientVersionPinned() bool {
	clientVersionPinnedMu.RLock()
	defer clientVersionPinnedMu.RUnlock()
	return clientVersionPinned
}

// isNewerVersion 判断 next 是否严格大于 cur。
//
// 仅用于「只升不降」：非法输入、相等、降级一律返回 false，
// 保证上游发布源偶发回退或返回脏数据时不会削弱现有 UA。
//
// [参数] cur：当前版本；next：候选版本。
// [返回] next 严格大于 cur 时为 true。
func isNewerVersion(cur, next string) bool {
	if !semverRe.MatchString(cur) || !semverRe.MatchString(next) {
		return false
	}
	a := strings.Split(cur, ".")
	b := strings.Split(next, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return y > x
		}
	}
	return false
}

// fetchClientVersion 依次尝试官方源与备用源，返回首个合法版本号。
//
// [返回] 合法版本号；两个源都失败时返回错误。
func fetchClientVersion() (string, error) {
	client := &http.Client{Timeout: clientVersionHTTPTimeout}

	// 1. 官方 COS 源：响应体就是裸版本号，最省流量也最权威。
	if v, err := fetchVersionPlain(client, clientVersionPrimaryURL); err == nil {
		return v, nil
	} else {
		log.Printf("[workbuddy] 客户端版本：官方源拉取失败，改用备用源：%v", err)
	}

	// 2. npm 备用源：需要从 package.json 里取 version 字段。
	return fetchVersionFromNPM(client, clientVersionBackupURL)
}

func fetchVersionPlain(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("状态码 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, clientVersionBodyLimit))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(body))
	if !semverRe.MatchString(v) {
		return "", fmt.Errorf("响应不是合法版本号：%q", truncateForLog(v))
	}
	return v, nil
}

func fetchVersionFromNPM(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("状态码 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, clientVersionBodyLimit))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return "", err
	}
	if !semverRe.MatchString(pkg.Version) {
		return "", fmt.Errorf("版本号非法：%q", truncateForLog(pkg.Version))
	}
	return pkg.Version, nil
}

// truncateForLog 截断日志里的异常响应内容，避免脏数据刷屏。
func truncateForLog(s string) string {
	const max = 40
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// refreshClientVersionOnce 拉取一次并应用，仅在新版本严格更大时更新。
//
// [返回] 无。失败只记录日志，保留当前值。
func refreshClientVersionOnce() {
	if isClientVersionPinned() {
		return
	}
	latest, err := clientVersionFetchFn()
	if err != nil {
		log.Printf("[workbuddy] 客户端版本：拉取失败，继续使用 %s：%v", currentClientVersion(), err)
		return
	}
	cur := currentClientVersion()
	if !isNewerVersion(cur, latest) {
		return
	}
	clientVersion.Store(latest)
	log.Printf("[workbuddy] 客户端版本：%s -> %s", cur, latest)
}

// startClientVersionUpdater 启动每日版本检查，重复调用只生效一次。
//
// 首次检查异步执行，不阻塞插件注册；之后按 24 小时 + 随机抖动循环。
//
// [返回] 无。
func startClientVersionUpdater() {
	clientVersionOnce.Do(func() {
		clientVersionStop = make(chan struct{})
		go clientVersionLoop(clientVersionStop)
	})
}

func clientVersionLoop(stop chan struct{}) {
	go refreshClientVersionOnce()
	for {
		wait := clientVersionCheckInterval + time.Duration(rand.Int64N(int64(clientVersionJitterMax)))
		timer := time.NewTimer(wait)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			refreshClientVersionOnce()
		}
	}
}
