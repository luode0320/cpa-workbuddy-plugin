# 项目历史事件

> 本文件追加关键历史事件并只保留最近 20 条（按日期倒序、新事件置顶、追加后自动裁剪）；普通启动默认不读取，只有历史追问、当前状态不足或真实卡点时才窄检索。

## 事件

- 2026-08-23：账号面板「删除账号」功能 **已实现未提交**：卡片右上角 `×` 删除图标 + 二次确认模态框（取消不请求 / 确认 POST 后刷新 / 失败 Toast 保留卡片）；后端新增严格删除接口 `POST /delete`（仅收 `auth_index`，重新校验存在性 → `isWorkbuddyAuthFileName` 文件名归属 → `hostAuthGetBundle` 解析 → `phys.AuthIndex` 一致 → 路径非空 → `isSafeWorkbuddyAuthPath` → `deleteAuthFileInDir` 物理删除 → `clearDeletedAccountState` 全维度清理 f.ID/auth_index/UID 三个键）。新增 `clearFailoverStateForAuth` / `clearDeletedAccountState` / `isWorkbuddyAuthFileName` 三个纯函数 + `auth_delete_test.go` 单测；`cgo-shim-build.py workbuddy` build/vet/test 全绿，panel.html 两脚本块 `node --check` 通过。覆盖边界：`handleDeleteAuth` 完整链路因 `hostCall` 依赖 cgo `hostAPI` 无法在 shim 环境单测，真实页面交互验证待做；未提交、未发版。
- 2026-08-23：账号卡统计位置微调 **已发布**（workbuddy 0.14.6 + qoderwork 0.9.3）：成功、失败、连败、冷却从标题徽标区移动到可用积分首行（`progressHTML(c,a)` 增统计段），`.pb-label` 支持换行，加载中保留「可用积分」，冷却 ticker 只刷新冷却秒数。双面板 4 个脚本块 `node --check` 全部通过，双插件 `cgo-shim-build.py` 的 build/vet/test 全部通过。提交链 195b2ea→a5711ee→f714598，CI 双 run success（32634002586/32634003971），16 assets checksums 全 OK，远端 raw URL 全部 200，registry raw 200 含 0.14.6/0.9.3
- 2026-08-23：账号卡统计位置微调 **已完成未发布**：workbuddy/qoderwork 将成功、失败、连败、冷却从标题徽标区移动到可用积分首行；`.pb-label` 支持换行，加载中保留「可用积分」，冷却 ticker 只刷新冷却秒数。双面板 4 个脚本块 `node --check` 全部通过，双插件 `cgo-shim-build.py` 的 build/vet/test 全部通过；当前仅两个 `panel.html` 有未提交改动。
- 2026-08-23：面板账号卡「成功/失败计数 + 连败/冷却」展示 **已发布**（workbuddy 0.14.5 + qoderwork 0.9.2）：wbAccount 补 Success/Failed（host.auth.list 透传，断点在插件组装丢失非上游缺失）+ FailCount/Cooling/CoolUntil（failoverStateSnapshot 从测试 helper 提升为 dashboard API）；panel.html 徽标区 badge.stat/badge.cooling + 1s 冷却倒计时 ticker；qoderwork 三件套同构适配。提交链 0de225e→1311366→f15bc92→6b20f0b，CI 双 run success（32630699531/32630699252），16 assets checksums 全 OK，远端 raw URL 全部 200
- 2026-08-23：workbuddy-provider v0.14.3 发布（流式切号链 GetBody==nil 断裂修复：`rebuildRequestWithSA` 改 `GetBody()→io.ReadAll→bytes.NewReader` 重建 body，产物 GetBody 恒可用，切号链可走满 retry_on_4xx 预算；0.14.2 复测仍 2 次 429 即中断的真根因，推翻"账号池仅 2 候选"错误假设；提交链 5ae916d→e0ffce0→a03686e→9e04131→06944e7，qoderwork 代码免疫仅修滞后注释，远端 raw URL 200）
- 2026-08-23：workbuddy-provider v0.14.2 + qoderwork-provider v0.9.1 发布（429 纳入同请求切号循环，`isAccountLevel4xx` 增 `StatusTooManyRequests` case；提交链 42b9ac3→4158a59→8a3f18c→d2d9bb9→9efc0d0，远端 raw URL 200 验证通过；0.14.1 面板 tab 补丁已由并行会话先行发布，故 429 修复独立 bump 0.14.2 不重打 tag）
- 2026-08-23：deepseek-vision 第四插件迁移完成（02:23-02:50）后由用户决策**撤销**（02:57）：宿主未配置视觉模型、插件依赖宿主 vision 回调 → 工作区全部回退到 HEAD，三插件基线恢复

- 2026-08-22：workbuddy-provider v0.12.0 发布，移除三池路由只留保号池（提交链 f64f35a→2cdd179→fec796e）
- 2026-08-22：40x 账号级换号重试（401/403/404/405，retry_on_4xx 预算默认 3），workbuddy+qoderwork 对称，未发版
- 2026-08-22：项目改名 cpa-plugin → cpa-workbuddy-plugin，registry/build.yml/go.mod 全链路同步
- 2026-08-22：workbuddy-provider v0.9.9 + qoderwork-provider v0.2.9 账户 failover 阶梯退避发布
- 2026-08-22：token-usage-tracker v0.1.5 清零 envelope 修复落库失败
- 2026-08-22：workbuddy-provider v0.9.4 feed 字段语义对调 + 多凭证批量导入
- 2026-08-22：workbuddy-provider v0.9.3 toggle 直写物理 auth 文件（host.auth.save 硬编码 StatusActive 根因）
- 2026-08-22：三插件 id 改名（workbuddy→workbuddy-provider 等），build.yml matrix 拆 id/src
- 2026-08-22：registry 显示名改名（WorkBuddy Provider 等），版本 0.9.5/0.2.7/0.1.6
- 2026-08-19：token-usage-tracker 拆分决策（文件 feed 而非共享 bbolt）

## 计数锚点区

> 本区由 `memory-usage-tracking-rules` 收口闸门维护：HISTORY 仅窄读计入，会话启动不读不计；被裁剪事件的锚点随事件一起删除（不保留 retired）；本区计数仅作主题热度弱信号。锚点 key 用事件 `- YYYY-MM-DD：` 后的核心主题短语（约前 12 字符，可前缀匹配）。

```yaml
version: 1
anchors: []
```
