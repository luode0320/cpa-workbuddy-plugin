// slow_account.go 实现「同模型 TTFT 横比 → 慢账号在该模型上冷却」的状态机。
//
// 定位：这是 accountFailover.go 与 usage_rotation.go 的第三个姊妹模块。
// 三者状态完全独立，理由各不相同：
//   - 失败冷却：由上游错误触发，每次成功即被 resetAccountFailover 清零；
//   - 轮换冷却：由连续承流时长触发，成功后仍要持续到窗口结束；
//   - 慢速冷却：由「比同池同模型慢太多」触发，成功后同样不应被清零——
//     慢是一种持续状态，一次「没那么慢」的成功不能证明账号已恢复。
//
// 为什么判据是相对倍数而不是绝对毫秒：生产同一模型（deepseek-v4.1-flash）
// 的请求 input 普遍在 33 万 tokens 量级，prefill 本身就落在十几秒区间，
// 十三秒与二十一秒的差异在绝对阈值下会被一并判慢，一次禁光整池健康账号。
// 用「同模型全池中位数」作基准，全池一起变慢时基准同步抬高，无人被判慢——
// 这正确表达了「上游整体拥堵不是某个账号的问题」。
//
// 状态只存内存：不写 auth 文件、不写 DB，进程重启即清空。与另两个机制一致。
//
// 最近修改时间：2026-10-25；改动原因：新增慢速账号冷却机制。
package main

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// 默认值。全部可由插件配置覆盖，显式填 0 表示关闭对应能力。
const (
	// defaultSlowAccountEnabled 是总开关默认值：开启。
	defaultSlowAccountEnabled = true
	// defaultSlowTTFTRatioX10 是倍数阈值 ×10（25 即 2.5 倍）。
	// 用整数存储避免浮点配置在逐行解析里引入精度与格式歧义。
	defaultSlowTTFTRatioX10 = 25
	// defaultSlowWindowMinutes 是 TTFT 样本统计窗口（分钟）。
	defaultSlowWindowMinutes = 15
	// defaultSlowMinSamples 是单账号单模型参与判定所需的最小样本数。
	defaultSlowMinSamples = 5
	// defaultSlowCooldownMinutes 是判定为慢之后的冷却时长（分钟）。
	defaultSlowCooldownMinutes = 10
	// defaultSlowMinTTFTMs 是绝对下限（毫秒）：低于该值一律不判慢。
	// 它挡住的是「全池都很快时，1.2s 对 400ms 也被算成 3 倍」这类相对放大。
	defaultSlowMinTTFTMs = 3000
	// defaultSlowMinPoolSize 是账号池准入下限：账号总数（启用+停用）超过它才允许判定。
	// 判据基准是各账号中位数的下四分位（P25），池子太小时该统计量退化成
	// 「取较小值」：两个账号里任一次抖动就会让其中一个被判慢、两号互相顶替；
	// 池内出现单个毛刺账号即可拉低基准。2026-10-25 生产实证——激进参数下
	// 池内 TTFT 仅 1.26 倍差却持续判慢、冷却主体在两号间反复跳变。
	// 账号数不足时相对判据只是在放大噪声，故以硬闸门挡住。
	defaultSlowMinPoolSize = 20
	// slowSampleCap 是每个「账号×模型」保留的样本上限，超出丢弃最早样本。
	slowSampleCap = 32
	// slowBaselineQuantile 是基准分位点：取各账号中位数的下四分位。
	// 用中位数的中位数会让「慢号占多数」的池子把基准抬到慢号水平，
	// 结果一个也判不出来；取 P25 让少数健康号仍能定义基准。
	slowBaselineQuantile = 0.25
	// slowPoolSizeRefreshInterval 是账号池规模的刷新周期。
	// 账号枚举走宿主 RPC（host.auth.list），是跨进程调用，绝不能放在每请求
	// 热路径上——判定入口只读内存缓存，这里由独立 ticker 定期刷新。
	slowPoolSizeRefreshInterval = 10 * time.Minute
)

