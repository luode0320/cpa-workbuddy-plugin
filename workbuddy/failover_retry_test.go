package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPickNextAuth_NoCurrent(t *testing.T) {
	// pickNextAuth always filters out its currentAuthID arg, so passing
	// the empty string sweeps the whole list — and we have no host RPC
	// available here, so the call must return ok=false without panicking.
	if _, _, ok := pickNextAuth(""); ok {
		t.Fatal("pickNextAuth must return ok=false when host RPC is unavailable")
	}
}

func TestReadAllUpstreamErr(t *testing.T) {
	cases := []struct {
		name string
		in   io.Reader
		want string
	}{
		{"nil reader", nil, ""},
		{"error reader", &errReader{}, ""},
		{"empty body", strings.NewReader(""), ""},
		{"plain text", strings.NewReader("Method Not Allowed"), "Method Not Allowed"},
		{"json body", strings.NewReader(`{"error":"forbidden"}`), `{"error":"forbidden"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readAllUpstreamErr(tc.in); got != tc.want {
				t.Fatalf("readAllUpstreamErr(%v) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestRebuildRequestWithSA_NilInputs(t *testing.T) {
	if _, err := rebuildRequestWithSA(nil, &storedAuth{}); err == nil {
		t.Fatal("rebuildRequestWithSA(nil, sa) must return an error")
	}
	orig, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", bytes.NewReader([]byte(`{}`)))
	if _, err := rebuildRequestWithSA(orig, nil); err == nil {
		t.Fatal("rebuildRequestWithSA(orig, nil) must return an error")
	}
}

func TestRebuildRequestWithSA_NoGetBody(t *testing.T) {
	// A request whose body isn't a *bytes.Reader / *strings.Reader / *bytes.Buffer
	// ends up with GetBody == nil. rebuildRequestWithSA must surface a
	// descriptive error rather than silently rebuilding a request with an
	// empty body.
	orig, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	if orig.GetBody != nil {
		t.Skip("http.NewRequest with nil body returns GetBody != nil on this runtime; unable to construct negative case")
	}
	_, err := rebuildRequestWithSA(orig, &storedAuth{})
	if err == nil {
		t.Fatal("rebuildRequestWithSA with no GetBody must error")
	}
	if !strings.Contains(err.Error(), "GetBody") {
		t.Fatalf("error should mention GetBody, got: %v", err)
	}
}

func TestRebuildRequestWithSA_GetBodyChain(t *testing.T) {
	// Regression for v0.14.3: the same-request rotation chain must survive
	// MULTIPLE rebuilds. Rebuild #1 consumes the original request's GetBody
	// (populated by http.NewRequestWithContext for *bytes.Reader bodies).
	// Rebuild #2 and onward consume the PREVIOUS rebuild's GetBody. If the
	// rebuilt request came back with GetBody == nil, rotation #2 fails with
	// "original request has no GetBody" — exactly the v0.14.2 user symptom
	// (2 x HTTP 429 then stop, despite a 20-account pool).
	payload := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
	orig, err := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if orig.GetBody == nil {
		t.Skip("http.NewRequest with *bytes.Reader body must populate GetBody on this runtime")
	}
	sa := &storedAuth{}
	cur := orig
	for i := 1; i <= 3; i++ {
		next, err := rebuildRequestWithSA(cur, sa)
		if err != nil {
			t.Fatalf("rebuild #%d: %v", i, err)
		}
		if next.GetBody == nil {
			t.Fatalf("rebuild #%d: next.GetBody == nil — rotation chain broken", i)
		}
		rc, err := next.GetBody()
		if err != nil {
			t.Fatalf("rebuild #%d: GetBody() failed: %v", i, err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("rebuild #%d: read body: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("rebuild #%d: body mismatch: got %q want %q", i, got, payload)
		}
		cur = next
	}
}

type errReader struct{}

func (e *errReader) Read(_ []byte) (int, error) { return 0, errors.New("simulated read error") }
