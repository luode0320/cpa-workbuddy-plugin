// checkin_retry_test.go 守护签到重试队列语义：幂等入队、永久失败排除、
// 成功取消、预算上限丢弃、快照排序，以及冷却顺延不消耗预算。
package main

import (
	"strings"
	"testing"
	"time"
)

func clearCheckinRetryQueue(t *testing.T) {
	t.Helper()
	checkinRetryMu.Lock()
	checkinRetryQueue = make(map[string]*checkinRetryEntry)
	checkinRetryMu.Unlock()
	t.Cleanup(func() {
		checkinRetryMu.Lock()
		checkinRetryQueue = make(map[string]*checkinRetryEntry)
		checkinRetryMu.Unlock()
	})
}

func TestCheckinRetryable(t *testing.T) {
	permanent := []string{
		"no credential",
		"No Credential",
		"凭据加载失败",
	}
	transient := []string{
		"当前参与用户太多，请稍后再试",
		"HTTP 502 Bad Gateway",
		"HTTP 0 empty response",
		"decode: unexpected end of JSON input",
		"设备已被封禁（设备级拦截，稍后重试）",
		"",
	}
	for _, m := range permanent {
		if checkinRetryable(m) {
			t.Errorf("checkinRetryable(%q) = true, want false", m)
		}
	}
	for _, m := range transient {
		if !checkinRetryable(m) {
			t.Errorf("checkinRetryable(%q) = false, want true", m)
		}
	}
}

func TestScheduleCheckinRetry_NewAndIdempotent(t *testing.T) {
	clearCheckinRetryQueue(t)
	const idx = "retry-test-idx-1"

	if got := scheduleCheckinRetry(idx, "file-a.json", "uid-1", "稍后再试"); !got {
		t.Fatal("first schedule should report a new entry")
	}
	e := checkinRetryQueue[idx]
	if e == nil {
		t.Fatal("entry not queued")
	}
	if e.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", e.Attempts)
	}
	firstNext := e.NextAt

	// Re-scheduling (concurrent fleet run) must not reset the attempt
	// counter or postpone the next run; only the message refreshes.
	if got := scheduleCheckinRetry(idx, "file-a.json", "uid-1", "仍然稍后再试"); got {
		t.Error("re-schedule should report false for an existing entry")
	}
	e = checkinRetryQueue[idx]
	if e.Attempts != 1 {
		t.Errorf("attempts after re-schedule = %d, want 1", e.Attempts)
	}
	if !e.NextAt.Equal(firstNext) {
		t.Error("next_at changed on re-schedule")
	}
	if e.LastMessage != "仍然稍后再试" {
		t.Errorf("last_message = %q, want refreshed message", e.LastMessage)
	}
}

func TestScheduleCheckinRetry_SkipsPermanentFailures(t *testing.T) {
	clearCheckinRetryQueue(t)
	if scheduleCheckinRetry("perm-1", "f.json", "uid", "no credential") {
		t.Error("no-credential failure must not be queued")
	}
	if scheduleCheckinRetry("perm-2", "f.json", "uid", "凭据加载失败") {
		t.Error("credential-load failure must not be queued")
	}
	if scheduleCheckinRetry("", "f.json", "uid", "稍后再试") {
		t.Error("empty auth_index must be rejected")
	}
	if len(checkinRetrySnapshot()) != 0 {
		t.Errorf("queue should be empty, got %d entries", len(checkinRetrySnapshot()))
	}
}

func TestCancelCheckinRetry(t *testing.T) {
	clearCheckinRetryQueue(t)
	scheduleCheckinRetry("cancel-1", "f.json", "uid", "稍后再试")
	cancelCheckinRetry("cancel-1")
	if len(checkinRetrySnapshot()) != 0 {
		t.Error("entry should be cancelled")
	}
	// Cancelling an unknown index must be a no-op, not a panic.
	cancelCheckinRetry("never-queued")
}

func TestRequeueCheckinRetry_CapDropsEntry(t *testing.T) {
	clearCheckinRetryQueue(t)
	e := &checkinRetryEntry{
		AuthIndex: "cap-1",
		AuthID:    "f.json",
		Attempts:  checkinRetryMax, // already at cap
		NextAt:    time.Now(),
	}
	requeueCheckinRetry(e, "稍后再试")
	if _, exists := checkinRetryQueue["cap-1"]; exists {
		t.Error("entry at attempt cap should be dropped, not re-queued")
	}
	// Permanent message also drops the entry even below the cap.
	e2 := &checkinRetryEntry{AuthIndex: "cap-2", Attempts: 3}
	requeueCheckinRetry(e2, "no credential")
	if _, exists := checkinRetryQueue["cap-2"]; exists {
		t.Error("permanent failure should not be re-queued")
	}
}

