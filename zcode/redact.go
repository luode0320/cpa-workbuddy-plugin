// redact.go 负责把凭据形态的文本从任何可能落到日志、宿主错误帧或用量数据流的
// 字符串里剥掉。zcode 的上游报文会原样带回 `Authorization` 头、JWT 与
// `access_token` 片段，而 `streamEmitError` 与 usage feed 的 `fail.body`
// 都会把它透出到宿主日志与 NDJSON 文件，因此所有对外暴露上游错误文本的出口
// 必须先经过 redactSecrets（需要限长时用 truncateRedacted）。
package main

import "regexp"

var (
	// redactREBearer 匹配 `Bearer <token>`，覆盖上游错误回显鉴权头的场景。
	redactREBearer = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]{12,}`)
	// redactREJWT 匹配标准三段式 JWT（header.payload.signature）。
	redactREJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`)
	// redactRETokenKV 匹配 `access_token=...` / `refresh_token: ...` 这类键值片段。
	redactRETokenKV = regexp.MustCompile(`(?i)((?:access_token|refresh_token|id_token)\s*[=:]\s*)([A-Za-z0-9._\-+/=]{12,})`)
	// redactREJWTLoose 兜底匹配裸出现的 JWT：没有 Bearer 前缀、也没有键名上下文。
	// 两段与三段都匹配（部分上游在签名为空时只回 header.payload）。
	redactREJWTLoose = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}(?:\.[A-Za-z0-9_\-]{4,}){1,2}\b`)
)

// redactSecrets 剥掉文本中的 bearer token 与 JWT 形态片段。
// [参数] s：待脱敏文本，可为空。
// [返回] 脱敏后的文本；空输入原样返回。
// 最近修改时间：2026-10-25；改动原因：新增，上游错误文本出口统一脱敏。
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	// 1. 先处理带 Bearer 前缀的鉴权头回显。
	s = redactREBearer.ReplaceAllString(s, "Bearer ***")
	// 2. 再处理完整三段式 JWT，避免被第 4 步的宽松规则切碎后留下片段。
	s = redactREJWT.ReplaceAllString(s, "***jwt***")
	// 3. 处理 access_token / refresh_token / id_token 键值片段，保留键名便于定位。
	s = redactRETokenKV.ReplaceAllString(s, "${1}***")
	// 4. 最后兜底裸 JWT。
	s = redactREJWTLoose.ReplaceAllString(s, "***jwt***")
	return s
}

// truncateRedacted 先脱敏再按字节截断，供需要限长的错误文本出口使用。
// [参数] s：待处理文本；n：保留的最大字节数。
// [返回] 脱敏并截断后的文本。
// 最近修改时间：2026-10-25；改动原因：新增，宿主错误帧与日志需要限长。
func truncateRedacted(s string, n int) string {
	return truncateBytes(redactSecrets(s), n)
}

// truncateBytes 把字符串按字节截断到 n。上游报文在 token 边界处是 ASCII，
// 直接按字节切不会破坏 UTF-8 多字节字符（中文正文出现在截断点时由调用方
// 保证只用于错误摘要，不影响协议正确性）。
// [参数] s：待截断文本；n：最大字节数。
// [返回] 不超过 n 字节的文本。
// 最近修改时间：2026-10-25；改动原因：新增，避免与 workbuddy 的 truncate 同名冲突。
func truncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}
