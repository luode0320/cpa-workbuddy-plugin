// failover_disable.go 把「账号上游失败」直接落成宿主认识的禁用状态：失败即写盘
// `disabled: true`，宿主文件 watcher 感知后停止把新请求路由到该账号；插件每 10
// 分钟串行遍历被本机制禁用的账号，只在失败时记住的那一个模型上探活，探通才写回
// `disabled: false` 恢复流量。
//
// 为什么必须有这一层：内存冷却只存在插件里，宿主选择器看不见（它只读
// `Auth.Disabled` / `Status`）。于是同一个请求内，宿主仍会把这个已知的坏号发给
// 插件、拿一个 400 再换下一个号，生产实测 workbuddy 单请求连试 14 个账号。
// 写盘禁用走的是宿主自己的路由判据。
//
// 与其它禁用机制的边界（本文件最重要的约束）：
//   - 用户手动禁用：盘上 disabled 但无本机制标记 → 一律不接管、不解除。
//   - 额度耗尽禁用（quota_disable.go）：两者可同时存在，各自独立判定，
//     谁也不会替对方解禁。
//
// 落盘一律走「读物理文件 → 改顶层键 → 整体写回」（readAuthDoc/writeAuthDoc），
// 不重建文档：重建会丢掉结构体里没有的顶层键，包括本机制自己的标记。
//
// 最近修改时间：2026-10-25；改动原因：新增，把故障排除从插件内存升级为宿主可见。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// failoverProbeInterval 是探活调度周期。固定 10 分钟：账号规模会到 200 个量级，
	// 60 秒扫一轮会让上游每分钟承受一整轮 ping。
	failoverProbeInterval = 10 * time.Minute

	// failoverStartupWait 是启动时等待宿主装配的最长时间。
	failoverStartupWait = 15 * time.Second

	// failoverStartupPoll 是等待宿主就绪时的轮询间隔。
	failoverStartupPoll = 250 * time.Millisecond

	// failoverDisableKey 系列是本机制拥有的三个顶层键：标记表示归属，
	// model 是探活回放的目标（进程重启后盘上是唯一可靠来源），at 供诊断。
	failoverDisableKey      = "failover_disable"
	failoverDisableModelKey = "failover_disable_model"
	failoverDisableAtKey    = "failover_disable_at"
)

var (
	// 宿主注入点：生产路径使用真实实现，单测替换为内存假宿主。
	failoverNowFn        = time.Now
	failoverPingFn       = pingFailoverProbe
	failoverResolveIdxFn = resolveAuthIndexForID
	failoverGetDocFn     = readAuthDocByIndex
	failoverWriteDocFn   = writeAuthDoc
	failoverListFn       = listHostAuthEntries

	// failoverStateMu 保护下面三张内存表。
	failoverStateMu sync.Mutex
	// failoverProbeModels 记录账号最近一次失败的模型，键为归一化账号标识。
	// 仅作内存加速：禁用期间同一个账号又换了个模型失败，盘上还留着旧模型，
	// 而最新故障才是探活要验证的对象。
	failoverProbeModels = make(map[string]string)
	// failoverDisabledLocal 记录「本进程写过禁用」的账号。热路径快速判据，
	// 不是真相源——真相源是物理文件，探活候选每轮从盘上枚举。
	failoverDisabledLocal = make(map[string]struct{})
	// failoverDisableAtLocal 记录本进程写入禁用时的时刻，与上一张表同键。
	// 宿主可能用内存 Metadata 重建 auth 文件抹掉本机制的顶层键，补回时
	// at 已无法从盘上读到，只能从这里取。
	failoverDisableAtLocal = make(map[string]string)

	// failoverTickMu 是单轮探活的不重叠闸门。
	failoverTickMu sync.Mutex

	// failoverDocLocksMu 保护 failoverDocLocks 这张按物理文档分片的锁表。
	failoverDocLocksMu sync.Mutex
	// failoverDocLocks 按 auth_index 分片串行化「读盘 → 判状态 → 写盘」这段
	// 读改写。同一次失败可能有多个记账入口，它们解析到同一个物理文件；
	// 不加锁会各写一次，每写一次都触发宿主 watcher 增量重载。
	failoverDocLocks = make(map[string]*sync.Mutex)
)