var (
	// slowAccountEnabled 是总开关，默认开启；关闭需显式配置 slow_account_enabled: false。
	slowAccountEnabled   = defaultSlowAccountEnabled
	slowAccountEnabledMu sync.RWMutex

	// slowTTFTRatioX10 对应 slow_ttft_ratio_x10。
	slowTTFTRatioX10 = defaultSlowTTFTRatioX10
	// slowWindowMinutes 对应 slow_window_minutes。
	slowWindowMinutes = defaultSlowWindowMinutes
	// slowMinSamples 对应 slow_min_samples。
	slowMinSamples = defaultSlowMinSamples
	// slowCooldownMinutes 对应 slow_cooldown_minutes。
	slowCooldownMinutes = defaultSlowCooldownMinutes
	// slowMinTTFTMs 对应 slow_min_ttft_ms。
	slowMinTTFTMs = defaultSlowMinTTFTMs
	// slowMinPoolSize 对应 slow_min_pool_size：账号总数超过该值才判定。
	slowMinPoolSize = defaultSlowMinPoolSize
	slowConfigMu    sync.RWMutex

	// slowMu guards slowStates.
	slowMu     sync.Mutex
	slowStates = make(map[string]*slowAccountState)

	// slowPoolSize 是最近一次探测到的账号总数（启用 + 停用），零值表示尚未探测成功。
	// 之所以要缓存而不是每次判定都查：账号枚举走宿主 RPC（跨进程），
	// 而判定入口挂在每次成功请求的成功路径上，同步调 RPC 会直接抬高响应延迟。
	slowPoolSize   int
	slowPoolSizeMu sync.RWMutex
)

// slowSample 是一次成功请求的首字延迟样本。
type slowSample struct {
	// at 是样本产生时刻，用于按时间窗裁剪。
	at time.Time
	// ttftNS 是首字延迟纳秒值。
	ttftNS uint64
}

// slowAccountState 是单个「账号 × 模型」的慢速状态。
type slowAccountState struct {
	// samples 是窗口内的 TTFT 样本，按到达顺序追加，超上限丢最早。
	samples []slowSample
	// cooldownUntil 非零表示该账号在该模型上因慢被冷却。
	cooldownUntil time.Time
	// cooldownStartedAt 是本轮冷却的起始时刻。冷却结束后只有该时刻之后的
	// 新样本参与判定，否则解冷瞬间旧样本会让账号立刻被再次判慢，形成
	// 「冷却—解冷—再冷却」的死循环。
	cooldownStartedAt time.Time
}

// windowSamplesLocked 返回窗口内的样本值副本，并就地丢弃过期样本。
// 调用方必须持有 slowMu。
// [参数] st：状态指针；window：统计窗口；now：当前时间；
//
//	respectCooldownStart：是否应用「冷却起点」下界。判定路径必须为 true
//	  （否则解冷后旧样本会立刻触发再次冷却，形成死循环）；面板展示路径为
//	  false（要展示的是账号近期的真实延迟，不该因为刚冷却过就显示成空）。
//
// [返回] 窗口内样本的 TTFT 纳秒值切片（新分配，可自由排序）。
// 最近修改时间：2026-10-25；改动原因：新增，时间窗语义的唯一实现。
func windowSamplesLocked(st *slowAccountState, window time.Duration, now time.Time, respectCooldownStart bool) []uint64 {
	if st == nil || len(st.samples) == 0 {
		return nil
	}
	cutoff := now.Add(-window)
	// 冷却结束后的样本下界：上一轮冷却期间及之前的样本不再参与判定。
	if respectCooldownStart && st.cooldownStartedAt.After(cutoff) {
		cutoff = st.cooldownStartedAt
	}
	// 样本按时间递增，找到第一个仍在窗口内的下标即可整体切片。
	start := 0
	for start < len(st.samples) && st.samples[start].at.Before(cutoff) {
		start++
	}
	if start > 0 {
		st.samples = st.samples[start:]
	}
	out := make([]uint64, len(st.samples))
	for i, s := range st.samples {
		out[i] = s.ttftNS
	}
	return out
}

// slowConfig 是一次读取得到的配置快照，避免判定过程中反复取锁。
type slowConfig struct {
	enabled     bool
	ratioX10    int64
	window      time.Duration
	minSamples  int
	cooldown    time.Duration
	minTTFTNano uint64
	// minPoolSize 是账号池准入下限：账号总数须严格大于它才允许判定。
	minPoolSize int
}

