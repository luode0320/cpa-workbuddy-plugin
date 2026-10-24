package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 本文件锁定「积分路由红线」契约。
//
// 扣子不声明 scheduler 能力（宿主只调用排序后第一个 scheduler 插件），
// 因此低额账号只能由执行入口拦截：返回 403 让宿主按账号级故障换号，
// 与上游 1000521「积分耗尽」走同一条已验证通道。
//
// 阈值 100 是扣子专用：单次对话消耗远高于兄弟插件的按次计费套餐，
// 沿用 20 只够一两次请求，起不到缓冲作用。

// seedAccounts 在临时账号目录写入多个账号并返回落盘结果。
//
// 与 active_ping_test.go 的 seedAccount 不同，这里必须能建出多个账号：
// 「全池兜底」用例的关键就是池里只剩一个低额号时不得拦截。
// [参数] t: 测试上下文；remaining: 每个账号的剩余积分，按顺序生成。
// [返回] 已落盘的账号列表。
func seedAccounts(t *testing.T, remaining ...int64) []*StoredAuth {
	t.Helper()
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })

	out := make([]*StoredAuth, 0, len(remaining))
	for i, value := range remaining {
		authID := "coze-r" + string(rune('a'+i))
		sa := &StoredAuth{
			Type:         providerName,
			Provider:     providerName,
			AuthID:       authID,
			CookieHeader: "sessionid=test-session; sid_tt=test-sid",
			SecUserID:    "sec-" + authID,
			Label:        "测试账号" + authID,
			CreatedAt:    "2026-10-20T00:00:00+08:00",
			UpdatedAt:    "2026-10-20T00:00:00+08:00",
		}
		if errWrite := writeAuthFileDirect(filepath.Join(dir, authID+".json"), sa); errWrite != nil {
			t.Fatalf("写入账号夹具失败：%v", errWrite)
		}
		storeCreditSummary(sa, &creditSummary{
			Total:     10000,
			Remain:    value,
			FetchedAt: time.Now().Format(time.RFC3339),
		})
		out = append(out, sa)
	}
	// 快照由积分循环在生产路径发布，测试夹具必须模拟这一步：
	// 否则闸门读到空池，会因「没有其他可用号」而一律放行，
	// 所有拦截类用例都会假通过。
	publishCreditPool(out)
	t.Cleanup(resetCreditCache)
	t.Cleanup(func() { publishCreditPool(nil) })
	return out
}

// TestMinRoutableCreditsDefaultIs100 锁定扣子的红线默认值是 100。
//
// 这个常量与兄弟插件的 20 不同，是产品判断而不是笔误，
// 用例把它钉住以防后续有人「统一口径」时误改回去。
// [参数] t: 测试上下文。
// [返回] 无。
func TestMinRoutableCreditsDefaultIs100(t *testing.T) {
	if defaultMinRoutableCredits != 100 {
		t.Fatalf("扣子积分红线默认值应为 100，实际 %d", defaultMinRoutableCredits)
	}
	if got := defaultConfig().MinRoutableCredits; got == nil || *got != 100 {
		t.Fatalf("默认配置的红线应为 100，实际 %v", got)
	}
}

// TestIsCreditsBelowRedlineBoundary 验证红线边界的精确判据。
//
// 等于红线视为可用（`<` 而不是 `<=`）：红线是「低于此值不再放行」，
// 恰好 100 分仍应承载请求。
// [参数] t: 测试上下文。
// [返回] 无。
func TestIsCreditsBelowRedlineBoundary(t *testing.T) {
	dir := t.TempDir()
	setAccountsDir(dir)
	t.Cleanup(func() { setAccountsDir("") })
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	sa := &StoredAuth{AuthID: "coze-boundary"}
	storeCreditSummary(sa, &creditSummary{Remain: 100, Total: 1000})

	if isCreditsBelowRedline(sa.AuthID) {
		t.Fatal("恰好等于红线不应判定为低额")
	}
	storeCreditSummary(sa, &creditSummary{Remain: 99, Total: 1000})
	if !isCreditsBelowRedline(sa.AuthID) {
		t.Fatal("低于红线必须判定为低额")
	}
}

