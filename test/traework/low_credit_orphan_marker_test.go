package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 积分硬禁用归属丢失（缺陷⑳，2026-11-11 生产实证）。
//
// 现场：14 个账号 remain 100~133、tier=normal，却长期 disabled=true 无人解禁。
// 根因是 disableForLowCredits 在 setHostAuthDisabled 成功时提前 return，三个归属
// 标记与 note 从未落盘；宿主重建 auth 文档只保留白名单键，标记再也补不回来，
// 于是既没人认领（ours=false）也没人解禁，账号被永久挡在候选池外。
//
// 下面三个用例分别锁死：① 标记必须落盘；② 标记丢了也要靠 note 认领并解禁；
// ③ 「积分回升启用」note 不得被误当成归属。

// fakeHostAuth 让 failover 读物理文档与写盘都落在内存里，避免真实磁盘与宿主 API。
func fakeHostAuth(t *testing.T, authIndex, authID string, doc map[string]any) *map[string]any {
	t.Helper()
	state := &doc
	oldPhys := failoverGetPhysicalFn
	failoverGetPhysicalFn = func(string) (*hostAuthPhysical, error) {
		raw, err := json.Marshal(*state)
		if err != nil {
			return nil, err
		}
		return &hostAuthPhysical{
			Name: "traework-" + authID + ".json",
			Path: "/tmp/traework-" + authID + ".json",
			JSON: raw,
		}, nil
	}
	t.Cleanup(func() { failoverGetPhysicalFn = oldPhys })

	oldPersist := failoverPersistFn
	failoverPersistFn = func(_, _ string, _ string, raw []byte) error {
		var written map[string]any
		if err := json.Unmarshal(raw, &written); err != nil {
			return err
		}
		*state = written
		return nil
	}
	t.Cleanup(func() { failoverPersistFn = oldPersist })
	return state
}

// TestLowCreditDisablePersistsMarkerBeforeHostAPI 锁死禁用必须先落盘。
//
// 宿主 API 无论成功与否，盘上都必须留下 disabled:true + 三键 + 停用 note；
// 否则标记会随宿主重建文档彻底蒸发，账号再也无法被本机制解禁。
func TestLowCreditDisablePersistsMarkerBeforeHostAPI(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	const authID = "acc-marker"
	state := fakeHostAuth(t, "idx-marker", authID, map[string]any{"disabled": false})

	cr := &traeCredits{TotalRemain: 7}
	if err := disableForLowCredits("idx-marker", authID, nil, cr, 100); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}

	if disabled, _ := (*state)["disabled"].(bool); !disabled {
		t.Fatalf("盘上 disabled 必须为 true，实际 %v", (*state)["disabled"])
	}
	if ours, _ := (*state)[creditDisableKey].(bool); !ours {
		t.Fatalf("盘上必须留下归属标记 %s，实际 %v", creditDisableKey, (*state)[creditDisableKey])
	}
	if _, ok := (*state)[creditDisableBelowKey]; !ok {
		t.Fatalf("盘上必须记录禁用线 %s", creditDisableBelowKey)
	}
	if _, ok := (*state)[creditDisableAtKey]; !ok {
		t.Fatalf("盘上必须记录禁用时刻 %s", creditDisableAtKey)
	}
	note, _ := (*state)["note"].(string)
	if !strings.HasPrefix(note, creditDisableNotePrefix) {
		t.Fatalf("note 必须是停用文案，实际 %q", note)
	}
}

// TestLowCreditReenableAdoptsNoteOnlyDoc 锁死「标记丢失但仍能解禁」。
//
// 复刻生产坏号：盘上 disabled:true、三键全无，只剩停用 note（宿主白名单保住了它）。
// 积分回升后必须靠 note 认领归属并解禁，不能因为 ours=false 就永久跳过。
func TestLowCreditReenableAdoptsNoteOnlyDoc(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(100)
	defer restoreBelow()

	const authID = "acc-note-only"
	state := fakeHostAuth(t, "idx-note", authID, map[string]any{
		"disabled": true,
		"note":     "积分不足停用 · 余0 < 禁用线100（积分回升后自动启用）",
	})

	tier := gradeCreditsForAuth("idx-note", authID, nil, &traeCredits{TotalRemain: 133})
	if tier != creditTierNormal {
		t.Fatalf("remain=133 应回正常档，实际 %s", tier)
	}
	if disabled, _ := (*state)["disabled"].(bool); disabled {
		t.Fatalf("积分回升后必须解禁，盘上仍为 disabled=true")
	}
	if _, ok := (*state)[creditDisableKey]; ok {
		t.Fatalf("解禁后不得残留归属标记 %s", creditDisableKey)
	}
	if note, _ := (*state)["note"].(string); !strings.Contains(note, "积分回升启用") {
		t.Fatalf("解禁后 note 应刷新为启用文案，实际 %q", note)
	}
}