// loadedSlowConfig 读取当前配置快照。
// [参数] 无。
// [返回] 配置快照；窗口/样本数/冷却非正数时返回 enabled=false，等同关闭。
// 最近修改时间：2026-10-25；改动原因：新增，慢速判定的唯一配置入口。
func loadedSlowConfig() slowConfig {
	slowAccountEnabledMu.RLock()
	enabled := slowAccountEnabled
	slowAccountEnabledMu.RUnlock()

	slowConfigMu.RLock()
	ratioX10 := slowTTFTRatioX10
	window := slowWindowMinutes
	minSamples := slowMinSamples
	cooldown := slowCooldownMinutes
	minTTFTMs := slowMinTTFTMs
	minPoolSize := slowMinPoolSize
	slowConfigMu.RUnlock()

	cfg := slowConfig{
		enabled:     enabled,
		ratioX10:    int64(ratioX10),
		window:      time.Duration(window) * time.Minute,
		minSamples:  minSamples,
		cooldown:    time.Duration(cooldown) * time.Minute,
		minTTFTNano: uint64(minTTFTMs) * uint64(time.Millisecond),
		minPoolSize: minPoolSize,
	}
	if cfg.ratioX10 <= 10 || cfg.window <= 0 || cfg.minSamples <= 0 || cfg.cooldown <= 0 {
		cfg.enabled = false
	}
	return cfg
}

// setLoadedSlowConfig 整套写入慢速冷却配置，并在真正变化时留下审计日志。
//
// 六个键共同决定判定结果，分开写会留下「阈值改了、窗口还是旧值」的中间态，
// 因此整体写入。配置变更必须留痕：慢速判定参数直接决定「哪些号被判慢」，
// 线上误判的第一排查动作就是核对参数。只在真正变化时打印，避免重载刷屏。
// [参数] enabled：总开关；ratioX10：倍数阈值 ×10；window：统计窗口（分钟）；
//
//	minSamples：样本门槛；cooldown：冷却时长（分钟）；minTTFTMs：绝对下限（毫秒）。
//
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，慢速冷却配置的唯一写入口与变更留痕。
func setLoadedSlowConfig(enabled bool, ratioX10, window, minSamples, cooldown, minTTFTMs, minPoolSize int) {
	slowAccountEnabledMu.Lock()
	prevEnabled := slowAccountEnabled
	slowAccountEnabled = enabled
	slowAccountEnabledMu.Unlock()

	slowConfigMu.Lock()
	prevRatio := slowTTFTRatioX10
	prevWindow := slowWindowMinutes
	prevMinSamples := slowMinSamples
	prevCooldown := slowCooldownMinutes
	prevMinTTFT := slowMinTTFTMs
	prevMinPool := slowMinPoolSize
	slowTTFTRatioX10 = ratioX10
	slowWindowMinutes = window
	slowMinSamples = minSamples
	slowCooldownMinutes = cooldown
	slowMinTTFTMs = minTTFTMs
	slowMinPoolSize = minPoolSize
	slowConfigMu.Unlock()

	if prevEnabled != enabled || prevRatio != ratioX10 || prevWindow != window ||
		prevMinSamples != minSamples || prevCooldown != cooldown || prevMinTTFT != minTTFTMs ||
		prevMinPool != minPoolSize {
		log.Printf("[zcode] slow config applied: enabled=%v ratio=%.1fx window=%dm min_samples=%d cooldown=%dm min_ttft=%dms min_pool=%d",
			enabled, float64(ratioX10)/10, window, minSamples, cooldown, minTTFTMs, minPoolSize)
	}
}

