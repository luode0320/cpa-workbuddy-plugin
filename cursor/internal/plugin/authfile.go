package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

// cursorAuthFilePrefix 是多账号认证文件的磁盘前缀（cursor-<hash8>.json）。
// 它与插件 id（cursor-provider）解耦：宿主按文件名前缀与顶层 type 识别本插件
// 的凭据，二者必须都匹配，否则面板看不到账号、路由也拿不到凭据。
const cursorAuthFilePrefix = "cursor-"

// authFileNameFor 依据账号身份（优先 account_id，其次 email）生成稳定的认证
// 文件名，与 buildAuthData 的命名规则保持一致，重复导入同一账号会落到同一文件。
func authFileNameFor(credentials cursorauth.Credentials) string {
	identity := strings.TrimSpace(credentials.AccountID)
	if identity == "" {
		identity = strings.TrimSpace(credentials.Email)
	}
	if identity == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(identity))
	return cursorAuthFilePrefix + hex.EncodeToString(digest[:8]) + ".json"
}

// isCursorAuthFileName 判断裸文件名是否属于本插件的凭据命名空间。
func isCursorAuthFileName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, cursorAuthFilePrefix)
}

// isSafeCursorAuthPath 同时校验文件名前缀与路径穿越，拒绝越界写入/删除。
func isSafeCursorAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "../") || strings.Contains(slash, "/..") {
		return false
	}
	base := filepath.Base(path)
	if !isCursorAuthFileName(base) {
		return false
	}
	if base != filepath.Base(filepath.Clean(path)) {
		return false
	}
	return true
}

// isPathUnderDir 判断 path 是否位于 dir 之内（两者均先规范化）。
func isPathUnderDir(path, dir string) bool {
	path = strings.TrimSpace(path)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return true
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return false
	}
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)+"..")
}

// buildCursorAuthFileJSON 生成 host.auth.save 所需的认证文件内容：顶层写入
// type/provider/disabled/note，并把凭据字段（access_token/refresh_token/…）
// 平铺到顶层，保证宿主重建记录与 ParseCredentials 都能读取。
func buildCursorAuthFileJSON(credentials cursorauth.Credentials, disabled bool, note string) ([]byte, error) {
	storage, err := cursorauth.MarshalCredentials(credentials)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"disabled": disabled,
		"note":     note,
	}
	for key, value := range nested {
		out[key] = value
	}
	return json.Marshal(out)
}

// writeCursorAuthFileDirect 通过「临时文件 + 重命名」原子写入物理认证文件。
// 禁用/启用标记等插件自有顶层字段必须走直写通道：host.auth.save 会重建记录并
// 丢弃未识别字段，只有直写才能让宿主文件监听器带着这些字段重新合成记录。
func writeCursorAuthFileDirect(path string, raw []byte) error {
	if !isSafeCursorAuthPath(path) {
		return fmt.Errorf("refusing direct write to unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing direct write to relative path: %s", path)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cursor-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	return nil
}

// persistCursorAuthDirect 是直写通道的高层封装（写入物理路径）。
func persistCursorAuthDirect(path string, raw []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("no physical auth path")
	}
	return writeCursorAuthFileDirect(path, raw)
}

// deleteCursorAuthFileInDir 删除物理认证文件，要求路径绝对、安全且位于 dir 内。
func deleteCursorAuthFileInDir(path, dir string) error {
	if !isSafeCursorAuthPath(path) {
		return fmt.Errorf("refusing to delete unsafe path: %s", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refusing to delete relative path: %s", path)
	}
	if dir != "" && !isPathUnderDir(path, dir) {
		return fmt.Errorf("refusing to delete path outside auth dir: %s (dir=%s)", path, dir)
	}
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// errString 把 error 转为字符串，nil 返回空串。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
