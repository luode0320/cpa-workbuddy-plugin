package main

import (
	"crypto/sha256"
	"encoding/hex"
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

const (
	glmAuthFilePrefix = "glm-web-"
	glmProviderID     = "glm-web-provider"
)

var (
	authFilesMu sync.RWMutex
	accountsDir string
)

// StoredAuth 是落盘的智谱清言网页账号凭据。
//
// RefreshToken 是长期凭据；AccessToken 是它换取的一次性调用令牌，
// ExpiresAt 记录过期时间（Unix 秒），面板据此判断是否需要刷新。
type StoredAuth struct {
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	AuthID       string   `json:"auth_id"`
	RefreshToken string   `json:"refresh_token"`
	AccessToken  string   `json:"access_token,omitempty"`
	ExpiresAt    int64    `json:"expires_at,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	Label        string   `json:"label"`
	Disabled     bool     `json:"disabled"`
	TestFailed   bool     `json:"test_failed"`
	Models       []string `json:"models,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	PhysicalPath string   `json:"-"`
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

// setAccountsDir 设置账号存储目录。
// [参数] dir: 目标目录。
// [返回] 无。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func setAccountsDir(dir string) {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()
	accountsDir = dir
}

// getAccountsDir 获取账号存储目录，默认 ~/.antigravity_cockpit/glm_web_accounts/。
// [参数] 无。
// [返回] 目录路径。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
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
	dir := filepath.Join(home, ".antigravity_cockpit", "glm_web_accounts")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// authIDFor 根据 refresh_token 生成稳定的账号 AuthID。
// [参数] refreshToken: 刷新令牌。
// [返回] 稳定 AuthID；空令牌返回空串。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func authIDFor(refreshToken string) string {
	key := strings.TrimSpace(refreshToken)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return glmAuthFilePrefix + hex.EncodeToString(digest[:8])
}

// authFileNameFor 根据 refresh_token 生成稳定文件名。
// [参数] refreshToken: 刷新令牌。
// [返回] 文件名；空令牌返回空串。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func authFileNameFor(refreshToken string) string {
	id := authIDFor(refreshToken)
	if id == "" {
		return ""
	}
	return id + ".json"
}

// isGLMAuthFileName 判断文件名是否属于本插件命名空间。
// [参数] name: 文件名。
// [返回] 是否属于。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func isGLMAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, glmAuthFilePrefix)
}

// isSafeGLMAuthPath 校验文件名前缀与路径穿越。
// [参数] path: 目标路径。
// [返回] 路径是否安全。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func isSafeGLMAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "../") || strings.Contains(slash, "/..") {
		return false
	}
	base := filepath.Base(path)
	if !isGLMAuthFileName(base) {
		return false
	}
	return base == filepath.Base(filepath.Clean(path))
}

// readAuthDoc 把物理 auth 文件读成顶层 map，并同时返回原始字节。
// [参数] filePath: 物理文件路径。
// [返回] 顶层键值表、原始字节、错误。
// 最近修改时间：2026-11-28 新增, 写盘保留未知顶层键。
func readAuthDoc(filePath string) (map[string]any, []byte, error) {
	if !isSafeGLMAuthPath(filePath) {
		return nil, nil, fmt.Errorf("unsafe auth file path: %s", filePath)
	}
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		return nil, nil, errRead
	}
	var doc map[string]any
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		return nil, raw, fmt.Errorf("decode auth doc failed: %w", errUnmarshal)
	}
	if doc == nil {
		return nil, raw, fmt.Errorf("auth doc is empty: %s", filePath)
	}
	return doc, raw, nil
}

