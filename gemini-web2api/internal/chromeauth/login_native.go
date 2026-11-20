package chromeauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/proxydial"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/proxypool"
	"github.com/gorilla/websocket"
)

const (
	geminiBaseURL  = "https://gemini.google.com"
	geminiStartURL = "https://gemini.google.com/app"
)

var (
	wizSNlM0ePattern = regexp.MustCompile(`"SNlM0e"\s*:\s*"([^"]+)"`)
	wizEmailPattern  = regexp.MustCompile(`"oPEP7c"\s*:\s*"([^"@]+@[^"]+)"`)
)

// IsolatedLoginRequest describes parameters for launching an isolated local Chrome window to sign in to Gemini Web.
type IsolatedLoginRequest struct {
	BrowserPath string
	ProxyURL    string
	AccountID   string
	OutputDir   string
	Timeout     time.Duration
}

// IsolatedLoginResult contains the captured StorageState and verified account identity.
type IsolatedLoginResult struct {
	AccountID    string
	Email        string
	StorageState auth.StorageState
	StoragePath  string
	VerifiedAt   time.Time
	CloseError   string
}

// FindLocalChromeBinary locates an installed Chrome / Chromium / Edge / Brave executable on the host.
func FindLocalChromeBinary() string {
	for _, envKey := range []string{"GEMINI_CHROME_PATH", "CHROME_PATH", "BROWSER_PATH"} {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			if info, err := os.Stat(v); err == nil && !info.IsDir() {
				return v
			}
		}
	}

	var candidates []string
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		appRelPaths := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		for _, rel := range appRelPaths {
			candidates = append(candidates, filepath.Join("/Applications", rel))
			if home != "" {
				candidates = append(candidates, filepath.Join(home, "Applications", rel))
			}
		}
	case "windows":
		roots := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LocalAppData"),
		}
		relPaths := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Chromium\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			for _, rel := range relPaths {
				candidates = append(candidates, filepath.Join(root, rel))
			}
		}
	default:
		candidates = append(candidates,
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
			"/usr/bin/microsoft-edge",
			"/usr/bin/brave-browser",
		)
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}

	for _, binName := range []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"chrome",
		"msedge",
		"microsoft-edge",
		"brave-browser",
	} {
		if path, err := exec.LookPath(binName); err == nil && path != "" {
			return path
		}
	}
	return ""
}

