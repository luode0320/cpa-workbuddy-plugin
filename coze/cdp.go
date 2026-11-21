package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 本文件是浏览器保活链路（TASK-06）的 CDP 底座。
//
// 为什么需要它：扣子把「程序化登录接口」按 IP/行为封了，但页面内的同源
// fetch 一直是通的。把续期请求放进真实浏览器上下文，才拿得到与真人一致
// 的埋点与 TLS 指纹。这里只做「起浏览器 / 连调试口 / 发 CDP 命令」，
// 业务语义在 browser_keep.go。
//
// 这套 CDP 客户端与「起浏览器 / 连调试口 / 发命令」的分层沿用 coze 自己的
// 浏览器保活实现，改动时保持 cdpCall、waitForCDPReady、pickFreeLocalPort
// 三处的行为一致，避免再踩一遍同样的坑。

const (
	// cdpReadyTimeout 是等待远程调试端口就绪的上限。
	//
	// 容器里冷启动 Chrome 常要 3-8 秒；给到 20 秒是为了覆盖磁盘慢的场景，
	// 超过这个时长基本可以判定起不来（缺共享库、无 /dev/shm、profile 损坏）。
	cdpReadyTimeout = 20 * time.Second
	// cdpReadyPollInterval 是探活间隔。
	cdpReadyPollInterval = 150 * time.Millisecond
	// cdpDialTimeout 是单次连接页面调试口的超时。
	cdpDialTimeout = 5 * time.Second
	// cdpCallTimeout 是单条 CDP 命令的超时。
	//
	// 页面内换发要等上游响应，给 10 秒；超时即放弃本轮，绝不无限挂住续期线程。
	cdpCallTimeout = 10 * time.Second
	// cdpShutdownTimeout 是关闭浏览器的宽限时间，超时后强杀。
	cdpShutdownTimeout = 3 * time.Second
)

// chromeProcess 表示一个已启动的浏览器实例。
type chromeProcess struct {
	// cmd 是浏览器进程。
	cmd *exec.Cmd
	// port 是远程调试端口。
	port int
	// stderr 收集启动期输出，用于解释「起来就退」的原因。
	stderr *bytes.Buffer
	// exited 标记进程是否已经退出。
	exited *atomic.Bool
}

// errChromeNotFound 表示本机找不到可用的浏览器可执行文件。
var errChromeNotFound = errors.New("未找到 Chrome/Chromium 可执行文件")

// chromeBinaryCandidates 返回当前平台上的候选浏览器路径。
//
// 顺序即优先级：Chrome 优先，Edge 次之（多数 Linux 镜像两个都没有，
// 此时保活链路整体不可用，属预期内的降级）。
// [参数] 无。
// [返回] 候选绝对路径列表。
// 最近修改时间：2026-11-21；改动原因：新增，跨平台定位浏览器。
func chromeBinaryCandidates() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		rel := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
		out := make([]string, 0, len(rel)*2)
		for _, r := range rel {
			out = append(out, filepath.Join("/Applications", r))
			if home != "" {
				out = append(out, filepath.Join(home, "Applications", r))
			}
		}
		return out
	case "windows":
		roots := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LocalAppData"),
		}
		rel := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Chromium\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
		}
		out := make([]string, 0, len(roots)*len(rel))
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			for _, r := range rel {
				out = append(out, filepath.Join(root, r))
			}
		}
		return out
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
			"/usr/bin/microsoft-edge",
		}
	}
}

// findChromeBinary 定位浏览器可执行文件。
//
// 配置项优先于一切：容器里常把 Chrome 装在非标准路径，靠自动探测不可靠。
// [参数] configured: 配置里的 chrome_path，可为空。
// [返回] 可执行文件绝对路径；找不到时返回 errChromeNotFound。
// 最近修改时间：2026-11-21；改动原因：新增，浏览器定位唯一出口。
func findChromeBinary(configured string) (string, error) {
	if v := strings.TrimSpace(configured); v != "" {
		if info, err := os.Stat(v); err == nil && !info.IsDir() {
			return v, nil
		}
		return "", fmt.Errorf("%w：配置的 chrome_path 不可用（%s）", errChromeNotFound, v)
	}
	for _, key := range []string{"COZE_CHROME_PATH", "CHROME_PATH", "BROWSER_PATH"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			if info, err := os.Stat(v); err == nil && !info.IsDir() {
				return v, nil
			}
		}
	}
	for _, candidate := range chromeBinaryCandidates() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errChromeNotFound
}

