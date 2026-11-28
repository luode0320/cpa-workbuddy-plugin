// host_bridge_open_timeout_test.go 锁定宿主桥 open 阶段的降级语义。
//
// 背景（2026-11-28 生产取证）：宿主 host.http.do_stream 是**同步阻塞到上游响应头**
// 才返回 stream_id 的 RPC，且宿主侧 ResponseHeaderTimeout 与 client.Timeout 双为 0
// ——宿主自己不做超时，open 可无限期挂起。插件侧 hostBridgeOpenTimeout 因此是唯一
// 兜底，超时后降级直连。TTFT 台阶 = 该预算 + 一次直连耗时。
//
// 该路径此前有三个缺陷，本文件分别回归：
//  1. 降级分支完全静默（无任何日志），生产 30s 台阶只能靠 TTFT 分布反推；
//  2. 预算写死 30s，无法在不发版的前提下压低台阶高度；
//  3. 看门狗本身零测试覆盖（全仓 *_test.go 里没有任何 hostBridgeOpenTimeout 引用），
//     所以 30s 白等长期无人发现。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHostBridgeOpenTimeoutRetriesOnNewConnection 验证 open 看门狗到点时**换一条新连接
// 重发**（第 2 次走直连），而不是干等或直接失败。
//
// 背景（2026-11-29 直测上游）：`event:timing_cost` 显示上游自身处理只要
// server_processing_time=901~1575ms（810KB / 152k tokens 亦然），而客户端观测的
// 响应头延迟会随机飙到 20~110s —— 长尾发生在响应头到达之前的链路上，且是
// 连接级的。同 body 8 次对照：单次干等 p90=max=80.7s；6~8s 看门狗 + 最多 3 次
// 重发 p90=max=11.7s。所以「卡住」的正确响应是换连接，不是等。
//
// 旧语义「超时即如实报错，绝不重发」的前提（上游真需要那么久）已被推翻：
// 它把上游拥塞全量转嫁给客户端，实测就是 20~110s 的首字。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：语义由「超时即失败」改为「超时即换连接重试」。
func TestHostBridgeOpenTimeoutRetriesOnNewConnection(t *testing.T) {
	origOpenCall := hostStreamOpenCallFn
	origDirect := hostStreamDirectFn
	origAvailable := hostBridgeAvailableFn
	origForce := streamDirectForced()
	origBudget := loadedHostBridgeOpenTimeout()
	origAttempts := streamOpenMaxAttempts
	t.Cleanup(func() {
		hostStreamOpenCallFn = origOpenCall
		hostStreamDirectFn = origDirect
		hostBridgeAvailableFn = origAvailable
		setStreamDirectForce(origForce)
		setHostBridgeOpenTimeout(origBudget)
		streamOpenMaxAttempts = origAttempts
	})

	// 1. 桥可用 + 未强制直连，确保第 1 次走宿主桥分支。
	hostBridgeAvailableFn = func() bool { return true }
	setStreamDirectForce(false)
	setHostBridgeOpenTimeout(100 * time.Millisecond)
	streamOpenMaxAttempts = 3

	// 2. 只让最内层 open RPC 挂起：看门狗包装仍在，验证它确实能兜住。
	hostStreamOpenCallFn = func(wireJSON []byte) ([]byte, error) {
		<-make(chan struct{})
		return nil, io.EOF
	}

	// 3. 直连 seam 必须被调用且只调用一次：那就是「换连接重发」。
	directCalls := 0
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalls++
		return &hostHTTPStream{liveBody: io.NopCloser(strings.NewReader(""))}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	// 4. 第 1 次在预算内被判超时，第 2 次直连成功。
	started := time.Now()
	stream, status, _, openErr := hostHTTPDoStream(req, "cb-1")
	elapsed := time.Since(started)
	if openErr != nil {
		t.Fatalf("open err = %v; want retry to recover", openErr)
	}
	if status != 200 {
		t.Fatalf("status = %d; want 200", status)
	}
	if directCalls != 1 {
		t.Fatalf("direct attempts = %d; want 1 (watchdog must retry on a new connection)", directCalls)
	}
	if stream == nil {
		t.Fatal("stream = nil; want the recovered stream")
	}
	// 5. 必须及时返回：预算是 100ms，给足调度余量后仍应远小于 30s。
	if elapsed > 5*time.Second {
		t.Fatalf("open took %v; want bounded by open budget", elapsed)
	}
}