// RunIncognitoLogin launches an isolated local Chrome window (--user-data-dir temp dir),
// waits for the user to sign in to Google Gemini Web (gemini.google.com), extracts cookies over CDP,
// verifies the session with gemini.NewClient, and closes the browser window.
func RunIncognitoLogin(ctx context.Context, req IsolatedLoginRequest) (IsolatedLoginResult, error) {
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return IsolatedLoginResult{}, errors.New("当前服务器无图形桌面环境（缺少 DISPLAY / WAYLAND_DISPLAY），无法弹出本机 Chrome 窗口，请粘贴 Cookie 导入")
	}

	chromeBin := strings.TrimSpace(req.BrowserPath)
	if chromeBin == "" {
		chromeBin = FindLocalChromeBinary()
	}
	if chromeBin == "" {
		return IsolatedLoginResult{}, errors.New("未找到本机 Chrome / Edge / Brave 浏览器，请安装 Chrome 或设置环境变量 GEMINI_CHROME_PATH")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	effectiveProxy, err := resolveLoginProxy(req.ProxyURL)
	if err != nil {
		return IsolatedLoginResult{}, err
	}

	browserProxyArg, cleanupRelay, err := prepareBrowserProxy(effectiveProxy)
	if err != nil {
		return IsolatedLoginResult{}, err
	}
	defer cleanupRelay()

	debugPort, err := pickFreeLocalPort()
	if err != nil {
		return IsolatedLoginResult{}, fmt.Errorf("分配本地调试端口失败: %w", err)
	}

	userDataDir, err := os.MkdirTemp("", "gemini-web2api-chrome-login-*")
	if err != nil {
		return IsolatedLoginResult{}, fmt.Errorf("创建临时浏览器配置目录失败: %w", err)
	}
	defer func() {
		for range 6 {
			if os.RemoveAll(userDataDir) == nil {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-blink-features=AutomationControlled",
		"--disable-features=Translate,OptimizationHints,MediaRouter,DialMediaRouteProvider,CalculateNativeWinOcclusion,EnableBoundSessionCredentials,DeviceBoundSessions,PersistDeviceBoundSessions,EnableChromeRefreshTokenBinding",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-background-networking",
		"--disable-sync",
		"--new-window",
	}
	if browserProxyArg != "" {
		args = append(args, "--proxy-server="+browserProxyArg)
	}
	args = append(args, geminiStartURL)

	cmd := exec.CommandContext(loginCtx, chromeBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return IsolatedLoginResult{}, fmt.Errorf("启动 Chrome 浏览器失败: %w", err)
	}

	var procExited atomic.Bool
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		procExited.Store(true)
		close(waitDone)
	}()

	closeBrowser := func(wsURL string) error {
		if wsURL != "" {
			_ = closeBrowserOverCDP(wsURL)
		}
		select {
		case <-waitDone:
			return nil
		case <-time.After(1500 * time.Millisecond):
		}
		if cmd.Process != nil && !procExited.Load() {
			if runtime.GOOS == "windows" {
				_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			}
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitDone:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("等待 Chrome 进程退出超时")
		}
	}
	defer func() {
		if !procExited.Load() {
			_ = closeBrowser("")
		}
	}()

	httpClient := &http.Client{Timeout: 3 * time.Second}
	if err := waitForCDPReady(loginCtx, httpClient, debugPort, &procExited, &stderr); err != nil {
		return IsolatedLoginResult{}, err
	}

	var (
		capturedCookies []auth.StateCookie
		verifiedEmail   string
		activeWSURL     string
		lastVerifyHash  string
		lastVerifyTime  time.Time
		injectedTargets = make(map[string]bool)
	)

	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-loginCtx.Done():
			return IsolatedLoginResult{}, errors.New("等待 Google Gemini Web 登录超时（5 分钟），请重试")
		case <-waitDone:
			return IsolatedLoginResult{}, errors.New("浏览器窗口已关闭，登录已取消")
		case <-ticker.C:
		}

		targets, err := listCDPPageTargets(loginCtx, httpClient, debugPort)
		if err != nil || len(targets) == 0 {
			continue
		}

		for _, t := range targets {
			if t.WebSocketDebuggerURL != "" && !injectedTargets[t.ID] {
				injectStealthOverCDP(loginCtx, t.WebSocketDebuggerURL)
				injectedTargets[t.ID] = true
			}
		}

		chosen := pickPreferredTarget(targets)
		if chosen.WebSocketDebuggerURL == "" {
			continue
		}
		activeWSURL = chosen.WebSocketDebuggerURL

		cookies, err := readCookiesOverCDP(loginCtx, activeWSURL)
		if err != nil || !hasRequiredGoogleCookies(cookies) {
			continue
		}

		sig := cookieSignature(cookies)
		if sig == lastVerifyHash && time.Since(lastVerifyTime) < 3*time.Second {
			continue
		}
		lastVerifyHash = sig
		lastVerifyTime = time.Now()

		email, ok := verifyGeminiSession(loginCtx, cookies, effectiveProxy, activeWSURL)
		if !ok {
			continue
		}

		capturedCookies = cookies
		verifiedEmail = email
		break
	}

	closeErr := closeBrowser(activeWSURL)

	accountID := AccountID(req.AccountID)
	if accountID == "" && verifiedEmail != "" {
		accountID = AccountID(verifiedEmail)
	}
	if accountID == "" {
		accountID = randomCookieAccountID()
	}

	var storedProxy string
	if strings.TrimSpace(req.ProxyURL) != "" && strings.TrimSpace(req.ProxyURL) != proxypool.Direct {
		if normalized, err := ValidateProxy(req.ProxyURL); err == nil {
			storedProxy = normalized
		}
	} else if strings.TrimSpace(req.ProxyURL) == proxypool.Direct {
		storedProxy = proxypool.Direct
	}

	state := auth.StorageState{
		Cookies: capturedCookies,
		Origins: []json.RawMessage{},
		Metadata: auth.Metadata{
			ID:    accountID,
			Proxy: storedProxy,
			Source: auth.ImportSource{
				Browser: "chrome-incognito",
			},
		},
	}

	var storagePath string
	if strings.TrimSpace(req.OutputDir) != "" {
		storagePath = filepath.Join(req.OutputDir, accountID, storageFileName)
		state.Metadata = auth.Inherit(storagePath, state.Metadata)
	}
	if !gemini.IsChromeVersion(state.Metadata.Chrome) {
		state.Metadata.Chrome = gemini.PickChromeVersion()
	}
	if storagePath != "" {
		file, err := auth.New(storagePath, state)
		if err != nil {
			return IsolatedLoginResult{}, err
		}
		if err := file.Write(); err != nil {
			return IsolatedLoginResult{}, err
		}
	}
	var closeErrText string
	if closeErr != nil {
		closeErrText = closeErr.Error()
	}
	return IsolatedLoginResult{
		AccountID:    accountID,
		Email:        verifiedEmail,
		StorageState: state,
		StoragePath:  storagePath,
		VerifiedAt:   time.Now().UTC(),
		CloseError:   closeErrText,
	}, nil
}

