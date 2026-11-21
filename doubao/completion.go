package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 本文件实现 /chat/completion 的请求体构造与 SSE 流解析。
//
// 协议事实来源：Lxapk/DouBao-CPA-Plugin 的 completion.go（实施总览 EXT-001，
// 无 license，仅作字段名与调用形态参考，实现按本仓库风格重写）+ 本仓库实测。
//
// 两个静默降级陷阱（错了两者都不报错，只是行为不对）：
//  1. 消息文本必须放在 content_block[0].content.text_block.text；写成 content
//     会被当作纯聊天，生成请求只会得到「描述」而不是「生成」。
//  2. option.aggregate_params.mode_id 必须为 "1"；缺失时请求被接受但不进入
//     生成路径。
//
// 与 /im/* 族相反，本端点 Content-Type 必须是纯 "application/json"。

// completionRequest 是 /chat/completion 的请求体。
//
// 首轮与确认轮共用一个结构体，靠两个可选字段区分：ChatAbility 只在确认轮出现，
// 首轮为 nil 因而不序列化。
type completionRequest struct {
	ClientMeta  clientMeta       `json:"client_meta"`
	Messages    []completionMsg  `json:"messages"`
	Option      completionOption `json:"option"`
	UserContext []any            `json:"user_context"`
	// Ext 是技能管线的顶层扩展（agent_mode=2）：网页端把扩展放在顶层而非
	// option 内部，两处同名不同层级，串了就整段失效。
	Ext map[string]string `json:"ext,omitempty"`
	// ChatAbility 只在视频确认轮出现，是「真正开跑」的开关。
	//
	// 首轮只回一段参数确认文本，不同步产生任何产物；带上该字段重发才会转成
	// 异步视频任务（2026-11-17 抓包对比首轮与确认轮，唯一新增的顶层键）。
	ChatAbility *chatAbility `json:"chat_ability,omitempty"`
}

// chatAbility 是视频确认轮的能力声明。
//
// AbilityParam 是**字符串套 JSON**（与 video_model 同一套路数），真实网页就是
// 这么发的，拆成嵌套对象反而与抓包不一致。
type chatAbility struct {
	// AbilityType 是能力类型，视频生成固定 17。
	AbilityType int `json:"ability_type"`
	// AbilityParam 是字符串化参数，见 videoAbilityParam。
	AbilityParam string `json:"ability_param"`
}

// clientMeta 把请求锚定到一个会话。
//
// LocalConversationID 是**必填项**：实测缺失或空串都会被上游以
// 710020202 "common invalid param" 拒绝（HTTP 200 + SSE 错误帧，不报 HTTP 错误），
// 且该值任意非空即可（网页端发的是 "local_<随机串>"）。这一项不在 Lxapk 的
// 参考实现里，是本仓库对照浏览器真实请求逐字段消融定位出来的。
//
// last_section_id / last_message_index 是续接点：新会话可空，续接既有会话时必须带。
//
// 这两项不用 omitempty 是为了与浏览器抓到的请求逐字节对齐（网页新会话时显式发
// last_section_id="" 与 last_message_index=null）。注意：早期曾把省略它们当成
// -1「委托输入协议不符」的成因，2026-11-16 21:20 已证伪 —— 真实浏览器原样 body
// 在未投放工作模式的账号上同样回 -1，故 -1 与请求体字段无关。
type clientMeta struct {
	// LocalConversationID 用 omitempty：新会话必填（缺失会被 710020202 拒），
	// 但视频确认轮的浏览器请求里**根本没有这个键**，只有续接用的
	// conversation_id / last_section_id / last_message_index。新会话路径始终会
	// 填一个非空值，所以 omitempty 只在确认轮起作用。
	LocalConversationID string       `json:"local_conversation_id,omitempty"`
	ConversationID      string       `json:"conversation_id"`
	BotID               string       `json:"bot_id"`
	LastSectionID       string       `json:"last_section_id"`
	LastMessageIndex    *int64       `json:"last_message_index"`
	LocalPermissions    []permission `json:"local_permissions,omitempty"`
}

// permission 是一条本地权限声明，网页端固定全量上报。
type permission struct {
	PermissionName string `json:"permission_name"`
	Status         int    `json:"status"`
}

// completionMsg 是请求中的一条消息。
type completionMsg struct {
	LocalMessageID string         `json:"local_message_id"`
	ContentBlock   []requestBlock `json:"content_block"`
	MessageStatus  int            `json:"message_status"`
}

// requestBlock 是上行消息的一个内容块。
type requestBlock struct {
	BlockType    int              `json:"block_type"`
	Content      requestBlockBody `json:"content"`
	BlockID      string           `json:"block_id"`
	ParentID     string           `json:"parent_id"`
	MetaInfo     []any            `json:"meta_info"`
	AppendFields []any            `json:"append_fields"`
	// ExtraInfo 是块级扩展：真实网页每块都发一个空对象，缺了不报错但形状不一致。
	ExtraInfo map[string]any `json:"extra_info"`
}

// requestBlockBody 是上行块体。
type requestBlockBody struct {
	TextBlock    *textBlockPayload `json:"text_block,omitempty"`
	PCEventBlock string            `json:"pc_event_block"`
}

// textBlockPayload 是文本块内容。
type textBlockPayload struct {
	Text        string `json:"text"`
	IconURL     string `json:"icon_url"`
	IconURLDark string `json:"icon_url_dark"`
	Summary     string `json:"summary"`
}

