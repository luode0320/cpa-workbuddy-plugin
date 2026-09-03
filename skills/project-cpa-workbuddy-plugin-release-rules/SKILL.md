---
name: project-cpa-workbuddy-plugin-release-rules
description: 当需要发布 cpa-workbuddy-plugin 仓库任意插件新版本（workbuddy-provider / qoderwork-provider / traework-provider / workbuddy-token-usage 四插件），或走完「修复→发布→生产验证」闭环（发布后对生产真实入口 https://cpa.luode.vip/v1 做行为验收，如流式 qwen3.8-max 长推理、用生产日志 stream_id 判定成败）时触发：版本 bump、commit、push、dispatch CI（必须带 version 输入）、下载 assets、更新 registry、远端验证、生产 plugin-store 部署、生产行为验收的一整套链路，含 GitHub 上行大流量稳定阻断时经生产服务器 SOCKS 隧道绕过。负责发布执行与门禁；本地逻辑验证用 cgo-plugin-isolated-test（cgo-shim-build.py），本 skill 只引用它，不重复实现。
---

# cpa-workbuddy-plugin 插件发布链路

## Skill 作用与适用场景

- 仓库四插件：`workbuddy-provider`（主，版本 0.14.x）、`qoderwork-provider`（0.9.x，已全量对齐 workbuddy 架构）、`traework-provider`（0.1.x，2026-08-28 首发布）、`workbuddy-token-usage`（0.2.x）
- 覆盖从「bump 版本」到「远端验证 ALL PASS + 生产服务器部署」的完整发布链路（0.12.x → 0.14.16 多次跑通，2026-08-29 一次会话内完成 workbuddy 0.14.16 / qoderwork 0.9.6 / traework 0.1.9 三插件并行发布闭环）
- **2026-09-01 起扩展「修复→发布→生产验证」闭环**（traework 0.1.27/0.1.28 完整跑通）：发布不是终点，必须对生产真实入口做行为验收，失败则携新证据继续修复再发（见 Step 14-16）
- 本地验证（cgo-shim / 单测）职责在 `cgo-plugin-isolated-test`，本 skill 只规定「何时、以什么环境验证」
- 发布前代码逻辑正确性由其他规则保证；本 skill 管「怎么把已验证的代码发出去、部署到生产、并在生产真实验收」

## 自动触发信号

- 用户说「发布」「发版」「升级到 X.Y.Z」「发一个新版本」
- 改动已就绪（fix/feat 已本地验证）需要走完 commit→push→CI→assets→registry 全链
- 新版本号已 bump 但远端 registry 还是旧版，需要补齐剩余链路
- 用户提出「修复→发布→生产验证」闭环（如「修复...并默认走修复→发布→生产验证闭环，失败继续修复」）：发布后必须续走生产行为验收（Step 14-16），不验收不算完成

## 进入后先做什么

1. **摸清工作树**：`git status --short` 全量看；区分「本次要发布的改动」与「并行/无关改动」（本项目常见并行 40x 重试等未提交改动，**严禁混入发布**）
2. **确认远端基线**：`git log --oneline -5`，记住远端 main 的 HEAD（push 成功后 diff 起点）
3. **确定版本号**：patch 级（bugfix/UX 微调）→ 0.12.x 递增；minor（行为/API 变更）→ 0.13.0。CHANGELOG 顶部是历史 Breaking Change 记录，格式 `## X.Y.Z`
4. **识别混合文件**：`git diff <file>` 检查 main.go / usage_config.go 等是否同时含并行改动 → 决定走「正常发布」还是「分离发布」

## 默认执行流程（13 步，不可跳步）

### Step 1 · bump 插件版本两处（registry 延迟到 Step 11 原子更新）

- `workbuddy/VERSION`（或其他插件目录 VERSION）：`0.12.2` → 新版本
- `workbuddy/main.go`：`var version = "0.12.2"` → 新版本（**注意：main.go 常是混合文件，见 Step 3**）
- **本步禁止提前修改 `registry.json.version`**。registry 的 `version` 与 `install.artifacts` 必须在 Step 11 由 `publish-assets.py` 同一次改动原子更新。提前 bump 并 push 会产生“新 version + 旧 artifacts”远端中间态，生产 store install 可能把旧二进制按新版本文件名落盘（0.1.13 实锤）。

### Step 2 · CHANGELOG 条目

`workbuddy/CHANGELOG.md` 顶部新增 `## X.Y.Z` 章节：Fix/Feat 标题 + 3-5 条要点 + 涉及文件清单。有 Breaking Change 用显式章节（参考 0.12.0 写法）。

### Step 3 · 混合文件分离（关键！）

当 main.go / usage_config.go / stream.go 等同时含并行改动（其他 AI 工具遗留）时，**不能整体 git add**。流程：

