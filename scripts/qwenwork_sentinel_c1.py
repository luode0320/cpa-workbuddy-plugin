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
        "old": '\tif sess != nil && strings.TrimSpace(sess.SessionID) != "" {\n'
               '\t\treq.Header.Set("x-session-id", sess.SessionID)\n'
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
        "old": 'maasSessionKey := maasSessionKeyFromPayload(bodyObj)',
        "new": 'maasSessionKey := ""\n\t_ = bodyObj',
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
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def main():
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
        code, out = run_gate()
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

    shutil.rmtree(BACKUP, ignore_errors=True)
    if failures:
        print("SENTINEL_FAILURES:", failures)
        return 1
    print("SENTINEL_ALL_OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
