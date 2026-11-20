package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 本文件实现视频产物的异步回灌。
//
// 图片是同步产物：同一次 /chat/completion 的 SSE 里 block_type=2074 就把直链带齐了。
// 视频不是——确认轮只回一句「本次使用 Seedance 2.0 Mini 生成，预计等待 5 分钟」，
// 真正的成品要等上游把一条助手消息写进会话消息链，再由客户端主动拉取：
//
//	POST /im/chain/single  {"cmd":3100,...}  →  downlink_body.pull_singe_chain_downlink_body.messages[]
//	                                            └ content_block[block_type=2074].content.creation_block.creations[].video
//
// 2026-11-17 在真实网页抓到的完整时序：确认轮结束后第 6~7 条请求就是两次
// /im/chain/single（anchor_index 分别为 9007199254740991 与 11），成品就在其中。
// 因此插件侧的做法不是「等 SSE 出视频」，而是「拿到 conversation_id 后拉消息链」。

// chainPullPath 是网页端拉取会话消息链的端点。
const chainPullPath = "/im/chain/single"

// 会话消息链的协议常量（2026-11-17 抓包值）。
const (
	// chainPullCmd 是「拉取消息链」的命令号。
	chainPullCmd = 3100
	// chainConversationTypeTask 是会话类型：3 = 会话（含创作任务）。
	chainConversationTypeTask = 3
	// chainPullChannel 是网页端使用的通道号。
	chainPullChannel = 2
	// chainPullVersion 是协议版本。
	chainPullVersion = "1"
	// chainPullAnchorLatest 表示从最新一条往前拉。
	chainPullAnchorLatest = int64(9007199254740991)
	// chainPullDefaultLimit 是一次拉取的条数上限。
	chainPullDefaultLimit = 20
	// chainPullScene 与 chainPullScenario 是网页端固定上报的场景标识，
	// 换成别的值上游不会报错，但可能不走正常的同步路径。
	chainPullScene    = "multi_device_red_dot_sync"
	chainPullScenario = "BatchSend|flow.agent.creation"
)

// videoPollInterval 是轮询消息链的间隔。
//
// 真实网页在 150 秒左右拿到成品，这里取 10 秒一轮：既不会把上游打出高频，
// 也能在成品就绪后一个间隔内返回。
//
// 是变量不是常量，只为让用例把等待压到毫秒级（见 swapVideoPollTiming）：
// 默认值下任何「不该等待却等待」的缺陷都要跑满 6 分钟才暴露，验证成本过高。
var videoPollInterval = 10 * time.Second

// videoPollTimeoutDefault 是等待视频产物的兜底总时长。
//
// 网页提示「预计等待 5 分钟」，实测 150 秒出片；给到 6 分钟留出上游排队余量。
// 正常路径读配置项 video_poll_timeout_seconds，这里只在该配置缺失时兜底。
const videoPollTimeoutDefault = 6 * time.Minute

// videoPollTimeoutOverride 让用例把总时长压到毫秒级（见 swapVideoPollTiming）。
var videoPollTimeoutOverride time.Duration

// videoCreationWaitDefault 是「等上游落下第一个创作块」的兜底上限。
//
// 为什么需要这个上限：上游拒答时照样返回 conversation_id，形态与正常首轮一致，
// 插件无法从回包区分「在渲染」与「根本没开始」。一旦没开始，光等成品只能等满
// videoPollTimeout（2026-11-21 生产实测白等 366 秒）。首个 2074 块必然早于成品，
// 用它当「是否真的开跑」的判据能把白等压到一个可接受的量级。
const videoCreationWaitDefault = 2 * time.Minute

// videoCreationWaitOverride 让用例把首块等待压到毫秒级。
var videoCreationWaitOverride time.Duration

// videoPollTimeoutValue 返回本次等待视频产物的有效总时长。
//
// 优先级：用例覆盖 > 配置项 video_poll_timeout_seconds > 内置兜底。
// [参数] 无。
// [返回] 有效总时长。
// 最近修改时间：2026-11-21 02:06:00；改动原因：视频等待不再写死，改为可配置。
func videoPollTimeoutValue() time.Duration {
	if videoPollTimeoutOverride > 0 {
		return videoPollTimeoutOverride
	}
	if secs := currentConfig().VideoPollTimeoutSeconds; secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return videoPollTimeoutDefault
}

// videoCreationWaitValue 返回本次「等首块出现」的有效上限。
// [参数] 无。
// [返回] 有效上限。
// 最近修改时间：2026-11-21 02:06:00；改动原因：新增，用于区分「在渲染」与「根本没开始」。
func videoCreationWaitValue() time.Duration {
	if videoCreationWaitOverride > 0 {
		return videoCreationWaitOverride
	}
	if secs := currentConfig().VideoCreationWaitSeconds; secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return videoCreationWaitDefault
}

