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
	"strconv"
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
type completionRequest struct {
	ClientMeta  clientMeta       `json:"client_meta"`
	Messages    []completionMsg  `json:"messages"`
	Option      completionOption `json:"option"`
	UserContext []any            `json:"user_context"`
	// Ext 是技能模式的顶层扩展（agent_mode=2 专用）；工作模式把扩展放进
	// option.ext，顶层留空即不输出，避免两种形态同时出现。
	Ext map[string]string `json:"ext,omitempty"`
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
	LocalConversationID string       `json:"local_conversation_id"`
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
	AgentMode         int    `json:"agent_mode"`
	TTSSwitch         bool   `json:"tts_switch"`
	NeedDeepThink     int    `json:"need_deep_think"`

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

	// ConversationMode 是**整数**，写字符串会被上游以 710020202
	// "common invalid param" 拒绝（HTTP 200 + SSE 错误帧，心跳先到、随后报错）。
	// 实测：写成 "2" 必错，写 2 或不写都正常；aggregate_params 里的同名字段
	// 反而是字符串，两处类型不同，不可互换。
	ConversationMode int `json:"conversation_mode,omitempty"`

	// ConversationInitOption 是会话初始化确认项，工作模式必带，缺它上游不建会话。
	ConversationInitOption *conversationInitOption `json:"conversation_init_option,omitempty"`

	// ConversationInitExt 承载模型主键与 agent 任务参数，工作模式必带。
	ConversationInitExt *conversationInitExt `json:"conversation_init_ext,omitempty"`

	// GeneralTaskParam 是工作模式的 agent 任务参数，技能模式不出现。
	GeneralTaskParam *generalTaskParam `json:"general_task_param,omitempty"`

	// IsOldUser 标记老用户，工作模式必带且恒为 true。
	IsOldUser bool `json:"is_old_user,omitempty"`

	// MessageStorageType 是消息存储类型，工作模式固定 0。
	MessageStorageType int `json:"message_storage_type"`

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
}

// recoveryOption 是网页端的恢复选项段。
type recoveryOption struct {
	IsRecovery          bool  `json:"is_recovery"`
	ReqCreateTimeSec    int64 `json:"req_create_time_sec"`
	AppendSSEEventScene int   `json:"append_sse_event_scene"`
}

// conversationInitOption 是会话初始化确认项。
type conversationInitOption struct {
	ProjectID           string `json:"project_id"`
	NeedAckConversation bool   `json:"need_ack_conversation"`
}

// conversationInitExt 是会话初始化扩展参数。
type conversationInitExt struct {
	ModelItemKey    string `json:"model_item_key"`
	ReasoningEffort string `json:"reasoning_effort"`
	ModeID          string `json:"mode_id"`
	AgentTaskParam  string `json:"agent_task_param"`
}

// agentTaskParam 描述 agent 运行环境，工作模式在 initExt 与 general_task_param 两处复用。
//
// 网页来源必须六个字段全部留空（runtime_type 为 1）：网页版没有本地沙箱，
// 填上客户端字段会被上游判成「桌面端协议」并与实际来源不符。
type agentTaskParam struct {
	RuntimeType   int    `json:"runtime_type"`
	FolderName    string `json:"folder_name"`
	DeviceName    string `json:"device_name"`
	Workspace     string `json:"workspace"`
	LocalAppID    string `json:"local_app_id"`
	LocalDeviceID string `json:"local_device_id"`
}

