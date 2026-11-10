// conn_pool_test.go 锁定连接池容量足以覆盖账号池并发。
//
// 生产背景（2026-11-10）：traework 有 28 个可用账号，每个在同一上游 host 上
// 独立承流。原配置 MaxIdleConnsPerHost=5 导致 28 并发时频繁新建连接
// （TCP + TLS 握手各一次），高并发窗口慢请求占比达 26.5%（并发 1-5 时仅 9.9%）。
package main

import (
	"net/http"
	"testing"
)

// TestSharedHTTPClientConnPoolCoversPool 验证连接池容量覆盖账号池规模。
// [参数] t：测试上下文
// [返回] 无
// 最近修改时间：2026-11-10 20:55:00；改动原因：新增，锁定连接池扩容。
func TestSharedHTTPClientConnPoolCoversPool(t *testing.T) {
	client := sharedHTTPClient()
	if client == nil || client.Transport == nil {
		t.Fatal("sharedHTTPClient 必须有 Transport")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型不符: %T", client.Transport)
	}
	// 生产账号池 28 个，每个账号独立承流：每 host 空闲连接必须覆盖它。
	const poolSize = 28
	if tr.MaxIdleConnsPerHost < poolSize {
		t.Fatalf("MaxIdleConnsPerHost=%d 小于账号池 %d，高并发会反复建连",
			tr.MaxIdleConnsPerHost, poolSize)
	}
	if tr.MaxIdleConns < poolSize {
		t.Fatalf("MaxIdleConns=%d 小于账号池 %d", tr.MaxIdleConns, poolSize)
	}
}
