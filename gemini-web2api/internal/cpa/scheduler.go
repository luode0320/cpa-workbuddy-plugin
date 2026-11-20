package cpa

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/config"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginabi"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/pluginapi"
)

type rpcRequestCompletion struct {
	pluginapi.RequestCompletion
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcQuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcQuotaResetRequest struct {
	pluginapi.QuotaResetRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (p *Plugin) handleSchedulerPick(raw []byte) (any, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode scheduler.pick request: %w", err)
	}
	if len(req.Candidates) == 0 {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}

	// Only handle scheduling when candidates belong to gemini-web2api.
	geminiWebCandidates := make([]pluginapi.SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, cand := range req.Candidates {
		if isGeminiWebProvider(cand.Provider) || isGeminiWebCandidate(cand) {
			geminiWebCandidates = append(geminiWebCandidates, cand)
		}
	}
	if len(geminiWebCandidates) == 0 {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	if p.serviceStopped.Load() {
		if len(geminiWebCandidates) < len(req.Candidates) {
			return pluginapi.SchedulerPickResponse{Handled: false}, nil
		}
		return pluginapi.SchedulerPickResponse{
			Handled:      true,
			Reject:       true,
			RejectCode:   "service_stopped",
			RejectReason: "gemini-web2api service is stopped",
		}, nil
	}

	rt, err := p.Runtime()
	if err != nil {
		return pluginapi.SchedulerPickResponse{
			Handled:         true,
			DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin,
		}, nil
	}

	cleanModel := StripModelPrefix(req.Model, p.Config().ModelPrefix)
	cleanModel = gemini.CanonicalModelID(config.MapModel(cleanModel))

	candidateByAccount := make(map[string]string, len(geminiWebCandidates))
	var poolFallbackCandidateID string
	for _, cand := range geminiWebCandidates {
		if strings.EqualFold(cand.Status, "disabled") || strings.EqualFold(cand.Status, "unavailable") {
			continue
		}
		accID := extractAccountIDFromAuth(nil, cand.Metadata, cand.Attributes)
		if accID == "" && strings.Contains(cand.ID, "@") {
			accID = strings.ToLower(strings.TrimSpace(cand.ID))
		}
		if accID != "" {
			candidateByAccount[accID] = cand.ID
		} else if poolFallbackCandidateID == "" {
			poolFallbackCandidateID = cand.ID
		}
	}

	poolStatus := rt.Pool.Status()
	now := time.Now()

	p.mu.RLock()
	routingStrategy := strings.ToLower(strings.TrimSpace(p.appCfg.RoutingStrategy))
	p.mu.RUnlock()

	// Prefer ready, allowed, non-cooling accounts supporting cleanModel with highest context window and idle slots.
	var bestAuthID string
	bestContextWindow := -1
	bestIdle := false

	accounts := poolStatus.Accounts
	start := 0
	if len(accounts) > 1 && routingStrategy != "fill-first" {
		start = int((p.rrCursor.Add(1) - 1) % uint64(len(accounts)))
	}

	for offset := 0; offset < len(accounts); offset++ {
		acc := accounts[(start+offset)%len(accounts)]
		if !acc.Enabled || acc.State != balancer.StateReady {
			continue
		}
		if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
			continue
		}
		modelCooling := false
		for _, mc := range acc.ModelCooldowns {
			if (strings.EqualFold(mc.Model, cleanModel) || gemini.CanonicalModelID(mc.Model) == cleanModel) && now.Before(mc.Until) {
				modelCooling = true
				break
			}
		}
		if modelCooling {
			continue
		}
		if !rt.Pool.AllowsAccountForModel(p.ctx, acc.AccountID, cleanModel) {
			continue
		}
		if cleanModel != "" {
			if client := rt.Pool.Client(acc.AccountID); client != nil && len(client.Models()) > 0 {
				if _, resolveErr := client.ResolveModel(cleanModel); resolveErr != nil {
					continue
				}
			}
		}
		authID, ok := candidateByAccount[strings.ToLower(strings.TrimSpace(acc.AccountID))]
		if !ok || authID == "" {
			continue
		}
		idle := acc.Pending == 0
		if bestAuthID == "" ||
			acc.ContextWindow > bestContextWindow ||
			(acc.ContextWindow == bestContextWindow && idle && !bestIdle) {
			bestAuthID = authID
			bestContextWindow = acc.ContextWindow
			bestIdle = idle
		}
	}

	if bestAuthID != "" {
		return pluginapi.SchedulerPickResponse{
			Handled: true,
			AuthID:  bestAuthID,
		}, nil
	}

	if poolFallbackCandidateID != "" {
		return pluginapi.SchedulerPickResponse{
			Handled: true,
			AuthID:  poolFallbackCandidateID,
		}, nil
	}

	// Check if all accounts for this model are cooling down
	if retryAt := rt.Pool.RetryAfter(p.ctx, cleanModel); !retryAt.IsZero() && time.Until(retryAt) > time.Minute {
		return pluginapi.SchedulerPickResponse{
			Handled:      true,
			Reject:       true,
			RejectCode:   "rate_limit_exceeded",
			RejectReason: fmt.Sprintf("all Gemini Web accounts for model %s are cooling down until %s", cleanModel, retryAt.Format(time.RFC3339)),
		}, nil
	}

	return pluginapi.SchedulerPickResponse{
		Handled:         true,
		DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin,
	}, nil
}

func isGeminiWebCandidate(cand pluginapi.SchedulerAuthCandidate) bool {
	if cand.Attributes != nil && isGeminiWebProvider(cand.Attributes["provider"]) {
		return true
	}
	if cand.Metadata != nil {
		if p, ok := cand.Metadata["provider"].(string); ok && isGeminiWebProvider(p) {
			return true
		}
		if t, ok := cand.Metadata["type"].(string); ok && isGeminiWebProvider(t) {
			return true
		}
	}
	idLower := strings.ToLower(strings.TrimSpace(cand.ID))
	return strings.HasPrefix(idLower, "gemini-web2api") || strings.HasPrefix(idLower, "gemini-web-")
}

func (p *Plugin) handleRequestComplete(raw []byte) (any, error) {
	var req rpcRequestCompletion
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

func (p *Plugin) handleQuotaIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
		CredentialProviders: []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
	}, nil
}

func (p *Plugin) handleQuotaDescribe() (any, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{"gemini-web2api", "gemini-web", "geminiweb2api"},
		DisplayName:        "Google Gemini Web",
		SupportsReset:      true,
	}, nil
}