// TestUnknownCreditsAreTreatedAsRoutable 验证查不到积分时放行。
//
// 积分接口抖动时若把账号判成低额，整池会在同一时刻同时不可用；
// 多打一次上游的代价远小于此，因此未知一律放行。
// 两条路径都要覆盖：判定函数本身，以及真正决定放行的闸门入口。
// [参数] t: 测试上下文。
// [返回] 无。
func TestUnknownCreditsAreTreatedAsRoutable(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	if isCreditsBelowRedline("coze-never-fetched") {
		t.Fatal("未查询过积分的账号不得被判为低额")
	}
	// 查询失败写入的摘要（Error 非空）同样视为未知。
	storeCreditSummary(&StoredAuth{AuthID: "coze-failed"}, &creditSummary{Error: "boom"})
	if isCreditsBelowRedline("coze-failed") {
		t.Fatal("积分查询失败不得被判为低额")
	}

	// 闸门入口：未知积分的号即使池里有健康号也必须放行，
	// 否则「查不到余额」会被当成「余额为 0」。
	accounts := seedAccounts(t, 5000)
	unknown := &StoredAuth{AuthID: "coze-unknown"}
	if errVerify := verifyAccountCredits(unknown); errVerify != nil {
		t.Fatalf("未知积分的账号必须放行，实际报错：%v", errVerify)
	}
	_ = accounts
}

// TestVerifyAccountCreditsSwitchesWhenPoolHasHealthy 验证池里还有达标号时拦截。
//
// 这是闸门存在意义的核心场景：低额号被宿主选中 → 直接 403 换号，
// 请求根本不打上游。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsSwitchesWhenPoolHasHealthy(t *testing.T) {
	accounts := seedAccounts(t, 30, 5000)
	low := accounts[0]

	errVerify := verifyAccountCredits(low)
	if errVerify == nil {
		t.Fatal("低额账号在有健康号可选时必须拦截")
	}
	coder, ok := errVerify.(statusCoder)
	if !ok {
		t.Fatalf("拦截错误必须实现 StatusCode，实际类型 %T", errVerify)
	}
	if coder.StatusCode() != http.StatusForbidden {
		t.Fatalf("必须返回 403 触发宿主换号，实际 %d", coder.StatusCode())
	}
}

// TestVerifyAccountCreditsAllowsWhenAllLow 验证全池都低时放行。
//
// 只剩低额号时仍要允许请求：把最后一个残血号也挡掉，
// 用户会直接看到无号可用，而实际上游可能还能跑完这一次。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsWhenAllLow(t *testing.T) {
	accounts := seedAccounts(t, 30, 50)
	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("全池低额时必须放行，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsAllowsHealthyAccount 验证达标账号直接放行。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsHealthyAccount(t *testing.T) {
	accounts := seedAccounts(t, 5000, 30)
	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("达标账号不应被拦截：%v", errVerify)
	}
}

// TestVerifyAccountCreditsIgnoresDisabledAndExpired 验证兜底不吃禁用/失效号。
//
// 「池里还有可用号」必须按真实可用性判定：禁用号、会话失效号都不该
// 被算作换号目的地，否则会把用户从低额号换到一个根本用不了的号。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsIgnoresDisabledAndExpired(t *testing.T) {
	accounts := seedAccounts(t, 30, 5000)
	// 把唯一的健康号改坏：禁用 + 会话失效。
	dir := getAccountsDir()
	healthy := accounts[1]
	healthy.Disabled = true
	healthy.AuthInvalid = true
	if errWrite := writeAuthFileDirect(filepath.Join(dir, healthy.AuthID+".json"), healthy); errWrite != nil {
		t.Fatalf("改写账号夹具失败：%v", errWrite)
	}
	// 快照必须跟着账号状态更新，否则闸门看到的仍是改坏之前的状态。
	publishCreditPool(accounts)

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("没有其他可用号时必须放行，实际报错：%v", errVerify)
	}
}

// TestCreditsBelowRedlineErrorCarries403 锁定拦截错误的状态码语义。
// [参数] t: 测试上下文。
// [返回] 无。
func TestCreditsBelowRedlineErrorCarries403(t *testing.T) {
	errCredits := &creditsBelowRedlineError{remain: 12, redline: 100}
	if errCredits.StatusCode() != http.StatusForbidden {
		t.Fatalf("必须返回 403，实际 %d", errCredits.StatusCode())
	}
	if msg := errCredits.Error(); msg == "" {
		t.Fatal("错误文案不能为空")
	}
}

