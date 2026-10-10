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
	zcodeAuthFilePrefix = "zcode-"
	zcodeProviderID     = "zcode-provider"
)

var (
	authFilesMu sync.RWMutex
	accountsDir string
)

// StoredAuth 代表落盘的账号结构体。
type StoredAuth struct {
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	AuthID       string   `json:"auth_id"`
	APIKey       string   `json:"api_key"`
	Label        string   `json:"label"`
	Disabled     bool     `json:"disabled"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Models       []string `json:"models,omitempty"`
	TestFailed   bool     `json:"test_failed"`
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

// setAccountsDir 设置账号存储目录（方便测试与动态配置覆盖）。
func setAccountsDir(dir string) {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()
	accountsDir = dir
}

// getAccountsDir 获取账号存储目录，默认 ~/.antigravity_cockpit/zcode_accounts/
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
	dir := filepath.Join(home, ".antigravity_cockpit", "zcode_accounts")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// authFileNameFor 根据 API Key 生成稳定的文件名（zcode-<hash8>.json）。
func authFileNameFor(apiKey string) string {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return zcodeAuthFilePrefix + hex.EncodeToString(digest[:8]) + ".json"
}

// authIDFor 根据 API Key 生成稳定的账号 AuthID。
func authIDFor(apiKey string) string {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return zcodeAuthFilePrefix + hex.EncodeToString(digest[:8])
}

// isZCodeAuthFileName 判断文件名是否属于 zcode 凭据命名空间。
func isZCodeAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, zcodeAuthFilePrefix)
}

// isSafeZCodeAuthPath 校验文件名前缀与路径穿越。
func isSafeZCodeAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "../") || strings.Contains(slash, "/..") {
		return false
	}
	base := filepath.Base(path)
	if !isZCodeAuthFileName(base) {
		return false
	}
	return base == filepath.Base(filepath.Clean(path))
}

// hostAuthSaveJSON 通过宿主 RPC 保存认证文件。
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	saveReq := pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: raw,
	}
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

// writeAuthFileDirect 原子落盘写入账号凭据 JSON 文件，并在宿主在线时同步。
func writeAuthFileDirect(filePath string, auth *StoredAuth) error {
	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	if auth.Type == "" {
		auth.Type = zcodeProviderID
	}
	if auth.Provider == "" {
		auth.Provider = zcodeProviderID
	}

	targetPath := filePath
	if auth.PhysicalPath != "" && isSafeZCodeAuthPath(auth.PhysicalPath) {
		targetPath = auth.PhysicalPath
	}

	if !isSafeZCodeAuthPath(targetPath) {
		return fmt.Errorf("unsafe auth file path: %s", targetPath)
	}

	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth failed: %w", err)
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("write tmp file failed: %w", err)
	}

	if err := os.Rename(tmpFile, targetPath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename tmp file failed: %w", err)
	}

	// 若尚未关联宿主物理路径且宿主可用，通过 host.auth.save 同步注册
	if auth.PhysicalPath == "" && !auth.Disabled {
		_ = hostAuthSaveJSON(filepath.Base(targetPath), data)
	}

	return nil
}

// readAuthFile 读取单个账号凭据文件。
func readAuthFile(filePath string) (*StoredAuth, error) {
	authFilesMu.RLock()
	defer authFilesMu.RUnlock()

	if !isSafeZCodeAuthPath(filePath) {
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

// listHostAuthFiles 从宿主 host.auth.list 获取所有 zcode 账号。
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
		if !isZCodeAuthFileName(f.Name) {
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
		if err := json.Unmarshal(getResp.JSON, &sa); err == nil && sa.APIKey != "" {
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
func listAllAuthFiles() ([]*StoredAuth, error) {
	seen := make(map[string]*StoredAuth)
	var list []*StoredAuth

	// 1. 尝试从宿主读取
	for _, sa := range listHostAuthFiles() {
		seen[sa.AuthID] = sa
		list = append(list, sa)
	}

	// 2. 读取本地目录
	dir := getAccountsDir()
	authFilesMu.RLock()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		authFilesMu.RUnlock()
		return list, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !isZCodeAuthFileName(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		data, errRead := os.ReadFile(fullPath)
		if errRead != nil {
			continue
		}
		var sa StoredAuth
		if err := json.Unmarshal(data, &sa); err == nil && sa.APIKey != "" {
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

// deleteAuthFileDirect 删除指定账号文件（同时清理宿主物理路径与本地目录）。
func deleteAuthFileDirect(authID string) error {
	accounts, _ := listAllAuthFiles()
	dir := getAccountsDir()

	authFilesMu.Lock()
	defer authFilesMu.Unlock()

	authID = strings.TrimSpace(authID)
	if !strings.HasPrefix(authID, zcodeAuthFilePrefix) {
		authID = zcodeAuthFilePrefix + authID
	}
	fileName := authID + ".json"
	if !isSafeZCodeAuthPath(fileName) {
		return fmt.Errorf("unsafe authID: %s", authID)
	}

	var removed bool
	for _, acc := range accounts {
		if acc.AuthID == authID && acc.PhysicalPath != "" && isSafeZCodeAuthPath(acc.PhysicalPath) {
			if err := os.Remove(acc.PhysicalPath); err == nil {
				removed = true
			}
		}
	}

	localPath := filepath.Join(dir, fileName)
	if err := os.Remove(localPath); err == nil {
		removed = true
	}

	if !removed {
		return os.ErrNotExist
	}
	return nil
}

// maskAPIKey 对 API Key 进行安全脱敏。
func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return "******"
	}
	return key[:4] + "...." + key[len(key)-4:]
}
