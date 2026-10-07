package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWorkBuddyAI_Constants asserts the isolated WorkBuddy AI domain, prefix, and identity.
func TestWorkBuddyAI_Constants(t *testing.T) {
	if authFilePrefix != "workbuddyai-" {
		t.Fatalf("authFilePrefix = %q; want workbuddyai- to prevent collision with domestic workbuddy-", authFilePrefix)
	}
	if providerName != "workbuddy-ai-provider" {
		t.Fatalf("providerName = %q; want workbuddy-ai-provider", providerName)
	}
	if upstreamBase != "https://www.workbuddy.ai" {
		t.Fatalf("upstreamBase = %q; want https://www.workbuddy.ai", upstreamBase)
	}
	if originReferer != "https://www.workbuddy.ai" {
		t.Fatalf("originReferer = %q; want https://www.workbuddy.ai", originReferer)
	}
}

// TestWorkBuddyAI_AuthFileName asserts auth file name generation and sanitization.
func TestWorkBuddyAI_AuthFileName(t *testing.T) {
	sa := &storedAuth{
		Account: storedAccount{
			UID: "usr_ai_9999",
		},
	}
	want := "workbuddyai-usr_ai_9999.json"
	if got := authFileNameFor(sa); got != want {
		t.Fatalf("authFileNameFor = %q; want %q", got, want)
	}

	// nil UID fallback
	if got := authFileNameFor(nil); got != authFileName {
		t.Fatalf("authFileNameFor(nil) = %q; want default %q", got, authFileName)
	}

	// IsWorkbuddyAuthFileName check
	if !isWorkbuddyAuthFileName("workbuddyai-123.json") {
		t.Fatalf("isWorkbuddyAuthFileName should accept workbuddyai-123.json")
	}
	if isWorkbuddyAuthFileName("workbuddy-123.json") {
		t.Fatalf("isWorkbuddyAuthFileName must reject domestic workbuddy-123.json")
	}
}

// TestWorkBuddyAI_Policy asserts credit exhaustion and error categorization.
func TestWorkBuddyAI_Policy(t *testing.T) {
	sa := &storedAuth{}
	if region := accountRegion(sa); region != "global" {
		t.Fatalf("accountRegion = %q; want global", region)
	}

	if !isHardCreditError(402, "") {
		t.Fatalf("HTTP 402 must be classified as hard credit error")
	}
	if !isHardCreditError(200, "credit exhausted") {
		t.Fatalf("credit exhausted body must be classified as hard credit error")
	}
	if !isHardCreditError(200, "点数耗尽") {
		t.Fatalf("点数耗尽 body must be classified as hard credit error")
	}

	// displayNote formatting
	cr := &creditsSummary{
		TotalRemain: 100,
		TotalUsed:   50,
		TotalSize:   150,
	}
	note := displayNote(sa, cr, false)
	if !strings.Contains(note, "Global") || !strings.Contains(note, "余100 已用50 总150") {
		t.Fatalf("displayNote = %q; unexpected format", note)
	}

	disabledNote := displayNote(sa, cr, true)
	if !strings.Contains(disabledNote, "已禁用") {
		t.Fatalf("displayNote for disabled account = %q; want 已禁用", disabledNote)
	}

	// shouldReenable / shouldReenableCN behavior for recovered credits
	if !shouldReenable(true, cr) {
		t.Fatalf("shouldReenable should be true when remain > 0")
	}
	if !shouldReenableCN(true, cr) {
		t.Fatalf("shouldReenableCN should be true when remain > 0")
	}
	exhaustedCR := &creditsSummary{TotalRemain: 0, TotalUsed: 150, TotalSize: 150}
	if shouldReenable(true, exhaustedCR) {
		t.Fatalf("shouldReenable should be false when exhausted")
	}
	if shouldReenableCN(true, exhaustedCR) {
		t.Fatalf("shouldReenableCN should be false when exhausted")
	}

	// lifecycleActionFor asserts: exhausted accounts must be disabled, NEVER deleted
	if act := lifecycleActionFor("global", exhaustedCR); act != lifecycleDisable {
		t.Fatalf("lifecycleActionFor(global, exhausted) = %v; want lifecycleDisable (never delete)", act)
	}
	if act := lifecycleActionFor("global", cr); act != lifecycleNone {
		t.Fatalf("lifecycleActionFor(global, healthy) = %v; want lifecycleNone", act)
	}
}

// TestWorkBuddyAI_Registration asserts plugin registration capabilities.
func TestWorkBuddyAI_Registration(t *testing.T) {
	reg := wbRegistration()
	if reg.Metadata.Name != "WorkBuddy AI" {
		t.Fatalf("reg.Metadata.Name = %q; want WorkBuddy AI", reg.Metadata.Name)
	}
	if !reg.Capabilities.AuthProvider || !reg.Capabilities.ModelProvider || !reg.Capabilities.Executor {
		t.Fatalf("reg capabilities must support Auth, Model, and Executor: %+v", reg.Capabilities)
	}

	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("json.Marshal(reg) failed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("empty registration json")
	}
}
