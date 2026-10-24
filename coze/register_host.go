package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 本文件实现「把已落盘的账号补注册到宿主」。
//
// 为什么需要它（2026-10-24 生产实证）：
// 插件把凭据写在自己的目录 `~/.antigravity_cockpit/coze_accounts/`，
// 但宿主只认自己的 spool（`mysqlstore/mysqlstore/auths/`）。
// 两者的同步点只有一处——`writeAuthFileDirect` 里的
// `if auth.PhysicalPath == ""` 分支，即「首次授权、尚未关联物理路径时」
// 才调 `host.auth.save`。
//
// 于是历史账号会出现这个死角：凭据文件在插件目录里、插件 `/accounts`
// 能读到并正常刷新积分，但宿主 spool 里没有对应条目，
// `GET /v1/models` 仍能列出模型（来自插件 model.static），
// 而真实请求必然 `auth_not_found: no auth available`。
//
// 生产 9 个账号全部处于该状态：能领积分、不能承载流量。
// 本文件提供显式补注册入口，把已有凭据按宿主命名规则重新推一遍。

// reRegisterResult 是单账号补注册结果。
type reRegisterResult struct {
	AuthID string `json:"auth_id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// reRegisterAccountsToHost 把全部本地账号补注册到宿主。
//
// 只处理「插件能读到、且未被禁用」的账号：禁用账号推给宿主会立刻
// 参与路由，与用户意图相反；无法读取的坏文件推上去只会让宿主报解析错。
// [参数] 无。
// [返回] 每个账号的结果与成功数。
func reRegisterAccountsToHost() ([]reRegisterResult, int) {
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return []reRegisterResult{{AuthID: "", OK: false, Error: errList.Error()}}, 0
	}
	results := make([]reRegisterResult, 0, len(accounts))
	success := 0
	for _, sa := range accounts {
		if sa == nil {
			continue
		}
		item := reRegisterResult{AuthID: sa.AuthID}
		if sa.Disabled {
			item.Error = "账号已禁用，跳过注册"
			results = append(results, item)
			continue
		}
		if errSave := reRegisterOneToHost(sa); errSave != nil {
			item.Error = errSave.Error()
			results = append(results, item)
			continue
		}
		item.OK = true
		success++
		results = append(results, item)
	}
	return results, success
}

// reRegisterOneToHost 把单个账号按宿主命名规则推送到宿主 spool。
//
// 文件名固定为 `<auth_id>.json`：宿主以文件名派生账号标识，
// 用别的名字会产生第二条记录，与插件侧账号对不上。
// 内容必须是当前落盘版本（含最新 Cookie），否则推上去的是一个过期快照。
// [参数] sa: 账号。
// [返回] 注册错误。
func reRegisterOneToHost(sa *StoredAuth) error {
	if sa == nil {
		return fmt.Errorf("账号为空")
	}
	authID := strings.TrimSpace(sa.AuthID)
	if authID == "" {
		return fmt.Errorf("账号标识为空")
	}
	if !isCozeAuthFileName(authID + ".json") {
		return fmt.Errorf("账号标识不符合扣子命名空间：%s", authID)
	}
	// 重新读取磁盘内容而不是用内存结构：注册要的是「当前生效凭据」，
	// 内存里可能持有续期前的旧 Cookie。
	var raw []byte
	if path := strings.TrimSpace(sa.PhysicalPath); path != "" && isSafeCozeAuthPath(path) {
		onDisk, errRead := readAuthFileRaw(path)
		if errRead != nil {
			return errRead
		}
		raw = onDisk
	} else {
		encoded, errMarshal := json.MarshalIndent(sa, "", "  ")
		if errMarshal != nil {
			return fmt.Errorf("序列化凭据失败：%w", errMarshal)
		}
		raw = encoded
	}
	return hostAuthSaveJSON(authID+".json", raw)
}

// handleReRegister 处理补注册请求。
// [参数] 无。
// [返回] 管理响应。
// 最近修改时间：2026-10-24；改动原因：新增，历史账号补注册入口。
func handleReRegister() ([]byte, error) {
	results, success := reRegisterAccountsToHost()
	// 逐账号留痕：补注册是低频人工动作，成败必须能从 `docker logs` 直接看到，
	// 否则面板只显示一个汇总数字，失败原因无从追查。
	for _, item := range results {
		fields := map[string]any{"auth_id": item.AuthID, "ok": item.OK}
		if item.Error != "" {
			fields["error"] = item.Error
			hostLogf("warn", "账号补注册宿主失败", fields)
			continue
		}
		hostLogf("info", "账号补注册宿主成功", fields)
	}
	payload, _ := json.Marshal(map[string]any{
		"ok":       success > 0,
		"total":    len(results),
		"success":  success,
		"failed":   len(results) - success,
		"accounts": results,
	})
	return makeHTTPResponse(http.StatusOK, payload)
}