// completionOption 承载行为开关。
type completionOption struct {
	SendMessageScene  string `json:"send_message_scene"`
	CreateTimeMS      int64  `json:"create_time_ms"`
	CollectID         string `json:"collect_id"`
	IsAudio           bool   `json:"is_audio"`
	AnswerWithSuggest bool   `json:"answer_with_suggest"`
	// AgentMode 用 omitempty：确认轮的真实请求里没有这个键，写 0 会多出一个
	// 首轮没有的字段，与抓包不一致。
	AgentMode     int  `json:"agent_mode,omitempty"`
	TTSSwitch     bool `json:"tts_switch"`
	NeedDeepThink int  `json:"need_deep_think"`

	// AggregateParams 是必需项，mode_id 缺失会让请求退回纯聊天路径。
	AggregateParams aggregateParams `json:"aggregate_params"`

	ModelConfig modelConfig `json:"model_config"`

	SSERecvEventOptions struct {
		SupportChunkDelta bool `json:"support_chunk_delta"`
	} `json:"sse_recv_event_options"`
	SupportLazyFetchStream bool `json:"support_lazy_fetch_stream"`

	UniqueKey string `json:"unique_key"`
	StartSeq  int    `json:"start_seq"`

	// NeedCreateConversation 要求上游分配新会话；空 conversation_id 且不带它
	// 会被以「参数不合法」拒绝。
	NeedCreateConversation bool `json:"need_create_conversation"`

	// 以下字段一律不写 omitempty：网页端每次都会把它们显式发出去，逐字节对齐
	// 真实请求，避免任何「形状差异」成为排查噪音（2026-11-16 抓包对齐）。
	ClickClearContext     bool           `json:"click_clear_context"`
	ConnectorInfoList     []any          `json:"connector_info_list"`
	DisableSSECache       bool           `json:"disable_sse_cache"`
	EditQueryID           []any          `json:"edit_query_id"`
	FromSuggest           bool           `json:"from_suggest"`
	IsAIPlayground        bool           `json:"is_ai_playground"`
	IsFromClickOption     bool           `json:"is_from_click_option"`
	IsFromClickSoftlink   bool           `json:"is_from_click_softlink"`
	IsRegen               bool           `json:"is_regen"`
	IsReplace             bool           `json:"is_replace"`
	IsSelectText          bool           `json:"is_select_text"`
	MessageFrom           int            `json:"message_from"`
	NoReplaceForRegen     bool           `json:"no_replace_for_regen"`
	RegenInstruction      string         `json:"regen_instruction"`
	RegenQueryID          []any          `json:"regen_query_id"`
	ResendForRegen        bool           `json:"resend_for_regen"`
	SceneType             int            `json:"scene_type"`
	SelectTextAction      string         `json:"select_text_action"`
	SharedAppID           string         `json:"shared_app_id"`
	SharedAppName         string         `json:"shared_app_name"`
	RelatedDeletedMessage map[string]any `json:"related_deleted_message_ids"`

	// RecoveryOption 是恢复/重试参数，网页端每次都带（且 req_create_time_sec 为当前秒）。
	RecoveryOption recoveryOption `json:"recovery_option"`

	// GeneralTaskParam 只在视频确认轮出现，值是对象（首轮是 nil，因而不序列化）。
	//
	// 首轮的同名数据放在顶层 Ext["general_task_param"]（字符串），两处层级与类型
	// 都不同；确认轮把它搬进 option 且改成对象，同时 ext 里也留一份字符串副本。
	GeneralTaskParam *generalTaskParamPayload `json:"general_task_param,omitempty"`

	// IsOldUser / MessageStorageType 仅确认轮出现：真实确认轮 option 里这两个键
	// 有值（true / 0），首轮没有。用指针区分「未设置」与「显式零值」。
	IsOldUser          *bool `json:"is_old_user,omitempty"`
	MessageStorageType *int  `json:"message_storage_type,omitempty"`
}

// generalTaskParamPayload 是确认轮 option.general_task_param 的对象形态。
//
// 四个键与真实确认轮逐字对齐；首轮的字符串版本多一层 agent_task_param_change，
// 两者不是同一份数据，不能复用构造逻辑。
type generalTaskParamPayload struct {
	// RuntimeType 是运行类型，确认轮固定 0（首轮字符串版本也是 0）。
	RuntimeType int `json:"runtime_type"`
	// AgentTaskParam 确认轮恒为空对象。
	AgentTaskParam map[string]any `json:"agent_task_param"`
	// ProjectID 确认轮恒为空串。
	ProjectID string `json:"project_id"`
	// ServerDoCompare 确认轮为 true。
	ServerDoCompare bool `json:"server_do_compare"`
}

// recoveryOption 是网页端的恢复选项段。
type recoveryOption struct {
	IsRecovery          bool  `json:"is_recovery"`
	ReqCreateTimeSec    int64 `json:"req_create_time_sec"`
	AppendSSEEventScene int   `json:"append_sse_event_scene"`
}

// aggregateParams 是生成路径选择参数。
type aggregateParams struct {
	MentionSkillList  string `json:"mention_skill_list"`
	MentionPluginList string `json:"mention_plugin_list"`
	MentionExt        string `json:"mention_ext"`
	ConversationMode  string `json:"conversation_mode"`
	ModeID            string `json:"mode_id"`
	ModelItemKey      string `json:"model_item_key"`
	AgentMode         string `json:"agent_mode"`
	ReasoningEffort   string `json:"reasoning_effort"`
	ProviderID        string `json:"provider_id"`
}

// modelConfig 是模型侧配置。
type modelConfig struct {
	ModelItemKey     string         `json:"model_item_key"`
	ModelExtraParams map[string]any `json:"model_extra_params"`
	ReasoningEffort  int            `json:"reasoning_effort"`
}

// nowMillis 返回当前 Unix 毫秒时间戳。
// [参数] 无。
// [返回] 毫秒时间戳。
// 最近修改时间：2026-10-17；改动原因：新增，请求体 create_time_ms 取值。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// buildCompletionRequest 组装一轮对话的请求体。
//
// 只有一条上游管线：网页端技能模式（agent_mode=2），靠 mention_skill_list 触发
// 生视频。会话续接点照常透传——视频要做「首轮确认 + 次轮出片」，第二轮
// 必须能指回同一个 conversation 与 section，否则上游只当新对话处理。
//
// 续接点非空即认定为视频确认轮（confirm=true），此时改用确认轮形态：option 走
// buildConfirmOption、ext 走 confirmExt、client_meta 不带 local_conversation_id。
// 调用方若确实要在既有会话里发普通技能请求，用 buildCompletionRequestWith 显式
// 指定 confirm=false。
// [参数] sa: 账号（提供 bot_id）；text: 用户文本；mode: 生成模式；
// conversationID / lastSectionID / lastMessageIndex: 会话续接点；
// needCreate: 是否请求上游分配新会话；params: 视频参数（可变，缺省即不传）。
// [返回] 请求体。
// 最近修改时间：2026-11-17 21:52:00；改动原因：新增视频参数可变入参并向下透传。
func buildCompletionRequest(sa *StoredAuth, text string, mode generationMode, conversationID, lastSectionID string, lastMessageIndex int64, needCreate bool, params ...videoParams) *completionRequest {
	resume := resumePoint{ConversationID: conversationID, SectionID: lastSectionID, MessageIndex: lastMessageIndex}
	return buildCompletionRequestWith(sa, text, mode, resume, needCreate, !resume.isZero(), params...)
}

