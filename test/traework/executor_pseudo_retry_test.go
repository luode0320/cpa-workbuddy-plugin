package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestRunTraeSyncStreamRetriesPseudoCompletionOnSameRequest 验证同步流丢弃账号 A 的伪完成并只返回账号 B。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:40:00；改动原因：锁定伪完成不能作为成功返回，必须在当前请求内换号。
func TestRunTraeSyncStreamRetriesPseudoCompletionOnSameRequest(t *testing.T) {
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	calls := make([]string, 0, 2)
	deps := traeSyncStreamDeps{
		CallLLM: func(_ *traeAuth, _ map[string]any, authID string) (*hostHTTPResponse, error) {
			calls = append(calls, authID)
			if authID == "auth-a" {
				return streamTestResponse("SENTINEL_FIRST_ACCOUNT_MUST_NOT_REACH_CLIENT"), nil
			}
			return streamTestResponse(strings.Repeat("SENTINEL_SECOND_ACCOUNT_COMPLETE_RESULT", 20)), nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID != "auth-a" {
				t.Fatalf("unexpected current auth: %s", currentAuthID)
			}
			return "auth-b", authB, true
		},
	}
	chunks, err := runTraeSyncStream(authA, map[string]any{"stream": true}, traeSyncStreamContext{
		Model:         "qwen-max-latest",
		UpstreamModel: "qwen3.8-max",
		AuthID:        "auth-a",
		AuthUID:       authA.UserID,
		Started:       time.Now(),
		InputChars:    3000,
		Budget:        1,
	}, deps)
	if err != nil {
		t.Fatalf("runTraeSyncStream: %v", err)
	}
	if len(calls) != 2 || calls[0] != "auth-a" || calls[1] != "auth-b" {
		t.Fatalf("calls = %v, want [auth-a auth-b]", calls)
	}
	joined := joinStreamChunkPayloads(chunks)
	if bytes.Contains(joined, []byte("SENTINEL_FIRST_ACCOUNT_MUST_NOT_REACH_CLIENT")) {
		t.Fatal("first account pseudo-completion leaked into the response")
	}
	if !bytes.Contains(joined, []byte("SENTINEL_SECOND_ACCOUNT_COMPLETE_RESULT")) {
		t.Fatal("second account result missing from the response")
	}
	if count, _, _ := failoverStateSnapshot("auth-a"); count != 1 {
		t.Fatalf("auth-a failure count = %d, want 1", count)
	}
}

// TestRunTraeSyncStreamFailsWhenPseudoCompletionPoolIsExhausted 验证所有账号伪完成时显式失败。
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-01 23:40:00；改动原因：池耗尽不得释放最后一次短结果伪装成功。
func TestRunTraeSyncStreamFailsWhenPseudoCompletionPoolIsExhausted(t *testing.T) {
	resetFailover(t)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	deps := traeSyncStreamDeps{
		CallLLM: func(_ *traeAuth, _ map[string]any, authID string) (*hostHTTPResponse, error) {
			return streamTestResponse("pseudo-" + authID), nil
		},
		PickNextAuth: func(string) (string, *traeAuth, bool) {
			return "auth-b", authB, true
		},
	}
	chunks, err := runTraeSyncStream(authA, nil, traeSyncStreamContext{
		Model:         "qwen-max-latest",
		UpstreamModel: "qwen3.8-max",
		AuthID:        "auth-a",
		AuthUID:       authA.UserID,
		Started:       time.Now(),
		InputChars:    3000,
		Budget:        1,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "account pool exhausted after 2 attempt(s)") {
		t.Fatalf("err = %v, want pool exhausted", err)
	}
	if chunks != nil {
		t.Fatalf("chunks = %d, want nil", len(chunks))
	}
}

// TestRunTraeSyncStreamDoesNotRetryAnAccountTwice 验证关闭跨请求冷却后，同一逻辑请求仍不会 A→B→A 回跳。
func TestRunTraeSyncStreamDoesNotRetryAnAccountTwice(t *testing.T) {
	resetFailover(t)
	setFailoverEnabled(false)
	authA := &traeAuth{Token: "token-a", UserID: "uid-a"}
	authB := &traeAuth{Token: "token-b", UserID: "uid-b"}
	calls := make([]string, 0, 2)
	deps := traeSyncStreamDeps{
		CallLLM: func(_ *traeAuth, _ map[string]any, authID string) (*hostHTTPResponse, error) {
			calls = append(calls, authID)
			return streamTestResponse("pseudo-" + authID), nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID == "auth-a" {
				return "auth-b", authB, true
			}
			return "auth-a", authA, true
		},
	}
	chunks, err := runTraeSyncStream(authA, nil, traeSyncStreamContext{
		Model: "qwen-max-latest", UpstreamModel: "qwen3.8-max", AuthID: "auth-a", AuthUID: authA.UserID,
		Started: time.Now(), InputChars: 3000, Budget: 3,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "account pool exhausted after 2 attempt(s)") {
		t.Fatalf("err = %v, want two-attempt pool exhaustion", err)
	}
	if chunks != nil {
		t.Fatalf("chunks = %d, want nil", len(chunks))
	}
	if len(calls) != 2 || calls[0] != "auth-a" || calls[1] != "auth-b" {
		t.Fatalf("calls = %v, want [auth-a auth-b]", calls)
	}
}

func streamTestResponse(text string) *hostHTTPResponse {
	body := "event: output\ndata: {\"response\":\"" + text + "\"}\n\nevent: done\ndata: {}\n\n"
	return &hostHTTPResponse{StatusCode: 200, Body: []byte(body)}
}

func joinStreamChunkPayloads(chunks []pluginapi.ExecutorStreamChunk) []byte {
	var out bytes.Buffer
	for _, chunk := range chunks {
		out.Write(chunk.Payload)
	}
	return out.Bytes()
}
