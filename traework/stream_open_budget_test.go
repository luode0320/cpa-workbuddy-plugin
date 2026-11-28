package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// 回归目标：单次尝试的预算必须覆盖「建连 + 上传请求体 + 等响应头」全过程。
//
// 生产实测（2026-11-29，0.5.68 上线后注入大上下文）：
//
//	stream open retry: attempt=1/3 elapsed_ms=25193 budget_ms=6000 body_bytes=1200517
//	stream open retry: attempt=1/3 elapsed_ms=32019 budget_ms=6000 body_bytes=1200517
//
// 单次尝试实际耗时是预算的 4~5 倍。根因是当时只靠 transport 的
// ResponseHeaderTimeout——它的计时起点是**请求体写完**之后，上传本身不受约束；
// 1.2MB body 上传要 ~19s，于是重试环的上界（预算×尝试次数）完全失效，
// 首字仍会出现 25~32s 的台阶。
//
// 修法：每次尝试用 WithCancel + AfterFunc(预算) 覆盖 open 全过程（含上传），
// 拿到响应头即停表，context 转交流 Close 回收——不能用 WithTimeout，否则
// 6s 后会把已经拿到响应头、正在正常生成的长流一起掐断。

// TestStreamOpenBudgetCoversUpload 验证上传阶段卡住时，单次尝试在预算附近结束，
// 而不是等到上传自己完成。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：为「上传也受 open 预算约束」加确定性回归。
func TestStreamOpenBudgetCoversUpload(t *testing.T) {
	origAvailable := hostBridgeAvailableFn
	origDirect := hostStreamDirectFn
	origAttempts := streamOpenMaxAttempts
	origBudget := loadedHostBridgeOpenTimeout()
	t.Cleanup(func() {
		hostBridgeAvailableFn = origAvailable
		hostStreamDirectFn = origDirect
		streamOpenMaxAttempts = origAttempts
		setHostBridgeOpenTimeout(origBudget)
	})

	// 直连路径，预算压到 120ms 让用例快速结束。
	hostBridgeAvailableFn = func() bool { return false }
	streamOpenMaxAttempts = 2
	setHostBridgeOpenTimeout(120 * time.Millisecond)

	// 模拟「上传卡住」：直连 seam 在 context 取消前不返回，取消后返回 ctx 错误。
	var (
		mu      sync.Mutex
		elapsed []time.Duration
	)
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		start := time.Now()
		<-req.Context().Done()
		mu.Lock()
		elapsed = append(elapsed, time.Since(start))
		mu.Unlock()
		return nil, 0, nil, req.Context().Err()
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat",
		bytes.NewReader(bytes.Repeat([]byte("x"), 1024)))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	started := time.Now()
	stream, _, _, openErr := hostHTTPDoStream(req, "cb-upload")
	total := time.Since(started)
	if openErr == nil {
		t.Fatal("want error when every attempt stalls in upload")
	}
	if stream != nil {
		t.Fatal("want nil stream when every attempt stalls")
	}

	// 1. 两次尝试都必须被预算打断，而不是无限等待。
	mu.Lock()
	got := append([]time.Duration(nil), elapsed...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("attempts observed = %d, want 2", len(got))
	}
	for i, d := range got {
		// 给足调度余量：预算 120ms，断言上限 2s 足以区分「预算生效」与「等到上传自己完成」。
		if d > 2*time.Second {
			t.Fatalf("attempt %d blocked %v; upload must be bounded by the open budget", i+1, d)
		}
	}

	// 2. 总耗时上界 ≈ 预算 × 尝试次数（留足调度余量），证明重试环的界是真的。
	if total > 3*time.Second {
		t.Fatalf("total %v; want bounded by budget x attempts", total)
	}
}

// TestStreamOpenBudgetDoesNotKillEstablishedStream 验证拿到响应头后预算停表：
// 已建立的流即使远超预算时长也必须继续可读，不能被 open deadline 掐断。
//
// 这是 WithTimeout 方案的致命缺陷：6s 的 deadline 会一直有效，长推理流读到
// 第 7 秒就被 context 取消，表现为「正文生成中途断流」。
//
// [参数] t: 当前测试。
// [返回] 无。
// 最近修改时间：2026-11-29；改动原因：锁定「预算只约束 open，不约束正文」。
func TestStreamOpenBudgetDoesNotKillEstablishedStream(t *testing.T) {
	origAvailable := hostBridgeAvailableFn
	origDirect := hostStreamDirectFn
	origBudget := loadedHostBridgeOpenTimeout()
	t.Cleanup(func() {
		hostBridgeAvailableFn = origAvailable
		hostStreamDirectFn = origDirect
		setHostBridgeOpenTimeout(origBudget)
	})

	hostBridgeAvailableFn = func() bool { return false }
	setHostBridgeOpenTimeout(80 * time.Millisecond)

	// 立即返回响应头，但正文要等 400ms（远超预算）才可读。
	release := make(chan struct{})
	hostStreamDirectFn = func(req *http.Request, bodyBytes []byte) (*hostHTTPStream, int, http.Header, error) {
		pr, pw := io.Pipe()
		go func() {
			<-release
			_, _ = pw.Write([]byte("event: output\ndata: {\"response\":\"late-but-valid\"}\n\n"))
			_ = pw.Close()
		}()
		return &hostHTTPStream{liveBody: pr}, 200, http.Header{}, nil
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	stream, status, _, err := hostHTTPDoStream(req, "cb-established")
	if err != nil {
		t.Fatalf("open err = %v; want success", err)
	}
	if status != 200 {
		t.Fatalf("status = %d; want 200", status)
	}
	defer stream.Close()

	// 让正文晚于预算到达，然后确认仍能读到——证明 deadline 已停表。
	time.Sleep(400 * time.Millisecond)
	close(release)
	body, err := io.ReadAll(newHostStreamReader(stream))
	if err != nil {
		t.Fatalf("read established stream: %v; open budget must not kill the body", err)
	}
	if !bytes.Contains(body, []byte("late-but-valid")) {
		t.Fatalf("body = %q; want the late payload intact", body)
	}
}