// buildCompletionRequestWith 组装请求体，并显式指定是否为确认轮。
//
// params 只有视频确认轮用得上（进 chat_ability），所以用变参表达「可选」：
// 签名里写死一个 videoParams 会逼所有只关心提示词的调用点补一个空结构，
// 那种噪音会让真正的改动看不见。
// [参数] sa: 账号；text: 用户文本；mode: 生成模式；resume: 会话续接点；
// needCreate: 是否新建会话；confirm: 是否使用视频确认轮形态；
// params: 视频参数（可变，缺省即按 protocol.go 的默认档）。
// [返回] 请求体。
// 最近修改时间：2026-11-17 21:52:00；改动原因：确认轮改用调用方给定的视频参数。
func buildCompletionRequestWith(sa *StoredAuth, text string, mode generationMode, resume resumePoint, needCreate bool, confirm bool, params ...videoParams) *completionRequest {
	botID := strings.TrimSpace(sa.BotID)
	if botID == "" {
		botID = defaultBotID
	}

	// 1. 先落地消息块：消息 id 由本地生成，上游只做透传回显。
	localMessageID := randomUUID()
	// 新会话（未给续接点）显式发 null，与浏览器一致。
	var lastIndex *int64
	if resume.MessageIndex > 0 {
		messageIndex := resume.MessageIndex
		lastIndex = &messageIndex
	}
	// 确认轮的浏览器请求里没有 local_conversation_id，留空并由 omitempty 省略。
	localConversationID := ""
	if !confirm {
		localConversationID = "local_" + randomUUID()
	}
	body := &completionRequest{
		ClientMeta: clientMeta{
			LocalConversationID: localConversationID,
			ConversationID:      resume.ConversationID,
			BotID:               botID,
			LastSectionID:       resume.SectionID,
			LastMessageIndex:    lastIndex,
			LocalPermissions:    localPermissionsFor(),
		},
		Messages: []completionMsg{{
			LocalMessageID: localMessageID,
			MessageStatus:  0,
			ContentBlock: []requestBlock{{
				BlockType:    blockTypeText,
				BlockID:      randomUUID(),
				ParentID:     "",
				MetaInfo:     []any{},
				AppendFields: []any{},
				ExtraInfo:    map[string]any{},
				Content: requestBlockBody{
					TextBlock: &textBlockPayload{Text: text},
				},
			}},
		}},
		UserContext: []any{},
	}

	// 2. 组装 option 与顶层 ext：两条形态不可混用。
	if confirm {
		body.Option = buildConfirmOption(mode, needCreate)
		body.Ext = confirmExt()
		// chat_ability 是确认轮「真正开跑」的唯一开关，必须由构造器负责注入：
		// 交给调用方手工挂会漏，漏了就只剩一段确认文本、不产生任何产物。
		// 参数取调用方给的那一份，与首轮提示词同源——两处各读一遍入参是 0.1.27
		// 出现「承诺 5 秒、提交 10 秒」的直接原因。
		body.ChatAbility = buildVideoConfirmAbility(text, videoParamsOf(params), videoConfirmModel)
		return body
	}
	body.Option = buildSkillOption(mode, needCreate)
	body.Ext = skillExt()
	return body
}

// localPermissionsFor 返回本地权限声明列表。
//
// status=3 是网页端真实取值（浏览器抓包 confirmed）。旧实现曾给工作模式报 1，
// 来自「客户端已授权」的错误推断，会让网页来源的请求被判成客户端协议
// （-1 delegated input protocol violation，2026-11-16 实测）。
// [参数] 无。
// [返回] 权限声明列表。
// 最近修改时间：2026-11-17 00:55:00；改动原因：工作模式删除，去掉无意义的入参。
func localPermissionsFor() []permission {
	// 1. 固定取值，与真实网页对齐。
	const status = 3

	// 2. 三条权限同名同值，缺一条会被上游当能力声明不完整。
	return []permission{
		{PermissionName: "ACCESS_COARSE_LOCATION", Status: status},
		{PermissionName: "ACCESS_FINE_LOCATION", Status: status},
		{PermissionName: "ACCESS_BACKGROUND_LOCATION", Status: status},
	}
}

// buildSkillOption 构造网页端技能管线的 option 段。
//
// 该链路已验证可用，字段取值保持不变：mode_id 固定 "1"、model_item_key "0"、
// agent_mode 2；生视频靠 mention_skill_list 激活。
// [参数] mode: 生成模式（Skill 决定 skill 清单）；needCreate: 是否新建会话。
// [返回] option 段。
// 最近修改时间：2026-11-14 02:30:00；改动原因：从 buildCompletionRequest 抽出，行为不变。
func buildSkillOption(mode generationMode, needCreate bool) completionOption {
	// 1. 固定字段一气写完：该组合已在上游验证可用，逐字段改动会打断生成链路。
	option := completionOption{
		AgentMode:     2,
		CreateTimeMS:  nowMillis(),
		NeedDeepThink: 0,
		AggregateParams: aggregateParams{
			MentionSkillList:  "[]",
			MentionPluginList: "[]",
			MentionExt:        "[{}]",
			ModeID:            "1",
			ModelItemKey:      "0",
			AgentMode:         "2",
			ReasoningEffort:   "3",
		},
		ModelConfig: modelConfig{
			ModelItemKey:     "0",
			ModelExtraParams: map[string]any{},
			ReasoningEffort:  3,
		},
		UniqueKey:              randomUUID(),
		NeedCreateConversation: needCreate,
	}
	// 2. 两个流式开关必须显式打开，否则拿不到增量帧。
	option.SSERecvEventOptions.SupportChunkDelta = true
	option.SupportLazyFetchStream = true

	// 3. 网页端每次都会显式发送这些默认值段，逐字节对齐真实请求。
	option.ConnectorInfoList = []any{}
	option.EditQueryID = []any{}
	option.RegenQueryID = []any{}
	option.RelatedDeletedMessage = map[string]any{}
	option.RecoveryOption = recoveryOption{
		IsRecovery:          false,
		ReqCreateTimeSec:    time.Now().Unix(),
		AppendSSEEventScene: 0,
	}

	// 4. 生成请求必须显式点名 skill，否则上游按普通对话处理。
	if mode.Skill > 0 {
		option.AggregateParams.MentionSkillList = fmt.Sprintf("[%d]", mode.Skill)
	}
	return option
}

// skillExt 返回网页端技能链路的顶层 ext 段。
// 网页端把扩展放在**顶层** ext（不是 option.ext），首轮共 6 键。
// [参数] 无。
// [返回] ext 字典。
// 最近修改时间：2026-11-14 02:30:00；改动原因：从 buildCompletionRequest 抽出，取值不变。
func skillExt() map[string]string {
	// 1. 技能链路的 general_task_param 是空壳，仅占位。
	generalTaskParam, _ := json.Marshal(map[string]any{
		"runtime_type":     0,
		"agent_task_param": map[string]any{},
		"agent_task_param_change": map[string]any{
			"runtime_changed":           false,
			"device_changed":            false,
			"sandbox_auth_type_changed": false,
		},
		"project_id":               "",
		"need_modify_conversation": false,
	})
	// 2. 固定键值：agent_mode=2 与 use_deep_think=0 是技能链路的标志位。
	return map[string]string{
		"agent_mode":                    "2",
		"use_deep_think":                "0",
		"general_task_param":            string(generalTaskParam),
		"collection_id":                 "",
		"is_finish":                     "1",
		"commerce_credit_config_enable": "0",
	}
}

