// 本文件校验请求构造与传输层的固定契约。
//
// 工作模式（agent_mode=1）整条管线已随「只做图片与视频」删除，原来围绕它
// 建立的一批用例一并下线；保留的用例覆盖技能管线的 option/ext 形状、网页端
// query 与请求头、设备标识派生，以及网页默认值字段的显式输出。
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// requestTestAccount 构造一份请求构造测试账号（值为假数据）。
// [参数] 无。
// [返回] 账号指针。
// 最近修改时间：2026-11-17 01:02:00；改动原因：工作模式删除，用例改名为请求构造通用夹具。
func requestTestAccount() *StoredAuth {
	return &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "requesttest",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		SVWebID:      testSVWebID,
	}
}

// TestBuildSkillOptionKeepsWebPipeline 校验技能管线（agent_mode=2）的 option 段。
//
// 生视频只能走网页端 skill 管线，这是插件唯一保留的上游管线。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-18 23:30:00；改动原因：图片能力删除，用例改用生视频。
func TestBuildSkillOptionKeepsWebPipeline(t *testing.T) {
	sa := requestTestAccount()
	body := buildCompletionRequest(sa, "生成一段海浪短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", "", 0, false)

	if body.Option.AgentMode != 2 {
		t.Errorf("技能管线 agent_mode 应为 2，实际 %d", body.Option.AgentMode)
	}
	if body.Option.AggregateParams.ModeID != "1" {
		t.Errorf("技能管线 mode_id 应为 \"1\"，实际 %q", body.Option.AggregateParams.ModeID)
	}
	if body.Ext["agent_mode"] != "2" || body.Ext["use_deep_think"] != "0" {
		t.Errorf("技能管线顶层 ext 取值错误：%v", body.Ext)
	}
	if body.Option.AggregateParams.MentionSkillList != "[5]" {
		t.Errorf("生视频 skill 注入错误：%q", body.Option.AggregateParams.MentionSkillList)
	}
}

// TestBuildConfirmOptionMatchesWebShape 校验视频确认轮的请求形态。
//
// 确认轮与首轮不是同一份 body（2026-11-17 抓包逐字段对比），四处硬性差异：
// 多了 chat_ability、agent_mode 键整个消失、aggregate_params 六个路径字段全空、
// client_meta 不带 local_conversation_id。少任何一条都会退回「只回参数确认」。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:36:00；改动原因：新增，锁定视频两段式的第二段形态。
func TestBuildConfirmOptionMatchesWebShape(t *testing.T) {
	sa := requestTestAccount()
	resume := resumePoint{ConversationID: "38454121040780034", SectionID: "38454121040780290", MessageIndex: 5}
	mode := generationMode{Skill: skillVideoGen, Kind: "video"}

	body := buildCompletionRequestWith(sa, "确认，开始生成", mode, resume, false, true)

	// 1. 路径参数必须全空：照首轮填满会被当成新的一轮对话。
	paths := body.Option.AggregateParams
	if paths.ModeID != "" || paths.AgentMode != "" || paths.ModelItemKey != "" {
		t.Errorf("确认轮路径参数必须留空，实际 %+v", paths)
	}
	if paths.ReasoningEffort != "" || paths.ConversationMode != "" {
		t.Errorf("确认轮推理档与会话类型必须留空，实际 %+v", paths)
	}
	if body.Option.ModelConfig.ModelItemKey != "" {
		t.Errorf("确认轮 model_item_key 必须为空，实际 %q", body.Option.ModelConfig.ModelItemKey)
	}

	// 2. agent_mode 只在 option 顶层消失；aggregate_params 内部仍留着空串键。
	// 用 map 判断顶层键，避免被嵌套同名字段误导。
	var optionTop map[string]any
	raw, _ := json.Marshal(body.Option)
	if errUnmarshal := json.Unmarshal(raw, &optionTop); errUnmarshal != nil {
		t.Fatalf("option 不是合法 JSON：%v", errUnmarshal)
	}
	if _, ok := optionTop["agent_mode"]; ok {
		t.Error("确认轮 option 顶层不得出现 agent_mode 键")
	}
	// 3. 三个「仅确认轮出现」的字段必须在。
	for _, key := range []string{"general_task_param", "is_old_user", "message_storage_type"} {
		if _, ok := optionTop[key]; !ok {
			t.Errorf("确认轮 option 缺字段 %s", key)
		}
	}
	// 4. client_meta 不带 local_conversation_id，但续接点必须在。
	if strings.Contains(string(raw), "local_conversation_id") {
		t.Error("option 里不应出现 local_conversation_id")
	}
	metaRaw, _ := json.Marshal(body.ClientMeta)
	if strings.Contains(string(metaRaw), "local_conversation_id") {
		t.Error("确认轮 client_meta 不得带 local_conversation_id")
	}
	if body.ClientMeta.ConversationID != resume.ConversationID || body.ClientMeta.LastSectionID != resume.SectionID {
		t.Errorf("确认轮续接点错误：%+v", body.ClientMeta)
	}
	if body.Option.NeedCreateConversation {
		t.Error("确认轮必须续接既有会话")
	}

	// 5. ext 只有 5 键，且不含首轮标志位。
	if len(body.Ext) != 5 {
		t.Errorf("确认轮 ext 应为 5 键，实际 %d：%v", len(body.Ext), body.Ext)
	}
	if _, ok := body.Ext["agent_mode"]; ok {
		t.Error("确认轮 ext 不得带 agent_mode")
	}
	if body.Ext["answer_with_suggest"] != "0" {
		t.Errorf("确认轮 ext 应带 answer_with_suggest=0，实际 %q", body.Ext["answer_with_suggest"])
	}

	// 6. chat_ability 的 ability_param 是字符串套 JSON。
	if body.ChatAbility == nil || body.ChatAbility.AbilityType != abilityTypeVideoGen {
		t.Fatalf("chat_ability 形态错误：%+v", body.ChatAbility)
	}
	var param map[string]any
	if errUnmarshal := json.Unmarshal([]byte(body.ChatAbility.AbilityParam), &param); errUnmarshal != nil {
		t.Fatalf("ability_param 不是合法 JSON 字符串：%v", errUnmarshal)
	}
	for _, key := range []string{"ratio", "model", "duration", "resolution", "input_box_content"} {
		if _, ok := param[key]; !ok {
			t.Errorf("ability_param 缺 %s：%v", key, param)
		}
	}
	// 上游靠这个格式串回填视频描述，%s 必须是字面量而不是本地格式化。
	inputBox, _ := param["input_box_content"].(map[string]any)
	if inputBox["reply_message_format"] != "生成视频：%s" {
		t.Errorf("reply_message_format 必须是含字面 %%s 的格式串，实际 %v", inputBox["reply_message_format"])
	}
}

