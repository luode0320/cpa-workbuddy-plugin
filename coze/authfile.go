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

// 本文件负责扣子账号凭据的落盘、读取与宿主注册。
//
// 落盘必须直写物理文件而不是走 host.auth.save：宿主保存会丢掉它不认识的
// 顶层字段（本插件的 cookies / login_at / last_renew_at 都在此列），
// 导致续期后的 Cookie 快照无法保存。

var (
	authFilesMu sync.RWMutex
	accountsDir string
)

// StoredAuth 是落盘的扣子账号结构体。
//
// CookieHeader 是当前生效的完整 Cookie 头（由 token beat 下发后拼装）；
// Cookies 是带 domain/path 属性的快照，重启时用于重建容器——
// 两者并存是因为 header 便于直接发请求，快照才能正确重放同名分域 Cookie。
type StoredAuth struct {
	Type         string `json:"type"`
	Provider     string `json:"provider"`
	AuthID       string `json:"auth_id"`
	CookieHeader string `json:"cookie_header"`
	SecUserID    string `json:"sec_user_id,omitempty"`
	UserID       int64  `json:"user_id,omitempty"`
	ScreenName   string `json:"screen_name,omitempty"`
	Mobile       string `json:"mobile,omitempty"`
	// Cookies 是带属性的 Cookie 快照，见 cookiejar.go。
	Cookies []savedCookie `json:"cookies,omitempty"`
	// LoginAt 是首次授权成功时间；LastRenewAt 是最近一次成功续期时间。
	// 两者分开记录，面板据此区分「刚登录」与「靠续期维持」的账号。
	LoginAt     string `json:"login_at,omitempty"`
	LastRenewAt string `json:"last_renew_at,omitempty"`
	// AuthInvalid 表示上游已明确判定该账号会话失效（如续期返回「会话过期」）。
	//
	// 不能只靠 Cookie 的 Expires 判活：扣子的会话 Cookie 不下发 Expires，
	// 本地看起来永不过期，但上游早已作废（2026-10-19 多账号实测）。
	// 该标记只由「上游明确拒绝」写入，重新授权或续期成功即清除。
	AuthInvalid bool   `json:"auth_invalid,omitempty"`
	Label       string `json:"label"`
	Disabled    bool   `json:"disabled"`
	// LowCreditDisable 表示该账号的禁用由「积分低于红线」机制写入。
	//
	// 它是归属标记：只有带此标记的账号才会被自动解禁。用户手动禁用
	// 或其它机制禁用的账号没有该标记，永不被巡检改动（见 credit_grade.go）。
	LowCreditDisable bool `json:"low_credit_disable,omitempty"`
	// LowCreditDisableBelow 记录禁用时的阈值，供人工判断「为什么被禁」。
	LowCreditDisableBelow int64 `json:"low_credit_disable_below,omitempty"`
	// LowCreditDisableAt 是禁用时刻（RFC3339），供面板展示。
	LowCreditDisableAt string `json:"low_credit_disable_at,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	// PhysicalPath 是账号文件绝对路径，不落盘（json:"-"）。
	PhysicalPath string `json:"-"`
	// LoadedDigest 是读取该账号时物理文件的内容摘要，不落盘（json:"-"）。
	//
	// 防覆盖判据必须锚定「读取那一刻」的内容：若在写入前才取摘要，
	// 比对的永远是文件自身，外部重新授权写入的新凭据会被静默覆盖
	// （2026-10-18 单测 TestApplyRenewal_SkipsWhenExternallyReplaced 实证）。
	LoadedDigest string `json:"-"`
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
// 最近修改时间：2026-10-18；改动原因：新增，测试注入点。
func setAccountsDir(dir string) {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()
	accountsDir = dir
}

// getAccountsDir 获取账号存储目录，默认 ~/.antigravity_cockpit/coze_accounts/。
// [参数] 无。
// [返回] 账号目录绝对路径。
// 最近修改时间：2026-10-18；改动原因：新增，账号目录解析。
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
	dir := filepath.Join(home, ".antigravity_cockpit", "coze_accounts")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// isCozeAuthFileName 判断文件名是否属于扣子凭据命名空间。
// [参数] name: 文件名。
// [返回] 属于时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，命名空间隔离判定。
func isCozeAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, cozeAuthFilePrefix)
}

// isSafeCozeAuthPath 校验文件路径前缀与路径穿越。
//
// 账号标识来自请求体（删除/启停接口），必须防止 "../" 把写入或删除
// 引到账号目录之外——这是文件操作的安全边界。
// [参数] path: 待校验路径。
// [返回] 安全时返回 true。
// 最近修改时间：2026-10-18；改动原因：新增，落盘路径安全闸门。
func isSafeCozeAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "../") || strings.Contains(slash, "/..") {
		return false
	}
	base := filepath.Base(path)
	if !isCozeAuthFileName(base) {
		return false
	}
	return base == filepath.Base(filepath.Clean(path))
}

// writeAuthFileDirect 原子落盘写入账号凭据 JSON 文件，并在宿主在线时同步。
// [参数] filePath: 目标文件路径；auth: 账号结构。
// [返回] 写入错误。
// 最近修改时间：2026-10-18；改动原因：新增，凭据物理落盘。
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
	if auth.PhysicalPath != "" && isSafeCozeAuthPath(auth.PhysicalPath) {
		targetPath = auth.PhysicalPath
	}
	if !isSafeCozeAuthPath(targetPath) {
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

// readAuthFileRaw 读取凭据文件的原始字节。
//
// 补注册到宿主时用它是为了「原样搬运」：宿主侧解析的是同一份 JSON，
// 反向重新序列化会丢掉字段顺序与未知字段，也可能把一个刚续期的
// 凭据写成内存快照的形态。
// [参数] filePath: 文件路径。
// [返回] 文件字节与读取错误。
// 最近修改时间：2026-10-24；改动原因：新增，宿主补注册原样搬运。
func readAuthFileRaw(filePath string) ([]byte, error) {
	authFilesMu.RLock()
	defer authFilesMu.RUnlock()

	if !isSafeCozeAuthPath(filePath) {
		return nil, fmt.Errorf("不安全的凭据文件路径：%s", filePath)
	}
	return os.ReadFile(filePath)
}

// hostAuthSaveJSON 通过宿主 RPC 保存认证文件。
// [参数] name: 文件名；raw: 文件内容。
// [返回] 同步错误。
// 最近修改时间：2026-10-18；改动原因：新增，宿主凭据注册通道。
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

// mutateAuthFile 在账号文件锁内完成「读盘 → 判定 → 写盘」。
//
// 为什么不能复用 readAuthFile + writeAuthFileDirect：那两步各自加锁，
// 中间的判定窗口里用户可能刚点过「禁用」，巡检的旧判定会把它覆盖成
// 「自动禁用」，次日积分到账后又自动解禁——违背用户意图。
// 判定必须基于磁盘的当前内容，因此读与写必须在同一把锁内完成。
//
// mutate 返回 false 表示放弃写入（判定认为无需改动）。
// [参数] filePath: 账号文件绝对路径；mutate: 在锁内对账号结构做原地修改。
// [返回] 是否真的写入了磁盘，以及写入错误。
// 最近修改时间：2026-10-25；改动原因：新增，分级判定需要原子读改写。
func mutateAuthFile(filePath string, mutate func(*StoredAuth) bool) (bool, error) {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	// 1. 路径安全闸门：账号路径来自宿主返回，仍须防穿越。
	if !isSafeCozeAuthPath(filePath) {
		return false, fmt.Errorf("不安全的凭据文件路径：%s", filePath)
	}
	// 2. 读盘拿到当前真相。
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		return false, errRead
	}
	var auth StoredAuth
	if errUnmarshal := json.Unmarshal(raw, &auth); errUnmarshal != nil {
		return false, errUnmarshal
	}
	auth.PhysicalPath = filePath
	auth.LoadedDigest = fileDigest(filePath)

	// 3. 在锁内做判定；判定认为无需改动时直接放弃，避免无谓写盘。
	if mutate == nil || !mutate(&auth) {
		return false, nil
	}
	auth.UpdatedAt = time.Now().Format(time.RFC3339)

	// 4. 序列化并原子写回（与 writeAuthFileDirect 同一套临时文件语义）。
	data, errMarshal := json.MarshalIndent(&auth, "", "  ")
	if errMarshal != nil {
		return false, fmt.Errorf("序列化凭据失败：%w", errMarshal)
	}
	tmpFile := fmt.Sprintf("%s.tmp.%d", filePath, time.Now().UnixNano())
	if errWrite := os.WriteFile(tmpFile, data, 0600); errWrite != nil {
		return false, fmt.Errorf("写入临时文件失败：%w", errWrite)
	}
	if errRename := os.Rename(tmpFile, filePath); errRename != nil {
		_ = os.Remove(tmpFile)
		return false, fmt.Errorf("重命名临时文件失败：%w", errRename)
	}
	return true, nil
}

// readAuthFile 读取单个账号凭据文件。
// [参数] filePath: 文件路径。
// [返回] 账号结构与读取错误。
// 最近修改时间：2026-10-18；改动原因：新增，单账号读取入口。
func readAuthFile(filePath string) (*StoredAuth, error) {
	authFilesMu.RLock()
	defer authFilesMu.RUnlock()

	if !isSafeCozeAuthPath(filePath) {
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
	auth.LoadedDigest = fileDigest(filePath)
	return &auth, nil
}

// listHostAuthFiles 从宿主 host.auth.list 获取所有扣子账号。
// [参数] 无。
// [返回] 账号列表；宿主不可用时返回 nil。
// 最近修改时间：2026-10-18；改动原因：新增，宿主账号来源。
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
		if !isCozeAuthFileName(f.Name) {
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
			sa.LoadedDigest = fileDigest(getResp.Path)
			out = append(out, &sa)
		}
	}
	return out
}

// listAllAuthFiles 列出所有有效账号（合并宿主与本地目录，按 AuthID 去重）。
//
// 去重键是账号标识而不是文件名：宿主同一物理文件可能以两种 ID 形态注册
// （带 .json 的路径形态与插件回传的裸标识），不去重会让面板出现两条相同账号。
// [参数] 无。
// [返回] 账号列表与错误。
// 最近修改时间：2026-10-18；改动原因：新增，账号来源合并。
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
		if entry.IsDir() || !isCozeAuthFileName(entry.Name()) {
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
				sa.LoadedDigest = fileDigest(fullPath)
				seen[sa.AuthID] = &sa
				list = append(list, &sa)
			}
		}
	}
	authFilesMu.RUnlock()
	return list, nil
}

// dedupeAuthByID 按账号标识去重，保留首次出现的条目。
// [参数] accounts: 原始账号列表，允许含 nil。
// [返回] 去重后的列表，顺序按首次出现。
// 最近修改时间：2026-10-18；改动原因：新增，重复注册收敛。
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

// maskIdentifier 对账号标识做安全脱敏，供展示与默认备注名使用。
// [参数] id: 账号标识。
// [返回] 脱敏后的字符串。
// 最近修改时间：2026-10-18；改动原因：新增，展示层脱敏。
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
// 最近修改时间：2026-10-18；改动原因：新增，账号删除入口。
func deleteAuthFileDirect(authID string) error {
	accounts, _ := listAllAuthFiles()
	dir := getAccountsDir()

	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	authID = strings.TrimSpace(authID)
	if !strings.HasPrefix(authID, cozeAuthFilePrefix) {
		authID = cozeAuthFilePrefix + authID
	}
	fileName := authID + ".json"
	if !isSafeCozeAuthPath(fileName) {
		return fmt.Errorf("不安全的账号标识：%s", authID)
	}

	var removed bool
	for _, acc := range accounts {
		if acc.AuthID == authID && acc.PhysicalPath != "" && isSafeCozeAuthPath(acc.PhysicalPath) {
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
