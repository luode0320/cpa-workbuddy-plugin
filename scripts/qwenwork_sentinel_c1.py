#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qwenwork 哨兵反证：逐条改坏修复点，要求对应测试真实 FAIL。

用法：python scripts/qwenwork_sentinel_c1.py
换行风格自适应（仓库 Go 文件为 CRLF），S5 走全量替换以命中「两处调用点都错」。
"""
import os
import shutil
import subprocess
import sys
import time

ROOT = r"F:\cpa-plugin"
PKG = os.path.join(ROOT, "qwenwork")
BACKUP = os.path.join(ROOT, ".sentinel-backup-qwenwork")

MUTATIONS = [
    {
        "name": "S1 低熵会话标识剔除失效",
        "file": "session_auth.go",
        "old": 'if sid == "" || lowEntropySessionID(sid) {',
        "new": 'if sid == "" {',
        "expect": "TestExtractSessionKey_SkipsLowEntropyHeader",
        "all": False,
    },
    {
        "name": "S2 出站缺 x-session-id 会话绑定",
        "file": "main.go",
        "old": '\tif sessionID != "" {\n'
               '\t\treq.Header.Set("x-session-id", sessionID)\n'
               '\t}\n',
        "new": '',
        "expect": "TestApplyMaasHeaders_CarriesSessionBinding",
        "all": False,
    },
    {
        "name": "S3 上游会话键不再按请求体派生",
        "file": "session_auth.go",
        "old": 'func maasSessionKeyFromPayload(obj map[string]any) string {\n\tif obj == nil {',
        "new": 'func maasSessionKeyFromPayload(obj map[string]any) string {\n\tif true {\n\t\treturn ""\n\t}\n\tif obj == nil {',
        "expect": "TestMaasSessionKeyFromPayload",
        "all": False,
    },
    {
        "name": "S4 票据写入不再淘汰超限条目",
        "file": "maas.go",
        "old": '\tglobalMaasCache.sessions[key] = sess\n\tpruneMaasSessionsLocked(time.Now())\n',
        "new": '\tglobalMaasCache.sessions[key] = sess\n',
        "expect": "TestStoreMaasSessionLocked_PrunesOverCapacity",
        "all": False,
    },
    {
        "name": "S5 执行入口不再用请求体派生的 MaaS 会话键",
        "file": "main.go",
        "old": 'maasSessionKey := prep.SessionKey',
        "new": 'maasSessionKey := ""',
        "expect": "TestExecutors_UsePayloadDerivedMaasSessionKey",
        "all": True,
    },
    {
        "name": "S6 异步流入口把客户端别名当上游模型传下去",
        "file": "main.go",
        "old": 'pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, upstreamModel,',
        "new": 'pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, req.Model,',
        "expect": "TestExecutors_UsePayloadDerivedMaasSessionKey",
        "all": False,
    },
    {
        "name": "S7 TTFT 基准退回到发请求之后",
        "file": "stream.go",
        "old": 'collector.markAttemptStart(attemptStarted)',
        "new": 'collector.markAttemptStart(time.Now())\n\t\t_ = attemptStarted',
        "expect": "TestPumpUpstreamStream_MarksTTFTBeforeRequest",
        "all": False,
    },
    {
        "name": "S8 排队判据漏掉 serviceAvailable:false",
        "file": "policy.go",
        "old": '\tif strings.Contains(strings.ReplaceAll(body, " ", ""), serviceUnavailableMarker) {\n\t\treturn true\n\t}\n',
        "new": '',
        "expect": "TestIsQueueFrame_ExtendedClassification",
        "all": False,
    },
    {
        "name": "S9 模型池忙不再豁免账号冷却",
        "file": "policy.go",
        "old": '\treturn isModelPoolBusyBody(body)\n',
        "new": '\treturn false\n',
        "expect": "TestModelPoolBusy_DoesNotCoolAccount",
        "all": False,
    },
    {
        "name": "S10 慢速准入闸门回到 20（生产池放行不了）",
        "file": "slow_account.go",
        "old": 'defaultSlowMinPoolSize = 6',
        "new": 'defaultSlowMinPoolSize = 20',
        "expect": "TestSlowPoolAdmitted_OpensForProductionPoolSize",
        "all": False,
    },
    {
        "name": "S11 流桥信封丢掉 host_callback_id",
        "file": "host_bridge.go",
        "old": '\t\tHostCallbackID: hostCallbackID,\n',
        "new": '\t\tHostCallbackID: "",\n',
        "expect": "TestBuildHostHTTPStreamWire_CarriesHostCallbackID",
        "all": False,
    },
    {
        "name": "S12 非流式入口不传 host_callback_id",
        "file": "main.go",
        "old": 'doExecuteOnceQwenWork(body, curSA, req.Model, maasSessionKey, req.HostCallbackID)',
        "new": 'doExecuteOnceQwenWork(body, curSA, req.Model, maasSessionKey, "")',
        "expect": "TestExecutors_PassHostCallbackIDToUpstream",
        "all": False,
    },
    {
        "name": "S13 共享客户端丢掉分阶段超时",
        "file": "host_bridge.go",
        "old": '\t\t\t\tTLSHandshakeTimeout: upstreamTLSHandshakeTimeout,\n',
        "new": '',
        "expect": "TestSharedHTTPClient_HasStagedTimeouts",
        "all": False,
    },
    {
        "name": "S14 前缀缓存字段退回只读顶层",
        "file": "usage.go",
        "old": '\tif v := nested("prompt_tokens_details", "cached_tokens", "cache_read_input_tokens"); v > 0 {\n\t\td.CachedTokens = v\n\t}\n',
        "new": '\t_ = nested("prompt_tokens_details", "cached_tokens")\n',
        "expect": "TestUsageDetailFromMap_ReadsPromptTokensDetails",
        "all": False,
    },
    {
        "name": "S15 首字慢阈值放大到 1 小时",
        "file": "slow_request_log.go",
        "old": 'const slowRequestLogThreshold = 10 * time.Second',
        "new": 'const slowRequestLogThreshold = 1 * time.Hour',
        "expect": "TestLogSlowRequest_OnlyAboveThreshold",
        "all": False,
    },
    {
        "name": "S16 票据签发日志失去节流",
        "file": "maas.go",
        "old": '\tif now.Sub(maasMintLastLog) < maasMintLogInterval {',
        "new": '\tif false {',
        "expect": "TestLogMaasTicketMint_Throttled",
        "all": False,
    },
    {
        "name": "S17 亲和头退回每请求随机",
        "file": "main.go",
        "old": '\treq.Header.Set("x-mm-client-request-id", aff.requestID)',
        "new": '\treq.Header.Set("x-mm-client-request-id", newMaasRequestID())',
        "expect": "TestApplyMaasHeaders_AffinityHeadersAreSessionScoped",
        "all": False,
    },
    {
        "name": "S18 request-set 缺失",
        "file": "main.go",
        "old": '\tif aff.requestSet != "" {\n\t\treq.Header.Set("x-mm-request-set", aff.requestSet)\n\t}\n',
        "new": '',
        "expect": "TestApplyMaasHeaders_AffinityHeadersAreSessionScoped",
        "all": False,
    },
    {
        "name": "S19 traceparent 的 trace 段退回每请求随机",
        "file": "maas.go",
        "old": '\ttraceID := hex.EncodeToString(sum[:])[:32]',
        "new": '\ttraceID := newMaasTraceID()\n\t_ = sum',
        "expect": "TestApplyMaasHeaders_AffinityHeadersAreSessionScoped",
        "all": False,
    },
    {
        "name": "S20 会话粘性不再检查在跑软上限",
        "file": "session_auth.go",
        "old": '\t\t\t\tif current < sessionAccountInflightSoftCap {',
        "new": '\t\t\t\tif true {',
        "expect": "TestPickSessionAuth_RebindsWhenOverSoftCap",
        "all": False,
    },
    {
        "name": "S21 选号退回「第一个候选」不看负载",
        "file": "account_load.go",
        "old": '\t\tn := accountLoadOf(c.ID)\n\t\tif bestID == "" || n < bestLoad {',
        "new": '\t\tn := accountLoadOf(c.ID)\n\t\tif bestID == "" || n < bestLoad || true {',
        "expect": "TestPickLeastLoaded_PrefersIdleKeepsOrder",
        "all": False,
    },
    {
        "name": "S22 单账号在飞软上限放大到 100",
        "file": "account_load.go",
        "old": 'const sessionAccountInflightSoftCap = 2',
        "new": 'const sessionAccountInflightSoftCap = 100',
        "expect": "TestPickSessionAuth_RebindsWhenOverSoftCap",
        "all": False,
    },
    {
        "name": "S23 兜底 base_url 退回实测 401 的 legacy 入口",
        "file": "maas.go",
        "old": 'const maasDefaultBaseURL = "https://api9.qwenwork.cn"',
        "new": 'const maasDefaultBaseURL = "https://maas.xiaoluozi.cn"',
        "expect": "TestMaasDefaultBaseURLMatchesOfficialClient",
        "all": False,
    },
    {
        "name": "S24 不发 x-mm-device-ua",
        "file": "main.go",
        "old": '\treq.Header.Set("x-mm-device-ua", maasDeviceUA())\n',
        "new": '',
        "expect": "TestApplyMaasHeaders_SendsDeviceUA",
        "all": False,
    },
    {
        "name": "S25 不采集前缀缓存创建量",
        "file": "usage.go",
        "old": '\tif v := nested("prompt_tokens_details", "cache_creation_input_tokens"); v > 0 {\n\t\td.CacheCreationTokens = v\n\t}\n',
        "new": '',
        "expect": "TestUsageDetailFromMap_ReadsCacheCreation",
        "all": False,
    },
    {
        "name": "S26 首帧前形态留痕不限帧数",
        "file": "usage.go",
        "old": '\tif len(c.prelude) >= ssePreludeLimit {',
        "new": '\tif false {',
        "expect": "TestSseUsageCollector_PreludeLimited",
        "all": False,
    },
    {
        "name": "S27 响应头白名单放宽到凭据头",
        "file": "sse_signature.go",
        "old": 'lk == "retry-after" || lk == "content-type" || lk == "server" || lk == "via" {',
        "new": 'lk == "retry-after" || lk == "content-type" || lk == "server" || lk == "via" || lk == "authorization" {',
        "expect": "TestUpstreamHeaderSummary_WhitelistOnly",
        "all": False,
    },
    {
        "name": "S28 裁剪方向反了（从旧往新累计）",
        "file": "context_budget.go",
        "old": '\tfor i := keepTail - 1; i >= head; i-- {',
        "new": '\tfor i := head; i < keepTail; i++ {',
        "expect": "TestTrimMessagesToBudget_KeepsHeadAndTail",
        "all": False,
    },
    {
        "name": "S29 会话键不再基于客户端原文派生",
        "file": "payload.go",
        "old": '\tprep.SessionKey = maasSessionKeyFromPayload(origin)',
        "new": '\tprep.SessionKey = ""',
        "expect": "TestPrepareUpstreamBody_SessionKeyUnaffectedByHardTrim",
        "all": False,
    },
    {
        "name": "S30 命中缓存时不再注入 checkpoint 摘要",
        "file": "context_summary.go",
        "old": '\ttrimmed = append(trimmed, buildCheckpointMessage(summary))\n',
        "new": '',
        "expect": "TestApplyContextSummary_CacheHit_InjectsCheckpoint",
        "all": False,
    },
    {
        "name": "S31 历史被改写后仍注入旧摘要",
        "file": "context_summary.go",
        "old": '\t\tif contextSummaryPrefixHash(norm, entry.covered) == entry.headHash {',
        "new": '\t\tif true {',
        "expect": "TestApplyContextSummary_StaleHistory_DiscardsSummary",
        "all": False,
    },
    {
        "name": "S32 滚动摘要丢掉 <prior-summary> 分支",
        "file": "context_summary.go",
        "old": '\t\tsb.WriteString("\\n\\nHere is the summary of the conversation before the <conversation> above:\\n\\n<prior-summary>\\n")\n',
        "new": '',
        "expect": "TestBuildContextSummaryPrompt_RollingSummary",
        "all": False,
    },
    {
        "name": "S33 摘要序列化丢掉官方标记",
        "file": "context_summary.go",
        "old": '\t\t\tparts = append(parts, "[Tool result]: "+truncateSummaryText(content))',
        "new": '\t\t\tparts = append(parts, truncateSummaryText(content))',
        "expect": "TestSerializeSummaryBlock_Markers",
        "all": False,
    },
    {
        "name": "S34 摘要缓存不再淘汰超限条目",
        "file": "context_summary.go",
        "old": '\tif len(contextSummaryCache) > contextSummaryCacheMaxEntries {',
        "new": '\tif false {',
        "expect": "TestStoreContextSummary_EvictsOldest",
        "all": False,
    },
    {
        "name": "S35 摘要前缀签名不再区分内容",
        "file": "context_summary.go",
        "old": '\tfor i := 0; i < n; i++ {\n\t\tm := messages[i]',
        "new": '\tfor i := 0; i < 0; i++ {\n\t\tm := messages[i]',
        "expect": "TestContextSummaryPrefixHash",
        "all": False,
    },
    {
        "name": "S36 执行入口不再登记后台摘要任务",
        "file": "main.go",
        "old": '\tmaybeEnqueueContextSummary(prep, sa, upstreamModel, maasSessionKey)',
        "new": '',
        "expect": "TestExecutors_EnqueueContextSummary",
        "all": False,
    },
]


def read(path):
    with open(path, "r", encoding="utf-8", newline="") as f:
        return f.read()


def write(path, text):
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)


def run_gate():
    proc = subprocess.run(
        [sys.executable, os.path.join(ROOT, "scripts", "cgo-shim-build.py"), "qwenwork"],
        cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        env=dict(os.environ, GOMAXPROCS="1", GOGC="20", GOFLAGS="-p=1"),
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


ENV_MARKERS = (
    "paging file is too small",
    "out of memory",
    "cannot allocate memory",
    "fork/exec",
)


def is_env_failure(out):
    """门禁失败若来自宿主资源耗尽（编译 OOM / 分页文件不足），不是被测代码的回归。

    Windows 上编译器进程被 OOM 杀掉时 go 会把 runtime.throw 的栈打到输出里；
    这类噪声会让「变异没被断言抓住」被误判成 NO-HIT，必须单独识别并重试。
    """
    low = out.lower()
    return any(m in low for m in ENV_MARKERS)


def main():
    # 控制台默认 GBK：Go 的 panic 栈里可能出现非 GBK 字符，直接 print 会抛
    # UnicodeEncodeError 把整个哨兵脚本打断（实测发生过一次）。
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass
    only = sys.argv[1:]
    mutations = [m for m in MUTATIONS if not only or any(m["name"].startswith(p) for p in only)]
    if not mutations:
        print("NO_MUTATION_SELECTED")
        return 1
    if os.path.isdir(BACKUP):
        shutil.rmtree(BACKUP)
    os.makedirs(BACKUP)
    for m in mutations:
        shutil.copy2(os.path.join(PKG, m["file"]), os.path.join(BACKUP, m["file"]))

    print("=== baseline ===")
    code, out = run_gate()
    if code != 0:
        print(out[-4000:])
        print("BENCHELINE_FAILED")
        return 1
    print("baseline all green")

    failures = []
    try:
        for m in mutations:
            src = os.path.join(PKG, m["file"])
            original = read(src)
            nl = "\r\n" if "\r\n" in original else "\n"
            old = m["old"].replace("\n", nl)
            new = m["new"].replace("\n", nl)
            if old not in original:
                print("[SKIP] %s: anchor not found" % m["name"])
                failures.append(m["name"] + " (anchor)")
                continue
            mutated = original.replace(old, new) if m.get("all") else original.replace(old, new, 1)
            write(src, mutated)
            try:
                code, out = run_gate()
                if code != 0 and is_env_failure(out):
                    # 宿主资源耗尽（Windows 分页文件极小 ⇒ 提交额度被打满时
                    # 编译器被系统杀掉）：等提交额度回落后重试，仍失败则判环境问题。
                    time.sleep(20)
                    code, out = run_gate()
                    if code != 0 and is_env_failure(out):
                        print("[ENV-FAILED] %s: 宿主资源耗尽，编译器被 OOM 杀掉" % m["name"])
                        write(src, original)
                        shutil.rmtree(BACKUP, ignore_errors=True)
                        return 2
            finally:
                write(src, original)
            hit = m["expect"] in out and code != 0
            print("[%s] %s -> expect %s" % ("FAIL-HIT" if hit else "NO-HIT", m["name"], m["expect"]))
            if not hit:
                failures.append(m["name"])
                print("\n".join(out.strip().splitlines()[-25:]))

        code, out = run_gate()
        print("=== restore recheck ===", "all green" if code == 0 else "STILL FAILING")
        if code != 0:
            print("\n".join(out.strip().splitlines()[-40:]))
            failures.append("restore")
    finally:
        shutil.rmtree(BACKUP, ignore_errors=True)

    if failures:
        print("SENTINEL_FAILURES:", failures)
        return 1
    print("SENTINEL_ALL_OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
