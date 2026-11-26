package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// workTestAccount 构造一份工作模式测试账号（值为假数据）。
// [参数] 无。
// [返回] 账号指针。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复工作模式测试账号装配。
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

// workOptionOf 取出工作模式请求体里的 general_task_param 对象。
// [参数] t: 测试上下文；option: 反序列化后的 option 段。
// [返回] 任务参数对象。
// 最近修改时间：2026-11-27 02:35:00；改动原因：新增，统一 any 字段的类型断言。
func workOptionOf(t *testing.T, option map[string]any) map[string]any {
	t.Helper()
	taskParam, ok := option["general_task_param"].(map[string]any)
	if !ok {
		t.Fatal("general_task_param 缺失或不是对象")
	}
	return taskParam
}

// TestBuildWorkOptionContract 校验工作模式 option 段符合网页协议。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复工作模式请求契约测试。
func TestBuildWorkOptionContract(t *testing.T) {
	sa := workTestAccount()
	mode, ok := modeForModel("doubao/deepseek-v4-flash")
	if !ok {
		t.Fatal("doubao/deepseek-v4-flash 应可解析")
	}
	body := buildCompletionRequest(sa, "整理这份纪要", mode, "", "", 0, true)

	if body.Option.AgentMode != 1 {
		t.Fatalf("工作模式 agent_mode 必须为 1，实际 %d", body.Option.AgentMode)
	}
	if body.Option.NeedDeepThink != needDeepThinkOn {
		t.Errorf("need_deep_think 应为 %d，实际 %d", needDeepThinkOn, body.Option.NeedDeepThink)
	}
	if body.Option.ConversationMode != workConversationMode {
		t.Errorf("顶层 conversation_mode 必须为整数 %d，实际 %d", workConversationMode, body.Option.ConversationMode)
	}
	if body.Option.AggregateParams.AgentMode != "1" {
		t.Errorf("aggregate_params.agent_mode 应为 \"1\"，实际 %q", body.Option.AggregateParams.AgentMode)
	}
	if body.Option.AggregateParams.ConversationMode != "2" {
		t.Errorf("aggregate_params.conversation_mode 应为 \"2\"，实际 %q", body.Option.AggregateParams.ConversationMode)
	}
	if body.Option.AggregateParams.ModeID != defaultWorkModeID {
		t.Errorf("mode_id 应为 %q，实际 %q", defaultWorkModeID, body.Option.AggregateParams.ModeID)
	}
	if body.Option.ConversationInitOption == nil || !body.Option.ConversationInitOption.NeedAckConversation {
		t.Error("conversation_init_option.need_ack_conversation 必须为 true，否则上游不建会话")
	}
	if body.Option.GeneralTaskParam == nil {
		t.Fatal("工作模式必须带 general_task_param")
	}
	if body.Option.IsOldUser == nil || !*body.Option.IsOldUser {
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
		if _, exists := body.Ext[key]; !exists {
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
	if !strings.Contains(body.Ext["general_task_param"], "server_do_compare") {
		t.Errorf("ext.general_task_param 必须是序列化后的任务参数，实际 %q", body.Ext["general_task_param"])
	}
}

// TestBuildWorkOptionModelKeyConsistent 校验 model_item_key 三处一致。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复主键一致性回归，适配 any 字段断言。
func TestBuildWorkOptionModelKeyConsistent(t *testing.T) {
	sa := workTestAccount()
	mode, ok := modeForModel("doubao/kimi-k3")
	if !ok {
		t.Fatal("doubao/kimi-k3 应可解析")
	}
	body := buildCompletionRequest(sa, "你好", mode, "", "", 0, true)

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
	if !strings.Contains(option.ConversationInitExt.AgentTaskParam, `"runtime_type":1`) {
		t.Errorf("init_ext 的 runtime_type 必须为 1（网页来源），实际 %q", option.ConversationInitExt.AgentTaskParam)
	}
	if strings.Contains(option.ConversationInitExt.AgentTaskParam, "sandbox_auth_type") {
		t.Errorf("init_ext 不得带客户端沙箱字段，实际 %q", option.ConversationInitExt.AgentTaskParam)
	}

	taskParam, isObject := option.GeneralTaskParam.(*generalTaskParam)
	if !isObject {
		t.Fatalf("工作模式 general_task_param 必须是 *generalTaskParam，实际 %T", option.GeneralTaskParam)
	}
	if !taskParam.ServerDoCompare {
		t.Error("general_task_param.server_do_compare 必须为 true")
	}
	if taskParam.RuntimeType != 1 {
		t.Errorf("general_task_param.runtime_type 必须为 1，实际 %d", taskParam.RuntimeType)
	}
	if taskParam.ProjectID != "" {
		t.Errorf("general_task_param.project_id 应为空串，实际 %q", taskParam.ProjectID)
	}
	if len(taskParam.ThreadLocalMessageID) != 1 {
		t.Fatalf("thread_local_message_id 应回指本轮消息，实际 %v", taskParam.ThreadLocalMessageID)
	}
	if taskParam.ThreadLocalMessageID[0] != body.Messages[0].LocalMessageID {
		t.Errorf("thread_local_message_id 必须等于 local_message_id，实际 %v vs %q",
			taskParam.ThreadLocalMessageID, body.Messages[0].LocalMessageID)
	}
}

// TestLocalPermissionsAlwaysWebStatus 校验两条管线的本地权限声明取值一致。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复权限状态测试，适配无参签名。
func TestLocalPermissionsAlwaysWebStatus(t *testing.T) {
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

// TestBothPipelinesShareWebQuery 校验两条管线使用同一套网页端 query 参数集。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复 query 同形回归，适配单参 completionURL。
func TestBothPipelinesShareWebQuery(t *testing.T) {
	sa := workTestAccount()
	const sampleBody = `{"messages":[{"content_block":[{"block_type":10000}]}]}`

	// 两条管线的 URL 构造走同一函数，差异只能来自签名与 web_tab_id。
	workURL := completionURL(sa, []byte(sampleBody))
	webURL := completionURL(sa, []byte(sampleBody))

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
		t.Fatalf("两条管线除签名与 web_tab_id 外必须同形，实际\n工作=%s\n网页=%s",
			workValues.Encode(), webValues.Encode())
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
	for _, key := range []string{"channel", "client_platform", "chromium_version", "runtime_version"} {
		if workValues.Get(key) != "" {
			t.Errorf("网页端 query 不应出现客户端键 %s=%q", key, workValues.Get(key))
		}
	}
}

// TestWorkHeadersUseWebFingerprint 校验请求头按网页端上报。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复请求头回归，适配单参 applyUpstreamHeaders。
func TestWorkHeadersUseWebFingerprint(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, doubaoSiteHost+"/chat/completion", nil)
	applyUpstreamHeaders(req)

	if got := req.Header.Get("User-Agent"); got != webUserAgent {
		t.Errorf("User-Agent 应为网页端 UA，实际 %q", got)
	}
	if got := req.Header.Get("Origin"); got != doubaoSiteHost {
		t.Errorf("Origin 应为 %q，实际 %q", doubaoSiteHost, got)
	}
	if got := req.Header.Get("Agw-Js-Conv"); got != "str" {
		t.Errorf("Agw-Js-Conv 应为单值 str，实际 %q", got)
	}
	for _, key := range []string{"x-flow-trace", "last-event-id"} {
		if req.Header.Get(key) != "" {
			t.Errorf("不得上报客户端风控头 %s", key)
		}
	}
}

// TestAtoiSafe 校验推理档位按数字前缀解析。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复档位解析测试。
func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{
		"4":   4,
		"4 ":  4,
		"abc": 0,
		"":    0,
		"12":  12,
	}
	for input, want := range cases {
		if got := atoiSafe(input); got != want {
			t.Errorf("atoiSafe(%q) 应为 %d，实际 %d", input, want, got)
		}
	}
}

// TestWorkOptionJSONShape 校验工作模式请求体的 JSON 形状。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复 JSON 形状回归。
func TestWorkOptionJSONShape(t *testing.T) {
	sa := workTestAccount()
	mode, ok := modeForModel("doubao/deepseek-v4-flash")
	if !ok {
		t.Fatal("doubao/deepseek-v4-flash 应可解析")
	}
	body := buildCompletionRequest(sa, "你好", mode, "", "", 0, true)
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("序列化失败：%v", errMarshal)
	}
	decoded := map[string]any{}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("反序列化失败：%v", errUnmarshal)
	}
	if _, hasTopExt := decoded["ext"]; !hasTopExt {
		t.Error("工作模式必须输出顶层 ext")
	}
	option, isObject := decoded["option"].(map[string]any)
	if !isObject {
		t.Fatal("option 段缺失")
	}
	for _, key := range []string{"general_task_param", "conversation_init_ext", "conversation_init_option", "agent_mode"} {
		if _, exists := option[key]; !exists {
			t.Errorf("option 段缺少键 %s", key)
		}
	}
	if _, exists := option["ext"]; exists {
		t.Error("option 段不得再带内层 ext（真实网页只发顶层 ext）")
	}

	taskParam := workOptionOf(t, option)
	if rt, isNumber := taskParam["runtime_type"].(float64); !isNumber || int(rt) != 1 {
		t.Errorf("general_task_param.runtime_type 必须序列化为数字 1，实际 %T=%v",
			taskParam["runtime_type"], taskParam["runtime_type"])
	}
	if sdc, _ := taskParam["server_do_compare"].(bool); !sdc {
		t.Error("general_task_param.server_do_compare 必须为 true")
	}
	for _, key := range []string{"client_option", "task_input_json", "need_modify_conversation", "agent_task_param_change"} {
		if _, exists := taskParam[key]; exists {
			t.Errorf("general_task_param 不得带客户端字段 %s", key)
		}
	}
	agentTask, isTask := taskParam["agent_task_param"].(map[string]any)
	if !isTask {
		t.Fatal("general_task_param.agent_task_param 缺失")
	}
	for _, key := range []string{"device_name", "workspace", "local_app_id", "local_device_id", "folder_name"} {
		if got, _ := agentTask[key].(string); got != "" {
			t.Errorf("agent_task_param.%s 必须为空串（网页来源不上报客户端设备），实际 %q", key, got)
		}
	}

	// 同名不同型陷阱：顶层 conversation_mode 是 JSON 数字，aggregate_params 内是字符串。
	if cm, isNumber := option["conversation_mode"].(float64); !isNumber {
		t.Errorf("option.conversation_mode 必须序列化为 JSON 数字，实际 %T=%v",
			option["conversation_mode"], option["conversation_mode"])
	} else if int(cm) != workConversationMode {
		t.Errorf("option.conversation_mode 应为 %d，实际 %v", workConversationMode, cm)
	}
	aggParams, isAgg := option["aggregate_params"].(map[string]any)
	if !isAgg {
		t.Fatal("option.aggregate_params 缺失")
	}
	if aggConv, isString := aggParams["conversation_mode"].(string); !isString {
		t.Errorf("aggregate_params.conversation_mode 必须序列化为 JSON 字符串，实际 %T=%v",
			aggParams["conversation_mode"], aggParams["conversation_mode"])
	} else if aggConv != strconv.Itoa(workConversationMode) {
		t.Errorf("aggregate_params.conversation_mode 应为 %q，实际 %q",
			strconv.Itoa(workConversationMode), aggConv)
	}
}

