package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本文件实现账号级阶梯冷却。
//
// 上游账号失败（429 / 402 / 5xx / 传输错误 / 登录态失效）后进入冷却窗口，窗口内
// 调度器一律跳过该账号，让请求改落到健康账号，而不是继续往已耗尽的账号上堆失败。
//
// 冷却时长按连续失败次数阶梯递增：第 1 次 30s、第 2 次 60s……封顶 10 分钟。
// 任意一次成功即清零并立即解除冷却。状态只存内存：不写 auth 文件、不写 DB，
// 进程重启即清空——冷却只是运行期路由优化，不是账号状态。
//
// 业务 400 不计入：那说明请求本身有问题，换任何账号都会同样失败，计入只会
// 把健康账号逐个冷却掉（既有插件踩过的坑）。

const (
	// failoverCooldownBase 是阶梯基数：第 n 次连续失败冷却 n × 基数（60s 起、每次 +60s）。
	failoverCooldownBase = 60 * time.Second

	// failoverCooldownMax 是阶梯上限，连续失败再多也不超过 10 分钟。
	failoverCooldownMax = 10 * time.Minute

	// failoverCooldownLadderCap 是达到上限所需的连续失败次数（600s / 60s = 10）。
	// 先比次数再相乘，避免次数极大时的乘法溢出。
	failoverCooldownLadderCap = int(failoverCooldownMax / failoverCooldownBase)
)

var (
	// failoverMu 保护 failoverStates。
	failoverMu sync.Mutex

	// failoverStates 按账号标识记录连续失败次数与冷却截止时间。
	failoverStates = make(map[string]*authFailoverState)
)

// authFailoverState 是一个账号的冷却状态。
type authFailoverState struct {
	// Count 是连续失败次数，成功一次即清零。
	Count int
	// CooldownUntil 是冷却截止时间；零值表示未冷却。
	CooldownUntil time.Time
}

// failoverCooldownFor 返回第 count 次连续失败对应的冷却时长。
// [参数] count: 当前连续失败次数。
// [返回] 冷却时长；count <= 0 返回 0。
// 最近修改时间：2026-10-17；改动原因：新增，阶梯计算。
func failoverCooldownFor(count int) time.Duration {
	if count <= 0 {
		return 0
	}
	if count >= failoverCooldownLadderCap {
		return failoverCooldownMax
	}
	return failoverCooldownBase * time.Duration(count)
}

// isAccountFailure 判断一次失败是否应计入账号冷却。
//
// 计入：传输层失败（status 0）、5xx、429、402（额度耗尽）、401/403（登录态失效）。
// 不计入：400 等业务错误——请求本身有问题，换号无意义且会误伤健康账号。
// [参数] status: HTTP 状态码；code: 上游业务码（可为 0）。
// [返回] 应计入时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，失败分类。
func isAccountFailure(status, code int) bool {
	if status == 0 || status >= 500 {
		return true
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden,
		http.StatusPaymentRequired, http.StatusTooManyRequests:
		return true
	}
	// HTTP 200 但流内报登录失效/限流：同样按账号级故障处理。
	return code == 710012001 || code == 710022004 || code == 710022005
}

// shouldRotate 判断一次失败是否值得在同一次请求内换号。
//
// 429 与登录态失效值得立刻换号（换号即可恢复）；5xx 与传输错误只记冷却、
// 不在同请求内烧预算——它们往往是上游整体抖动，换号也大概率失败。
// [参数] status: HTTP 状态码；code: 上游业务码。
// [返回] 应换号时返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，同请求换号判定。
func shouldRotate(status, code int) bool {
	if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired {
		return true
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	return code == 710012001 || code == 710022004 || code == 710022005
}

// noteAccountFailure 递增连续失败计数并施加阶梯冷却。
// [参数] authID: 账号标识；status: HTTP 状态码；code: 上游业务码。
// [返回] true 表示已计入冷却。
// 最近修改时间：2026-10-17；改动原因：新增，失败记账。
func noteAccountFailure(authID string, status, code int) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" || !isAccountFailure(status, code) {
		return false
	}

	failoverMu.Lock()
	defer failoverMu.Unlock()

	state := failoverStates[authID]
	if state == nil {
		state = &authFailoverState{}
		failoverStates[authID] = state
	}
	state.Count++
	state.CooldownUntil = time.Now().Add(failoverCooldownFor(state.Count))
	return true
}

// isAccountCoolingDown 判断账号是否仍在冷却窗口内。
// [参数] authID: 账号标识。
// [返回] 冷却中返回 true。
// 最近修改时间：2026-10-17；改动原因：新增，路由跳过判定。
func isAccountCoolingDown(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}

	failoverMu.Lock()
	defer failoverMu.Unlock()

	state := failoverStates[authID]
	return state != nil && time.Now().Before(state.CooldownUntil)
}

// resetAccountFailover 在请求成功后清零计数并解除冷却。
// [参数] authID: 账号标识。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，成功清零。
func resetAccountFailover(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}

	failoverMu.Lock()
	defer failoverMu.Unlock()
	delete(failoverStates, authID)
}

// accountFailureCount 返回账号当前连续失败次数（供面板展示）。
// [参数] authID: 账号标识。
// [返回] 连续失败次数。
// 最近修改时间：2026-10-17；改动原因：新增，面板状态展示。
func accountFailureCount(authID string) int {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}

	failoverMu.Lock()
	defer failoverMu.Unlock()
	if state := failoverStates[authID]; state != nil {
		return state.Count
	}
	return 0
}

// resetFailoverStates 清空全部冷却状态（仅测试使用）。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-10-17；改动原因：新增，避免测试间状态串扰。
func resetFailoverStates() {
	failoverMu.Lock()
	defer failoverMu.Unlock()
	failoverStates = make(map[string]*authFailoverState)
}
