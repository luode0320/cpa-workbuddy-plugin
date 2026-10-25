package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestAuthDataForLogin_CarriesDisableState 覆盖禁用状态随 Metadata 回传的契约。
//
// 为什么必须有这条用例：宿主在每次请求结束后都用插件 auth.parse 给出的
// Metadata 重建物理 auth 文件（白名单语义，未列出的顶层键一律丢弃）。若
// disabled 与三个低额禁用归属标记不回传，宿主一次 persist 就会把「插件刚写的
// 禁用」连归属一起抹掉，巡检再也认不出这个号，账号提前回到路由。
//
// 变异验证：把 authDataForLogin 里新增的四个键任意删掉一个，本用例立即失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，防宿主 persist 抹掉禁用状态。
func TestAuthDataForLogin_CarriesDisableState(t *testing.T) {
	sa := &StoredAuth{
		AuthID:                "coze-d1",
		Label:                 "测试账号",
		Disabled:              true,
		LowCreditDisable:      true,
		LowCreditDisableBelow: 100,
		LowCreditDisableAt:    "2026-10-25T00:00:00Z",
	}
	data := authDataForLogin(sa)

	cases := []struct {
		key  string
		want any
	}{
		{"disabled", true},
		{"low_credit_disable", true},
		{"low_credit_disable_below", int64(100)},
		{"low_credit_disable_at", "2026-10-25T00:00:00Z"},
	}
	for _, tc := range cases {
		got, ok := data.Metadata[tc.key]
		if !ok {
			t.Fatalf("metadata 必须带 %s，否则宿主 persist 会抹掉禁用状态", tc.key)
		}
		if got != tc.want {
			t.Fatalf("metadata[%s] = %v (%T), want %v (%T)", tc.key, got, got, tc.want, tc.want)
		}
	}
}

// TestAuthDataForLogin_HealthyAccountNotDisabled 覆盖反向边界：健康账号回传的
// disabled 必须是 false，不能因为「键存在」就让宿主把所有账号都判成禁用。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，锁死禁用判据的正负两侧。
func TestAuthDataForLogin_HealthyAccountNotDisabled(t *testing.T) {
	sa := &StoredAuth{AuthID: "coze-h1", Label: "健康账号"}
	data := authDataForLogin(sa)

	if disabled, _ := data.Metadata["disabled"].(bool); disabled {
		t.Fatal("健康账号回传 disabled 必须为 false")
	}
	if flag, _ := data.Metadata["low_credit_disable"].(bool); flag {
		t.Fatal("健康账号回传 low_credit_disable 必须为 false")
	}
	if data.Disabled {
		t.Fatal("健康账号回传顶层 Disabled 必须为 false，否则宿主会把所有账号停用")
	}
}

// TestAuthDataForLogin_CarriesDisabledField 覆盖宿主状态判定的唯一来源。
//
// 为什么必须有这条用例：宿主的 pluginAuthDataToCoreAuth 只用 AuthData 顶层
// Disabled 推导 Status，Metadata 里的 "disabled" 在该路径上不参与判定。只回传
// Metadata 会让插件侧的写盘禁用在宿主侧继续算 active——认证文件页按启用统计、
// 按「停用」筛选查不到，宿主调度器也继续把它当候选（2026-10-25 生产实证）。
//
// 变异验证：删掉 authDataForLogin 里的 Disabled 赋值行，本用例立即失败。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，锁死宿主状态判定字段。
func TestAuthDataForLogin_CarriesDisabledField(t *testing.T) {
	disabledAccount := &StoredAuth{
		AuthID:                "coze-d2",
		Label:                 "低额禁用账号",
		Disabled:              true,
		LowCreditDisable:      true,
		LowCreditDisableBelow: 200,
		LowCreditDisableAt:    "2026-10-25T14:16:13+08:00",
	}
	if data := authDataForLogin(disabledAccount); !data.Disabled {
		t.Fatal("禁用账号必须回传顶层 Disabled=true，否则宿主仍按 active 统计与调度")
	}
}

// TestAuthParse_RoundTripsDisableState 覆盖端到端闭环：宿主 persist 重建后的
// 文件（Metadata 白名单形态）被 auth.parse 重新认领时，禁用状态必须仍然完整，
// 且能再次序列化回同样内容——这是「抹掉 → 认领 → 再抹掉」不会发生的前提。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，锁死 parse 侧的回传闭环。
func TestAuthParse_RoundTripsDisableState(t *testing.T) {
	orig := &StoredAuth{
		AuthID:                "coze-p1",
		Label:                 "往返账号",
		CookieHeader:          "session=abc",
		Disabled:              true,
		LowCreditDisable:      true,
		LowCreditDisableBelow: 250,
		LowCreditDisableAt:    "2026-10-24T12:00:00Z",
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName,
		FileName: "coze-p1.json",
		RawJSON:  raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	respRaw, err := handleParseAuth(body)
	if err != nil {
		t.Fatalf("handleParseAuth: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(respRaw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatal("envelope not ok")
	}
	var resp struct {
		Handled bool `json:"handled"`
		Auth    struct {
			Disabled bool           `json:"disabled"`
			Metadata map[string]any `json:"metadata"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !resp.Handled {
		t.Fatal("coze 文件必须被认领")
	}
	// 顶层字段是宿主判定 Status 的唯一来源，缺失即等于「禁用对宿主不可见」。
	if !resp.Auth.Disabled {
		t.Fatal("parse 回传的顶层 disabled 丢了，宿主会把该号算成 active 并继续调度")
	}
	if disabled, _ := resp.Auth.Metadata["disabled"].(bool); !disabled {
		t.Fatal("parse 回传的 metadata 丢了 disabled，宿主 persist 会解禁该号")
	}
	if flag, _ := resp.Auth.Metadata["low_credit_disable"].(bool); !flag {
		t.Fatal("parse 回传的 metadata 丢了 low_credit_disable，巡检将失去归属判据")
	}
}