```bash
mkdir -p /tmp/cpa-0xxx
# ① 备份：混合文件（tracked 改动）+ 所有并行 untracked 文件
cp workbuddy/main.go workbuddy/usage_config.go workbuddy/accountFailover.go \
   workbuddy/accountFailover_test.go workbuddy/stream.go /tmp/cpa-0xxx/
mv workbuddy/failover_retry.go workbuddy/failover_retry_test.go \
   workbuddy/retry_config.go workbuddy/retry_config_test.go /tmp/cpa-0xxx/
# ② 恢复 HEAD（tracked 的用 checkout，untracked 的已被 mv 走）
git checkout -- workbuddy/main.go workbuddy/usage_config.go workbuddy/accountFailover.go \
   workbuddy/accountFailover_test.go workbuddy/stream.go
# ③ 重做自己的改动（version 行、requestPreserveTick 等）
# ④ 确认工作区只剩自己要发布的文件
git status --short workbuddy/
```

⚠️ **并行改动横跨多文件是整体**：只 checkout 部分文件会让剩余 untracked 测试引用缺失符号 → cgo-shim FAIL（实测：TestRetryOn4xxConfig_LoadsViaConfigure）。untracked 的并行文件必须一起 mv 走。

### Step 4 · 验证（模拟 CI 构建环境）

CI 构建的是 **push 的 commit**（= HEAD + 本次发布的文件），不是工作树。所以分离发布时必须先移走所有并行文件再验证：

```bash
python scripts/cgo-shim-build.py workbuddy   # 必须全绿
```

- 全绿 ≠ 新测试进编译 → 用哨兵法确认（见 cgo-plugin-isolated-test）
- 成功后 shim 目录会被自动删（残留空壳 `rmdir` 逐个删）

### Step 5 · 精确 add + 提交

```bash
git add registry.json workbuddy/VERSION workbuddy/main.go workbuddy/panel.html ...  # 显式列自己文件
git diff --cached --name-only
# 反向确认无并行文件混入：
git diff --cached --name-only | grep -iE "failover|retry|stream|account" || echo "CLEAN"
git commit -m "fix(watchdog): ..."   # 或 feat / chore(release)
```

### Step 6 · 恢复并行改动 + 验证

```bash
cp /tmp/cpa-0xxx/main.go /tmp/cpa-0xxx/usage_config.go ... workbuddy/
mv /tmp/cpa-0xxx/failover_retry.go ... workbuddy/
git status --short | grep -cE "^ M|^\?\?"   # 数量应等于发布前的并行改动条数
git diff workbuddy/main.go workbuddy/usage_config.go | grep -v "^warning:"  # 应只剩并行内容
```

### Step 7 · push（SSH，直接推）

```bash
git push origin main
```

- origin 是 SSH remote（ssh.github.com:443），SSH key 已认证，**直接 push 即可**（2026-08-23 实测）。
- 旧版 HTTPS + GIT_ASKPASS 方案已废弃；仅在 SSH 失效（网络代理切换等）时才临时回退，askpass 路径必须 Windows 风格 `C:\...` 且用完即删。

### Step 8 · dispatch CI + 轮询

```python
import json, time, urllib.request, urllib.error, pathlib
TOKEN = pathlib.Path('C:/Users/luode/.github/token').read_text().strip()
API = 'https://api.github.com/repos/luode0320/cpa-workbuddy-plugin'
def call(method, url, body=None, tries=5):
    for i in range(tries):
        try:
            req = urllib.request.Request(url, method=method)
            req.add_header('Authorization', f'Bearer {TOKEN}')
            req.add_header('Accept', 'application/vnd.github+json')
            req.add_header('X-GitHub-Api-Version', '2022-11-28')
            data = json.dumps(body).encode() if body is not None else None
            if data: req.add_header('Content-Type', 'application/json')
            with urllib.request.urlopen(req, data=data, timeout=30) as r:
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()
        except Exception as e:
            print(f'  retry {i+1}: {e}', flush=True); time.sleep(2 ** i)
    return -1, ''
st, body = call('POST', f'{API}/actions/workflows/build.yml/dispatches',
    {'ref':'main', 'inputs':{'plugin':'workbuddy-provider', 'version':'0.14.16'}})
print('dispatch:', st)                     # 期望 204
time.sleep(3)
st, body = call('GET', f'{API}/actions/runs?event=workflow_dispatch&per_page=1')
run = json.loads(body).get('workflow_runs', [{}])[0]
print('run:', run.get('id'), '| head:', (run.get('head_sha') or '')[:7], '| status:', run.get('status'))
# ⚠️ 必须确认 head == 自己刚 push 的 commit（避免触发旧 run）
# ⚠️⚠️ inputs 必须同时含 plugin（provider id）和 version —— 缺 version 时 workflow 照样 success
#    但 Release job 直接 skipped：不建 tag、不建 Release、release-assets 下载 404（0.14.16 首发实测）
# ⚠️ plugin 必须传 provider id（如 traework-provider），传源码目录名 traework → 422
```

轮询（**15 分钟超时**，构建一般 3.5-5.5 分钟，偶发 queued 6 分钟+）：

```python
RUN = 32585681475   # 上一步打印的 run id
deadline = time.time() + 900
while time.time() < deadline:
    run = call('GET', f'{API}/actions/runs/{RUN}')[1]
    run = json.loads(run)
    status, concl = run.get('status'), run.get('conclusion')
    print(f'{time.strftime("%H:%M:%S")} status={status} conclusion={concl}', flush=True)
    if status == 'completed': break
    time.sleep(20)
if status != 'completed': raise SystemExit('TIMEOUT')
assert concl == 'success', 'CI FAILED'
```

