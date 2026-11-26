package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// TestRenewBackoffDelayCaps 校验退避随时长增长并封顶 24 小时。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，退避边界验证。
func TestRenewBackoffDelayCaps(t *testing.T) {
	if got := renewBackoffDelay(0); got != 0 {
		t.Fatalf("零失败应无退避，实际 %v", got)
	}
	if got := renewBackoffDelay(1); got != time.Hour {
		t.Fatalf("一次失败应退避 1 小时，实际 %v", got)
	}
	if got := renewBackoffDelay(30); got != renewMaxBackoff {
		t.Fatalf("多次失败应封顶 24 小时，实际 %v", got)
	}
}

// TestRunRenewOnceSkipsBackedOffAndDisabled 校验退避与停用账号被跳过。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，单轮调度过滤验证。
func TestRunRenewOnceSkipsBackedOffAndDisabled(t *testing.T) {
	renewBackoffMu.Lock()
	renewBackoff = map[string]*renewBackoffState{}
	renewBackoffMu.Unlock()

	var mu sync.Mutex
	renewed := map[string]int{}
	deps := renewDeps{
		listAccounts: func() ([]*StoredAuth, error) {
			return []*StoredAuth{
				{AuthID: "healthy", CookieHeader: "sessionid=h"},
				{AuthID: "backed", CookieHeader: "sessionid=b"},
				{AuthID: "disabled", CookieHeader: "sessionid=d", Disabled: true},
			}, nil
		},
		renew: func(sa *StoredAuth) (*renewResult, error) {
			mu.Lock()
			renewed[sa.AuthID]++
			mu.Unlock()
			return &renewResult{Renewed: true, CookieHeader: sa.CookieHeader}, nil
		},
		apply: func(sa *StoredAuth, res *renewResult, digest string) error { return nil },
	}
	renewBackoffHit("backed", "失败")
	runRenewOnce(context.Background(), deps)

	mu.Lock()
	defer mu.Unlock()
	if renewed["healthy"] != 1 {
		t.Fatalf("健康账号应续期 1 次，实际 %d", renewed["healthy"])
	}
	if renewed["backed"] != 0 {
		t.Fatalf("退避账号应跳过，实际 %d", renewed["backed"])
	}
	if renewed["disabled"] != 0 {
		t.Fatalf("停用账号应跳过，实际 %d", renewed["disabled"])
	}
}

// TestRunRenewOnceBackoffOnFailure 校验续期失败账号进入退避。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，失败退避验证。
func TestRunRenewOnceBackoffOnFailure(t *testing.T) {
	renewBackoffMu.Lock()
	renewBackoff = map[string]*renewBackoffState{}
	renewBackoffMu.Unlock()

	deps := renewDeps{
		listAccounts: func() ([]*StoredAuth, error) {
			return []*StoredAuth{{AuthID: "fail", CookieHeader: "sessionid=f"}}, nil
		},
		renew: func(sa *StoredAuth) (*renewResult, error) {
			return nil, errors.New("上游超时")
		},
		apply: func(sa *StoredAuth, res *renewResult, digest string) error { return nil },
	}
	runRenewOnce(context.Background(), deps)
	if renewNextAt(&StoredAuth{AuthID: "fail"}).IsZero() {
		t.Fatal("失败账号应设置退避时间")
	}
}

// TestRenewOneAccountMarksInvalidOnSessionExpired 校验会话失效错误落到账号标记。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，报错分支也要标失效，面板才如实显示。
func TestRenewOneAccountMarksInvalidOnSessionExpired(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/doubao-invalid.json"
	sa := &StoredAuth{AuthID: "doubao-invalid", CookieHeader: "sessionid=x", PhysicalPath: path}
	if errWrite := writeAuthFileDirect(path, sa); errWrite != nil {
		t.Fatalf("准备账号文件失败：%v", errWrite)
	}
	deps := renewDeps{
		renew: func(sa *StoredAuth) (*renewResult, error) {
			return nil, errors.New("续期失败（第一步）：会话过期，请重新登录")
		},
		apply: func(sa *StoredAuth, res *renewResult, digest string) error { return nil },
	}
	renewOneAccount(context.Background(), sa, deps)
	if !sa.AuthInvalid {
		t.Fatal("会话失效错误应标记 AuthInvalid")
	}
	back, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("读回账号文件失败：%v", errRead)
	}
	var stored StoredAuth
	if errParse := json.Unmarshal(back, &stored); errParse != nil {
		t.Fatalf("解析账号文件失败：%v", errParse)
	}
	if !stored.AuthInvalid {
		t.Fatalf("失效标记应落盘，实际 %s", string(back))
	}
}

// TestStopRenewLoopEndsGoroutine 校验停止后协程确实退出。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-27；改动原因：新增，定时器生命周期验证。
func TestStopRenewLoopEndsGoroutine(t *testing.T) {
	startRenewLoop(time.Hour)
	stopRenewLoop()
	activeRenew.mu.Lock()
	cancel := activeRenew.cancel
	activeRenew.mu.Unlock()
	if cancel != nil {
		t.Fatal("停止后不应残留定时器句柄")
	}
}
