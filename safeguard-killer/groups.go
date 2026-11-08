package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	methodHostModelExecute     = "host.model.execute"
	methodHostModelStream      = "host.model.execute_stream"
	methodHostModelStreamRead  = "host.model.stream_read"
	methodHostModelStreamClose = "host.model.stream_close"
	methodHostStreamEmit       = "host.stream.emit"
	methodHostStreamClose      = "host.stream.close"
)

type modelInfo struct {
	ID          string `json:"ID"`
	Object      string `json:"Object"`
	OwnedBy     string `json:"OwnedBy"`
	DisplayName string `json:"DisplayName"`
}

type modelRegistrationResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
}

type modelRouteRequest struct {
	RequestedModel string `json:"RequestedModel"`
}

type modelRouteResponse struct {
	Handled    bool   `json:"Handled"`
	TargetKind string `json:"TargetKind,omitempty"`
	Reason     string `json:"Reason,omitempty"`
}

type executorRequest struct {
	Model          string         `json:"Model"`
	Format         string         `json:"Format"`
	Stream         bool           `json:"Stream"`
	Alt            string         `json:"Alt"`
	Headers        http.Header    `json:"Headers"`
	Query          url.Values     `json:"Query"`
	SourceFormat   string         `json:"SourceFormat"`
	Payload        []byte         `json:"Payload"`
	Metadata       map[string]any `json:"Metadata"`
	StreamID       string         `json:"stream_id"`
	HostCallbackID string         `json:"host_callback_id"`
}

type executorResponse struct {
	Payload []byte      `json:"Payload"`
	Headers http.Header `json:"Headers,omitempty"`
}

type executorStreamOpenResponse struct {
	Headers http.Header `json:"headers"`
}