func (p *Plugin) handleQuotaFetch(raw []byte) (any, error) {
	var req rpcQuotaFetchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rt, err := p.Runtime()
	if err != nil {
		return nil, err
	}

	accID := extractAccountIDFromAuth(req.StorageJSON, req.Metadata, req.Attributes)
	poolStatus := rt.Pool.Status()
	var matched *balancer.AccountStatus
	for i := range poolStatus.Accounts {
		if accID != "" && strings.EqualFold(poolStatus.Accounts[i].AccountID, accID) {
			matched = &poolStatus.Accounts[i]
			break
		}
	}
	if matched != nil && matched.State == balancer.StateInitializing {
		rt.Accounts.WaitFirstInit(p.ctx, matched.AccountID)
		poolStatus = rt.Pool.Status()
		for i := range poolStatus.Accounts {
			if strings.EqualFold(poolStatus.Accounts[i].AccountID, matched.AccountID) {
				matched = &poolStatus.Accounts[i]
				break
			}
		}
	}
	if matched == nil && len(poolStatus.Accounts) > 0 {
		matched = &poolStatus.Accounts[0]
	}

	tierName := "Free"
	if req.Attributes != nil && strings.TrimSpace(req.Attributes["tier"]) != "" {
		tierName = formatTierDisplay(req.Attributes["tier"])
	} else if req.Metadata != nil {
		if t, ok := req.Metadata["tier"].(string); ok && strings.TrimSpace(t) != "" {
			tierName = formatTierDisplay(t)
		}
	}
	remaining := 1.0
	resetTime := ""
	desc := "Ready"
	modelsCount := 0.0
	buckets := make([]pluginapi.QuotaBucket, 0, 2)

	if matched != nil {
		if matched.Tier != "" {
			tierName = formatTierDisplay(matched.Tier)
		}
		modelsCount = float64(len(matched.Models))
		desc = fmt.Sprintf("State: %s", matched.State)
		if matched.Usage != nil {
			if matched.Usage.Current != nil {
				rem := 1.0 - matched.Usage.Current.UsedRatio
				if rem < 0 {
					rem = 0
				}
				var rTime string
				if !matched.Usage.Current.ResetAt.IsZero() {
					rTime = matched.Usage.Current.ResetAt.Format(time.RFC3339)
				}
				buckets = append(buckets, pluginapi.QuotaBucket{
					Window:            "current",
					RemainingFraction: rem,
					ResetTime:         rTime,
					Description:       fmt.Sprintf("Current window used %.1f%%", matched.Usage.Current.UsedRatio*100),
				})
			}
			if matched.Usage.Weekly != nil {
				rem := 1.0 - matched.Usage.Weekly.UsedRatio
				if rem < 0 {
					rem = 0
				}
				var rTime string
				if !matched.Usage.Weekly.ResetAt.IsZero() {
					rTime = matched.Usage.Weekly.ResetAt.Format(time.RFC3339)
				}
				buckets = append(buckets, pluginapi.QuotaBucket{
					Window:            "weekly",
					RemainingFraction: rem,
					ResetTime:         rTime,
					Description:       fmt.Sprintf("Weekly window used %.1f%%", matched.Usage.Weekly.UsedRatio*100),
				})
			}
		}
		if matched.State == balancer.StateCooldown || len(matched.ModelCooldowns) > 0 {
			remaining = 0.0
			if matched.CooldownUntil != nil && !matched.CooldownUntil.IsZero() {
				resetTime = matched.CooldownUntil.Format(time.RFC3339)
			}
			if matched.CooldownReason != "" {
				desc = matched.CooldownReason
			}
		} else if matched.State == balancer.StateAuthRequired || !matched.Enabled {
			remaining = 0.0
			if matched.LastError != "" {
				desc = matched.LastError
			}
		}
	}

	if len(buckets) == 0 {
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            "current",
			RemainingFraction: remaining,
			ResetTime:         resetTime,
			Description:       desc,
		})
	}

	return pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "Google Gemini Web",
			TierName: tierName,
			TierID:   strings.ToLower(tierName),
		},
		Summary: []pluginapi.QuotaMetric{
			{Key: "accounts", Label: "Total Accounts", Value: float64(poolStatus.Total), Format: "number"},
			{Key: "models", Label: "Available Models", Value: modelsCount, Format: "number"},
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Gemini Web Quota",
				Buckets:     buckets,
			},
		},
	}, nil
}

