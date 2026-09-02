package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestRunTraeAsyncStreamSameAuthRetryWhenNoCandidateThenSucceeds 验证伪完成且池中已无其它候选时，
// 插件对当前账号同号退避重试一次（窗口性限流自愈），重试成功则正常收尾而非判 pool exhausted。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-02 00:10:00；改动原因：锁定 FIX-A 单账号池同号重试行为（生产实证 2351：
// 同号窗口性限流约 60s 自愈，直接 pool exhausted 会让单号池请求必败）。
func TestRunTraeAsyncStreamSameAuthRetryWhenNoCandidateThenSucceeds(t *testing.T) {
	disableUsageOutputs(t)
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	opened := make([]string, 0, 2)
	upstreamCloses := 0
	clientCloses := 0
	var emitted [][]byte
	call := 0
	deps := traeAsyncStreamDeps{
		Open: func(_ *traeAuth, _ map[string]any, authID, _ string) (traeAsyncUpstream, int, error) {
			opened = append(opened, authID)
			call++
			var text string
			if call == 1 {
				// 第一次同号伪完成：短内容，必须对客户端零泄漏。
				text = "SENTINEL_SAME_AUTH_PSEUDO_MUST_NOT_REACH_CLIENT"
			} else {
				text = strings.Repeat("SENTINEL_SAME_AUTH_COMPLETE_RESULT", 20)
			}
			return traeAsyncUpstream{
				Reader: strings.NewReader(streamTestResponse(text).BodyString()),
				Close:  func() { upstreamCloses++ },
			}, 200, nil
		},
		// 单账号池：无其它候选。
		PickNextAuth: func(string) (string, *traeAuth, bool) { return "", nil, false },
		Emit: func(_ string, payload []byte) error {
			emitted = append(emitted, bytes.Clone(payload))
			return nil
		},
		Close: func(string) { clientCloses++ },
	}
	runTraeAsyncStream(authA, "auth-a", traeAsyncStreamContext{
		StreamID: "client-stream-same-auth", Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max",
		Started: time.Now(), InputChars: 3000, Budget: 1,
	}, deps)

	if len(opened) != 2 || opened[0] != "auth-a" || opened[1] != "auth-a" {
		t.Fatalf("opened = %v, want [auth-a auth-a]", opened)
	}
	if upstreamCloses != 2 || clientCloses != 1 {
		t.Fatalf("closes = upstream:%d client:%d, want 2/1", upstreamCloses, clientCloses)
	}
	joined := bytes.Join(emitted, nil)
	if bytes.Contains(joined, []byte("SENTINEL_SAME_AUTH_PSEUDO_MUST_NOT_REACH_CLIENT")) {
		t.Fatal("same-auth pseudo-completion leaked into client stream")
	}
	if !bytes.Contains(joined, []byte("SENTINEL_SAME_AUTH_COMPLETE_RESULT")) {
		t.Fatal("same-auth retry complete result missing from client stream")
	}
	if countFinishReason(emitted, "stop") != 1 {
		t.Fatalf("stop count = %d, want 1", countFinishReason(emitted, "stop"))
	}
	// 伪完成核算后，同号重试成功会 resetAccountFailover 清零。
	if count, _, _ := failoverStateSnapshot("auth-a"); count != 0 {
		t.Fatalf("auth-a failure count = %d, want 0 after successful same-auth retry", count)
	}
}

// TestRunTraeSyncStreamSameAuthRetryWhenNoCandidateThenSucceeds 验证同步流等价行为：
// 伪完成且无其它候选时同号退避重试，重试成功则返回完整结果。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-02 00:10:00；改动原因：与异步协调器收敛一致的 FIX-A 单号池同号重试。
func TestRunTraeSyncStreamSameAuthRetryWhenNoCandidateThenSucceeds(t *testing.T) {
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	calls := make([]string, 0, 2)
	call := 0
	deps := traeSyncStreamDeps{
		CallLLM: func(_ *traeAuth, _ map[string]any, authID string) (*hostHTTPResponse, error) {
			calls = append(calls, authID)
			call++
			var text string
			if call == 1 {
				text = "SENTINEL_SAME_AUTH_PSEUDO_MUST_NOT_REACH_CLIENT"
			} else {
				text = strings.Repeat("SENTINEL_SAME_AUTH_COMPLETE_RESULT", 20)
			}
			return streamTestResponse(text), nil
		},
		PickNextAuth: func(string) (string, *traeAuth, bool) { return "", nil, false },
	}
	chunks, err := runTraeSyncStream(authA, map[string]any{"stream": true}, traeSyncStreamContext{
		Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max", AuthID: "auth-a", AuthUID: authA.UserID,
		Started: time.Now(), InputChars: 3000, Budget: 1,
	}, deps)
	if err != nil {
		t.Fatalf("runTraeSyncStream: %v", err)
	}
	if len(calls) != 2 || calls[0] != "auth-a" || calls[1] != "auth-a" {
		t.Fatalf("calls = %v, want [auth-a auth-a]", calls)
	}
	joined := joinStreamChunkPayloads(chunks)
	if bytes.Contains(joined, []byte("SENTINEL_SAME_AUTH_PSEUDO_MUST_NOT_REACH_CLIENT")) {
		t.Fatal("same-auth pseudo-completion leaked into the response")
	}
	if !bytes.Contains(joined, []byte("SENTINEL_SAME_AUTH_COMPLETE_RESULT")) {
		t.Fatal("same-auth retry complete result missing from the response")
	}
	if count, _, _ := failoverStateSnapshot("auth-a"); count != 0 {
		t.Fatalf("auth-a failure count = %d, want 0 after successful same-auth retry", count)
	}
}
