// host_auth.go wraps the host's auth-store RPC (host.auth.list / get /
// get_bundle). These are the only paths the plugin uses to read auth files;
// writes go through hostAuthPersist / hostAuthPersistMigrate in lifecycle.go.
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

// hostAuthList returns all qoderwork credentials known to the host.
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
	// Fresh slice — resp.Files[:0] would alias the RPC response's backing
	// array (P1-3: fragile pattern, safe today but could break if resp is
	// ever cached/reused).
	//
	// Filter by filename prefix, NOT by Type/Provider: many existing auth
	// files on disk don't carry a "type"/"provider" field (they were written
	// before that convention), and EqualFold("", providerName) returns false
	// for them — meaning we'd incorrectly include files that have the
	// workbuddy- prefix but no type field. Filename prefix is the only
	// reliable cross-version discriminator.
	out := make([]pluginapi.HostAuthFileEntry, 0, len(resp.Files))
	// Disk files are named qoderwork-<uid>.json (authFilePrefix), NOT
	// providerName+"-" — filtering by "qoderwork-provider-" hides every
	// credential from the panel (accounts always empty).
	prefix := authFilePrefix
	for _, f := range resp.Files {
		if strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hostAuthGet fetches the credential JSON for one auth index.
func hostAuthGet(authIndex string) (*storedAuth, error) {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return nil, err
	}
	return parseStored(phys.JSON)
}

// hostAuthGetBundle is one host.auth.get for both storage and physical metadata
// (avoids the previous double-RPC in dashboard: get + getPhysical).
func hostAuthGetBundle(authIndex string) (*storedAuth, *hostAuthPhysical, error) {
	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return nil, nil, err
	}
	sa, err := parseStored(phys.JSON)
	if err != nil {
		return nil, phys, err
	}
	return sa, phys, nil
}

// waitAuthDisabledState polls host.auth.list until the account's in-memory
// disabled state matches want. The host applies direct file writes through its
// async file watcher, so a toggle must confirm the new state before anything
// reads it back — otherwise the caller sees the stale pre-write memory state.
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

// hostAuthListFn / hostAuthGetPhysicalFn 是 zip 导出链路的包级测试接缝：
// 生产指向真实 host RPC，单测替换为内存桩，避免为可测性改动既有调用方。
var hostAuthListFn = hostAuthList
var hostAuthGetPhysicalFn = hostAuthGetPhysical