// applySlowConfig 把 YAML 里出现的 slow_* 键应用到运行时配置。
//
// 字段是指针类型：nil 表示该键未配置，此时保持默认值不动；只有显式出现才覆盖。
// 少了这层区分，一次只改其它键的 reconfigure 会把人工关闭的慢速冷却冲回默认开启。
// [参数] decoded：解析出的 YAML 配置。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，慢速冷却配置入口。
func applySlowConfig(decoded Config) {
	// 任一键出现即视为「本插件关心慢速配置」，整套写入；全为 nil 时直接跳过。
	if decoded.SlowAccountEnabled == nil && decoded.SlowTTFTRatioX10 == nil &&
		decoded.SlowWindowMinutes == nil && decoded.SlowMinSamples == nil &&
		decoded.SlowCooldownMin == nil && decoded.SlowMinTTFTMs == nil &&
		decoded.SlowMinPoolSize == nil {
		return
	}

	slowAccountEnabledMu.RLock()
	enabled := slowAccountEnabled
	slowAccountEnabledMu.RUnlock()

	slowConfigMu.RLock()
	ratioX10 := slowTTFTRatioX10
	window := slowWindowMinutes
	minSamples := slowMinSamples
	cooldown := slowCooldownMinutes
	minTTFTMs := slowMinTTFTMs
	minPoolSize := slowMinPoolSize
	slowConfigMu.RUnlock()

	if decoded.SlowAccountEnabled != nil {
		enabled = *decoded.SlowAccountEnabled
	}
	if decoded.SlowTTFTRatioX10 != nil && *decoded.SlowTTFTRatioX10 > 0 {
		ratioX10 = *decoded.SlowTTFTRatioX10
	}
	if decoded.SlowWindowMinutes != nil && *decoded.SlowWindowMinutes > 0 {
		window = *decoded.SlowWindowMinutes
	}
	if decoded.SlowMinSamples != nil && *decoded.SlowMinSamples > 0 {
		minSamples = *decoded.SlowMinSamples
	}
	if decoded.SlowCooldownMin != nil && *decoded.SlowCooldownMin > 0 {
		cooldown = *decoded.SlowCooldownMin
	}
	if decoded.SlowMinTTFTMs != nil && *decoded.SlowMinTTFTMs >= 0 {
		minTTFTMs = *decoded.SlowMinTTFTMs
	}
	if decoded.SlowMinPoolSize != nil && *decoded.SlowMinPoolSize >= 0 {
		minPoolSize = *decoded.SlowMinPoolSize
	}

	setLoadedSlowConfig(enabled, ratioX10, window, minSamples, cooldown, minTTFTMs, minPoolSize)
}

// slowKeysFor 返回该账号在慢速表里可能出现的全部键（账号×模型）。
// 与 failoverKeysLocked / rotationKeysFor 同口径，屏蔽宿主 auth 标识多形态差异。
// [参数] authID：账号标识；model：上游模型名。
// [返回] 去重后的键列表；账号或模型为空时返回空切片。
// 最近修改时间：2026-10-25；改动原因：新增，账号×模型级状态键构造。
func slowKeysFor(authID, model string) []string {
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if authID == "" || model == "" {
		return nil
	}
	keys := []string{authID + "\x00" + model}
	if norm := normalizeFailoverKey(authID); norm != "" && norm != authID {
		keys = append(keys, norm+"\x00"+model)
	}
	return keys
}

// slowStateLocked 返回该「账号×模型」的状态指针，不存在时按需创建。
// 调用方必须持有 slowMu。
// [参数] authID：账号标识；model：上游模型名；create：不存在时是否创建。
// [返回] 状态指针；create=false 且不存在时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，收敛多键映射的重复查找。
func slowStateLocked(authID, model string, create bool) *slowAccountState {
	keys := slowKeysFor(authID, model)
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if st, ok := slowStates[key]; ok {
			return st
		}
	}
	if !create {
		return nil
	}
	st := &slowAccountState{}
	for _, key := range keys {
		slowStates[key] = st
	}
	return st
}

