---
name: project-cpa-workbuddy-plugin-release-rules
description: 当需要发布 cpa-workbuddy-plugin 仓库任意插件新版本（workbuddy-provider / qoderwork-provider / traework-provider / workbuddy-token-usage 四插件）时触发：版本 bump、commit、SSH push、dispatch CI（必须带 version 输入）、下载 assets、更新 registry、远端验证、生产服务器 plugin-store 部署的一整套发布链路。负责发布执行与门禁；本地逻辑验证用 cgo-plugin-isolated-test（cgo-shim-build.py），本 skill 只引用它，不重复实现。
---

# cpa-workbuddy-plugin 插件发布链路

## Skill 作用与适用场景

- 仓库四插件：`workbuddy-provider`（主，版本 0.14.x）、`qoderwork-provider`（0.9.x，已全量对齐 workbuddy 架构）、`traework-provider`（0.1.x，2026-08-28 首发布）、`workbuddy-token-usage`（0.2.x）
- 覆盖从「bump 版本」到「远端验证 ALL PASS + 生产服务器部署」的完整发布链路（0.12.x → 0.14.16 多次跑通，2026-08-29 一次会话内完成 workbuddy 0.14.16 / qoderwork 0.9.6 / traework 0.1.9 三插件并行发布闭环）
- 本地验证（cgo-shim / 单测）职责在 `cgo-plugin-isolated-test`，本 skill 只规定「何时、以什么环境验证」
- 发布前代码逻辑正确性由其他规则保证；本 skill 管「怎么把已验证的代码发出去、部署到生产」

## 自动触发信号

- 用户说「发布」「发版」「升级到 X.Y.Z」「发一个新版本」
- 改动已就绪（fix/feat 已本地验证）需要走完 commit→push→CI→assets→registry 全链
- 新版本号已 bump 但远端 registry 还是旧版，需要补齐剩余链路

## 进入后先做什么

1. **摸清工作树**：`git status --short` 全量看；区分「本次要发布的改动」与「并行/无关改动」（本项目常见并行 40x 重试等未提交改动，**严禁混入发布**）
2. **确认远端基线**：`git log --oneline -5`，记住远端 main 的 HEAD（push 成功后 diff 起点）
3. **确定版本号**：patch 级（bugfix/UX 微调）→ 0.12.x 递增；minor（行为/API 变更）→ 0.13.0。CHANGELOG 顶部是历史 Breaking Change 记录，格式 `## X.Y.Z`
4. **识别混合文件**：`git diff <file>` 检查 main.go / usage_config.go 等是否同时含并行改动 → 决定走「正常发布」还是「分离发布」

## 默认执行流程（13 步，不可跳步）

### Step 1 · bump 版本三处（漏一处 = 面板/产物/registry 不一致）

- `workbuddy/VERSION`（或其他插件目录 VERSION）：`0.12.2` → 新版本
- `workbuddy/main.go`：`var version = "0.12.2"` → 新版本（**注意：main.go 常是混合文件，见 Step 3**）
- `registry.json`：对应插件条目 `"version": "0.12.2"` → 新版本（registry.json 的 `plugins` 是 **list**，用 `id` 匹配条目）

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

```bash
# 服务器本机（docker host 网络，端口 8317）；MANAGEMENT_KEY 来自服务器 config 的 remote-management.secret-key
curl -s -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v0/management/plugin-store/workbuddy-provider/install?version=0.14.16"
```

- **这是热部署唯一可靠路径**（2026-08-29 实测）：
  - `docker cp` 新 .so 进容器 → **不触发**插件加载（进程不重扫）
  - `PUT /v0/management/plugins/<p>/config` 写同内容配置 → **不触发** fsnotify 重载
  - 只有 plugin-store install 会下载 assets → 落盘 → 热重载（面板日志可见加载时间戳）
- 部署后验证：`GET /v0/management/plugins/<p>/accounts`（或面板资源 URL）返回 200 + 预期数据；面板页 `GET /v0/resource/plugins/<p>/panel` 200
- 最终态核对：生产各插件版本应与 registry.json 完全一致

```bash
rm -rf /tmp/cpa-0xxx cpa-shim-* 2>/dev/null
git log --oneline -5 && git status --short | grep -cE "^ M|^\?\?"   # 并行改动条数应不变
```

## 关键命令速查

| 操作 | 命令 |
|---|---|
| 版本 bump | VERSION + main.go `var version` + registry.json `"version"` 三处 |
| 本地验证 | `python scripts/cgo-shim-build.py <plugin>`（需先移走并行文件模拟 CI） |
| push | `git push origin main`（SSH remote，直接推；HTTPS+askpass 仅作 SSH 失效时的回退） |
| 下载资产 | `python scripts/download-release-assets.py <VER> <provider-id>`（⚠️ 与 publish 参数顺序相反） |
| 发布 registry | `python scripts/publish-assets.py <PLUGIN> <VER>` |
| 生产部署 | `curl -X POST -H "Authorization: Bearer $KEY" "http://127.0.0.1:8317/v0/management/plugin-store/<provider-id>/install?version=<VER>"` |
| registry 校验 | `python scripts/validate-registry.py` |

## 踩坑清单（全部实测）

1. **（遗留回退）GIT_ASKPASS 路径**：SSH 已是默认 push 方式（见 Step 7）；仅 SSH 失效回退 HTTPS 时使用，且必须 Windows 风格 `C:\...`；`/c/...` 报 cannot spawn（git.exe 不认 POSIX 路径）。askpass 用完即删。
2. **0.9.7 教训**：assets commit **必须 push** 后才能 publish-assets.py——publish 后 registry 指向 raw.githubusercontent 的 URL，不 push 则远端 404。
3. **分离发布**：并行改动横跨多文件是整体；tracked 用 checkout 恢复、untracked 必须 mv 走；验证要在「纯 HEAD+自己文件」环境跑，否则混入并行测试符号导致 FAIL。
4. **版本三处漏一处**：VERSION/main.go/registry.json 任一遗漏 → 面板版本号、构建产物版本、registry 版本不一致。
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

## 权责边界与不负责事项

- 只负责「发布执行」：版本 bump、提交、push、CI、assets、registry、远端验证
- 不负责代码正确性验证本身（那是 cgo-plugin-isolated-test + 各开发规则）
- 不负责替代用户决策（版本号语义、是否发布）
- 不把并行/无关改动混入发布（用户明确要求另发时除外，须显式列出文件）
- 发布完成后提醒用户重启宿主（c-shared 插件需 CLIProxyAPI 重启才生效）

## 执行通过 / 驳回标准

- 通过：三处版本一致、commit 链清晰（feat/fix → assets → registry）、远端验证 ALL PASS、生产服务器已部署到同版本（或已提示用户部署）、并行改动完好
- 通过：发布内容与工作树其他改动严格隔离（staged 列表人工可核）
- 驳回：跳过任一步骤（尤其 push assets 或远端验证）
- 驳回：版本号三处不一致、registry 有旧版本残留、并行改动丢失
