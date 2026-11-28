# -*- coding: utf-8 -*-
"""Safely set traework-provider config keys.

WHY THIS EXISTS (2026-11-29 事故): the CPA management API's
`PUT /v0/management/plugins/<id>/config` **replaces the whole config object** —
it does NOT merge. Sending only `{stream_direct_forced:false}` wiped every other
key, including `enabled`, which silently took traework-provider out of routing
(observed as `enabled=False effective=False registered=False`).

This script therefore:
  1. GETs the current config,
  2. merges the requested keys on top of it,
  3. PUTs the merged object,
  4. reads back and asserts both the requested keys AND `enabled` survived.

Usage: safe_set_cfg.py key=value [key=value ...]
"""
import json
import subprocess
import sys

KEY = 'Ld@588588'
BASE = 'http://127.0.0.1:8317'
PID = 'traework-provider'

pairs = {}
for arg in sys.argv[1:]:
    if '=' not in arg:
        raise SystemExit('bad arg: %s (want key=value)' % arg)
    k, v = arg.split('=', 1)
    if v.lower() in ('true', 'false'):
        pairs[k] = (v.lower() == 'true')
    else:
        try:
            pairs[k] = int(v)
        except ValueError:
            pairs[k] = v


def curl(method, path, body=None):
    cmd = ['curl', '-s', '-m', '60', '-X', method, '-H', 'X-Management-Key: ' + KEY,
           '-w', '\n%{http_code}', BASE + path]
    if body is not None:
        cmd += ['-H', 'Content-Type: application/json', '-d', json.dumps(body)]
    out = subprocess.run(cmd, capture_output=True, text=True).stdout
    raw, _, code = out.rpartition('\n')
    try:
        parsed = json.loads(raw) if raw.strip() else None
    except Exception:
        parsed = {'_raw': raw[:300]}
    return int(code or 0), parsed


code, cur = curl('GET', '/v0/management/plugins/%s/config' % PID)
if code != 200 or not isinstance(cur, dict):
    raise SystemExit('cannot read current config (http %s)' % code)

# 保留 store 与所有未提及的键，只覆盖调用方指定的键。
merged = dict(cur)
merged.update(pairs)

# 安全阀：任何写入都不允许把插件关掉，除非显式传 enabled=false。
if pairs.get('enabled') is not False and merged.get('enabled') is not True:
    merged['enabled'] = True
    print('SAFETY: forced enabled=true (was not explicitly disabled)')

print('keys to write: %s' % json.dumps(pairs, ensure_ascii=False))
code, body = curl('PUT', '/v0/management/plugins/%s/config' % PID, merged)
print('PUT -> http %s %s' % (code, json.dumps(body, ensure_ascii=False)[:160]))

code, after = curl('GET', '/v0/management/plugins/%s/config' % PID)
eff = {k: v for k, v in (after or {}).items() if k != 'store'}
print('=== effective after ===')
print(json.dumps(eff, ensure_ascii=False, indent=1))

problems = []
for k, v in pairs.items():
    if eff.get(k) != v:
        problems.append('%s: want %r got %r' % (k, v, eff.get(k)))
if pairs.get('enabled') is not False and eff.get('enabled') is not True:
    problems.append('enabled got switched off')
print('VERIFY_%s' % ('OK' if not problems else 'FAILED ' + '; '.join(problems)))
sys.exit(0 if not problems else 1)
