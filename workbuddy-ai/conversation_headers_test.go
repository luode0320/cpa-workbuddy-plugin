package main

import (
	"net/http"
	"regexp"
	"testing"
)

func TestBackendHeadersConversationTrace(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 2 {
		req, err := http.NewRequest(http.MethodPost, endpointChat, nil)
		if err != nil {
			t.Fatal(err)
		}
		backendHeaders(req, &storedAuth{})
		for _, key := range []string{"X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID"} {
			value := req.Header.Get(key)
			if !pattern.MatchString(value) || seen[value] {
				t.Fatalf("invalid or reused %s: %q", key, value)
			}
			seen[value] = true
		}
		if req.Header.Get("X-Root-Request-ID") != req.Header.Get("X-Conversation-Request-ID") {
			t.Fatal("root request ID must identify this request")
		}
	}
}