// buildConfirmOption 构造视频确认轮的 option 段。
//
// 确认轮与首轮不是同一份形态（2026-11-17 抓包逐字段对比）：
//   - agent_mode 键整个不出现（首轮是 2）；
//   - aggregate_params 六个路径字段全为空串（首轮是 mode_id=1 / agent_mode=2 …）；
//   - model_config 只留空 model_item_key 与空 map，没有 reasoning_effort；
//   - need_create_conversation=false（必须续接既有会话）；
//   - 多出 general_task_param 对象、is_old_user、message_storage_type 三项。
//
// 上游只认「续接点 + chat_ability」这一组合来真正开跑，路径参数留空是它的正常
// 形态；照首轮那样填满反而会被当成新的一轮对话。
// [参数] mode: 生成模式（仅用于注释语义，确认轮不写路径参数）；needCreate 恒 false。
// [返回] option 段。
// 最近修改时间：2026-11-17 01:36:00；改动原因：新增，视频两段式的第二段形态。
func buildConfirmOption(mode generationMode, needCreate bool) completionOption {
	// 1. 基础字段与技能轮同源，但路径参数一律留空。
	option := completionOption{
		CreateTimeMS:  nowMillis(),
		NeedDeepThink: 0,
		AggregateParams: aggregateParams{
			MentionSkillList:  "[]",
			MentionPluginList: "[]",
			MentionExt:        "[{}]",
		},
		ModelConfig: modelConfig{
			ModelItemKey:     "",
			ModelExtraParams: map[string]any{},
		},
		UniqueKey:              randomUUID(),
		NeedCreateConversation: needCreate,
		GeneralTaskParam: &generalTaskParamPayload{
			RuntimeType:     0,
			AgentTaskParam:  map[string]any{},
			ProjectID:       "",
			ServerDoCompare: true,
		},
	}
	// 2. 两个流式开关与首轮一致，否则拿不到增量帧。
	option.SSERecvEventOptions.SupportChunkDelta = true
	option.SupportLazyFetchStream = true

	// 3. 三个「仅确认轮出现」的标量：显式置值，靠指针与 omitempty 表达存在性。
	isOldUser := true
	storageType := 0
	option.IsOldUser = &isOldUser
	option.MessageStorageType = &storageType

	// 4. 网页端默认值段与首轮一致。
	option.ConnectorInfoList = []any{}
	option.EditQueryID = []any{}
	option.RegenQueryID = []any{}
	option.RelatedDeletedMessage = map[string]any{}
	option.RecoveryOption = recoveryOption{
		IsRecovery:          false,
		ReqCreateTimeSec:    time.Now().Unix(),
		AppendSSEEventScene: 0,
	}
	// 5. 确认轮不点名 skill：走的是 chat_ability 而不是 mention_skill_list。
	_ = mode
	return option
}

// confirmExt 返回视频确认轮的顶层 ext 段。
//
// 与首轮 skillExt 的差异：没有 agent_mode / use_deep_think / sub_conv_firstmet_type，
// 多一个 answer_with_suggest="0"，且 general_task_param 换成确认轮那份对象序列化。
// [参数] 无。
// [返回] ext 字典。
// 最近修改时间：2026-11-17 01:36:00；改动原因：新增，确认轮 ext 只有 5 键。
func confirmExt() map[string]string {
	taskParam, _ := json.Marshal(generalTaskParamPayload{
		RuntimeType:     0,
		AgentTaskParam:  map[string]any{},
		ProjectID:       "",
		ServerDoCompare: true,
	})
	return map[string]string{
		"answer_with_suggest":           "0",
		"general_task_param":            string(taskParam),
		"collection_id":                 "",
		"is_finish":                     "1",
		"commerce_credit_config_enable": "0",
	}
}

// buildVideoConfirmAbility 组装视频确认轮的 chat_ability。
//
// ability_param 是**字符串套 JSON**：真实网页这么发，拆成对象会与抓包不一致。
// reply_message_format 里的 %s 是上游回填视频描述的位置，不是本地格式化占位，
// 必须原样送出。
// [参数] text: 用户在确认轮输入的文本（真实网页原样回填进 user_input_content）；
// params: 目标参数（时长/清晰度/比例），必须与首轮提示词里给出的一致；
// model: 上游模型代号。
// [返回] 能力声明。
// 最近修改时间：2026-11-17 21:52:00；改动原因：三个参数改从 videoParams 取，避免与首轮脱钩。
func buildVideoConfirmAbility(text string, params videoParams, model string) *chatAbility {
	param, _ := json.Marshal(map[string]any{
		"ratio":      params.Ratio,
		"model":      model,
		"duration":   params.Duration,
		"resolution": params.Resolution,
		"input_box_content": map[string]any{
			"user_input_content":   text,
			"reply_message_format": "生成视频：%s",
		},
	})
	return &chatAbility{
		AbilityType:  abilityTypeVideoGen,
		AbilityParam: string(param),
	}
}

// videoParamsOf 取变参里的第一个，没有就用缺省值。
//
// 用变参表达「可选」的取舍见 buildCompletionRequestWith 的注释；这里负责把
// 「没传」翻译成与 protocol.go 默认档完全一致的 videoParams，任何调用方都不
// 需要自己拼一份缺省结构。
// [参数] params: 变参，最多取第一个。
// [返回] 归一后的参数（缺省时即默认档）。
// 最近修改时间：2026-11-17 21:52:00；改动原因：新增，收敛变参取值。
func videoParamsOf(params []videoParams) videoParams {
	if len(params) > 0 {
		return params[0]
	}
	return normalizeVideoParams(0, "", "")
}

// appendVideoParamHint 把目标参数追加到首轮提示词末尾。
//
// 为什么要写进提示词：上游是让模型读提示词、再由模型回出「参数确认」文本的。
// 不把参数说清楚，模型会按自己的默认值回出确认文本，而确认轮的 chat_ability
// 用的是调用方给的值——两段就脱钩了（0.1.27 的「承诺 5 秒、提交 10 秒」）。
// 缺省参数时一字不加：不传参数的老调用方请求体必须与之前逐字节一致。
// [参数] prompt: 折叠后的提示词；mode: 生成模式；params: 归一后的视频参数。
// [返回] 可能要追加参数要求的新提示词。
// 最近修改时间：2026-11-17 21:52:00；改动原因：新增，首轮提示词必须与确认轮同源。
func appendVideoParamHint(prompt string, mode generationMode, params videoParams) string {
	// 1. 只有视频能力且参数非缺省才追加：其余情况保持原样。
	if mode.Kind != "video" || params.isDefault() {
		return prompt
	}

	// 2. 单独成段，避免与用户原话粘连成一个长句被模型忽略。
	return fmt.Sprintf("%s\n\n视频参数要求：时长 %d 秒、清晰度 %s、画面比例 %s。请直接按这组参数生成，不要再改成其它值。",
		prompt, params.Duration, params.Resolution, params.Ratio)
}