// TestConfigurePluginParsesRoutableCredits 验证配置项解析与零值语义。
//
// 关键点：缺省键保持默认 100，显式 0 才是关闭闸门的 kill switch。
// 若用值类型，两种情况都会塌缩成 0，闸门会静默失效。
// [参数] t: 测试上下文。
// [返回] 无。
func TestConfigurePluginParsesRoutableCredits(t *testing.T) {
	t.Cleanup(setMinRoutableCredits(defaultMinRoutableCredits))
	t.Cleanup(func() { activeConfig.Store(defaultConfig()) })

	// 缺省键：保持默认红线。
	payload, _ := json.Marshal(map[string][]byte{"config_yaml": []byte("timeout_seconds: 30\n")})
	if errConfigure := configurePlugin(payload); errConfigure != nil {
		t.Fatalf("配置解析失败：%v", errConfigure)
	}
	if got := loadedMinRoutableCredits(); got != 100 {
		t.Fatalf("缺省键应保持默认 100，实际 %d", got)
	}

	// 显式配置：覆盖默认值。
	payload, _ = json.Marshal(map[string][]byte{"config_yaml": []byte("min_routable_credits: 250\n")})
	if errConfigure := configurePlugin(payload); errConfigure != nil {
		t.Fatalf("配置解析失败：%v", errConfigure)
	}
	if got := loadedMinRoutableCredits(); got != 250 {
		t.Fatalf("应生效为 250，实际 %d", got)
	}

	// 显式 0：关闭闸门（kill switch）。
	payload, _ = json.Marshal(map[string][]byte{"config_yaml": []byte("min_routable_credits: 0\n")})
	if errConfigure := configurePlugin(payload); errConfigure != nil {
		t.Fatalf("配置解析失败：%v", errConfigure)
	}
	if got := loadedMinRoutableCredits(); got != 0 {
		t.Fatalf("显式 0 应关闭闸门，实际 %d", got)
	}
}

// TestRunCreditTickClaimsDailyCreditBeforeFetch 验证「先领后查」的顺序约束。
//
// 顺序反了不会报错，只会让闸门拿到领取前的余额——账号明明刚领到 1500 分
// 却被判成 0 分并挡回宿主。断言方式是记录调用序列，而不是只看余额，
// 因为倒序时余额最终仍是正确值，只有序列能区分两种实现。
// [参数] t: 测试上下文。
// [返回] 无。
func TestRunCreditTickClaimsDailyCreditBeforeFetch(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	sa := &StoredAuth{AuthID: "coze-order"}
	var mu sync.Mutex
	var order []string
	deps := creditDeps{
		list: func() ([]*StoredAuth, error) { return []*StoredAuth{sa}, nil },
		claim: func(*StoredAuth) bool {
			mu.Lock()
			order = append(order, "claim")
			mu.Unlock()
			return true
		},
		fetch: func(*StoredAuth) (*creditSummary, error) {
			mu.Lock()
			order = append(order, "fetch")
			mu.Unlock()
			return &creditSummary{Total: 1500, Remain: 1500}, nil
		},
		confirm: func(*StoredAuth, *creditSummary) {
			mu.Lock()
			order = append(order, "confirm")
			mu.Unlock()
		},
		store: storeCreditSummary,
		logf:  func(string, string, map[string]any) {},
	}

	runCreditTick(deps)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"claim", "fetch", "confirm"}
	if len(order) != len(want) {
		t.Fatalf("调用序列应为 %v，实际 %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("调用序列应为 %v，实际 %v", want, order)
		}
	}
}

// TestRunCreditTickSkipsClaimWhenDisabledAccount 验证禁用账号不参与领取。
//
// 禁用号不承载流量，为它领积分纯属浪费上游调用；
// 而刷新循环本就在投递阶段跳过禁用账号，领取必须共用同一判据。
// [参数] t: 测试上下文。
// [返回] 无。
func TestRunCreditTickSkipsClaimWhenDisabledAccount(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	claimed := 0
	deps := creditDeps{
		list: func() ([]*StoredAuth, error) {
			return []*StoredAuth{{AuthID: "coze-off", Disabled: true}}, nil
		},
		claim: func(*StoredAuth) bool { claimed++; return true },
		fetch: func(*StoredAuth) (*creditSummary, error) {
			return &creditSummary{Total: 0, Remain: 0}, nil
		},
		store: storeCreditSummary,
		logf:  func(string, string, map[string]any) {},
	}

	runCreditTick(deps)
	if claimed != 0 {
		t.Fatalf("禁用账号不应触发领取，实际调用 %d 次", claimed)
	}
}

