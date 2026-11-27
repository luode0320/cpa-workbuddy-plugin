// instance_guard.go 让热重载后的旧插件实例自行退出常驻循环。
//
// 宿主热重载只把旧 .so 记入 retired 列表、不调用 plugin_shutdown，旧实例的
// goroutine 会一直跑下去。qwenwork 的 4 小时 token 保活因此会在每次热重载后
// 多出一份：多个实例并发对同一账号调 deviceToken/refresh，先到者刷新并轮换
// refresh token，后到者拿旧 token 收到 401 TOKEN_EXPIRE，健康账号被误标
// session-dead。跨实例去重必须借助进程外可见的标记文件——进程内 mutex 对
// 旧实例无效（它有自己的副本）。
package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// instanceGuardOnce 保证本进程只登记一次。
	instanceGuardOnce sync.Once
	// instanceGuardID 是本实例的唯一标识。
	instanceGuardID string
)

// pluginInstancePath 返回实例所有权标记文件路径。
// [返回] 标记文件的绝对路径。
// 最近修改时间：2026-11-27；改动原因：新增，收敛热重载遗留的保活循环实例。
func pluginInstancePath() string {
	return filepath.Join(os.TempDir(), "cpa-qwenwork-provider-instance")
}

// claimPluginInstance 把本实例登记为当前活跃实例。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增。
func claimPluginInstance() {
	instanceGuardOnce.Do(func() {
		instanceGuardID = strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
		if owner, err := os.ReadFile(pluginInstancePath()); err == nil {
			if orphanOwnerMarker(string(owner)) {
				log.Printf("[qwenwork] taking over orphan instance marker (previous %s no longer alive)", strings.TrimSpace(string(owner)))
			}
		}
		_ = os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644)
	})
}

// orphanOwnerMarker 判断标记内容是否属于「已退出进程留下的孤儿标记」。
//
// 标记格式为「<纳秒36进制>-<pid>」。判定刻意保守：只有能明确解出 pid、且该
// 进程确实已不存在时才认定为孤儿；解析不出 pid 的标记按「可能是更新实例」
// 处理，宁可多停一个循环，也不让未知来源的标记失效守卫语义。
// [参数] owner：标记文件的原始内容。
// [返回] true 表示标记所有者进程已不存在，本实例应当接管。
// 最近修改时间：2026-11-27；改动原因：新增，识别短命进程留下的标记。
func orphanOwnerMarker(owner string) bool {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		// 空/损坏标记不可能代表活跃实例，直接接管。
		return true
	}
	idx := strings.LastIndex(owner, "-")
	if idx < 0 || idx == len(owner)-1 {
		return false
	}
	pid, err := strconv.Atoi(owner[idx+1:])
	if err != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		// 同一进程内的第二次登记不是孤儿。
		return false
	}
	return !processAlive(pid)
}

// pluginInstanceSuperseded 报告本实例是否已被更新实例取代。
// [返回] true 表示应停止常驻循环。
// 最近修改时间：2026-11-27；改动原因：新增。
func pluginInstanceSuperseded() bool {
	claimPluginInstance()
	raw, err := os.ReadFile(pluginInstancePath())
	if err != nil {
		// 读不到标记时不阻断循环，宁可多跑一轮也不静默停摆。
		return false
	}
	owner := strings.TrimSpace(string(raw))
	if owner == instanceGuardID {
		return false
	}
	if orphanOwnerMarker(owner) {
		// 标记来自死进程：既不是「更新的实例」，也不该压死本实例。
		// 直接接管标记，让后续判断回到正常轨道。
		_ = os.WriteFile(pluginInstancePath(), []byte(instanceGuardID), 0o644)
		return false
	}
	return true
}

// retireIfSuperseded 是常驻循环的标准实例守卫闸门：已被更新实例取代时打一条
// 带 loop 名的停止日志并返回 true，调用方据此直接 return 退出循环。
// [参数] loop：循环名，仅用于停止日志定位。
// [返回] true 表示本实例已被取代，调用方必须立即 return。
// 最近修改时间：2026-11-27；改动原因：新增，把实例守卫收敛成统一闸门。
func retireIfSuperseded(loop string) bool {
	if !pluginInstanceSuperseded() {
		return false
	}
	log.Printf("[qwenwork] %s stopped: superseded by newer instance", loop)
	return true
}
