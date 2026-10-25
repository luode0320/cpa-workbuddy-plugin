package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// handleExecExecute 处理非流式请求。
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	cfg := currentConfig()
	upstreamModel := mappedModel(req.Model, cfg)
	startTime := time.Now()

	sa := resolveTargetAuth(req.AuthID, req.StorageJSON, upstreamModel)
	curPayload := mutateModel(req.Payload, upstreamModel)

	var lastErr error
	var finalRespBytes []byte
	var finalStatusCode int
	// finalTTFTNS 保留最终成功尝试的首字节时延，供 publishUsage 与慢速冷却使用。
	var finalTTFTNS uint64

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层会跳过它们；本请求若仍落到这里说明上游刚恢复或该组合尚未被
	// 标记，按正常流程发起即可。

	// 最多 3 次换号重试
	for attempt := 0; attempt < 3; attempt++ {
		if sa == nil || sa.APIKey == "" {
			lastErr = fmt.Errorf("no available zcode account")
			break
		}

		respBytes, status, attemptTTFT, err := executeOnceTimed(curPayload, sa, cfg)
		finalStatusCode = status
		finalTTFTNS = attemptTTFT
		if err == nil {
			finalRespBytes = respBytes
			lastErr = nil
			// 一次真实成功即证明该模型已恢复，清掉该模型上残留的冷却。
			resetModelBlockedState(upstreamModel)
			break
		}

		lastErr = err
		noteAccountFailureForModel(sa.AuthID, upstreamModel, status, err.Error())

		// 11128：命中即写入账号×模型冷却并停止换号。上游拦截的是这个
		// 组合，其余账号大概率同样命中；冷却探活会在到期前回放验证。
		if isModelBlockedError(status, err.Error()) {
			noteModelBlockedHit(sa.AuthID, upstreamModel)
			lastErr = fmt.Errorf("upstream %d: %s", status, err.Error())
			break
		}

		if !shouldRotateOnUpstreamErr(status, err.Error()) {
			break
		}

		// 尝试换下一个可用账号（跳过同模型冷却中的账号）
		nextSA, hasNext := pickNextAuth(sa.AuthID, upstreamModel)
		if !hasNext || nextSA == nil {
			break
		}
		sa = nextSA
	}

	durationMS := time.Since(startTime).Milliseconds()
	authID := ""
	accountLabel := ""
	if sa != nil {
		authID = sa.AuthID
		accountLabel = sa.Label
		if accountLabel == "" {
			accountLabel = sa.Username
		}
	}

	if lastErr != nil {
		publishUsage(authID, accountLabel, req.Model, upstreamModel, startTime, usageTokensDetail{}, durationMS, finalStatusCode, true, lastErr.Error(), 0)
		// 返回给宿主的错误会进宿主日志，出口前脱敏；上面的分类判定与记账
		// 已用原始报文完成，不受影响。
		return nil, fmt.Errorf("%s", redactSecrets(lastErr.Error()))
	}

	// 提取用量
	inTokens := gjson.GetBytes(finalRespBytes, "usage.input_tokens").Int()
	outTokens := gjson.GetBytes(finalRespBytes, "usage.output_tokens").Int()
	detail := usageTokensDetail{
		InputTokens:  inTokens,
		OutputTokens: outTokens,
		TotalTokens:  inTokens + outTokens,
	}
	publishUsage(authID, accountLabel, req.Model, upstreamModel, startTime, detail, durationMS, finalStatusCode, false, "", finalTTFTNS)
	resetAccountFailover(authID)
	// 慢速观测：只记真实成功请求的首字延迟，样本达标时判定该账号在该模型上
	// 是否显著慢于同池水平。失败尝试的耗时不计入，避免污染样本。
	noteSlowObservation(authID, upstreamModel, finalTTFTNS)

	return okEnvelope(pluginapi.ExecutorResponse{Payload: finalRespBytes})
}

