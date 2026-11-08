// legacy_auth_migrate.go 把历史上因缺 uid 而落盘的 legacy 单文件
// traework.json 迁移成 per-uid 文件（traework-<uid>.json）。
//
// 背景（2026-11-08 用户现场）：第三方导出的账号 JSON（{"accounts":[{name,
// token, refreshToken, ...}]}）不带 uid/nickname，旧版解析只拿到 token，
// UserID 为空 → authFileNameFor 回退 legacy 名 traework.json。该名称不匹配
// 面板枚举用的 traework- 前缀，于是宿主「认证文件管理」里能看到一个
// traework，而 TraeWork 面板账号列表里没有它——导入成功却面板空白。
//
// 修复分两层：parseTraeAuth 现在从 JWT 的 data.id 补 uid、从 name 补
// nickname（新导入即正确）；本文件负责把已经落错的存量文件自愈成 per-uid
// 命名，挂在 watchdog 启动序列里跑一次（与 purgeLegacyAnomalyFlags 同
// 范式），迁移后账号立刻出现在面板。幂等：迁移完成后 legacy 名不再存在。
package main

import (
	"log"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	// legacyAuthListFn 走全量列表：legacy 名不匹配 traework- 前缀，常规
	// hostAuthList 看不到它。抽成变量供单测注入假宿主。
	legacyAuthListFn = hostAuthListAll
	// legacyAuthPersistFn 负责「写 per-uid 文件 + 删 legacy」，复用已验证的
	// 原子直写通道（host.auth.save 会丢顶层未知字段，不能用于迁移）。
	legacyAuthPersistFn = persistAuthDirect
	// legacyAuthRemoveFn 仅在同 uid 新文件已存在时删除孤儿 legacy。
	legacyAuthRemoveFn = deleteAuthFileInDir
)

// legacyAuthMigrationPlan 是 legacy 迁移的决策结果。
// Migrate=false 表示跳过；否则 NewName/NewPath 给出目标位置。
type legacyAuthMigrationPlan struct {
	Migrate bool
	NewName string
	NewPath string
}

// planLegacyAuthMigration 判断一条认证记录是否应迁移成 per-uid 文件。
//
// 仅在同时满足以下条件时迁移，其余一律不动（坏数据绝不猜）：
//  1. 文件名恰为 legacy 名 traework.json（traework-<uid>.json 不迁移）；
//  2. 物理路径非空；
//  3. 文档能解析出非空 uid（修复后的 parseTraeAuth 会从 JWT 兜底）；
//  4. 目标文件名不是 legacy 名。
//
// [参数]
//   - name: 宿主侧文件名；path: 物理路径；raw: 物理文件 JSON
//
// [返回]
//   - legacyAuthMigrationPlan: 迁移决策与目标文件名/路径
//
// 最近修改时间：2026-11-08；改动原因：新增，自愈缺 uid 的 legacy 单文件。
func planLegacyAuthMigration(name, path string, raw []byte) legacyAuthMigrationPlan {
	if !isLegacyAuthName(name) || trimSpace(path) == "" || len(raw) == 0 {
		return legacyAuthMigrationPlan{}
	}
	a, err := parseTraeAuth(raw)
	if err != nil || a == nil || trimSpace(a.UserID) == "" {
		return legacyAuthMigrationPlan{}
	}
	newName := authFileNameFor(a)
	if newName == "" || isLegacyAuthName(newName) {
		return legacyAuthMigrationPlan{}
	}
	return legacyAuthMigrationPlan{
		Migrate: true,
		NewName: newName,
		NewPath: filepath.Join(filepath.Dir(path), newName),
	}
}

// legacyAuthTargetExists 判断目标文件名是否已在宿主列表中。
// 同 uid 的新文件已存在时只清孤儿 legacy，绝不覆盖已有账号文档。
//
// [参数]
//   - files: 宿主全量条目；name: 目标文件名
//
// [返回]
//   - bool: 目标文件名是否已存在（大小写不敏感）
func legacyAuthTargetExists(files []pluginapi.HostAuthFileEntry, name string) bool {
	name = strings.TrimSpace(name)
	for _, f := range files {
		if strings.EqualFold(strings.TrimSpace(f.Name), name) {
			return true
		}
	}
	return false
}

// migrateLegacyAuthFiles 遍历宿主全量认证条目，把可确定身份的 legacy 单文件
// 迁移成 per-uid 文件。挂在 watchdog 启动序列里跑一次，逐文件失败只记日志、
// 不阻断其余账号。
//
// [参数]
//   - 无
//
// [返回]
//   - 无
//
// 最近修改时间：2026-11-08；改动原因：新增，存量 legacy 单文件自愈。
func migrateLegacyAuthFiles() {
	files, err := legacyAuthListFn()
	if err != nil {
		return // 宿主未就绪：下次启动再试
	}
	for _, f := range files {
		// 先用文件名收窄，只对 legacy 名读盘：hostAuthListAll 含其它插件的
		// 认证，不能为了迁移逐个读取与本插件无关的凭据。
		if !isLegacyAuthName(f.Name) {
			continue
		}
		phys, gerr := hostAuthGetPhysicalFn(f.AuthIndex)
		if gerr != nil || phys == nil {
			continue
		}
		plan := planLegacyAuthMigration(phys.Name, phys.Path, phys.JSON)
		if !plan.Migrate {
			continue
		}
		// 同 uid 的新文件已在列表里：账号本身已正确注册，只需清掉孤儿 legacy。
		if legacyAuthTargetExists(files, plan.NewName) {
			if rerr := legacyAuthRemoveFn(phys.Path, filepath.Dir(phys.Path)); rerr != nil {
				log.Printf("[legacy-migrate] remove orphan %s failed: %v", phys.Name, rerr)
				continue
			}
			log.Printf("[legacy-migrate] removed orphan %s (account already at %s)", phys.Name, plan.NewName)
			continue
		}
		// 写 per-uid 文件并删除 legacy（persistAuthDirect 原子替换 + 清理旧名）。
		if perr := legacyAuthPersistFn(plan.NewName, plan.NewPath, phys.Path, phys.JSON); perr != nil {
			log.Printf("[legacy-migrate] migrate %s -> %s failed: %v", phys.Name, plan.NewName, perr)
			continue
		}
		log.Printf("[legacy-migrate] migrated %s -> %s", phys.Name, plan.NewName)
	}
}