func randomCookieAccountID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "account-" + hex.EncodeToString(b[:])
}

func resolveLoginProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return proxypool.System(), nil
	}
	if raw == proxypool.Direct {
		return "", nil
	}
	return ValidateProxy(raw)
}

func prepareBrowserProxy(proxyAddr string) (string, func(), error) {
	noop := func() {}
	proxyAddr = strings.TrimSpace(proxyAddr)
	if proxyAddr == "" {
		return "", noop, nil
	}
	parsed, err := url.Parse(proxyAddr)
	if err != nil {
		return "", noop, fmt.Errorf("解析代理地址失败: %w", err)
	}
	if parsed.User == nil && !strings.EqualFold(parsed.Scheme, "https") {
		return proxyAddr, noop, nil
	}
	relay, err := proxydial.StartRelay(parsed)
	if err != nil {
		return "", noop, fmt.Errorf("启动本机代理中转失败: %w", err)
	}
	return "socks5://" + relay.Addr().String(), func() { _ = relay.Close() }, nil
}

func pickFreeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, errors.New("无法获取空闲端口")
	}
	return addr.Port, nil
}

type cdpPageTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func waitForCDPReady(ctx context.Context, client *http.Client, port int, exited *atomic.Bool, stderr *bytes.Buffer) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if exited.Load() {
			msg := strings.TrimSpace(stderr.String())
			if msg != "" {
				return fmt.Errorf("Chrome 启动后立即退出: %s", msg)
			}
			return errors.New("Chrome 启动后立即退出")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return errors.New("等待 Chrome 远程调试端口就绪超时")
}

func listCDPPageTargets(ctx context.Context, client *http.Client, port int) ([]cdpPageTarget, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDP /json/list status %d", resp.StatusCode)
	}
	var all []cdpPageTarget
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	pages := make([]cdpPageTarget, 0, len(all))
	for _, item := range all {
		if item.Type == "page" && item.WebSocketDebuggerURL != "" {
			pages = append(pages, item)
		}
	}
	return pages, nil
}

func pickPreferredTarget(targets []cdpPageTarget) cdpPageTarget {
	for _, t := range targets {
		if strings.HasPrefix(t.URL, geminiBaseURL) {
			return t
		}
	}
	for _, t := range targets {
		if strings.Contains(t.URL, "accounts.google.com") {
			return t
		}
	}
	return targets[0]
}

const stealthInitScript = `(() => {
	try {
		Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
	} catch (_) {}
})();`

