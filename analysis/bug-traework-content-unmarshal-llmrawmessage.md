---
schema_version: 1
doc_id: BUG-TRAE-CONTENT-AND-MODEL
doc_type: bug
source_ids: [USER-TRAE-MODEL-ERROR]
status: accepted
version: v1.0
current_slice: Trae content、host 与模型参数故障链
updated_at: 2026-08-30 02:55:11
reader_level: business_general
writing_style: plain_chinese
appendix_policy: preserve_existing_or_one_terminal_appendix
---

# Bug 主文档：traework 切换 qwen3.8-max 报 4001 content 反序列化失败

结论：原始 content 类型错误和对话 host 错误均已在生产修复，后续 Seed 参数错误已定位为短 ID 不等于精确 `config_name`。影响：Trae 账号在选择错误模型 ID 时会收到参数无效，动态模型发现可避免继续暴露失真的手工 ID。范围：消息格式、对话域名、发布错装、模型参数和账号级模型发现。非范围：本轮动态模型代码尚未发布，不代表生产已具备自动模型列表。变化：账号级模型能力改为读取上游真实模型目录，失败时保留配置或内置列表。完成标准：本地 build、vet、test 及异常回退均通过，发布后再做生产对话回归。术语说明：`config_name` 是 Trae 对话接口接受的精确模型标识。验证状态：历史 0.1.12/0.1.13 修复已生产验证；动态模型功能本地验证通过，因本轮未发布而未执行生产验证。

## 文档信息

- 来源：用户对 `qwen3.8-max` 与 `seed-2.1-pro` 的连续真实报错。
- 当前状态：动态模型发现已完成本地实现和验证，尚未提交或发布。
- 图片资产决策：N/A。原因：本故障以请求报文、状态码、哈希和代码测试为证据，不涉及 UI 或视觉差异。证据：正文全部证据均为文本、表格和测试输出。

## 验证结论

- 历史 content 与 host 修复已在 0.1.12、0.1.13 生产环境验证。
- 动态模型发现的本地 build、vet、test、异常回退和测试污染检查均通过。

## 完成标准

- 发布后使用账号动态列表选择 `Doubao-Seed-2.1-Pro` 与 `qwen3.8-max` 完成真实对话。
- 本轮没有发布授权，因此生产回归未执行，不能声称动态模型功能已在生产生效。

## 问题现象

用户使用 Trae 导入的账号（traework 插件），切换到 qwen3.8-max 模型后请求一直失败：

```
{"code":4001,"message":"bad request: json: cannot unmarshal string into
Go struct field LLMRawMessage.messages.content of type []*idecopilot.LLMRawMessageContent"}
```

外层包装：`upstream HTTP 400`（Trae 网关返回，cli-proxy-api 转发层透传）。

## 根因结论（静态定位闭环）

- **异常点**：`traework/stream.go` 的 `toTraeMessages`（修改前 247 行）。
- **根因**：该函数把 OpenAI 消息规范化为 `{role, content: string}`（纯字符串），但 Trae 上游 `llm_utils_chat` 网关的 `LLMRawMessage` 结构体定义 `messages[].content` 为 `[]*LLMRawMessageContent`（content parts 数组），Go 反序列化 string → 切片失败，返回 4001。
- **证据链**：
  1. `traework/stream.go:247`（修改前）`case string:` 直接 `"content": c`，发出字符串。
  2. `traework/upstream.go:114` `buildTraePayload` 原样透传 messages。
  3. 参照开源实现 `Sliverkiss/traework2api` 的 `internal/upstream/payload.go` `PrepareBody` 改写规则 1 明确注释：`content 字符串 → [{"type":"text","text":...}]；已是数组 → 透传`，与其 README 声称实测可用（llm_utils_chat + solo_work_lite，32 模型）互证。
- **为何现在才爆**：报错来自服务端 idecopilot 结构体反序列化，属协议硬校验。此前未暴露大概率因为此前使用的模型/通道链路不同或 Trae 服务端收紧校验；与本插件代码无关的时间线因素不影响根因判定，发送格式本身不满足上游契约。

## 修复方案（已实施）

