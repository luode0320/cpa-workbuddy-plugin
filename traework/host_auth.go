// host_auth.go wraps the host's auth-store RPC (host.auth.list / get /
// get_bundle). These are the only paths the plugin uses to read auth files;
// writes go through hostAuthPersist / hostAuthSaveJSON in authfile.go.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// rpcHostAuthListResponse mirrors the host's host.auth.list envelope result.
type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
}

// hostAuthList returns all traework credentials known to the host.
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list: bad envelope")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	// Filter by filename prefix, NOT by Type/Provider: many existing auth
	// files on disk don't carry a "type"/"provider" field, and the prefix is
	// the only reliable cross-version discriminator.
	out := make([]pluginapi.HostAuthFileEntry, 0, len(resp.Files))
	// Disk files are named traework-<uid>.json (authFilePrefix), NOT
	// providerName+"-" — filtering by "traework-provider-" hid every
	// imported credential from the panel (accounts always empty).
	prefix := authFilePrefix
	for _, f := range resp.Files {
		if strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hostAuthGet fetches the parsed credential for one auth index.
func hostAuthGet(authIndex string) (*traeAuth, error) {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return nil, err
	}
	return parseTraeAuth(phys.JSON)
}

// hostAuthGetBundle is one host.auth.get for both the parsed credential and
// the physical metadata (avoids a double RPC in the dashboard).
//
// 走 hostAuthGetPhysicalFn 而不是直接调 hostAuthGetPhysical：面板行构建是唯一
// 需要「同一份假宿主同时提供列表与物理文档」的链路，没有注入点就没有任何
// 单测能覆盖行字段（见 panel_rotation_badge_test.go）。
func hostAuthGetBundle(authIndex string) (*traeAuth, *hostAuthPhysical, error) {
	phys, err := hostAuthGetPhysicalFn(authIndex)
	if err != nil {
		return nil, nil, err
	}
	a, err := parseTraeAuth(phys.JSON)
	if err != nil {
		return nil, phys, err
	}
	return a, phys, nil
}

// waitAuthDisabledState polls host.auth.list until the account's in-memory
// disabled state matches want. The host applies direct file writes through its
// async file watcher, so a toggle must confirm the new state before the panel
// reloads — otherwise the dashboard reads the stale pre-write memory state.
//
// 禁用的确认尤其重要：写盘成功但宿主没吸收 = 「写了就算禁了」的静默失效，
// 请求仍会被路由到这个已知坏号。
//
// [参数] authIndex：宿主 auth_index；want：期望的 disabled 状态；timeout：最长等待。
// [返回] true 表示宿主内存状态已与期望一致。
// 最近修改时间：2026-10-22；改动原因：新增，供禁用探活机制确认写盘已被宿主吸收。
func waitAuthDisabledState(authIndex string, want bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		files, err := hostAuthList()
		if err == nil {
			for _, f := range files {
				if f.AuthIndex != authIndex {
					continue
				}
				if f.Disabled == want {
					return true
				}
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