// TestNormalizeVideoParams 校验视频参数归一：缺省、合法、越界三态。
//
// 这个函数是「调用方要什么」的唯一收敛点：首轮提示词与确认轮 chat_ability
// 都从它的返回值取值，所以三种边界必须都钉住——否则两段请求会各解释一遍入参。
func TestNormalizeVideoParams(t *testing.T) {
	// 1. 全空入参（调用方没提要求）一律回落缺省。
	if got := normalizeVideoParams(0, "", ""); !got.isDefault() {
		t.Errorf("全空入参应回落缺省，实际 %+v", got)
	}

	// 2. 合法值原样保留，不做任何改写。
	got := normalizeVideoParams(5, "1080p", "9:16")
	want := videoParams{Duration: 5, Resolution: "1080p", Ratio: "9:16"}
	if got != want {
		t.Errorf("合法入参应原样保留，实际 %+v，期望 %+v", got, want)
	}

	// 3. 空白字符串等同于未传，不能变成空串提交给上游。
	if out := normalizeVideoParams(0, "  ", "\t"); !out.isDefault() {
		t.Errorf("空白字符串应回落缺省，实际 %+v", out)
	}

	// 4. 时长闭区间：上下界保留，越界回落。
	if out := normalizeVideoParams(videoDurationMin, "", ""); out.Duration != videoDurationMin {
		t.Errorf("下界 %d 应被接受，实际 %d", videoDurationMin, out.Duration)
	}
	if out := normalizeVideoParams(videoDurationMax, "", ""); out.Duration != videoDurationMax {
		t.Errorf("上界 %d 应被接受，实际 %d", videoDurationMax, out.Duration)
	}
	if out := normalizeVideoParams(videoDurationMax+1, "", ""); out.Duration != defaultVideoDuration {
		t.Errorf("越上界应回落缺省 %d，实际 %d", defaultVideoDuration, out.Duration)
	}
	if out := normalizeVideoParams(-3, "", ""); out.Duration != defaultVideoDuration {
		t.Errorf("负时长应回落缺省 %d，实际 %d", defaultVideoDuration, out.Duration)
	}
}

