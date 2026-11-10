package main

// stale_disable_correct.go 纠正「旧世代实例按自己编译期禁用线写下的禁用」。
//
// 背景（2026-11-11 生产实证，本文件存在的唯一理由）：
// 宿主热重载只把旧 .so 记入 retired 列表，从不调用 plugin_shutdown
// （main.go 的 cliproxyPluginShutdown 是有意空实现），因此旧世代实例的
// goroutine 会一直跑到进程结束。生产实测进程内同时 mmap 着 9 代：
//   v0.5.41 / 42 / 44 / 45 / 46 / 47 / 48 / 50 / 51
// 其中 v0.5.41/42/44 的 watchdog 里**没有** retireIfSuperseded 闸门
// （strings 实测：这三个 .so 没有 "watchdog loop" 文案，v0.5.45 起才出现），
// 它们每 10 分钟用自己编译期的禁用线常量 100 重写
// 「disabled:true + low_credit_disable_below:100」，
// 把 remain=12~92 的健康号逐个禁掉，可用池被反复削到 2/51。
//
// 为什么不能只靠实例守卫：守卫只能让**新**实例停下来，管不住已经跑起来的
// 旧实例。禁用也不能靠调大刷新频率硬碰——旧代与新代同周期（10 分钟），
// 谁后写谁赢，账号池持续震荡。
//
// 本文件用「阈值指纹」识别旧代写入：盘上 low_credit_disable_below 记录的
// 值远大于当前生效线时，这条禁用不可能是本代写的（本代只写
// effectiveLowCreditDisableBelow()），据此无条件释放。
//
// 最近修改时间：2026-11-11；改动原因：新增，跨代阈值错写的唯一可自愈通道。

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

const (
	// staleDisableCorrectInterval 是纠正循环周期。
	//
	// 必须显著小于旧代的 10 分钟 watchdog 周期：旧代刚写完就尽快纠正回去，
	// 把账号「被禁用」的暴露窗口从最长 10 分钟压到 3 分钟以内。
	// 取 3 分钟而不是更短，是为了避免与刷新队列抢上游积分 API 配额。
	staleDisableCorrectInterval = 3 * time.Minute

	// staleDisableCorrectFirstDelay 是首轮延迟：等宿主把 auth 列表接好，
	// 且错开其它插件的启动抖动。
	staleDisableCorrectFirstDelay = 45 * time.Second
)

// staleDisableBelowRatio 判定「盘上阈值是否来自旧世代」的宽松倍数。
//
// 不用等值比较：旧代可能缓存了任意一代的常量（100、50…），只要明显高于
// 当前生效线就说明不是本代写的。取 1 倍是为了排除「当前线恰好被运维调高」
// 的误判——那种情况下本代自己就会写同样的值。
func staleDisableBelowRatio(stored, current int64) bool {
	if current <= 0 || stored <= 0 {
		return false
	}
	return stored > current
}

// correctStaleDisable 检查单个账号是否被旧世代按过期禁用线误禁，是则释放。
//
// 释放条件必须同时满足（缺一不可，任何一条不成立都不动它）：
//  1. 盘上 disabled 为 true；
//  2. 带本机制标记 low_credit_disable（不是 manual / failover / exhausted）；
//  3. 盘上记录的 low_credit_disable_below 大于当前生效线（证明不是本代写的）；
//  4. 当前余额 >= 生效禁用线（真的不该被禁）；
//  5. 无 manual_disable / failover_disable / exhausted_disable。
//
// [参数] authIndex：宿主 auth_index；authID：账号标识。
// [返回] true 表示本次做过释放。
// 最近修改时间：2026-11-11；改动原因：新增，纠正旧世代跨代写入。
func correctStaleDisable(authIndex, authID string) bool {
	if !failoverActive() {
		return false
	}
	current := effectiveLowCreditDisableBelow()
	if current <= 0 {
		// 禁用线关闭时不主动解禁：关闭语义是「不再新增禁用」，
		// 历史禁用由正常分级路径按刷新结果处理。
		return false
	}
	phys, err := failoverGetPhysicalFn(strings.TrimSpace(authIndex))
	if err != nil || phys == nil || len(phys.JSON) == 0 {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(phys.JSON, &doc); err != nil || doc == nil {
		return false
	}
	// 条件 1：必须处于禁用态。
	if disabled, _ := doc["disabled"].(bool); !disabled {
		return false
	}
	// 条件 5：别的机制拥有的号一律不碰。
	for _, k := range []string{"manual_disable", "failover_disable", "exhausted_disable"} {
		if v, _ := doc[k].(bool); v {
			return false
		}
	}
	// 条件 2：必须是本机制禁的（否则无从判断阈值归属）。
	if v, _ := doc[creditDisableKey].(bool); !v {
		return false
	}
	// 条件 3：盘上阈值必须明显高于当前生效线。
	stored := int64(0)
	switch v := doc[creditDisableBelowKey].(type) {
	case float64:
		stored = int64(v)
	case int64:
		stored = v
	case int:
		stored = int64(v)
	}
	if !staleDisableBelowRatio(stored, current) {
		return false
	}
	// 条件 4：当前余额必须已经不低于生效线。读缓存快照，不在这里打上游。
	cr, ok := cachedCredits(authID)
	if !ok || cr == nil {
		return false
	}
	if cr.TotalRemain < current {
		return false
	}
	// 全部成立：按当前生效线重新判档后用正常解禁路径释放。
	if err := reenableForLowCredits(authIndex, authID, nil, cr); err != nil {
		log.Printf("[traework] stale-disable correct failed: auth=%s stored_below=%d current=%d err=%v",
			authID, stored, current, err)
		return false
	}
	log.Printf("[traework] stale-disable corrected: auth=%s remain=%d stored_below=%d current_below=%d",
		authID, cr.TotalRemain, stored, current)
	return true
}

// correctAllStaleDisables 扫描全量账号并纠正旧世代写入的禁用。
//
// [返回] 本次释放的账号数。
// 最近修改时间：2026-11-11；改动原因：新增，供纠正循环调用。
func correctAllStaleDisables() int {
	files, err := hostAuthListAll()
	if err != nil || len(files) == 0 {
		return 0
	}
	n := 0
	for _, f := range files {
		authIndex := strings.TrimSpace(f.AuthIndex)
		if authIndex == "" {
			continue
		}
		if correctStaleDisable(authIndex, f.ID) {
			n++
		}
	}
	return n
}

// staleDisableCorrectLoop 是纠正循环的常驻入口。
//
// 带实例守卫：新版实例取代旧版后，旧版的纠正循环必须退出，否则每一代都在
// 扫全量账号，白白放大宿主 RPC 压力。
// 最近修改时间：2026-11-11；改动原因：新增。
func staleDisableCorrectLoop() {
	time.Sleep(staleDisableCorrectFirstDelay)
	if retireIfSuperseded("stale disable correct loop") {
		return
	}
	for {
		if n := correctAllStaleDisables(); n > 0 {
			log.Printf("[traework] stale-disable sweep released %d account(s)", n)
		}
		time.Sleep(staleDisableCorrectInterval)
		if retireIfSuperseded("stale disable correct loop") {
			return
		}
	}
}
