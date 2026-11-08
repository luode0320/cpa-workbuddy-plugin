package main

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// withLegacyMigrateStubs 替换 legacy 迁移的三处宿主接缝并在测试结束后恢复。
// [参数] t: 测试上下文；listFn: 全量列表桩；getFn: 物理读取桩
// [返回] 记录 persist / remove 调用的指针
// 最近修改时间：2026-11-08；改动原因：新增，让迁移编排可在无 cgo 宿主下单测。
func withLegacyMigrateStubs(t *testing.T, listFn func() ([]pluginapi.HostAuthFileEntry, error), getFn func(string) (*hostAuthPhysical, error)) (*[]string, *[]string) {
	t.Helper()
	origList, origGet := legacyAuthListFn, hostAuthGetPhysicalFn
	origPersist, origRemove := legacyAuthPersistFn, legacyAuthRemoveFn
	t.Cleanup(func() {
		legacyAuthListFn, hostAuthGetPhysicalFn = origList, origGet
		legacyAuthPersistFn, legacyAuthRemoveFn = origPersist, origRemove
	})
	var persisted, removed []string
	legacyAuthListFn = listFn
	hostAuthGetPhysicalFn = getFn
	legacyAuthPersistFn = func(name, path, legacyPath string, raw []byte) error {
		persisted = append(persisted, name+"|"+path+"|"+legacyPath)
		return nil
	}
	legacyAuthRemoveFn = func(path, dir string) error {
		removed = append(removed, path)
		return nil
	}
	return &persisted, &removed
}

// TestPlanLegacyAuthMigration 覆盖迁移决策的准入与拒绝分支。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，锁定「只迁移可确定身份的 legacy 名」。
func TestPlanLegacyAuthMigration(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "traework.json")
	valid := []byte(`{"name":"用户1","token":"` + fakeTraeJWT("uid-legacy") + `","refreshToken":"rt"}`)

	cases := []struct {
		name     string
		fileName string
		path     string
		raw      []byte
		wantMig  bool
		wantName string
	}{
		{name: "legacy且可解析uid", fileName: "traework.json", path: legacyPath, raw: valid, wantMig: true, wantName: "traework-uid-legacy.json"},
		{name: "非legacy名不迁移", fileName: "traework-uid-legacy.json", path: filepath.Join(dir, "traework-uid-legacy.json"), raw: valid, wantMig: false},
		{name: "空路径不迁移", fileName: "traework.json", path: "", raw: valid, wantMig: false},
		{name: "无token不迁移", fileName: "traework.json", path: legacyPath, raw: []byte(`{"name":"用户1"}`), wantMig: false},
		{name: "uid不可解析不迁移", fileName: "traework.json", path: legacyPath, raw: []byte(`{"name":"用户1","token":"plain-token"}`), wantMig: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planLegacyAuthMigration(tc.fileName, tc.path, tc.raw)
			if plan.Migrate != tc.wantMig {
				t.Fatalf("Migrate = %v, want %v (plan=%+v)", plan.Migrate, tc.wantMig, plan)
			}
			if tc.wantMig {
				if plan.NewName != tc.wantName || filepath.Base(plan.NewPath) != tc.wantName {
					t.Fatalf("target = %q / %q, want %q", plan.NewName, plan.NewPath, tc.wantName)
				}
			}
		})
	}
}

// TestMigrateLegacyAuthFiles_MigratesOrphan 验证 legacy 单文件被迁到 per-uid
// 命名，且 persist 同时收到旧路径用于清理。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增核心迁移用例。
func TestMigrateLegacyAuthFiles_MigratesOrphan(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "traework.json")
	raw := []byte(`{"name":"用户1","token":"` + fakeTraeJWT("uid-legacy") + `","refreshToken":"rt"}`)
	persisted, _ := withLegacyMigrateStubs(t,
		func() ([]pluginapi.HostAuthFileEntry, error) {
			return []pluginapi.HostAuthFileEntry{{Name: "traework.json", AuthIndex: "idx-legacy"}}, nil
		},
		func(idx string) (*hostAuthPhysical, error) {
			return &hostAuthPhysical{AuthIndex: idx, Name: "traework.json", Path: legacyPath, JSON: raw}, nil
		},
	)

	migrateLegacyAuthFiles()

	if len(*persisted) != 1 {
		t.Fatalf("persist calls = %d, want 1", len(*persisted))
	}
	want := "traework-uid-legacy.json|" + filepath.Join(dir, "traework-uid-legacy.json") + "|" + legacyPath
	if (*persisted)[0] != want {
		t.Fatalf("persist args = %q, want %q", (*persisted)[0], want)
	}
}

// TestMigrateLegacyAuthFiles_RemovesOrphanWhenTargetExists 验证同 uid 的新文件
// 已存在时只清 legacy 孤儿，不重复写账号。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，防止迁移覆盖已有账号文档。
func TestMigrateLegacyAuthFiles_RemovesOrphanWhenTargetExists(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "traework.json")
	raw := []byte(`{"token":"` + fakeTraeJWT("uid-dup") + `"}`)
	persisted, removed := withLegacyMigrateStubs(t,
		func() ([]pluginapi.HostAuthFileEntry, error) {
			return []pluginapi.HostAuthFileEntry{
				{Name: "traework.json", AuthIndex: "idx-legacy"},
				{Name: "traework-uid-dup.json", AuthIndex: "idx-new"},
			}, nil
		},
		func(idx string) (*hostAuthPhysical, error) {
			if idx != "idx-legacy" {
				// 目标文件条目：真实宿主按索引返回它自己的文档，桩必须同构，
				// 否则同一份 legacy 文档会被二次规划，测出假重复。
				return &hostAuthPhysical{AuthIndex: idx, Name: "traework-uid-dup.json", Path: filepath.Join(dir, "traework-uid-dup.json"), JSON: []byte(`{"token":"` + fakeTraeJWT("uid-dup") + `"}`)}, nil
			}
			return &hostAuthPhysical{AuthIndex: idx, Name: "traework.json", Path: legacyPath, JSON: raw}, nil
		},
	)

	migrateLegacyAuthFiles()

	if len(*persisted) != 0 {
		t.Fatalf("persist calls = %d, want 0 (account already exists)", len(*persisted))
	}
	if len(*removed) != 1 || (*removed)[0] != legacyPath {
		t.Fatalf("removed = %v, want [%s]", *removed, legacyPath)
	}
}

// TestMigrateLegacyAuthFiles_ListErrorNoop 验证宿主未就绪时静默跳过。
// [参数] t: 测试上下文
// [返回] 无
// 最近修改时间：2026-11-08；改动原因：新增，锁定启动早期失败不写盘。
func TestMigrateLegacyAuthFiles_ListErrorNoop(t *testing.T) {
	persisted, removed := withLegacyMigrateStubs(t,
		func() ([]pluginapi.HostAuthFileEntry, error) {
			return nil, errAuthMissing()
		},
		func(string) (*hostAuthPhysical, error) { return nil, errAuthMissing() },
	)

	migrateLegacyAuthFiles()

	if len(*persisted) != 0 || len(*removed) != 0 {
		t.Fatalf("unexpected writes on list error: persist=%v remove=%v", *persisted, *removed)
	}
}