// generalTaskParam 是工作模式的任务参数总成。
//
// 网页版只上报这六个字段；客户端专属的 client_option / task_input_json /
// agent_task_param_change / need_modify_conversation 一律不出现。
type generalTaskParam struct {
	Action               int            `json:"action"`
	ThreadLocalMessageID []string       `json:"thread_local_message_id"`
	ProjectID            string         `json:"project_id"`
	RuntimeType          int            `json:"runtime_type"`
	AgentTaskParam       agentTaskParam `json:"agent_task_param"`
	ServerDoCompare      bool           `json:"server_do_compare"`
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
// mode.WorkMode 决定走哪条上游管线：true 时按客户端工作模式构造
// （agent_mode=1，模型由 model_item_key 决定）；false 时按网页端技能模式构造
// （agent_mode=2，靠 mention_skill_list 触发生成管线）。
// [参数] sa: 账号（提供 bot_id）；text: 用户文本；mode: 生成模式；
// conversationID / lastSectionID / lastMessageIndex: 会话续接点；
// needCreate: 是否请求上游分配新会话。
// [返回] 请求体。
// 最近修改时间：2026-11-14 02:30:00；改动原因：拆分工作模式与技能模式两条构造分支。
func buildCompletionRequest(sa *StoredAuth, text string, mode generationMode, conversationID, lastSectionID string, lastMessageIndex int64, needCreate bool) *completionRequest {
	cfg := currentConfig()
	botID := strings.TrimSpace(sa.BotID)
	if botID == "" {
		botID = strings.TrimSpace(cfg.WorkBotID)
	}
	if botID == "" {
		botID = defaultBotID
	}

	// 1. 先落地消息块：工作模式的 general_task_param 必须回指同一个消息 id。
	localMessageID := randomUUID()
	// 新会话（未给续接点）显式发 null，与浏览器一致。
	var lastIndex *int64
	if lastMessageIndex > 0 {
		lastIndex = &lastMessageIndex
	}
	body := &completionRequest{
		ClientMeta: clientMeta{
			LocalConversationID: "local_" + randomUUID(),
			ConversationID:      conversationID,
			BotID:               botID,
			LastSectionID:       lastSectionID,
			LastMessageIndex:    lastIndex,
			LocalPermissions:    localPermissionsFor(mode.WorkMode),
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
				Content: requestBlockBody{
					TextBlock: &textBlockPayload{Text: text},
				},
			}},
		}},
		UserContext: []any{},
	}

	// 2. 按管线分别组装 option：两条管线的扩展都放在顶层 ext。
	if mode.WorkMode {
		body.Option = buildWorkOption(cfg, sa, mode, localMessageID, needCreate)
		generalTaskJSON, _ := json.Marshal(body.Option.GeneralTaskParam)
		body.Ext = workModeExt(string(generalTaskJSON))
		return body
	}
	body.Option = buildSkillOption(mode, needCreate)
	body.Ext = skillExt()
	return body
}

