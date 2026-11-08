package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// fakeTraeJWT 构造一个结构合法的 Trae 访问令牌（header.payload.signature）。
// payload 只带 data.id，与真实令牌的 {"data":{"id":"..."}} 同形；签名段不参与
// 解析，因此不需要真实密钥。测试只验证「从 payload 提取身份」，不验证签名。
//
// [参数]
//   - uid: 要编码进 payload.data.id 的账号标识
//
// [返回]
//   - string: 三段式 JWT 字符串
func fakeTraeJWT(uid string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"data":{"id":"` + uid + `"},"exp":1795322470}`))
	return header + "." + payload + ".signature"
}

// TestUIDFromTraeJWT 覆盖 JWT 身份提取的正常与畸形输入。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，锁定第三方导出格式的 uid 兜底契约。
func TestUIDFromTraeJWT(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{name: "标准JWT", token: fakeTraeJWT("181896157339808"), want: "181896157339808"},
		{name: "payload补padding", token: strings.Split(fakeTraeJWT("uid-pad"), ".")[0] + "." + base64.StdEncoding.EncodeToString([]byte(`{"data":{"id":"uid-pad"}}`)) + ".sig", want: "uid-pad"},
		{name: "空令牌", token: "", want: ""},
		{name: "非JWT", token: "plain-token-value", want: ""},
		{name: "坏base64", token: "h.%%%.s", want: ""},
		{name: "无data.id", token: "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`)) + ".s", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := uidFromTraeJWT(tc.token); got != tc.want {
				t.Fatalf("uidFromTraeJWT() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseTraeAuth_ExternalExportShape 用用户实际导入的 1.json 账号形状
// （name + token + refreshToken + deviceId + machineId，无 uid/nickname）
// 验证身份兜底：uid 来自 JWT、nickname 来自 name、设备指纹保留，且文件名
// 落到 per-uid 命名而不是被面板隐藏的 legacy traework.json。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，回归「导入成功但面板空白」。
func TestParseTraeAuth_ExternalExportShape(t *testing.T) {
	const uid = "181896157339808"
	raw := []byte(`{"name":"用户06076610181","token":"` + fakeTraeJWT(uid) + `","refreshToken":"rt-1","deviceId":"3216283905996619","machineId":"206e1cff","enabled":true,"cookies":[{"name":"sessionid","value":"x"}]}`)
	a, err := parseTraeAuth(raw)
	if err != nil {
		t.Fatalf("parseTraeAuth(external export) = %v", err)
	}
	if a.UserID != uid {
		t.Fatalf("uid = %q, want %q", a.UserID, uid)
	}
	if a.Nickname != "用户06076610181" {
		t.Fatalf("nickname = %q, want 用户06076610181", a.Nickname)
	}
	if a.DeviceID != "3216283905996619" || a.MachineID != "206e1cff" {
		t.Fatalf("device fingerprint lost: deviceId=%q machineId=%q", a.DeviceID, a.MachineID)
	}
	if got, want := authFileNameFor(a), "traework-"+uid+".json"; got != want {
		t.Fatalf("authFileNameFor() = %q, want %q", got, want)
	}
}

// TestParseCredentialImport_ExternalExportAccount 走真实导入入口，验证前端
// 展开后发送的单账号对象（无 uid、无 credential）能解析出身份与 per-uid 文件名。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，锁定 /import 端到端契约。
func TestParseCredentialImport_ExternalExportAccount(t *testing.T) {
	const uid = "181896157339808"
	raw := []byte(`{"name":"用户06076610181","token":"` + fakeTraeJWT(uid) + `","refreshToken":"8hROkVzuau","deviceId":"3216283905996619","machineId":"206e1cff","enabled":true,"cookies":[{"name":"sessionid","value":"x"}]}`)
	a, err := parseCredentialImport(raw)
	if err != nil {
		t.Fatalf("parseCredentialImport(external account) = %v", err)
	}
	if a.UserID != uid || a.Nickname != "用户06076610181" {
		t.Fatalf("identity = uid:%q nickname:%q, want %q/用户06076610181", a.UserID, a.Nickname, uid)
	}
	if got, want := authFileNameFor(a), "traework-"+uid+".json"; got != want {
		t.Fatalf("authFileNameFor() = %q, want %q", got, want)
	}
}

// TestParseTraeAuth_ExplicitFieldsWin 确认显式 uid/nickname 不被兜底覆盖。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，防止兜底劫持已有身份字段。
func TestParseTraeAuth_ExplicitFieldsWin(t *testing.T) {
	raw := []byte(`{"uid":"explicit-uid","nickname":"显式昵称","name":"顶层名称","token":"` + fakeTraeJWT("jwt-uid") + `"}`)
	a, err := parseTraeAuth(raw)
	if err != nil {
		t.Fatalf("parseTraeAuth() = %v", err)
	}
	if a.UserID != "explicit-uid" {
		t.Fatalf("uid = %q, want explicit-uid", a.UserID)
	}
	if a.Nickname != "显式昵称" {
		t.Fatalf("nickname = %q, want 显式昵称", a.Nickname)
	}
}

// TestParseTraeAuth_CredentialNicknameWinsOverName 确认 credentials 解出的
// nickname 优先于顶层 name（storage.json 形状不被导出格式的 name 干扰）。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，锁定兜底优先级。
func TestParseTraeAuth_CredentialNicknameWinsOverName(t *testing.T) {
	plain := `{"token":"` + fakeTraeJWT("uid-cred") + `","userId":"uid-cred","account":{"username":"alice"}}`
	// 同时给出 credential 与顶层 name：credential 解出的 nickname 必须赢，
	// 顶层 name 只作为缺 nickname 时的兜底。
	raw, _ := json.Marshal(map[string]any{
		"credential": plain,
		"name":       "顶层名称",
	})
	a, err := parseTraeAuth(raw)
	if err != nil {
		t.Fatalf("parseTraeAuth(storage) = %v", err)
	}
	if a.Nickname != "alice" {
		t.Fatalf("nickname = %q, want alice", a.Nickname)
	}
}