### Step 9 · 下载 assets + 校验

```bash
python scripts/download-release-assets.py 0.14.17 workbuddy-provider
# ⚠️ 参数顺序与 publish-assets.py 相反：download 是 <version> [plugin]，publish 是 <plugin> <version>
# 传反 → tag 拼成 "0.14.17-vworkbuddy-provider" → Release API 404（易误判为资产未建/CDN 延迟）
```

产物在 `release-assets/workbuddy-provider-0.14.17/`：7 个 zip（darwin amd64/arm64、freebsd amd64、linux amd64/arm64、windows amd64/arm64）+ checksums.txt。

### Step 10 · 提交 assets + push（0.9.7 教训：必须先 push assets 再 publish）

```bash
git add release-assets/workbuddy-provider-0.14.16/
git commit -m "chore(release): add workbuddy-provider 0.14.16 release assets (<描述>)"
git push origin main   # 同 Step 7，SSH 直推
```

### Step 11 · publish registry + 提交 + push

```bash
python scripts/publish-assets.py workbuddy-provider 0.14.16
git diff registry.json | grep -v "^warning:"   # 确认只改 workbuddy-provider 的 artifacts
git add registry.json
git commit -m "chore(registry): publish workbuddy-provider 0.14.16 (<描述>)"
git push origin main   # 同 Step 7
```

### Step 12 · 远端验证 + 清理

```python
import json, urllib.request, hashlib
RAW='https://raw.githubusercontent.com/luode0320/cpa-workbuddy-plugin/main/registry.json'
reg=json.loads(urllib.request.urlopen(RAW, timeout=30).read().decode())
e=next(p for p in reg['plugins'] if p['id']=='workbuddy-provider')
print('version:', e['version'])
arts=e['install']['artifacts']; ok=True
for a in arts:
    d=urllib.request.urlopen(a['url'], timeout=60).read()
    sha=hashlib.sha256(d).hexdigest()
    match=(len(d)==a['size']) and (sha==a['sha256'])
    print(f"  {a['goos']}/{a['goarch']}: size={len(d)}/{a['size']} sha={'OK' if sha==a['sha256'] else 'MISMATCH'}")
    ok=ok and match
raw=json.dumps(reg)
for old in ['0.14.15','0.14.14','0.13.1','0.13.0']:   # 该插件除当前版本外的近期历史版本
    if old in raw: print(f'RESIDUE: {old}'); ok=False
print('FINAL:', 'ALL PASS' if ok else 'FAIL')
```

⚠️ raw URL 验证对刚 push 的文件有 CDN 缓存延迟（短暂 404 后自愈），404 需等待 1-2 分钟重试。

### Step 13 · 生产服务器部署（plugin-store install API）

> **授权说明（2026-08-31）**：生产部署是完整发布链路的固有收尾步骤，默认已获授权，agent 不得以"需要额外授权"为由跳过。只要用户在本轮显式提出"发布 / 部署 / 上生产 / 更新生产"等发布意图，或用户此前已明确"不需要授权，默认都是授权的"，Step 13 就应直接执行并验证到位，直到生产插件版本与 registry 对齐（`active_version` = 目标版本、账号/面板接口 200），无需再单独询问。跳过生产部署导致"仓库最新但生产仍旧版"属发布未完成。

```bash
# 服务器本机（docker host 网络，端口 8317）；MANAGEMENT_KEY 来自服务器 config 的 remote-management.secret-key
curl -s -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v0/management/plugin-store/workbuddy-provider/install?version=0.14.16"
```

- **这是热部署唯一可靠路径**（2026-08-29 实测）：
  - `docker cp` 新 .so 进容器 → **不触发**插件加载（进程不重扫）
  - `PUT /v0/management/plugins/<p>/config` 写同内容配置 → **不触发** fsnotify 重载
  - 只有 plugin-store install 会下载 assets → 落盘 → 热重载（面板日志可见加载时间戳）
- **install 后必须校验落盘二进制内容，不可信任响应里的 version/path**：registry 外层 version 已 bump、artifacts 尚未 publish 的 CDN 中间态，会把旧二进制按新版本文件名落盘（0.1.13 实锤：`traework-provider-v0.1.13.so` 的 sha256 与 0.1.12 完全一致，重启后 registered 仍为 0.1.12）。至少执行以下一项，推荐两项都做：
  - `sha256sum <生产落盘.so>` 与本地 release zip 内 `.so` 的 sha256 比对，必须一致；
  - 重启宿主后核对日志 `plugin loaded` 与 `plugin registered` 的版本均等于目标版本。
  - 不一致时：等待 raw CDN 完全传播后重新 `plugin-store install?version=<目标版本>`，再次校验哈希，一致后重启宿主加载。
- 部署后验证：`GET /v0/management/plugins/<p>/accounts`（或面板资源 URL）返回 200 + 预期数据；面板页 `GET /v0/resource/plugins/<p>/panel` 200
- 最终态核对：生产各插件版本应与 registry.json 完全一致