// writeAuthDoc 把顶层 map 整体原子写回物理 auth 文件。
// [参数] filePath: 文件路径；doc: 完整顶层键值表。
// [返回] 写入错误。
// 最近修改时间：2026-11-28 新增, 原子写盘保留未知顶层键。
func writeAuthDoc(filePath string, doc map[string]any) error {
	if !isSafeGLMAuthPath(filePath) {
		return fmt.Errorf("unsafe auth file path: %s", filePath)
	}
	if doc == nil {
		return fmt.Errorf("auth doc is nil: %s", filePath)
	}
	data, errMarshal := json.MarshalIndent(doc, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal auth doc failed: %w", errMarshal)
	}

	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	dir := filepath.Dir(filePath)
	if errMkdir := os.MkdirAll(dir, 0755); errMkdir != nil {
		return fmt.Errorf("mkdir failed: %w", errMkdir)
	}
	tmpFile := fmt.Sprintf("%s.tmp.%d", filePath, time.Now().UnixNano())
	if errWrite := os.WriteFile(tmpFile, data, 0600); errWrite != nil {
		return fmt.Errorf("write tmp file failed: %w", errWrite)
	}
	if errRename := os.Rename(tmpFile, filePath); errRename != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename tmp file failed: %w", errRename)
	}
	return nil
}

// mergeStoredAuthIntoDoc 把账号已知字段合并进盘上顶层键值表。
// [参数] doc: 盘上文档；auth: 账号凭据。
// [返回] 序列化失败时返回错误。
// 最近修改时间：2026-11-28 新增, 归并要求保留未知顶层键。
func mergeStoredAuthIntoDoc(doc map[string]any, auth *StoredAuth) error {
	if doc == nil || auth == nil {
		return fmt.Errorf("nil doc or auth")
	}
	raw, errMarshal := json.Marshal(auth)
	if errMarshal != nil {
		return fmt.Errorf("marshal auth failed: %w", errMarshal)
	}
	var fields map[string]any
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil {
		return fmt.Errorf("decode auth fields failed: %w", errUnmarshal)
	}
	for k, v := range fields {
		doc[k] = v
	}
	return nil
}

// writeAuthFileDirect 原子落盘写入账号凭据 JSON 文件。
// [参数] filePath: 目标路径；auth: 账号凭据。
// [返回] 写入错误。
// 最近修改时间：2026-11-28 新增, 落盘保留未知顶层键并同步宿主。
func writeAuthFileDirect(filePath string, auth *StoredAuth) error {
	if auth.Type == "" {
		auth.Type = glmProviderID
	}
	if auth.Provider == "" {
		auth.Provider = glmProviderID
	}
	targetPath := filePath
	if auth.PhysicalPath != "" && isSafeGLMAuthPath(auth.PhysicalPath) {
		targetPath = auth.PhysicalPath
	}
	if !isSafeGLMAuthPath(targetPath) {
		return fmt.Errorf("unsafe auth file path: %s", targetPath)
	}

	doc, _, errRead := readAuthDoc(targetPath)
	if errRead != nil {
		doc = map[string]any{}
	}
	if errMerge := mergeStoredAuthIntoDoc(doc, auth); errMerge != nil {
		return errMerge
	}
	if errWrite := writeAuthDoc(targetPath, doc); errWrite != nil {
		return errWrite
	}

	data, errMarshal := json.MarshalIndent(auth, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal auth failed: %w", errMarshal)
	}
	if auth.PhysicalPath == "" && !auth.Disabled {
		_ = hostAuthSaveJSON(filepath.Base(targetPath), data)
	}
	return nil
}

// hostAuthSaveJSON 通过宿主 RPC 保存认证文件。
// [参数] name: 文件名；raw: JSON 内容。
// [返回] 保存错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	saveReq := pluginapi.HostAuthSaveRequest{Name: name, JSON: raw}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, err := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		return fmt.Errorf("host.auth.save failed")
	}
	return nil
}

// readAuthFile 读取单个账号凭据文件。
// [参数] filePath: 文件路径。
// [返回] 账号凭据、错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func readAuthFile(filePath string) (*StoredAuth, error) {
	authFilesMu.RLock()
	defer authFilesMu.RUnlock()
	if !isSafeGLMAuthPath(filePath) {
		return nil, fmt.Errorf("unsafe auth file path: %s", filePath)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var auth StoredAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	auth.PhysicalPath = filePath
	return &auth, nil
}