// recordSlowSample 记入一次成功请求的首字延迟样本。
//
// 只接受成功请求：失败请求的「耗时」是错误返回耗时，不代表首字延迟；
// 混进来会把健康账号的样本拉到错误量级。
// [参数] authID：实际承载请求的账号标识；model：上游模型名；
//
//	ttftNS：本次首字延迟纳秒值；now：当前时间。
//
// [返回] 写入后该「账号×模型」的样本数。
// 最近修改时间：2026-10-25；改动原因：新增样本写入入口，并补冷却到期恢复日志。
func recordSlowSample(authID, model string, ttftNS uint64, now time.Time) int {
	cfg := loadedSlowConfig()
	if !cfg.enabled || ttftNS == 0 {
		return 0
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if authID == "" || model == "" {
		return 0
	}

	slowMu.Lock()
	st := slowStateLocked(authID, model, true)
	if st == nil {
		slowMu.Unlock()
		return 0
	}
	// 到期恢复留痕：冷却已过期而账号再次成功，说明它重新进入可选池。
	// 放在这里而不是 slowCoolingDown 里，是因为该函数挂在每请求的路由热路径，
	// 必须保持纯读；这里本就持有写锁且只在真实成功时走到。
	// 必须先于「冷却中」判断执行：否则过期后第一条样本会被下面那个分支
	// 当成仍在冷却而提前返回，恢复事件永远不会被记录。
	// 打点后必须把 cooldownUntil 清零：否则「已过期」这个条件在后续每条成功
	// 请求上都成立，日志会无限重复刷屏（生产实证：同一账号 1 分钟内刷 8 条）。
	expiredAfter := time.Duration(0)
	if !st.cooldownUntil.IsZero() && !now.Before(st.cooldownUntil) {
		expiredAfter = now.Sub(st.cooldownUntil)
		st.cooldownUntil = time.Time{}
	}
	// 冷却中的样本不参与判定，记进来只会让恢复后的基准失真。
	if now.Before(st.cooldownUntil) {
		count := len(st.samples)
		slowMu.Unlock()
		return count
	}
	st.samples = append(st.samples, slowSample{at: now, ttftNS: ttftNS})
	if len(st.samples) > slowSampleCap {
		st.samples = st.samples[len(st.samples)-slowSampleCap:]
	}
	count := len(st.samples)
	slowMu.Unlock()

	if expiredAfter > 0 {
		log.Printf("[zcode] slow cooldown expired: auth=%s model=%s expired_after=%s ttft=%dms",
			authID, model, expiredAfter.Truncate(time.Second), ttftNS/1e6)
	}
	return count
}

// slowMedian 返回样本的中位数；空切片返回 0。
// [参数] values：样本切片（本函数会就地排序）。
// [返回] 中位数。
// 最近修改时间：2026-10-25；改动原因：新增，中位数口径的唯一实现。
func slowMedian(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

// slowQuantile 返回已排序切片的分位数（0 ≤ q ≤ 1），空切片返回 0。
// [参数] sorted：已按升序排序的切片；q：分位点。
// [返回] 该分位的值。
// 最近修改时间：2026-10-25；改动原因：新增，基准计算需要可调分位。
func slowQuantile(sorted []uint64, q float64) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}

// slowBaselineForModel 计算同模型全池基准：各账号中位数的下四分位（P25）。
//
// 只统计窗口内样本数达标的账号——刚接入的账号样本不足，把它们算进来会让基准
// 被单条毛刺带偏。
//
// 为什么用下四分位而不是中位数：多账号池里「慢号占多数」是常见形态（上游对
// 部分账号限速、其余正常），若取中位数，基准会被多数慢号抬到慢号自己的水平，
// 结果所有慢号都判不出来——机制在上线后静默失效。下四分位让少数健康账号
// 仍有能力定义基准；同时它也不是最小值，单个异常快的账号无法把基准压到
// 地板上，再叠加 slow_min_ttft_ms 绝对下限兜住低延迟场景。
// 调用方必须持有 slowMu。
// [参数] model：上游模型名；minSamples：样本数门槛；
//
//	window：统计窗口；now：当前时间。
//
// [返回] 基准值（纳秒）；无达标账号时返回 0。
// 最近修改时间：2026-10-25；改动原因：新增，相对判据的基准计算。
func slowBaselineForModelLocked(model string, minSamples int, window time.Duration, now time.Time) uint64 {
	medians := make([]uint64, 0, len(slowStates))
	seen := make(map[*slowAccountState]struct{}, len(slowStates))
	for key, st := range slowStates {
		if st == nil {
			continue
		}
		// 同一状态可能挂在多个键下（原文 + 归一化），按指针去重。
		if _, dup := seen[st]; dup {
			continue
		}
		if !strings.HasSuffix(key, "\x00"+model) {
			continue
		}
		seen[st] = struct{}{}
		values := windowSamplesLocked(st, window, now, true)
		if len(values) < minSamples {
			continue
		}
		medians = append(medians, slowMedian(values))
	}
	if len(medians) == 0 {
		return 0
	}
	sort.Slice(medians, func(i, j int) bool { return medians[i] < medians[j] })
	return slowQuantile(medians, slowBaselineQuantile)
}

// evaluateSlowAccount 判定某「账号×模型」是否应进入慢速冷却。
//
// 三个条件同时成立才判慢：倍数超阈值、绝对值超下限、样本数达标。
// 冷却前先做全冷检查：若同模型下除它以外全部已冷却，先放开一半，
// 否则最后一次判定会把该模型的全部账号关掉，调度无号可选。
// [参数] authID：账号标识；model：上游模型名；now：当前时间。
// [返回] true 表示本次被置入慢速冷却。
// 最近修改时间：2026-10-25；改动原因：新增慢速判定入口，并补判定取证与防抽干放开日志。
func evaluateSlowAccount(authID, model string, now time.Time) bool {
	cfg := loadedSlowConfig()
	if !cfg.enabled {
		return false
	}
	// 准入闸门：账号池太小（≤ minPoolSize）时 P25 基线不成立，直接不判。
	if !slowPoolAdmitted(cfg.minPoolSize) {
		return false
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if authID == "" || model == "" {
		return false
	}

	slowMu.Lock()

	st := slowStateLocked(authID, model, false)
	if st == nil {
		slowMu.Unlock()
		return false
	}
	if now.Before(st.cooldownUntil) {
		slowMu.Unlock()
		return false
	}

	values := windowSamplesLocked(st, cfg.window, now, true)
	if len(values) < cfg.minSamples {
		slowMu.Unlock()
		return false
	}
	median := slowMedian(values)
	if median < cfg.minTTFTNano {
		slowMu.Unlock()
		return false
	}
	baseline := slowBaselineForModelLocked(model, cfg.minSamples, cfg.window, now)
	if baseline == 0 {
		slowMu.Unlock()
		return false
	}
	// 用整数比较 median/baseline >= ratio/10，避免浮点误差。
	if median*10 < baseline*uint64(cfg.ratioX10) {
		slowMu.Unlock()
		return false
	}

	// 全冷检查：同模型下除本账号外全部已冷却时，先放开一半。
	released := releaseSharedSlowCooldownsLocked(model, authID, now)

	st.cooldownUntil = now.Add(cfg.cooldown)
	// 记录本轮冷却起点：解冷后只有这之后的样本参与判定，避免旧样本让它
	// 一解冷就被再次判慢。样本本身保留，供面板继续展示平均延迟。
	st.cooldownStartedAt = now
	slowMu.Unlock()

	// 判定取证：把判据的三个输入（中位数、基线、阈值）与冷却时长一次性记全，
	// 否则线上只能看到「某号在冷却」，无法判断是真实慢还是参数配错。
	// 日志必须在解锁之后写：log.Printf 会做 I/O，持 slowMu 期间阻塞会让所有
	// 路由判定排队。
	log.Printf("[zcode] slow cooldown entered: auth=%s model=%s median=%dms baseline=%dms ratio=%.1fx threshold=%.1fx samples=%d cooldown=%s released=%d",
		authID, model, median/1e6, baseline/1e6, float64(median)/float64(baseline),
		float64(cfg.ratioX10)/10, len(values), cfg.cooldown, len(released))
	// 防抽干放开必须单独留痕：否则线上看到某号突然不再冷却，无法区分
	// 「到期自然恢复」与「为腾出候选被强制放开」。
	if len(released) > 0 {
		log.Printf("[zcode] slow cooldown released to avoid pool drain: model=%s count=%d accounts=%s",
			model, len(released), strings.Join(released, ","))
	}
	return true
}

// releaseSharedSlowCooldownsLocked 在同模型「除目标账号外全部已冷却」时放开一半。
//
// 放开剩余时间最长的那些（按 cooldownUntil 降序），因为它们的冷却最不可能是
// 短窗口残留；同时清空样本，让它们按全新一轮重新计时。
// 调用方必须持有 slowMu。
// [参数] model：上游模型名；excluding：即将被冷却的账号；now：当前时间。
// [返回] 被放开的账号标识列表（仅用于日志与测试）。
// 最近修改时间：2026-10-25；改动原因：新增，实现防抽干策略。
func releaseSharedSlowCooldownsLocked(model, excluding string, now time.Time) []string {
	excludeNorm := normalizeFailoverKey(excluding)
	type cooling struct {
		id  string
		top time.Time
	}
	var list []cooling
	seen := make(map[*slowAccountState]struct{}, len(slowStates))
	for key, st := range slowStates {
		if st == nil || !now.Before(st.cooldownUntil) {
			continue
		}
		if _, dup := seen[st]; dup {
			continue
		}
		if !strings.HasSuffix(key, "\x00"+model) {
			continue
		}
		seen[st] = struct{}{}
		id := strings.TrimSuffix(key, "\x00"+model)
		if id == excluding {
			continue
		}
		if excludeNorm != "" && normalizeFailoverKey(id) == excludeNorm {
			continue
		}
		list = append(list, cooling{id: id, top: st.cooldownUntil})
	}
	if len(list) == 0 {
		return nil
	}
	// 放开一半（向上取整）：同模型只剩一个冷却号时不放开，避免等于没冷却。
	release := (len(list) + 1) / 2
	sort.SliceStable(list, func(i, j int) bool { return list[i].top.After(list[j].top) })

	released := make([]string, 0, release)
	for i := 0; i < release && i < len(list); i++ {
		st := slowStateLocked(list[i].id, model, false)
		if st == nil {
			continue
		}
		st.cooldownUntil = time.Time{}
		st.cooldownStartedAt = now
		released = append(released, list[i].id)
	}
	return released
}

// slowCoolingDown 判定账号在该模型上是否因慢处于冷却中。
//
// 与 isAuthModelCoolingDown 并列而非合并：两者生命周期不同——失败冷却会被
// 一次成功清零，慢速冷却必须稳定持续到窗口结束。
//
// 本函数保持纯读语义（只判断、不改状态）：它挂在每请求的路由热路径上，
// 且被面板与换号候选过滤共同调用；在这里回收状态会让「查一次」变成写操作，
// 与 slowCooldownSnapshot 的读取语义打架。到期恢复的可观测性改由
// noteSlowObservation 的恢复检测负责（那里本来就持有写锁与时间基准）。
// [参数] authID：账号标识；model：上游模型名；now：当前时间。
// [返回] true 表示仍在慢速冷却期内。
// 最近修改时间：2026-10-25；改动原因：新增，路由层排除的唯一判据。
func slowCoolingDown(authID, model string, now time.Time) bool {
	cfg := loadedSlowConfig()
	if !cfg.enabled {
		return false
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if authID == "" || model == "" {
		return false
	}
	slowMu.Lock()
	defer slowMu.Unlock()
	st := slowStateLocked(authID, model, false)
	if st == nil {
		return false
	}
	return now.Before(st.cooldownUntil)
}

// noteSlowObservation 是请求成功后的慢速打点入口：记样本，达标则判定冷却。
//
// 打点必须在「真实成功」路径上调用一次；失败换号不记样本。
// [参数] authID：实际承载请求的账号标识；model：上游模型名；ttftNS：首字延迟。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：新增，把配置读取与判定串成单一入口。
func noteSlowObservation(authID, model string, ttftNS uint64) {
	cfg := loadedSlowConfig()
	if !cfg.enabled || ttftNS == 0 {
		return
	}
	// 准入闸门放在打点之前而不是只在判定里：账号数不足时连样本都不该记。
	// 池子长大后若拿着欠账的陈旧样本立刻判一轮，等于用不成立的历史数据误判。
	if !slowPoolAdmitted(cfg.minPoolSize) {
		return
	}
	now := time.Now()
	if recordSlowSample(authID, model, ttftNS, now) < cfg.minSamples {
		return
	}
	if evaluateSlowAccount(authID, model, now) {
		pruneSlowStates(now)
	}
}

// pruneSlowStates 清理已到期且无样本痕迹的状态，防止 map 无限增长。
// [参数] now：当前时间。
// [返回] 清理的条目数。
// 最近修改时间：2026-10-25；改动原因：新增，与 failover / rotation 状态清理同口径。
func pruneSlowStates(now time.Time) int {
	slowMu.Lock()
	defer slowMu.Unlock()
	removed := 0
	for key, st := range slowStates {
		if st == nil {
			delete(slowStates, key)
			removed++
			continue
		}
		if len(st.samples) == 0 && !now.Before(st.cooldownUntil) {
			delete(slowStates, key)
			removed++
		}
	}
	return removed
}

// slowCoolingModelsFor 返回该账号当前因慢而冷却的全部模型（按名称排序）。
// 面板按账号渲染，而慢速冷却按账号×模型生效，因此需要这份清单把
// 「该号整体健康但个别模型让位」这一事实表达清楚。
// [参数] authID：账号标识；now：当前时间。
// [返回] 冷却中的模型名列表；无冷却时返回 nil。
// 最近修改时间：2026-10-25；改动原因：新增，面板展示的读取入口。
func slowCoolingModelsFor(authID string, now time.Time) []string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	slowMu.Lock()
	defer slowMu.Unlock()
	var models []string
	seen := make(map[*slowAccountState]struct{}, len(slowStates))
	for key, st := range slowStates {
		if st == nil || !now.Before(st.cooldownUntil) {
			continue
		}
		if _, dup := seen[st]; dup {
			continue
		}
		idx := strings.Index(key, "\x00")
		if idx < 0 {
			continue
		}
		keyID, model := key[:idx], key[idx+1:]
		if keyID != authID && normalizeFailoverKey(keyID) != normalizeFailoverKey(authID) {
			continue
		}
		seen[st] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// slowCooldownSnapshot 返回该账号在该模型上的慢速冷却剩余时长与窗口内平均 TTFT。
// 供面板展示使用；无状态时返回 (0, 0, false)。
// [参数] authID：账号标识；model：上游模型名；now：当前时间。
// [返回] 剩余冷却时长、窗口内平均 TTFT 纳秒值、是否处于慢速冷却。
// 最近修改时间：2026-10-25；改动原因：新增，面板可观测性的唯一读取入口。
func slowCooldownSnapshot(authID, model string, now time.Time) (time.Duration, uint64, bool) {
	// 先取配置快照再进 slowMu：与 evaluateSlowAccount 的锁顺序保持一致，
	// 避免不同调用路径上出现锁序反转。
	cfg := loadedSlowConfig()
	slowMu.Lock()
	defer slowMu.Unlock()
	st := slowStateLocked(authID, model, false)
	if st == nil {
		return 0, 0, false
	}
	avg := uint64(0)
	if values := windowSamplesLocked(st, cfg.window, now, false); len(values) > 0 {
		var sum uint64
		for _, v := range values {
			sum += v
		}
		avg = sum / uint64(len(values))
	}
	if !now.Before(st.cooldownUntil) {
		return 0, avg, false
	}
	return st.cooldownUntil.Sub(now), avg, true
}

// refreshSlowPoolSize 重新探测账号总数（启用 + 停用）并写入缓存。
//
// 由独立 ticker 周期调用，不参与请求热路径。探测失败时保留上一次已知值：
// 宿主 RPC 偶发失败不应让准入闸门忽开忽关。
// [参数] 无。
// [返回] 本次探测到的账号数；失败时返回上一次已知值（首次失败返回 0）。
// 最近修改时间：2026-10-25；改动原因：新增，账号池准入闸门的唯一取数入口。
func refreshSlowPoolSize() int {
	size, ok := countHostAuthFiles()
	if !ok {
		slowPoolSizeMu.RLock()
		prev := slowPoolSize
		slowPoolSizeMu.RUnlock()
		return prev
	}
	slowPoolSizeMu.Lock()
	prev := slowPoolSize
	slowPoolSize = size
	slowPoolSizeMu.Unlock()
	// 只在规模变化时留痕：该值 10 分钟刷新一次，每次都打会淹没日志。
	if prev != size {
		log.Printf("[zcode] slow pool size updated: prev=%d now=%d", prev, size)
	}
	return size
}

// currentSlowPoolSize 返回缓存的账号总数；零值表示尚未探测成功。
// [参数] 无。
// [返回] 账号总数。
// 最近修改时间：2026-10-25；改动原因：新增，判定入口读取缓存（热路径只读内存）。
func currentSlowPoolSize() int {
	slowPoolSizeMu.RLock()
	defer slowPoolSizeMu.RUnlock()
	return slowPoolSize
}

// slowPoolAdmitted 判断账号池规模是否达到准入下限。
//
// 零值（尚未探测成功）一律视为不达标：宁可晚一个刷新周期生效，
// 也不能在未知状态下用退化基准误判账号。
// [参数] minPoolSize：账号总数须严格大于该值。
// [返回] true 表示允许慢速判定。
// 最近修改时间：2026-10-25；改动原因：新增，准入闸门判定的唯一实现。
func slowPoolAdmitted(minPoolSize int) bool {
	size := currentSlowPoolSize()
	return size > minPoolSize
}

// slowPoolSizeLoop 周期刷新账号池规模，供准入闸门读取。
//
// 首轮立即探测（不留 10 分钟盲区），之后按 slowPoolSizeRefreshInterval 周期刷新。
// [参数] 无。
// [返回] 无；本函数在独立 goroutine 中常驻。
// 最近修改时间：2026-10-25；改动原因：新增，账号池规模的唯一刷新入口。
func slowPoolSizeLoop() {
	refreshSlowPoolSize()
	ticker := time.NewTicker(slowPoolSizeRefreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		refreshSlowPoolSize()
	}
}

func init() {
	// 账号池规模刷新：慢速冷却的准入闸门需要「启用 + 停用」的账号总数，
	// 而账号枚举是宿主 RPC（跨进程）。放到独立循环里周期探测，判定入口
	// 只读内存缓存，避免每次成功请求都付一次 RPC 往返。
	go slowPoolSizeLoop()
}