```bash
rm -rf /tmp/cpa-0xxx cpa-shim-* 2>/dev/null
git log --oneline -5 && git status --short | grep -cE "^ M|^\?\?"   # 并行改动条数应不变
```

### Step 14 · 生产行为验收（真实入口，非冒烟）

> **授权说明（2026-09-01，traework 0.1.27/0.1.28 闭环实跑）**：用户显式走「修复→发布→生产验证」闭环时，生产直连验证已获授权；只有「部署冒烟」（install 后版本/hash/面板 200）不算验收完成，必须对生产真实入口做**行为验收**。生产行为验收属于用户显式授权的闭环步骤，agent 不得以"生产禁区"为由跳过；但凭据必须全程脱敏（见 Step 15）。

- **生产入口**：`https://cpa.luode.vip/v1`（OpenAI 兼容，`Authorization: Bearer <API key>`）。API key 与 management key 相同，从生产容器 config 的 `remote-management.secret-key` 提取。
- **验收目标模型**：`qwen3.8-max`（长推理场景，曾发生「生成中途停止 / 伪完成短答 / 240s 无字节宿主 499」三类症状）。
- **验收样本**：流式 `POST /v1/responses`（或 `/v1/chat/completions`），提示词要求至少 8 个编号章节 + 随机结尾 nonce（如 `END_NONCE`）强制长回答；同 session 连续多轮覆盖多账号，并留稳定性窗口（每版本建议 ≤20 个长请求，避免无限消耗生产配额）。
- **成功指纹**：流式响应完整返回、结尾含 nonce、无 240s 超时 / 499 / 伪完成 / 换号中断。关联生产日志 `stream_id` 时序：`exec stream async scheduled: ... stream_id=N` → `exec stream async done: ... stream_id=N attempt=1 chunks=M` 为健康；若出现 `pseudo retry` / `pool exhausted` / `attempt>1` / 断流补 `length`，需结合响应与日志证据判定是否仍属缺陷。
- **失败处理**：任一未恢复短结束 / 超时 / 伪完成 → 冻结该逻辑请求证据（stream_id + 时间窗 + 脱敏响应摘要），退回修复，再走「修复→发布→生产验证」闭环；不得用一次偶然长输出宣告完成。

### Step 15 · 凭据纪律与清理（强制）

- **API key / management key 只允许写临时文件**（如 `mktemp` 或 `printf` 到 `.tmp_verify/`），使用后立即删除；**禁止在日志、错误信息、测试报告、终端输出、Agent 回复、会话交接、执行失败案例、自动知识摘要、记忆与 skill 文档中回显凭据原值**——文档只存脱敏标识（字节数 / 指纹）。
- 提取 management key（**必须宿主机管道串联，不要嵌套 `docker exec sh -c "grep | grep"`**，嵌套返回空）：
  ```bash
  KEY=$(docker exec cli-proxy-api grep -A20 'remote-management' /CLIProxyAPI/config.yaml \
    | grep 'secret-key' | head -1 | sed 's/.*secret-key:[[:space:]]*//' | tr -d '"' | tr -d '[:space:]')
  ```
- 生产验证后清理：临时 key / 请求 / 响应 / 脚本文件全部删除，只保留脱敏统计（请求数、stream_id、成败、耗时、章节/nonce 是否完整）。

### Step 16 · GitHub 上行大流量阻断绕过（SOCKS 隧道）

> **背景（2026-09-02 实测）**：本机网络对 GitHub **上行大流量稳定阻断**——`git push`（send-pack 阶段断流）、上传 5MB 对象、上传 2MB API body 均被断（curl 52/55/65 + `SSL UNEXPECTED_EOF`）；下行 / `ls-remote` / 小对象 push 正常。遇到「本地全绿但 push / dispatch / 上传总是中途断」优先怀疑此场景，不是凭据问题。

**绕过：经生产服务器 SSH 动态 SOCKS 隧道**（生产服务器出网正常）：

```bash
# 开隧道（后台，不用时 kill）
ssh -i ~/.ssh/id_ed25519_cpa-server -p 18998 -N -D 127.0.0.1:1080 root@45.207.222.65 &
```

**git push 经代理**（只对本次命令生效）：

```bash
git -c http.proxy=socks5h://127.0.0.1:1080 push origin main
```

- **curl 原生支持 socks5h**：`curl --socks5-hostname 127.0.0.1:1080 <url>`（dispatch / 下载 raw 资产 / 远端验证都可用）。
- **Python urllib 不认 socks5**：`ProxyHandler` 收到 socks5 会报 `Remote end closed connection`——需要走代理的 HTTP 调用一律改用 curl，不要用 urllib + SOCKS 代理。
- 用完 `kill <tunnel_pid>` 关闭隧道；临时脚本 / 临时 key 一并清理。

## 关键命令速查

