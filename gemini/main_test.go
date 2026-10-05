package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestRegistrationDeclaresCapabilities(t *testing.T) {
	raw, errRegister := handleRegister([]byte(`{}`))
	if errRegister != nil {
		t.Fatalf("handleRegister returned error: %v", errRegister)
	}
	defer cliproxyPluginShutdown()

	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		t.Fatalf("decode envelope: %v", errDecode)
	}
	if !envelope.OK {
		t.Fatalf("registration envelope is not OK: %#v", envelope.Error)
	}
	var reg abiRegistration
	if errDecode := json.Unmarshal(envelope.Result, &reg); errDecode != nil {
		t.Fatalf("decode registration: %v", errDecode)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", reg.SchemaVersion, pluginabi.SchemaVersion)
	}
	if reg.Metadata.Name != "Gemini Provider" {
		t.Fatalf("metadata name = %q, want %q", reg.Metadata.Name, "Gemini Provider")
	}
	if reg.Metadata.Version != version {
		t.Fatalf("metadata version = %q, want %q", reg.Metadata.Version, version)
	}
	if !reg.Capabilities.AuthProvider || !reg.Capabilities.Executor || !reg.Capabilities.UsagePlugin {
		t.Fatalf("missing required capabilities: %#v", reg.Capabilities)
	}
	if !reg.Capabilities.RequestTranslator || !reg.Capabilities.ResponseTranslator {
		t.Fatalf("missing translator capability: %#v", reg.Capabilities)
	}
}

func TestExtractGeminiUsage(t *testing.T) {
	payload := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "hello"}]}}],
		"usageMetadata": {
			"promptTokenCount": 15,
			"candidatesTokenCount": 25,
			"totalTokenCount": 40,
			"cachedContentTokenCount": 5
		}
	}`)
	detail := extractGeminiUsage(payload)
	if detail.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", detail.InputTokens)
	}
	if detail.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25", detail.OutputTokens)
	}
	if detail.TotalTokens != 40 {
		t.Errorf("TotalTokens = %d, want 40", detail.TotalTokens)
	}
	if detail.CachedTokens != 5 {
		t.Errorf("CachedTokens = %d, want 5", detail.CachedTokens)
	}
}

func TestGeminiStreamUsageCollector(t *testing.T) {
	started := time.Now().Add(-50 * time.Millisecond)
	collector := &geminiStreamUsageCollector{}

	chunk1 := []byte(`{"candidates": [{"content": {"parts": [{"text": "hi"}]}}]}`)
	collector.feed(chunk1)
	if collector.ttftNS(started) == 0 {
		t.Errorf("expected non-zero ttftNS")
	}

	chunk2 := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "!"}]}}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 2,
			"totalTokenCount": 12
		}
	}`)
	collector.feed(chunk2)
	if collector.detail.TotalTokens != 12 {
		t.Errorf("TotalTokens = %d, want 12", collector.detail.TotalTokens)
	}
}