// listHostAuthFiles 从宿主读取所有本插件账号。
// [参数] 无。
// [返回] 账号列表。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func listHostAuthFiles() []*StoredAuth {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil
	}

	var out []*StoredAuth
	for _, f := range resp.Files {
		if !isGLMAuthFileName(f.Name) {
			continue
		}
		getBody, _ := json.Marshal(map[string]string{"auth_index": f.AuthIndex})
		getRaw, errGet := hostCall(pluginabi.MethodHostAuthGet, getBody)
		if errGet != nil {
			continue
		}
		var getEnv envelope
		if err := json.Unmarshal(getRaw, &getEnv); err != nil || !getEnv.OK {
			continue
		}
		var getResp rpcHostAuthGetResponse
		if err := json.Unmarshal(getEnv.Result, &getResp); err != nil {
			continue
		}
		var sa StoredAuth
		if err := json.Unmarshal(getResp.JSON, &sa); err == nil && strings.TrimSpace(sa.RefreshToken) != "" {
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

// listAllAuthFiles 列出所有有效账号（合并宿主与本地目录）。
// [参数] 无。
// [返回] 账号列表、错误。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func listAllAuthFiles() ([]*StoredAuth, error) {
	seen := make(map[string]*StoredAuth)
	var list []*StoredAuth

	for _, sa := range listHostAuthFiles() {
		seen[sa.AuthID] = sa
		list = append(list, sa)
	}

	dir := getAccountsDir()
	authFilesMu.RLock()
	entries, err := os.ReadDir(dir)
	authFilesMu.RUnlock()
	if err != nil && !os.IsNotExist(err) {
		return list, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isGLMAuthFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		sa, errRead := readAuthFile(path)
		if errRead != nil || strings.TrimSpace(sa.RefreshToken) == "" {
			continue
		}
		if sa.AuthID == "" {
			sa.AuthID = strings.TrimSuffix(entry.Name(), ".json")
		}
		if _, exists := seen[sa.AuthID]; exists {
			continue
		}
		seen[sa.AuthID] = sa
		list = append(list, sa)
	}
	return list, nil
}

// findAuthByID 按 AuthID 查找账号。
// [参数] authID: 账号标识。
// [返回] 账号指针；未找到返回 nil。
// 最近修改时间：2026-11-28 新增, 智谱清言网页反代插件落盘骨架。
func findAuthByID(authID string) *StoredAuth {
	authID = strings.TrimSuffix(strings.TrimSpace(authID), ".json")
	if authID == "" {
		return nil
	}
	accounts, err := listAllAuthFiles()
	if err != nil {
		return nil
	}
	for _, sa := range accounts {
		if sa.AuthID == authID {
			return sa
		}
	}
	return nil
}

// resolveTargetAuth 按请求指定的账号解析目标，缺失时回退调度器选号。
// [参数] reqAuthID: 宿主指定账号标识；storageJSON: 宿主下发的凭据快照。
// [返回] 目标账号；无可选账号返回 nil。
// 最近修改时间：2026-11-28 新增, 账号解析与选号入口。
func resolveTargetAuth(reqAuthID string, storageJSON []byte) *StoredAuth {
	if len(storageJSON) > 0 {
		var sa StoredAuth
		if err := json.Unmarshal(storageJSON, &sa); err == nil && strings.TrimSpace(sa.RefreshToken) != "" {
			if sa.AuthID == "" {
				sa.AuthID = authIDFor(sa.RefreshToken)
			}
			if !sa.Disabled {
				return &sa
			}
		}
	}
	if sa := findAuthByID(reqAuthID); sa != nil && !sa.Disabled {
		return sa
	}
	accounts, err := listAllAuthFiles()
	if err != nil {
		return nil
	}
	for _, sa := range accounts {
		if !sa.Disabled {
			return sa
		}
	}
	return nil
}