| 操作 | 命令 |
|---|---|
| 版本 bump | Step 1 只改 VERSION + main.go `var version`；Step 11 由 publish-assets.py 原子更新 registry version + artifacts |
| 本地验证 | `python scripts/cgo-shim-build.py <plugin>`（需先移走并行文件模拟 CI） |
| push | `git push origin main`（SSH remote，直接推；HTTPS+askpass 仅作 SSH 失效时的回退） |
| 下载资产 | `python scripts/download-release-assets.py <VER> <provider-id>`（⚠️ 与 publish 参数顺序相反） |
| 发布 registry | `python scripts/publish-assets.py <PLUGIN> <VER>` |
| 生产部署 | `curl -X POST -H "Authorization: Bearer $KEY" "http://127.0.0.1:8317/v0/management/plugin-store/<provider-id>/install?version=<VER>"` |
| 生产行为验收 | `POST https://cpa.luode.vip/v1/responses`（或 `/v1/chat/completions`），流式 `qwen3.8-max` 长推理 + 关联生产日志 stream_id（Step 14） |
| 网络阻断绕过 | `ssh -i ~/.ssh/id_ed25519_cpa-server -p 18998 -N -D 127.0.0.1:1080 root@45.207.222.65` + git `-c http.proxy=socks5h://127.0.0.1:1080`；HTTP 走 `curl --socks5-hostname`（Step 16） |
| registry 校验 | `python scripts/validate-registry.py` |

## 踩坑清单（全部实测）

