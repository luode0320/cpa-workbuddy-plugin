package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	authFilesMu sync.RWMutex
	accountsDir string
)

// StoredAuth 是落盘的豆包账号结构体。
//
// CookieHeader 是全部凭据（豆包无独立 token，会话即身份）；SecUserID / UserID /
// ScreenName 用于账号标识与面板展示；DeviceID / WebID / WebTabID 是设备级标识，
// 业务 API 的 query 参数集需要它们（见 protocol.go），故与凭据一同持久化。
type StoredAuth struct {
	Type          string `json:"type"`
	Provider      string `json:"provider"`
	AuthID        string `json:"auth_id"`
	CookieHeader  string `json:"cookie_header"`
	FlowSecUserID string `json:"flow_cur_user_sec_id,omitempty"`
	SecUserID     string `json:"sec_user_id,omitempty"`
	UserID        int64  `json:"user_id,omitempty"`
	ScreenName    string `json:"screen_name,omitempty"`
	Mobile        string `json:"mobile,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	WebID         string `json:"web_id,omitempty"`
	WebTabID      string `json:"web_tab_id,omitempty"`
	BotID         string `json:"bot_id,omitempty"`
	// Cookies 是上游滚动下发的 Cookie 快照（见 cookiejar.go）。
	// 首次导入时为空，第一次请求后由 durableJar 写回；与 CookieHeader 并存：
	// 前者是随上游续期的滚动值，后者是用户导入时的原始登录态快照。
	Cookies      []savedCookie `json:"cookies,omitempty"`
	Label        string        `json:"label"`
	Disabled     bool          `json:"disabled"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	PhysicalPath string        `json:"-"`
}

type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
}

// setAccountsDir 设置账号存储目录（供测试覆盖）。
// [参数] dir: 目标目录。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，测试注入点。
func setAccountsDir(dir string) {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()
	accountsDir = dir
}

// getAccountsDir 获取账号存储目录，默认 ~/.antigravity_cockpit/doubao_accounts/。
// [参数] 无。
// [返回] 账号目录绝对路径。
// 最近修改时间：2026-10-17；改动原因：新增，账号目录解析。
func getAccountsDir() string {
	authFilesMu.RLock()
	if accountsDir != "" {
		defer authFilesMu.RUnlock()
		return accountsDir
	}
	authFilesMu.RUnlock()

	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".antigravity_cockpit", "doubao_accounts")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// isDoubaoAuthFileName 判断文件名是否属于豆包凭据命名空间。
// [参数] name: 文件名。
// [返回] 属于时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，命名空间隔离判定。
func isDoubaoAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, doubaoAuthFilePrefix)
}

// isSafeDoubaoAuthPath 校验文件路径前缀与路径穿越。
// [参数] path: 待校验路径。
// [返回] 安全时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，落盘路径安全闸门。
func isSafeDoubaoAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "../") || strings.Contains(slash, "/..") {
		return false
	}
	base := filepath.Base(path)
	if !isDoubaoAuthFileName(base) {
		return false
	}
	return base == filepath.Base(filepath.Clean(path))
}

// writeAuthFileDirect 原子落盘写入账号凭据 JSON 文件，并在宿主在线时同步。
// [参数] filePath: 目标文件路径；auth: 账号结构。
// [返回] 写入错误。
// 最近修改时间：2026-10-17；改动原因：新增，凭据物理落盘（不走 host.auth.save 以免丢未知字段）。
func writeAuthFileDirect(filePath string, auth *StoredAuth) error {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	if auth.Type == "" {
		auth.Type = providerName
	}
	if auth.Provider == "" {
		auth.Provider = providerName
	}

	targetPath := filePath
	if auth.PhysicalPath != "" && isSafeDoubaoAuthPath(auth.PhysicalPath) {
		targetPath = auth.PhysicalPath
	}
	if !isSafeDoubaoAuthPath(targetPath) {
		return fmt.Errorf("不安全的凭据文件路径：%s", targetPath)
	}

	data, errMarshal := json.MarshalIndent(auth, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("序列化凭据失败：%w", errMarshal)
	}

	dir := filepath.Dir(targetPath)
	if errMkdir := os.MkdirAll(dir, 0755); errMkdir != nil {
		return fmt.Errorf("创建目录失败：%w", errMkdir)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if errWrite := os.WriteFile(tmpFile, data, 0600); errWrite != nil {
		return fmt.Errorf("写入临时文件失败：%w", errWrite)
	}
	if errRename := os.Rename(tmpFile, targetPath); errRename != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("重命名临时文件失败：%w", errRename)
	}

	// 宿主可用且尚未关联物理路径时，通过 host.auth.save 同步注册。
	if auth.PhysicalPath == "" && !auth.Disabled {
		_ = hostAuthSaveJSON(filepath.Base(targetPath), data)
	}
	return nil
}

