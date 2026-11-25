# 2026-11-25 traework keepalive 401 修复 CYCLE01 6-review

## 结论

STYLE: PASS

- 审查范围：`traework/keepalive.go`、`traework/keepalive_test.go`（本轮全部改动文件）
- 静态规则路由：`static_owner_router.py --root C:\Users\luode\.dsh\skills --changed traework/keepalive.go,traework/keepalive_test.go --json` → 命中 STYLE-01/02/03/04/08/09
- 本轮改动为单一根因（ExchangeToken 两层 envelope 解析层级错误）的手术式修复，未扩大 diff、未新增配置项、未碰其它逻辑

## STYLE-01 静态格式与编码一致性

- 两文件 BOM=False；`keepalive.go` CRLF=658/LF-only=0（tracked 历史行尾风格），`keepalive_test.go` 工作树 LF-only=364（git autocrlf 提交时自动转 CRLF，非本轮引入）；尾随空白=0
- 改动缩进、括号、换行与所在函数既有写法一致

## STYLE-02 命名与符号引用一致

- 新增 `env` / `envLower` / `unixMillisRFC3339` 命名语义清晰，与 `browserlogin.go` 的 `env` 命名对齐
- 复用文件既有符号 `pickString`、`trimSpace`、`truncateRedacted`，未重复造轮子；JSON PascalCase/camelCase key 与 `browserlogin.go:794-822` 保持一致

## STYLE-03 注释分层与定义位置

- `keepalive.go` 全文件为英文注释，本轮新增注释（函数头根因说明、`unixMillisRFC3339` 函数注释）均为英文，跟随当前文件稳定写法（与 doubao 模块中文元信息约定不同，按 style-priority 以当前文件为准）
- 函数头注释只写目的与根因，不写逐行过程；定义位置紧贴使用点

## STYLE-04 函数签名与参数结构

- `unixMillisRFC3339(v any) string` 单入参单返回，职责单一
- `keepaliveExchange` 签名未变，调用方零改动

## STYLE-08 语言与框架特异写法（golang-patterns）

- `json.Number.Int64()` 正确处理解码数值，失败返回空串而非丢弃 error
- `time.UnixMilli(ms).UTC().Format(time.RFC3339)` 与 `browserlogin.go` 时间格式一致
- `map[string]any` 取值统一走 `pickString`，避免类型断言散落

## STYLE-09 测试资产与编码前契约

- 4 个新测试追加在既有 `traework/keepalive_test.go`，未新建散落测试文件，符合该项目测试落点约定
- 测试名 `TestKeepaliveExchange*` / `TestUnixMillisRFC3339` 与被测符号对应；哨兵反证已验证测试真实进编译链

## 回归证据

- 门禁 `python scripts/cgo-shim-build.py traework`：go build / go vet / go test 全绿，EXITCODE=0
- 哨兵反证：临时 `token := ""` → `TestKeepaliveExchangeParsesEnvelope` 与 `TestKeepaliveExchangeCamelCaseResult` 真实 FAIL（EXITCODE=1）；恢复 `pickString` 后全绿
- 6-review 只检查代码风格、格式、位置、注释、日志、可读性与目录归位，不判断业务正确性、需求覆盖或发布放行