// completionEvent 是一条解码后的 SSE 事件。
type completionEvent struct {
	// Name 是 "event:" 字段。
	Name string
	// Data 是 "data:" 负载（多行 data 以换行拼接）。
	Data []byte
}

// responseBlock 是流式消息中的一个内容块。
//
// Content 用 RawMessage：流式端点内联下发对象，而存储会话端点会二次编码，
// 两种形态都由调用方各自解析。
type responseBlock struct {
	BlockType int             `json:"block_type"`
	BlockID   string          `json:"block_id"`
	Content   json.RawMessage `json:"content"`
	IsFinish  bool            `json:"is_finish"`
	PatchType int             `json:"patch_type"`
}

// blockContent 是本插件读取的块体并集。
type blockContent struct {
	TextBlock     *textBlockContent `json:"text_block"`
	CreationBlock json.RawMessage   `json:"creation_block"`
}

// textBlockContent 是下行文本块内容。
type textBlockContent struct {
	Text string `json:"text"`
}

// streamMessageContent 是 STREAM_MSG_NOTIFY / STREAM_CHUNK 的 content 对象。
type streamMessageContent struct {
	ContentBlock  []responseBlock `json:"content_block"`
	ContentStatus int             `json:"content_status"`
	Ext           json.RawMessage `json:"ext"`
}

// textOf 拼接消息中的文本块。
// [参数] 无。
// [返回] 文本内容。
// 最近修改时间：2026-10-17；改动原因：新增，文本增量提取。
func (m *streamMessageContent) textOf() string {
	var b strings.Builder
	for i := range m.ContentBlock {
		if m.ContentBlock[i].BlockType != blockTypeText {
			continue
		}
		var c blockContent
		if json.Unmarshal(m.ContentBlock[i].Content, &c) != nil || c.TextBlock == nil {
			continue
		}
		b.WriteString(c.TextBlock.Text)
	}
	return b.String()
}

// reasoningOf 拼接深度思考块。
// [参数] 无。
// [返回] 思考内容。
// 最近修改时间：2026-10-17；改动原因：新增，reasoning_content 数据来源。
func (m *streamMessageContent) reasoningOf() string {
	var b strings.Builder
	for i := range m.ContentBlock {
		if m.ContentBlock[i].BlockType != blockTypeDeepThink {
			continue
		}
		var c blockContent
		if json.Unmarshal(m.ContentBlock[i].Content, &c) != nil || c.TextBlock == nil {
			continue
		}
		b.WriteString(c.TextBlock.Text)
	}
	return b.String()
}

// patchOp 是 STREAM_CHUNK 的增量补丁项。
type patchOp struct {
	PatchObject int        `json:"patch_object"`
	PatchType   int        `json:"patch_type"`
	PatchValue  patchValue `json:"patch_value"`
}

// patchValue 是补丁值。
//
// ContentBlock 是当前形态；Content 是旧包装，部分补丁类型仍在用，两种都要认。
type patchValue struct {
	ContentBlock []responseBlock    `json:"content_block"`
	Content      messageContentBody `json:"content"`
	Ext          json.RawMessage    `json:"ext"`
}

// blocks 返回补丁携带的全部块（兼容两种拼写）。
// [参数] 无。
// [返回] 内容块列表。
// 最近修改时间：2026-10-17；改动原因：新增，双形态兼容。
func (p *patchValue) blocks() []responseBlock {
	if len(p.ContentBlock) > 0 {
		return p.ContentBlock
	}
	return p.Content.ContentBlock
}

// textOf 拼接补丁中的文本块。
//
// 空 text_block 只是保持块 id 存活，不是内容，不能当增量。
// [参数] 无。
// [返回] 文本内容。
// 最近修改时间：2026-10-17；改动原因：新增，补丁文本提取。
func (p *patchValue) textOf() string {
	var out strings.Builder
	for _, b := range p.blocks() {
		if b.BlockType != blockTypeText {
			continue
		}
		var c blockContent
		if json.Unmarshal(b.Content, &c) != nil || c.TextBlock == nil {
			continue
		}
		out.WriteString(c.TextBlock.Text)
	}
	return out.String()
}

// messageContentBody 是补丁与整消息共用的 content 包装。
type messageContentBody struct {
	ContentBlock []responseBlock `json:"content_block"`
}

// streamError 是 STREAM_ERROR 事件的负载。
//
// extra.decision 是关键：710022004 看起来像限流（error_msg 写的就是 "rate limited"），
// 实际是上游弹的人机验证，判定依据全在 decision 里（type=verify /
// subtype=semantic_reasoning / log_id）。不解析它就只能靠错误码猜成因，
// 0.1.21 正是这样把「人机验证」误判成「设备指纹不匹配」的。
type streamError struct {
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
	Extra     struct {
		Decision string `json:"decision"`
		Ack      string `json:"ack"`
	} `json:"extra"`
}

// verifyDecision 是 extra.decision 里与「人机验证」相关的那部分字段。
//
// 上游把 decision 编成一段 JSON 字符串塞在 extra 里，字段名与含义见
// 2026-11-16 实测帧：type=verify、subtype=semantic_reasoning、
// from=shark_admin、verify_scene=doubao_message_web。
type verifyDecision struct {
	Code        string `json:"code"`
	From        string `json:"from"`
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Region      string `json:"region"`
	LogID       string `json:"log_id"`
	VerifyScene string `json:"verify_scene"`
}

// verifyOf 解析 extra.decision，判定这次失败是否由人机验证触发。
// [参数] 无。
// [返回] 人机验证信息；不是验证帧或解析失败时返回 nil。
// 最近修改时间：2026-11-16 06:05:00；改动原因：新增，710022004 的真实成因要靠 decision 判定。
func (e *streamError) verifyOf() *verifyDecision {
	// 1. decision 是嵌套 JSON 字符串，空串直接排除。
	raw := strings.TrimSpace(e.Extra.Decision)
	if raw == "" {
		return nil
	}

	// 2. 只认 type=verify，其他决策类型不改变提示口径。
	var d verifyDecision
	if json.Unmarshal([]byte(raw), &d) != nil || d.Type != "verify" {
		return nil
	}
	return &d
}

