package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 本文件实现「活跃实例自检」，防止已退役的插件实例继续写盘。
//
// 为什么必须有它（2026-11-08 生产实证）：宿主热重载时只把旧实例标记为
// retired（host.go 的 retireLoadedPluginLocked），**并不会调用它的
// cliproxyPluginShutdown**——shutdown 只在 Unload / ShutdownAll 时才触发。
// 于是旧实例的巡检 goroutine 会继续存活，并按它自己那份编译期的旧阈值
// 写盘禁用账号。
//
// 实证形态：coze 0.7.4 → 0.7.5 热重载后，0.7.4 的僵尸巡检在 19:02:52
// 用旧红线 200 把 0.7.5 刚解禁的 23 个账号整批重新禁用，直接引发
// 19:02:55-19:02:58 连续 4 个 503。写盘内容里 low_credit_disable_below=200
// 是只有 0.7.4 二进制里才存在的常量，正是这次误禁的判定证据。
//
// 防御方式：写盘前向宿主问一次「当前活跃的 coze-provider 是哪个 .so」，
// 路径里的版本号不是自己就整轮放弃写盘。宿主对已退役实例的请求一律拒绝，
// 因此这条自检不会误伤真正的活跃实例。

const (
	// cpaActiveGuardTimeout 是自检请求的超时。
	//
	// 自检每轮巡检只做一次，超时预算必须远小于巡检周期（1 小时），
	// 但也不能太长：宿主短暂不可用时应尽快放弃本轮写盘，而不是卡住整轮。
	cpaActiveGuardTimeout = 5 * time.Second

	// cpaActiveGuardPathPrefix 是宿主 plugins 列表里 path 字段的插件文件名前缀。
	//
	// 路径形如 plugins/linux/amd64/coze-provider-v0.7.5.so，
	// 这里只取到版本号之前的稳定部分，避免不同平台的目录差异。
	cpaActiveGuardPathPrefix = "coze-provider-v"

	// cpaActiveGuardVersionSuffix 是版本号在 .so 文件名里的分隔后缀。
	cpaActiveGuardVersionSuffix = ".so"
)

// cpaActiveGuardFetch 是自检函数的注入点，供单测替换。
//
// 生产实现会真发一次宿主请求；单测换成假实现即可覆盖
// 「已退役」与「仍是活跃实例」两条分支，不需要宿主环境。
var cpaActiveGuardFetch = fetchHostActivePluginPath

// hostPluginEntry 是宿主 GET /v0/management/plugins 返回的单条插件信息。
//
// 只取本文件需要的字段：id 用于定位自身条目，path 用于比对版本。
type hostPluginEntry struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// fetchHostActivePluginPath 查询宿主当前活跃的 coze-provider 插件路径。
//
// [参数] 无。
// [返回] 宿主返回的 path 字段；查不到或出错时返回空串。
// 最近修改时间：2026-11-08；改动原因：新增，退役实例自检的数据来源。
func fetchHostActivePluginPath() string {
	url, key := loadedCPAManagement()
	if key == "" {
		return ""
	}
	req, errBuild := http.NewRequest(http.MethodGet, url+"/v0/management/plugins", nil)
	if errBuild != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+key)
	status, body, errDo := cpaHostHTTPDo(req)
	if errDo != nil || status < 200 || status >= 300 {
		return ""
	}
	var payload struct {
		Plugins []hostPluginEntry `json:"plugins"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ""
	}
	for _, entry := range payload.Plugins {
		if entry.ID == providerName {
			return entry.Path
		}
	}
	return ""
}

// activeInstanceIsCurrent 判断当前实例是否仍是宿主认可的活跃实例。
//
// 判据是「宿主报告的 path 里的版本号 == 自己编译期的版本号」。
// 宿主查不到自身条目时返回 true：宁可放行也不要在宿主接口异常时
// 把所有账号的禁用状态卡住不动——未知不等于已退役。
//
// [参数] 无。
// [返回] true 表示可以继续写盘。
// 最近修改时间：2026-11-08；改动原因：新增，退役实例写盘防护。
func activeInstanceIsCurrent() bool {
	hostPath := cpaActiveGuardFetch()
	if strings.TrimSpace(hostPath) == "" {
		return true
	}
	return strings.Contains(hostPath, cpaActiveGuardPathPrefix+version+cpaActiveGuardVersionSuffix)
}

// ensureActiveInstanceBeforeWrite 在写盘前做一次活跃实例自检。
//
// 返回 false 时调用方必须放弃本轮所有写盘动作（禁用、解禁、标记更新），
// 但仍然允许继续刷新积分与更新缓存——读与缓存没有副作用，
// 只有写盘才会与新实例互相覆盖。
//
// [参数] logf: 日志函数。
// [返回] true 表示当前实例仍是活跃实例，可以写盘。
// 最近修改时间：2026-11-08；改动原因：新增，热重载后旧实例不得写盘。
func ensureActiveInstanceBeforeWrite(logf func(string, string, map[string]any)) bool {
	if activeInstanceIsCurrent() {
		return true
	}
	if logf != nil {
		logf("warn", "插件实例已退役，跳过本轮写盘", map[string]any{
			"version": version,
		})
	}
	return false
}

// describeActiveGuardState 返回自检状态的诊断文本，供面板与日志使用。
//
// [参数] 无。
// [返回] 人类可读的状态描述。
// 最近修改时间：2026-11-08；改动原因：新增，诊断辅助。
func describeActiveGuardState() string {
	hostPath := cpaActiveGuardFetch()
	if strings.TrimSpace(hostPath) == "" {
		return fmt.Sprintf("未知（宿主未报告 coze-provider 路径，本实例版本 %s）", version)
	}
	if activeInstanceIsCurrent() {
		return fmt.Sprintf("活跃（宿主路径 %s）", hostPath)
	}
	return fmt.Sprintf("已退役（宿主路径 %s，本实例版本 %s）", hostPath, version)
}