// TestAppendVideoParamHint 校验首轮提示词只在「视频 + 非缺省参数」时追加要求。
//
// 缺省时必须一字不加：不传参数的老调用方拿到的请求体要与改动前逐字节一致，
// 否则等于给所有存量流量悄悄换了提示词。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:02:00；改动原因：新增，锁住缺省路径不变。
func TestAppendVideoParamHint(t *testing.T) {
	// 1. 非视频能力一律不追加，哪怕参数非缺省。
	// 这里用不带 skill 的空模式作替身：图片能力已删除，但「Kind 不是 video」这条
	// 守卫仍然要守住——将来再多一条能力时它同样靠这行生效。
	plainMode := generationMode{}
	if got := appendVideoParamHint("画一只猫", plainMode, normalizeVideoParams(5, "1080p", "9:16")); got != "画一只猫" {
		t.Errorf("非视频能力不该改提示词，实际 %q", got)
	}

	// 2. 视频但参数缺省：提示词原样保留。
	video := generationMode{Skill: skillVideoGen, Kind: "video"}
	plain := "生成一段海浪拍打礁石的短视频"
	if got := appendVideoParamHint(plain, video, normalizeVideoParams(0, "", "")); got != plain {
		t.Errorf("缺省参数不该改提示词，实际 %q", got)
	}

	// 3. 视频且参数非缺省：三个值都要出现在提示词里。
	got := appendVideoParamHint(plain, video, normalizeVideoParams(5, "1080p", "9:16"))
	for _, want := range []string{"5 秒", "1080p", "9:16"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示词应含 %q，实际 %q", want, got)
		}
	}
	// 用户原话必须完整保留在前，不能被参数说明挤掉。
	if !strings.HasPrefix(got, plain) {
		t.Errorf("提示词必须以用户原话开头，实际 %q", got)
	}
}

// TestBuildConfirmOptionUsesRequestedParams 校验确认轮提交的就是调用方要的那一套。
//
// 0.1.27 的真实偏差：上游对用户承诺 5 秒，确认轮 ability_param 却恒提交 10 秒，
// 因为两段各读一份硬编码常量。这里把「同一份 videoParams 贯穿两段」钉死。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 22:02:00；改动原因：新增，回归 0.1.27 的参数脱钩。
func TestBuildConfirmOptionUsesRequestedParams(t *testing.T) {
	sa := requestTestAccount()
	resume := resumePoint{ConversationID: "38454121040780034", SectionID: "38454121040780290", MessageIndex: 5}
	mode := generationMode{Skill: skillVideoGen, Kind: "video"}
	params := normalizeVideoParams(5, "1080p", "9:16")

	body := buildCompletionRequestWith(sa, "确认，开始生成", mode, resume, false, true, params)
	if body.ChatAbility == nil {
		t.Fatal("确认轮必须带 chat_ability")
	}
	var param map[string]any
	if errUnmarshal := json.Unmarshal([]byte(body.ChatAbility.AbilityParam), &param); errUnmarshal != nil {
		t.Fatalf("ability_param 不是合法 JSON 字符串：%v", errUnmarshal)
	}
	if duration, _ := param["duration"].(float64); int(duration) != 5 {
		t.Errorf("ability_param.duration 应为 5，实际 %v", param["duration"])
	}
	if param["resolution"] != "1080p" {
		t.Errorf("ability_param.resolution 应为 1080p，实际 %v", param["resolution"])
	}
	if param["ratio"] != "9:16" {
		t.Errorf("ability_param.ratio 应为 9:16，实际 %v", param["ratio"])
	}

	// 不传参数时回落缺省，与 0.1.27 完全一致。
	fallback := buildCompletionRequestWith(sa, "确认，开始生成", mode, resume, false, true)
	var plain map[string]any
	if errUnmarshal := json.Unmarshal([]byte(fallback.ChatAbility.AbilityParam), &plain); errUnmarshal != nil {
		t.Fatalf("默认 ability_param 不是合法 JSON：%v", errUnmarshal)
	}
	if duration, _ := plain["duration"].(float64); int(duration) != defaultVideoDuration {
		t.Errorf("不传参数时 duration 应为 %d，实际 %v", defaultVideoDuration, plain["duration"])
	}
	if plain["resolution"] != defaultVideoResolution || plain["ratio"] != defaultVideoRatio {
		t.Errorf("不传参数时应回落缺省清晰度与比例，实际 %v / %v", plain["resolution"], plain["ratio"])
	}
}