func TestCheckinRetrySnapshot_Order(t *testing.T) {
	clearCheckinRetryQueue(t)
	scheduleCheckinRetry("snap-b", "b.json", "uid-b", "稍后再试")
	scheduleCheckinRetry("snap-a", "a.json", "uid-a", "稍后再试")
	// Make snap-a due sooner than snap-b.
	checkinRetryMu.Lock()
	if e := checkinRetryQueue["snap-a"]; e != nil {
		e.NextAt = time.Now().Add(30 * time.Second)
	}
	checkinRetryMu.Unlock()

	snap := checkinRetrySnapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	first, _ := snap[0]["auth_index"].(string)
	if first != "snap-a" {
		t.Errorf("first snapshot entry = %q, want snap-a (earliest next_at)", first)
	}
	if !strings.Contains(snap[0]["next_at"].(string), "T") {
		t.Errorf("next_at should be RFC3339, got %v", snap[0]["next_at"])
	}
}

// TestCheckinAccount_CooldownResult 锁定冷却跳过路径会在结果中带回剩余冷却时长，
// 供重试队列识别为「未打上游」并顺延，而不是消耗一次 attempt。
func TestCheckinAccount_CooldownResult(t *testing.T) {
	a := &traeAuth{Token: "cooldown-token", UserID: "uid-cooldown-result"}
	markCheckinCooldown(a)
	t.Cleanup(func() { checkinCooldownUntil.Delete(checkinCooldownKey(a)) })

	res := checkinAccount(a)
	if res.OK {
		t.Fatal("cooldown attempt must not report success")
	}
	if res.Cooldown <= 0 {
		t.Fatalf("cooldown = %v, want > 0", res.Cooldown)
	}
}

// TestRequeueCheckinRetryWaiting_KeepsBudget 验证冷却顺延不递增 attempt，
// 并把下次执行推迟到冷却结束之后，让重试预算留给真实打上游的尝试。
func TestRequeueCheckinRetryWaiting_KeepsBudget(t *testing.T) {
	clearCheckinRetryQueue(t)
	const idx = "wait-budget-1"
	e := &checkinRetryEntry{
		AuthIndex: idx,
		AuthID:    "f.json",
		Attempts:  5,
		NextAt:    time.Now(),
	}
	wait := 55 * time.Minute
	requeueCheckinRetryWaiting(e, "限流冷却中，约 55 分钟后重试", wait)

	got := checkinRetryQueue[idx]
	if got == nil {
		t.Fatal("waiting entry not re-queued")
	}
	if got.Attempts != 5 {
		t.Errorf("attempts = %d, want 5 (cooldown must not consume budget)", got.Attempts)
	}
	if !got.NextAt.After(time.Now().Add(wait)) {
		t.Errorf("next_at = %v, want after now+%v", got.NextAt, wait)
	}
	if !strings.Contains(got.LastMessage, "冷却") {
		t.Errorf("last_message = %q, want cooldown message", got.LastMessage)
	}
}

// TestRequeueCheckinRetryWaiting_AtCapStillQueued 验证即使 attempt 已达上限，
// 冷却顺延仍保留条目（预算未消耗），避免满预算账号当天彻底失去补签机会。
func TestRequeueCheckinRetryWaiting_AtCapStillQueued(t *testing.T) {
	clearCheckinRetryQueue(t)
	const idx = "wait-cap-1"
	e := &checkinRetryEntry{
		AuthIndex: idx,
		Attempts:  checkinRetryMax,
		NextAt:    time.Now(),
	}
	requeueCheckinRetryWaiting(e, "限流冷却中，约 12 分钟后重试", 12*time.Minute)
	if got := checkinRetryQueue[idx]; got == nil {
		t.Fatal("cooling entry at attempt cap should still be queued")
	}
}

// TestNewSlotResetsRetryBudget 验证每轮定时取消遗留条目后重新入队会得到完整预算，
// 这是 runFleetCheckin 在新一轮调用 cancelCheckinRetry 后的可观测契约。
func TestNewSlotResetsRetryBudget(t *testing.T) {
	clearCheckinRetryQueue(t)
	const idx = "slot-reset-1"
	if !scheduleCheckinRetry(idx, "f.json", "uid-1", "稍后再试") {
		t.Fatal("first schedule should add a new entry")
	}
	checkinRetryQueue[idx].Attempts = checkinRetryMax - 1 // 模拟上一槽位遗留的高预算条目
	cancelCheckinRetry(idx)
	if len(checkinRetrySnapshot()) != 0 {
		t.Fatal("stale entry should be cancelled at new slot")
	}
	if !scheduleCheckinRetry(idx, "f.json", "uid-1", "稍后再试") {
		t.Fatal("re-schedule after cancel should add a new entry")
	}
	if got := checkinRetryQueue[idx].Attempts; got != 1 {
		t.Errorf("attempts after slot reset = %d, want 1", got)
	}
}
