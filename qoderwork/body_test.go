package main

import (
	"encoding/json"
	"testing"
)

// TestOpenAIRequestAcceptsContentPartsArray 验证多模态 content 部件数组
// （[{"type":"text","text":"..."}]）能被解析并提取出文本，而非直接报
// "cannot unmarshal array into ... content of type string"。
func TestOpenAIRequestAcceptsContentPartsArray(t *testing.T) {
	raw := []byte(`{"model":"Qwen3.8-Flash","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"你好"}]}]}`)
	req := &openAIRequest{}
	if err := json.Unmarshal(raw, req); err != nil {
		t.Fatalf("content 部件数组应能解析, got error: %v", err)
	}
	if got := extractLatestUserPrompt(req.Messages); got != "你好" {
		t.Fatalf("expected 你好, got %q", got)
	}
}

// TestOpenAIRequestAcceptsPlainStringContent 验证纯字符串 content 形态仍兼容。
func TestOpenAIRequestAcceptsPlainStringContent(t *testing.T) {
	raw := []byte(`{"model":"Qwen3.8-Flash","messages":[{"role":"user","content":"hello"}]}`)
	req := &openAIRequest{}
	if err := json.Unmarshal(raw, req); err != nil {
		t.Fatalf("字符串 content 应能解析, got error: %v", err)
	}
	if got := extractLatestUserPrompt(req.Messages); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

// TestOpenAIRequestJoinsMultipleTextParts 验证多个文本部件按换行拼接。
func TestOpenAIRequestJoinsMultipleTextParts(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`)
	req := &openAIRequest{}
	if err := json.Unmarshal(raw, req); err != nil {
		t.Fatalf("多文本部件应能解析, got error: %v", err)
	}
	if got := extractLatestUserPrompt(req.Messages); got != "a\nb" {
		t.Fatalf("expected a\\nb, got %q", got)
	}
}

// TestBuildQoderBodyFromContentPartsPayload 验证多模态 payload 能走通
// handleExecExecute 的完整构建链路：解析 → 提取文本 → 渲染上游 body。
func TestBuildQoderBodyFromContentPartsPayload(t *testing.T) {
	payload := []byte(`{"model":"Qwen3.8-Flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":"写一首诗"}]}]}`)
	req := &openAIRequest{}
	if err := json.Unmarshal(payload, req); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	body, err := buildQoderBody(req, cpaToUpstreamKey(stripProviderPrefix("Qwen3.8-Flash")), "")
	if err != nil {
		t.Fatalf("buildQoderBody 失败: %v", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(body, &rendered); err != nil {
		t.Fatalf("上游 body 不是合法 JSON: %v", err)
	}
	cc, _ := rendered["chat_context"].(map[string]any)
	txt, _ := cc["text"].(map[string]any)
	if got, _ := txt["text"].(string); got != "写一首诗" {
		t.Fatalf("expected 写一首诗 in chat_context.text.text, got %q", got)
	}
	mc, _ := rendered["model_config"].(map[string]any)
	if got, _ := mc["key"].(string); got != "qfmodel" {
		t.Fatalf("expected upstream key qfmodel, got %q", got)
	}
}
