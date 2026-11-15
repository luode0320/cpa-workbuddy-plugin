package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// workTestAccount 构造一份工作模式测试账号（值为假数据）。
// [参数] 无。
// [返回] 账号指针。
// 最近修改时间：2026-11-15 22:32:00；改动原因：补设备指纹，覆盖 fp 透传路径。
func workTestAccount() *StoredAuth {
	return &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "worktest",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
		SVWebID:      testSVWebID,
	}
}

// TestBuildWorkOptionContract 校验工作模式 option 段符合客户端协议。
//
// 抽取六个会静默降级的字段：agent_mode、need_deep_think、conversation_mode、
// aggregate_params.agent_mode、conversation_init_option、general_task_param。
// 任一缺失上游都照常返回 HTTP 200，只是按普通网页端对话处理。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，锁定工作模式请求契约。
func TestBuildWorkOptionContract(t *testing.T) {
	sa := workTestAccount()
	body := buildCompletionRequest(sa, "整理这份纪要", modeForModel("doubao/deepseek-v4-flash"), "", "", 0, true)

	if body.Option.AgentMode != 1 {
		t.Fatalf("工作模式 agent_mode 必须为 1，实际 %d", body.Option.AgentMode)
	}
	if body.Option.NeedDeepThink != needDeepThinkOn {
		t.Errorf("need_deep_think 应为 %d，实际 %d", needDeepThinkOn, body.Option.NeedDeepThink)
	}
	if body.Option.ConversationMode != workConversationMode {
		t.Errorf("顶层 conversation_mode 必须为整数 %d（工作时间，写成字符串会被上游 710020202 拒绝），实际 %d",
			workConversationMode, body.Option.ConversationMode)
	}
	if body.Option.AggregateParams.AgentMode != "1" {
		t.Errorf("aggregate_params.agent_mode 应为 \"1\"，实际 %q", body.Option.AggregateParams.AgentMode)
	}
	if body.Option.AggregateParams.ConversationMode != "2" {
		t.Errorf("aggregate_params.conversation_mode 应为 \"2\"，实际 %q", body.Option.AggregateParams.ConversationMode)
	}
	if body.Option.AggregateParams.ModeID != defaultWorkModeID {
		t.Errorf("mode_id 应为 %q（工作任务），实际 %q", defaultWorkModeID, body.Option.AggregateParams.ModeID)
	}
	if body.Option.ConversationInitOption == nil || !body.Option.ConversationInitOption.NeedAckConversation {
		t.Error("conversation_init_option.need_ack_conversation 必须为 true，否则上游不建会话")
	}
	// 工作模式的扩展必须写在顶层 ext：浏览器在网页「工作」标签下发请求时就是这么发的，
	// 写进 option.ext 会被上游按未知字段忽略，工作模式字段随之静默失效。
	if body.Option.GeneralTaskParam == nil {
		t.Fatal("工作模式必须带 general_task_param")
	}
	if !body.Option.IsOldUser {
		t.Error("is_old_user 必须为 true")
	}

	wantExtKeys := []string{
		"general_task_param",
		"use_deep_think",
		"agent_mode",
		"conversation_init_option",
		"sub_conv_firstmet_type",
		"collection_id",
		"is_finish",
		"commerce_credit_config_enable",
	}
	for _, key := range wantExtKeys {
		if _, ok := body.Ext[key]; !ok {
			t.Errorf("顶层 ext 缺少键 %s", key)
		}
	}
	if len(body.Ext) != len(wantExtKeys) {
		t.Errorf("顶层 ext 应恰好 %d 键，实际 %d 键：%v", len(wantExtKeys), len(body.Ext), body.Ext)
	}
	if body.Ext["agent_mode"] != "1" || body.Ext["use_deep_think"] != "10001" {
		t.Errorf("顶层 ext 取值错误：%v", body.Ext)
	}
	if body.Ext["sub_conv_firstmet_type"] != "1" {
		t.Errorf("sub_conv_firstmet_type 应为 \"1\"，实际 %q", body.Ext["sub_conv_firstmet_type"])
	}
	// ext 里的 general_task_param 必须与本轮 general_task_param 同源，否则上游按
	// 两套任务参数取其一，容易出现「消息发出去了但没进 agent 管线」。
	if !strings.Contains(body.Ext["general_task_param"], "server_do_compare") {
		t.Errorf("ext.general_task_param 必须是序列化后的任务参数，实际 %q", body.Ext["general_task_param"])
	}
}