// streamErrorHint 把 STREAM_ERROR 帧翻译成面向用户的文案。
//
// 有 decision 的验证帧优先走验证文案：错误码本身（710022004）在两种成因下
// 完全相同，只有 decision 能区分「人机验证」与「真的被限流」。
// [参数] e: 已解析的错误帧。
// [返回] 中文提示。
// 最近修改时间：2026-11-16 06:05:00；改动原因：新增，验证帧按 decision 给专项文案。
func streamErrorHint(e *streamError) string {
	// 1. 人机验证帧：说清要做什么，并带上游 log_id 便于对账。
	if d := e.verifyOf(); d != nil {
		return fmt.Sprintf("上游要求人机验证（%s，语义选图）：该账号已进入验证态，需要在浏览器里登录同一账号并手动完成一次验证（豆包网页任意发一句话，弹出的九宫格按要求拖图即可）；验证通过后账号即恢复，无需重新授权。上游 log_id=%s，verify_scene=%s", d.Subtype, d.LogID, d.VerifyScene)
	}

	// 2. 其他错误帧沿用错误码文案。
	return statusCodeHint(e.ErrorCode, e.ErrorMsg)
}

// parseStreamError 解码 STREAM_ERROR 事件。
// [参数] data: 事件负载。
// [返回] 错误结构；不是错误帧时第二返回值为 false。
// 最近修改时间：2026-11-16 06:00:00；改动原因：新增 extra.decision 解析，用于区分人机验证与限流。
func parseStreamError(data []byte) (*streamError, bool) {
	var e streamError
	if json.Unmarshal(data, &e) != nil || e.ErrorCode == 0 {
		return nil, false
	}
	return &e, true
}

// scanSSE 逐帧解析 SSE 流。
//
// 分帧规则：空行结束一帧；"event:" 取事件名；"data:" 可多行，以换行拼接。
// 缓冲区放大到 8MiB：生成块携带图片数据时单行可轻易超过 bufio 默认的 64KiB，
// 否则会被当成超长行丢弃。
// onEvent 返回 false 表示调用方已拿到所需内容，立即停止读取。
// [参数] r: 流读取器；onEvent: 事件回调。
// [返回] 读取错误。
// 最近修改时间：2026-10-17；改动原因：新增，SSE 分帧核心。
func scanSSE(r io.Reader, onEvent func(completionEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	var current completionEvent
	flush := func() bool {
		if current.Name == "" && len(current.Data) == 0 {
			return true
		}
		keep := onEvent(current)
		current = completionEvent{}
		return keep
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, "event:"):
			current.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if len(current.Data) > 0 {
				current.Data = append(current.Data, '\n')
			}
			current.Data = append(current.Data, chunk...)
		}
	}
	// 流结束时若没有尾随空行，最后一帧仍然有效。
	if !flush() {
		return nil
	}
	return scanner.Err()
}

// newUpstreamClient 构造带上游凭据的 HTTP 客户端。
//
// 使用 durableJar 而非原始 header：上游下发的 Set-Cookie 需要被捕获并落盘
// （见 cookiejar.go）。已落盘的快照优先，首次导入的账号用原始 header 播种。
// 播种完成后先补一次替代会话 Cookie（ttwid）：4 条 session Cookie 已被出站
// 过滤掉，没有 ttwid 的请求会拿到空响应而非报错，属于最难排查的静默失败。
// [参数] sa: 账号；timeout: 客户端超时，0 表示不设（流式请求由 context 约束）。
// [返回] HTTP 客户端与 Jar。
// 最近修改时间：2026-11-15 15:56:00；改动原因：播种后自动补齐替代会话 Cookie。
func newUpstreamClient(sa *StoredAuth, timeout time.Duration) (*http.Client, *durableJar) {
	jar := newDurableJar()
	if len(sa.Cookies) > 0 {
		jar.restore(sa.Cookies)
	} else {
		jar.seedFromHeader(sa.CookieHeader, doubaoSiteHost+"/")
	}
	if sa.PhysicalPath != "" {
		jar.bind(sa.PhysicalPath)
	}
	// 1. 缺 ttwid 时先预热一次站点首页；失败不阻断，请求仍会发出（只是可能空响应）。
	_ = warmUpFallbackCookie(jar, 0)
	// 流式请求同样走共享传输层：默认 Transport 的 10s 握手超时会让慢节点
	// 直接断连，而回复可能已在上游开始生成、无法重来。
	return &http.Client{Jar: jar, Transport: sharedUpstreamTransport, Timeout: timeout}, jar
}

// applyUpstreamHeaders 设置业务端点所需的请求头。
//
// 固定用网页端 UA 与单值 agw-js-conv：换客户端 UA 会被上游按「桌面端协议」
// 解析请求体，与实际 body 形状不符，直接回 -1 delegated input protocol
// violation（2026-11-16 实测）。
// [参数] req: 请求。
// [返回] 无。
// 最近修改时间：2026-11-17 00:55:00；改动原因：工作模式删除，去掉无意义的入参。
func applyUpstreamHeaders(req *http.Request) {
	// 1. 只上报网页端那一套。
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Origin", doubaoSiteHost)
	req.Header.Set("Referer", doubaoSiteHost+"/chat/")
	req.Header.Set("Agw-Js-Conv", "str")
}

// completionEndpointOverride 是 /chat/completion 端点的测试覆盖点；非空时优先使用。
// 生产路径下始终为空，端点由 doubaoSiteHost + 路径常量拼装。
var completionEndpointOverride string

// swapCompletionEndpoint 临时覆盖对话端点，返回恢复函数（仅测试使用）。
// [参数] endpoint: 替换后的完整 URL；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-17；改动原因：新增，让流式链路可在 httptest 下验证。
func swapCompletionEndpoint(endpoint string) func() {
	prev := completionEndpointOverride
	completionEndpointOverride = endpoint
	return func() { completionEndpointOverride = prev }
}

// completionURL 返回对话端点完整地址（测试可通过 swapCompletionEndpoint 覆盖）。
//
// 用网页端参数集（aid=497858、doubao_device_platform=web、web_platform=browser）：
// 换客户端参数集会让上游按桌面端协议解析，与实际 body 形状不符，直接回 -1
// （2026-11-16 实测）。
//
// 必须补齐字节风控签名（msToken + a_bogus），否则上游直接回 710022002
// 「当前服务访问频繁」，与账号状态无关（见 sign.go）。
// [参数] sa: 账号（提供设备级标识与 msToken）；body: 即将发送的请求体（a_bogus 覆盖 body）。
// [返回] 完整 URL。
// 最近修改时间：2026-11-17 00:55:00；改动原因：工作模式删除，去掉无意义的入参。
func completionURL(sa *StoredAuth, body []byte) string {
	// 1. 测试覆盖点优先，保证流式链路能在 httptest 下验证。
	if completionEndpointOverride != "" {
		return completionEndpointOverride
	}

	// 2. 参数集固定；签名在参数集之后做，先签再改会失效。
	return signChatCompletionURL(sa, endpointURL(sa, "/chat/completion", nil), body)
}