1. **（遗留回退）GIT_ASKPASS 路径**：SSH 已是默认 push 方式（见 Step 7）；仅 SSH 失效回退 HTTPS 时使用，且必须 Windows 风格 `C:\...`；`/c/...` 报 cannot spawn（git.exe 不认 POSIX 路径）。askpass 用完即删。
2. **0.9.7 教训**：assets commit **必须 push** 后才能 publish-assets.py——publish 后 registry 指向 raw.githubusercontent 的 URL，不 push 则远端 404。
3. **分离发布**：并行改动横跨多文件是整体；tracked 用 checkout 恢复、untracked 必须 mv 走；验证要在「纯 HEAD+自己文件」环境跑，否则混入并行测试符号导致 FAIL。
4. **版本三处最终一致，但禁止一次提前 bump**：Step 1 只改 VERSION/main.go；Step 11 由 publish-assets.py 同时更新 registry.version + artifacts。最终三处必须一致，过程中不得把“新 registry version + 旧 artifacts”推到远端。
5. **dispatch 偶发 SSL EOF / exit 35**：api.github.com 瞬时窗口，curl/urllib 都可能失败 → 指数退避重试（tries=5）。
6. **registry.json 结构**：`plugins` 是 **list**（不是 dict），条目用 `id` 匹配；artifacts 在 `install.artifacts`。
7. **零残留**：旧版本号字符串不得出现在 registry.json（历史 artifacts 会被 publish 替换）。
8. **CI 构建的是 push 的 commit**，不是工作树 → 本地验证必须模拟 CI 环境（移走并行文件），否则「本地绿、CI 红」。
9. **dispatch 后核对 run head_sha** == 自己 push 的 commit。
10. **cgo-shim 成功即删 shim 目录**：想跑 `go test -v` 哨兵需手动建持久 shim；残留空壳 `rmdir` 逐个删（rm -rf 可能卡 3 分钟+）。
11. **gofmt CRLF 假象**：历史 tracked 文件 CRLF 时 `gofmt -l` 全量列出；只确认自己新写/新改的文件不在列表。
12. **qoderwork 已全量对齐 workbuddy（2026-08-28 完成 1-9 同步）**：此前是老版（publishUsage 8 参数、无 preserve watchdog、无 session_auth），现仅存少量 HEAD 基线差异；同步改动仍只能逐函数适配，不能整体覆盖文件。
13. **Windows rm -rf cpa-shim-\***：go 子进程/杀软持句柄会挂起，`ls -d cpa-shim-*` 确认后 rmdir 逐删。
14. ~~**qoderwork 下载脚本硬编码**~~（已修复）：`download-release-assets.py` 已参数化，四插件通用；但参数顺序是 `<version> [plugin]`，与 publish-assets.py 的 `<plugin> <version>` **互为相反**（0.14.17 实测传反 → 404）。
15. **验证一律前台跑**：cgo-shim 验证用 `run_in_background` + `| tail` 时 qoderwork 曾挂 23 分钟无输出（后台 bash 环境/残留 shim 目录问题），前台跑 ~6s 秒过；挂起先 TaskStop 再前台重跑，不要死等。
16. **GitHub API 列表缓存延迟**：push/dispatch 后 `actions/runs` 与 `releases` 列表可能仍显示旧条目，须按 `head_sha` / `releases/tags/<tag>` 精确查询为准。
17. **分离发布边界**：仅当「工作树存在与发布内容无关的并行 go 改动」时才需要移走分离；纯规则文件（AGENTS.md/PROJECT_*/skills 等非 go 文件）不参与编译，无需分离，直接验证即可（0.13.0 发布内容即并行批本身，未分离一次通过）。
18. **并行会话竞争（0.13.1 实测）**：发布期间其他 AI 会话可能同时改工作树——盘点后、commit 前、dispatch 前各核对一次 `VERSION` / `main.go version` / `git status` 与上次差异；发现版本号/CHANGELOG 被预写、他人已 commit push 时：a) `git show HEAD:<file>` 验证对方 commit 是否包含你的改动（9854d22 带走我 Edit 的 CHANGELOG 但漏了 panel.html）b) dispatch 前查 `actions/runs?head_sha=<你的commit>`，发现他人 run 竞争同一 tag 时先 `POST /actions/runs/<id>/cancel` 取消对方（产物必须是你的 superset 才可取消），保留自己的 run。
19. **rm askpass 路径必须 POSIX**：`rm -f 'C:\Users\...'` 在 Git Bash 中反斜杠被当相对路径 → safe-delete 拦截报错、文件残留；一律 `rm -f /c/Users/luode/.github/git-askpass.sh`。
20. **registry 版本号预写中间态**：他人可能先改 registry 的 version 字段（如 token-usage 0.1.7→0.1.8）但 release/artifacts 未发——`validate-registry.py` 只校验版本格式不校验 URL 与版本匹配；发布前识别此类中间态并告知用户，勿替用户补发未授权插件。
21. **dispatch 必须同时传 plugin + version（0.14.16 首发实测）**：只传 plugin 时 workflow 显示 success 但 Release job 直接 skipped——不建 tag、不建 Release，随后 download-release-assets.py 404（易误判为 CDN 延迟）。补发：重新 dispatch 带 `inputs:{plugin,version}`。
22. **dispatch plugin 参数是 provider id（2026-08-29 实锤）**：build.yml `inputs.plugin` 是 choice 类型，options 仅四插件 id；传源码目录名（traework/workbuddy/token-usage-tracker）→ HTTP 422 + 空 body。目录名映射：workbuddy→workbuddy-provider、qoderwork→qoderwork-provider、token-usage-tracker→workbuddy-token-usage、traework→traework-provider。curl 通道报「无权访问」403 是代理层噪声，别信。
23. **CI run 轮询上限按 15 分钟设计**：出现 in_progress→queued 6 分钟→in_progress（共 12 分钟）的先例；11 分钟超时会误杀，deadline 用 900 秒。
24. **TLS 备用通道**：curl/urllib 对 api.github.com TLS 握手可能间歇性失败（匿名+认证都可能挂）；PowerShell `Invoke-RestMethod` + Tls12 稳定可用。注意 PowerShell 后台任务约 2 分钟被强杀（长轮询用 Bash 后台 + curl）；`Invoke-RestMethod` 显式 `-TimeoutSec 30`。
25. **生产部署唯一路径是 plugin-store install（见 Step 13）**：docker cp .so 不触发加载、PUT config 同内容不触发 fsnotify 重载；面板「发布成功但行为没变」先查服务器实际加载的版本再排查代码。
26. **插件内文件名前缀判断必须用独立常量（authFilePrefix 铁律，traework 0.1.9 根因）**：`providerName+"-"` 派生前缀会带上 `-provider` 后缀（`traework-provider-`），而宿主落盘文件名是 `traework-<uid>.json` → hostAuthList 恒零匹配 → 面板「暂无账号」。各插件目录已有 `authFilePrefix` 常量（`traework-`/`qoderwork-`/`workbuddy-`），新增文件过滤逻辑必须引用它。
27. **发布前 fetch 对齐**：多 AI 会话共享同一物理工作树时，push 前 `git fetch` + 确认 remote main 未前进，避免 rebase 撞车；pull --rebase 被拒时先 commit 自己的 staged 内容再 rebase（"index contains uncommitted changes"）。
28. **store install 报 `direct plugin version not found`（502，毫秒级返回）≠ registry 未发布（0.1.10 实测）**：registry push 后数分钟内，宿主 Go 客户端命中的 raw.githubusercontent CDN 边缘节点可能缓存滞后（同机 curl 反而看到新版——边缘节点不同）。宿主 FetchRegistry 无本地缓存（v7.2.129 源码实锤，install 每次实时拉取）。处置：等几分钟重试即成功；判定顺序=本地 raw 校验 → 服务器侧 curl → 重试。
29. **store install 返回目标 version/path 也可能装入旧二进制（0.1.13 实锤）**：若先在发布 commit 里把 registry 外层 `version` bump 到新值，但 `install.artifacts` 仍指旧版，宿主 CDN 命中该中间态时会把旧 `.so` 保存成新版本文件名；API 仍返回 `status=installed, version=新版本`，具有强欺骗性。本次生产 `traework-provider-v0.1.13.so` 的 sha256 `bd279d...` 与本地 0.1.12 完全一致，且 `strings` 显示内置版本 0.1.12；等待 publish CDN 传播后重装，sha256 才变为正确 0.1.13 的 `759b09...`。**铁律：install 后必须比对落盘二进制 sha256/内置版本，且重启后 loaded/registered 版本一致，不能只信响应 JSON 与文件名。**
30. **生产服务器校验通道（0.1.14 实测，容器与宿主机能力不同）**：`cli-proxy-api` 容器是 host 网络（端口 8317 宿主机可达），但容器内**没有 `curl`、没有 `strings`**，且 `/CLIProxyAPI` 只在容器内存在——宿主机上 `ls /CLIProxyAPI/plugins/...` 直接报 No such file or directory。正确分工：
    - **取 management key**：`KEY=$(docker exec <cid> grep -A20 'remote-management' /CLIProxyAPI/config.yaml | grep 'secret-key' | head -1 | sed 's/.*secret-key:[[:space:]]*//' | tr -d '"' | tr -d '[:space:]')`。**必须用宿主机管道串联，不要写成 `docker exec <cid> sh -c "grep ... | grep ..."` 的多层嵌套**——实测嵌套写法返回空值（key_len=0）。字段确认为 `secret-key`，位于 `remote-management` 之后约第 22 行，`grep -A3` 范围不够会取空。
    - **调 API**：用**宿主机 `curl`** 访问 `http://127.0.0.1:8317`（容器内 curl 不存在，会报 `sh: 1: curl: not found`）。
    - **校验落盘 .so**：必须在**容器内**执行（`docker exec <cid> sh -c "..."`）。`sha256sum` 容器内有；`strings` 没有，用 `grep -a -oE '0\.1\.1[0-9]' <file> | sort -u` 提取内置版本，用 `grep -a -c '<特征串>'` 确认新功能已进二进制。
    - install 响应的 `path` 是**相对路径**（如 `plugins/linux/amd64/traework-provider-v0.1.14.so`），拼接容器前缀 `/CLIProxyAPI/` 才是校验路径。
    - 快速判定热加载是否生效：`GET /v0/management/plugins` 返回列表中该插件的 `version` 已变为目标版本，且 accounts/panel 返回 200，即可不重启宿主（0.1.14 实测 install 后即生效，`restart_required=false`）。