// TestRunCreditTickStoresAndIsolatesFailures 验证刷新循环的写入与失败隔离。
//
// 单账号失败不得影响其他账号，也不得写入错误摘要污染闸门。
// [参数] t: 测试上下文。
// [返回] 无。
func TestRunCreditTickStoresAndIsolatesFailures(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	good := &StoredAuth{AuthID: "coze-good"}
	bad := &StoredAuth{AuthID: "coze-bad"}
	deps := creditDeps{
		list: func() ([]*StoredAuth, error) { return []*StoredAuth{good, bad}, nil },
		fetch: func(sa *StoredAuth) (*creditSummary, error) {
			if sa.AuthID == "coze-bad" {
				return nil, http.ErrHandlerTimeout
			}
			return &creditSummary{Total: 1000, Remain: 777}, nil
		},
		store: storeCreditSummary,
		logf:  func(string, string, map[string]any) {},
	}

	if got := runCreditTick(deps); got != 1 {
		t.Fatalf("应只有 1 个账号刷新成功，实际 %d", got)
	}
	if remain, known := cachedCreditRemain("coze-good"); !known || remain != 777 {
		t.Fatalf("成功账号的积分应写入缓存，实际 remain=%d known=%v", remain, known)
	}
	if _, known := cachedCreditRemain("coze-bad"); known {
		t.Fatal("失败账号不得写入缓存")
	}
}

// TestRunCreditTickPublishesPoolSnapshot 验证刷新结束时真的发布了闸门快照。
//
// 闸门只读快照（见 policy.go），而快照只有这里能更新：少了这一步，
// 余额变化永远不会反映到闸门，低额账号会一直被当成健康号放行。
// 断言方式是「刷新前后闸门结论翻转」——只看返回值无法区分
// 「积分写进缓存」与「快照也跟着发布」两件事。
// [参数] t: 测试上下文。
// [返回] 无。
func TestRunCreditTickPublishesPoolSnapshot(t *testing.T) {
	t.Cleanup(resetCreditCache)
	t.Cleanup(func() { publishCreditPool(nil) })
	resetCreditCache()
	publishCreditPool(nil)

	healthy := &StoredAuth{AuthID: "coze-p1", CookieHeader: "sessionid=x", Label: "h"}
	low := &StoredAuth{AuthID: "coze-p2", CookieHeader: "sessionid=y", Label: "l"}

	// 初始：两个号都充足，闸门不会拦任何号。
	storeCreditSummary(healthy, &creditSummary{Total: 10000, Remain: 9000})
	storeCreditSummary(low, &creditSummary{Total: 10000, Remain: 9000})
	deps := creditDeps{
		list: func() ([]*StoredAuth, error) { return []*StoredAuth{healthy, low}, nil },
		fetch: func(sa *StoredAuth) (*creditSummary, error) {
			return &creditSummary{Total: 10000, Remain: 9000}, nil
		},
		store: storeCreditSummary,
		logf:  func(string, string, map[string]any) {},
	}
	runCreditTick(deps)
	if errCredits := verifyAccountCredits(low); errCredits != nil {
		t.Fatalf("两个号都充足时不应拦截：%v", errCredits)
	}

	// 第二轮：low 掉到红线以下，快照必须跟着更新。
	deps.fetch = func(sa *StoredAuth) (*creditSummary, error) {
		if sa.AuthID == low.AuthID {
			return &creditSummary{Total: 10000, Remain: 10}, nil
		}
		return &creditSummary{Total: 10000, Remain: 9000}, nil
	}
	runCreditTick(deps)
	if errCredits := verifyAccountCredits(low); errCredits == nil {
		t.Fatal("刷新后低额号必须被拦截：快照未随缓存更新")
	}
}

