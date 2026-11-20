package cpa_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// expectedPluginVersion 读取插件根目录 VERSION 文件的版本号，供发布相关断言比对。
//
// 版本号只允许存在一个真相源（VERSION 文件 + CI 的 -X main.version 注入），
// 所以测试不能在断言里硬编码版本字面量，否则每次发版都要改测试；这里按
// 包目录相对路径回读 VERSION，与 CI 解析 VERSION 的口径保持一致。
//
// [参数] t：测试上下文，用于在读取失败时直接终止当前用例。
// [返回] 去掉首尾空白的版本号字符串，例如 "1.0.10"。
// 最近修改时间：2026-11-21；改动原因：发版 1.0.10 时把硬编码版本断言改成回读 VERSION。
func expectedPluginVersion(t *testing.T) string {
	t.Helper()
	versionPath := filepath.Join("..", "..", "VERSION")
	raw, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatalf("read VERSION at %s: %v", versionPath, err)
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		t.Fatalf("VERSION at %s is empty", versionPath)
	}
	return version
}