// TestLowCreditNoteDoesNotAdoptEnabledDoc 反向守卫：
// 「积分回升启用」note 不是停用态，不得被当成归属而重新纳入解禁逻辑。
func TestLowCreditNoteDoesNotAdoptEnabledDoc(t *testing.T) {
	if lowCreditDisableFromNote([]byte(`{"note":"积分回升启用 · 余187"}`)) {
		t.Fatalf("启用文案不得被判为停用归属")
	}
	if !lowCreditDisableFromNote([]byte(`{"note":"积分不足停用 · 余0 < 禁用线100（积分回升后自动启用）"}`)) {
		t.Fatalf("停用文案必须被判为归属")
	}
	if lowCreditDisableFromNote([]byte(`{"note":"耗尽停用 · 余0 已用100"}`)) {
		t.Fatalf("lifecycle 的耗尽停用文案不归本机制管")
	}
}

// TestGradeCredits_ReleasesOrphanDisabledAccount 锁定孤儿禁用的自愈。
//
// 生产现场（2026-11-11）：41 个禁用账号里 36 个没有任何归属标记、
// failed_count / success_count / note 全部丢失（mtime 集中在 0.5.45 热重载那一刻），
// remain 却在 6~98。它们不是积分不足，是宿主重建 auth 文档把标记抹掉后，
// ours=false 让解禁分支再也进不来，可用池塌缩到 10/51。
//
// 兜底口径：盘上 disabled:true 且 manual / failover / exhausted / low_credit
// 四者皆无 → 无主，插件有权按当前余额释放。
func TestGradeCredits_ReleasesOrphanDisabledAccount(t *testing.T) {
	resetFailover(t)
	restoreBelow := setLowCreditDisableBelow(10)
	defer restoreBelow()

	const authID = "acc-orphan"
	// 生产形态：只有 disabled 与凭据字段，没有任何本机制/其它机制的标记。
	state := fakeHostAuth(t, "idx-orphan", authID, map[string]any{
		"disabled": true,
		"uid":      authID,
		"note":     "",
	})

	gradeCreditsForAuth("idx-orphan", authID, nil, &traeCredits{TotalRemain: 79})

	if disabled, _ := (*state)["disabled"].(bool); disabled {
		t.Fatalf("无主禁用账号在余额充足时必须被释放，实际仍 disabled=true；state=%v", *state)
	}
}

// TestGradeCredits_KeepsManualAndExhaustedDisabled 反向守卫：
// 有明确归属的禁用账号不得被孤儿兜底抢走。
func TestGradeCredits_KeepsManualAndExhaustedDisabled(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
	}{
		{"手动停用不得接管", map[string]any{"disabled": true, "manual_disable": true}},
		{"故障停用不得接管", map[string]any{"disabled": true, "failover_disable": true}},
		{"额度耗尽停用不得接管", map[string]any{"disabled": true, "exhausted_disable": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFailover(t)
			restoreBelow := setLowCreditDisableBelow(10)
			defer restoreBelow()

			authID := "acc-" + strings.ReplaceAll(tc.name, " ", "-")
			state := fakeHostAuth(t, "idx-"+authID, authID, tc.doc)
			gradeCreditsForAuth("idx-"+authID, authID, nil, &traeCredits{TotalRemain: 79})
			if disabled, _ := (*state)["disabled"].(bool); !disabled {
				t.Fatalf("%s：必须保持 disabled，实际被误释放；state=%v", tc.name, *state)
			}
		})
	}
}

// 保持 pluginapi 引用，避免假宿主改造后导入被判定未使用。
var _ = pluginapi.HostAuthFileEntry{}
