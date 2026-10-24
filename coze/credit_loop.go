package main

import (
	"sync"
	"time"
)

// 本文件保留积分巡检的共享信号与缓存写入内核。
//
// 循环本体已由 credit_sweep.go 的 1 小时全量巡检接管（2026-10-25）：
// 签到、落盘与分级都在那一轮里完成，本文件不再有自己的 tick 循环，
// 只保留 reconfigure 重排信号、停止信号与「写内存缓存」这一个纯函数。

var (
	// creditTickCh 合并外部触发（reconfigure）到同一轮，缓冲 1。
	creditTickCh = make(chan struct{}, 1)

	// creditStopCh 在插件 shutdown 时关闭，用于立即唤醒巡检循环。
	//
	// 只关闭不写入：向已关闭 channel 发送会 panic，因此 requestCreditTick
	// 始终只写 creditTickCh，两者互不干扰。
	creditStopCh   = make(chan struct{})
	creditStopOnce sync.Once

	// creditStartOnce 保证巡检循环只启动一次。
	creditStartOnce sync.Once
)

// storeCreditSummary 把积分摘要写入缓存。
// [参数] sa: 账号；summary: 积分摘要。
// [返回] 无。
// 最近修改时间：2026-10-25；改动原因：键归一化，与闸门读取侧保持一致。
func storeCreditSummary(sa *StoredAuth, summary *creditSummary) {
	if sa == nil || summary == nil {
		return
	}
	key := creditSnapshotKey(sa.AuthID)
	if key == "" {
		key = creditSnapshotKey(sa.SecUserID)
	}
	if key == "" {
		return
	}
	creditsCache.mu.Lock()
	creditsCache.entries[key] = creditCacheEntry{summary: *summary, fetched: time.Now()}
	creditsCache.mu.Unlock()
}