31. **Git Bash 的 `/tmp` 与 Windows 原生 Python 不通（0.14.18 实测）**：`git show HEAD:registry.json > /tmp/reg_head.json` 再交给 Windows 原生 Python（`binaries/python/.../python.exe`）读取会 `FileNotFoundError` —— Git Bash 的 `/tmp` 映射到 MSYS 临时目录，Windows 进程按字面路径找不到。跨「Git Bash 命令 → Windows Python」传文件一律改用 stdin 管道：`git show HEAD:registry.json | PYTHONIOENCODING=utf-8 python -c "import sys,json; a=json.loads(sys.stdin.read()); ..."`。同类隐患：`unzip` 解包目标、任何经 `/tmp` 中转给 Windows 程序的临时文件。
32. **分离发布「备份混合文件」可能残留自己的改动 + 回退版本号（0.1.15 实测）**：发布前工作树的混合文件（config/main/usage 等）已含自己上一轮 Edit 的内容，`cp` 备份后 `git checkout` 重做再 `cp` 恢复，恢复的文件 = HEAD + 自己的改动 + 并行改动。两个隐蔽后果：
    - **自己的改动残留**：`git diff traework/ | grep -c "usage_feed\|configureUsageFeed\|recordUsageFeed"` 非 0 → 并行会话后续 commit 会把你的改动带进去（内容虽同，但污染对方 diff）。恢复后必须逐个核对 `git diff <file>` 只含并行内容。
    - **版本号回退**：并行备份的 main.go `var version` 是发布前旧值（如 0.1.14），恢复后工作树版本回退 → 下次 commit 覆盖发布版本。恢复后 `sed -i 's/var version = "旧版"/var version = "新版"/' traework/main.go` 修正回发布版本。