// TestBuildSkillOptionOmitsConfirmOnlyFields 校验首轮不夹带确认轮专属字段。
//
// 首轮带上 chat_ability 会直接变成「真开跑」，用户连参数都没看到就开始花额度；
// general_task_param / is_old_user / message_storage_type 同理只属于确认轮。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:36:00；改动原因：新增，防止两轮形态互相污染。
func TestBuildSkillOptionOmitsConfirmOnlyFields(t *testing.T) {
	sa := requestTestAccount()
	body := buildCompletionRequest(sa, "帮我生成一段海浪拍打礁石的短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", "", 0, true)

	if body.ChatAbility != nil {
		t.Error("首轮不得带 chat_ability：带上就会立刻开跑，跳过参数确认与额度确认")
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "chat_ability") {
		t.Error("首轮序列化后不得出现 chat_ability")
	}
	if body.Option.GeneralTaskParam != nil || body.Option.IsOldUser != nil || body.Option.MessageStorageType != nil {
		t.Errorf("首轮不得带确认轮专属 option 字段：%+v", body.Option)
	}
	if body.Ext["agent_mode"] != "2" {
		t.Errorf("首轮 ext 必须保留 agent_mode=2，实际 %v", body.Ext)
	}
	if body.ClientMeta.LocalConversationID == "" {
		t.Error("首轮 client_meta 必须带 local_conversation_id（缺失会被 710020202 拒）")
	}
}

// TestLocalPermissionsWebStatus 校验本地权限声明取值。
//
// 浏览器在网页端下发的三条权限全部是 status=3。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:02:00；改动原因：删除工作模式分支，改为单次调用。
func TestLocalPermissionsWebStatus(t *testing.T) {
	perms := localPermissionsFor()
	if len(perms) == 0 {
		t.Fatal("本地权限声明不能为空")
	}
	for _, p := range perms {
		if p.Status != 3 {
			t.Errorf("权限 %s 状态应为 3，实际 %d", p.PermissionName, p.Status)
		}
	}
}

// TestBusinessQueryUsesWebParams 校验 query 使用网页端参数集。
//
// 2026-11-16 实测：浏览器下发的就是 aid=497858 / doubao_device_platform=web /
// web_platform=browser 这套参数；换成客户端参数集（aid=582478、channel=win）
// 会与请求体形状矛盾，上游直接回 -1 delegated input protocol violation。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:02:00；改动原因：两条管线合并，改为单管线直接断言参数集。
func TestBusinessQueryUsesWebParams(t *testing.T) {
	sa := requestTestAccount()
	const sampleBody = `{"messages":[{"content_block":[{"block_type":10000}]}]}`
	rawURL := completionURL(sa, []byte(sampleBody))
	values, errParse := url.ParseQuery(strings.SplitN(rawURL, "?", 2)[1])
	if errParse != nil {
		t.Fatalf("query 应为合法编码：%v", errParse)
	}

	want := map[string]string{
		"aid":                    doubaoAID,
		"real_aid":               doubaoAID,
		"device_platform":        "web",
		"doubao_device_platform": "web",
		"web_platform":           "browser",
		"pc_version":             doubaoPCVersion,
		"device_id":              sa.DeviceID,
		"tea_uuid":               sa.WebID,
		"web_id":                 sa.WebID,
		"fp":                     testSVWebID,
	}
	for key, expect := range want {
		if got := values.Get(key); got != expect {
			t.Errorf("query %s 应为 %q，实际 %q", key, expect, got)
		}
	}
	// 客户端专属键一旦出现，说明又走错参数集了。
	for _, key := range []string{"channel", "client_platform", "chromium_version", "runtime_version"} {
		if got := values.Get(key); got != "" {
			t.Errorf("网页端 query 不应出现客户端键 %s=%q", key, got)
		}
	}
	if values.Get("a_bogus") == "" {
		t.Error("URL 必须带 a_bogus 签名")
	}
}

// TestWebQueryRotatesTabID 校验 web_tab_id 每次请求都换新。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:54:00；改动原因：按需传非空 web_tab_id 才能触发换新逻辑。
func TestWebQueryRotatesTabID(t *testing.T) {
	first, _ := url.ParseQuery(buildBusinessQuery("7300000000000000001", "7300000000000000002", randomUUID(), testSVWebID, nil))
	second, _ := url.ParseQuery(buildBusinessQuery("7300000000000000001", "7300000000000000002", randomUUID(), testSVWebID, nil))
	if first.Get("web_tab_id") == second.Get("web_tab_id") {
		t.Errorf("web_tab_id 每次请求必须换新 UUID，实际两次都是 %q", first.Get("web_tab_id"))
	}
}

