# verify_main.go 模板（本地直连 Trae 上游）

> 从 2026-09-01 实测验证程序提炼（新账号 uid 2257747741770235 qwen3.8-max「分析这个项目」完整 done 的那次）。放在 `/tmp/traework_verify/verify_main.go`，与 shim 后的 traework 源码同包编译。

## 前置：源文件准备 + cgo shim

见主 SKILL.md「默认执行流程 Step 1-2」。shim 后 main.go 的 `func main() {}` 改名为 `pluginEntryStub() {}`。

## 完整模板

```go
// verify_main.go — 本地直连验证程序（不入库，临时资产）。
// 复用 traework 插件的解密 / header / payload / SSE 收尾逻辑，
// 用 storage.json 里的真实 Trae SOLO 账号对指定模型发起
// llm_utils_chat 流式请求，判定「<目标任务>」能否完整完成。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const storageJSONPath = `C:\Users\luode\AppData\Roaming\TRAE SOLO CN\User\globalStorage\storage.json`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatalf("[verify] FAIL: %v", err)
	}
}

func run() error {
	// 1. 读取 storage.json 并提取 icube 凭据 blob（tc-header 加密）。
	raw, err := os.ReadFile(storageJSONPath)
	if err != nil {
		return fmt.Errorf("read storage.json: %w", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse storage.json: %w", err)
	}
	blobRaw, ok := m[icubeCloudideKey]
	if !ok {
		return fmt.Errorf("storage.json missing %s", icubeCloudideKey)
	}
	var blob string
	if err := json.Unmarshal(blobRaw, &blob); err != nil || trimSpace(blob) == "" {
		return fmt.Errorf("%s is not a credential string", icubeCloudideKey)
	}

	// 2. 解密凭据（tc-header AES，与插件 load 路径一致）。
	cred, err := decryptCredentialString(blob)
	if err != nil {
		return fmt.Errorf("decrypt credential: %w", err)
	}
	uid := trimSpace(cred.UserID)
	if uid == "" {
		return fmt.Errorf("decrypted credential has no userId")
	}
	log.Printf("[verify] 账号解密成功 uid=%s token_len=%d host=%s expiredAt=%s",
		uid, len(cred.Token), trimSpace(cred.Host), trimSpace(cred.ExpiredAt))

	// 3. 构造 traeAuth（deviceId/machineId 留空，与 storage.json 直导路径一致）。
	a := &traeAuth{Token: cred.Token, UserID: uid, Host: cred.Host}
	if !a.hasToken() {
		return fmt.Errorf("empty token after decrypt")
	}

	// 4. 构造「<目标任务>」请求体，走插件 buildTraePayload 流式路径。
	prompt := "<在这里写目标 prompt，如：请全面分析当前项目 F:\\cpa-plugin，包括结构/模块/功能/技术栈/问题/建议，尽量详尽完整。>"
	messages := toTraeMessages([]map[string]any{
		{"role": "system", "content": "<系统提示>"},
		{"role": "user", "content": prompt},
	})
	model := "<模型名，如 qwen3.8-max>"
	// max_tokens 传 0 → buildTraePayload 流式路径补默认 streamDefaultMaxTokens=20000。
	payload := buildTraePayload(messages, model, true, 0, nil, nil)
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	log.Printf("[verify] payload: model=%s fn=solo_work_lite stream=true max_tokens=%v body_bytes=%d",
		model, payload["max_tokens"], len(bodyBytes))

	// 5. 构造请求 + 自定义长超时 client（关键：插件 sharedHTTPClient 120s 会掐长流式）。
	reqURL := apiHostFor(a) + llmUtilsChatPath
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header = buildTraeAuthHeaders(a)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req = req.WithContext(ctx)

	client := &http.Client{Timeout: 0} // 交给 context 控制总时长
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("llm_utils_chat transport: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("[verify] 上游 HTTP %d（%s）", resp.StatusCode, time.Since(started).Round(time.Millisecond))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		return fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, truncateRedacted(string(body), 300))
	}

	// 6. 带进度日志的流式扫描（区分「无数据挂起」与「有数据但慢」）。
	outChunks, outChars, reasoningChars, term, scanErr := verifyCollectWithProgress(resp.Body)
	elapsed := time.Since(started)
	if scanErr != nil {
		log.Printf("[verify] 扫描错误: %v", scanErr)
	}

	// 7. 输出结论证据（脱敏，不打印 token 原值）。
	log.Printf("[verify] 结果: http=%d termination=%s chunks=%d out_chars=%d reasoning_chars=%d elapsed=%s",
		resp.StatusCode, terminationLabel(term), outChunks, outChars, reasoningChars, elapsed.Round(time.Millisecond))
	switch term {
	case terminationDone:
		log.Printf("[verify] 结论: 上游正常完成（done 收尾），无断流无错误。")
	case terminationOutputEOF:
		log.Printf("[verify] 结论: 上游中途断流（部分 output 后 EOF 无 done），触发 length 兜底收尾。")
	case terminationInvalid:
		log.Printf("[verify] 结论: 空响应（无 output 无 done），无效。")
	}
	return nil
}

// verifyCollectWithProgress 复用 collectTraeStream 的 SSE 判定逻辑，额外统计
// output 字符数 / reasoning 字符数，并每 10 秒打印一次进度。
func verifyCollectWithProgress(r io.Reader) (chunks int, outChars, reasoningChars int, term traeStreamTermination, err error) {
	var terminal traeSSETerminal
	started := time.Now()
	lastLog := time.Now()
	err = scanSSE(r, func(ev sseEvent) error {
		switch ev.Event {
		case "output":
			text, reasoning, ok := terminal.recordOutput(ev.Data)
			if !ok {
				return nil
			}
			chunks++
			outChars += len(text)
			reasoningChars += len(reasoning)
		case "done":
			terminal.recordDone()
		case "error":
			msg := streamErrData(ev.Data)
			if msg == "" {
				msg = ev.Data
			}
			return fmt.Errorf("upstream event:error: %s", truncateRedacted(msg, 200))
		}
		if now := time.Now(); now.Sub(lastLog) >= 10*time.Second {
			log.Printf("[verify] 进度: elapsed=%s chunks=%d out_chars=%d reasoning_chars=%d",
				now.Sub(started).Round(time.Second), chunks, outChars, reasoningChars)
			lastLog = now
		}
		return nil
	}, terminal.hasPayload)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	t, cerr := terminal.classify(200)
	if cerr != nil {
		return 0, 0, 0, 0, cerr
	}
	return chunks, outChars, reasoningChars, t, nil
}
```

## 运行

```bash
cd /tmp/traework_verify
CGO_ENABLED=0 GOFLAGS=-mod=mod go build -o verify.exe . && ./verify.exe | tee verify_run1.log
```

## 判定速查

| termination | 含义 | 结论 |
|---|---|---|
| `done` | 收到明确 done 事件 | 完整完成 ✅ |
| `output_eof` | 有部分 output 但 EOF 无 done | 上游断流，插件补 length 兜底收尾 |
| `invalid` | 无 output 无 done | 空响应，无效 |

## 证据要求

- 临时目录保留：`verify_main.go` + `verify_run*.log`（运行日志）
- 日志必须含：账号 uid（脱敏 token）、HTTP 状态、termination、分片数、正文/思考字符数、总耗时
- 结论必须区分「账号级」与「插件级」：换账号同请求对比，才能下账号级结论（参考 2026-09-01：新账号 done vs 生产账号短输出，定性为账号级问题）