// handleExecStream 处理流式请求。
// [参数] raw: 宿主传入的流式执行请求 JSON。
// [返回] 成功返回含响应头与流标识的信封 JSON；序列化失败返回 error。
// 最近修改时间：2026-10-14 02:30:00 上游请求改用无整体超时的推理客户端，避免长思考流被掐断
func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	cfg := currentConfig()
	upstreamModel := mappedModel(req.Model, cfg)
	startTime := time.Now()

	sa := resolveTargetAuth(req.AuthID, req.StorageJSON, upstreamModel)
	curPayload := mutateModel(req.Payload, upstreamModel)

	var lastStreamErr error
	var finalResp *http.Response
	var finalCancel context.CancelFunc

	// 模型级拦截不再有独立的全局闸门：被拦截的账号×模型已写入冷却，
	// 路由层会跳过它们，本请求正常发起即可。

	for attempt := 0; attempt < 3; attempt++ {
		if sa == nil || sa.APIKey == "" {
			lastStreamErr = fmt.Errorf("no available zcode account")
			break
		}

		ctx, cancel := context.WithCancel(context.Background())
		httpReq, err := prepareUpstreamRequest(ctx, curPayload, sa, cfg)
		if err != nil {
			cancel()
			lastStreamErr = err
			break
		}

		resp, err := inferenceHTTPClient.Do(httpReq)
		if err != nil {
			cancel()
			lastStreamErr = err
			noteAccountFailureForModel(sa.AuthID, upstreamModel, 0, err.Error())
			nextSA, hasNext := pickNextAuth(sa.AuthID, upstreamModel)
			if !hasNext || nextSA == nil {
				break
			}
			sa = nextSA
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			cancel()
			// 这里必须保留完整上游报文：isModelBlockedError 依赖在正文里查找
			// 11128，提前截断会把风控码切掉导致误判。脱敏统一在出口
			// （streamEmitError / publishUsage）完成。
			errMsg := fmt.Sprintf("upstream status %d: %s", resp.StatusCode, string(body))
			lastStreamErr = fmt.Errorf("%s", errMsg)
			noteAccountFailureForModel(sa.AuthID, upstreamModel, resp.StatusCode, errMsg)

			// 11128：写入账号×模型冷却后停止换号，由冷却探活验证恢复。
			if isModelBlockedError(resp.StatusCode, errMsg) {
				noteModelBlockedHit(sa.AuthID, upstreamModel)
				break
			}

			if shouldRotateOnUpstreamErr(resp.StatusCode, errMsg) {
				nextSA, hasNext := pickNextAuth(sa.AuthID, upstreamModel)
				if hasNext && nextSA != nil {
					sa = nextSA
					continue
				}
			}
			break
		}

		finalResp = resp
		finalCancel = cancel
		lastStreamErr = nil
		break
	}

	durationMS := time.Since(startTime).Milliseconds()
	authID := ""
	accountLabel := ""
	if sa != nil {
		authID = sa.AuthID
		accountLabel = sa.Label
		if accountLabel == "" {
			accountLabel = sa.Username
		}
	}

	if lastStreamErr != nil {
		streamEmitError(req.StreamID, lastStreamErr.Error())
		streamClose(req.StreamID)
		publishUsage(authID, accountLabel, req.Model, upstreamModel, startTime, usageTokensDetail{}, durationMS, 500, true, lastStreamErr.Error(), 0)
		return okEnvelope(streamResponse{Headers: streamHeaders()})
	}

	// 异步读取并逐块推送
	go pumpUpstreamStream(context.Background(), finalCancel, req.StreamID, finalResp, sa.AuthID, accountLabel, req.Model, upstreamModel, startTime)

	return okEnvelope(streamResponse{Headers: streamHeaders()})
}

// resolveTargetAuth 按请求指定的 auth_id 解析目标账号，缺失时回退调度器选号。
// [参数] reqAuthID：宿主指定的账号标识；storageJSON：宿主随请求下发的凭据快照；
// model：已归一化的上游模型名，用于模型维度冷却判定，可为空。
// [返回] 目标账号；无可选账号时返回 nil。
// 最近修改时间：2026-10-19 02:00:00；改动原因：冷却判定带模型维度。
func resolveTargetAuth(reqAuthID string, storageJSON []byte, model string) *StoredAuth {
	if len(storageJSON) > 0 {
		var sa StoredAuth
		if err := json.Unmarshal(storageJSON, &sa); err == nil && strings.TrimSpace(sa.APIKey) != "" {
			if sa.AuthID == "" {
				sa.AuthID = authIDFor(sa.APIKey)
			}
			if accountRoutable(&sa, model) {
				return &sa
			}
		}
	}

	reqAuthID = strings.TrimSpace(reqAuthID)
	cleanAuthID := strings.TrimSuffix(reqAuthID, ".json")
	if reqAuthID != "" {
		accounts, err := listAllAuthFiles()
		if err == nil {
			for _, acc := range accounts {
				if acc.AuthID == reqAuthID || acc.AuthID == cleanAuthID || authFileNameFor(acc.APIKey) == cleanAuthID+".json" {
					if accountRoutable(acc, model) {
						return acc
					}
				}
			}
		}
	}

	// 从调度器选取健康账号
	if sa := pickActiveAuth(model); sa != nil {
		return sa
	}

	// 兜底：若全在冷却中，选用未禁用的有效账号避免死锁
	accounts, err := listAllAuthFiles()
	if err == nil && len(accounts) > 0 {
		for _, acc := range accounts {
			if !acc.Disabled && acc.APIKey != "" && !acc.TestFailed {
				return acc
			}
		}
	}

	// 兜底：若配置文件里写死了 api_key，构造单账号
	cfg := currentConfig()
	if cfg.APIKey != "" {
		return &StoredAuth{
			AuthID:   "zcode-config-static",
			APIKey:   cfg.APIKey,
			Label:    "Config API Key",
			Disabled: false,
		}
	}

	return nil
}

