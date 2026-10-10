package main

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed panel.html
var panelHTML []byte

func handlePanelPage() []byte {
	resp := pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-cache, no-store, must-revalidate"},
		},
		Body: panelHTML,
	}
	data, _ := jsonMarshal(resp)
	return data
}

// jsonMarshal 简单封装
func jsonMarshal(v any) ([]byte, error) {
	return okEnvelope(v)
}

func isPanelPath(path string) bool {
	p := strings.TrimRight(path, "/")
	return p == "" || p == "/panel" || p == "/status" || p == "/v0/resource/plugins/"+zcodeProviderID+"/panel"
}