// pickFreeLocalPort 取一个本地空闲端口给远程调试口用。
//
// 先 Listen 再立即关闭：拿到端口号后到浏览器真正监听之间有一个极短窗口，
// 但同一台机器上并发抢同一个端口的概率可以忽略，且启动失败会被
// waitChromeReady 明确报出来，不会静默错连到别的进程。
// [参数] 无。
// [返回] 端口号或错误。
// 最近修改时间：2026-11-21；改动原因：新增，端口选择。
func pickFreeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, errors.New("无法获取空闲端口")
	}
	return addr.Port, nil
}

// launchChrome 启动一个带远程调试口的浏览器实例。
//
// 三个必须项（已实测，缺一即失败）：
//  1. --user-data-dir 必须指向非默认目录：Chrome 136 起拒绝在默认 profile
//     上开调试端口，表现为端口不监听、进程秒退；
//  2. --disable-extensions：即使是 headless，也会加载宿主已装的扩展，
//     扩展的 service worker 会抢走新标签页，导致页面调试口连上却打不开页面；
//  3. --remote-debugging-address=127.0.0.1：容器里默认可能绑到 0.0.0.0，
//     调试口暴露到网络等于把浏览器控制权送人。
//
// [参数] ctx: 生命周期；binary: 浏览器路径；profileDir: 独立 profile 目录；
// headless: 是否无头。
// [返回] 已启动的进程句柄。
// 最近修改时间：2026-11-21；改动原因：新增，浏览器启动。
func launchChrome(ctx context.Context, binary, profileDir string, headless bool) (*chromeProcess, error) {
	port, errPort := pickFreeLocalPort()
	if errPort != nil {
		return nil, errPort
	}
	if errMkdir := os.MkdirAll(profileDir, 0o700); errMkdir != nil {
		return nil, fmt.Errorf("创建浏览器 profile 目录失败：%w", errMkdir)
	}
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-blink-features=AutomationControlled",
		"--disable-features=Translate,EnableChromeRefreshTokenBinding",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--window-size=1280,900",
		"about:blank",
	}
	if headless {
		// --headless=new 才有完整的渲染与网络栈；旧 --headless 下部分
		// 埋点脚本不执行，换发请求会被上游当成异常客户端。
		args = append([]string{"--headless=new"}, args...)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	// 不给 stdout 接管道：浏览器会往里写大量日志，没人读就和管道容量
	// 较劲，写满即阻塞。
	if errStart := cmd.Start(); errStart != nil {
		return nil, fmt.Errorf("启动浏览器失败：%w", errStart)
	}
	proc := &chromeProcess{cmd: cmd, port: port, stderr: stderr, exited: &atomic.Bool{}}
	go func() {
		_ = cmd.Wait()
		proc.exited.Store(true)
	}()
	return proc, nil
}

// waitChromeReady 等待远程调试端口可用。
// [参数] ctx: 生命周期；client: HTTP 客户端。
// [返回] 就绪返回 nil；进程提前退出或超时返回错误。
// 最近修改时间：2026-11-21；改动原因：新增，就绪探活。
func (p *chromeProcess) waitChromeReady(ctx context.Context, client *http.Client) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", p.port)
	deadline := time.Now().Add(cdpReadyTimeout)
	for time.Now().Before(deadline) {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if p.exited.Load() {
			if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
				return fmt.Errorf("浏览器启动后立即退出：%s", truncateText(msg, 400))
			}
			return errors.New("浏览器启动后立即退出")
		}
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if errReq == nil {
			resp, errDo := client.Do(req)
			if errDo == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cdpReadyPollInterval):
		}
	}
	return fmt.Errorf("等待浏览器远程调试端口就绪超时（%s）", cdpReadyTimeout)
}

// close 关闭浏览器进程。
//
// 先礼后兵：正常退出比强杀更不容易留下写坏的 profile（写坏的 profile
// 下次启动会丢登录态，等于白保活）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，进程回收。
func (p *chromeProcess) close() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	deadline := time.Now().Add(cdpShutdownTimeout)
	for time.Now().Before(deadline) {
		if p.exited.Load() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.cmd.Process.Kill()
}

