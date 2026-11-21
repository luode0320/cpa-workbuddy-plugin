package main

import (
	"path/filepath"
	"testing"
)

// 本文件覆盖续期写盘的活跃实例闸门（TASK-04）。
//
// 背景：插件热重载后旧实例仍在内存里跑着定时器，它手里是上一版的 Cookie。
// 旧实例若继续写盘，会把新实例刚续好的凭据覆盖回旧值，或把自己判出的
// 「失效」写进账号文件，让新实例刚恢复的账号重新变成待授权。
// 积分巡检早已接了这道闸门（credit_sweep.go:53），续期链路漏接。

// stubRenewCanWrite 临时替换闸门，返回还原函数。
//
// [参数] t: 测试上下文；allow: 闸门判定结果。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，闸门用例共用。
func stubRenewCanWrite(t *testing.T, allow bool) {
	t.Helper()
	prev := renewCanWriteFn
	renewCanWriteFn = func(func(string, string, map[string]any)) bool { return allow }
	t.Cleanup(func() { renewCanWriteFn = prev })
}

// TestApplyRenewal_RetiredInstanceDoesNotWrite 验证退役实例不落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，热重载覆盖防护主行为。
func TestApplyRenewal_RetiredInstanceDoesNotWrite(t *testing.T) {
	stubRenewCanWrite(t, false)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}
	before := sa.CookieHeader

	if errApply := applyRenewal(sa, &renewResult{Renewed: true, CookieHeader: "sessionid=retired"}); errApply != nil {
		t.Fatalf("退役实例放弃写盘不应报错，实际 %v", errApply)
	}
	if sa.CookieHeader != before {
		t.Fatalf("退役实例不得改内存凭据：%q → %q", before, sa.CookieHeader)
	}
	loaded, errLoad := readAuthFile(sa.PhysicalPath)
	if errLoad != nil {
		t.Fatalf("回读失败：%v", errLoad)
	}
	if loaded.CookieHeader != before {
		t.Fatalf("退役实例不得写盘：文件仍应为 %q，实际 %q", before, loaded.CookieHeader)
	}
}

// TestApplyRenewal_ActiveInstanceWrites 验证活跃实例正常落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，闸门不得误伤正常路径。
func TestApplyRenewal_ActiveInstanceWrites(t *testing.T) {
	stubRenewCanWrite(t, true)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}

	if errApply := applyRenewal(sa, &renewResult{Renewed: true, CookieHeader: "sessionid=active"}); errApply != nil {
		t.Fatalf("活跃实例写盘失败：%v", errApply)
	}
	loaded, errLoad := readAuthFile(sa.PhysicalPath)
	if errLoad != nil {
		t.Fatalf("回读失败：%v", errLoad)
	}
	if loaded.CookieHeader != "sessionid=active" {
		t.Fatalf("活跃实例应写入新凭据，实际 %q", loaded.CookieHeader)
	}
}

// TestMarkSessionInvalid_RetiredInstanceDoesNotWrite 验证退役实例不写失效标记。
//
// 旧实例判出的「失效」只代表它手里那份旧 Cookie 失效，写进文件会让
// 新实例刚续好的账号在面板上重新变成待重新授权。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，误报失效防护。
func TestMarkSessionInvalid_RetiredInstanceDoesNotWrite(t *testing.T) {
	stubRenewCanWrite(t, false)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}

	markSessionInvalid(sa, "续期失败（第一步）：会话过期，请重新登录")
	if sa.AuthInvalid {
		t.Fatal("退役实例不得在内存里标失效")
	}
	loaded, errLoad := readAuthFile(sa.PhysicalPath)
	if errLoad != nil {
		t.Fatalf("回读失败：%v", errLoad)
	}
	if loaded.AuthInvalid {
		t.Fatal("退役实例不得把失效标记写进文件")
	}
}

// TestMarkSessionInvalid_ActiveInstanceMarks 验证活跃实例正常标失效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21；改动原因：新增，闸门不得误伤正常路径。
func TestMarkSessionInvalid_ActiveInstanceMarks(t *testing.T) {
	stubRenewCanWrite(t, true)
	sa, err := newWritableAuth(t)
	if err != nil {
		t.Fatalf("准备账号失败：%v", err)
	}

	markSessionInvalid(sa, "续期失败（第一步）：会话过期，请重新登录")
	loaded, errLoad := readAuthFile(sa.PhysicalPath)
	if errLoad != nil {
		t.Fatalf("回读失败：%v", errLoad)
	}
	if !loaded.AuthInvalid {
		t.Fatal("活跃实例应把失效标记写进文件")
	}
}

// newWritableAuth 在临时目录里造一个可读写的账号文件。
//
// [参数] t: 测试上下文。
// [返回] 已加载并锚定摘要的账号；准备失败时的错误。
// 最近修改时间：2026-11-21；改动原因：新增，闸门用例的共享前置。
func newWritableAuth(t *testing.T) (*StoredAuth, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coze-auth.json")
	seed := &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       "coze-guard",
		CookieHeader: "sessionid=origin",
		PhysicalPath: path,
	}
	if err := writeAuthFileDirect(path, seed); err != nil {
		return nil, err
	}
	loaded, err := readAuthFile(path)
	if err != nil {
		return nil, err
	}
	loaded.LoadedDigest = fileDigest(path)
	return loaded, nil
}