// TestUpstreamHeadersUseWebFingerprint 校验请求头按网页端上报。
//
// 客户端风控头（agw-js-conv 双值 / last-event-id / x-flow-trace / 客户端 UA）与
// 网页形状的请求体矛盾，会让上游按桌面客户端协议解析并回 -1。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:02:00；改动原因：删除工作模式入参，改为单次调用。
func TestUpstreamHeadersUseWebFingerprint(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, doubaoSiteHost+"/chat/completion", nil)
	applyUpstreamHeaders(req)

	if got := req.Header.Get("User-Agent"); got != webUserAgent {
		t.Errorf("UA 应为浏览器 UA，实际 %q", got)
	}
	if got := req.Header.Get("Agw-Js-Conv"); got != "str" {
		t.Errorf("Agw-Js-Conv 应为 %q（客户端双值写法会被判协议不符），实际 %q", "str", got)
	}
	if got := req.Header.Get("Origin"); got != doubaoSiteHost {
		t.Errorf("Origin 应为 %q，实际 %q", doubaoSiteHost, got)
	}
	if got := req.Header.Get("Referer"); got != doubaoSiteHost+"/chat/" {
		t.Errorf("Referer 应为 %q，实际 %q", doubaoSiteHost+"/chat/", got)
	}
	for _, key := range []string{"last-event-id", "x-flow-trace"} {
		if got := req.Header.Get(key); got != "" {
			t.Errorf("不应带客户端风控头 %s，实际 %q", key, got)
		}
	}
}

// TestDeviceIDForIsStable 校验设备标识派生稳定且补齐 web_id。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，设备标识派生回归。
func TestDeviceIDForIsStable(t *testing.T) {
	first := &StoredAuth{AuthID: doubaoAuthFilePrefix + "dev", CookieHeader: validCookieHeader}
	second := &StoredAuth{AuthID: doubaoAuthFilePrefix + "dev", CookieHeader: validCookieHeader}
	idFirst := deviceIDFor(first)
	idSecond := deviceIDFor(second)
	if idFirst != idSecond {
		t.Fatalf("同一账号两次派生必须一致，实际 %q vs %q", idFirst, idSecond)
	}
	if !strings.HasPrefix(idFirst, "73") || len(idFirst) != 18 {
		t.Errorf("设备号应为 73 前缀的 18 位数字串，实际 %q", idFirst)
	}
	if first.WebID != first.DeviceID {
		t.Errorf("web_id 缺失时应与设备号同源，实际 %q vs %q", first.WebID, first.DeviceID)
	}
	present := &StoredAuth{AuthID: doubaoAuthFilePrefix + "dev2", CookieHeader: validCookieHeader, DeviceID: "7300000000000000009"}
	if got := deviceIDFor(present); got != "7300000000000000009" {
		t.Errorf("已有设备号不应被改写，实际 %q", got)
	}
}

// TestEndpointURLCarriesRealFingerprint 校验 URL 带上账号真实 fp。
//
// 这是 0.1.21 的核心回归点：fp 一旦退回 "verify_"+deviceID 的假值，请求会被
// 上游按陌生设备判风控，稳定回 710022004 / 710022002（见 fingerprintOf 注释）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:02:00；改动原因：两条管线合并，改为单次调用。
func TestEndpointURLCarriesRealFingerprint(t *testing.T) {
	sa := requestTestAccount()

	got := completionURL(sa, []byte("{}"))
	if !strings.Contains(got, "fp="+url.QueryEscape(testSVWebID)) {
		t.Errorf("URL 必须带账号真实 fp，实际 %s", got)
	}
	if strings.Contains(got, "verify_"+sa.DeviceID) {
		t.Errorf("URL 不得出现由 device_id 拼出的假 fp，实际 %s", got)
	}

	// 老账号没有采集过 fp：应省略该参数，而不是发一个空值。
	bare := &StoredAuth{AuthID: doubaoAuthFilePrefix + "nofp", CookieHeader: validCookieHeader, DeviceID: "7300000000000000003"}
	if got := endpointURL(bare, "/chat/completion", nil); strings.Contains(got, "fp=") {
		t.Errorf("未采集 fp 时不应出现 fp 参数，实际 %s", got)
	}
}

