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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHostBridgeOpenTimeoutDoesNotResend 验证 open 看门狗到点时**不重发**上游请求，
// 而是把错误如实返回（由换号逻辑收口）。
//
// 背景（2026-11-28 生产取证）：旧语义「超时即降级直连并重发一次完整请求」会让
// 客户端总耗时 = 看门狗预算 + 第二次上游耗时（生产 8/8 降级事件均为此形态，
// 表现为 33~36s / 123~126s 人造台阶），且同一逻辑请求在上游变成两份。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 17:00:00；改动原因：看门狗语义改为不重发。
func TestHostBridgeOpenTimeoutDoesNotResend(t *testing.T) {
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

	// 1. 桥可用 + 未强制直连，确保走宿主桥分支。
	hostBridgeAvailableFn = func() bool { return true }
	setStreamDirectForce(false)
	setHostBridgeOpenTimeout(100 * time.Millisecond)

	// 2. 只让最内层 open RPC 挂起：看门狗包装仍在，验证它确实能兜住。
	hostStreamOpenCallFn = func(wireJSON []byte) ([]byte, error) {
		<-make(chan struct{})
		return nil, io.EOF
	}

	// 3. 直连 seam 绝不能被调用：调用即代表发生了重发。
	directCalled := false
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		directCalled = true
		return &hostHTTPStream{liveBody: io.NopCloser(strings.NewReader(""))}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost, "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	// 4. open 应在预算内返回错误，而不是永久阻塞、也不应重发。
	started := time.Now()
	stream, _, _, openErr := hostHTTPDoStream(req, "cb-1")
	elapsed := time.Since(started)
	if openErr == nil {
		t.Fatalf("open err = nil; want watchdog error (no resend)")
	}
	if !errors.Is(openErr, errHostBridgeOpenTimeout) {
		t.Fatalf("open err = %v; want it to wrap errHostBridgeOpenTimeout", openErr)
	}
	if directCalled {
		t.Fatalf("direct seam called on watchdog timeout; must not resend the upstream request")
	}
	if stream != nil {
		t.Fatalf("stream = %#v; want nil on watchdog timeout", stream)
	}
	// 5. 必须及时返回：预算是 100ms，给足调度余量后仍应远小于 30s。
	if elapsed > 5*time.Second {
		t.Fatalf("watchdog took %v; want bounded by open budget", elapsed)
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

// TestHostBridgeOpenTimeoutDefaultAboveUpstreamP99 锁定默认预算不低于上游响应头
// 实测 p99，避免把合法慢请求误判成桥故障。
//
// 生产直连实测（2026-11-28，380k token 大上下文）：上游响应头延迟 p50≈5~10s、
// p90≈19.6s、p99≈103s。旧默认 30s 落在 p90~p99 之间，会把正常慢请求掐断并触发
// 整包重发——这正是 33~36s TTFT 台阶（= 30s 白等 + 一次直连耗时）的来源。
// 因此默认值必须显著高于 30s。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-28 15:20:00；改动原因：把「默认值定值依据」变成回归断言，
// 防止有人为了压台阶高度又把它调回 30s 以下。
func TestHostBridgeOpenTimeoutDefaultAboveUpstreamP99(t *testing.T) {
	if hostBridgeOpenTimeoutDefault <= 30*time.Second {
		t.Fatalf("default budget = %v; want > 30s (30s 是历史台阶地板，会掐断 p90~p99 的合法慢请求)",
			hostBridgeOpenTimeoutDefault)
	}
	if hostBridgeOpenTimeoutDefault < 90*time.Second {
		t.Fatalf("default budget = %v; want >= 90s (上游响应头实测 p99≈103s)",
			hostBridgeOpenTimeoutDefault)
	}
}