// hostAuthSaveJSON 通过宿主 RPC 保存认证文件。
// [参数] name: 文件名；raw: 文件内容。
// [返回] 同步错误。
// 最近修改时间：2026-10-17；改动原因：新增，宿主凭据注册通道。
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("凭据文件名为空")
	}
	saveReq := pluginapi.HostAuthSaveRequest{Name: name, JSON: raw}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, errCall := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if errCall != nil {
		return errCall
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResp, &env); errUnmarshal != nil || !env.OK {
		return fmt.Errorf("host.auth.save 调用失败")
	}
	return nil
}

// readAuthFile 读取单个账号凭据文件。
// [参数] filePath: 文件路径。
// [返回] 账号结构与读取错误。
// 最近修改时间：2026-10-17；改动原因：新增，单账号读取入口。
func readAuthFile(filePath string) (*StoredAuth, error) {
	authFilesMu.RLock()
	defer authFilesMu.RUnlock()

	if !isSafeDoubaoAuthPath(filePath) {
		return nil, fmt.Errorf("不安全的凭据文件路径：%s", filePath)
	}
	data, errRead := os.ReadFile(filePath)
	if errRead != nil {
		return nil, errRead
	}
	var auth StoredAuth
	if errUnmarshal := json.Unmarshal(data, &auth); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	auth.PhysicalPath = filePath
	return &auth, nil
}

// listHostAuthFiles 从宿主 host.auth.list 获取所有豆包账号。
// [参数] 无。
// [返回] 账号列表；宿主不可用时返回 nil。
// 最近修改时间：2026-10-17；改动原因：新增，宿主账号来源。
func listHostAuthFiles() []*StoredAuth {
	raw, errCall := hostCall(pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		return nil
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil || !env.OK {
		return nil
	}
	var resp rpcHostAuthListResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		return nil
	}

	var out []*StoredAuth
	for _, f := range resp.Files {
		if !isDoubaoAuthFileName(f.Name) {
			continue
		}
		getBody, _ := json.Marshal(map[string]string{"auth_index": f.AuthIndex})
		getRaw, errGet := hostCall(pluginabi.MethodHostAuthGet, getBody)
		if errGet != nil {
			continue
		}
		var getEnv envelope
		if errUnmarshal := json.Unmarshal(getRaw, &getEnv); errUnmarshal != nil || !getEnv.OK {
			continue
		}
		var getResp rpcHostAuthGetResponse
		if errUnmarshal := json.Unmarshal(getEnv.Result, &getResp); errUnmarshal != nil {
			continue
		}
		var sa StoredAuth
		if errUnmarshal := json.Unmarshal(getResp.JSON, &sa); errUnmarshal == nil && sa.CookieHeader != "" {
			if sa.AuthID == "" {
				sa.AuthID = strings.TrimSuffix(f.Name, ".json")
			}
			sa.Disabled = sa.Disabled || f.Disabled
			sa.PhysicalPath = getResp.Path
			out = append(out, &sa)
		}
	}
	return out
}