// TestBuildWorkOptionModelKeyConsistent 校验 model_item_key 三处一致。
//
// conversation_init_ext / aggregate_params / model_config 各带一份 model_item_key，
// 上游取不一致会被拒或按错误的模型计费。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，主键一致性回归。
func TestBuildWorkOptionModelKeyConsistent(t *testing.T) {
	sa := workTestAccount()
	body := buildCompletionRequest(sa, "你好", modeForModel("doubao/kimi-k3"), "", "", 0, true)

	option := body.Option
	if option.ConversationInitExt == nil {
		t.Fatal("工作模式必须带 conversation_init_ext")
	}
	keys := []string{
		option.ConversationInitExt.ModelItemKey,
		option.AggregateParams.ModelItemKey,
		option.ModelConfig.ModelItemKey,
	}
	for _, key := range keys {
		if key == "" {
			t.Fatal("model_item_key 不能为空")
		}
		if key != keys[0] {
			t.Fatalf("model_item_key 三处必须一致，实际 %v", keys)
		}
	}
	if !strings.Contains(option.ConversationInitExt.ModelItemKey, "kimi-k3") {
		t.Errorf("kimi-k3 应解析到对应上游主键，实际 %q", option.ConversationInitExt.ModelItemKey)
	}
	if !strings.Contains(option.ConversationInitExt.AgentTaskParam, "local_device_id") {
		t.Errorf("init_ext 的 agent_task_param 必须是序列化 JSON，实际 %q", option.ConversationInitExt.AgentTaskParam)
	}
	// 网页来源的工作模式只上报空的设备字段：填上本机主机名与客户端 aid 会被
	// 上游按桌面客户端协议解析，直接回 -1 delegated input protocol violation。
	if !strings.Contains(option.ConversationInitExt.AgentTaskParam, `"runtime_type":1`) {
		t.Errorf("init_ext 的 runtime_type 必须为 1（网页来源），实际 %q", option.ConversationInitExt.AgentTaskParam)
	}
	if strings.Contains(option.ConversationInitExt.AgentTaskParam, "sandbox_auth_type") ||
		strings.Contains(option.ConversationInitExt.AgentTaskParam, "local_app_id\":\"5") {
		t.Errorf("init_ext 不得带客户端沙箱字段或非空 local_app_id，实际 %q", option.ConversationInitExt.AgentTaskParam)
	}
	if !option.GeneralTaskParam.ServerDoCompare {
		t.Error("general_task_param.server_do_compare 必须为 true（网页来源固定上报）")
	}
	if option.GeneralTaskParam.RuntimeType != 1 {
		t.Errorf("general_task_param.runtime_type 必须为 1，实际 %d", option.GeneralTaskParam.RuntimeType)
	}
	if option.GeneralTaskParam.ProjectID != "" {
		t.Errorf("general_task_param.project_id 应为空串，实际 %q", option.GeneralTaskParam.ProjectID)
	}
	if len(option.GeneralTaskParam.ThreadLocalMessageID) != 1 {
		t.Fatalf("thread_local_message_id 应回指本轮消息，实际 %v", option.GeneralTaskParam.ThreadLocalMessageID)
	}
	if option.GeneralTaskParam.ThreadLocalMessageID[0] != body.Messages[0].LocalMessageID {
		t.Errorf("thread_local_message_id 必须等于 local_message_id，实际 %v vs %q", option.GeneralTaskParam.ThreadLocalMessageID, body.Messages[0].LocalMessageID)
	}
}