// streamCompletion 发起一次 /chat/completion 请求并逐帧回调。
//
// 流式请求不设客户端超时：回复可能持续数分钟，由 ctx 的 deadline 约束。
// [参数] ctx: 上下文；sa: 账号；body: 请求体；onEvent: 事件回调（返回 false 提前结束）。
// [返回] 请求或读取错误。
// 最近修改时间：2026-10-17；改动原因：新增，SSE 上游调用。
func streamCompletion(ctx context.Context, sa *StoredAuth, body *completionRequest, onEvent func(completionEvent) bool) error {
	// 1. 先编码请求体：签名要吃真实 body，必须先序列化再签。
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return fmt.Errorf("编码请求失败：%w", errMarshal)
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(sa, raw), bytes.NewReader(raw))
	if errReq != nil {
		return fmt.Errorf("构造请求失败：%w", errReq)
	}
	// 与 /im/* 相反：本端点只接受纯 application/json。
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "text/event-stream")
	applyUpstreamHeaders(req)

	// 2. 流式请求不设客户端超时，回复可能持续数分钟，由 ctx 约束。
	client, jar := newUpstreamClient(sa, 0)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return fmt.Errorf("请求上游失败：%w", errDo)
	}
	defer resp.Body.Close()
	defer func() {
		// 滚动下发的设备 Cookie 在请求结束后落盘；失败不影响本次回复。
		_ = jar.persist(sa)
	}()

	// 3. 非 200 一律转结构化上游错误，供换号与冷却判定使用。
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &upstreamError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("上游返回 HTTP %d：%s", resp.StatusCode, truncateText(string(payload), 200)),
		}
	}

	return scanSSE(resp.Body, onEvent)
}

// truncateText 截断过长文本，用于错误信息。
// [参数] s: 原文；n: 最大字节数。
// [返回] 截断后的文本。
// 最近修改时间：2026-10-17；改动原因：新增，错误信息裁剪。
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// businessEndpointOverride 是业务 JSON 端点的测试覆盖点；非空时所有业务 POST
// 都打到该地址。生产路径下始终为空。
var businessEndpointOverride string

// swapBusinessEndpoint 临时覆盖业务端点基址，返回恢复函数（仅测试使用）。
// [参数] base: 替换后的基址（不含路径）；空串表示恢复默认。
// [返回] 恢复原值的函数。
// 最近修改时间：2026-10-17；改动原因：新增，无水印解析等端点可在 httptest 下验证。
func swapBusinessEndpoint(base string) func() {
	prev := businessEndpointOverride
	businessEndpointOverride = base
	return func() { businessEndpointOverride = prev }
}

// resumePoint 是会话续接点：指回上一条提问，让上游把新消息接在同一个 section 下。
//
// 视频是两段式的：首轮只回参数确认，次轮要「接着上一句说」。次轮的请求体里
// agent_mode / conversation_mode / model_item_key 全部留空，上游完全靠这三个
// 字段找回上下文（2026-11-17 抓包确认）。新会话时三个字段都为空，语义不变。
type resumePoint struct {
	// ConversationID 是上游分配的会话 id。
	ConversationID string
	// SectionID 是上一条回复所在分节 id。
	SectionID string
	// MessageIndex 是上一条提问在会话中的序号，0 表示不带。
	MessageIndex int64
}

// isZero 判断是否为「新会话」。
// [参数] 无。
// [返回] 三个字段全空时返回 true。
// 最近修改时间：2026-11-17 01:30:00；改动原因：新增，续接点判空。
func (r resumePoint) isZero() bool {
	return strings.TrimSpace(r.ConversationID) == "" && strings.TrimSpace(r.SectionID) == "" && r.MessageIndex == 0
}

// completionResult 是一次对话的聚合结果。
type completionResult struct {
	// Text 是可见回复正文。
	Text string
	// Reasoning 是深度思考内容。
	Reasoning string
	// ConversationID 是上游分配的会话 id。
	ConversationID string
	// SectionID 是本次回复所在分节 id。
	SectionID string
	// MessageIndex 是本次提问在会话中的序号。
	MessageIndex int64
	// Assets 是本次回复中的生成结果（图片/视频），纯文本回复为空。
	Assets []creationAsset
}

// resumePointOf 把聚合结果收敛成下一轮可用的续接点。
// [参数] 无。
// [返回] 续接点。
// 最近修改时间：2026-11-17 01:30:00；改动原因：新增，回程续接点。
func (r *completionResult) resumePointOf() resumePoint {
	if r == nil {
		return resumePoint{}
	}
	return resumePoint{ConversationID: r.ConversationID, SectionID: r.SectionID, MessageIndex: r.MessageIndex}
}

// collectCompletion 执行一轮新会话对话并聚合完整结果。
//
// 这是 collectCompletionWithResume 的新会话特例：不带续接点、要求上游新建会话。
// 保留这个签名是因为大量用例与测试替身只关心「发一句话拿回复」，让它们也被迫
// 构造空续接点没有收益。
// [参数] ctx: 上下文；sa: 账号；text: 用户文本；mode: 生成模式；
// conversationID: 会话 id（空表示新会话）；needCreate: 是否要求上游新建会话；
// onDelta: 文本增量回调。
// [返回] 聚合结果与错误。
// 最近修改时间：2026-11-17 01:32:00；改动原因：新增续接点版主函数，本函数改为委托。
func collectCompletion(ctx context.Context, sa *StoredAuth, text string, mode generationMode, conversationID string, needCreate bool, onDelta func(string)) (*completionResult, error) {
	return collectCompletionWithResume(ctx, sa, text, mode, resumePoint{ConversationID: conversationID}, needCreate, onDelta)
}