// listAllAuthFiles 列出所有有效账号（合并宿主与本地目录，按 AuthID 去重）。
//
// 去重键是账号标识而不是文件名：同一个物理文件可能被宿主以两种 ID 形态注册
// （带 .json 的路径形态与插件回传的裸标识），旧版本因此让面板出现两条一模一样的
// 账号。这里统一按 AuthID 收敛，保留先出现的那条。
// [参数] 无。
// [返回] 账号列表与错误。
// 最近修改时间：2026-10-17；改动原因：宿主来源补去重，修复一个账号显示两条。
func listAllAuthFiles() ([]*StoredAuth, error) {
	hostAccounts := dedupeAuthByID(listHostAuthFiles())

	seen := make(map[string]*StoredAuth, len(hostAccounts))
	list := make([]*StoredAuth, 0, len(hostAccounts))
	for _, sa := range hostAccounts {
		seen[sa.AuthID] = sa
		list = append(list, sa)
	}

	// 本地目录补充。
	dir := getAccountsDir()
	authFilesMu.RLock()
	entries, errReadDir := os.ReadDir(dir)
	if errReadDir != nil && !os.IsNotExist(errReadDir) {
		authFilesMu.RUnlock()
		return list, errReadDir
	}
	for _, entry := range entries {
		if entry.IsDir() || !isDoubaoAuthFileName(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		data, errRead := os.ReadFile(fullPath)
		if errRead != nil {
			continue
		}
		var sa StoredAuth
		if errUnmarshal := json.Unmarshal(data, &sa); errUnmarshal == nil && sa.CookieHeader != "" {
			if sa.AuthID == "" {
				sa.AuthID = strings.TrimSuffix(entry.Name(), ".json")
			}
			if _, exists := seen[sa.AuthID]; !exists {
				sa.PhysicalPath = fullPath
				seen[sa.AuthID] = &sa
				list = append(list, &sa)
			}
		}
	}
	authFilesMu.RUnlock()
	return list, nil
}

// dedupeAuthByID 按账号标识去重，保留首次出现的条目。
//
// 宿主同一物理文件可能以两种 ID 形态出现（路径形态带 .json、插件回传形态不带），
// 两者账号标识相同。抽成独立函数是为了让这条收敛规则可被单测直接覆盖：
// 宿主来源在测试环境不可用，无法通过 listAllAuthFiles 走到该分支。
// [参数] accounts: 原始账号列表，允许含 nil。
// [返回] 去重后的列表，顺序按首次出现。
// 最近修改时间：2026-10-17；改动原因：新增，重复注册收敛。
func dedupeAuthByID(accounts []*StoredAuth) []*StoredAuth {
	seen := make(map[string]struct{}, len(accounts))
	out := make([]*StoredAuth, 0, len(accounts))
	for _, sa := range accounts {
		if sa == nil {
			continue
		}
		if _, exists := seen[sa.AuthID]; exists {
			continue
		}
		seen[sa.AuthID] = struct{}{}
		out = append(out, sa)
	}
	return out
}

// saveCredentials 校验并落盘一份导入的登录态凭据。
//
// 流程：解析 Cookie header → 字段齐备性校验 → 判据端点确认登录态 → 落盘。
// 判据不通过时**不落盘**，避免把垃圾凭据写成可用账号（实测伪造 Cookie 仍能
// 让 /alice/basic/launch 返回 code:0，故不能只看业务端点的 code）。
// [参数] header: 用户粘贴的 Cookie header；label: 备注名（可空）；timeout: 判据请求超时。
// [返回] 落盘后的账号结构与错误。
// 最近修改时间：2026-10-17；改动原因：新增，导入主链路（解析 → 校验 → 探测 → 落盘）。
func saveCredentials(header, label string, timeout time.Duration) (*StoredAuth, error) {
	creds, errParse := parseCredentials(header)
	if errParse != nil {
		return nil, errParse
	}

	probed, errProbe := probeAccountInfo(creds, timeout)
	if errProbe != nil {
		return nil, errProbe
	}

	authID := authIDFor(probed)
	if authID == "" {
		return nil, fmt.Errorf("无法派生账号标识：凭据缺少 sec_user_id 与 flow_cur_user_sec_id")
	}
	fileName := authFileNameFor(probed)
	fullPath := filepath.Join(getAccountsDir(), fileName)

	if strings.TrimSpace(label) == "" {
		label = strings.TrimSpace(probed.ScreenName)
	}
	if label == "" {
		label = "豆包 " + maskIdentifier(authID)
	}

	now := time.Now().Format(time.RFC3339)
	sa := &StoredAuth{
		Type:          providerName,
		Provider:      providerName,
		AuthID:        authID,
		CookieHeader:  probed.CookieHeader,
		FlowSecUserID: probed.FlowSecUserID,
		SecUserID:     probed.SecUserID,
		UserID:        probed.UserID,
		ScreenName:    probed.ScreenName,
		Mobile:        probed.Mobile,
		BotID:         defaultBotID,
		Label:         label,
		Disabled:      false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	// 导入即固化设备标识：工作模式每次请求都要带 device_id / fp / tea_uuid，
	// 同一账号换标识会被上游按多设备登录归因，必须与账号一起持久化。
	sa.DeviceID = deviceIDFor(sa)
	sa.WebID = strings.TrimSpace(sa.WebID)
	sa.WebTabID = randomUUID()
	if errWrite := writeAuthFileDirect(fullPath, sa); errWrite != nil {
		return nil, errWrite
	}
	return sa, nil
}

// maskIdentifier 对账号标识做安全脱敏，供展示与默认备注名使用。
// [参数] id: 账号标识。
// [返回] 脱敏后的字符串。
// 最近修改时间：2026-10-17；改动原因：新增，展示层脱敏。
func maskIdentifier(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return "******"
	}
	return id[:4] + "...." + id[len(id)-4:]
}

// deleteAuthFileDirect 删除指定账号文件（同时清理宿主物理路径与本地目录）。
// [参数] authID: 账号标识。
// [返回] 删除错误；目标不存在时返回 os.ErrNotExist。
// 最近修改时间：2026-10-17；改动原因：新增，账号删除入口。
func deleteAuthFileDirect(authID string) error {
	accounts, _ := listAllAuthFiles()
	dir := getAccountsDir()

	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	authID = strings.TrimSpace(authID)
	if !strings.HasPrefix(authID, doubaoAuthFilePrefix) {
		authID = doubaoAuthFilePrefix + authID
	}
	fileName := authID + ".json"
	if !isSafeDoubaoAuthPath(fileName) {
		return fmt.Errorf("不安全的账号标识：%s", authID)
	}

	var removed bool
	for _, acc := range accounts {
		if acc.AuthID == authID && acc.PhysicalPath != "" && isSafeDoubaoAuthPath(acc.PhysicalPath) {
			if errRemove := os.Remove(acc.PhysicalPath); errRemove == nil {
				removed = true
			}
		}
	}

	localPath := filepath.Join(dir, fileName)
	if errRemove := os.Remove(localPath); errRemove == nil {
		removed = true
	}

	if !removed {
		return os.ErrNotExist
	}
	return nil
}