func prepareUpstreamRequest(ctx context.Context, payload []byte, sa *StoredAuth, cfg Config) (*http.Request, error) {
	targets := resolveUpstreamTargets(sa, cfg)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no usable upstream credential for account %s", sa.AuthID)
	}
	target := targets[0]

	reqURL := target.URL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	fp := mimic.New(cfg.Mimic, keyID(target.Credential))
	fp.ApplyRequest(httpReq.Header, target.Credential, mimic.UUID4())
	applyTargetAuth(httpReq.Header, target)
	return httpReq, nil
}

// executeOnce 向上游发起一次非流式推理请求并读取完整响应体。
// [参数] payload: 已改写的请求体；sa: 目标账号凭据；cfg: 当前插件配置。
// [返回] 响应体字节、HTTP 状态码与错误；状态码非 2xx 时错误携带上游响应体。
// 最近修改时间：2026-10-14 02:30:00 上游请求改用无整体超时的推理客户端，避免长思考输出被掐断
func executeOnce(payload []byte, sa *StoredAuth, cfg Config) ([]byte, int, error) {
	body, status, _, err := executeOnceTimed(payload, sa, cfg)
	return body, status, err
}

// executeOnceTimed 与 executeOnce 相同，额外返回本次尝试的首字节时延（纳秒）。
//
// 非流式路径原先恒把 TTFT 记成 0，慢速冷却因此完全没有样本可判。这里用
// firstByteReader 在第一次 Read 有空字节返回时打点——它等价于「上游开始回
// 数据」的时刻，而不是读完全部正文的时刻。
// [参数] payload：上游请求体；sa：账号凭据；cfg：插件配置。
// [返回] 响应正文、HTTP 状态码、首字节时延纳秒（未观测到时为 0）、错误。
// 最近修改时间：2026-10-25；改动原因：新增慢速冷却所需的非流式 TTFT 采集。
func executeOnceTimed(payload []byte, sa *StoredAuth, cfg Config) ([]byte, int, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	httpReq, err := prepareUpstreamRequest(ctx, payload, sa, cfg)
	if err != nil {
		return nil, 0, 0, err
	}

	attemptStarted := time.Now()
	resp, err := inferenceHTTPClient.Do(httpReq)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	tracker := &firstByteReader{inner: io.LimitReader(resp.Body, 64<<20), started: attemptStarted}
	body, errRead := io.ReadAll(tracker)
	if errRead != nil {
		return nil, resp.StatusCode, tracker.ttftNS(), errRead
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, tracker.ttftNS(), fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	return body, resp.StatusCode, tracker.ttftNS(), nil
}

// firstByteReader 包装上游响应体，在第一次读到非空字节时记录首字节时延。
type firstByteReader struct {
	inner   io.Reader
	started time.Time
	at      time.Time
}

func (r *firstByteReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	// 只有真正拿到字节才算首字节：空读（n==0）可能只是缓冲区未就绪。
	if n > 0 && r.at.IsZero() {
		r.at = time.Now()
	}
	return n, err
}

// ttftNS 返回首字节时延纳秒值；未观测到时返回 0。
func (r *firstByteReader) ttftNS() uint64 {
	if r.at.IsZero() || r.started.IsZero() {
		return 0
	}
	d := r.at.Sub(r.started)
	if d <= 0 {
		return 0
	}
	return uint64(d.Nanoseconds())
}

func mutateModel(raw []byte, model string) []byte {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return append([]byte(nil), raw...)
	}
	if model != "" {
		body["model"] = model
	}
	relaxMaxTokensInPlace(body)
	out, err := json.Marshal(body)
	if err != nil {
		return append([]byte(nil), raw...)
	}
	return out
}
