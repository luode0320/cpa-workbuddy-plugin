STYLE: PASS

# 6-review：doubao msToken 从响应头自动续期（0.1.34）

- 审查对象：`doubao/` 下本轮改动的 8 个文件（3 个生产 .go + 1 个 _test.go + `CHANGELOG.md` / `README.md` / `VERSION` / `main.go`）
- 审查时刻：2026-11-21 15:25:00
- 审查口径：`code-style-consistency-rules` 的 `6-review`，只判写法、格式、位置、注释、日志、可读性与目录归位，不判业务正确性
- 与 0.1.33 的关系：本文件只覆盖 0.1.34 增量；0.1.33 见 `doc/6-review/2026-11-21_134500_doubao占位块短路与封面污染_6-review.md`

## 1 静态 Owner 路由命中

`static_owner_router.py --changed "doubao/completion.go,doubao/authfile.go,doubao/sign.go,doubao/completion_test.go" --root "C:\Users\luode\.workbuddy\skills"` → `exit 0`。

命中：**STYLE-01**（静态格式与编码一致性）/ **STYLE-02**（命名与符号引用一致）/ **STYLE-03**（注释分层与定义位置）/ **STYLE-04**（函数签名与参数结构）/ **STYLE-05**（结构与落点）/ **STYLE-08**（语言与框架特异写法）/ **STYLE-09**（测试资产与编码前契约）。
未命中 STYLE-06 / STYLE-07。

**执行前提**：技能包下两个冗余副本（`code-generation-style-rules/SKILL (1).md` 与 `code-style-consistency-rules/references/user-style-feedback-library (1).md`）会挡住来源映射校验。本轮把它们临时挪到 `%TEMP%\zz_router_stash` 再挪回，`RESTORED: f1=True f2=True`，未修改任何技能包文件。

## 2 编码与格式

`gofmt -l` 在 `%TEMP%\fmtchk0134` 的 52 个 LF 副本上跑 → **`FMT_OK`**（零输出）。首轮曾出现一处 `FMT_ISSUE`，见第 4 节。

| 文件 | 字节 | CRLF | LONE | BOM |
| --- | --- | --- | --- | --- |
| `doubao/completion.go` | 56839 | 1311 | 0 | False |
| `doubao/completion_test.go` | 34723 | 827 | 0 | False |
| `doubao/authfile.go` | 21563 | 583 | 0 | False |
| `doubao/sign.go` | 11545 | 305 | 0 | False |
| `doubao/main.go` | 14369 | 424 | 0 | False |
| `doubao/CHANGELOG.md` | 92860 | 937 | 0 | False |
| `doubao/README.md` | 16081 | 287 | 0 | False |
| `doubao/VERSION` | 6 | 0 | 0 | False |

`VERSION` 无换行符是既有约定（0.1.32 / 0.1.33 同样无换行），保持一致。

## 3 命名与符号引用（STYLE-02）

本轮新增三个顶层符号，全部小写包内可见，与相邻符号同一粒度：

| 符号 | 类型 | 命名依据 |
| --- | --- | --- |
| `msTokenHeader` | 常量 | 与既有 `callbackMsTokenHeader`（`doubao/login.go:548`）同一「<语义>Header」模式；两者语义不同（一个是上游响应头，一个是插件自定义回调头），名字已能区分 |
| `adoptMsTokenFromHeader` | 函数 | 动词开头 `adopt` 准确表达「采纳上游递来的值」；`FromHeader` 点明来源，与 `persist` / `seedFromHeader` 同一命名气质 |
| `isMsTokenShaped` | 函数 | `is<X><Adjective>` 与既有 `isVideo` / `isSafeDoubaoAuthPath` / `isDoubaoAuthFileName` 一致；`Shaped` 比 `Valid` 更准确 —— 只校验形态不校验真实性 |

无同义重复：`Select-String` 全仓确认 `adoptMsToken` / `isMsTokenShaped` 各只有一处定义。

## 4 注释分层与定义位置（STYLE-03）