// chainPullRequest 是 /im/chain/single 的请求体。
type chainPullRequest struct {
	Cmd        int             `json:"cmd"`
	UplinkBody chainPullUplink `json:"uplink_body"`
	SequenceID string          `json:"sequence_id"`
	Channel    int             `json:"channel"`
	Version    string          `json:"version"`
}

// chainPullUplink 是 uplink_body 段。
//
// 字段名里的 singe 是上游自己的拼写（少一个 l），照抄不要纠正——改成 single
// 会得到空消息链，而且不报错。
type chainPullUplink struct {
	PullBody chainPullBody `json:"pull_singe_chain_uplink_body"`
}

// chainPullBody 是实际的拉取参数。
type chainPullBody struct {
	ConversationID       string            `json:"conversation_id"`
	AnchorIndex          int64             `json:"anchor_index"`
	ConversationType     int               `json:"conversation_type"`
	Direction            int               `json:"direction"`
	Limit                int               `json:"limit"`
	Ext                  map[string]string `json:"ext"`
	Filter               chainPullFilter   `json:"filter"`
	EvaluateABParams     string            `json:"evaluate_ab_params"`
	EvaluateCommonParams string            `json:"evaluate_common_params"`
	Option               chainPullOption   `json:"option"`
}

// chainPullFilter 是消息过滤条件，网页端始终给空列表。
type chainPullFilter struct {
	IndexList []int `json:"index_list"`
}

// chainPullOption 是拉取选项。
type chainPullOption struct {
	LazyLoadStrategy int `json:"lazy_load_strategy"`
}

// chainPullResponse 是 /im/chain/single 的响应体。
type chainPullResponse struct {
	DownlinkBody struct {
		PullBody struct {
			Messages []chainMessage `json:"messages"`
			HasMore  bool           `json:"has_more"`
		} `json:"pull_singe_chain_downlink_body"`
	} `json:"downlink_body"`
	StatusCode int    `json:"status_code"`
	StatusDesc string `json:"status_desc"`
}

// chainMessage 是消息链里的一条消息。
//
// 只关心助手消息（user_type=2）：用户消息里不会有创作产物。
type chainMessage struct {
	UserType     int          `json:"user_type"`
	SectionID    string       `json:"section_id"`
	Index        int64        `json:"index"`
	ContentBlock []chainBlock `json:"content_block"`
}

// chainBlock 是消息里的一个内容块。
type chainBlock struct {
	BlockType int             `json:"block_type"`
	Content   json.RawMessage `json:"content"`
}

// buildChainPullRequest 组装一次消息链拉取请求。
//
// anchor_index 取最大值表示「从最新一条往前」，limit 给整段而不是 1：视频消息
// 可能不在最后一条（后面还会跟助手追加的补充说明），只拉 1 条会漏。
// [参数] conversationID: 会话 id；limit: 拉取条数（0 用默认值）。
// [返回] 请求体。
// 最近修改时间：2026-11-17 01:40:00；改动原因：新增，视频产物回灌。
func buildChainPullRequest(conversationID string, limit int) chainPullRequest {
	if limit <= 0 {
		limit = chainPullDefaultLimit
	}
	return chainPullRequest{
		Cmd: chainPullCmd,
		UplinkBody: chainPullUplink{PullBody: chainPullBody{
			ConversationID:   conversationID,
			AnchorIndex:      chainPullAnchorLatest,
			ConversationType: chainConversationTypeTask,
			Direction:        1,
			Limit:            limit,
			Ext: map[string]string{
				"pull_single_chain_scene": chainPullScene,
				"sync_scenario":           chainPullScenario,
			},
			Filter:               chainPullFilter{IndexList: []int{}},
			EvaluateABParams:     "",
			EvaluateCommonParams: "",
			Option:               chainPullOption{LazyLoadStrategy: 0},
		}},
		SequenceID: randomUUID(),
		Channel:    chainPullChannel,
		Version:    chainPullVersion,
	}
}