// localPermissionsFor 返回本地权限声明列表。
//
// 两条管线都上报 status=3：这是网页端真实取值（浏览器抓包 confirmed）。
// 旧实现工作模式报 1，来自「客户端已授权」的错误推断，会让网页来源的
// 工作模式请求被判成客户端协议（-1 delegated input protocol violation）。
// [参数] workMode: 是否工作模式（保留入参以表明取值与管线无关）。
// [返回] 权限声明列表。
// 最近修改时间：2026-11-16 01:44:00；改动原因：统一上报 3，与真实网页对齐。
func localPermissionsFor(workMode bool) []permission {
	// 1. 两条管线同值；保留入参只是为了让调用点语义清楚。
	_ = workMode
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
// agent_mode 2；生图/生视频靠 mention_skill_list 激活。
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
//
// 该字典只有技能链路使用；工作模式的 ext 在 option 内部，两者互斥。
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

// buildWorkOption 构造工作模式（agent_mode=1）的 option 段。
//
// 六个必填项缺一不可：agent_mode=1、conversation_mode=2、need_deep_think=10001、
// conversation_init_option、conversation_init_ext、general_task_param。
// model_item_key 必须同时出现在 conversation_init_ext、model_config、
// aggregate_params 三处，任一处缺失或不一致都会让上游回落到账号默认模型。
// [参数] cfg: 运行期配置；mode: 生成模式（提供模型主键与档位）；
// localMessageID: 本轮消息 id，供 general_task_param 回指；needCreate: 是否新建会话。
// [返回] option 段。
// 最近修改时间：2026-11-14 02:30:00；改动原因：新增，落地豆包客户端工作模式协议。
func buildWorkOption(cfg Config, sa *StoredAuth, mode generationMode, localMessageID string, needCreate bool) completionOption {
	modeID := strings.TrimSpace(cfg.WorkModeID)
	if modeID == "" {
		modeID = defaultWorkModeID
	}
	effort := strings.TrimSpace(mode.ReasoningEffort)
	if effort == "" {
		effort = defaultWorkReasoningEffort
	}
	memoryProfile := strings.TrimSpace(mode.MemoryProfile)
	if memoryProfile == "" {
		memoryProfile = cfg.WorkMemoryProfile
	}
	totalWindow := strings.TrimSpace(mode.TotalWindow)
	if totalWindow == "" {
		totalWindow = cfg.WorkTotalWindow
	}

	// 1. 组装 agent 任务参数：网页来源六字段全空，runtime_type 固定 1。
	taskParam := agentTaskParam{RuntimeType: 1}
	taskParamJSON, _ := json.Marshal(taskParam)

	// 2. 组装 option 主体：三处 model_item_key 必须取同一个值。
	option := completionOption{
		CreateTimeMS:           nowMillis(),
		AnswerWithSuggest:      false,
		AgentMode:              1,
		NeedDeepThink:          needDeepThinkOn,
		UniqueKey:              randomUUID(),
		StartSeq:               0,
		NeedCreateConversation: needCreate,
		ConversationMode:       workConversationMode,
		IsOldUser:              true,
		MessageStorageType:     0,
		ConversationInitOption: &conversationInitOption{NeedAckConversation: true},
		ConversationInitExt: &conversationInitExt{
			ModelItemKey:    mode.ModelItemKey,
			ReasoningEffort: effort,
			ModeID:          modeID,
			AgentTaskParam:  string(taskParamJSON),
		},
		ModelConfig: modelConfig{
			ModelItemKey: mode.ModelItemKey,
			ModelExtraParams: map[string]any{
				"memory_profile":    memoryProfile,
				"total_window_size": totalWindow,
			},
			ReasoningEffort: atoiSafe(effort),
		},
		AggregateParams: aggregateParams{
			MentionSkillList:  "[]",
			MentionPluginList: "[]",
			MentionExt:        "[{}]",
			ConversationMode:  strconv.Itoa(workConversationMode),
			ModeID:            modeID,
			ModelItemKey:      mode.ModelItemKey,
			AgentMode:         "1",
			ReasoningEffort:   effort,
			ProviderID:        "",
		},
	}
	option.SSERecvEventOptions.SupportChunkDelta = true
	option.SupportLazyFetchStream = true

	// 3. 补齐网页端每次都会显式发送的默认值段：缺这些键上游按「委托输入协议不符」
	//    直接回 -1（2026-11-16 消融：只补这些默认值就让 -1 变回 710022004）。
	option.ConnectorInfoList = []any{}
	option.EditQueryID = []any{}
	option.RegenQueryID = []any{}
	option.RelatedDeletedMessage = map[string]any{}
	option.RecoveryOption = recoveryOption{
		IsRecovery:          false,
		ReqCreateTimeSec:    time.Now().Unix(),
		AppendSSEEventScene: 0,
	}

	// 4. 组装 general_task_param；网页来源不带任何客户端沙箱字段。
	option.GeneralTaskParam = &generalTaskParam{
		Action:               0,
		ThreadLocalMessageID: []string{localMessageID},
		ProjectID:            "",
		RuntimeType:          1,
		AgentTaskParam:       taskParam,
		ServerDoCompare:      true,
	}
	return option
}

// workModeExt 返回工作模式的顶层 ext 段。
//
// 网页版把扩展放在**顶层** ext（不是 option.ext），共 8 键；缺 sub_conv_firstmet_type
// 会被上游判成非网页来源。conversation_init_option 必须是带 project_id 的 JSON 串。
// [参数] generalTaskJSON: 已序列化的 general_task_param 串。
// [返回] 顶层 ext 字典。
// 最近修改时间：2026-11-16 01:45:00；改动原因：新增，工作模式扩展搬到顶层并与真实网页对齐。
func workModeExt(generalTaskJSON string) map[string]string {
	return map[string]string{
		"general_task_param":            generalTaskJSON,
		"use_deep_think":                strconv.Itoa(needDeepThinkOn),
		"agent_mode":                    "1",
		"conversation_init_option":      "{\"project_id\":\"\",\"need_ack_conversation\":true}",
		"sub_conv_firstmet_type":        "1",
		"collection_id":                 "",
		"is_finish":                     "1",
		"commerce_credit_config_enable": "0",
	}
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
// 两条管线都用网页端 UA 与单值 agw-js-conv：工作模式在网页版里就是网页来源，
// 换客户端 UA 会被上游按「桌面端协议」解析请求体，与实际 body 形状不符，
// 直接回 -1 delegated input protocol violation（2026-11-16 实测）。
// [参数] req: 请求；workMode: 是否工作模式（保留入参以表明取值与管线无关）。
// [返回] 无。
// 最近修改时间：2026-11-16 01:46:00；改动原因：取消客户端指纹头，统一按网页端上报。
func applyUpstreamHeaders(req *http.Request, workMode bool) {
	// 1. 两条管线同源同版本，只上报网页端那一套。
	_ = workMode
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
// 两条管线都用网页端参数集：工作模式在浏览器里就是网页来源（aid=497858、
// doubao_device_platform=web、web_platform=browser）。用客户端参数集会让上游
// 按桌面端协议解析，与实际 body 形状不符，直接回 -1（2026-11-16 实测）。
//
// 两条管线都必须补齐字节风控签名（msToken + a_bogus），否则上游直接回
// 710022002「当前服务访问频繁」，与账号状态无关（见 sign.go）。
// [参数] sa: 账号（提供设备级标识与 msToken）；workMode: 是否工作模式（保留入参以表明取值与管线无关）；body: 即将发送的请求体（a_bogus 覆盖 body）。
// [返回] 完整 URL。
// 最近修改时间：2026-11-16 03:16:00；改动原因：a_bogus 把请求体算进签名，签名必须拿到真实 body。
func completionURL(sa *StoredAuth, workMode bool, body []byte) string {
	// 1. 测试覆盖点优先，保证流式链路能在 httptest 下验证。
	if completionEndpointOverride != "" {
		return completionEndpointOverride
	}

	// 2. 两条管线同一参数集；签名在参数集之后做，先签再改会失效。
	_ = workMode
	return signChatCompletionURL(sa, endpointURL(sa, "/chat/completion", nil), body)
}

// streamCompletion 发起一次 /chat/completion 请求并逐帧回调。
//
// 流式请求不设客户端超时：回复可能持续数分钟，由 ctx 的 deadline 约束。
// [参数] ctx: 上下文；sa: 账号；body: 请求体；onEvent: 事件回调（返回 false 提前结束）。
// [返回] 请求或读取错误。
// 最近修改时间：2026-10-17；改动原因：新增，SSE 上游调用。
func streamCompletion(ctx context.Context, sa *StoredAuth, body *completionRequest, onEvent func(completionEvent) bool) error {
	// 1. 由请求体反推管线：agent_mode=1 即工作模式，URL 与请求头都要跟着切换。
	workMode := body.Option.AgentMode == 1
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return fmt.Errorf("编码请求失败：%w", errMarshal)
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(sa, workMode, raw), bytes.NewReader(raw))
	if errReq != nil {
		return fmt.Errorf("构造请求失败：%w", errReq)
	}
	// 与 /im/* 相反：本端点只接受纯 application/json。
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "text/event-stream")
	applyUpstreamHeaders(req, workMode)

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

// postBusinessJSON 向业务端点发一次 JSON POST 并返回原始响应体。
//
// 与 streamCompletion 的区别：这些端点（如无水印解析）返回普通 JSON 而非 SSE。
// [参数] ctx: 上下文；sa: 账号；path: 端点路径；raw: 已编码的请求体。
// [返回] 响应体与错误。
// 最近修改时间：2026-10-17；改动原因：新增，非流式业务端点调用。
func postBusinessJSON(ctx context.Context, sa *StoredAuth, path string, raw []byte) ([]byte, error) {
	// 1. 业务端点固定拼网页端基址与网页端指纹，测试可整体覆盖基址。
	url := endpointURL(sa, path, nil)
	if businessEndpointOverride != "" {
		url = businessEndpointOverride + path
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if errReq != nil {
		return nil, fmt.Errorf("构造请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", "application/json")
	applyUpstreamHeaders(req, false)

	// 2. 请求结束后把滚动下发的 Cookie 落盘，失败不影响本次结果。
	client, jar := newUpstreamClient(sa, time.Duration(currentConfig().TimeoutSeconds)*time.Second)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("请求上游失败：%w", errDo)
	}
	defer resp.Body.Close()
	defer func() {
		_ = jar.persist(sa)
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取上游响应失败：%w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("上游返回 HTTP %d：%s", resp.StatusCode, truncateText(string(body), 200)),
		}
	}
	return body, nil
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

// collectCompletion 执行一轮对话并聚合完整结果。
//
// 文本聚合口径（2026-10-17 实测确认）：回复正文分散在多种事件里，必须按到达顺序
// 累加——STREAM_MSG_NOTIFY 的首块、STREAM_CHUNK 的 patch、CHUNK_DELTA 的 text
// 各贡献一段，缺任何一类都会丢字。反过来，FULL_MSG_NOTIFY 携带的是**用户自己的
// 提问回显**（message.user_type=1），把它计入会把问题拼进回答；只有 user_type=2
// 的整消息才是助手回复，可作最终权威值覆盖累加结果。
//
// onDelta 非空时，每收到一段文本增量回调一次（流式调用方使用）。
// [参数] ctx: 上下文；sa: 账号；text: 用户文本；mode: 生成模式；
// conversationID: 会话 id（空表示新会话）；needCreate: 是否要求上游新建会话；
// onDelta: 文本增量回调。
// [返回] 聚合结果与错误。
// 最近修改时间：2026-10-17；改动原因：新增，TASK-004 流解析聚合。
func collectCompletion(ctx context.Context, sa *StoredAuth, text string, mode generationMode, conversationID string, needCreate bool, onDelta func(string)) (*completionResult, error) {
	// 1. 组装请求体：mode 决定走工作模式还是技能模式管线。
	body := buildCompletionRequest(sa, text, mode, conversationID, "", 0, needCreate)

	var (
		textBuf   strings.Builder
		fullText  string
		reasoning strings.Builder
		assets    []creationAsset
		convID    = conversationID
		sectionID string
		msgIndex  int64
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
				sectionID = ack.AckClientMeta.SectionID
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
	// 6. 生成结果补全直链：hydrate 失败不影响已拿到的资产标识。
	if len(assets) > 0 {
		assets = hydrateAssets(ctx, sa, assets)
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