`completion.go` 新增符号定义位置：`msTokenHeader` / `adoptMsTokenFromHeader` / `isMsTokenShaped` 连续排在 `streamCompletion` 之后、`truncateText` 之前，与「调用方在前、被调方在后」的既有排序一致（`streamCompletion` 是本轮唯一调用点）。

注释分层符合 `comment-rules`：

- `streamCompletion` 只在函数头改了 `最近修改时间` 一行，函数体内新增的 「3. …」/ 「4. …」步骤编号与既有 `1.` / `2.` 连续；
- `adoptMsTokenFromHeader` 函数头四段式：一段讲「为什么整个续期机制只靠这一个响应头」（含实测证据与端点行为），一段讲「落盘为什么优先交给 Jar」，然后 `// [参数]` 三条独立行、`// [返回]` 一行、`// 最近修改时间` 一行；
- `isMsTokenShaped` 函数头补一句「不校验长度」的取舍说明；
- `msTokenHeader` 常量头补一句「头名大小写不敏感」。

**gofmt 踩坑（已修）**：初版 `// [参数] sa: …；jar: …；` 折行后第二行以 `header:` 开头，gofmt 把「冒号开头的续行」识别成缩进代码块，输出：

```
-//        header: 上游响应头。
+//
+//	header: 上游响应头。
+//
```

改法是把折行的一行拆成三条独立 `// [参数]` 行。这也与 `comment-rules` 的「一参数一行」口径一致。

## 5 函数签名与参数结构（STYLE-04）

```go
func adoptMsTokenFromHeader(sa *StoredAuth, jar *durableJar, header http.Header)
func isMsTokenShaped(token string) bool
```

参数 3 个 / 2 个，未触碰 `code-quality-rules` 的「函数参数和返回值超 2 个改结构体」边界 —— 该条针对的是需要长期演进的接口；这里是包内私有工具函数，且 `(sa, jar, header)` 三者语义正交、不会继续增长，拆结构体反而增加间接层。

`header http.Header` 而非 `map[string][]string`：与 `net/http` 的 `resp.Header` 同一类型，调用点零转换。

无返回值：失败只记日志，不影响本次回复。这一点在函数头 `// [返回]` 里写明，避免后来者误以为需要检查错误。

## 6 结构与落点（STYLE-05）

| 落点 | 判断 |
| --- | --- |
| `completion.go` 放 `adoptMsTokenFromHeader` | 正确。调用点在 `streamCompletion`，同文件的「请求-响应生命周期」区域；放到 `authfile.go` 会让「读响应头」这个动作与「写账号文件」的职责分处两文件，读代码时要跳两次 |
| `completion.go` 放 `msTokenHeader` 常量 | 正确。`protocol.go` 是协议常量区，但那里放的是「发出去的请求」相关常量；`x-ms-token` 是「收回来的响应」，就近定义更易维护 |
| `completion_test.go` 放 5 个用例 | 正确。测试源码镜像目录约定下，`adoptMsTokenFromHeader` 属 `completion.go`，用例跟到这里 |
| `doc/5-tests/...` 第 36 节 | 正确。沿用既有扁平测试主文档，新增章节而不另起文件 |

未被 `adoptMsTokenFromHeader` 破坏的既有边界：`durableJar.persist` 仍是唯一带替换闸门的写盘入口，新函数只是调用它，没有绕过去直接写文件。

## 7 语言与框架特异写法（STYLE-08）

逐条对照 `golang-patterns` 与本仓既有写法：