// TestBuildSkillOptionKeepsWebPipeline 校验技能模式（agent_mode=2）未被改动。
//
// 生图/生视频只能走网页端 skill 管线，改造工作模式时不得连带改坏它。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，技能管线回归。
func TestBuildSkillOptionKeepsWebPipeline(t *testing.T) {
	sa := workTestAccount()
	body := buildCompletionRequest(sa, "画一只猫", generationMode{Skill: skillImageGen, Kind: "image"}, "", "", 0, false)

	if body.Option.AgentMode != 2 {
		t.Errorf("技能管线 agent_mode 应为 2，实际 %d", body.Option.AgentMode)
	}
	if body.Option.AggregateParams.ModeID != "1" {
		t.Errorf("技能管线 mode_id 应为 \"1\"，实际 %q", body.Option.AggregateParams.ModeID)
	}
	if body.Option.GeneralTaskParam != nil || body.Option.ConversationInitExt != nil {
		t.Error("技能管线不得带工作模式字段")
	}
	if body.Ext["agent_mode"] != "2" || body.Ext["use_deep_think"] != "0" {
		t.Errorf("技能管线顶层 ext 取值错误：%v", body.Ext)
	}
	if body.Option.AggregateParams.MentionSkillList != "[3]" {
		t.Errorf("生图 skill 注入错误：%q", body.Option.AggregateParams.MentionSkillList)
	}
}

// TestLocalPermissionsAlwaysWebStatus 校验两条管线的本地权限声明取值一致。
//
// 浏览器在网页「工作」标签下发的三条权限全部是 status=3；旧实现给工作模式报 1，
// 等于向上游声明「这是桌面客户端」，与请求体形状矛盾，实测直接回 -1
// delegated input protocol violation（2026-11-16）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:50:00；改动原因：工作模式改为网页来源，权限状态随之统一为 3。
func TestLocalPermissionsAlwaysWebStatus(t *testing.T) {
	for _, workMode := range []bool{true, false} {
		perms := localPermissionsFor(workMode)
		if len(perms) == 0 {
			t.Fatalf("本地权限声明不能为空（workMode=%v）", workMode)
		}
		for _, p := range perms {
			if p.Status != 3 {
				t.Errorf("workMode=%v 的权限 %s 状态应为 3，实际 %d", workMode, p.PermissionName, p.Status)
			}
		}
	}
}