- **改动点**：仅 `traework/stream.go` `toTraeMessages`。
  - `content` 为 string → 包装为 `[{"type":"text","text":...}]`（新增 `textParts` helper）。
  - `content` 为多模态数组 → 不再 join 成字符串，逐 part 映射为 `{"type":"text","text":...}` 数组透传。
- **根因修复判定**：改的是协议构造源头（消息规范化函数），所有调用路径（executor.go 非流式 59 行 / 流式 130 行）均经过该函数，单一源头消除，无表层特判。
- **备选方案（未采用）**：在 payload 序列化层做二次包装，会让规范化函数与 wire format 脱节，且两处构造点需重复处理，劣于源头修复。

## 风险评估

- 影响范围：traework 插件全部模型的请求消息构造（一处函数），无公共跨插件模块。
- 风险等级：低。与外部已实测参照实现同形状；行为变化仅"错误格式 → 正确格式"。
- 已知残留风险：上游若对 `role`/`tool_calls` 等另有形状要求（如 tool 回传 `function_call`），是独立问题，本轮报错未涉及。

## 验证

- 本地 `python scripts/cgo-shim-build.py traework`：`go build` / `go vet` / `go test` 全绿（2026-08-30）。
- 待真实验证：服务器部署后用 qwen3.8-max 发起对话（生产部署走插件商店 install，见发布规则）。

## 状态

根因定位：完成。修复实施：完成（未提交 Git，等用户显式授权）。真实上游回归：待部署后确认。

---

## 追加根因 2：对话/签到 host 共用常量导致 llm_utils_chat TLB 404（0.1.13 修复）

- 现象：0.1.12 修好 4001 后，对话仍报 `upstream HTTP 404 ... <center>TLB</center>`（网关路由不存在特征，非鉴权拒绝）。
- 根因：0.1.11 为修签到 `checkin_credits/*` TLB 404，把 `defaultAPIHost` 从 `trae-api-cn.mchost.guru` 改为 `api.trae.cn`。但该常量被 `defaultTraeConfig().APIHost` 复用，而 `cfg.APIHost` 的唯一消费者是对话链路 `apiHostFor`（`llm_utils_chat` 路径）；签到实际走 `checkinHost()`（凭据 Host → 常量 fallback），不读 `cfg.APIHost`。共用常量 → 对话请求打到无 `/api/agent/v3/*` 路由的 `api.trae.cn` → TLB 404。
- 修复（`traework/config.go` + `upstream.go`）：新增 `defaultChatAPIHost = "https://trae-api-cn.mchost.guru"` 供对话默认；`defaultAPIHost`（api.trae.cn）仅作签到 fallback。两域名各归其位。服务器 config 无 `api_host` 覆盖（确认走默认值，修复有效）。
- 验证：cgo-shim build/vet/test 全绿；0.1.13 发布部署成功，accounts/panel 200，restart_required=false。

## 追加根因 3：CDN 中间态把 0.1.12 二进制保存为 0.1.13 文件名

用户在 0.1.13 部署后用 `seed-2.1-pro` 实测仍返回同样 TLB 404。运行时取证最终确认：

1. 服务器侧无认证路由探针：
   - `trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat` → HTTP 401 JSON（路由存在）；
   - `api.trae.cn/api/agent/v3/llm_utils_chat` → HTTP 404 TLB HTML（路由不存在）。
2. 同一生产账号、同 payload/headers A/B：
   - mchost.guru → HTTP 200 SSE（进入业务层，返回参数类 4001）；
   - api.trae.cn → HTTP 404 TLB，与用户报错逐字一致。
3. 文件级哈希：
   - 本地正确 0.1.13 `.so` sha256：`759b09c8aff9b7ce88594626fce2aa406811c0fc39a8e7e240240f3776188499`；
   - 生产名为 `traework-provider-v0.1.13.so` 的 sha256：`bd279db129053cfcd3227cd14d408f9bb03cffcc7739c58df1d9f55e46f1fe06`；
   - 该值与本地 0.1.12 `.so` 完全一致，`strings` 也显示内置版本 `0.1.12`。
