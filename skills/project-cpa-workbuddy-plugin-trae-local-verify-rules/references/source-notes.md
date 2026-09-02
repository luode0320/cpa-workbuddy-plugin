# Source Notes

## 2026-09-01 创建：project-cpa-workbuddy-plugin-trae-local-verify-rules

- **创建原因**：用户点名「先把这个本地调整账号跑通推理的经验吸收到本地 skill 中」（skill-absorption-rules 通道 B 主动点名）
- **来源**：本地直连 Trae 上游（llm_utils_chat）复用 traework 插件逻辑测试账号推理的完整实操（2026-08-31/09-01 会话）
- **触发场景**：需要本地验证「某个 Trae 账号 + 某模型能否跑通完整推理」，不污染插件仓库、不依赖宿主 host RPC
- **覆盖**：
  - 5 步默认流程：准备临时目录 → cgo-shim 构建 → 编写 verify_main.go → 运行与判定 → 清理
  - 复用 traework 源码的解密（decryptCredentialString / iCubeAuthInfo blob / saltA^saltB AES-128-CBC + sha512）、header（Authorization=Cloud-IDE-JWT）、payload（buildTraePayload / max_tokens=0 补默认 20000）、SSE 解析（scanSSE / classify）
  - verify-main-template.md：完整可运行 verify_main.go 模板 + done/output_eof/invalid 判定速查表 + 证据要求
  - 5 条实测踩坑：sharedHTTPClient 120s 截断长流式（必须自定义 client+context）、先 reasoning 后正文、storage.json 账号 ≠ 生产账号、Windows 无 CGO 直连、SSE output 格式
- **关联 skill**：`project-cpa-workbuddy-plugin-release-rules`（发布链路，只引用 cgo 验证不重复）；`cgo-plugin-isolated-test`（cgo-shim 机制实现者，本 skill 引用不重复）
- **边界**：只管「本地单账号 × 单模型能否跑通推理」的验证；生产侧账号池路由、failover、4xx 重试等运行时行为不归本 skill
