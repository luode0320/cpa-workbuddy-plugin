package main

import (
	"embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed panel.html
var panelAssets embed.FS

// handlePanelPage 返回内嵌的管理面板页面。
// [参数] 无。
// [返回] 已包装为标准 envelope 的管理响应字节。
// 最近修改时间：2026-11-28 14:40:00；改动原因：修正为 ManagementResponse 结构，修复面板白屏。
func handlePanelPage() []byte {
	body, err := panelAssets.ReadFile("panel.html")
	if err != nil {
		body = []byte("<html><body>panel asset missing</body></html>")
	}
	resp := pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
		},
		Body: body,
	}
	data, _ := okEnvelope(resp)
	return data
}
