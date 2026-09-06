// executor_open_failover_test.go 锁定 open 阶段失败的换号契约：transport 级
// 错误（TTFB 超时等 status=0）必须与账号级 4xx 同责换号，池中健康候选要在同
// 一请求内被尝试；400 业务错仍直通。生产实证 stream 4553（2026-09-06）：上游
// 对单账号挂起 60s 不回响应头，旧判定 isAccountLevel4xx(0)=false 直接终局，
// 池中健康账号未被尝试，trae 会话一次失败即整请求失败。
package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	testTransportTimeoutErr = `llm_utils_chat stream transport: Post "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat": net/http: timeout awaiting response headers`
	testUpstream400Err      = `upstream 400: {"error":{"code":"invalid_request"}}`
	testHealthySSE          = "event: output\ndata: {\"response\":\"ok\"}\n\nevent: done\ndata: {}\n\n"
)

// asyncOpenTimeoutDeps 构造注入依赖：auth-a 恒定 transport 超时（或 400），
// auth-b 恒定健康流；openOrder/callOrder 记录尝试顺序供断言。
func asyncOpenTimeoutDeps(openErrForA error, openOrder *[]string, emitted *[]string) traeAsyncStreamDeps {
	return traeAsyncStreamDeps{
		Open: func(a *traeAuth, payload map[string]any, authID, hostCallbackID string) (traeAsyncUpstream, int, error) {
			*openOrder = append(*openOrder, authID)
			if authID == "auth-a" {
				return traeAsyncUpstream{}, 0, openErrForA
			}
			return traeAsyncUpstream{Reader: strings.NewReader(testHealthySSE), Close: func() {}}, 200, nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID == "auth-a" {
				return "auth-b", &traeAuth{UserID: "uid-b"}, true
			}
			return "", nil, false
		},
		Emit: func(streamID string, payload []byte) error {
			*emitted = append(*emitted, string(payload))
			return nil
		},
		Close: func(streamID string) {},
	}
}

// TestAsyncOpenTransportTimeoutRotatesAuth 验证异步协调器在 open 阶段遭遇
// transport 超时（status=0）时换号到池中健康候选，而不是直接终局。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-06 23:50:00；改动原因：0.1.53 回归锁定 stream 4553 修复。
func TestAsyncOpenTransportTimeoutRotatesAuth(t *testing.T) {
	var openOrder []string
	var emitted []string
	deps := asyncOpenTimeoutDeps(fmt.Errorf(testTransportTimeoutErr), &openOrder, &emitted)

	runTraeAsyncStream(&traeAuth{UserID: "uid-a"}, "auth-a", traeAsyncStreamContext{
		StreamID:       "stream-test",
		HostCallbackID: "cb-test",
		Model:          "DeepSeek-V4-Flash",
		UpstreamModel:  "deepseek-v4-flash",
		Payload:        map[string]any{"prompt": "hi"},
		Started:        time.Now().Add(-time.Second),
		InputChars:     10,
		Budget:         3,
		SessionKey:     "sess-test",
	}, deps)

	if len(openOrder) != 2 || openOrder[0] != "auth-a" || openOrder[1] != "auth-b" {
		t.Fatalf("open order = %v, want [auth-a auth-b] (rotate on transport timeout)", openOrder)
	}
	joined := strings.Join(emitted, "\n")
	if !strings.Contains(joined, "ok") {
		t.Fatalf("emitted payload missing healthy auth-b content; got %q", joined)
	}
	if strings.Contains(joined, "timeout awaiting response headers") {
		t.Fatalf("timeout error must not surface to client when a healthy candidate exists; got %q", joined)
	}
}

// TestSyncOpenTransportTimeoutRotatesAuth 验证同步协调器同样在 open 阶段
// transport 超时（status=0）时换号重试。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-06 23:50:00；改动原因：0.1.53 回归锁定 sync 分支同款修复。
func TestSyncOpenTransportTimeoutRotatesAuth(t *testing.T) {
	var callOrder []string
	deps := traeSyncStreamDeps{
		CallLLM: func(a *traeAuth, payload map[string]any, authID string) (*hostHTTPResponse, error) {
			callOrder = append(callOrder, authID)
			if authID == "auth-a" {
				return nil, fmt.Errorf(`llm_utils_chat transport: Post "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat": net/http: timeout awaiting response headers`)
			}
			return &hostHTTPResponse{StatusCode: 200, Body: []byte(testHealthySSE)}, nil
		},
		PickNextAuth: func(currentAuthID string) (string, *traeAuth, bool) {
			if currentAuthID == "auth-a" {
				return "auth-b", &traeAuth{UserID: "uid-b"}, true
			}
			return "", nil, false
		},
	}

	chunks, err := runTraeSyncStream(&traeAuth{UserID: "uid-a"}, map[string]any{"prompt": "hi"}, traeSyncStreamContext{
		Model:         "DeepSeek-V4-Flash",
		UpstreamModel: "deepseek-v4-flash",
		AuthID:        "auth-a",
		AuthUID:       "uid-a",
		Started:       time.Now().Add(-time.Second),
		InputChars:    10,
		Budget:        3,
		SessionKey:    "sess-test",
	}, deps)

	if err != nil {
		t.Fatalf("sync stream must recover via rotation, got error: %v", err)
	}
	if len(callOrder) != 2 || callOrder[0] != "auth-a" || callOrder[1] != "auth-b" {
		t.Fatalf("call order = %v, want [auth-a auth-b]", callOrder)
	}
	if len(chunks) == 0 {
		t.Fatal("healthy auth-b chunks must be returned")
	}
}

// TestAsyncOpenBusiness400DoesNotRotate 验证 400 业务错（请求本身的问题，与账号
// 无关）仍然直通终局，不烧换号预算——既有 kill switch 契约不回归。
//
// [参数] t: 当前测试。
// [返回] 无；断言失败时由 testing 终止用例。
// 最近修改时间：2026-09-06 23:50:00；改动原因：isAccountFailure 判定替换后锁定 400 直通。
func TestAsyncOpenBusiness400DoesNotRotate(t *testing.T) {
	var openOrder []string
	var emitted []string
	deps := asyncOpenTimeoutDeps(fmt.Errorf(testUpstream400Err), &openOrder, &emitted)

	runTraeAsyncStream(&traeAuth{UserID: "uid-a"}, "auth-a", traeAsyncStreamContext{
		StreamID:       "stream-test",
		HostCallbackID: "cb-test",
		Model:          "DeepSeek-V4-Flash",
		UpstreamModel:  "deepseek-v4-flash",
		Payload:        map[string]any{"prompt": "hi"},
		Started:        time.Now().Add(-time.Second),
		InputChars:     10,
		Budget:         3,
		SessionKey:     "sess-test",
	}, deps)

	if len(openOrder) != 1 {
		t.Fatalf("open order = %v, want exactly one attempt for business 400 (no rotation)", openOrder)
	}
	joined := strings.Join(emitted, "\n")
	if !strings.Contains(joined, "error") {
		t.Fatalf("business 400 must surface the error to client; got %q", joined)
	}
}
