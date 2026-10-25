package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 面板徽标（2026-10-25）。
//
// 三类新状态必须在行 JSON 里出现，否则用户在面板上看不到「这个号为什么被跳过」：
//   - 轮换让位（rotation_cooling / rotation_until / rotation_served_m）；
//   - 积分档位（credit_tier / low_credit_disable）。
//
// 同时锁定「停用探活中」与「轮换让位」不能互相顶替：两者成因不同、恢复路径不同。

// setupPanelFixture 准备假宿主 + 隔离 sidecar + 清空的轮换/积分状态。
//
// 面板行构建同时用了两条读取链：failoverGetPhysicalFn（读物理文档，newFailoverFakeHost
// 已注入）和 panelListFn（读宿主账号列表）。后者必须一起接到同一份假宿主上，
// 否则 handleAccounts 一进来就返回 host API unavailable，行构建逻辑根本跑不到。
func setupPanelFixture(t *testing.T) *failoverFakeHost {
	t.Helper()
	clearFailoverStates()
	t.Cleanup(clearFailoverStates)
	clearAccountCacheForTest(t)
	clearRotationStates()
	t.Cleanup(clearRotationStates)
	restoreBelow := setLowCreditDisableBelow(defaultLowCreditDisableBelow)
	t.Cleanup(restoreBelow)
	h := newFailoverFakeHost(t)

	origPanelList := panelListFn
	panelListFn = func() ([]pluginapi.HostAuthFileEntry, error) {
		// 复刻 hostAuthList 的前缀过滤：生产只返回 traework-<uid>.json，
		// 若这里原样透传假宿主的裸 ID，面板行会全部被过滤掉、测不到渲染逻辑。
		files, err := failoverListFn()
		if err != nil {
			return nil, err
		}
		out := make([]pluginapi.HostAuthFileEntry, 0, len(files))
		for _, f := range files {
			if strings.HasPrefix(strings.ToLower(f.Name), authFilePrefix) {
				out = append(out, f)
			}
		}
		return out, nil
	}
	t.Cleanup(func() { panelListFn = origPanelList })

	// 面板行构建用 hostAuthGetBundle 读物理文档，它走 hostAuthGetPhysicalFn
	// （与 failoverGetPhysicalFn 分开的出口），必须一起接到假宿主上。
	//
	// 同时补一个占位 token：面板行会被 parseTraeAuth 解析，缺 token 的文档整行
	// 被跳过；addAccount 造的文档只有 uid/disabled，不补的话测到的是「行被跳过」
	// 而不是「字段渲染」。
	origGetPhys := hostAuthGetPhysicalFn
	hostAuthGetPhysicalFn = func(authIndex string) (*hostAuthPhysical, error) {
		phys, err := failoverGetPhysicalFn(authIndex)
		if err != nil || phys == nil || len(phys.JSON) == 0 {
			return phys, err
		}
		var doc map[string]any
		if json.Unmarshal(phys.JSON, &doc) != nil || doc == nil {
			return phys, nil
		}
		if _, ok := doc["token"]; !ok {
			doc["token"] = "panel-fixture-token"
			if raw, mErr := json.Marshal(doc); mErr == nil {
				phys.JSON = raw
			}
		}
		return phys, nil
	}
	t.Cleanup(func() { hostAuthGetPhysicalFn = origGetPhys })

	return h
}

// handleAccountsRows 调生产入口并解出 accounts 数组。
func handleAccountsRows(t *testing.T) []map[string]any {
	t.Helper()
	res := handleAccounts()
	rawRows, ok := res["accounts"]
	if !ok {
		t.Fatalf("handleAccounts 结果缺少 accounts：%+v", res)
	}
	rows, ok := rawRows.([]traeAccountView)
	if !ok {
		t.Fatalf("accounts 类型不符：%T", rawRows)
	}
	// 走一遍 JSON 往返，确保断言的正是面板实际收到的字段名（json tag）。
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal rows: %v", err)
	}
	return out
}

// findRow 按 auth_id 找行。
func findRow(rows []map[string]any, authID string) map[string]any {
	for _, r := range rows {
		if r["auth_id"] == authID {
			return r
		}
	}
	return nil
}

// TestPanel_RotationBadgeFields 锁定轮换三字段进入行 JSON。
func TestPanel_RotationBadgeFields(t *testing.T) {
	h := setupPanelFixture(t)
	const authID = "panel-rot"
	h.addAccount(authID, "idx-rot", nil)
	cacheCredits(authID, &traeCredits{TotalRemain: 500})

	// 让该账号进入轮换冷却。
	now := time.Now()
	markAuthUsed(authID, now)
	if !evaluateRotation(authID, []string{authID, "panel-other"}, now.Add(61*time.Minute)) {
		t.Fatal("precondition: 应已进入轮换冷却")
	}

	row := findRow(handleAccountsRows(t), authID)
	if row == nil {
		t.Fatalf("账号 %s 必须出现在面板行里", authID)
	}
	if row["rotation_cooling"] != true {
		t.Fatalf("轮换冷却中的账号必须带 rotation_cooling=true，got %v", row["rotation_cooling"])
	}
	if v, _ := row["rotation_until"].(float64); v <= 0 {
		t.Fatalf("必须给出 rotation_until 供倒计时，got %v", row["rotation_until"])
	}
	// 冷却中承流时长按定义返回 0（不在计时），字段可省略。
}