// TestHandleExecExecuteRejectsLowCreditBeforeUpstream 验证执行入口真的拦截。
//
// 断言的是「没有发出上游请求」：只要闸门晚于建连，请求就已经白跑，
// 低额账号的存在也就失去了意义。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleExecExecuteRejectsLowCreditBeforeUpstream(t *testing.T) {
	accounts := seedAccounts(t, 30, 5000)
	low := accounts[0]

	env := newExecutorTestEnv(t, func(conn *websocket.Conn) {})
	upstreamCalls := 0
	env.doer.handler = func(*http.Request, []byte) (*http.Response, error) {
		upstreamCalls++
		return jsonResponse(200, `{}`), nil
	}

	payload, _ := json.Marshal(map[string]any{
		"model":    "coze/doubao-seed-2-0-lite-260215",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	storage, _ := json.Marshal(low)
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{
		Payload:     payload,
		StorageJSON: storage,
	})

	_, errExec := handleExecExecute(raw)
	if errExec == nil {
		t.Fatal("低额账号必须被入口拦截")
	}
	coder, ok := errExec.(statusCoder)
	if !ok {
		t.Fatalf("拦截错误必须实现 StatusCode，实际类型 %T", errExec)
	}
	if coder.StatusCode() != http.StatusForbidden {
		t.Fatalf("必须返回 403，实际 %d", coder.StatusCode())
	}
	if upstreamCalls != 0 {
		t.Fatalf("拦截必须在触达上游前完成，实际调用 %d 次", upstreamCalls)
	}
}

// TestVerifyAccountCreditsUsesMemorySnapshotOnly 锁定闸门不得发起宿主 RPC。
//
// 闸门位于每个请求的执行入口。若它调用 host.auth.list，每次推理都会
// 多出「一次列目录 + 每账号一次读取」的 RPC 扇出，把首字时间拖慢数毫秒
// 到数十毫秒。这里用「清空账号目录」制造反证：只要闸门还依赖磁盘/宿主，
// 清空后它就读不到池子，会退化成放行；只有纯内存快照才能继续拦截。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsUsesMemorySnapshotOnly(t *testing.T) {
	accounts := seedAccounts(t, 30, 5000)
	low := accounts[0]

	// 抹掉磁盘账号：宿主 RPC 与目录扫描都会失败或返回空。
	dir := getAccountsDir()
	if errRemove := os.RemoveAll(dir); errRemove != nil {
		t.Fatalf("清理账号目录失败：%v", errRemove)
	}

	if errVerify := verifyAccountCredits(low); errVerify == nil {
		t.Fatal("闸门必须只依赖内存快照，账号目录消失不应让它失效")
	}
}

// ageCreditCache 把全部积分缓存条目改老，模拟刷新循环停摆。
// [参数] age: 条目年龄。
// [返回] 无。
func ageCreditCache(age time.Duration) {
	creditsCache.mu.Lock()
	for key := range creditsCache.entries {
		entry := creditsCache.entries[key]
		entry.fetched = time.Now().Add(-age)
		creditsCache.entries[key] = entry
	}
	creditsCache.mu.Unlock()
}

// TestVerifyAccountCreditsRejectsDepletedPoolWith400 验证全池零积分时快速失败。
//
// 这是 2026-10-21 生产事故形态：7 个号全部 0 分，闸门按「全池低额则放行」
// 交给上游，宿主把每个号都白打一遍（各 1.2-3.2 秒）才拿到 1000521。
// 判据收紧为「确定 0 分」：换号已无目的地，继续放行只产生上游消耗。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsRejectsDepletedPoolWith400(t *testing.T) {
	accounts := seedAccounts(t, 0, 0, 0)

	errVerify := verifyAccountCredits(accounts[0])
	if errVerify == nil {
		t.Fatal("全池确定 0 分时必须快速失败，不得再打上游")
	}
	coder, ok := errVerify.(statusCoder)
	if !ok {
		t.Fatalf("快速失败错误必须实现 StatusCode，实际类型 %T", errVerify)
	}
	// 必须是 400：403 会让宿主继续轮转下一个账号并冷却当前号，
	// 全池耗尽时等于把整池白打一遍上游。
	if coder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("全池耗尽必须返回请求级 400 以终止轮转，实际 %d", coder.StatusCode())
	}
}