// TestWorkOptionCarriesWebDefaults 校验网页端默认值字段必须显式输出。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27 02:35:00；改动原因：恢复网页默认值字段回归。
func TestWorkOptionCarriesWebDefaults(t *testing.T) {
	sa := workTestAccount()
	mode, ok := modeForModel("doubao/deepseek-v4-flash")
	if !ok {
		t.Fatal("doubao/deepseek-v4-flash 应可解析")
	}
	body := buildCompletionRequest(sa, "你好", mode, "", "", 0, true)
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("序列化失败：%v", errMarshal)
	}
	decoded := map[string]any{}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("反序列化失败：%v", errUnmarshal)
	}

	option, isObject := decoded["option"].(map[string]any)
	if !isObject {
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
			t.Errorf("option 缺少网页默认值字段 %s", key)
		}
	}

	// 2. 数组型默认值必须是 []，不能是字符串 "[]"。
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
	rel, isRelObject := option["related_deleted_message_ids"].(map[string]any)
	if !isRelObject {
		t.Errorf("option.related_deleted_message_ids 必须是 JSON 对象，实际 %T=%v",
			option["related_deleted_message_ids"], option["related_deleted_message_ids"])
	} else if len(rel) != 0 {
		t.Errorf("option.related_deleted_message_ids 应为空对象，实际 %v", rel)
	}

	// 4. recovery_option 必须带当前秒级时间戳。
	recovery, isRecovery := option["recovery_option"].(map[string]any)
	if !isRecovery {
		t.Fatal("option.recovery_option 缺失")
	}
	if sec, isNumber := recovery["req_create_time_sec"].(float64); !isNumber || int64(sec) < 1000000000 {
		t.Errorf("recovery_option.req_create_time_sec 必须是当前秒级时间戳，实际 %T=%v",
			recovery["req_create_time_sec"], recovery["req_create_time_sec"])
	}

	// 5. client_meta 的续接点必须显式输出（新会话为 "" 与 null）。
	meta, isMeta := decoded["client_meta"].(map[string]any)
	if !isMeta {
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