// normalizeFailoverKey 把账号标识归一化为内存表键：去空白、去 .json 后缀、转小写。
// [参数] authID：账号标识或文件名。
// [返回] 归一化键；空输入返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，统一内存表键形态。
func normalizeFailoverKey(authID string) string {
	key := strings.ToLower(strings.TrimSpace(authID))
	key = strings.TrimSuffix(key, ".json")
	return key
}

// failoverDocLock 返回某个物理文档对应的互斥锁，不存在时创建。
// [参数] authIndex：宿主 auth_index。
// [返回] 该文档的锁。
// 最近修改时间：2026-10-25；改动原因：新增，串行化同一文档的读改写。
func failoverDocLock(authIndex string) *sync.Mutex {
	key := strings.TrimSpace(authIndex)
	failoverDocLocksMu.Lock()
	defer failoverDocLocksMu.Unlock()
	lock, ok := failoverDocLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		failoverDocLocks[key] = lock
	}
	return lock
}

// markFailoverDisabledLocalAt 记录账号已被本进程禁用，并记下禁用时刻。
// 同时写入归一化标识与 auth_index 两种键形态：不同调用方拿到的标识形态不同。
// [参数] authID：账号标识；authIndex：宿主 auth_index，可为空；at：禁用时刻。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，供热路径判据与标记补回共用。
func markFailoverDisabledLocalAt(authID, authIndex, at string) {
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	for _, key := range []string{normalizeFailoverKey(authID), normalizeFailoverKey(authIndex)} {
		if key == "" {
			continue
		}
		failoverDisabledLocal[key] = struct{}{}
		failoverDisableAtLocal[key] = at
	}
}

// clearFailoverDisabledLocal 清除账号的本进程禁用记忆。
// [参数] authID：账号标识；authIndex：宿主 auth_index，可为空。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，解除禁用时同步清内存。
func clearFailoverDisabledLocal(authID, authIndex string) {
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	for _, key := range []string{normalizeFailoverKey(authID), normalizeFailoverKey(authIndex)} {
		if key == "" {
			continue
		}
		delete(failoverDisabledLocal, key)
		delete(failoverDisableAtLocal, key)
	}
}

// isFailoverDisabledLocal 报告账号是否由本进程写过禁用。
// [参数] authID：账号标识。
// [返回] true 表示本进程写过。
// 最近修改时间：2026-10-25；改动原因：新增，供路由与标记补回判定。
func isFailoverDisabledLocal(authID string) bool {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return false
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	_, ok := failoverDisabledLocal[key]
	return ok
}

// failoverDisableAtFor 返回账号被本进程禁用时的时刻。
// [参数] authID：账号标识。
// [返回] RFC3339 时刻；无记录时返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，标记补回时保留原始时刻。
func failoverDisableAtFor(authID string) string {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return ""
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	return failoverDisableAtLocal[key]
}

// rememberFailoverProbeModel 记录账号最近一次失败的模型。
// [参数] authID：账号标识；model：已归一化的上游模型名。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，探活需回放最新故障模型。
func rememberFailoverProbeModel(authID, model string) {
	key := normalizeFailoverKey(authID)
	model = strings.TrimSpace(model)
	if key == "" || model == "" {
		return
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	failoverProbeModels[key] = model
}

// failoverProbeModelFor 返回账号最近一次失败的模型。
// [参数] authID：账号标识。
// [返回] 上游模型名；无记录时返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，供探活与标记补回读取。
func failoverProbeModelFor(authID string) string {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return ""
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	return failoverProbeModels[key]
}

// forgetFailoverProbeModel 清除账号的失败模型记录。
// [参数] authID：账号标识。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，恢复后清理内存。
func forgetFailoverProbeModel(authID string) {
	key := normalizeFailoverKey(authID)
	if key == "" {
		return
	}
	failoverStateMu.Lock()
	defer failoverStateMu.Unlock()
	delete(failoverProbeModels, key)
}

// listHostAuthEntries 返回宿主侧的账号文件条目（含 auth_index 与 disabled）。
// failoverProbeTargets 需要 auth_index 与宿主判定过的 disabled，而 listHostAuthFiles
// 把它们折进了 StoredAuth，因此这里单独保留一份原始条目。
// [参数] 无。
// [返回] 条目列表与错误。
// 最近修改时间：2026-10-25；改动原因：新增，探活候选需要原始 auth_index 与 disabled。
func listHostAuthEntries() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list: bad envelope")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// resolveAuthIndexForID 按账号标识反查宿主 auth_index 与物理路径。
// [参数] authID：账号标识（可带或不带 .json）。
// [返回] auth_index 与物理路径；解析失败返回空串。
// 最近修改时间：2026-10-25；改动原因：新增，写盘禁用需要 auth_index 定位物理文件。
func resolveAuthIndexForID(authID string) (string, string) {
	target := normalizeFailoverKey(authID)
	if target == "" {
		return "", ""
	}
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return "", ""
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return "", ""
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return "", ""
	}
	for _, f := range resp.Files {
		if !isZCodeAuthFileName(f.Name) {
			continue
		}
		if normalizeFailoverKey(f.Name) == target || normalizeFailoverKey(f.AuthIndex) == target ||
			normalizeFailoverKey(f.ID) == target {
			return f.AuthIndex, f.Path
		}
	}
	return "", ""
}

