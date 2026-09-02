# Source Notes

## 2026-08-23 创建：project-cpa-workbuddy-plugin-release-rules

- **创建原因**：用户点名「把提交、push、构建、registry 等后续一整套发布的流程吸收到项目的 skill」
- **来源**：cpa-workbuddy-plugin 0.12.0 / 0.12.1 / 0.12.2 三次完整发布实操（对话 + .workbuddy/memory/2026-08-22.md + 2026-08-23.md）
- **覆盖**：版本 bump 三处、混合文件分离发布、HTTPS+askpass push、dispatch CI、下载 assets、publish registry、远端验证、12 条实测踩坑
- **关联 skill**：cgo-plugin-isolated-test（本地验证职责，本 skill 引用不重复）；luode-skills 仓库 `project-interface-release-execution-rules` 是接口测试门禁（不同职责域，无重叠）
- **边界**：只管发布执行链路；代码正确性验证、需求/编码规则在其他 skill

## 2026-08-29 更新：补齐 0.14.16 / 0.9.6 / 0.1.9 三插件发布实战新铁律

- **更新原因**：2026-08-29 晚间会话完成 workbuddy-provider 0.14.16（失败筛选功能）/ qoderwork-provider 0.9.6 / traework-provider 0.1.9 三插件并行发布闭环，暴露多条 SKILL.md 未覆盖的坑
- **来源**：.workbuddy/memory/2026-08-29.md + MEMORY.md 铁律 + 会话实操
- **主要变更**：
  - push 由 HTTPS+askpass 改为 SSH 直推（origin 是 SSH remote，Step 7 重写；askpass 降级为遗留回退）
  - dispatch 铁律：inputs 必须同时含 plugin（provider id）+ version，缺 version → Release job skipped（不建 tag/Release，assets 下载 404）；plugin 传源码目录名 → 422
  - CI 轮询上限 11 分钟 → 15 分钟（deadline 900s，实测出现过 12 分钟 run）
  - 新增 Step 13 生产服务器部署：POST /v0/management/plugin-store/<provider-id>/install?version=<x.y.z>（docker cp 与 PUT config 均不触发热重载）
  - 下载脚本已参数化 <provider-id> <version>，item 14 标注已修复
  - qoderwork 状态更新为已全量对齐 workbuddy（2026-08-28）
  - 踩坑清单追加 21-27：TLS 备用通道、authFilePrefix 铁律（traework 面板「暂无账号」第三层根因）、多会话共享工作树 fetch 对齐
  - 插件清单更新为四插件 + 当前版本带（workbuddy 0.14.x / qoderwork 0.9.x / traework 0.1.x / token-usage 0.2.x）
- **验证**：全文回读无过时残留；版本示例统一为 0.14.16

## 2026-08-30 修正：download-release-assets.py 参数顺序（0.14.17 发布实测）

- **纠正自误**：2026-08-29 更新时把下载脚本参数顺序误记为 `<provider-id> <version>`，实际签名是 `<version> [plugin]`——与 publish-assets.py 的 `<plugin> <version>` **互为相反**。0.14.17 发布时传反 → tag 拼错 → Release API 404（症状与「资产未建」一致，已用 Release API 复核排除）。
- **修正位置**：Step 9 命令示例、关键命令速查表、踩坑 item 14。
- **经验**：沉淀脚本用法前先用 `head <script> | grep usage` 核对真实签名，不凭记忆写参数顺序。

## 2026-08-30 补充：store install CDN 边缘滞后误报（traework 0.1.10 发布实测）

- **新增踩坑 item 28**：registry push 后数分钟内 store install 可能报 `plugin_manifest_invalid: direct plugin version not found`（502 毫秒级返回），根因是宿主 Go 客户端命中的 raw CDN 边缘节点缓存滞后（同机 curl 反而看到新版）。宿主 FetchRegistry 无本地缓存（v7.2.129 源码实锤）。处置：等几分钟重试即成功。
- 同时记录 0.1.10 链路「接管并行会话 run」实战：CI run 与发布内容一致时不取消、直接接管后续链路；assets/registry 提交被并行会话抢先时以内容一致为准，不再重复提交。

## 2026-09-02 吸收：修复→发布→生产验证闭环（traework 0.1.28 完整跑通）

- **更新原因**：用户点名「将这个修复+发布+生产检验的流程和使用还有 url 密钥都吸收到这个项目的 skill 中。后续也会使用」。
- **来源**：0.1.28「异步流式宿主流桥打开挂死 → 30s 超时 + 降级插件直连 live 流」修复 + 发布 + 生产验证完整闭环实操（2026-09-01/02）。
- **主要变更（SKILL.md）**：
  - description 扩展：加入「修复→发布→生产验证闭环」「生产真实入口 https://cpa.luode.vip/v1 行为验收（流式 qwen3.8-max 长推理 + stream_id 判定）」「GitHub 上行阻断 SOCKS 隧道绕过」触发语义。
  - 新增 Step 14 生产行为验收（验收样本 / 成功指纹 / 失败处理）、Step 15 凭据纪律与清理（key 只写临时文件用后即删、禁回显原值、management key 提取命令宿主机管道串联）、Step 16 GitHub 上行大流量阻断绕过（生产服务器 SOCKS 隧道 + git socks5h + curl --socks5-hostname + urllib 不认 socks5）。
  - 关键命令速查表新增生产行为验收与网络阻断绕过两行；权责边界加入「生产行为验收」职责。
  - 踩坑清单追加 34-39：修复→发布→生产验证闭环、生产验证样本与成败指纹、凭据纪律、GitHub 上行阻断、生产日志中文回显、/v1/responses 与 /v1/chat/completions 格式差异。
- **整理去重**：新增内容与既有 Step 13 生产部署、踩坑 30（management key 提取）等无逐字重复；Step 15 的 key 提取命令与踩坑 30 互补（30 是容器校验分工，15 是凭据纪律），未造成门控层叠。
- **同域冗余扫描**：范围=本 skill + trae-local-verify-rules + cgo-plugin-isolated-test（发布链 / 本地验证 / 生产验收三域）。发现 0 处逐字重复、0 处门控层叠、0 处散落产物；本 skill 新增生产验收为域专属职责，trae-local-verify 只管本地直连验证、cgo-plugin-isolated-test 只管本地编译验证，无交叉。PASS。
- **环境依赖登记**：本 skill 无环境变量 / 宿主配置依赖新增（SOCKS 隧道依赖的 SSH key / 服务器地址是项目事实，已写入 skill 正文本身，非离开机器失效的配置）。
- **凭据纪律**：URL 与密钥不入 skill 正文原值——Step 14/15/16 仅写占位与提取命令，key 不落盘（正文只提「与 management key 相同，从 config 提取」），符合禁止回显凭据原值的仓库红线。
- **字典刷新**：本项目 skills/ 无 data.js / 字典.md（skill 字典机制属于 luode-skills 仓库，跨项目只读），不适用。
- **验证**：全文回读 + `git diff --check` PASS；UTF-8 读取无乱码。
