# Changelog

本文件记录 `safeguard-killer` 插件的版本变更。格式按版本号倒序，每条包含背景、改动、验证与涉及文件。

## 0.1.0

### Feat - 首次从上游 vendor 并纳入本仓库发布链路

- 背景: 需要一套能在本仓库自行构建、自行发布、可长期维护的「拒答救援 + Smart Groups 路由」能力。上游 `esauvisky/CLIProxyAPI-Safeguard-Killer-Plugin` v0.5.10 已具备完整能力（拒答检测与透明换模型、Claude Code 拒答污染头剥离、auto-mode 分类器旁路、配额加权凭证平衡、5 分钟 prompt-cache 粘性），因此原样 vendor 进本仓库，只改归属与版本号，不改业务逻辑。
- 归属改造: `pluginName` 保持 `Safeguard Killer`；`pluginAuthor` 改为 `luode0320`；`pluginRepo` / `pluginLogo` 指向本仓库；`pluginVersion` 由常量改为 `var pluginVersion = version`，版本号交由 CI 以 `-ldflags -X main.version` 注入，与本仓库其余插件一致。plugin id 仍为 `safeguard-killer`，因此管理路由 `/v0/management/plugins/safeguard-killer/state` 与 `CONFIG_API_URL` 无需改名。
- 模块改造: `go.mod` module 改为 `github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`，`go 1.26` → `go 1.26.0` 对齐仓库其余插件；依赖不变（`github.com/tiktoken-go/tokenizer v0.8.1`、`gopkg.in/yaml.v3 v3.0.1`、间接 `github.com/dlclark/regexp2/v2 v2.5.1`）。插件不依赖 CLIProxyAPI SDK，纯 ABI v1，因此与本仓库任何插件并存且不受宿主 SDK 版本约束。
- 发布链路: `build.yml` 增加 tag 模式 `safeguard-killer-v*`、dispatch choice、test matrix、`PLUGINS` 字典、release job 的 tag 判定与 `tag → plugin` 分支、`DISPLAY` 显示名；`registry.json` 增加 6 平台（darwin amd64/arm64、linux amd64/arm64、windows amd64/arm64）direct 安装条目。
- 本地验证工具修复: `scripts/cgo-shim-build.py` 原先只会替换标准 ABI 符号，遇到本插件的三个 C 静态封装（`store_host_api` / `call_host_api` / `free_host_buffer`）会编译失败。已补三个 shim 桩并把 `C.free_host_buffer` 的替换规则排到 `C.free` 之前（前缀规则会先把 `C.free_host_buffer` 吃成 `cgoFree_host_buffer`）。
- 验证: `python scripts/cgo-shim-build.py safeguard-killer` 的 build / vet / test 全绿（`ok github.com/luode0320/cpa-workbuddy-plugin/safeguard-killer`）；`python scripts/validate-registry.py registry.json` 返回 `OK registry.json: 12 plugin(s), schema_version=2`。
- 涉及文件: `safeguard-killer/`（16 个 `.go` 含 7 个 `_test.go`、`go.mod`、`go.sum`、`LICENSE`、`Makefile`、`README.md`、`VERSION`、`tests/abi_test.c`）、`assets/icons/SafeguardKiller.png`、`.github/workflows/build.yml`、`registry.json`、`scripts/cgo-shim-build.py`。