// readAuthDocByIndex 按 auth_index 读取物理 auth 文档。
// [参数] authIndex：宿主 auth_index。
// [返回] 顶层键值表与物理路径；读取失败返回错误。
// 最近修改时间：2026-10-25；改动原因：新增，禁用与恢复统一从这里取盘上真值。
func readAuthDocByIndex(authIndex string) (map[string]any, string, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, "", fmt.Errorf("auth_index is required")
	}
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, errCall := hostCall(pluginabi.MethodHostAuthGet, body)
	if errCall != nil {
		return nil, "", errCall
	}
	var env envelope
	if errEnv := json.Unmarshal(raw, &env); errEnv != nil || !env.OK {
		return nil, "", fmt.Errorf("host.auth.get: bad envelope")
	}
	var resp rpcHostAuthGetResponse
	if errUnmarshal := json.Unmarshal(env.Result, &resp); errUnmarshal != nil {
		return nil, "", errUnmarshal
	}
	if len(resp.JSON) == 0 {
		return nil, resp.Path, fmt.Errorf("auth doc is empty for index %s", authIndex)
	}
	var doc map[string]any
	if errDoc := json.Unmarshal(resp.JSON, &doc); errDoc != nil || doc == nil {
		return nil, resp.Path, fmt.Errorf("auth doc is unparsable for index %s", authIndex)
	}
	return doc, resp.Path, nil
}

// parseFailoverMarkers 从盘上文档读取本机制的三标记状态。
// [参数] doc：顶层键值表。
// [返回] 是否带本机制禁用标记、探活回放模型、禁用时刻。
// 最近修改时间：2026-10-25；改动原因：新增，禁用与恢复判定共用。
func parseFailoverMarkers(doc map[string]any) (bool, string, string) {
	if doc == nil {
		return false, "", ""
	}
	marked, _ := doc[failoverDisableKey].(bool)
	model, _ := doc[failoverDisableModelKey].(string)
	at, _ := doc[failoverDisableAtKey].(string)
	return marked, strings.TrimSpace(model), strings.TrimSpace(at)
}

// docDisabled 读取盘上文档的 disabled 真值。
// [参数] doc：顶层键值表。
// [返回] true 表示盘上已禁用。
// 最近修改时间：2026-10-25；改动原因：新增，禁用判定需要盘上真值。
func docDisabled(doc map[string]any) bool {
	if doc == nil {
		return false
	}
	v, _ := doc["disabled"].(bool)
	return v
}

