package chromeauth

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/proxypool"
)

const (
	defaultImportLanguage = "en-US,en;q=0.9"
	storageFileName       = "storage-state.json"
)

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// pendingImport 是通过验证后写入 path 的认证状态与验证请求使用的代理
type pendingImport struct {
	state        auth.StorageState
	path         string
	requestProxy string
}

// CookieImportOptions 保存 Cookie 导入参数，Cookies 为 Cookie Header、浏览器导出的 JSON 或 Netscape cookies.txt
type CookieImportOptions struct {
	Cookies      string
	ID           string
	Output       string
	Proxy        string
	RequestProxy string
	AuthUser     int
}

// ImportCookies 从 Cookie 文本生成账号状态，返回状态文件路径与模型数
func ImportCookies(ctx context.Context, options CookieImportOptions) (string, int, error) {
	id := AccountID(options.ID)
	if id == "" {
		return "", 0, fmt.Errorf("id 必须包含英文字母或数字")
	}
	if options.AuthUser < 0 {
		return "", 0, fmt.Errorf("authuser 不能为负数")
	}
	cookies, err := parseCookies(options.Cookies)
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(options.Output, id, storageFileName)
	metadata, requestProxy, err := importIdentity(path, auth.Metadata{
		ID:       id,
		Proxy:    options.Proxy,
		Source:   auth.ImportSource{Browser: "cookie-header"},
		Language: defaultImportLanguage,
		AuthUser: options.AuthUser,
	}, options.RequestProxy)
	if err != nil {
		return "", 0, err
	}
	pending := pendingImport{
		state:        auth.StorageState{Cookies: cookies, Metadata: metadata},
		path:         path,
		requestProxy: requestProxy,
	}
	paths, modelCount, err := validateAndWriteImports(ctx, []pendingImport{pending})
	if err != nil {
		return "", 0, err
	}
	return paths[0], modelCount, nil
}

// importIdentity 返回写入 path 的账号身份与导入请求使用的代理：账号已导入过时沿用它的停用标记、Chrome 版本、配对续签摘要与未重新填写的固定代理，新账号按版本池占比分配 Chrome 版本
func importIdentity(path string, metadata auth.Metadata, fallback string) (auth.Metadata, string, error) {
	metadata.Proxy = strings.TrimSpace(metadata.Proxy)
	metadata = auth.Inherit(path, metadata)
	if !gemini.IsChromeVersion(metadata.Chrome) {
		metadata.Chrome = gemini.PickChromeVersion()
	}
	fixed, requestProxy, err := importProxies(metadata.Proxy, fallback)
	metadata.Proxy = fixed
	return metadata, requestProxy, err
}

func importProxies(proxy string, fallback string) (string, string, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy == proxypool.Direct {
		return proxy, "", nil
	}
	fixed, err := ValidateProxy(proxy)
	if err != nil {
		return "", "", err
	}
	return fixed, cmp.Or(fixed, fallback), nil
}

// ValidateProxy 校验 HTTP、HTTPS、SOCKS4 或 SOCKS5 代理 URL 并返回去掉空白的值
func ValidateProxy(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("proxy 必须是 http、https、socks4、socks4a、socks5 或 socks5h URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
		return value, nil
	default:
		return "", fmt.Errorf("proxy 必须是 http、https、socks4、socks4a、socks5 或 socks5h URL")
	}
}

// validateAndWriteImports 以首页、模型目录与用量请求验证每个账号，全部通过后写入认证状态，返回各状态文件的路径与模型数
func validateAndWriteImports(ctx context.Context, pending []pendingImport) ([]string, int, error) {
	models := make(map[string]struct{})
	for index := range pending {
		item := &pending[index]
		source := gemini.AccountSource{
			ID:       item.state.Metadata.ID,
			Cookies:  auth.GeminiCookies(item.state.Cookies),
			Language: item.state.Metadata.Language,
			AuthUser: item.state.Metadata.AuthUser,
			Chrome:   item.state.Metadata.Chrome,
			Save: func(cookies []gemini.Cookie) error {
				item.state.Cookies = auth.StateCookies(cookies)
				return nil
			},
		}
		client, err := gemini.NewClient(source, item.requestProxy, false)
		if err != nil {
			return nil, 0, fmt.Errorf("验证账号 %s: %w", item.state.Metadata.ID, err)
		}
		accountContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = client.Init(accountContext)
		if err == nil {
			_, err = client.FetchUsage(accountContext)
		}
		cancel()
		client.CloseIdleConnections()
		if err != nil {
			return nil, 0, fmt.Errorf("验证账号 %s: %w", item.state.Metadata.ID, err)
		}
		for _, model := range client.Models() {
			models[model.ID] = struct{}{}
		}
	}
	paths := make([]string, len(pending))
	for index, item := range pending {
		file, err := auth.New(item.path, item.state)
		if err != nil {
			return nil, 0, err
		}
		if err := file.Write(); err != nil {
			return nil, 0, err
		}
		paths[index] = file.Path()
	}
	return paths, len(models), nil
}

// AccountID 返回邮箱对应的账号 ID
func AccountID(email string) string {
	return strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(email)), "-"), "-")
}