// assetsFromChainResponse 从消息链响应里提取创作产物。
//
// 只取助手消息：用户消息里可能回显确认文本，但不会有 creations。
//
// 第二个返回值 sawCreation 表示「这条会话的消息链里到底有没有出现过创作块」。
// 它必须单独返回：parseCreationBlock 会把「只有 status、没有 key/URL」的占位块
// 丢掉，所以「在渲染」与「根本没开始」两种情况的资产列表都是空，只看资产无法区分。
// [参数] raw: 响应原文。
// [返回] 产物列表、是否出现过创作块、错误。
// 最近修改时间：2026-11-21 02:07:00；改动原因：新增 sawCreation，用于区分在渲染与没开跑。
func assetsFromChainResponse(raw []byte) ([]creationAsset, bool, error) {
	if len(raw) == 0 {
		return nil, false, fmt.Errorf("消息链接口返回空响应")
	}
	var resp chainPullResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, false, fmt.Errorf("消息链响应不是合法 JSON：%w", errUnmarshal)
	}
	// 上游用 status_code 而非 HTTP 状态码表达业务失败。
	if resp.StatusCode != 0 {
		return nil, false, fmt.Errorf("消息链接口返回 %d：%s", resp.StatusCode, resp.StatusDesc)
	}

	var (
		out         []creationAsset
		sawCreation bool
	)
	for _, msg := range resp.DownlinkBody.PullBody.Messages {
		if msg.UserType != 2 {
			continue
		}
		for _, block := range msg.ContentBlock {
			if block.BlockType != blockTypeCreation {
				continue
			}
			// 只要块在，就算上游已经开跑，哪怕它还没产出直链。
			sawCreation = true
			out = append(out, parseCreationBlock(block.Content)...)
		}
	}
	return out, sawCreation, nil
}

// videoAssetAmong 从产物列表里挑出第一个可用的视频。
// [参数] assets: 产物列表。
// [返回] 视频产物与是否存在。
// 最近修改时间：2026-11-17 01:40:00；改动原因：新增，取视频而非图片。
func videoAssetAmong(assets []creationAsset) (creationAsset, bool) {
	for _, asset := range assets {
		if asset.isVideo() && asset.URL != "" {
			return asset, true
		}
	}
	return creationAsset{}, false
}

// postChainJSON 向 /im/* 族端点发一次 JSON POST。
//
// 不能复用对话链路那份请求头：/chat/completion 用的是纯 application/json，而
// /im/* 族要求 "application/json; encoding=utf-8"，缺 encoding 声明会被上游以
// 712012002「不支持编码类型」直接拒绝（2026-11-17 实测）。两族端点的要求正好相反，
// 共用一份请求头必然有一边失败。
// [参数] ctx: 上下文；sa: 账号；path: 端点路径；raw: 已编码的请求体。
// [返回] 响应体与错误。
// 最近修改时间：2026-11-18 23:35:00；改动原因：注释不再引用已删除的函数名。
func postChainJSON(ctx context.Context, sa *StoredAuth, path string, raw []byte) ([]byte, error) {
	url := endpointURL(sa, path, nil)
	if businessEndpointOverride != "" {
		url = businessEndpointOverride + path
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if errReq != nil {
		return nil, fmt.Errorf("构造消息链请求失败：%w", errReq)
	}
	req.Header.Set("Content-Type", contentTypeIM)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	applyUpstreamHeaders(req)

	client, jar := newUpstreamClient(sa, time.Duration(currentConfig().TimeoutSeconds)*time.Second)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("请求消息链接口失败：%w", errDo)
	}
	defer resp.Body.Close()
	defer func() {
		_ = jar.persist(sa)
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取消息链响应失败：%w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("消息链接口返回 HTTP %d：%s", resp.StatusCode, truncateText(string(body), 200))
	}
	return body, nil
}

// fetchChainAssets 拉一次消息链并解析产物。
// [参数] ctx: 上下文；sa: 账号；conversationID: 会话 id。
// [返回] 产物列表、是否出现过创作块、错误。
// 最近修改时间：2026-11-21 02:08:00；改动原因：透传 sawCreation，供首块等待判据使用。
func fetchChainAssets(ctx context.Context, sa *StoredAuth, conversationID string) ([]creationAsset, bool, error) {
	body, errMarshal := json.Marshal(buildChainPullRequest(conversationID, 0))
	if errMarshal != nil {
		return nil, false, fmt.Errorf("构造消息链请求失败：%w", errMarshal)
	}
	raw, errPost := postChainJSON(ctx, sa, chainPullPath, body)
	if errPost != nil {
		return nil, false, errPost
	}
	return assetsFromChainResponse(raw)
}

