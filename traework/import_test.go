package main

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"path/filepath"
	"strings"
	"testing"
)

// htmlEscaped 复现宿主对 management JSON 响应体做的 html.EscapeString：
// 导出备份里内层 credential 字符串的引号会变成 &#34;，还原前不是合法 JSON。
//
// [参数]
//   - s: 原始字符串
//
// [返回]
//   - string: 与宿主一致的实体转义结果
func htmlEscaped(s string) string {
	return html.EscapeString(s)
}

// fakeExportEntry 构造 /export 产出的备份条目（含内层 credential）。
//
// [参数]
//   - inner: 内层 credential 的 JSON 文本
//   - escape: 是否模拟宿主把内层字符串做 HTML 实体转义
//
// [返回]
//   - []byte: 备份条目 JSON
func fakeExportEntry(inner string, escape bool) []byte {
	val := inner
	if escape {
		val = htmlEscaped(inner)
	}
	raw, _ := json.Marshal(map[string]any{
		"name":       "traework-181896157339808.json",
		"auth_index": "faec20447e276461",
		"uid":        "181896157339808",
		"nickname":   "用户06076610181",
		"credential": val,
	})
	return raw
}

// fakeStorageFile builds a storage.json-shaped document carrying a plaintext
// credential under the icube key (mirrors the real Trae SOLO layout without
// touching a real credential).
func fakeStorageFile(icubeVal string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"telemetry.machineId":              "m1",
		"iCubeAuthInfo://icube.cloudide":   icubeVal,
		"iCubeServerData://icube.cloudide": `{"entitlementInfo":{"identityStr":"Free"}}`,
	})
	return raw
}

// TestParseCredentialImport_WholeStorageFile verifies that a whole
// storage.json file (the shape the import button reads from disk) yields a
// fully decrypted traeAuth with token/uid/nickname populated.
func TestParseCredentialImport_WholeStorageFile(t *testing.T) {
	plain := `{"token":"tok-1","userId":"uid-1","account":{"username":"alice"}}`
	a, err := parseCredentialImport(fakeStorageFile(plain))
	if err != nil {
		t.Fatalf("parseCredentialImport(whole storage.json) = %v", err)
	}
	if a.Token != "tok-1" {
		t.Fatalf("token = %q, want tok-1", a.Token)
	}
	if a.UserID != "uid-1" {
		t.Fatalf("uid = %q, want uid-1", a.UserID)
	}
	if a.Nickname != "alice" {
		t.Fatalf("nickname = %q, want alice", a.Nickname)
	}
	if !a.hasToken() {
		t.Fatal("parsed auth should carry a usable token")
	}
}

// TestParseCredentialImport_RawValue verifies the paste-style input (raw
// plaintext credential JSON) still parses directly.
func TestParseCredentialImport_RawValue(t *testing.T) {
	plain := `{"token":"tok-2","userId":"uid-2"}`
	a, err := parseCredentialImport([]byte(plain))
	if err != nil {
		t.Fatalf("parseCredentialImport(raw value) = %v", err)
	}
	if a.Token != "tok-2" || a.UserID != "uid-2" {
		t.Fatalf("unexpected auth: token=%q uid=%q", a.Token, a.UserID)
	}
}

// TestParseCredentialImport_NoIcubeKey verifies a storage.json without the
// icube credential key is rejected with a clear error.
func TestParseCredentialImport_NoIcubeKey(t *testing.T) {
	if _, err := parseCredentialImport([]byte(`{"foo":1}`)); err == nil {
		t.Fatal("expected error for storage.json without icube key")
	}
}

// TestParseCredentialImport_WholeFileWithoutCredential verifies that a
// storage.json whose icube value is not a credential string is rejected.
func TestParseCredentialImport_WholeFileWithoutCredential(t *testing.T) {
	if _, err := parseCredentialImport(fakeStorageFile("not-a-credential")); err == nil {
		t.Fatal("expected error for non-credential icube value")
	}
}

