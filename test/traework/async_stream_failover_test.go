package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRunTraeAsyncStreamRetriesPseudoCompletionWithoutLeakingFirstAttempt 验证异步流在同一 StreamID 内从 A 切到 B。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:50:00；改动原因：锁定失败账号零泄漏、callback 复用和唯一终止。
func TestRunTraeAsyncStreamRetriesPseudoCompletionWithoutLeakingFirstAttempt(t *testing.T) {
	disableUsageOutputs(t)
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	opened := make([]string, 0, 2)
	callbacks := make([]string, 0, 2)
	upstreamCloses := map[string]int{}
	var emitted [][]byte
	clientCloses := 0
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error) {
			opened = append(opened, authID)
			callbacks = append(callbacks, hostCallbackID)
			text := "SENTINEL_FIRST_ACCOUNT_MUST_NOT_REACH_CLIENT"
			if authID == "auth-b" {
				text = strings.Repeat("SENTINEL_SECOND_ACCOUNT_COMPLETE_RESULT", 20)
			}
			return traeAsyncUpstream{
				Reader: strings.NewReader(streamTestResponse(text).BodyString()),
				Close:  func() { upstreamCloses[authID]++ },
			}, 200, nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID != "auth-a" {
				t.Fatalf("unexpected current auth: %s", currentAuthID)
			}
			return "auth-b", authB, true
		},
		Emit: func(streamID string, payload []byte) error {
			if streamID != "client-stream-1" {
				t.Fatalf("streamID = %q", streamID)
			}
			emitted = append(emitted, bytes.Clone(payload))
			return nil
		},
		Close: func(streamID string) {
			if streamID != "client-stream-1" {
				t.Fatalf("close streamID = %q", streamID)
			}
			clientCloses++
		},
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID:       "client-stream-1",
		HostCallbackID: "callback-1",
		Model:          "qwen-max-latest",
		UpstreamModel:  "qwen3.8-max",
		Payload:        map[string]any{"stream": true},
		Started:        time.Now(),
		InputChars:     3000,
		Budget:         1,
	}, deps)

	if len(opened) != 2 || opened[0] != "auth-a" || opened[1] != "auth-b" {
		t.Fatalf("opened = %v, want [auth-a auth-b]", opened)
	}
	if len(callbacks) != 2 || callbacks[0] != "callback-1" || callbacks[1] != "callback-1" {
		t.Fatalf("callbacks = %v", callbacks)
	}
	if upstreamCloses["auth-a"] != 1 || upstreamCloses["auth-b"] != 1 {
		t.Fatalf("upstream closes = %v", upstreamCloses)
	}
	if clientCloses != 1 {
		t.Fatalf("client closes = %d, want 1", clientCloses)
	}
	joined := bytes.Join(emitted, nil)
	if bytes.Contains(joined, []byte("SENTINEL_FIRST_ACCOUNT_MUST_NOT_REACH_CLIENT")) {
		t.Fatal("first account pseudo-completion leaked into client stream")
	}
	if !bytes.Contains(joined, []byte("SENTINEL_SECOND_ACCOUNT_COMPLETE_RESULT")) {
		t.Fatal("second account result missing from client stream")
	}
	if countFinishReason(emitted, "stop") != 1 {
		t.Fatalf("stop count = %d, want 1", countFinishReason(emitted, "stop"))
	}
	if count, _, _ := failoverStateSnapshot("auth-a"); count != 1 {
		t.Fatalf("auth-a failure count = %d, want 1", count)
	}
}

// TestRunTraeAsyncStreamPseudoPoolExhaustionClosesWithError 验证伪完成池耗尽时只发错误并关闭一次。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:50:00；改动原因：禁止把最后一个伪完成释放为成功。
func TestRunTraeAsyncStreamPseudoPoolExhaustionClosesWithError(t *testing.T) {
	disableUsageOutputs(t)
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	upstreamCloses := 0
	clientCloses := 0
	var emitted [][]byte
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, authID, _ string) (traeAsyncUpstream, int, error) {
			return traeAsyncUpstream{
				Reader: strings.NewReader(streamTestResponse("pseudo-" + authID).BodyString()),
				Close:  func() { upstreamCloses++ },
			}, 200, nil
		},
		PickNextAuth: func(string) (string, *traeAuth, bool) { return "auth-b", authB, true },
		Emit: func(_ string, payload []byte) error {
			emitted = append(emitted, bytes.Clone(payload))
			return nil
		},
		Close: func(string) { clientCloses++ },
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID: "client-stream-pool", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000, Budget: 1,
	}, deps)

	if upstreamCloses != 2 || clientCloses != 1 {
		t.Fatalf("closes = upstream:%d client:%d", upstreamCloses, clientCloses)
	}
	if countFinishReason(emitted, "stop") != 0 {
		t.Fatal("pool exhaustion must not emit stop")
	}
	if !bytes.Contains(bytes.Join(emitted, nil), []byte("account pool exhausted after 2 attempt(s)")) {
		t.Fatalf("missing pool exhaustion error: %q", emitted)
	}
}

