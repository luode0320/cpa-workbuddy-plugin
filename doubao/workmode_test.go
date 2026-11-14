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
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，工作模式用例共用账号。
func workTestAccount() *StoredAuth {
	return &StoredAuth{
		Type:         providerName,
		Provider:     providerName,
		AuthID:       doubaoAuthFilePrefix + "worktest",
		CookieHeader: validCookieHeader,
		BotID:        defaultBotID,
		DeviceID:     "7300000000000000001",
		WebID:        "7300000000000000002",
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
	if body.Option.GeneralTaskParam == nil {
		t.Fatal("工作模式必须带 general_task_param")
	}
	if !body.Option.IsOldUser {
		t.Error("is_old_user 必须为 true")
	}

	// 技能模式的内层 ext 与顶层 ext 在工作模式里互斥：内层出现、顶层不得出现。
	if len(body.Ext) != 0 {
		t.Errorf("工作模式不得写顶层 ext，实际 %v", body.Ext)
	}
	wantExtKeys := []string{"general_task_param", "use_deep_think", "agent_mode", "is_finish", "conversation_init_option", "commerce_credit_config_enable"}
	for _, key := range wantExtKeys {
		if _, ok := body.Option.Ext[key]; !ok {
			t.Errorf("option.ext 缺少键 %s", key)
		}
	}
	if body.Option.Ext["agent_mode"] != "1" || body.Option.Ext["use_deep_think"] != "10001" {
		t.Errorf("option.ext 取值错误：%v", body.Option.Ext)
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
	if !strings.Contains(option.GeneralTaskParam.TaskInputJSON, "schema_version") {
		t.Errorf("task_input_json 必须是序列化 JSON，实际 %q", option.GeneralTaskParam.TaskInputJSON)
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
	if len(body.Option.Ext) != 0 {
		t.Errorf("技能管线不得写 option 内层 ext，实际 %v", body.Option.Ext)
	}
	if body.Ext["agent_mode"] != "2" || body.Ext["use_deep_think"] != "0" {
		t.Errorf("技能管线顶层 ext 取值错误：%v", body.Ext)
	}
	if body.Option.AggregateParams.MentionSkillList != "[3]" {
		t.Errorf("生图 skill 注入错误：%q", body.Option.AggregateParams.MentionSkillList)
	}
}

// TestWorkModePermissionsDiffersFromWeb 校验两条管线的本地权限声明不同。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，权限状态回归。
func TestWorkModePermissionsDiffersFromWeb(t *testing.T) {
	work := localPermissionsFor(true)
	web := localPermissionsFor(false)
	if len(work) == 0 || len(web) == 0 {
		t.Fatal("本地权限声明不能为空")
	}
	for _, p := range work {
		if p.Status != 1 {
			t.Errorf("工作模式权限状态应为 1，实际 %d（%s）", p.Status, p.PermissionName)
		}
	}
	for _, p := range web {
		if p.Status != 3 {
			t.Errorf("网页端权限状态应为 3，实际 %d（%s）", p.Status, p.PermissionName)
		}
	}
}

// TestBuildClientQueryIsClientShaped 校验工作模式 query 是客户端形态。
//
// 关键是 aid=582478 与 channel=win：网页端的 497858 / web_platform=browser
// 会让上游按网页端解析请求体，工作模式字段被静默忽略。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，客户端参数集回归。
func TestBuildClientQueryIsClientShaped(t *testing.T) {
	values, errParse := url.ParseQuery(buildClientQuery("7300000000000000001", "7300000000000000002"))
	if errParse != nil {
		t.Fatalf("query 应为合法编码：%v", errParse)
	}
	want := map[string]string{
		"aid":                    doubaoClientAID,
		"real_aid":               doubaoClientAID,
		"channel":                "win",
		"client_platform":        "pc_client",
		"doubao_device_platform": "desktop",
		"web_platform":           "desktop",
		"pc_version":             doubaoClientPCVersion,
		"runtime":                "web",
		"runtime_version":        doubaoClientRuntimeVersion,
		"chromium_version":       doubaoClientChromiumVersion,
		"device_id":              "7300000000000000001",
		"fp":                     "verify_7300000000000000001",
		"tea_uuid":               "7300000000000000001",
		"web_id":                 "7300000000000000002",
	}
	for key, expect := range want {
		if got := values.Get(key); got != expect {
			t.Errorf("query %s 应为 %q，实际 %q", key, expect, got)
		}
	}
	if values.Get("web_tab_id") == "" {
		t.Error("web_tab_id 必须每次请求新生成")
	}
	if got := values.Get("aid"); got == doubaoAID {
		t.Error("工作模式不得沿用网页端 aid")
	}
}

// TestBuildClientQueryRotatesTabID 校验 web_tab_id 每次请求都换新。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，标签页标识轮换回归。
func TestBuildClientQueryRotatesTabID(t *testing.T) {
	first, _ := url.ParseQuery(buildClientQuery("7300000000000000001", ""))
	second, _ := url.ParseQuery(buildClientQuery("7300000000000000001", ""))
	if first.Get("web_tab_id") == second.Get("web_tab_id") {
		t.Error("web_tab_id 每次请求必须换新 UUID")
	}
	if first.Get("web_id") != "" {
		t.Errorf("webID 为空时不应写 web_id，实际 %q", first.Get("web_id"))
	}
}

// TestWorkModeHeadersUseClientFingerprint 校验工作模式请求头指纹。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，请求头回归。
func TestWorkModeHeadersUseClientFingerprint(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, doubaoSiteHost+"/chat/completion", nil)
	applyUpstreamHeaders(req, true)
	if got := req.Header.Get("User-Agent"); got != clientUserAgent {
		t.Errorf("工作模式 UA 应为客户端 UA，实际 %q", got)
	}
	if got := req.Header.Get("agw-js-conv"); got != clientAgwJsConv {
		t.Errorf("agw-js-conv 应为 %q，实际 %q", clientAgwJsConv, got)
	}
	if got := req.Header.Get("last-event-id"); got != clientLastEventID {
		t.Errorf("last-event-id 应为 %q，实际 %q", clientLastEventID, got)
	}
	if got := req.Header.Get("x-flow-trace"); got != flowTraceHeader {
		t.Errorf("x-flow-trace 应为 %q，实际 %q", flowTraceHeader, got)
	}

	webReq, _ := http.NewRequest(http.MethodPost, doubaoSiteHost+"/chat/completion", nil)
	applyUpstreamHeaders(webReq, false)
	if got := webReq.Header.Get("User-Agent"); got != webUserAgent {
		t.Errorf("网页端 UA 应为浏览器 UA，实际 %q", got)
	}
	if got := webReq.Header.Get("Agw-Js-Conv"); got != "str" {
		t.Errorf("网页端 Agw-Js-Conv 应为 str，实际 %q", got)
	}
	if got := webReq.Header.Get("x-flow-trace"); got != "" {
		t.Errorf("网页端不应带 x-flow-trace，实际 %q", got)
	}
}

// TestWorkHomeDir 校验 home_dir 推导。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，工作目录推导回归。
func TestWorkHomeDir(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"C:\\Users\\luode\\Doubao\\workspace": "C:\\Users\\luode",
		"D:\\data":                            "",
	}
	for in, want := range cases {
		if got := workHomeDir(in); got != want {
			t.Errorf("workHomeDir(%q) 应为 %q，实际 %q", in, want, got)
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

// TestWorkOptionJSONShape 校验序列化后的键路径。
//
// 只看结构体字段容易漏掉 json tag 写错：上游按"路径 + 键名"取值，
// 顶层 ext 与 option.ext 同名不同层级，串了就整段失效。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-14 03:50:00；改动原因：新增，序列化键路径回归。
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
	if _, hasTopExt := decoded["ext"]; hasTopExt {
		t.Error("工作模式不得输出顶层 ext")
	}
	option, ok := decoded["option"].(map[string]any)
	if !ok {
		t.Fatal("option 段缺失")
	}
	for _, key := range []string{"general_task_param", "conversation_init_ext", "conversation_init_option", "ext", "agent_mode"} {
		if _, exists := option[key]; !exists {
			t.Errorf("option 段缺少键 %s", key)
		}
	}
	clientOption, ok := option["general_task_param"].(map[string]any)["client_option"].(map[string]any)
	if !ok {
		t.Fatal("general_task_param.client_option 缺失")
	}
	if clientOption["os"] != workClientOS {
		t.Errorf("client_option.os 应为 %q，实际 %v", workClientOS, clientOption["os"])
	}
	if sandboxID, _ := clientOption["sandbox_id"].(string); !strings.HasPrefix(sandboxID, "sandbox-") {
		t.Errorf("sandbox_id 应为 sandbox- 前缀，实际 %q", sandboxID)
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