33. **`GET /v0/management/plugins` 响应是 `{"plugins":[...]}`（非裸数组，0.1.15 实测）**：`json.load(sys.stdin)` 直接遍历报 `AttributeError: 'str' object has no attribute 'get'`，须取 `["plugins"]`；且该列表条目的顶层**没有 `version` 字段（ver=None）**——踩坑 30 中「列表 version 已变为目标版本」的判定不成立，确认热加载版本用 `docker logs cli-proxy-api --since 20m | grep "hot reloaded"` 最直接（`active_version=X.Y.Z retired_version=旧版`）。另 `/v0/management/plugins/<id>` 单插件详情端点不存在（404），别用它。
34. **「修复→发布→生产验证」闭环（2026-09-02，traework 0.1.28 完整跑通）**：用户显式授权该闭环时，发布后必须做生产行为验收才算完成——「部署冒烟」只证明装上了，不证明修好了。本轮 0.1.28 修复根因是**异步流式宿主流桥打开阶段可永久阻塞**（`hostCall` 同步 cgo 无超时 → 客户端 240s 无字节 → 宿主 499 → feed 记「失败（HTTP 200）0/12」）。修复=`hostBridgeOpenTimeout=30s`（goroutine+select 竞速）+ 超时降级插件自有 client 直连 live 流 + `hostBridgeAvailableFn`/`hostStreamOpenFn` 注入点 + `host_stream_timeout_test.go` 哨兵验证法。生产验收=4 次流式 qwen3.8-max 长推理全部 `attempt=1` 完整 done（stream_id 1850/1853/1856/1857），修复前 stream_id=1664 scheduled 后 240s 无日志 + 499 的场景彻底闭环。
35. **生产验证样本与成败指纹（0.1.28 实测）**：流式 `POST /v1/responses` 提示词要求多章节 + 随机 `END_NONCE` 结尾；成功=完整返回 + 含 nonce + 日志 `exec stream async scheduled: ... stream_id=N` → `exec stream async done: ... attempt=1 chunks=M`（无 pseudo retry / pool exhausted 即健康）；失败指纹=`pseudo retry` / `attempt>1` / 断流补 `length` / 240s 无字节。关联生产日志：`docker logs cli-proxy-api --since 30m | grep <stream_id 或时间段>`。
36. **凭据纪律（全链路强制）**：API key / management key 只写临时文件（`printf` 到 `.tmp_verify/`）用后即删；**禁止在日志 / 测试报告 / 终端输出 / Agent 回复 / 会话交接 / 记忆 / skill 文档中回显凭据原值**——文档只存脱敏标识（字节数 / 指纹）。生产验证后临时 key / 请求 / 响应 / 脚本全部清理，只留脱敏统计。
37. **GitHub 上行大流量稳定阻断（2026-09-02 实测）**：本机对 GitHub push / 大对象上传稳定断流（curl 52/55/65 + `SSL UNEXPECTED_EOF`），下行正常 → **用生产服务器 SOCKS 隧道绕过**（`ssh -i ~/.ssh/id_ed25519_cpa-server -p 18998 -N -D 127.0.0.1:1080 root@45.207.222.65`）：git 加 `-c http.proxy=socks5h://127.0.0.1:1080 push`，HTTP 走 `curl --socks5-hostname`。**urllib 不认 socks5**（`Remote end closed connection`）→ 走代理的 HTTP 一律 curl。隧道用完 `kill`。
38. **生产日志 grep 中文回显**：生产日志正文含中文（如「失败（HTTP 200）」），取日志经管道 / 重定向时用 `docker logs ... 2>&1 | grep ...`，终端回显中文乱码不影响判定；判定以 stream_id / attempt / chunks 等 ASCII 字段为准。
39. **`/v1/responses` 与 `/v1/chat/completions` 的 JSON 差异**：responses 流式 body 是 `{"type":"response.output_text.delta","delta":"..."}` + 末尾 `response.completed`，chat/completions 是 `data: {"choices":[...]}` chunks + `[DONE]`；脚本解析前先确认端点与格式，不要混用解析器。
40. **SSH 会话内 curl 偶发返回 000（2026-09-03 实测，traework 0.1.33 部署验收）**：经 SSH 在服务器侧 curl 127.0.0.1:8317（accounts/panel 等任意端点）偶发返回 000（连接层瞬时抖动），不代表服务异常——同一命令立即复测即恢复 200。判定铁律：000 先复测 2-3 次再下结论，不得基于单次 000 回滚或重装。另：生产 feed 文件长期运行会被轮换重建（0.1.33 验收时 22:28 重建为 3203 字节），重建后 grep 不到本插件记录属正常，需主动发轻量流量触发新记录再验证。
41. **本机 SSH push 瞬时断流先复测、后隧道（2026-09-04 实测，traework 0.1.36）**：`git push origin main` 偶发报 `Please make sure you have the correct access rights`（SSH 出站瞬时断，`ls-remote` 同时 `Connection closed by 198.18.0.98`），几分钟内自愈——**判定顺序：ls-remote 复测 2 次 → 仍败才开 SOCKS 隧道**（踩坑 37），不要一败就切隧道。另：Windows git 的 `GIT_ASKPASS` 指向 `.sh` 脚本不生效（git.exe 无法 spawn shell 脚本），push 挂起无输出直至强杀——HTTPS 回退的 askpass 必须用 `.cmd`/`.exe` 形态，或干脆等 SSH 复测恢复直连。
42. **截图圈选删除任务先确认区块归属（2026-09-04 实测，traework 0.1.35 误删）**：用户截图红框 + 「这个删掉」时，红框指向与文字描述可能不一致（0.1.35 实指下方「子系统状态」，被误读为上方「用量汇总」并发布上线）。铁律：动手前用文字向用户复述「删除 X、保留 Y」边界，或与上一版本基线 diff 核对目标区块函数归属；已发布错误时用前向修复新版本（revert 会回到含目标区块的旧版，非用户要的中间态）。

## 权责边界与不负责事项

- 只负责「发布执行 + 生产行为验收」：版本 bump、提交、push、CI、assets、registry、远端验证、生产部署、生产真实入口验收
- 不负责代码正确性验证本身（那是 cgo-plugin-isolated-test + 各开发规则）
- 不负责替代用户决策（版本号语义、是否发布）
- 不把并行/无关改动混入发布（用户明确要求另发时除外，须显式列出文件）
- 发布完成后提醒用户重启宿主（c-shared 插件需 CLIProxyAPI 重启才生效；plugin-store install 多数场景热重载已生效，见 Step 13/踩坑 33）

## 执行通过 / 驳回标准

- 通过：三处版本一致、commit 链清晰（feat/fix → assets → registry）、远端验证 ALL PASS、生产服务器已部署到同版本（或已提示用户部署）、并行改动完好
- 通过：发布内容与工作树其他改动严格隔离（staged 列表人工可核）
- 驳回：跳过任一步骤（尤其 push assets 或远端验证）
- 驳回：版本号三处不一致、registry 有旧版本残留、并行改动丢失