// TestRunTraeAsyncStreamDoesNotRetryAfterEmitFailure 验证客户端下发失败后不会再开备用账号。
func TestRunTraeAsyncStreamDoesNotRetryAfterEmitFailure(t *testing.T) {
	disableUsageOutputs(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	openCount := 0
	clientCloses := 0
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, _ string, _ string) (traeAsyncUpstream, int, error) {
			openCount++
			return traeAsyncUpstream{Reader: strings.NewReader(streamTestResponse(strings.Repeat("healthy", 100)).BodyString()), Close: func() {}}, 200, nil
		},
		PickNextAuth: func(string) (string, *traeAuth, bool) {
			t.Fatal("emit failure must not select another account")
			return "", nil, false
		},
		Emit:  func(string, []byte) error { return errors.New("client stream closed") },
		Close: func(string) { clientCloses++ },
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID: "client-stream-cancel", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000, Budget: 1,
	}, deps)
	if openCount != 1 || clientCloses != 1 {
		t.Fatalf("open/close = %d/%d, want 1/1", openCount, clientCloses)
	}
}

// TestRunTraeAsyncStreamDoesNotRetryAnAccountTwice 验证关闭跨请求冷却后，异步逻辑请求仍不会 A→B→A 回跳。
func TestRunTraeAsyncStreamDoesNotRetryAnAccountTwice(t *testing.T) {
	disableUsageOutputs(t)
	resetFailover(t)
	setFailoverEnabled(false)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	opened := make([]string, 0, 2)
	clientCloses := 0
	var emitted [][]byte
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, authID, _ string) (traeAsyncUpstream, int, error) {
			opened = append(opened, authID)
			return traeAsyncUpstream{
				Reader: strings.NewReader(streamTestResponse("pseudo-" + authID).BodyString()),
				Close:  func() {},
			}, 200, nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID == "auth-a" {
				return "auth-b", authB, true
			}
			return "auth-a", authA, true
		},
		Emit: func(_ string, payload []byte) error {
			emitted = append(emitted, bytes.Clone(payload))
			return nil
		},
		Close: func(string) { clientCloses++ },
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID: "client-stream-cycle", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000, Budget: 3,
	}, deps)
	if len(opened) != 2 || opened[0] != "auth-a" || opened[1] != "auth-b" {
		t.Fatalf("opened = %v, want [auth-a auth-b]", opened)
	}
	if clientCloses != 1 {
		t.Fatalf("client closes = %d, want 1", clientCloses)
	}
	if !bytes.Contains(bytes.Join(emitted, nil), []byte("account pool exhausted after 2 attempt(s)")) {
		t.Fatalf("missing two-attempt pool exhaustion error: %q", emitted)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("reader panic")
}

// TestRunTraeAsyncStreamClosesUpstreamOnPanic 验证 attempt 内 panic 关闭上游并由协调器统一结束客户端流。
func TestRunTraeAsyncStreamClosesUpstreamOnPanic(t *testing.T) {
	disableUsageOutputs(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	upstreamCloses := 0
	clientCloses := 0
	var emitted [][]byte
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, _, _ string) (traeAsyncUpstream, int, error) {
			return traeAsyncUpstream{Reader: panicReader{}, Close: func() { upstreamCloses++ }}, 200, nil
		},
		Emit: func(_ string, payload []byte) error {
			emitted = append(emitted, bytes.Clone(payload))
			return nil
		},
		Close: func(string) { clientCloses++ },
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID: "client-stream-panic", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000,
	}, deps)
	if upstreamCloses != 1 || clientCloses != 1 {
		t.Fatalf("closes = upstream:%d client:%d, want 1/1", upstreamCloses, clientCloses)
	}
	if len(emitted) != 1 || !bytes.Contains(emitted[0], []byte("coordinator panic")) {
		t.Fatalf("panic error payloads = %q", emitted)
	}
}

func countFinishReason(payloads [][]byte, reason string) int {
	needle := []byte(`"finish_reason":"` + reason + `"`)
	count := 0
	for _, payload := range payloads {
		if bytes.Contains(payload, needle) {
			count++
		}
	}
	return count
}

func disableUsageOutputs(t *testing.T) {
	t.Helper()
	usageFeedMu.Lock()
	oldEnabled := usageFeedEnabled
	oldPath := usageFeedPath
	usageFeedEnabled = false
	usageFeedPath = ""
	usageFeedMu.Unlock()
	setUsageReport("", "")
	t.Cleanup(func() {
		usageFeedMu.Lock()
		usageFeedEnabled = oldEnabled
		usageFeedPath = oldPath
		usageFeedMu.Unlock()
	})
}

func (r *hostHTTPResponse) BodyString() string {
	if r == nil {
		return ""
	}
	return string(r.Body)
}