// collectCompletionWithResume 执行一轮对话并聚合完整结果。
//
// 文本聚合口径（2026-10-17 实测确认）：回复正文分散在多种事件里，必须按到达顺序
// 累加——STREAM_MSG_NOTIFY 的首块、STREAM_CHUNK 的 patch、CHUNK_DELTA 的 text
// 各贡献一段，缺任何一类都会丢字。反过来，FULL_MSG_NOTIFY 携带的是**用户自己的
// 提问回显**（message.user_type=1），把它计入会把问题拼进回答；只有 user_type=2
// 的整消息才是助手回复，可作最终权威值覆盖累加结果。
//
// 续接点是视频两段式的前提：次轮的 agent_mode / conversation_mode /
// model_item_key 全部留空，上游只靠 conversation_id + section_id +
// message_index 找回上下文（2026-11-17 抓包确认）。三个字段缺一个都会让次轮
// 变成一条孤立的新消息，参数确认永远等不到「开始生成」。
//
// onDelta 非空时，每收到一段文本增量回调一次（流式调用方使用）。
// [参数] ctx: 上下文；sa: 账号；text: 用户文本；mode: 生成模式；
// resume: 会话续接点（零值表示新会话）；needCreate: 是否要求上游新建会话；
// onDelta: 文本增量回调。
// [返回] 聚合结果与错误。
// 最近修改时间：2026-11-17 01:32:00；改动原因：新增，会话续接点贯穿。
func collectCompletionWithResume(ctx context.Context, sa *StoredAuth, text string, mode generationMode, resume resumePoint, needCreate bool, onDelta func(string), params ...videoParams) (*completionResult, error) {
	// 1. 组装请求体：mode 决定激活视频 skill。
	body := buildCompletionRequest(sa, text, mode, resume.ConversationID, resume.SectionID, resume.MessageIndex, needCreate, params...)

	var (
		textBuf   strings.Builder
		fullText  string
		reasoning strings.Builder
		assets    []creationAsset
		convID    = resume.ConversationID
		sectionID = resume.SectionID
		msgIndex  = resume.MessageIndex
		streamErr error
	)

	// 2. 逐帧聚合：正文按到达顺序累加，并同步回调增量。
	appendText := func(piece string) {
		if piece == "" {
			return
		}
		textBuf.WriteString(piece)
		if onDelta != nil {
			onDelta(piece)
		}
	}

	// 3. 事件分发：按事件名各自解析，遇到错误帧或结束帧立即停止读取。
	handleEvent := func(ev completionEvent) bool {
		switch ev.Name {
		case "SSE_ACK":
			var ack struct {
				AckClientMeta struct {
					ConversationID string `json:"conversation_id"`
					SectionID      string `json:"section_id"`
				} `json:"ack_client_meta"`
				QueryList []struct {
					MessageIndex int64 `json:"message_index"`
				} `json:"query_list"`
			}
			if json.Unmarshal(ev.Data, &ack) == nil {
				if ack.AckClientMeta.ConversationID != "" {
					convID = ack.AckClientMeta.ConversationID
				}
				// 逐字段非空才覆盖：确认轮带着 section_id 进来，上游 ACK 里这个字段
				// 可能为空，无条件赋值会把有效的续接点抹成空串，客户端下一次续接
				// 就断了（attachResumePoint 遇到空串会整条省略）。
				if ack.AckClientMeta.SectionID != "" {
					sectionID = ack.AckClientMeta.SectionID
				}
				if len(ack.QueryList) > 0 {
					msgIndex = ack.QueryList[0].MessageIndex
				}
			}

		case "FULL_MSG_NOTIFY":
			// 整消息事件：user_type=1 是用户提问回显，必须忽略；
			// user_type=2 才是助手回复，作为最终权威值。
			var payload struct {
				Message struct {
					UserType     int             `json:"user_type"`
					ContentBlock []responseBlock `json:"content_block"`
					Content      string          `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(ev.Data, &payload) == nil && payload.Message.UserType == 2 {
				content := streamMessageContent{ContentBlock: payload.Message.ContentBlock}
				if t := content.textOf(); t != "" {
					fullText = t
				}
				if r := content.reasoningOf(); r != "" {
					reasoning.Reset()
					reasoning.WriteString(r)
				}
				assets = collectCreationAssets(content.ContentBlock, assets)
			}

		case "STREAM_MSG_NOTIFY":
			var payload struct {
				Content streamMessageContent `json:"content"`
			}
			if json.Unmarshal(ev.Data, &payload) == nil {
				appendText(payload.Content.textOf())
				if r := payload.Content.reasoningOf(); r != "" {
					reasoning.Reset()
					reasoning.WriteString(r)
				}
				assets = collectCreationAssets(payload.Content.ContentBlock, assets)
			}

		case "STREAM_CHUNK", "CHUNK_DELTA":
			var payload struct {
				Content streamMessageContent `json:"content"`
				PatchOp []patchOp            `json:"patch_op"`
				Delta   string               `json:"delta"`
				Text    string               `json:"text"`
			}
			if json.Unmarshal(ev.Data, &payload) != nil {
				break
			}

			// 同一帧可能同时携带多种形态，按优先级只取一种，避免重复。
			switch {
			case payload.Delta != "":
				appendText(payload.Delta)
			case payload.Text != "":
				appendText(payload.Text)
			default:
				for _, op := range payload.PatchOp {
					appendText(op.PatchValue.textOf())
				}
				appendText(payload.Content.textOf())
			}
			if r := payload.Content.reasoningOf(); r != "" {
				reasoning.WriteString(r)
			}
			// 生成结果可能出现在 patch_value 里（实测路径），也可能在 content 里。
			for _, op := range payload.PatchOp {
				assets = collectCreationAssets(op.PatchValue.blocks(), assets)
			}
			assets = collectCreationAssets(payload.Content.ContentBlock, assets)

		case "STREAM_ERROR", "ERROR":
			if e, ok := parseStreamError(ev.Data); ok {
				streamErr = &upstreamError{Code: e.ErrorCode, Message: streamErrorHint(e)}
				return false
			}

		case "SSE_REPLY_END":
			// 正文在首个 SSE_REPLY_END（end_type=1，消息已结束）之前全部到齐；
			// 其后的帧只是推荐追问等附属信息。立即停止读取，不等待 Keep-Alive 空闲连接。
			return false
		}
		return true
	}

	// 4. 发起流式请求并收尾：结束帧之后不再关心剩余字节。
	if errStream := streamCompletion(ctx, sa, body, handleEvent); errStream != nil {
		return nil, errStream
	}
	if streamErr != nil {
		return nil, streamErr
	}

	// 5. 定稿正文：助手整消息事件存在时以它为准，否则用增量累加结果。
	final := textBuf.String()
	if fullText != "" {
		final = fullText
	}
	// 生成类回复可能只有媒体没有正文；有媒体就不算空回复。
	if strings.TrimSpace(final) == "" && strings.TrimSpace(reasoning.String()) == "" && len(assets) == 0 {
		return nil, errors.New("上游没有返回内容")
	}
	return &completionResult{
		Text:           final,
		Reasoning:      reasoning.String(),
		ConversationID: convID,
		SectionID:      sectionID,
		MessageIndex:   msgIndex,
		Assets:         assets,
	}, nil
}
