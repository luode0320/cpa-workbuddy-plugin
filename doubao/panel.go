package main

import (
	_ "embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed panel.html
var panelHTML []byte

// handlePanelPage 返回内嵌的管理面板页面（已包装为标准 envelope）。
// [参数] 无。
// [返回] 管理响应信封。
// 最近修改时间：2026-10-17；改动原因：新增，面板页出口。
func handlePanelPage() []byte {
	resp := pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
		},
		Body: panelHTML,
	}
	data, _ := okEnvelope(resp)
	return data
}