type hostModelRequest struct {
	EntryProtocol  string      `json:"entry_protocol"`
	ExitProtocol   string      `json:"exit_protocol"`
	Model          string      `json:"model"`
	Stream         bool        `json:"stream"`
	Body           []byte      `json:"body"`
	Headers        http.Header `json:"headers"`
	Query          url.Values  `json:"query"`
	Alt            string      `json:"alt"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type hostModelResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	Body       []byte      `json:"body"`
}

type hostModelStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}

type hostStreamRef struct {
	StreamID       string `json:"stream_id"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostStreamReadResponse struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

type hostStreamEmit struct {
	StreamID       string `json:"stream_id"`
	Payload        []byte `json:"payload,omitempty"`
	Error          string `json:"error,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// groupAttempt is one target model tried (or skipped) for a group request.
type groupAttempt struct {
	Model  string `json:"model"`
	Result string `json:"result"`
}

func groupModelRegistration(cfg pluginConfig) modelRegistrationResponse {
	models := make([]modelInfo, 0, len(cfg.Groups))
	for _, group := range cfg.Groups {
		models = append(models, modelInfo{ID: group.ID, Object: "model", OwnedBy: groupProvider, DisplayName: group.Name})
	}
	return modelRegistrationResponse{Provider: groupProvider, Models: models}
}

// routeGroup sends requests for group codenames (and Claude Code's hardcoded refusal-fallback or
// subagent fallback model IDs when fallback_on_refusal is active) to this plugin's executor.
func routeGroup(raw []byte, cfg pluginConfig) ([]byte, error) {
	var req modelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if _, ok := resolveTargetGroup(cfg, req.RequestedModel, ""); !ok {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{Handled: true, TargetKind: "self", Reason: "model group"})
}

// resolveTargetGroup finds the configured group for a requested model ID, or maps Claude Code's
// hardcoded refusal-fallback model ("claude-opus-4-8") to the session's active group (or the
// first configured group, excluding claude-* models since Claude just refused).
func resolveTargetGroup(cfg pluginConfig, requestedModel, session string) (modelGroup, bool) {
	if group, ok := cfg.group(requestedModel); ok {
		return group, true
	}
	if !cfg.Features.FallbackOnRefusal || len(cfg.Groups) == 0 {
		return modelGroup{}, false
	}
	lower := strings.ToLower(strings.TrimSpace(requestedModel))
	if lower != "claude-opus-4-8" && lower != "claude-opus-4.8" {
		return modelGroup{}, false
	}
	picked := cfg.Groups[0]
	if session != "" {
		stateMu.Lock()
		for _, g := range cfg.Groups {
			if _, ok := groupSessions[session+"\x00"+g.ID]; ok {
				picked = g
				break
			}
		}
		stateMu.Unlock()
	}
	nonClaude := make([]string, 0, len(picked.Models))
	for _, m := range picked.Models {
		if !strings.HasPrefix(strings.ToLower(m), "claude-") {
			nonClaude = append(nonClaude, m)
		}
	}
	if len(nonClaude) > 0 {
		picked.Models = nonClaude
	}
	return picked, true
}

// executeGroup tries the group's models in order and returns the first successful response.
func executeGroup(raw []byte, cfg pluginConfig) ([]byte, error) {
	var req executorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	session := groupSessionKey(req)
	group, ok := resolveTargetGroup(cfg, req.Model, session)
	if !ok {
		return nil, fmt.Errorf("model group %s is not configured", req.Model)
	}
	now := time.Now()
	aux := isAuxiliaryRequest(req.Payload)
	sanitizedPayload, sanitizedHeaders, _, _ := sanitizeRequest(req.Payload, req.Headers, cfg)
	order, attempts, prevLookupModel := groupCandidateOrder(cfg, session, group, now)
	var lastErr error
	for _, model := range order {
		reason, errSkip := skipReason(cfg, model, now)
		if errSkip != nil {
			return nil, errSkip
		}
		if reason != "" {
			attempts = append(attempts, groupAttempt{Model: model, Result: "skipped: " + reason})
			continue
		}
		body, errBody := withModel(sanitizedPayload, model)
		if errBody != nil {
			return nil, errBody
		}
		rawResp, errCall := hostCall(methodHostModelExecute, hostModelRequest{
			EntryProtocol: req.SourceFormat, ExitProtocol: req.Format, Model: model, Body: body,
			Headers: sanitizedHeaders, Query: req.Query, Alt: req.Alt, HostCallbackID: req.HostCallbackID,
		})
		if errCall != nil {
			attempts = append(attempts, groupAttempt{Model: model, Result: "failed: " + errCall.Error()})
			lastErr = errCall
			continue
		}
		var resp hostModelResponse
		if errDecode := json.Unmarshal(rawResp, &resp); errDecode != nil {
			return nil, fmt.Errorf("decode host.model.execute response: %w", errDecode)
		}
		if cfg.Features.FallbackOnRefusal {
			if errRefusal := checkNonStreamResponse(resp.Body); errRefusal != nil {
				attempts = append(attempts, groupAttempt{Model: model, Result: "failed: " + errRefusal.Error()})
				lastErr = errRefusal
				continue
			}
		}
		attempts = append(attempts, groupAttempt{Model: model, Result: "served"})
		recordGroupDecision(group, session, attempts, model)
		bodyOut := resp.Body
		if !aux {
			prevModel := bindGroupSession(cfg, session, group.ID, model, prevLookupModel, now)
			notice := buildGroupNotice(cfg, group, attempts, model, prevModel)
			bodyOut = injectNonStreamNotice(resp.Body, effectiveFormat(req.Format, req.SourceFormat), notice)
		}
		return okEnvelope(executorResponse{Payload: bodyOut, Headers: cleanResponseHeaders(resp.Headers)})
	}
	recordGroupDecision(group, session, attempts, "")
	if !aux && cfg.Features.ClientFallbackNotices && hasRefusalAttempt(attempts) {
		notice := buildGroupExhaustedNotice(cfg, group, attempts)
		bodyOut := buildExhaustedNonStreamBody(effectiveFormat(req.Format, req.SourceFormat), group.ID, notice)
		return okEnvelope(executorResponse{Payload: bodyOut, Headers: http.Header{"Content-Type": []string{"application/json"}}})
	}
	return nil, groupFailure(group, attempts, lastErr)
}

// executeGroupStream opens the first group model that produces visible output without refusing
// and forwards its chunks.
func executeGroupStream(raw []byte, cfg pluginConfig) ([]byte, error) {
	var req executorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if req.StreamID == "" {
		return nil, fmt.Errorf("executor.execute_stream request has no stream_id")
	}
	session := groupSessionKey(req)
	group, ok := resolveTargetGroup(cfg, req.Model, session)
	if !ok {
		return nil, fmt.Errorf("model group %s is not configured", req.Model)
	}
	now := time.Now()
	aux := isAuxiliaryRequest(req.Payload)
	sanitizedPayload, sanitizedHeaders, _, hadSafeguards := sanitizeRequest(req.Payload, req.Headers, cfg)
	stampSafeguards := cfg.Features.BypassAutoModeClassifier && hadSafeguards
	order, attempts, prevLookupModel := groupCandidateOrder(cfg, session, group, now)
	var lastErr error
	for _, model := range order {
		reason, errSkip := skipReason(cfg, model, now)
		if errSkip != nil {
			return nil, errSkip
		}
		if reason != "" {
			attempts = append(attempts, groupAttempt{Model: model, Result: "skipped: " + reason})
			continue
		}
		body, errBody := withModel(sanitizedPayload, model)
		if errBody != nil {
			return nil, errBody
		}
		rawResp, errCall := hostCall(methodHostModelStream, hostModelRequest{
			EntryProtocol: req.SourceFormat, ExitProtocol: req.Format, Model: model, Stream: true, Body: body,
			Headers: sanitizedHeaders, Query: req.Query, Alt: req.Alt, HostCallbackID: req.HostCallbackID,
		})
		if errCall != nil {
			attempts = append(attempts, groupAttempt{Model: model, Result: "failed: " + errCall.Error()})
			lastErr = errCall
			continue
		}
		var resp hostModelStreamResponse
		if errDecode := json.Unmarshal(rawResp, &resp); errDecode != nil {
			return nil, fmt.Errorf("decode host.model.execute_stream response: %w", errDecode)
		}
		prefetched, done, errPrefetch := prefetchStream(resp.StreamID, req.HostCallbackID, cfg.Features.FallbackOnRefusal, cfg.StreamPrefetchBytes)
		if errPrefetch != nil {
			attempts = append(attempts, groupAttempt{Model: model, Result: "failed: " + errPrefetch.Error()})
			lastErr = errPrefetch
			continue
		}
		attempts = append(attempts, groupAttempt{Model: model, Result: "served"})
		recordGroupDecision(group, session, attempts, model)
		shiftIndices := false
		if !aux {
			prevModel := bindGroupSession(cfg, session, group.ID, model, prevLookupModel, now)
			notice := buildGroupNotice(cfg, group, attempts, model, prevModel)
			prefetched, shiftIndices = injectStreamNotice(prefetched, effectiveFormat(req.Format, req.SourceFormat), notice)
		}
		go forwardStream(resp.StreamID, req.StreamID, req.HostCallbackID, prefetched, done, shiftIndices, stampSafeguards, cfg.Features.FallbackOnRefusal, session, group, model)
		return okEnvelope(executorStreamOpenResponse{Headers: cleanResponseHeaders(resp.Headers)})
	}
	recordGroupDecision(group, session, attempts, "")
	if !aux && cfg.Features.ClientFallbackNotices && len(attempts) > 0 {
		notice := buildGroupExhaustedNotice(cfg, group, attempts)
		go emitExhaustedNoticeStream(req.StreamID, req.HostCallbackID, effectiveFormat(req.Format, req.SourceFormat), group.ID, notice)
		return okEnvelope(executorStreamOpenResponse{Headers: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}})
	}
	return nil, groupFailure(group, attempts, lastErr)
}

// forwardStream flushes prefetched chunks and copies the remainder of the target model's stream.
func forwardStream(source, target, callbackID string, prefetched [][]byte, sourceDone, shiftIndices, stampSafeguards, checkRefusal bool, session string, group modelGroup, model string) {
	seenToolIDs := make(map[string]bool)
	for _, payload := range prefetched {
		payload = processStreamSafeguardChunk(payload, seenToolIDs, stampSafeguards)
		if _, errEmit := hostCall(methodHostStreamEmit, hostStreamEmit{StreamID: target, Payload: payload, HostCallbackID: callbackID}); errEmit != nil {
			if !sourceDone {
				_, _ = hostCall(methodHostModelStreamClose, hostStreamRef{StreamID: source, HostCallbackID: callbackID})
			}
			return
		}
	}
	if sourceDone {
		closeStream(target, callbackID, "")
		return
	}
	for {
		rawRead, errRead := hostCall(methodHostModelStreamRead, hostStreamRef{StreamID: source, HostCallbackID: callbackID})
		if errRead != nil {
			closeStream(target, callbackID, errRead.Error())
			return
		}
		var chunk hostStreamReadResponse
		if errDecode := json.Unmarshal(rawRead, &chunk); errDecode != nil {
			closeStream(target, callbackID, "decode host.model.stream_read response: "+errDecode.Error())
			return
		}
		if len(chunk.Payload) > 0 {
			if checkRefusal {
				if refusal, _, _, _ := inspectStreamChunk(chunk.Payload); refusal != "" {
					advanceGroupSessionOnRefusal(session, group, model, time.Now())
				}
			}
			payload := chunk.Payload
			if shiftIndices {
				payload, _ = shiftClaudeStreamChunk(payload, false, "")
			}
			payload = processStreamSafeguardChunk(payload, seenToolIDs, stampSafeguards)
			if _, errEmit := hostCall(methodHostStreamEmit, hostStreamEmit{StreamID: target, Payload: payload, HostCallbackID: callbackID}); errEmit != nil {
				// The client stream is gone; release the target stream. Its close result has no reader.
				_, _ = hostCall(methodHostModelStreamClose, hostStreamRef{StreamID: source, HostCallbackID: callbackID})
				return
			}
		}
		if chunk.Error != "" || chunk.Done {
			closeStream(target, callbackID, chunk.Error)
			return
		}
	}
}

func closeStream(target, callbackID, errText string) {
	// The host owns the client connection; a failed close has no other channel to report through.
	_, _ = hostCall(methodHostStreamClose, hostStreamEmit{StreamID: target, Error: errText, HostCallbackID: callbackID})
}

// skipReason reports why a target model should not be tried: every credential serving it is
// below min_remaining. Credentials without quota data (API keys, unsupported providers) are
// assumed usable, so a model is only skipped on positive evidence.
func skipReason(cfg pluginConfig, model string, now time.Time) (string, error) {
	if cfg.ManagementKey == "" {
		return "", nil
	}
	snapshot, errHost := hostState(now)
	if errHost != nil {
		return "", errHost
	}
	modelMap := mergedModels(snapshot)
	if snapshot.modelsErr != nil {
		ensureFallbackModels(cfg.ManagementKey, snapshot.auths)
	}
	var exhausted []string
	for _, auth := range snapshot.auths {
		if auth.Disabled || !servesKnownModel(modelMap, auth.ID, model) {
			continue
		}
		entry := rankEntry{AuthID: auth.ID}
		rateCredential(&entry, model, cfg.MinRemaining, now)
		if entry.Excluded == "" || !strings.Contains(entry.Excluded, "% left") {
			return "", nil
		}
		exhausted = append(exhausted, auth.ID+" ("+entry.Excluded+")")
	}
	if len(exhausted) == 0 {
		return "", nil
	}
	return "all credentials low on quota: " + strings.Join(exhausted, ", "), nil
}

// withModel sets the top-level "model" field the client sent to the target model.
// Protocols that carry the model only in the path (Gemini) have no "model" field and are passed
// through unchanged.
func withModel(payload []byte, model string) ([]byte, error) {
	bodyOut, _, _, _ := sanitizeRequest(payload, nil, currentConfig())
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(bodyOut, &body); errUnmarshal != nil {
		return nil, fmt.Errorf("decode group request body: %w", errUnmarshal)
	}
	if _, ok := body["model"]; !ok {
		return bodyOut, nil
	}
	encoded, _ := json.Marshal(model)
	body["model"] = encoded
	return json.Marshal(body)
}

func groupFailure(group modelGroup, attempts []groupAttempt, lastErr error) error {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		parts = append(parts, attempt.Model+": "+attempt.Result)
	}
	status := http.StatusServiceUnavailable
	var errStatus *statusError
	if errors.As(lastErr, &errStatus) && errStatus.HTTPStatus != 0 {
		status = errStatus.HTTPStatus
	}
	return &statusError{Code: "model_group_exhausted", Message: fmt.Sprintf("model group %s: every model failed (%s)", group.ID, strings.Join(parts, "; ")), HTTPStatus: status}
}

func recordGroupDecision(group modelGroup, session string, attempts []groupAttempt, served string) {
	reason := "every model failed"
	if served != "" {
		reason = "served by " + served
	}
	stateMu.Lock()
	recordDecision(decision{Time: time.Now(), Kind: "group", Model: group.ID, Session: session, AuthID: served, Reason: reason, Attempts: attempts})
	stateMu.Unlock()
}
