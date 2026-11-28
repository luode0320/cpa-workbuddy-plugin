#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qwenwork 改动文件的静态格式检查：gofmt（LF 副本）、BOM、尾随空白、换行一致性。"""
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = r"F:\cpa-plugin"
PKG = os.path.join(ROOT, "qwenwork")

FILES = [
    "session_auth.go", "payload.go", "main.go", "maas.go", "stream.go",
    "failover_retry.go", "accountFailover.go", "policy.go", "slow_account.go",
    "host_bridge.go", "active_ping.go",
    "slow_account_test.go", "max_tokens_test.go", "session_seed_test.go",
    "maas_headers_test.go", "queue_classification_test.go", "ttft_measure_test.go",
    "host_callback_test.go",
]


def main():
    problems = []
    tmp = tempfile.mkdtemp(prefix="qwenwork-fmt-")
    for name in FILES:
        src = os.path.join(PKG, name)
        raw = open(src, "rb").read()
        if raw.startswith(b"\xef\xbb\xbf"):
            problems.append("%s: UTF-8 BOM" % name)
        text = raw.decode("utf-8")
        normalized = text.replace("\r\n", "\n")
        for i, line in enumerate(normalized.split("\n"), 1):
            if line != line.rstrip(" \t"):
                problems.append("%s:%d: 尾随空白" % (name, i))
                break
        crlf = text.count("\r\n")
        lf = text.count("\n") - crlf
        lone = text.count("\r") - crlf
        if lone:
            problems.append("%s: 存在 %d 个孤立 CR" % (name, lone))
        # 复制成 LF 副本后跑 gofmt
        dst = os.path.join(tmp, name)
        with open(dst, "wb") as f:
            f.write(raw.replace(b"\r\n", b"\n"))
    proc = subprocess.run(["gofmt", "-l", tmp], capture_output=True, text=True,
                          encoding="utf-8", errors="replace")
    out = (proc.stdout or "").strip()
    if out:
        for line in out.splitlines():
            problems.append("gofmt: %s" % os.path.basename(line))
    shutil.rmtree(tmp, ignore_errors=True)

    if problems:
        print("FORMAT_PROBLEMS:")
        for p in problems:
            print("  " + p)
        return 1
    print("FORMAT_OK (%d files): no BOM, no trailing whitespace, no lone CR, gofmt clean" % len(FILES))
    return 0


if __name__ == "__main__":
    sys.exit(main())
