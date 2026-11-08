package main

import (
	"testing"
	"time"
)

// 本文件锁定「退役实例跑巡检也不得写盘」这条防线。
//
// 事故背景（2026-11-08 生产实证）：宿主热重载后旧实例的巡检 goroutine
// 继续存活，用旧红线 200 把新实例刚解禁的 23 个账号整批重新禁用。
// 这层防线必须在「巡检执行体」里生效，而不只是判定函数里。

// TestRunCreditSweepSkipsWriteForRetiredInstance 验证退役实例整轮不写盘。
//
// 断言三件事：分级一次都没被调用、快照没落盘、闸门快照没发布。
// 但积分仍然刷新进了缓存——读与缓存无副作用，只有写盘会造成互相覆盖。
//
// 变异验证：把 allowWrite 判定去掉后，本用例必须 FAIL。
//
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-08；改动原因：新增，退役实例写盘防护。
func TestRunCreditSweepSkipsWriteForRetiredInstance(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	sa := &StoredAuth{AuthID: "coze-retired", CookieHeader: "sessionid=x"}
	graded := 0
	persisted := 0
	published := 0

	deps := creditSweepDeps{
		list:    func() ([]*StoredAuth, error) { return []*StoredAuth{sa}, nil },
		fetch:   func(sa *StoredAuth) (*creditSummary, error) { return &creditSummary{Total: 1000, Remain: 5}, nil },
		store:   storeCreditSummary,
		record:  func(string, *creditSummary, string) {},
		persist: func() error { persisted++; return nil },
		publish: func([]*StoredAuth) { published++ },
		logf:    func(string, string, map[string]any) {},
		now:     time.Now,
		grade: func(sa *StoredAuth, cr *creditSummary) creditTier {
			graded++
			// 5 分低于红线 10：活跃实例会在这里写盘禁用。
			return creditTierDisabled
		},
		canWrite: func(func(string, string, map[string]any)) bool { return false },
	}

	report := runCreditSweepWith(deps)
	if report == nil {
		t.Fatal("退役实例也应产出巡检报告")
	}
	if graded != 0 {
		t.Fatalf("退役实例不得执行分级写盘，实际调用 %d 次", graded)
	}
	if persisted != 0 {
		t.Fatalf("退役实例不得落盘积分快照，实际落盘 %d 次", persisted)
	}
	if published != 0 {
		t.Fatalf("退役实例不得发布闸门快照，实际发布 %d 次", published)
	}
	if report.Disabled != 0 || report.ReEnabled != 0 {
		t.Fatalf("退役实例不得产生禁用或解禁统计，实际 %+v", report)
	}
	// 积分仍应刷新进缓存：读与缓存没有副作用。
	if remain, known := cachedCreditRemain("coze-retired"); !known || remain != 5 {
		t.Fatalf("退役实例仍应刷新积分缓存，实际 remain=%d known=%v", remain, known)
	}
}

// TestRunCreditSweepWritesForActiveInstance 验证活跃实例照常写盘。
//
// 这是上一条的反向边界：防线不能把正常实例也拦掉。
//
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-08；改动原因：新增，活跃实例反向边界。
func TestRunCreditSweepWritesForActiveInstance(t *testing.T) {
	t.Cleanup(resetCreditCache)
	resetCreditCache()

	sa := &StoredAuth{AuthID: "coze-active", CookieHeader: "sessionid=x"}
	graded := 0
	persisted := 0
	published := 0

	deps := creditSweepDeps{
		list:    func() ([]*StoredAuth, error) { return []*StoredAuth{sa}, nil },
		fetch:   func(sa *StoredAuth) (*creditSummary, error) { return &creditSummary{Total: 1000, Remain: 5}, nil },
		store:   storeCreditSummary,
		record:  func(string, *creditSummary, string) {},
		persist: func() error { persisted++; return nil },
		publish: func([]*StoredAuth) { published++ },
		logf:    func(string, string, map[string]any) {},
		now:     time.Now,
		grade: func(sa *StoredAuth, cr *creditSummary) creditTier {
			graded++
			return creditTierDisabled
		},
		canWrite: func(func(string, string, map[string]any)) bool { return true },
	}

	runCreditSweepWith(deps)
	if graded != 1 {
		t.Fatalf("活跃实例必须执行分级写盘，实际调用 %d 次", graded)
	}
	if persisted != 1 {
		t.Fatalf("活跃实例必须落盘积分快照，实际落盘 %d 次", persisted)
	}
	if published != 1 {
		t.Fatalf("活跃实例必须发布闸门快照，实际发布 %d 次", published)
	}
}