// ensureVideoAsset 在视频确认轮补齐异步产物。
//
// 只有确认轮才可能产生产物：首轮的 SSE 只是一段「请确认参数」的文本，上游根本没
// 有开跑，此时去轮询消息链只会白等满 videoPollTimeout 再报超时（2026-11-17 02:33
// 生产实测：首轮等满 6 分钟回 503）。所以判据是「这一次是不是确认轮」，而不是
// 「是不是视频模式」—— 两者在首轮同时成立，只看模式必然误入。
//
// 超时取 videoPollTimeoutValue() 外加一分钟余量：调用方传进来的 ctx 已在上游流
// 结束时被取消，直接复用会让回灌立刻返回 context canceled。余量必须基于同一个
// 有效值算，否则配置调大后上下文会先于轮询超时触发，报出来的是 context deadline
// exceeded 而不是业务文案。
// [参数] sa: 账号；mode: 生成模式；resume: 本次请求的续接点；res: 待补齐的执行结果。
// [返回] 错误（视频等不到成品时返回）。
// 最近修改时间：2026-11-21 02:09:00；改动原因：总时长改读配置，与对话超时分离。
func ensureVideoAsset(sa *StoredAuth, mode generationMode, resume resumePoint, res *completionResult) error {
	if res == nil || mode.Kind != "video" || resume.isZero() || len(res.Assets) > 0 || res.ConversationID == "" {
		return nil
	}
	pollTimeout := videoPollTimeoutValue()
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout+time.Minute)
	defer cancel()
	asset, errWait := waitVideoAsset(ctx, sa, res.ConversationID)
	if errWait != nil {
		return errWait
	}
	res.Assets = append(res.Assets, asset)
	return nil
}

// chainPollAccountHook 是回灌账号的观察点（仅测试使用）。
//
// 为什么需要它：回灌用的是 Cookie，而 Cookie 的 Domain 绑定在 doubao 站点上，
// 用例里的 httptest 服务收不到这些 Cookie，无法从请求头反推「用了哪个账号」。
// 这个钩子把实际入参暴露出来，让「回灌必须用实际执行账号」这条约束可以被断言。
var chainPollAccountHook func(*StoredAuth)

// waitVideoAsset 轮询消息链直到拿到视频直链或超时。
//
// 为什么要轮询而不是等 SSE：确认轮的 SSE 只回一段「正在生成」的文本就结束了，
// 成品由上游异步写入会话消息链，没有任何事件会推给本次连接。
//
// 为什么要额外等「首块」：上游拒答时照样返回 conversation_id，形态与正常确认轮
// 一致，插件从回包分不出「在渲染」与「根本没开始」。没开始时干等成品只能等满
// video_poll_timeout_seconds（2026-11-21 生产实测白等 366 秒）。首个创作块必然
// 早于成品出现，用它当判据可以把「没开跑」的失败提前到 video_creation_wait_seconds。
// [参数] ctx: 上下文（取消即返回）；sa: 账号；conversationID: 会话 id。
// [返回] 视频产物与错误。
// 最近修改时间：2026-11-21 02:10:00；改动原因：新增首块等待，拒答态不再白等满总时长。
func waitVideoAsset(ctx context.Context, sa *StoredAuth, conversationID string) (creationAsset, error) {
	if chainPollAccountHook != nil {
		chainPollAccountHook(sa)
	}
	if conversationID == "" {
		return creationAsset{}, fmt.Errorf("视频已提交但会话 id 为空，无法回灌产物")
	}
	pollTimeout := videoPollTimeoutValue()
	creationWait := videoCreationWaitValue()
	started := time.Now()
	deadline := started.Add(pollTimeout)
	creationDeadline := started.Add(creationWait)
	var sawCreation bool
	// 第一轮立刻拉一次：短时长视频常在确认轮结束前就已入库。
	for {
		assets, saw, errFetch := fetchChainAssets(ctx, sa, conversationID)
		if errFetch == nil {
			sawCreation = sawCreation || saw
			if asset, ok := videoAssetAmong(assets); ok {
				return asset, nil
			}
		}
		// 首块等待只在「还没见过创作块」时生效：见过就说明上游确实开跑了，
		// 后面多慢都属于正常等待，直接放到总超时。
		if !sawCreation && !time.Now().Before(creationDeadline) {
			if errFetch != nil {
				return creationAsset{}, fmt.Errorf("上游没有开始渲染（%v 内消息链里没有任何创作块，最后一次拉取失败：%v）：这个账号当前可能无法出片，请先用面板「测试模型」确认，或换账号重试", creationWait, errFetch)
			}
			return creationAsset{}, fmt.Errorf("上游没有开始渲染（%v 内消息链里没有任何创作块）：这个账号当前可能无法出片，请先用面板「测试模型」确认，或换账号重试", creationWait)
		}
		// 拉取失败不立即退出：上游偶发返回非零 status_code，下一轮通常就正常。
		if time.Now().After(deadline) {
			if errFetch != nil {
				return creationAsset{}, fmt.Errorf("等待视频超时，最后一次拉取失败：%w", errFetch)
			}
			return creationAsset{}, fmt.Errorf("等待视频超时（%v）：上游仍未产出成品，稍后可用同一 conversation_id 重试", pollTimeout)
		}
		select {
		case <-ctx.Done():
			return creationAsset{}, ctx.Err()
		case <-time.After(videoPollInterval):
		}
	}
}