// disableAccountForFailover 把一次账号上游失败落成宿主可见的禁用状态。
//
// 幂等与认领规则（本函数的核心约束）：
//   - 盘上已 disabled：不再写盘。带本机制标记视为幂等成功；不带标记说明是
//     用户手动禁用或额度耗尽禁用，本机制不认领、不接管其恢复，返回 false。
//   - 盘上 enabled：写入 disabled + 三个自有键，一次写盘。
//
// 无论是否写盘，都刷新内存里的失败模型，让探活回放最新故障模型。
// [参数] authID：失败账号标识；model：已归一化的失败模型名，可为空。
// [返回] true 表示该账号当前处于本机制的禁用管辖之下。
// 最近修改时间：2026-10-25；改动原因：新增，失败双写的写盘落点。
func disableAccountForFailover(authID, model string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	rememberFailoverProbeModel(authID, model)

	authIndex, _ := failoverResolveIdxFn(authID)
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		log.Printf("[zcode] failover disable skipped: no auth_index for auth=%s", authID)
		return false
	}
	// 锁必须罩住「读盘 → 判 disabled → 写盘」整段：这三步之间只要让出，第二个
	// 记账入口就会读到同一份未禁用文档并再写一次，每写一次都触发宿主 reload。
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	doc, path, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil {
		log.Printf("[zcode] failover disable skipped: no physical doc for auth=%s", authID)
		return false
	}
	marked, _, _ := parseFailoverMarkers(doc)
	if docDisabled(doc) {
		// 已被禁用：谁的禁用谁负责。只有本机制的标记才认领；
		// 手动 / 额度耗尽一律不接管，避免抢走别人的恢复职责。
		return marked
	}

	// 逐键改盘上现文档，不重建：盘上任何既有顶层键都原样保留。
	at := failoverNowFn().UTC().Format(time.RFC3339)
	doc["disabled"] = true
	doc[failoverDisableKey] = true
	doc[failoverDisableModelKey] = strings.TrimSpace(model)
	doc[failoverDisableAtKey] = at
	if path == "" {
		log.Printf("[zcode] failover disable skipped: empty path for auth=%s", authID)
		return false
	}
	if errWrite := failoverWriteDocFn(path, doc); errWrite != nil {
		log.Printf("[zcode] failover disable write failed: auth=%s err=%v", authID, errWrite)
		return false
	}
	markFailoverDisabledLocalAt(authID, authIndex, at)
	log.Printf("[zcode] failover disabled account: auth=%s model=%s", authID, strings.TrimSpace(model))
	return true
}

// reenableAccountForFailover 解除本机制写入的禁用，让账号重新接流量。
//
// 只认自己的标记：盘上没有 failover_disable 就一律不动，用户手动禁用更不碰。
// [参数] authIndex：宿主 auth_index，空串时按 authID 反查；authID：账号标识。
// [返回] true 表示已解除禁用。
// 最近修改时间：2026-10-25；改动原因：新增，探活成功与真实请求成功的恢复落点。
func reenableAccountForFailover(authIndex, authID string) bool {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	if authIndex == "" {
		resolved, _ := failoverResolveIdxFn(authID)
		authIndex = strings.TrimSpace(resolved)
	}
	if authIndex == "" {
		return false
	}
	lock := failoverDocLock(authIndex)
	lock.Lock()
	defer lock.Unlock()

	doc, path, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil || path == "" {
		return false
	}
	marked, _, _ := parseFailoverMarkers(doc)
	if !marked {
		// 不是本机制禁的号：用户手动禁用或额度耗尽禁用，必须由各自的流程解除。
		return false
	}

	delete(doc, failoverDisableKey)
	delete(doc, failoverDisableModelKey)
	delete(doc, failoverDisableAtKey)
	doc["disabled"] = false
	if errWrite := failoverWriteDocFn(path, doc); errWrite != nil {
		log.Printf("[zcode] failover re-enable write failed: auth=%s err=%v", authID, errWrite)
		return false
	}
	clearFailoverDisabledLocal(authID, authIndex)
	forgetFailoverProbeModel(authID)
	log.Printf("[zcode] failover re-enabled account: auth=%s", authID)
	return true
}

// restoreFailoverMarkersIntoDoc 在盘上文档被抹掉标记时把它们补回。
//
// 宿主可能用内存 Metadata 重建 auth 文件，把本机制的三个顶层键一起抹掉；此时
// 盘上只剩 `disabled: true`，探活候选再也认不出这个号。补回条件严格限定为
// 「本进程写过禁用」+「盘上确实 disabled」，避免替手动禁用伪造归属，
// 也避免把已经恢复的账号重新标成禁用。
// [参数] doc：盘上顶层键值表；authID：账号标识；model：探活回放模型。
// [返回] true 表示补写了标记。
// 最近修改时间：2026-10-25；改动原因：新增，宿主 persist 抹标记的保底。
func restoreFailoverMarkersIntoDoc(doc map[string]any, authID, model string) bool {
	if doc == nil || !isFailoverDisabledLocal(authID) {
		return false
	}
	if !docDisabled(doc) {
		return false
	}
	if marked, _, _ := parseFailoverMarkers(doc); marked {
		return false
	}
	at := failoverDisableAtFor(authID)
	if at == "" {
		at = failoverNowFn().UTC().Format(time.RFC3339)
	}
	doc[failoverDisableKey] = true
	doc[failoverDisableModelKey] = strings.TrimSpace(model)
	doc[failoverDisableAtKey] = at
	return true
}

