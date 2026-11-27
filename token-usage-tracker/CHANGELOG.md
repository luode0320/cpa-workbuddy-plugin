# Changelog

## 0.2.9 (2026-11-27)

### Fix - 插件自限内存上限并错峰启动，抑制容器 OOM

- 背景: 每个 CPA 插件以 `-buildmode=c-shared` 编译、各自链接一份完整 Go runtime，宿主的 `GOMEMLIMIT` 会被每个 runtime 独立读取，形不成整进程总预算；容器内存上限才是唯一硬约束。启动与批量高峰时多个 runtime 同时扩张，整进程会被 OOM 杀掉（生产实测：启动 23 秒内被 OOM，稳态仅 476MB）。
- 改动: 新增 `memory_limit.go`——插件 `init()` 里 `debug.SetMemoryLimit` 给自身堆设独立软上限（默认 128MiB，可用 `CPA_PLUGIN_MEMORY_LIMIT_MB` 覆盖），并提供 `goAfterStartupJitter` / `waitStartupJitter` 两个启动错峰助手（随机 0~3s）。feed 导入循环改为错峰启动。
- 语义: 软上限只作用于本插件自己的 runtime，超限只触发本插件 GC，不影响宿主与其它插件；错峰只推迟后台循环的首轮启动，不改变任何周期与业务语义；需要「停止即返回」的循环（doubao 续期、通知类）用 ctx 感知等待，不受睡眠影响。
- 验证: `cgo-shim-build.py` build+vet+test 全绿；新增 `memory_limit_test.go` 覆盖「默认/覆盖/非法值回落」「runtime 实际生效上限」「错峰后确实执行」「取消时立即返回」四类断言。