// TestParseCredentialImport_HTMLEScapedAccountObject 覆盖红框现场：导出备份条目
// 的内层 credential 是第三方导出账号对象，且引号被宿主转义成 &#34;。
// 修复前它先以「非法 JSON」失败，再被 storage.json 分支覆盖成缺 icube 键提示。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，回归 11 条导出回灌失败。
func TestParseCredentialImport_HTMLEScapedAccountObject(t *testing.T) {
	const uid = "181896157339808"
	inner := `{"name":"用户06076610181","token":"` + fakeTraeJWT(uid) + `","refreshToken":"rt-1","deviceId":"3216283905996619"}`
	a, err := parseCredentialImport(fakeExportEntry(inner, true))
	if err != nil {
		t.Fatalf("parseCredentialImport(html-escaped account object) = %v", err)
	}
	if a.UserID != uid {
		t.Fatalf("uid = %q, want %q", a.UserID, uid)
	}
	if a.Nickname != "用户06076610181" {
		t.Fatalf("nickname = %q, want 用户06076610181", a.Nickname)
	}
	if !a.hasToken() {
		t.Fatal("解析结果应带可用 token")
	}
}

// TestParseCredentialImport_WrappedCloudResponse 覆盖另一种失败形状：内层
// credential 是云 API 响应包装 {ResponseMetadata, Result:{Token,...}}，同样被
// HTML 实体转义。归一化必须先还原实体、再取 Result 层，否则拿不到 token。
// 该用例刻意不带外层 uid：身份只能来自展平后 Result.Token 的 JWT，从而证明
// 展平确实发生在解析之前。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，锁定响应包装的展平规则。
func TestParseCredentialImport_WrappedCloudResponse(t *testing.T) {
	const uid = "1835541700020409"
	inner := `{"ResponseMetadata":{"RequestId":"","TraceID":"0000"},"Result":{"Token":"` + fakeTraeJWT(uid) +
		`","RefreshToken":"rt-result","TokenExpireAt":1795322470,"BoundDeviceID":"dev-1"}}`
	raw, _ := json.Marshal(map[string]any{
		"name":       "traework-" + uid + ".json",
		"auth_index": "e2ea4d9494006d15",
		"credential": htmlEscaped(inner),
	})
	a, err := parseCredentialImport(raw)
	if err != nil {
		t.Fatalf("parseCredentialImport(wrapped cloud response) = %v", err)
	}
	if a.UserID != uid {
		t.Fatalf("uid = %q, want %q", a.UserID, uid)
	}
	if a.RefreshToken != "rt-result" {
		t.Fatalf("refreshToken = %q, want rt-result", a.RefreshToken)
	}
	if !a.hasToken() {
		t.Fatal("解析结果应带可用 token")
	}
}

// TestParseCredentialImport_OuterTokenWinsOverStaleInner 确认外层字段优先：
// 导出后 keepalive 已刷新过 token 时，回灌不能把新 token 覆盖成导出快照里的旧值。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，锁定「外层优先」的合并方向。
func TestParseCredentialImport_OuterTokenWinsOverStaleInner(t *testing.T) {
	const uid = "181896157339808"
	inner := `{"token":"stale-token","refreshToken":"stale-rt"}`
	raw, _ := json.Marshal(map[string]any{
		"name":       "traework-" + uid + ".json",
		"auth_index": "idx",
		"uid":        uid,
		"token":      fakeTraeJWT(uid),
		"credential": htmlEscaped(inner),
	})
	a, err := parseCredentialImport(raw)
	if err != nil {
		t.Fatalf("parseCredentialImport(outer token) = %v", err)
	}
	if a.Token == "stale-token" {
		t.Fatal("内层旧 token 覆盖了外层新 token")
	}
	if a.UserID != uid {
		t.Fatalf("uid = %q, want %q", a.UserID, uid)
	}
}

// TestParseCredentialImport_NestedCredentialTwoLevels 锁定导出条目的真实两层嵌套：
// 外层 credential 对象里再带一个被 HTML 实体转义的 credential 字符串。只展开一层
// 会把残留字符串当成 CredentialRaw 再走解密，重新触发同一条失败。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，回归两层嵌套展开。
func TestParseCredentialImport_NestedCredentialTwoLevels(t *testing.T) {
	const uid = "181896157339808"
	account := `{"name":"用户06076610181","token":"` + fakeTraeJWT(uid) + `","refreshToken":"rt-1"}`
	raw, _ := json.Marshal(map[string]any{
		"name":       "traework-" + uid + ".json",
		"auth_index": "idx",
		"uid":        uid,
		"nickname":   "用户06076610181",
		"credential": map[string]any{
			"credential":   htmlEscaped(account),
			"refreshToken": "rt-outer",
			"type":         providerName,
			"uid":          uid,
		},
	})
	a, err := parseCredentialImport(raw)
	if err != nil {
		t.Fatalf("parseCredentialImport(nested two levels) = %v", err)
	}
	if a.UserID != uid {
		t.Fatalf("uid = %q, want %q", a.UserID, uid)
	}
	if !a.hasToken() {
		t.Fatal("解析结果应带可用 token")
	}
}

