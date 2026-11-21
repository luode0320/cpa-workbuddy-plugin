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

// pendingImport 是通过验证后写入账号目录的认证状态与验证请求使用的代理
//
// 账号目录不在这里固定：桥接页与扩展推上来的 Cookie 里没有邮箱，
// 账号 ID 只能等校验阶段从会话自身解析出来，因此路径改为写入前按当前 ID 现算。
type pendingImport struct {
	state auth.StorageState
	// output 是账号目录的父目录，最终路径为 <output>/<账号 ID>/storage-state.json
	output       string
	requestProxy string
}

// path 返回按当前账号 ID 计算出的认证状态文件路径
//
// [返回] 形如 <output>/<账号 ID>/storage-state.json 的绝对或相对路径。
// 最近修改时间：2026-11-21；改动原因：新增，替代原先在导入前就固定的路径字段。
func (p pendingImport) path() string {
	return filepath.Join(p.output, p.state.Metadata.ID, storageFileName)
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

// ImportCookies 从 Cookie 文本生成账号状态，返回状态文件路径与模型数。
//
// options.ID 允许为空：桥接页与浏览器扩展推上来的只有 Cookie Header，
// 里面没有邮箱，账号标识只能在校验阶段从 Gemini 会话自身解析出来。
// 因此 id 为空时不预置目录，等校验通过后再按解析出的邮箱落到账号目录。
//
// [参数] ctx 控制整个校验与落盘过程的生命周期；options 是 Cookie 文本、可选的账号 ID、
// 账号目录父路径、固定代理、请求代理与多账号序号。
// [返回] 状态文件路径、本次会话暴露的模型数，以及解析、校验或落盘失败时的错误。
// 最近修改时间：2026-11-21；改动原因：允许账号 ID 为空，改由校验阶段从会话解析。
func ImportCookies(ctx context.Context, options CookieImportOptions) (string, int, error) {
	id := AccountID(options.ID)
	if strings.TrimSpace(options.ID) != "" && id == "" {
		return "", 0, fmt.Errorf("id 必须包含英文字母或数字")
	}
	if options.AuthUser < 0 {
		return "", 0, fmt.Errorf("authuser 不能为负数")
	}
	cookies, err := parseCookies(options.Cookies)
	if err != nil {
		return "", 0, err
	}
	metadata, requestProxy, err := importIdentity(id, options.Output, auth.Metadata{
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
		output:       options.Output,
		requestProxy: requestProxy,
	}
	paths, modelCount, err := validateAndWriteImports(ctx, []pendingImport{pending})
	if err != nil {
		return "", 0, err
	}
	return paths[0], modelCount, nil
}

// importIdentity 返回写入账号目录的账号身份与导入请求使用的代理
//
// 账号已导入过时沿用它的停用标记、Chrome 版本、配对续签摘要与未重新填写的固定代理，
// 新账号按版本池占比分配 Chrome 版本。id 为空表示账号标识尚未确定（桥接页只推了
// Cookie），此时没有任何既有状态可继承，直接按新账号分配 Chrome 版本。
//
// [参数] id 是净化后的账号标识，允许为空；output 是账号目录的父路径；
// metadata 是待补全的账号元信息；fallback 是未指定固定代理时使用的请求代理。
// [返回] 补全后的元信息、本次导入请求实际使用的代理，以及代理参数非法时的错误。
// 最近修改时间：2026-11-21；改动原因：id 允许为空，跳过无从继承的既有状态。
func importIdentity(id string, output string, metadata auth.Metadata, fallback string) (auth.Metadata, string, error) {
	metadata.Proxy = strings.TrimSpace(metadata.Proxy)
	if id != "" {
		path := filepath.Join(output, id, storageFileName)
		metadata = auth.Inherit(path, metadata)
	}
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
//
// 账号 ID 缺失时在这里补齐：校验用的 client 能读出真实邮箱，用它算出账号 ID
// 再落盘，桥接页这种「只有 Cookie、没有邮箱」的导入路径才能闭环。
//
// [参数] ctx 控制校验与落盘的生命周期，单个账号的校验另有 30 秒超时；
// pending 是待验证并落盘的认证状态与请求代理列表。
// [返回] 各账号状态文件路径、所有账号模型并集的数量，以及校验、ID 解析或落盘失败时的错误。
// 最近修改时间：2026-11-21；改动原因：校验后按会话邮箱补齐账号 ID，再按现算路径落盘。
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
			return nil, 0, fmt.Errorf("验证账号: %w", err)
		}
		accountContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = client.Init(accountContext)
		if err == nil {
			_, err = client.FetchUsage(accountContext)
		}
		cancel()
		if err != nil {
			client.CloseIdleConnections()
			return nil, 0, fmt.Errorf("验证账号: %w", err)
		}
		if item.state.Metadata.ID == "" {
			// 邮箱是唯一能从会话里稳定拿到的账号身份来源，拿不到就没法落盘。
			if id := AccountID(client.Email()); id != "" {
				item.state.Metadata.ID = id
			}
		}
		client.CloseIdleConnections()
		for _, model := range client.Models() {
			models[model.ID] = struct{}{}
		}
	}
	paths := make([]string, len(pending))
	for index, item := range pending {
		if item.state.Metadata.ID == "" {
			return nil, 0, fmt.Errorf("无法从账号会话解析出账号标识，请改用「手动粘贴 Cookie」并填写标签")
		}
		// 账号已导入过时沿用它的停用标记、Chrome 版本、配对续签摘要与固定代理。
		item.state.Metadata = auth.Inherit(item.path(), item.state.Metadata)
		file, err := auth.New(item.path(), item.state)
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