// TestHostBridgeOpenSuccessKeepsStreamID 验证 open 正常返回时保留 streamID，
// 不触发降级。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 14:30:00；改动原因：锁定非超时路径不被降级误伤。
func TestHostBridgeOpenSuccessKeepsStreamID(t *testing.T) {
	origOpenCall := hostStreamOpenCallFn
	origDirect := hostStreamDirectFn
	origAvailable := hostBridgeAvailableFn
	origForce := streamDirectForced()
	origBudget := loadedHostBridgeOpenTimeout()
	t.Cleanup(func() {
		hostStreamOpenCallFn = origOpenCall
		hostStreamDirectFn = origDirect
		hostBridgeAvailableFn = origAvailable
		setStreamDirectForce(origForce)
		setHostBridgeOpenTimeout(origBudget)
	})

	hostBridgeAvailableFn = func() bool { return true }
	setStreamDirectForce(false)
	setHostBridgeOpenTimeout(5 * time.Second)

	hostStreamOpenCallFn = func(wireJSON []byte) ([]byte, error) {
		raw := `{"status_code":200,"stream_id":"stream-ok"}`
		env := envelope{OK: true, Result: json.RawMessage(raw)}
		return json.Marshal(env)
	}
	directCalled := false
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalled = true
		return nil, 0, nil, nil
	}

	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	stream, statusCode, _, openErr := hostHTTPDoStream(req, "cb-2")
	if openErr != nil {
		t.Fatalf("open err = %v; want success", openErr)
	}
	if directCalled {
		t.Fatalf("direct seam called on successful bridge open")
	}
	if statusCode != 200 {
		t.Fatalf("statusCode = %d; want 200", statusCode)
	}
	if stream == nil || stream.streamID != "stream-ok" {
		t.Fatalf("stream = %#v; want streamID stream-ok", stream)
	}
}

// TestHostBridgeOpenTimeoutConfigurable 验证配置项 host_bridge_open_timeout_seconds
// 能改写入参，且非正数被忽略（缺省键保持当前值的 kill-switch 语义）。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 14:30:00；改动原因：新增运行时旋钮，需锁定解析边界。
func TestHostBridgeOpenTimeoutConfigurable(t *testing.T) {
	origBudget := loadedHostBridgeOpenTimeout()
	t.Cleanup(func() { setHostBridgeOpenTimeout(origBudget) })

	// 1. 正常写入。
	setHostBridgeOpenTimeout(12 * time.Second)
	if got := loadedHostBridgeOpenTimeout(); got != 12*time.Second {
		t.Fatalf("budget = %v; want 12s", got)
	}
	// 2. 非正数忽略，保持既有值。
	setHostBridgeOpenTimeout(0)
	if got := loadedHostBridgeOpenTimeout(); got != 12*time.Second {
		t.Fatalf("budget after 0 = %v; want unchanged 12s", got)
	}
	setHostBridgeOpenTimeout(-5 * time.Second)
	if got := loadedHostBridgeOpenTimeout(); got != 12*time.Second {
		t.Fatalf("budget after -5s = %v; want unchanged 12s", got)
	}
	// 3. 错误文案随预算同步刷新，避免日志里报旧值。
	if !strings.Contains(errHostBridgeOpenTimeout.Error(), "12s") {
		t.Fatalf("err text = %q; want it to carry current budget 12s", errHostBridgeOpenTimeout.Error())
	}
}

// TestHostBridgeOpenTimeoutDefaultIsShortPerAttempt 锁定默认预算是**秒级单次尝试**
// 预算，而不是历史那个 180s 的「等到上游尾部」。
//
// 旧断言的依据是「上游响应头 p99≈103s，所以预算必须 >= 90s 才是如实等待」。
// 该依据已被直测推翻（2026-11-29）：上游自身处理只要 server_processing_time=
// 901~1575ms，20~110s 全部发生在响应头到达之前，且换连接重发即可绕开
// （同 body 8 次：干等 p90=max=80.7s vs 预算+重试 p90=max=11.7s）。
// 短预算 + 重试环的总耗时上界 ≈ 6s × 3 = 18s，远优于干等 80s。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：默认值由 180s 改为 6s，断言同步反过来，
// 防止有人再把它调回「等到上游尾部」导致重试环前面白等三分钟。
func TestHostBridgeOpenTimeoutDefaultIsShortPerAttempt(t *testing.T) {
	if hostBridgeOpenTimeoutDefault > 10*time.Second {
		t.Fatalf("default budget = %v; want <= 10s（单次尝试预算必须短，重试才划算）",
			hostBridgeOpenTimeoutDefault)
	}
	if hostBridgeOpenTimeoutDefault < 3*time.Second {
		t.Fatalf("default budget = %v; want >= 3s（健康窗口响应头实测最大 4.6s，太短会误伤健康请求）",
			hostBridgeOpenTimeoutDefault)
	}
	// 总上界必须仍然可控：预算 × 尝试次数不应超过 20s。
	if total := hostBridgeOpenTimeoutDefault * time.Duration(streamOpenMaxAttemptsDefault); total > 20*time.Second {
		t.Fatalf("worst case = %v; want <= 20s (budget × attempts)", total)
	}
}
