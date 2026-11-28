package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// creditSummary 是账号在上游的积分与额度快照。
// 字段全部来自 /chatglm/user-api/user/info 的 result.member_info。
type creditSummary struct {
	// LeftScore 剩余积分，上游以字符串下发（如 "199.01"）。
	LeftScore string `json:"left_score"`
	// TokenLeftPercent 剩余额度百分比（如 "99%"）。
	TokenLeftPercent string `json:"token_left_percent"`
	// TokenUsagePercent 已用额度百分比（如 "1%"）。
	TokenUsagePercent string `json:"token_usage_percent"`
	// MemberStatus 会员状态，1 表示普通用户。
	MemberStatus int `json:"member_status"`
	// ScoreRule 积分规则说明，上游原文。
	ScoreRule string `json:"score_rule"`
	// FetchedAt 本次快照时刻，供面板显示新鲜度。
	FetchedAt string `json:"fetched_at"`
}

// userInfoEnvelope 是用户信息端点的响应信封。
type userInfoEnvelope struct {
	Status int `json:"status"`
	Result struct {
		MemberInfo struct {
			MemberStatus      int    `json:"member_status"`
			TokenUsagePercent string `json:"token_usage_percent"`
			TokenLeftPercent  string `json:"token_left_percent"`
			LeftScore         string `json:"left_score"`
			ScoreRule         string `json:"score_rule"`
		} `json:"member_info"`
	} `json:"result"`
}

// fetchCredits 用 access_token 拉取账号积分与额度快照。
// [参数] accessToken: 调用令牌；cfg: 插件配置。
// [返回] 积分快照、错误。
// 最近修改时间：2026-11-28 新增, 面板展示剩余积分与额度。
func fetchCredits(accessToken string, cfg Config) (*creditSummary, error) {
	deviceID := deviceIDFromToken(accessToken)
	if deviceID == "" {
		return nil, fmt.Errorf("access_token 缺少 device_id，请重新授权")
	}
	sign := generateSign(cfg.SignSecret, time.Now().UnixMilli(), newNonce())
	req, err := http.NewRequest(http.MethodGet, cfg.UserInfoEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build user info request: %w", err)
	}
	h := refreshHeaders("", sign, deviceID)
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Accept", "application/json")
	req.Header = h

	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user info request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read user info response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info status %d: %s", resp.StatusCode, string(raw))
	}

	var envelope userInfoEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode user info response: %w", err)
	}
	mi := envelope.Result.MemberInfo
	return &creditSummary{
		LeftScore:         strings.TrimSpace(mi.LeftScore),
		TokenLeftPercent:  strings.TrimSpace(mi.TokenLeftPercent),
		TokenUsagePercent: strings.TrimSpace(mi.TokenUsagePercent),
		MemberStatus:      mi.MemberStatus,
		ScoreRule:         strings.TrimSpace(mi.ScoreRule),
		FetchedAt:         time.Now().Format(time.RFC3339),
	}, nil
}

// handleMgmtCredits 返回指定账号的积分快照。
// [参数] query: 管理请求查询参数，需带 auth_id。
// [返回] 管理响应信封字节、错误。
// 最近修改时间：2026-11-28 新增, 面板懒加载积分不拖慢账号列表。
func handleMgmtCredits(query url.Values) ([]byte, error) {
	authID := strings.TrimSpace(query.Get("auth_id"))
	if authID == "" {
		return jsonResponse(map[string]any{"ok": false, "error": "auth_id required"})
	}
	sa := findAuthByID(authID)
	if sa == nil {
		return jsonResponse(map[string]any{"ok": false, "error": "account not found"})
	}
	cfg := currentConfig()
	accessToken, err := acquireToken(sa.RefreshToken, cfg)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": fmt.Sprintf("token: %v", err)})
	}
	credits, err := fetchCredits(accessToken, cfg)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(map[string]any{"ok": true, "credits": credits})
}