// TestBothPipelinesShareWebQuery 校验两条管线使用同一套网页端 query 参数集。
//
// 2026-11-16 实测：浏览器在网页「工作」标签下发的就是 aid=497858 /
// doubao_device_platform=web / web_platform=browser 这套参数；换成客户端参数集
// （aid=582478、channel=win）会与请求体形状矛盾，上游直接回 -1
// delegated input protocol violation。因此两条管线的 URL 只应在签名后不同。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:51:00；改动原因：取消客户端参数集，改为同形回归。
func TestBothPipelinesShareWebQuery(t *testing.T) {
	sa := workTestAccount()
	const sampleBody = `{"messages":[{"content_block":[{"block_type":10000}]}]}`
	workURL := completionURL(sa, true, []byte(sampleBody))
	webURL := completionURL(sa, false, []byte(sampleBody))
	if workURL == webURL {
		t.Fatalf("两次调用必须各自独立签名（a_bogus 带时间戳），实际完全相同：%s", workURL)
	}

	// 比较口径：去掉签名参数后两条管线必须一字不差。
	workValues, errWork := url.ParseQuery(strings.SplitN(workURL, "?", 2)[1])
	if errWork != nil {
		t.Fatalf("工作模式 query 应为合法编码：%v", errWork)
	}
	webValues, errWeb := url.ParseQuery(strings.SplitN(webURL, "?", 2)[1])
	if errWeb != nil {
		t.Fatalf("网页模式 query 应为合法编码：%v", errWeb)
	}
	workValues.Del("a_bogus")
	webValues.Del("a_bogus")
	workValues.Del("web_tab_id")
	webValues.Del("web_tab_id")
	if workValues.Encode() != webValues.Encode() {
		t.Fatalf("两条管线除签名与 web_tab_id 外必须同形，实际\n工作=%s\n网页=%s", workValues.Encode(), webValues.Encode())
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
		if got := workValues.Get(key); got != expect {
			t.Errorf("query %s 应为 %q，实际 %q", key, expect, got)
		}
	}
	// 客户端专属键一旦回来，说明又走错管线了。
	for _, key := range []string{"channel", "client_platform", "chromium_version", "runtime_version"} {
		if workValues.Get(key) != "" {
			t.Errorf("网页端 query 不应出现客户端键 %s=%q", key, workValues.Get(key))
		}
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

// TestUpstreamHeadersUseWebFingerprint 校验两条管线都按网页端上报请求头。
//
// 客户端风控头（agw-js-conv 双值 / last-event-id / x-flow-trace / 客户端 UA）与
// 网页形状的请求体矛盾，会让上游按桌面客户端协议解析并回 -1。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:51:00；改动原因：取消客户端指纹头，改为同形回归。
func TestUpstreamHeadersUseWebFingerprint(t *testing.T) {
	for _, workMode := range []bool{true, false} {
		req, _ := http.NewRequest(http.MethodPost, doubaoSiteHost+"/chat/completion", nil)
		applyUpstreamHeaders(req, workMode)

		if got := req.Header.Get("User-Agent"); got != webUserAgent {
			t.Errorf("workMode=%v 的 UA 应为浏览器 UA，实际 %q", workMode, got)
		}
		if got := req.Header.Get("Agw-Js-Conv"); got != "str" {
			t.Errorf("workMode=%v 的 Agw-Js-Conv 应为 %q（客户端双值写法会被判协议不符），实际 %q", workMode, "str", got)
		}
		if got := req.Header.Get("Origin"); got != doubaoSiteHost {
			t.Errorf("workMode=%v 的 Origin 应为 %q，实际 %q", workMode, doubaoSiteHost, got)
		}
		if got := req.Header.Get("Referer"); got != doubaoSiteHost+"/chat/" {
			t.Errorf("workMode=%v 的 Referer 应为 %q，实际 %q", workMode, doubaoSiteHost+"/chat/", got)
		}
		for _, key := range []string{"last-event-id", "x-flow-trace"} {
			if got := req.Header.Get(key); got != "" {
				t.Errorf("workMode=%v 不应带客户端风控头 %s，实际 %q", workMode, key, got)
			}
		}
	}
}

// TestAtoiSafe 校验推理档位字符串解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，档位解析回归。
func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"0": 0, "4": 4, " 4 ": 4, "4档": 4, "": 0, "abc": 0, "12x": 12}
	for in, want := range cases {
		if got := atoiSafe(in); got != want {
			t.Errorf("atoiSafe(%q) 应为 %d，实际 %d", in, want, got)
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

// TestEndpointURLCarriesRealFingerprint 校验两条管线的 URL 都带上账号真实 fp。
//
// 这是 0.1.21 的核心回归点：fp 一旦退回 "verify_"+deviceID 的假值，请求会被
// 上游按陌生设备判风控，稳定回 710022004 / 710022002（见 fingerprintOf 注释）。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:52:00；改动原因：客户端参数集删除，改为统一断言网页端 URL。
func TestEndpointURLCarriesRealFingerprint(t *testing.T) {
	sa := workTestAccount()

	for _, workMode := range []bool{true, false} {
		got := completionURL(sa, workMode, []byte("{}"))
		if !strings.Contains(got, "fp="+url.QueryEscape(testSVWebID)) {
			t.Errorf("workMode=%v 的 URL 必须带账号真实 fp，实际 %s", workMode, got)
		}
		if strings.Contains(got, "verify_"+sa.DeviceID) {
			t.Errorf("workMode=%v 的 URL 不得出现由 device_id 拼出的假 fp，实际 %s", workMode, got)
		}
	}

	// 老账号没有采集过 fp：两条管线都应省略该参数，而不是发一个空值。
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

// TestWorkOptionJSONShape 校验序列化后的键路径。
//
// 只看结构体字段容易漏掉 json tag 写错：上游按"路径 + 键名"取值，
// 顶层 ext 与 option.ext 同名不同层级，串了就整段失效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 01:53:00；改动原因：工作模式改为网页形状，断言改为锁网页任务参数。
func TestWorkOptionJSONShape(t *testing.T) {
	sa := workTestAccount()
	body := buildCompletionRequest(sa, "你好", modeForModel("doubao/deepseek-v4-flash"), "", "", 0, true)
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("序列化失败：%v", errMarshal)
	}
	decoded := map[string]any{}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("反序列化失败：%v", errUnmarshal)
	}
	if _, hasTopExt := decoded["ext"]; !hasTopExt {
		t.Error("工作模式必须输出顶层 ext：浏览器在网页「工作」标签下发请求就是这么发的")
	}
	option, ok := decoded["option"].(map[string]any)
	if !ok {
		t.Fatal("option 段缺失")
	}
	for _, key := range []string{"general_task_param", "conversation_init_ext", "conversation_init_option", "agent_mode"} {
		if _, exists := option[key]; !exists {
			t.Errorf("option 段缺少键 %s", key)
		}
	}
	// 内层 ext 是客户端的写法；网页来源只有顶层 ext，内层出现即说明两条路径混用了。
	if _, exists := option["ext"]; exists {
		t.Error("option 段不得再带内层 ext（真实网页只发顶层 ext）")
	}

	taskParam, ok := option["general_task_param"].(map[string]any)
	if !ok {
		t.Fatal("general_task_param 缺失")
	}
	if rt, isNumber := taskParam["runtime_type"].(float64); !isNumber || int(rt) != 1 {
		t.Errorf("general_task_param.runtime_type 必须序列化为数字 1，实际 %T=%v", taskParam["runtime_type"], taskParam["runtime_type"])
	}
	if sdc, _ := taskParam["server_do_compare"].(bool); !sdc {
		t.Error("general_task_param.server_do_compare 必须为 true")
	}
	for _, key := range []string{"client_option", "task_input_json", "need_modify_conversation", "agent_task_param_change"} {
		if _, exists := taskParam[key]; exists {
			t.Errorf("general_task_param 不得带客户端字段 %s", key)
		}
	}
	agentTask, ok := taskParam["agent_task_param"].(map[string]any)
	if !ok {
		t.Fatal("general_task_param.agent_task_param 缺失")
	}
	for _, key := range []string{"device_name", "workspace", "local_app_id", "local_device_id", "folder_name"} {
		if got, _ := agentTask[key].(string); got != "" {
			t.Errorf("agent_task_param.%s 必须为空串（网页来源不上报客户端设备），实际 %q", key, got)
		}
	}

	// 同名不同型陷阱：顶层 conversation_mode 必须是 JSON 数字，aggregate_params
	// 里的同名字段必须是 JSON 字符串；任一写反上游都回 710020202。
	if cm, isNumber := option["conversation_mode"].(float64); !isNumber {
		t.Errorf("option.conversation_mode 必须序列化为 JSON 数字，实际 %T=%v", option["conversation_mode"], option["conversation_mode"])
	} else if int(cm) != workConversationMode {
		t.Errorf("option.conversation_mode 应为 %d，实际 %v", workConversationMode, cm)
	}
	aggParams, ok := option["aggregate_params"].(map[string]any)
	if !ok {
		t.Fatal("option.aggregate_params 缺失")
	}
	if aggConv, isString := aggParams["conversation_mode"].(string); !isString {
		t.Errorf("aggregate_params.conversation_mode 必须序列化为 JSON 字符串，实际 %T=%v", aggParams["conversation_mode"], aggParams["conversation_mode"])
	} else if aggConv != strconv.Itoa(workConversationMode) {
		t.Errorf("aggregate_params.conversation_mode 应为 %q，实际 %q", strconv.Itoa(workConversationMode), aggConv)
	}
}

// TestWorkOptionCarriesWebDefaults 校验网页端默认值字段必须显式输出。
//
// 上游对「委托输入协议」做结构校验：网页每次都会把这些键显式发出去，
// 用 omitempty 省略后上游直接回 -1 delegated input protocol violation。
// 2026-11-16 消融：只给插件原生 body 补上这些默认值，错误码就从 -1 变成
// 710022004，说明它们是 -1 的判据而不是可选装饰。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-16 04:50:00；改动原因：新增，网页默认值字段回归。
func TestWorkOptionCarriesWebDefaults(t *testing.T) {
	sa := workTestAccount()
	body := buildCompletionRequest(sa, "你好", modeForModel("doubao/deepseek-v4-flash"), "", "", 0, true)
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
