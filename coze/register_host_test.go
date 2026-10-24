package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 本文件覆盖「把已落盘账号补注册到宿主」：文件名合规、跳过禁用账号、
// 原样搬运磁盘内容。

// TestReRegisterOneToHost_RejectsUnsafeID 验证不合规账号标识被拒绝。
//
// 宿主要求文件名以 .json 结尾且落在自身命名空间；把用户可控的 auth_id
// 直接拼成文件名前必须校验，否则会写到认证目录之外。
// [参数] t: 测试上下文。
// [返回] 无。
func TestReRegisterOneToHost_RejectsUnsafeID(t *testing.T) {
	dir := t.TempDir()
	prev := getAccountsDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir(prev) })

	for _, id := range []string{"", "../../etc/passwd", "other-provider-1"} {
		sa := &StoredAuth{AuthID: id, CookieHeader: "sessionid=x"}
		if errRegister := reRegisterOneToHost(sa); errRegister == nil {
			t.Fatalf("非法账号标识 %q 必须被拒绝", id)
		}
	}
}

// TestReRegisterAccountsToHost_SkipsDisabled 验证禁用账号不推给宿主。
//
// 推上去会立刻参与路由，与「禁用」的用户意图相反；
// 这是补注册最容易踩的坑——批量操作顺手把禁用号也激活了。
// [参数] t: 测试上下文。
// [返回] 无。
func TestReRegisterAccountsToHost_SkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	prev := getAccountsDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir(prev) })

	sa := &StoredAuth{AuthID: "coze-off", CookieHeader: "sessionid=x", Disabled: true}
	payload, errMarshal := json.Marshal(sa)
	if errMarshal != nil {
		t.Fatalf("构造失败：%v", errMarshal)
	}
	if errWrite := os.WriteFile(filepath.Join(dir, "coze-off.json"), payload, 0600); errWrite != nil {
		t.Fatalf("写入失败：%v", errWrite)
	}

	results, success := reRegisterAccountsToHost()
	if success != 0 {
		t.Fatalf("禁用账号不应注册成功，实际 success=%d", success)
	}
	var found bool
	for _, item := range results {
		if item.AuthID == "coze-off" {
			found = true
			if item.OK {
				t.Fatal("禁用账号结果不应标记为成功")
			}
			if item.Error == "" {
				t.Fatal("禁用账号必须给出跳过原因")
			}
		}
	}
	if !found {
		t.Fatal("禁用账号应出现在结果里（而不是被静默丢弃）")
	}
}

// TestReadAuthFileRaw_RejectsTraversal 验证原样读取也走路径安全闸门。
// [参数] t: 测试上下文。
// [返回] 无。
func TestReadAuthFileRaw_RejectsTraversal(t *testing.T) {
	if _, errRead := readAuthFileRaw("../../etc/passwd"); errRead == nil {
		t.Fatal("路径穿越必须被拒绝")
	}
	if _, errRead := readAuthFileRaw("/tmp/not-coze.json"); errRead == nil {
		t.Fatal("非扣子命名空间的文件必须被拒绝")
	}
}