// cdpRequest 是一条 CDP 命令。
type cdpRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// cdpResponse 是一条 CDP 响应。
type cdpResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// cdpClient 是一个页面级 CDP 连接。
type cdpClient struct {
	// conn 是 websocket 连接。
	conn *websocket.Conn
	// mu 串行化命令：CDP 是单条连接多路复用，两条命令并发写会让响应
	// 交叠，读循环无法正确配对。
	mu sync.Mutex
	// nextID 是命令序号。
	nextID int
}

// call 发一条 CDP 命令并等待同 ID 的响应。
//
// 读循环会丢弃 ID 不匹配的消息（事件推送、迟到的响应），照抄参考实现。
// [参数] ctx: 生命周期；method: CDP 方法名；params: 参数；out: 结果落点，可为 nil。
// [返回] 命令错误或结果解析错误。
// 最近修改时间：2026-11-21；改动原因：新增，CDP 调用唯一出口。
func (c *cdpClient) call(ctx context.Context, method string, params any, out any) error {
	if c == nil || c.conn == nil {
		return errors.New("CDP 连接未建立")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
		_ = c.conn.SetReadDeadline(deadline)
	}
	if errWrite := c.conn.WriteJSON(cdpRequest{ID: id, Method: method, Params: params}); errWrite != nil {
		return fmt.Errorf("发送 CDP 命令 %s 失败：%w", method, errWrite)
	}
	for {
		var msg cdpResponse
		if errRead := c.conn.ReadJSON(&msg); errRead != nil {
			return fmt.Errorf("读取 CDP 响应 %s 失败：%w", method, errRead)
		}
		if msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("CDP %s 错误 (%d)：%s", method, msg.Error.Code, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	}
}

// close 关闭 CDP 连接。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，连接回收。
func (c *cdpClient) close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

// cdpPageTarget 是 /json/list 返回的调试目标。
type cdpPageTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// dialCDPPage 连接浏览器里第一个 page 类型目标的调试口。
// [参数] ctx: 生命周期；client: HTTP 客户端；port: 调试端口。
// [返回] 已建立的 CDP 客户端。
// 最近修改时间：2026-11-21；改动原因：新增，页面调试口连接。
func dialCDPPage(ctx context.Context, client *http.Client, port int) (*cdpClient, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errReq != nil {
		return nil, errReq
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("枚举浏览器调试目标失败：%w", errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	var targets []cdpPageTarget
	if errDecode := json.NewDecoder(resp.Body).Decode(&targets); errDecode != nil {
		return nil, fmt.Errorf("解析调试目标失败：%w", errDecode)
	}
	wsURL := ""
	for _, t := range targets {
		if t.Type == "page" && strings.TrimSpace(t.WebSocketDebuggerURL) != "" {
			wsURL = t.WebSocketDebuggerURL
			break
		}
	}
	if wsURL == "" {
		return nil, errors.New("浏览器内没有可用的页面调试目标")
	}
	dialCtx, cancel := context.WithTimeout(ctx, cdpDialTimeout)
	defer cancel()
	conn, _, errDial := websocket.DefaultDialer.DialContext(dialCtx, wsURL, nil)
	if errDial != nil {
		return nil, fmt.Errorf("连接页面调试口失败：%w", errDial)
	}
	return &cdpClient{conn: conn, nextID: 0}, nil
}

// evaluate 在页面内执行一段脚本并取回字符串结果。
//
// awaitPromise 恒开：页面内的换发是一次 fetch，脚本返回的是 Promise。
// 不走 returnByValue 以外的通途，避免拿到无法序列化的对象。
// [参数] ctx: 生命周期；expr: 脚本表达式。
// [返回] 脚本结果字符串；页面异常时返回错误。
// 最近修改时间：2026-11-21；改动原因：新增，页面内取数。
func (c *cdpClient) evaluate(ctx context.Context, expr string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	params := map[string]any{
		"expression":    expr,
		"awaitPromise":  true,
		"returnByValue": true,
	}
	var result struct {
		Result struct {
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if errCall := c.call(callCtx, "Runtime.evaluate", params, &result); errCall != nil {
		return "", errCall
	}
	if result.ExceptionDetails != nil {
		detail := strings.TrimSpace(result.ExceptionDetails.Exception.Description)
		if detail == "" {
			detail = strings.TrimSpace(result.ExceptionDetails.Text)
		}
		return "", fmt.Errorf("页面脚本异常：%s", truncateText(detail, 300))
	}
	if result.Result.Value == nil {
		return "", nil
	}
	if s, ok := result.Result.Value.(string); ok {
		return s, nil
	}
	raw, errMarshal := json.Marshal(result.Result.Value)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(raw), nil
}

// navigate 让页面跳转到指定地址并等文档就绪。
// [参数] ctx: 生命周期；url: 目标地址。
// [返回] 跳转失败或就绪超时。
// 最近修改时间：2026-11-21；改动原因：新增，页面跳转。
func (c *cdpClient) navigate(ctx context.Context, url string) error {
	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	var out struct {
		FrameID string `json:"frameId"`
	}
	if errNav := c.call(callCtx, "Page.navigate", map[string]any{"url": url}, &out); errNav != nil {
		return errNav
	}
	deadline := time.Now().Add(cdpCallTimeout)
	for time.Now().Before(deadline) {
		state, errState := c.evaluate(ctx, "document.readyState")
		if errState == nil && state == "complete" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("等待页面加载完成超时：%s", url)
}

// cdpCookie 是 CDP 侧的 Cookie 结构。
type cdpCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// setCookies 把 Cookie 注入浏览器。
//
// Expires 为零值时不传该字段：CDP 里 expires=-1 表示会话 Cookie，
// 传 0 反而会被当成 1970 年已过期而被丢弃。
// [参数] ctx: 生命周期；cookies: 待注入的 Cookie。
// [返回] 注入失败错误。
// 最近修改时间：2026-11-21；改动原因：新增，凭据注入。
func (c *cdpClient) setCookies(ctx context.Context, cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	items := make([]cdpCookie, 0, len(cookies))
	for _, ck := range cookies {
		if ck == nil || strings.TrimSpace(ck.Name) == "" {
			continue
		}
		item := cdpCookie{
			Name:     ck.Name,
			Value:    ck.Value,
			Domain:   cookieDomainOrDefault(ck.Domain),
			Path:     ck.Path,
			Secure:   ck.Secure,
			HTTPOnly: ck.HttpOnly,
		}
		if item.Path == "" {
			item.Path = "/"
		}
		if !ck.Expires.IsZero() {
			item.Expires = float64(ck.Expires.Unix())
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	return c.call(callCtx, "Network.setCookies", map[string]any{"cookies": items}, nil)
}

// getAllCookies 取回浏览器当前全部 Cookie。
//
// 用 getAllCookies 而不是 getCookies：换发后服务端可能往别的路径/子域
// 下发会话主项，只取当前 URL 的 Cookie 会漏掉它们。
// [参数] ctx: 生命周期。
// [返回] Cookie 列表。
// 最近修改时间：2026-11-21；改动原因：新增，换发结果回收。
func (c *cdpClient) getAllCookies(ctx context.Context) ([]*http.Cookie, error) {
	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	var out struct {
		Cookies []cdpCookie `json:"cookies"`
	}
	if errCall := c.call(callCtx, "Network.getAllCookies", nil, &out); errCall != nil {
		return nil, errCall
	}
	result := make([]*http.Cookie, 0, len(out.Cookies))
	for _, item := range out.Cookies {
		ck := &http.Cookie{
			Name:     item.Name,
			Value:    item.Value,
			Domain:   item.Domain,
			Path:     item.Path,
			Secure:   item.Secure,
			HttpOnly: item.HTTPOnly,
		}
		if item.Expires > 0 {
			ck.Expires = time.Unix(int64(item.Expires), 0)
		}
		result = append(result, ck)
	}
	return result, nil
}

// cookieDomainOrDefault 补齐 Cookie 的域。
//
// 扣子下发的 Cookie 域有时是空的（服务端按 host-only 下发），注入时必须
// 显式给出域，否则 CDP 会拒收。
// [参数] domain: 原始域。
// [返回] 补全后的域。
// 最近修改时间：2026-11-21；改动原因：新增，域补全。
func cookieDomainOrDefault(domain string) string {
	d := strings.TrimSpace(domain)
	if d != "" {
		return d
	}
	return "www.coze.cn"
}

// truncateText 截断过长的文本，避免把整段浏览器日志塞进错误与日志。
// [参数] text: 原文；limit: 上限字符数。
// [返回] 截断结果。
// 最近修改时间：2026-11-21；改动原因：新增，日志可读性。
func truncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
