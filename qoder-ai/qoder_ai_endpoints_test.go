package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestQoderAI_GlobalEndpointsContract verifies that Qoder AI uses dedicated
// global domain constants and correct endpoints.
func TestQoderAI_GlobalEndpointsContract(t *testing.T) {
	if providerName != "qoder-ai-provider" {
		t.Fatalf("providerName=%q; want 'qoder-ai-provider'", providerName)
	}
	if authFileName != "qoderai.json" {
		t.Fatalf("authFileName=%q; want 'qoderai.json'", authFileName)
	}
	if authFilePrefix != "qoderai-" {
		t.Fatalf("authFilePrefix=%q; want 'qoderai-'", authFilePrefix)
	}
	if upstreamBase != "https://openapi.qoder.sh" {
		t.Fatalf("upstreamBase=%q; want 'https://openapi.qoder.sh'", upstreamBase)
	}
	if gatewayBase != "https://openapi.qoder.sh" {
		t.Fatalf("gatewayBase=%q; want 'https://openapi.qoder.sh'", gatewayBase)
	}
	if !strings.HasPrefix(endpointCheckinStatus, "https://openapi.qoder.sh/sash/api/v1/me/daily-check-in/status") {
		t.Fatalf("endpointCheckinStatus=%q; want prefix https://openapi.qoder.sh/...", endpointCheckinStatus)
	}
	if !strings.HasPrefix(endpointCheckinClaim, "https://openapi.qoder.sh/sash/api/v1/me/daily-check-in/claim") {
		t.Fatalf("endpointCheckinClaim=%q; want prefix https://openapi.qoder.sh/...", endpointCheckinClaim)
	}
	if qoderWebsite != "https://qoder.com" {
		t.Fatalf("qoderWebsite=%q; want 'https://qoder.com'", qoderWebsite)
	}
}

// TestQoderAI_CheckinClaimResponseShape verifies the parsing of daily 100 credit claim.
func TestQoderAI_CheckinClaimResponseShape(t *testing.T) {
	raw := "{\"success\": true, \"rewardCredits\": 100, \"result\": \"SUCCESS\"}"
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	rc, ok := m["rewardCredits"].(float64)
	if !ok || int64(rc) != 100 {
		t.Fatalf("rewardCredits=%v; want 100", m["rewardCredits"])
	}
}
