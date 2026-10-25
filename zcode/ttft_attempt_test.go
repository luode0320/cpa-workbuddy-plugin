// ttft_attempt_test.go 锁定 zcode 的非流式 TTFT 采集。
//
// zcode 的非流式路径原先恒把 TTFT 记成 0，慢速冷却因此完全没有样本可判。
// firstByteReader 负责在第一次读到字节时打点，本文件守住这条链路。
package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestFirstByteReader_RecordsFirstNonEmptyRead 守住首字节打点语义。
func TestFirstByteReader_RecordsFirstNonEmptyRead(t *testing.T) {
	// 起点回拨 50ms，避免测试执行过快导致纳秒差为零而掩盖真实打点逻辑。
	started := time.Now().Add(-50 * time.Millisecond)
	r := &firstByteReader{
		inner:   strings.NewReader("hello world"),
		started: started,
	}

	buf := make([]byte, 5)
	if _, err := r.Read(buf); err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if r.at.IsZero() {
		t.Fatal("读到非空字节后必须记录首字节时刻")
	}
	if got := r.ttftNS(); got == 0 {
		t.Fatal("首字节时延不得为 0")
	}
	// 第二次读不得覆盖首字节时刻。
	first := r.at
	time.Sleep(2 * time.Millisecond)
	if _, err := r.Read(buf); err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if !r.at.Equal(first) {
		t.Fatal("首字节时刻只能记录一次，后续读不得覆盖")
	}
}

// TestFirstByteReader_EmptyReadDoesNotMark 守住空读不算首字节。
//
// 缓冲区未就绪时的 n==0 返回不代表上游回了数据；若把它当成首字节，
// TTFT 会被系统性压低，慢速判据随之失真。
func TestFirstByteReader_EmptyReadDoesNotMark(t *testing.T) {
	r := &firstByteReader{inner: &emptyThenData{}, started: time.Now()}

	buf := make([]byte, 8)
	// 第一次空读。
	n, _ := r.Read(buf)
	if n != 0 {
		t.Fatalf("前置：第一次应为空读，实际 %d", n)
	}
	if !r.at.IsZero() {
		t.Fatal("空读不得记录首字节时刻")
	}
	// 第二次拿到数据才打点。
	if n, _ := r.Read(buf); n == 0 {
		t.Fatal("前置：第二次应有数据")
	}
	if r.at.IsZero() {
		t.Fatal("拿到数据后必须记录首字节时刻")
	}
}

// TestFirstByteReader_UnobservableReturnsZero 守住无起点时返回 0。
func TestFirstByteReader_UnobservableReturnsZero(t *testing.T) {
	r := &firstByteReader{inner: strings.NewReader("x")}
	if got := r.ttftNS(); got != 0 {
		t.Fatalf("未读数据时必须返回 0，实际 %d", got)
	}
}

// emptyThenData 第一次 Read 返回空，之后返回数据，用于模拟缓冲区未就绪。
type emptyThenData struct{ served bool }

func (e *emptyThenData) Read(p []byte) (int, error) {
	if !e.served {
		e.served = true
		return 0, nil
	}
	n := copy(p, "data")
	return n, io.EOF
}