func injectStealthOverCDP(ctx context.Context, wsURL string) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(callCtx, wsURL, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var out map[string]any
	_ = cdpCall(callCtx, conn, 1, "Page.enable", nil, &out)
	_ = cdpCall(callCtx, conn, 2, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": stealthInitScript,
	}, &out)
	_ = cdpCall(callCtx, conn, 3, "Runtime.evaluate", map[string]any{
		"expression": stealthInitScript,
	}, &out)
}

type cdpCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite"`
}

func readCookiesOverCDP(ctx context.Context, wsURL string) ([]auth.StateCookie, error) {
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(callCtx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var result struct {
		Cookies []cdpCookie `json:"cookies"`
	}
	if err := cdpCall(callCtx, conn, 1, "Network.getAllCookies", nil, &result); err != nil {
		return nil, err
	}

	out := make([]auth.StateCookie, 0, len(result.Cookies))
	for _, c := range result.Cookies {
		if c.Name == "" || c.Value == "" || !googleDomain(c.Domain) {
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		out = append(out, auth.StateCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     path,
			Expires:  c.Expires,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: sameSiteName(c.SameSite),
		})
	}
	return out, nil
}

func hasRequiredGoogleCookies(cookies []auth.StateCookie) bool {
	var hasSAPISID, hasPSID bool
	for _, c := range cookies {
		switch c.Name {
		case "SAPISID", "__Secure-3PAPISID", "__Secure-1PAPISID":
			if strings.TrimSpace(c.Value) != "" {
				hasSAPISID = true
			}
		case "__Secure-1PSID", "SID":
			if strings.TrimSpace(c.Value) != "" {
				hasPSID = true
			}
		}
	}
	return hasSAPISID && hasPSID
}

func cookieSignature(cookies []auth.StateCookie) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Domain+":"+c.Name+"="+c.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func verifyGeminiSession(ctx context.Context, cookies []auth.StateCookie, proxyAddr string, wsURL string) (string, bool) {
	if email, snlm0e := inspectGeminiPageOverCDP(ctx, wsURL); snlm0e != "" {
		if email != "" {
			return strings.ToLower(strings.TrimSpace(email)), true
		}
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	geminiCookies := auth.GeminiCookies(cookies)
	client, err := gemini.NewClient(gemini.AccountSource{ID: "verify", Cookies: geminiCookies}, proxyAddr, false)
	if err != nil {
		return "", false
	}
	defer client.CloseIdleConnections()
	if err := client.Init(verifyCtx); err != nil {
		return "", false
	}
	email := strings.ToLower(strings.TrimSpace(client.Email()))
	return email, true
}

func inspectGeminiPageOverCDP(ctx context.Context, wsURL string) (email string, snlm0e string) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(callCtx, wsURL, nil)
	if err != nil {
		return "", ""
	}
	defer conn.Close()

	var evalResp struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	expr := `(() => {
		try {
			if (!location.hostname.endsWith('gemini.google.com')) return '';
			const html = document.documentElement ? document.documentElement.innerHTML : '';
			return html;
		} catch (_) {
			return '';
		}
	})()`
	if err := cdpCall(callCtx, conn, 1, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, &evalResp); err != nil {
		return "", ""
	}
	html := evalResp.Result.Value
	if html == "" {
		return "", ""
	}
	if m := wizSNlM0ePattern.FindStringSubmatch(html); len(m) > 1 {
		snlm0e = m[1]
	}
	if m := wizEmailPattern.FindStringSubmatch(html); len(m) > 1 {
		email = m[1]
	}
	return email, snlm0e
}

func closeBrowserOverCDP(wsURL string) error {
	callCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(callCtx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	var out map[string]any
	return cdpCall(callCtx, conn, 1, "Browser.close", nil, &out)
}

type cdpRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type cdpResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func cdpCall(ctx context.Context, conn *websocket.Conn, id int, method string, params any, out any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.SetReadDeadline(deadline)
	}
	if err := conn.WriteJSON(cdpRequest{ID: id, Method: method, Params: params}); err != nil {
		return err
	}
	for {
		var msg cdpResponse
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("CDP %s error (%d): %s", method, msg.Error.Code, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	}
}
