# QoderWork CPA 插件

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 QoderWork（qoder.com.cn，CN 区）Provider 插件：多账号 OAuth 登录与凭据 JSON 导入、动态模型、COSY 签名推理、每日签到、积分面板、token 自动保活。

## 功能

| 能力 | 说明 |
|---|---|
| **双登录方式** | ① OAuth 设备授权（PKCE，浏览器授权，dt- 30 天 + drt- 1 年自动旋转）② 凭据 JSON 导入（离线回灌完整 auth 文件）——两家族可共存于同一 auth 文件 |
| **登录自动领包** | OAuth 登录成功后自动判断并领取一次性 Pro 升级包（eligibility → claim） |
| **COSY 推理** | RSA 包 AES 会话密钥 + MD5 请求签名，对接 gateway.qoder.com.cn SSE 流式 |
| **动态模型** | COSY 拉取 `/algo/api/v2/model/list`（chat scene），10 静态模型兜底 |
| **每日签到** | 面板手动签到（单账号/批量）+ 每 4 小时定时自动签到（00/04/08/12/16/20），签到后返回最新积分快照 |
| **积分面板** | 账号卡片：昵称/积分/计划/签到状态/操作（签到/刷新/选用） |
| **token 保活** | 22:00 定时刷新；按 token 前缀路由（drt- → deviceToken/refresh，jrt- → jobToken/refresh），PAT 永不劫持 OAuth 刷新 |
| **auth 隔离** | 文件名前缀 `qoderwork-` 过滤，与 workbuddy 等其他插件互不干扰 |

## 安装

### 从 Release（推荐）

```bash
# 按你的平台下载（示例 linux/arm64）
unzip qoderwork_0.2.6_linux_arm64.zip
cp qoderwork.so /path/to/cliproxyapi/plugins/qoderwork.so
```

### 从源码

```bash
cd qoderwork
CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "-X main.version=0.2.6" -o qoderwork.so .
cp qoderwork.so /path/to/cliproxyapi/plugins/
```

### config.yaml

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    qoderwork:
      enabled: true

      # Per-request account-failover budget for 40x errors (default 10,
      # range 0-10). When a request hits an account-level 40x (401/403/
      # 404/405), the plugin retries the SAME request on a different
      # qoderwork account up to `retry_on_4xx` times before giving up.
      # Set to 0 to disable on-request account rotation (use as a kill
      # switch during global outage recovery). The failing account is
      # also recorded in the cooldown list so subsequent requests skip
      # it.
      retry_on_4xx: 10

# 模型别名（可选）
oauth-model-alias:
  qoderwork:
    - name: qmodel_preview
      alias: qoder/qwen3.8-max
    - name: qmodel_latest
      alias: qoder/qwen3.7-max
```

## 使用

### 方式一：OAuth 登录（推荐）

1. CPA 管理面板 → Auth 文件 → QoderWork OAuth 登录卡片
2. 浏览器打开授权链接 → 登录 qoder.com.cn（阿里云 SSO）→ 点 Continue 授权
3. 插件自动轮询拿 token 落盘（dt-/drt-），并自动领取 Pro 升级包（若 eligible）

### 方式二：凭据导入（JSON，支持批量）

1. 面板（`/v0/resource/plugins/qoderwork/panel`）→ 右上角「导入 QoderWork 凭证」
2. 粘贴或选择凭据 JSON，逐条串行离线回灌，落盘为 `qoderwork-<uid>.json`

支持的输入形态（同一份输入里混排也认）：

- **单条凭据**：嵌套 `{"auth":{...},"account":{...}}` 或平铺 `{"accessToken":...,"uid":...}`
- **凭据数组**：`[{...},{...}]`
- **导出备份**：`{"accounts":[{"credential":{...}}]}`（即面板「导出备份」产物）
- **JSONL**：每行一个 JSON 对象
- **多文件**：一次选择多个 `.json` / `.jsonl` / `.txt`（单文件 ≤ 2MB），选中即展开为逐条待导入项

统一走 `POST /import-cred`（离线回灌，不打上游换 token）；逐条串行、单项失败不中断，
失败明细常驻弹窗可原地重试，已成功项不会重复写入。

### 面板

`/v0/resource/plugins/qoderwork/panel`（需 management key）

- 账号卡片：积分余额 / 计划 / 签到按钮（签到后返回最新积分）
- 「全部签到」批量签到；自动签到开关（00/04/08/12/16/20）
- 「导入 QoderWork 凭证」：粘贴或选择凭据 JSON 一键导入（见方式二）
- 签到按钮显示「未下发」= 上游按账号下发了活动框架但没有 `CLAIM_BENEFIT` 条目（部分账号本来就没有签到活动），不是插件失败；「不可签」= 该账号不参与活动
- 签到排查看日志：`docker logs cli-proxy-api | grep '\[qoderwork\] checkin'`，可见 `ok credit=+N` / `skip reason=already` / `skip reason=no_campaign` / 失败原因
- 上游 campaigns **不提供连续签到天数**，面板不再显示「连续 N 天」（此前为编造值）

## 失败处置

- **写盘禁用门限**（`failover_disable_min_count`，默认 3）：瞬时失败（5xx / 传输层错误 / 软限流文案 / 零字节断流）先只走内存阶梯冷却（60s 起、10 分钟封顶，任意一次成功即清零），连续达到门限才把账号写盘禁用；确定性失败（401/403/404/405 凭据问题、402 额度耗尽、110 日请求上限）首次即禁用。填 1 恢复「首次失败即禁用」的旧行为。
- **永不罚号**：排队帧（`isQueued` / `queueCount` / 业务码 10605）与模型级故障（`all backends failed` 等）——它们与调用账号无关，记账只会把健康号逐个摘光。
- **恢复路径**：被禁用账号由 10 分钟探活回放「最近一次失败模型」验证，探通即解禁；冷却到期不等于解禁。

## 凭证家族与刷新

```
auth 文件字段（可共存）：
  accessToken:   dt- (OAuth, ~30d) 或 jt- (PAT 交换, 24h)
  refreshToken:  drt- (~1y, 旋转)   或 jrt- (48h)
  personalToken: pt- (长期兜底，导入即永久)
```

- 刷新路由按 `refreshToken` 前缀：`drt-` → `deviceToken/refresh`；`jrt-` → `jobToken/refresh` → 失败 fallback PAT re-exchange
- `personalToken` 永不主动覆盖活跃 token，只做最终兜底
- host 15 分钟 auto-refresh + 插件 22:00 keepalive 均按此规则

## 模型

10 个静态模型（`qmodel_preview` 等）+ COSY 动态拉取。CPA 侧别名示例：`qoder/qwen3.8-max` → `qmodel_preview`。

## 参考文档

- [KNOWLEDGE.md](../KNOWLEDGE.md) — QoderWork API 逆向全记录（COSY 签名/编码/端点）
- [analysis/api-endpoints-scan.md](../analysis/api-endpoints-scan.md) — 客户端全端点扫描
- [analysis/qoderwork-real-oauth.md](../analysis/qoderwork-real-oauth.md) — OAuth 设备授权流程 + 本地测试证据

## License

MIT（based on workbuddy by lovingfish，见 [LICENSE](LICENSE)）