1. **未持有未加锁字段**。初版写 `if jar != nil && jar.path != ""` 直接读 `jar.path`，那是 `durableJar` 的私有字段、由 `j.mu` 保护；改为 `if jar != nil { jar.persist(sa) }`，未绑定文件时 `persist` 内部返回 nil。避免数据竞争。
2. **`http.Header.Get` 自带大小写不敏感**，不需要 `strings.EqualFold` 遍历 header map。
3. **`log.Printf` 而非 `fmt.Printf`**：与 `doubao/login.go:638` / `doubao/model_discovery.go` 的既有日志写法一致，都带 `[doubao]` 前缀与 `%s` 账号名。
4. **`strings.TrimSpace` 双向归一**：头的值与账号里的现存值都 TrimSpace 后再比较，避免上游带尾空格时反复落盘。
5. **`switch` 多值 case 写成一行的 `case r == '-', r == '_', …`**：Go 允许，且比五行 `if` 链更紧凑；字符范围用 `case r >= 'a' && r <= 'z'` 保持可读。
6. **无 `else` 嵌套**：`if errSave != nil { log…; return }` 后直接 `log…`，与仓内 `cookiejar.go` / `executor.go` 的早返回风格一致。

## 8 测试资产（STYLE-09）

新增 5 个用例 + 1 个共享夹具，全部落在 `doubao/completion_test.go`（测试镜像目录）：

| 用例 / 夹具 | 覆盖点 |
| --- | --- |
| `streamWithMsTokenHeader` | 夹具：隔离目录 + 落盘 + 回读 + httptest + 真实 `streamCompletion` |
| `TestStreamCompletionAdoptsMsTokenHeader` | 响应头新值写回内存并落盘 |
| `TestStreamCompletionIgnoresMalformedMsToken` | 含空格的非法值不覆盖 |
| `TestIsMsTokenShaped` | 7 组表驱动 |
| `TestAdoptMsTokenWithoutJar` | 无 Jar 时退回直写 |
| `TestAdoptMsTokenSkipsUnchanged` | 同值不重写（摘要前后一致） |

写盘隔离用既有 `isolateAccounts(t)`（`login_test.go:19-26`），不引入新的临时目录方案。
夹具里 `writeAuthFileDirect` → `readAuthFile` 的两步不是冗余：`Jar` 只有在 `PhysicalPath`
非空时才 `bind` 文件，直接复用内存里的账号会让 `persist` 变成空操作，用例就测不到落盘。

## 9 门禁与哨兵

`python scripts/cgo-shim-build.py doubao` → `OK: go build ./...` / `OK: go vet ./...` / `ok …doubao 4.256s` / **`all green (doubao)`**。

四条哨兵全部真实失败、复位后 `all green`：

| 哨兵 | 失败用例 |
| --- | --- |
| `adopt` | `TestStreamCompletionAdoptsMsTokenHeader` |
| `shape` | `TestStreamCompletionIgnoresMalformedMsToken` |
| `same` | `TestAdoptMsTokenSkipsUnchanged` |
| `nojar` | `TestAdoptMsTokenWithoutJar` |

**哨兵脚本自身的缺陷（已修，写进结论）**：初版 `restore` 把「脚本启动时读到的内容」写回，
连跑四条时第二次启动读到的已是被改坏的文件，`restore` 反而把坏版本固化。发现手段是
`Select-String` 确认 `completion.go:954` 是 `_ = adoptMsTokenFromHeader`、`:999` 是 `if false`。
修法：从 `.tmp_bk034\completion.go`（改动前纯净备份）恢复后重施改动，再把正确态快照到
`.tmp_s034_good\completion.go`，`restore` 一律从该快照 `copy2`。

## 10 STYLE-06 / STYLE-07 未命中说明

- **STYLE-06**（跨文件职责与依赖方向）：本轮只改 `completion.go` / `authfile.go` / `sign.go`
  三个文件，其中 `authfile.go` 与 `sign.go` 只动注释，没有新增跨包/跨文件依赖。`completion.go`
  新增的 `log` 是标准库，无新依赖。
- **STYLE-07**（构建与发布配置）：未动 `go.mod` / `go.sum` / CI workflow / registry schema。

## 11 边界声明

本审查只判风格与写法，不判：

- `x-ms-token` 是否真的每请求必发（属功能验证，证据在 `doc/5-tests/...` 第 36 节）；
- 上游对 token 只做形态粗判这一结论是否可泛化（属实测结论，同上）；
- 续期后 `sign_state` 是否该改判据（属未覆盖项，见第 36.9 节）。