func (p *Plugin) handleQuotaReset(raw []byte) (any, error) {
	var req rpcQuotaResetRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rt, err := p.Runtime()
	if err != nil {
		return nil, err
	}

	accID := extractAccountIDFromAuth(req.StorageJSON, req.Metadata, req.Attributes)
	if accID != "" {
		if err := rt.Pool.ClearCooldown(accID, ""); err != nil {
			return pluginapi.QuotaResetResponse{
				Success: false,
				Message: err.Error(),
			}, nil
		}
		if st := findPoolAccountStatus(rt, accID); st != nil {
			for _, mc := range st.ModelCooldowns {
				_ = rt.Pool.ClearCooldown(accID, mc.Model)
			}
		}
		p.notifyHostCooldownReset(req.HostCallbackID, req.AuthID, accID, "")
		return pluginapi.QuotaResetResponse{
			Success: true,
			Message: "Cleared Gemini Web cooldowns for account " + accID,
		}, nil
	}

	for _, st := range rt.Pool.Status().Accounts {
		_ = rt.Pool.ClearCooldown(st.AccountID, "")
		for _, mc := range st.ModelCooldowns {
			_ = rt.Pool.ClearCooldown(st.AccountID, mc.Model)
		}
		p.notifyHostCooldownReset(req.HostCallbackID, "", st.AccountID, "")
	}
	return pluginapi.QuotaResetResponse{
		Success: true,
		Message: "Cleared Gemini Web cooldowns for all accounts",
	}, nil
}

func (p *Plugin) notifyHostCooldownReset(callbackID, authID, accountID, model string) {
	cleanAcc := strings.ToLower(strings.TrimSpace(accountID))
	if authID == "" && cleanAcc != "" {
		authID = fmt.Sprintf("gemini-web2api-%s.json", sanitizeFileComponent(cleanAcc))
	}
	req := map[string]any{
		"host_callback_id": strings.TrimSpace(callbackID),
		"provider":         ProviderID,
		"auth_id":          authID,
		"account_id":       cleanAcc,
	}
	if m := strings.TrimSpace(model); m != "" && m != "*" {
		req["model"] = m
	}
	_, _ = p.callHost(pluginabi.MethodHostRoutingResetCooldown, req)
}