// TestImportRoundTrip_DiskFileReReadable 验证归一化后的账号落盘再回读仍可用：
// 备份条目解析 → buildAuthFileJSON → parseTraeAuth（宿主重载路径）→ 文件名与 uid
// 一致。这是红框现象的真正闭环：导入报成功但面板空白，或反过来导入失败，
// 都必须在「落盘可回读」这一层被证明打通。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，锁定落盘—回读闭环。
func TestImportRoundTrip_DiskFileReReadable(t *testing.T) {
	const uid = "181896157339808"
	account := `{"name":"用户06076610181","token":"` + fakeTraeJWT(uid) + `","refreshToken":"rt-1"}`
	entry, _ := json.Marshal(map[string]any{
		"name":       "traework-" + uid + ".json",
		"auth_index": "idx",
		"credential": map[string]any{"credential": htmlEscaped(account), "uid": uid},
	})
	a, err := parseCredentialImport(entry)
	if err != nil {
		t.Fatalf("parseCredentialImport() = %v", err)
	}
	if got, want := authFileNameFor(a), "traework-"+uid+".json"; got != want {
		t.Fatalf("authFileNameFor() = %q, want %q（文件名不匹配前缀会让面板列不出该账号）", got, want)
	}
	raw, err := buildAuthFileJSON(a, false, "imported via panel", nil)
	if err != nil {
		t.Fatalf("buildAuthFileJSON() = %v", err)
	}
	// 宿主重载路径：AuthParse 用的就是 parseTraeAuth(物理文档)。
	back, err := parseTraeAuth(raw)
	if err != nil {
		t.Fatalf("parseTraeAuth(落盘文档) = %v", err)
	}
	if back.UserID != uid || !back.hasToken() {
		t.Fatalf("回读丢失身份: uid=%q hasToken=%v", back.UserID, back.hasToken())
	}
	if !isTraeworkAuthFileName(authFileNameFor(a)) {
		t.Fatal("落盘文件名不在本插件命名空间内")
	}
}

// TestParseCredentialImport_ExportEntryErrorIsNotMasked 确认备份条目解析失败时
// 保留真实错误，不再被 storage.json 分支替换成「缺 icube 键」——那句提示会让
// 用户去翻一个与本次导入无关的文件。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，回归误导性错误文本。
func TestParseCredentialImport_ExportEntryErrorIsNotMasked(t *testing.T) {
	raw := fakeExportEntry(`{"noToken":true}`, false)
	_, err := parseCredentialImport(raw)
	if err == nil {
		t.Fatal("expected error for export entry without token")
	}
	if strings.Contains(err.Error(), icubeCloudideKey) {
		t.Fatalf("错误被 storage.json 分支覆盖: %v", err)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("错误未指向真实原因: %v", err)
	}
}

// TestParseCredentialImport_TcBlobCredentialStaysIntact 确认内层 credential 是
// tc blob（base64 串，不是 JSON 对象）时不被展平破坏，仍走原解密路径。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-11；改动原因：新增，防止归一化误伤 blob 形状。
func TestParseCredentialImport_TcBlobCredentialStaysIntact(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("not-a-real-tc-buffer-but-still-a-string"))
	raw, _ := json.Marshal(map[string]any{"credential": blob})
	if _, err := parseCredentialImport(raw); err == nil {
		t.Fatal("expected error for undecryptable blob")
	} else if strings.Contains(err.Error(), "storage.json") {
		t.Fatalf("blob 形状被误判成 storage.json: %v", err)
	}
}
func TestStorageGlobalDir_UsesAPPDATA(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\test\AppData\Roaming`)
	got := storageGlobalDir()
	want := filepath.Join(`C:\Users\test\AppData\Roaming`, "TRAE SOLO CN", "User", "globalStorage")
	if got != want {
		t.Fatalf("storageGlobalDir() = %q, want %q", got, want)
	}
}

// TestStorageGlobalDir_FallbackHome verifies the home-dir fallback when
// APPDATA is unset.
func TestStorageGlobalDir_FallbackHome(t *testing.T) {
	t.Setenv("APPDATA", "")
	got := storageGlobalDir()
	if got == "" || got == "%APPDATA%\\TRAE SOLO CN\\User\\globalStorage" {
		t.Fatalf("storageGlobalDir() fallback looks wrong: %q", got)
	}
}