// failoverProbeCandidate 描述一个待探活的禁用账号。
type failoverProbeCandidate struct {
	authID    string
	authIndex string
	model     string
}

// failoverProbeTargets 从盘上枚举「由本机制禁用」的账号作为探活候选。
//
// 候选必须从盘上枚举而不是内存表：进程重启后内存表为空，但盘上仍有一批被
// 本机制禁用的号需要恢复，只靠内存会让它们永远停在禁用状态。
// [参数] 无。
// [返回] 候选列表；宿主不可用时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，探活候选的唯一来源。
func failoverProbeTargets() []failoverProbeCandidate {
	files, errList := failoverListFn()
	if errList != nil {
		return nil
	}

	out := make([]failoverProbeCandidate, 0, len(files))
	for _, f := range files {
		authIndex := strings.TrimSpace(f.AuthIndex)
		if !f.Disabled || authIndex == "" || !isZCodeAuthFileName(f.Name) {
			continue
		}
		doc, _, errDoc := failoverGetDocFn(authIndex)
		if errDoc != nil || doc == nil {
			continue
		}
		marked, diskModel, _ := parseFailoverMarkers(doc)
		if !marked {
			continue
		}
		// 内存里的「最近失败模型」比盘上更新：禁用期间同一个账号换了个模型
		// 又失败，盘上还留着旧模型，而最新故障才是要验证的对象。
		model := failoverProbeModelFor(f.ID)
		if model == "" {
			model = failoverProbeModelFor(authIndex)
		}
		if model == "" {
			model = diskModel
		}
		out = append(out, failoverProbeCandidate{
			authID:    strings.TrimSpace(f.Name),
			authIndex: authIndex,
			model:     model,
		})
	}
	return out
}

// resolveFailoverAuth 把候选的 auth_index 解析为可用账号凭据。
// [参数] authIndex：宿主 auth_index。
// [返回] 账号凭据；解析失败返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，探活需要完整凭据。
func resolveFailoverAuth(authIndex string) *StoredAuth {
	accounts, err := listAllAuthFiles()
	if err != nil {
		return nil
	}
	for _, acc := range accounts {
		if acc.AuthID == authIndex {
			return acc
		}
	}
	return nil
}

// runFailoverProbeTick 执行一轮禁用账号探活：串行遍历，每个账号只回放当初
// 失败的那一个模型，该模型探通才解除禁用。
//
// 为什么只探失败模型：每个账号的每个模型额度彼此独立，在别的模型上探通不能
// 证明失败模型已恢复。全量轮测会让账号带着仍然坏着的模型回到路由，下一个请求
// 照样失败，还白烧了其它模型的额度。
//
// 没有失败模型可回放时跳过：无法验证就不恢复，保持禁用比盲目放回安全。
// 探活失败的账号不写任何盘、不递增任何计数，保持禁用等下一轮——禁用状态本身
// 就是「不可用」的完整表达，再写一次只会平白触发宿主 reload。
// [参数] resolve：auth_index 到凭据的解析函数（测试注入）。
// [返回] 本轮实际探测的账号数。
// 最近修改时间：2026-10-25；改动原因：新增，写盘禁用的恢复通道。
func runFailoverProbeTick(resolve func(string) *StoredAuth) int {
	targets := failoverProbeTargets()
	probed := 0
	for _, tc := range targets {
		sa := resolve(tc.authIndex)
		if sa == nil {
			continue
		}
		model := strings.TrimSpace(tc.model)
		if model == "" {
			log.Printf("[zcode] failover probe skipped: no failed model for auth=%s", tc.authIndex)
			continue
		}
		probed++
		if errPing := failoverPingFn(sa, model); errPing != nil {
			log.Printf("[zcode] failover probe failed: auth=%s model=%s err=%s",
				tc.authIndex, model, truncateRedacted(errPing.Error(), 160))
			continue
		}
		log.Printf("[zcode] failover probe ok: auth=%s model=%s", tc.authIndex, model)
		reenableAccountForFailover(tc.authIndex, tc.authID)
	}
	return probed
}