4. 原因：发布流程先 push 了 `registry.json.version=0.1.13`，但 `install.artifacts` 当时仍指向 0.1.12。生产 install 命中 raw CDN 中间态，把旧二进制按新版本文件名保存；install API 仍返回 `version=0.1.13`，无法据此识别错装。
5. 处置：待 publish CDN 传播后重新 install；生产 `.so` sha256 变为正确的 `759b09c8...`，内置版本为 0.1.13；重启宿主后日志 `plugin loaded` / `plugin registered` 均为 0.1.13，accounts/panel 均 200。

发布规则已从根因上调整：Step 1 只 bump 插件 VERSION/main.go，禁止提前 push registry version；Step 11 由 `publish-assets.py` 原子更新 registry version + artifacts；install 后强制比对落盘二进制 sha256/内置版本。

## 最终状态（0.1.13）

- 4001（content 格式）→ 已修复（0.1.12）。
- TLB 404（对话/签到 host 混用）→ 已修复（0.1.13）。
- 生产错装旧二进制 → 已重新 install 正确 0.1.13 并重启，文件哈希及注册版本双重确认。
- `qwen3.8-max` 已确认是上游返回的精确 `config_name`；`seed-2.1-pro` 不是有效上游 ID，需使用 `Doubao-Seed-2.1-Pro`。

## 追加根因 4：手工模型短 ID 不等于上游精确 config_name

正确的 0.1.13 生效后，`seed-2.1-pro` 不再返回 TLB 404，而是以 HTTP 200 SSE 返回业务错误 `4001 param is invalid`。这说明请求已经进入模型业务层，剩余问题是模型参数本身。

同一生产账号、同一消息请求的 A/B 结果：

| `config_name` | 结果 |
| --- | --- |
| `seed-2.1-pro` | SSE 业务错误 4001，参数无效 |
| `Doubao-Seed-2.1-Pro` | 成功进入推理 |
| `qwen3.8-max` | 成功进入推理 |

根因是客户端展示短 ID 被直接当作上游 `config_name`。Trae 对话接口要求精确模型标识，不能根据展示名或文档示例猜测。为兼容已有客户端，`traework/upstream.go` 增加了 3 个已确认的 Seed 短 ID 映射；未知但本身精确的 ID 仍原样透传，`auto` 继续使用 `inline_chat`。

## 动态模型发现方案

账号级模型能力改为调用：

```text
POST https://trae-api-cn.mchost.guru/api/ide/v1/get_detail_param
```

请求使用 `function=solo_work_lite`、`config_names=null`、`need_prompt=false` 和 `poly_prompt=true`。当前生产账号实测返回约 37 个真实 `config_name`，其中包含 `Doubao-Seed-2.1-Pro`、`Doubao-Seed-2.1-Turbo`、`Doubao-Seed-Evolving`、`qwen3.8-max` 等。

实现语义：

1. `model.for_auth` 从 `AuthModelRequest.StorageJSON` 解析账号，使用现有 `hostHTTPDo` 调用动态接口。
2. 动态成功时，以 `config_info_list[].config_name` 为事实源；按 ID 去重，忽略空 ID，展示名缺失时回退为 ID。
3. 请求失败、非 2xx、响应无法解析或列表为空时，回退 `loadedModels()`，保留 `config_yaml models` 的覆盖语义。
4. `model.static` 没有账号凭据，继续返回配置或内置静态列表。
5. 动态成功时不混入手工列表，避免把已知错误的短 ID 重新暴露给客户端。

## 动态模型本地验证

`traework/models_dynamic_test.go` 使用 `httptest.NewServer` 模拟上游，验证正确路径、认证头、请求参数、去重、展示名回退，以及无效请求、缺失凭据、非 2xx、坏 JSON、空列表 5 类降级路径。`traework/upstream_model_test.go` 覆盖 Seed 别名、精确 ID 透传和 `auto` 路由。

```text
[cgo-shim] OK: go build ./...
[cgo-shim] OK: go vet ./...
ok github.com/luode0320/cpa-workbuddy-plugin/traework 1.075s
[cgo-shim] OK: go test ./...
[cgo-shim] all green (traework)
```

本轮动态模型代码处于“已修改、未提交、未发布”状态。生产环境仍是 0.1.13，不包含动态模型发现与 Seed 短 ID 映射；本地验证不能替代发布后的生产模型回归。