// TestFingerprintOfReadsAccountField 校验 fp 只从账号字段读，不做任何派生。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-15 22:34:00；改动原因：新增，设备指纹取值口径回归。
func TestFingerprintOfReadsAccountField(t *testing.T) {
	if got := fingerprintOf(nil); got != "" {
		t.Errorf("空账号应返回空串，实际 %q", got)
	}
	if got := fingerprintOf(&StoredAuth{DeviceID: "7300000000000000001"}); got != "" {
		t.Errorf("未采集时应返回空串，不能拿 device_id 顶替，实际 %q", got)
	}
	if got := fingerprintOf(&StoredAuth{SVWebID: "  " + testSVWebID + "  "}); got != testSVWebID {
		t.Errorf("应返回去空白后的账号值，实际 %q", got)
	}
}

// TestSkillOptionCarriesWebDefaults 校验网页端默认值字段必须显式输出。
//
// 上游对「委托输入协议」做结构校验：网页每次都会把这些键显式发出去，
// 用 omitempty 省略后上游直接回 -1 delegated input protocol violation。
// 2026-11-16 消融：补齐这些默认值后，错误码从 -1 变成 710022004（人机验证），
// 说明它们是协议判据而不是可选装饰。这批字段由技能管线独占。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-17 01:02:00；改动原因：工作模式删除，用例迁到技能管线。
func TestSkillOptionCarriesWebDefaults(t *testing.T) {
	sa := requestTestAccount()
	body := buildCompletionRequest(sa, "生成一段海浪短视频", generationMode{Skill: skillVideoGen, Kind: "video"}, "", "", 0, true)
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("序列化失败：%v", errMarshal)
	}
	decoded := map[string]any{}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("反序列化失败：%v", errUnmarshal)
	}

	option, ok := decoded["option"].(map[string]any)
	if !ok {
		t.Fatal("option 段缺失")
	}
	// 1. 布尔/数字/字符串默认值必须全部出现。
	for _, key := range []string{
		"click_clear_context", "disable_sse_cache", "from_suggest", "is_ai_playground",
		"is_from_click_option", "is_from_click_softlink", "is_regen", "is_replace",
		"is_select_text", "no_replace_for_regen", "resend_for_regen",
		"message_from", "scene_type", "regen_instruction", "select_text_action",
		"shared_app_id", "shared_app_name",
	} {
		if _, exists := option[key]; !exists {
			t.Errorf("option 缺少网页默认值字段 %s（省略会被上游判成委托输入协议不符）", key)
		}
	}
	// 2. 数组型默认值必须是 []，不能是字符串 "[]"（写成字符串回 710020202）。
	for _, key := range []string{"connector_info_list", "edit_query_id", "regen_query_id"} {
		arr, isSlice := option[key].([]any)
		if !isSlice {
			t.Errorf("option.%s 必须是 JSON 数组，实际 %T=%v", key, option[key], option[key])
			continue
		}
		if len(arr) != 0 {
			t.Errorf("option.%s 应为空数组，实际 %v", key, arr)
		}
	}
	// 3. related_deleted_message_ids 是对象 {}，不是空数组。
	rel, isObject := option["related_deleted_message_ids"].(map[string]any)
	if !isObject {
		t.Errorf("option.related_deleted_message_ids 必须是 JSON 对象，实际 %T=%v", option["related_deleted_message_ids"], option["related_deleted_message_ids"])
	} else if len(rel) != 0 {
		t.Errorf("option.related_deleted_message_ids 应为空对象，实际 %v", rel)
	}
	// 4. recovery_option 必须带当前秒级时间戳。
	recovery, isObject := option["recovery_option"].(map[string]any)
	if !isObject {
		t.Fatalf("option.recovery_option 缺失")
	}
	if sec, isNumber := recovery["req_create_time_sec"].(float64); !isNumber || int64(sec) < 1000000000 {
		t.Errorf("recovery_option.req_create_time_sec 必须是当前秒级时间戳，实际 %T=%v", recovery["req_create_time_sec"], recovery["req_create_time_sec"])
	}

	// 5. client_meta 的续接点必须显式输出（新会话为 "" 与 null）。
	meta, ok := decoded["client_meta"].(map[string]any)
	if !ok {
		t.Fatal("client_meta 段缺失")
	}
	if _, exists := meta["last_section_id"]; !exists {
		t.Error("client_meta.last_section_id 必须显式输出（新会话为空串）")
	}
	if val, exists := meta["last_message_index"]; !exists {
		t.Error("client_meta.last_message_index 必须显式输出（新会话为 null）")
	} else if val != nil {
		t.Errorf("新会话的 client_meta.last_message_index 应为 null，实际 %v", val)
	}
}