// runFailoverTickNonOverlap 在不重叠的前提下执行一轮探活。
//
// 上一轮还在探测时 TryLock 失败，本轮直接丢弃而不是排队——堆积轮次只会让同一批
// 账号被重复探测。真正的探测在子 goroutine 里跑，让 ticker 自身不阻塞。
// [参数] tick：本轮要执行的动作。
// [返回] true 表示本轮已启动，false 表示因上一轮未结束被跳过。
// 最近修改时间：2026-10-25；改动原因：新增，避免探活轮次堆积。
func runFailoverTickNonOverlap(tick func()) bool {
	if !failoverTickMu.TryLock() {
		return false
	}
	go func() {
		defer failoverTickMu.Unlock()
		tick()
	}()
	return true
}

// failoverProbeLoop 是禁用账号探活的常驻循环。
//
// init() 早于宿主装配，立即 tick 只会拿到 host API unavailable，
// 因此先等待宿主就绪再进入 ticker。
// [参数] 无。
// [返回] 无；循环永不返回。
// 最近修改时间：2026-10-25；改动原因：新增，10 分钟一轮的恢复通道。
func failoverProbeLoop() {
	deadline := time.Now().Add(failoverStartupWait)
	for !hostAPIAvailable() && time.Now().Before(deadline) {
		time.Sleep(failoverStartupPoll)
	}
	ticker := time.NewTicker(failoverProbeInterval)
	defer ticker.Stop()
	for range ticker.C {
		runFailoverTickNonOverlap(func() {
			runFailoverProbeTick(resolveFailoverAuth)
		})
	}
}

// hostAPIAvailable 报告宿主插件调用表是否已装配。
// [参数] 无。
// [返回] true 表示可以发起宿主 RPC。
// 最近修改时间：2026-10-25；改动原因：从 cooldown_probe.go 迁入，探活循环等待宿主就绪用。
func hostAPIAvailable() bool {
	hostAPIMu.RLock()
	defer hostAPIMu.RUnlock()
	return hostAPI != nil
}

// pingFailoverProbe 是禁用恢复探活的适配层：只回放失败模型，
// 成功返回 nil。与 active_ping 的边界——本函数绝不写 test_failed，
// 该标签是硬排除且永不自动过期，探活失败只应让账号继续留在禁用状态。
// [参数] sa：账号凭据；model：已归一化的上游模型名。
// [返回] nil 表示探活成功。
// 最近修改时间：2026-10-25；改动原因：新增，替代被删除的冷却前探活适配层。
func pingFailoverProbe(sa *StoredAuth, model string) error {
	ok, _, errMsg, _, _ := pingUpstreamOnceFn(sa, model)
	if ok {
		return nil
	}
	if strings.TrimSpace(errMsg) == "" {
		errMsg = "failover probe failed"
	}
	return fmt.Errorf("%s", errMsg)
}

// isFailoverDisabled 判定账号当前是否处于「由本机制写入的禁用」状态。
//
// 两级判定：先查内存镜像（热路径，绝大多数命中在这里），未命中再读盘。
// 只读盘而不看内存会在每次调度都发一次宿主 RPC；只看内存则会在进程重启后
// 把盘上仍禁用的号当成可用——两者都不可接受。
//
// 注意：它只回答「本机制是否禁了它」，用户手动禁用的号由宿主自己排除，
// 不在这里判断，避免与别人的恢复职责纠缠。
// [参数] authID：账号标识。
// [返回] true 表示该账号已被本机制禁用。
// 最近修改时间：2026-10-25；改动原因：新增，取代原内存冷却判定。
func isFailoverDisabled(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	if isFailoverDisabledLocal(authID) {
		return true
	}
	authIndex, _ := failoverResolveIdxFn(authID)
	if strings.TrimSpace(authIndex) == "" {
		return false
	}
	doc, _, err := failoverGetDocFn(authIndex)
	if err != nil || doc == nil {
		return false
	}
	marked, _, _ := parseFailoverMarkers(doc)
	return marked && docDisabled(doc)
}