// TestVerifyAccountCreditsDepletedPoolStillSwitchesWhenHealthyExists 验证健康号存在时仍走换号。
//
// 全池耗尽判据不得抢在换号判据前面：只要还有一个号能承载流量，
// 正确动作是换过去（403），而不是直接告诉用户失败（400）。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsDepletedPoolStillSwitchesWhenHealthyExists(t *testing.T) {
	accounts := seedAccounts(t, 0, 5000)

	errVerify := verifyAccountCredits(accounts[0])
	if errVerify == nil {
		t.Fatal("有健康号可选时必须拦截")
	}
	coder, ok := errVerify.(statusCoder)
	if !ok {
		t.Fatalf("拦截错误必须实现 StatusCode，实际类型 %T", errVerify)
	}
	if coder.StatusCode() != http.StatusForbidden {
		t.Fatalf("有健康号时必须返回 403 换号，实际 %d", coder.StatusCode())
	}
}

// TestVerifyAccountCreditsAllowsWhenPoolSnapshotEmpty 验证快照未发布时放行。
//
// 池快照只有积分循环跑完第一轮才发布。热重载后的窗口里快照为空，
// 此时若按「池里没有可用号」判定全池耗尽，会把整池误判成死号、
// 全线拒绝服务。空池必须退回放行。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsWhenPoolSnapshotEmpty(t *testing.T) {
	accounts := seedAccounts(t, 0)
	publishCreditPool(nil)

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("快照未发布时必须放行，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsAllowsWhenCreditCacheStale 验证缓存过期时不判全池耗尽。
//
// 「已知 0 分」不等于「现在仍是 0 分」：积分接口只写成功结果，
// 刷新循环停摆时缓存会长期停在旧值。若据此判死整池，
// 一次循环故障就会让账号明明补了积分仍持续拒绝服务。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsWhenCreditCacheStale(t *testing.T) {
	accounts := seedAccounts(t, 0, 0)
	ageCreditCache(creditDepletedTrustWindow + time.Minute)

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("积分数据过期时必须放行让上游自己判，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsAllowsWhenAnyAccountUnknown 验证有账号积分未知时不判全池耗尽。
//
// 未知不等于 0 分：余额接口抖动或被限流时缓存里没有结论，
// 把它当成「确定没积分」会把可用号一起判死。此时期望行为是
// 换到那个未知号试一次（403），而不是判全池耗尽（400）。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsWhenAnyAccountUnknown(t *testing.T) {
	accounts := seedAccounts(t, 0, 0)

	unknown := accounts[1]
	creditsCache.mu.Lock()
	creditsCache.entries[unknown.AuthID] = creditCacheEntry{
		summary: creditSummary{Error: "上游积分接口超时"},
		fetched: time.Now(),
	}
	creditsCache.mu.Unlock()
	publishCreditPool(accounts)

	errVerify := verifyAccountCredits(accounts[0])
	if errVerify == nil {
		t.Fatal("本号 0 分且有未知号时应换号，不应放行")
	}
	if _, isDepleted := errVerify.(*creditsPoolDepletedError); isDepleted {
		t.Fatalf("存在积分未知的账号时不得判定全池耗尽，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsAllowsResidualCredits 验证残血但非零的号仍放行。
//
// 全池耗尽判据只认 remain 恰好为 0：低于红线但仍有残值的号
// 可能跑得完这一轮，原有「放行让上游自己判」的语义不能被收紧。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsResidualCredits(t *testing.T) {
	accounts := seedAccounts(t, 30, 50)

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("全池残血但非零时必须放行，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsHealthyAccountSurvivesStaleDepletedPool 验证本号健康时不被滞后快照判死。
//
// 池快照最多滞后一个刷新周期：新账号补了积分进来，快照里却还没有它。
// 此时若只看「快照里可用号全 0」就判全池耗尽，会把明明能用的号拒绝服务，
// 所以全池判据必须同时确认当前账号自己也确定 0 分。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsHealthyAccountSurvivesStaleDepletedPool(t *testing.T) {
	accounts := seedAccounts(t, 5000, 0)
	healthy := accounts[0]

	// 快照只包含那个 0 分号：模拟健康号尚未进入快照的滞后窗口。
	publishCreditPool(accounts[1:])

	if errVerify := verifyAccountCredits(healthy); errVerify != nil {
		t.Fatalf("本号积分充足时必须放行，实际报错：%v", errVerify)
	}
}

// TestVerifyAccountCreditsAllowsWhenPeerCreditCacheStale 验证他号缓存过期时不判全池耗尽。
//
// 与上一条不同：本号的数据是新的（确实 0 分），但池里另一个号的数据已过期。
// 过期意味着它可能已经充值（扣子每日登录送积分），此时判定「全池确定耗尽」
// 会把那个其实可用的号一起判死并拒绝服务，必须放行让上游自己判。
// [参数] t: 测试上下文。
// [返回] 无。
func TestVerifyAccountCreditsAllowsWhenPeerCreditCacheStale(t *testing.T) {
	accounts := seedAccounts(t, 0, 0)
	peer := accounts[1]

	// 只把另一个号的数据改老，本号保持新鲜。
	creditsCache.mu.Lock()
	entry := creditsCache.entries[peer.AuthID]
	entry.fetched = time.Now().Add(-creditDepletedTrustWindow - time.Minute)
	creditsCache.entries[peer.AuthID] = entry
	creditsCache.mu.Unlock()

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("他号积分数据过期时必须放行，实际报错：%v", errVerify)
	}
}

// TestCreditsPoolDepletedErrorCarries400 锁定全池耗尽错误的状态码语义。
//
// 400 是这套止血逻辑的关键：宿主按请求级故障处理（不冷却、不换号），
// 退化成 403 或无状态码都会让整池被白打一遍。
// [参数] t: 测试上下文。
// [返回] 无。
func TestCreditsPoolDepletedErrorCarries400(t *testing.T) {
	errCredits := &creditsPoolDepletedError{}
	if errCredits.StatusCode() != http.StatusBadRequest {
		t.Fatalf("必须返回 400，实际 %d", errCredits.StatusCode())
	}
	if msg := errCredits.Error(); msg == "" {
		t.Fatal("错误文案不能为空")
	}
	// 必须能被失败分类识别为请求级：走的是 statusCoder 接口。
	status, code := executionFault(errCredits)
	if status != http.StatusBadRequest || code != faultCodeRequest {
		t.Fatalf("信封分类必须是请求级 400，实际 %d/%s", status, code)
	}
}

// TestHandleExecExecuteRejectsDepletedPoolWithoutUpstream 验证入口在全池耗尽时零上游消耗。
//
// 这是本轮修复的真实目标：不是「返回什么错误码」，而是
// 「不再产生上游请求与积分消耗」。只测判定函数会漏掉入口没接上的情况。
// [参数] t: 测试上下文。
// [返回] 无。
func TestHandleExecExecuteRejectsDepletedPoolWithoutUpstream(t *testing.T) {
	accounts := seedAccounts(t, 0, 0, 0)

	env := newExecutorTestEnv(t, func(conn *websocket.Conn) {})
	upstreamCalls := 0
	env.doer.handler = func(*http.Request, []byte) (*http.Response, error) {
		upstreamCalls++
		return jsonResponse(200, `{}`), nil
	}

	payload, _ := json.Marshal(map[string]any{
		"model":    "coze/doubao-seed-2-0-lite-260215",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	storage, _ := json.Marshal(accounts[0])
	raw, _ := json.Marshal(pluginapi.ExecutorRequest{
		Payload:     payload,
		StorageJSON: storage,
	})

	_, errExec := handleExecExecute(raw)
	if errExec == nil {
		t.Fatal("全池耗尽必须被入口拦截")
	}
	coder, ok := errExec.(statusCoder)
	if !ok {
		t.Fatalf("拦截错误必须实现 StatusCode，实际类型 %T", errExec)
	}
	if coder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("必须返回请求级 400 终止换号，实际 %d", coder.StatusCode())
	}
	if upstreamCalls != 0 {
		t.Fatalf("全池耗尽必须零上游消耗，实际调用 %d 次", upstreamCalls)
	}
}

// TestDepletedPoolGateDisabledByKillSwitch 验证红线设为 0 时全池判据一并关闭。
//
// min_routable_credits: 0 是「完全不要闸门」的 kill switch。
// 若全池耗尽判据不受它约束，运维关掉闸门后仍会被 400 拒绝服务，
// 这个开关就不再是开关。
// [参数] t: 测试上下文。
// [返回] 无。
func TestDepletedPoolGateDisabledByKillSwitch(t *testing.T) {
	accounts := seedAccounts(t, 0, 0)
	before := loadedMinRoutableCredits()
	applyMinRoutableCredits(0)
	t.Cleanup(func() { applyMinRoutableCredits(before) })

	if errVerify := verifyAccountCredits(accounts[0]); errVerify != nil {
		t.Fatalf("红线为 0 时必须放行，实际报错：%v", errVerify)
	}
}