// TestPanel_RotationServedMinutesVisible 锁定「承流 Nm」可见。
func TestPanel_RotationServedMinutesVisible(t *testing.T) {
	h := setupPanelFixture(t)
	const authID = "panel-served"
	h.addAccount(authID, "idx-served", nil)
	cacheCredits(authID, &traeCredits{TotalRemain: 500})

	// 连续使用 5 分钟后仍在承流（未达让位阈值）。
	base := time.Now().Add(-5 * time.Minute)
	markAuthUsed(authID, base)
	markAuthUsed(authID, time.Now())

	row := findRow(handleAccountsRows(t), authID)
	if row == nil {
		t.Fatalf("账号 %s 必须出现在面板行里", authID)
	}
	if row["rotation_cooling"] == true {
		t.Fatal("未达阈值时不应显示轮换让位")
	}
	if v, _ := row["rotation_served_m"].(float64); v < 4 {
		t.Fatalf("承流时长应约为 5 分钟，got %v", row["rotation_served_m"])
	}
}

// TestPanel_CreditTierFields 锁定积分档位与「积分停用」徽标的判据。
func TestPanel_CreditTierFields(t *testing.T) {
	h := setupPanelFixture(t)
	// 分开配置两条线，才能同时覆盖三档：
	//   保活线 20、禁用线 100 → 500=normal，50=preserve，10=disabled。
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()
	restoreMin := setMinRoutableCredits(20)
	defer restoreMin()

	const (
		normalID   = "panel-tier-normal"
		preserveID = "panel-tier-preserve"
		disabledID = "panel-tier-disabled"
	)
	h.addAccount(normalID, "idx-normal", nil)
	h.addAccount(preserveID, "idx-preserve", nil)
	h.addAccount(disabledID, "idx-disabled", nil)
	cacheCredits(normalID, &traeCredits{TotalRemain: 500})
	cacheCredits(preserveID, &traeCredits{TotalRemain: 50})
	cacheCredits(disabledID, &traeCredits{TotalRemain: 10})

	// 三档分别验证 creditTierFor 的落点（500 已高于禁用线 → normal；
	// 50 在禁用线以下 → disabled；preserve 档需要「高于禁用线但低于保活线」，
	// 用禁用线 0 的配置单独覆盖，见下一个子用例）。
	rows := handleAccountsRows(t)
	if got := findRow(rows, normalID)["credit_tier"]; got != "normal" {
		t.Fatalf("remain=500 应为 normal，got %v", got)
	}
	if got := findRow(rows, disabledID)["credit_tier"]; got != "disabled" {
		t.Fatalf("remain=10 应为 disabled 档，got %v", got)
	}

	// 保活档：关闭禁用线，remain 落在 (0, 保活线) 之间。
	// 注意方向是 remain < preserveBelow（保活线是「低于它才降级」），
	// 所以这里要把该号的积分调到保活线以下。
	setLowCreditDisableBelow(0)
	cacheCredits(preserveID, &traeCredits{TotalRemain: 10})
	rows = handleAccountsRows(t)
	if got := findRow(rows, preserveID)["credit_tier"]; got != "preserve" {
		t.Fatalf("禁用线关闭时 remain=10 应落在保活档（remain < 保活线 20），got %v", got)
	}
	// 反向守卫：remain=50 高于保活线 20 时必须回到 normal，不能一律降级。
	cacheCredits(preserveID, &traeCredits{TotalRemain: 50})
	if got := findRow(handleAccountsRows(t), preserveID)["credit_tier"]; got != "normal" {
		t.Fatalf("remain=50 高于保活线时应为 normal，got %v", got)
	}

	// 积分停用徽标只认「本机制标记 + 盘上确实 disabled」。
	setLowCreditDisableBelow(100)
	h.mu.Lock()
	h.docs["idx-disabled"]["disabled"] = true
	h.docs["idx-disabled"][creditDisableKey] = true
	h.mu.Unlock()
	disabledRow := findRow(handleAccountsRows(t), disabledID)
	if disabledRow["low_credit_disable"] != true {
		t.Fatalf("由本机制禁用的号必须带 low_credit_disable=true（否则无法与耗尽/手动区分），row=%v", disabledRow)
	}
}

// TestPanel_UnknownCreditsNoTierBadge 锁定「无积分数据不出档位徽章」。
func TestPanel_UnknownCreditsNoTierBadge(t *testing.T) {
	h := setupPanelFixture(t)
	const authID = "panel-no-credits"
	// 账号存在但没有任何积分快照，且上游查询会失败（假宿主无凭据）。
	h.addAccount(authID, "idx-nocredits", nil)

	row := findRow(handleAccountsRows(t), authID)
	if row == nil {
		t.Fatalf("账号 %s 必须出现在面板行里", authID)
	}
	if tier, ok := row["credit_tier"]; ok && tier != "" {
		t.Fatalf("没有积分数据时不得给出档位（会把「没测到」显示成「正常」），got %v", tier)
	}
}

// TestPanel_FailoverAndRotationAreSeparate 锁定两者互不顶替。
func TestPanel_FailoverAndRotationAreSeparate(t *testing.T) {
	h := setupPanelFixture(t)
	const authID = "panel-both"
	h.addAccount(authID, "idx-both", nil)
	cacheCredits(authID, &traeCredits{TotalRemain: 500})

	// 同时处于故障禁用与轮换冷却。
	failoverStateMarkDisabled(authID, "m1")
	markFailoverDisabledLocal(authID, "idx-both")
	now := time.Now()
	markAuthUsed(authID, now)
	if !evaluateRotation(authID, []string{authID, "panel-other"}, now.Add(61*time.Minute)) {
		t.Fatal("precondition: 应已进入轮换冷却")
	}

	row := findRow(handleAccountsRows(t), authID)
	if row == nil {
		t.Fatalf("账号 %s 必须出现在面板行里", authID)
	}
	if row["rotation_cooling"] != true {
		t.Fatal("轮换冷却必须独立显示，不得被故障禁用状态覆盖")
	}
	if row["failover_disabled"] != true {
		t.Fatal("故障禁用必须保留显示，不得被轮换状态覆盖")
	}
}
