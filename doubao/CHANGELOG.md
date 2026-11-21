## 0.1.33 (2026-11-21)

### 修复
- **生成中的占位块会让回灌被整个跳过，并把封面图当视频返回**。`assetFromVideo` 在
  `download_url` 缺失时会退到 `cover.image_preview`，而 `ensureVideoAsset` 用
  `len(res.Assets) > 0` 判「已有产物」。两者叠加：确认轮的 SSE 里只要先推一个
  `status=1` 带封面的 2074 块，回灌就整段跳过，客户端最后拿到
  `{"type":"video","url":"...png"}` 这种把 JPEG 标成视频的条目。
  修法：新增 `playable()` / `hasPlayableAsset()` 两个判据，回灌守卫改用
  「有可播放的成品」；`assetFromVideo` 的封面兜底整段删除，占位阶段 URL 保持为空。
- **正文说「已生成视频」而 `media` 是空的**。`describeAssets` 只统计 `len(assets)`，
  `mediaExtension` 却会跳过没有 URL 的条目，两边口径不一致。改为两边都用 `playable()`。
- **`SSE_ACK` 把有效的 `section_id` 抹成空串**。确认轮带着上一轮的 `section_id` 进来，
  上游 ACK 里这个字段可能为空，原实现无条件赋值；而对外输出是「非空才写」，结果客户端
  下一次续接直接断链。改为逐字段非空才覆盖。
- **`attachAssets` 依赖 `[]map[string]any` 的具体类型断言**。只要响应过一道 JSON 往返，
  `choices` 会退化成 `[]any`，旧实现静默 `return`，附件全丢且不报错。新增
  `firstChoiceMessage()` 兼容两种形态。
- **`bestStream` 同分辨率多码率时取值不确定**。`video_list` 是 map，遍历顺序随机，
  同分时只按像素面积比较会随机挑一条直链。改为先按 key 定序，同分时结果可复现。
- **`executorExecuteStream` 对同一份 `raw` 解了两次**。第一次只为拿 `stream_id`，
  会把含 `StorageJSON` 的整个请求体再解析一遍。新增 `streamIDOf()`：先查
  `Metadata["stream_id"]`，再对 raw 做最小结构体解析。

### 变更
- `creation_parse.go` 的 `assetFromVideo` 不再用封面兜底地址与尺寸。封面是一张 JPEG，
  塞进 `URL` 会让下游出现自相矛盾的类型标记。占位阶段资产仍然返回（`Key` 非空即返回），
  回灌层靠 `sawCreation` 判断上游是否真的开跑，丢掉占位块会误报「上游没有开始渲染」。
- 没有成品直链时，`media[]` 里不会出现该条目，`width` / `height` 也可能整体缺失
  （封面不再补尺寸）。需要真实分辨率仍以下载后 `ffprobe` 为准。

### 已知
- 占位块与成品的区分只依赖「URL 是否非空」。若将来上游给占位块也带上可访问地址
  （例如临时占位视频），这层判据会失效，需要改看 `status`。
- 本次改动不影响对话超时（`timeout_seconds` 120）与流式超时（`stream_timeout_seconds`
  600），也不改动两个视频专属等待项。

## 0.1.32 (2026-11-21)

### 修复
- **上游拒答时确认轮白等满总时长**。上游在「视频技能没被激活」时不提交渲染任务，
  而是回一段「当前这个对话窗口没有视频渲染执行入口」之类的文本 —— 但它**照样返回
  `conversation_id`**，形态与正常首轮完全一致。插件因此四条件全成立、进入回灌，
  在消息链里一遍遍拉空，直到 6 分钟总超时才报「等待视频超时」。2026-11-21 生产实测
  白等 366 秒，而真相是「压根没开始」。
  现在多了一条判据：**消息链里到底有没有出现过 `block_type=2074`（创作块）**。
  `assetsFromChainResponse` 与 `fetchChainAssets` 增加 `sawCreation` 返回值 ——
  必须单独返回，因为 `parseCreationBlock` 会把「只有 status、没有 key/URL」的占位块
  丢掉，所以「在渲染」与「根本没开始」两种情况的资产列表都是空，只看资产分不出来。
  `waitVideoAsset` 在「还没见过创作块」时按 `video_creation_wait_seconds` 提前失败，
  文案为「上游没有开始渲染（… 内消息链里没有任何创作块）：这个账号当前可能无法出片，
  请先用面板「测试模型」确认，或换账号重试」。**见过创作块后就不再套这个上限**，
  上游确实开跑的情况下慢属于正常，直接放到总超时，不会误伤慢账号。

### 新增
- `main.go` 两个**视频专属**配置项，均只在视频回灌路径上读：
  - `video_poll_timeout_seconds`（默认 360）：确认轮等待产物的总时长。
  - `video_creation_wait_seconds`（默认 120）：等首个创作块出现的上限。
  对话超时 `timeout_seconds`（默认 120）与 `stream_timeout_seconds`（默认 600）
  **一个字节没动**，非视频请求的等待行为与 0.1.31 完全一致。

### 变更
- `video_chain.go` 的 `videoPollTimeout` 由写死变量改为
  `videoPollTimeoutDefault` + `videoPollTimeoutOverride` + `videoPollTimeoutValue()`，
  取值顺序为「用例覆盖 > 配置项 > 兜底」。`ensureVideoAsset` 的上下文超时改用同一个
  有效值加一分钟余量，否则配置调大后上下文会先触发，报出来的是
  `context deadline exceeded` 而不是业务文案。

### 已知
- 首块等待是「没开跑就早失败」的判据，不是「一定能出片」的保证：上游在渲染中途失败
  仍要等到总超时。
- 上游拒答率在分钟级波动（2026-11-21 实测同一提示词连发两次，一次给提示词、一次拒答），
  拒答时首轮仍会返回续接点，客户端需按 README「首轮就拒答」一节自行判断。

# Changelog

## 0.1.31 (2026-11-21)

### 修复
- **视频确认轮被换到别的账号后必然拿不到成品**（多账号场景的真实缺陷）。选号本身
  是轮转的（`scheduler.go` 的 `schedulerIndex`），池里有两个以上账号时首轮落 A、
  确认轮必落 B。上游只认建立会话的那个账号，跨账号续接拿不到上下文，于是每次确
  认轮都白等满 6 分钟再报「等待视频超时」，还把这次追问的额度烧在一个健康账号上。
  三层一起改：

  1. **会话粘性**（新增 `session_sticky.go`）：`conversation_id → auth_id` 内存表，
     TTL 30 分钟，首轮成功后登记，续接请求在 `decodeExecutorRequest` 末尾回钉到首
     轮那个账号。取不到绑定账号时明确报错，不静默回落到当前账号 —— 那只会把一次注
     定失败的续接记成一个健康账号的失败。
  2. **续接请求禁止换号**（`executor_failover.go`）：`resume` 非零时失败直接返回，
     不进入 `pickAccount`。429 这类「本来该立刻换号」的信号在续接请求里同样不换。
     失败账号仍照常记账冷却，但不会波及另一个账号。
  3. **回灌用实际执行账号**（`runWithFailover` 返回值增加 `*StoredAuth`）：
     `executor.go:209`、`executor_stream.go:82`、`executor_stream.go:117` 三处
     `ensureVideoAsset` 改用实际跑通的那个账号。此前一律传外层首选账号，一旦
     failover 换过号，回灌就会拿 A 的 Cookie 去拉 B 的会话。

### 新增
- `session_sticky.go` / `session_sticky_test.go`：粘性表读写、TTL 回收、非新会话
  回钉、绑定账号缺失即失败，以及 `runWithFailover` 的首轮登记与续接禁换号回归。
- `video_chain.go` 增加测试观察点 `chainPollAccountHook`：回灌走的是 Cookie，而
  Cookie 的 Domain 绑定在 doubao 站点上，用例里的 httptest 服务收不到这些 Cookie，
  无法从请求头反推「用了哪个账号」，只能靠钩子暴露入参。

### 已知
- 粘性表是进程内内存表：插件热重载后会清空。已重启但会话未过期时，续接请求会退回
  「沿用当前账号」的行为（不报错），需要重新发起首轮才能重建成片。
- 上游处于拒答波动态时，首轮本身就回「我没有视频生成能力」，确认轮仍会白等满
  `videoPollTimeout`（6 分钟，插件内固定值，不是配置项）。识别拒答并快速失败不在
  本版范围内。

## 0.1.30 (2026-11-18)

### 破坏性变更

- **删除 `doubao/image`，插件只做生视频**（需求变更 `CHG-001`，用户 2026-11-18 明确
  授权）。模型目录从两条别名收敛到一条：`buildCompletionRequest` 拿到 `doubao/image`
  现在由宿主目录先行拦截，返回 400 `model_not_found` / `unknown provider for model doubao/image`；若绕过宿主直连插件执行器，插件自己报「不支持的模型 `doubao/image`：本插件只做生视频，可选 doubao/video」。
  不再静默透传到上游。调用方若仍请求图片模型，需要改用 `doubao/video`。
- 默认模型别名 `defaultModelID` 由 `image` 改为 `video`：面板「测试」在未指定模型时
  打的不再是生图。

### 删除

- **整条图片产物链路**：`skillImageGen`（上游 skill 3）、`creationImage` /
  `creationPlaceholder` 两个结构体、`creationItem.Image` 字段、`assetFromImage`
  解析函数（55 行）、`attachAssets` 的多模态 `image_url` 分支、`describeAssets` 的
  「已生成图片」分支、`streamAssetFrames` 的图片帧分支。
- **`creation_watermark.go` 整个文件**：它只对图片生效——`hydrateAssets` 的守卫是
  `if asset.Key == "" || asset.isVideo() { continue }`，视频从来不走它。随文件一起删掉
  `resolveNoWatermark` 与 `/creativity/resource/get_without_watermark` 调用，以及只服务
  它的 `postBusinessJSON`（44 行）。
- **面板测试的图片判据**：`activeTestSawCreation` 与 `capabilityVerb` 两个函数整体删除。
  只剩生视频后，`runActiveTest` 的结论收敛为单一路径：`SSE_ACK` 里拿到会话 id 判通路
  正常，否则按「能正常出字但上游没有建立会话」失败。

### 变更

- **`isVideo()` 保留**：目录只剩视频后它恒为 `true`，但渲染层与回灌层靠它区分「成品
  直链」与「封面兜底」两类地址，删掉会让调用点失去自解释性。
- 探针提示词池只剩一组（`testPingPromptsByKind` 只有 `"video"` 键），
  `testPingFallbackKind` 改为 `"video"`。

### 已知

- 视频确认轮拿到续接点后仍可能等满 `video_poll_timeout`（默认 6 分钟）才报超时：上游
  会在同一组参数下随机拒答（先建会话再回一段「我无法直接生成短视频」），此时既没有
  产物也不会报错。2026-11-17 生产实测同一输入（5 秒 / 720p / auto）一次出片、一次拒答。
- `media[].duration` 是上游产物的真实时长，不是请求的目标时长；`url_expire` 到期后地址
  失效，需要重新生成。

## 0.1.29 (2026-11-17)

### 修复

- **面板「测试」对生图/生视频是假阴性**（0.1.28 收紧判据后暴露）：`runActiveTest` 的
  回调写成「只要有输出就 `return false` 关流」，而两种能力都是**先说后出**——上游先发
  一段文本，产物帧随后才到。于是 2.2 秒就关流，永远等不到产物帧：同一账号走正式链路
  12.2 秒真的出了 2048×2048 图，面板却报「没能出图」。现在改成：
  - 文本帧只作记录（`sawText` + 预览），**不终止读取**；
  - 生产物帧才判成功并立即收工；
  - 生视频首轮**永远不会有产物帧**（两段式，产物由确认轮之后的消息链回灌），判据改用
    `SSE_ACK` 里的 `ack_client_meta.conversation_id`：会话真的建起来了才判通路正常，
    成功文案明说「能否真出片要带会话续接点再发确认轮」。
- **成功文案尾部挂一个空标点**：上游常在产物前先发一个句号或换行，看着像有文本却不含
  信息，拼进结果会得到「…耗时 15.1s）：。」。新增 `meaningfulPreview` 去掉只有标点的
  预览（真机复验实测到）。

### 修复（流式两段式）

- **流式响应拿不到会话续接点**：视频两段式要求客户端在首轮之后带上
  `conversation_id` / `section_id` / `last_message_index` 再发一次确认轮，而流式帧里
  这三项一直缺失——增量帧在文本到达时就发出去了，那时聚合还没完成，取不到分节 id 与
  消息序号。现在终止帧（`finish_reason=stop`）统一带上完整续接点：
  `streamStopFrame(model string, resume resumePoint)` 取代原来只传 `conversationID`
  的写法，两条流式路径（`collectStreamResponse` / `pumpCompletionStream`）都传
  `res.resumePointOf()`。
- **续接点逐字段判空写**：`attachResumePoint` 原先硬写三个键，上游只给会话 id、不给
  分节 id 时会写出空串，客户端以为「续接点齐了但值不对」。现在缺哪个字段就省略哪个，
  一眼能看出缺的是什么。

### 已知

- 视频确认轮拿到续接点后仍可能等满 `video_poll_timeout`（默认 6 分钟）才报超时：上游
  会在同一组参数下随机拒答（先建会话再回一段「我无法直接生成短视频」），此时既没有
  产物也不会报错。2026-11-17 生产实测同一输入（5 秒 / 720p / auto）一次出片、一次拒答。
- `media[].duration` 是上游产物的真实时长，不是请求的目标时长；`url_expire` 到期后地址
  失效，需要重新生成。

## 0.1.28 (2026-11-17)

### 新增

- **视频参数可由调用方指定**：`doubao/video` 支持三个可选平铺字段（与续接点同层），
  不传时与 0.1.27 逐字节一致：
  - `video_duration` 整数，合法区间 1~60，缺省 10；
  - `video_resolution` 字符串，缺省 `720p`；
  - `video_ratio` 字符串，缺省 `auto`。
  越界不报错，按缺省处理——一个笔误不该让整次生成失败。
- **首轮提示词与确认轮参数同源**：传了非缺省值时，首轮提示词追加
  「视频参数要求：时长 N 秒、清晰度 X、画面比例 Y，请直接按这组参数生成」，确认轮
  `chat_ability.ability_param` 用同一套值提交。两处都从同一个 `videoParams` 取值，
  不再出现「上游对用户承诺 5 秒、插件实际提交 10 秒」的脱钩（0.1.27 缺陷）。
- **`media[].duration` 回填产物真实时长**：视频条目的 `duration` 取上游
  `video.duration`（浮点秒，抓包实测 `10.08`）；图片条目没有该键；上游未给时省略，
  不写 0。与请求的**目标**时长不是一回事——上游可能取整或改写。

### 修复

- **面板「测试」对生图/生视频是假阳性**：探针提示词原先是「你好 / 今天几号 / 说一句
  问候语」这类对话文本，上游用一段文字作答，判据又是「有任何输出帧即算成功」（连思考
  块都算），于是不能出图的账号也报「活跃成功」。现在两处一起收紧：
  1. 提示词按能力分池，`image` 问「画一只橘猫…」、`video` 问「生成一段海浪拍打礁石的
     短视频…」，未知 `Kind` 回落生图池；池内多条按能力独立原子轮换（同一账号反复发
     相同提示词会被上游按内容重复度判定异常，2026-11-16 用户明确要求）。
  2. 成功判据改为必须解析到 `block_type=2074` 创作块；只有文字时判失败，文案区分三档：
     上游报错 / 「账号能正常出字，但没能出图（出片）」/ 「上游没有返回任何输出」。
     `user_type=1` 的提问回显里带的创作块不算产物。

### 测试

- 新增 11 个用例：`TestNormalizeVideoParams` / `TestAppendVideoParamHint` /
  `TestBuildConfirmOptionUsesRequestedParams` / `TestMediaExtensionCarriesVideoDuration` /
  `TestMediaExtensionOmitsUnknownDuration` / `TestMergeAssetsKeepsDuration` /
  `TestTestPingPromptRotates` / `TestActiveTestSawCreationDetectsCreationBlock` /
  `TestCapabilityLabels` / `TestRunActiveTestRejectsTextOnlyReply`，并改写
  `TestRunActiveTestStopsOnFirstFrame`（改发创作块帧，否则判据收紧后它测的不再是成功路径）。
- **六处哨兵全部真失败过**，不是「写了用例就算」：删 `Duration: video.Duration`、
  删 `mediaExtension` 的 duration 块、删 `mergeAssets` 的 duration 分支、同时破坏参数同源
  调用点与 `params.isDefault()` 守卫、成功判据退回「有输出即成功」、把 image 池换成视频
  提示词——每一处都让对应用例立刻失败，恢复后 `all green`。

### 已知

- 参数由调用方显式传，插件**不解析**上游确认文本。上游确认文案是自然语言，实测出现过
  「你要求的 3 秒低于视频生成的最低时长」这种协商语句；正则解析会随文案改版失效，
  而调用方本来就要发确认轮，多带三个标量字段没有额外成本。
- 视频模型代号固定为 `seedance_v2.0_mini`，不开放给调用方（换模型属能力变更）。
- 确认轮之后的成品回灌仍需等 1~5 分钟，这是上游异步生成的真实耗时。
- 视频直链带 `url_expire`，过期后需重新生成；插件不做本地转存。

## 0.1.27 (2026-11-17)

### 修复

- **首轮视频请求不再误入异步回灌**（0.1.26 生产事故）：`ensureVideoAsset` 原先只看
  「是不是视频模式」，而首轮请求同时满足「视频模式」与「已有 conversation_id」，
  于是首轮就进回灌轮询、白等满 `video_poll_timeout` 再超时——生产实测 2026-11-17 02:33
  首轮耗时 6 分 13 秒后返回 503，日志为
  `err=等待视频超时（6m0s）：上游仍未产出成品，稍后可用同一 conversation_id 重试`。
  判据改为「这一次是不是确认轮」：`ensureVideoAsset` 新增 `resume` 入参，续接点为零即
  立刻返回，只把参数确认文本与续接点交给调用方。首轮现在秒级返回。
- 新增回归用例 `TestEnsureVideoAssetSkipsFirstRound`：视频模式 + 有会话 id + 空续接点，
  必须立刻返回且不得塞资产。哨兵验证过——去掉 `resume.isZero()` 后该用例立刻失败。

### 已知

- 确认轮之后的成品回灌仍需等 1~5 分钟，这是上游异步生成的真实耗时，不是插件开销。
- 视频直链带 `url_expire`，过期后需重新生成；插件不做本地转存。

## 0.1.26 (2026-11-17)

### 变更

- **插件改为「只做图片与视频生成」**：工作模式对话、技能文本对话、每日签到、额度查询
  全部删除。模型目录只剩 `doubao/image` 与 `doubao/video`；未知名一律报「不支持的模型」，
  不再回落到任何默认模型。删掉的代码：`checkin.go` 及其测试、`workModelAliases` 十条、
  `buildWorkOption` / `workModeExt`、工作模式七个常量、`atoiSafe`、`Config` 里七项 `work_*`。
- **视频从「只回参数确认」变为真能返回可下载地址**，走两段式且额度只由调用方的第二次
  请求触发：
  1. 首轮 `doubao/video` 回参数确认文本，响应体带 `conversation_id` / `section_id` /
     `message_index` 三个续接点；
  2. 调用方把这三字段放进请求体再发一次，插件构造确认轮请求；
  3. 插件经 `/im/chain/single` 轮询取回 `download_url`，实测约 150 秒。
- 确认轮请求形态与真实网页逐字段对齐：新增顶层 `chat_ability`（`ability_type=17`、
  `ability_param` 是字符串套 JSON）、`client_meta` 去掉 `local_conversation_id`、
  `option` 去掉 `agent_mode` 键、`ext` 换成 5 键、`general_task_param` 换成对象形态、
  消息块补 `extra_info`。`chat_ability` 由请求构造器注入，漏了只会回确认文本。
- 视频产物结构按真实响应重写：地址取 `download_url`（不是 `url`），宽高是**字符串**，
  `video_model` 是字符串套 JSON，封面在 `cover.image_preview` / `cover.image_thumb`。
  地址缺失时退到 `video_list` 里分辨率最高的那条（`main_url` 是 base64）。

### 修复

- **`/im/*` 族端点的 Content-Type 与 `/chat/completion` 正好相反**：前者必须
  `application/json; encoding=utf-8`，缺了会被上游以 `712012002`「不支持编码类型」
  直接拒绝；共用一份请求头必然有一边失败。已拆成 `postChainJSON`。
- 会话续接点原先恒为空：`collectCompletion` 只接 `conversationID`，`last_section_id`
  与 `last_message_index` 永远是空值与 0。新增 `resumePoint` 与
  `collectCompletionWithResume`，四条执行路径（非流式 / 流式 / 失败重试 / 活跃测试）全部贯通。
  确认轮依赖这三个字段，缺了就等于每次开新会话，永远停在确认页。

### 已知

- 视频必须由调用方显式发第二次请求：插件不替你花额度。首轮只回参数确认。
- 视频生成约 1~5 分钟，`download_url` 带 `url_expire`，过期需重新生成。
- 轮询预算 6 分钟；超时会报「稍后可用同一 conversation_id 重试」。
- 存量部署的 `config.yaml` 里留着旧的 `work_*` 七项不会报错，yaml 会宽松忽略。

## 0.1.25 (2026-11-16)

### 修复

- **`-1 delegated input protocol violation` 的真因是「该账号没有被投放到豆包工作模式」，
  0.1.23 的「请求体缺默认值字段」定性是错的**。三步铁证：① 同账号在真实浏览器点
  网页「工作」标签发同一请求，也回同一帧
  `{"error_code":-1,"error_msg":"delegated input protocol violation","extra":{"is_replay":"false"}}`；
  ② 把浏览器原样抓到的 body（含 `reasoning_effort=3`、`messages[].content_block[].extra_info`
  等全部字段）原封不动重放，健康账号正常出字、本账号仍回 `-1`；③ `launch` 端点（页面
  原生 `fetch` + 页面原生签名，非插件请求）对两个账号返回的 `data.ug_campaign_info`
  不同：健康账号有 `campaign_group_code=pc_incentive_task_mvp_v3`（`material_map` 里标题
  「免费用豆包工作」、`schema` 指向 `/pc/agent-explore`），本账号该字段为空串、
  `material_map` 整个缺失。0.1.22 的定性方向是对的，0.1.23 用插件自己的请求体消融
  得出的结论属于把相关当因果。
- **错误文案不再把用户引向无效动作**。`-1` 改为「该账号没有被投放到豆包工作模式
  （上游活动 `pc_incentive_task_mvp_v3` 未覆盖此账号），请求本身没有问题：请在面板
  改用其他账号，或在豆包电脑版参与「免费用豆包工作」活动后重试」；不再让用户怀疑
  插件版本或请求体形状。
- **`-1` 纳入同请求换号，但不计入账号冷却**。工作模式请求打到未投放账号时立刻换到
  下一个账号（否则每次都要手动切号）；但 `-1` 只否掉 `agent_mode=1`，同账号的技能
  模式（`agent_mode=2`）完全正常，若计入冷却会把整个账号跳过 60 秒起，连能用的技能
  模式一起打不通。分类依据因此拆开：`shouldRotate` 含 `-1`，`isAccountFailure` 不含。

### 已知

- 结论已用「真实浏览器同账号同请求」交叉验证，排除请求体、设备标识（`device_id` /
  `web_id` 回退旧值）、`model_item_key`（换成 Seed 基座 `"0"`）、`gd_random` cookie、
  客户端参数集（`aid=582478` / `desktop` / `2.30.1`）、签名来源（页面 SDK 现签）等
  全部变量。
- 账号是否被投放该活动属上游灰度事实，插件侧无法自助加入：活动页
  `/pc/agent-explore` 只提供「去下载」豆包电脑版，网页端无法参与。

## 0.1.24 (2026-11-16)

### 修复

- **`710022004` 的真因是「人机验证（语义选图）」，0.1.21 起的「设备指纹不匹配」
  归因是错的**。解析上游返回的 `extra.decision` 才看到真实语义：
  `{"code":"10000","from":"shark_admin","type":"verify","subtype":"semantic_reasoning",
  "log_id":"…","verify_scene":"doubao_message_web"}` —— `type=verify` 说明这是上游对
  该账号弹的人机验证，既不是限流，也与 `s_v_web_id` 无关。用 CDP 驱动真实 Chrome
  在同一账号上发消息，页面中央当场弹出九宫格验证码（标题「用于身体锻炼的物品」），
  iframe 指向 `rmc.bytedance.com/verifycenter/captcha/v2`，左下角明文 log_id 与 SSE
  的 `decision.log_id` 完全一致。0.1.20 当时的判断是对的，0.1.21 只看了错误码和
  `error_msg` 就把它推翻，方向错了。
- **错误文案不再把用户引向无效动作**。`710022004` / `710022005` 改为「上游要求该
  账号完成人机验证（语义选图）：请在浏览器里登录同一账号，在豆包网页任意发一句话，
  按弹出的九宫格完成一次验证；验证通过后账号即恢复，不需要重新授权」；`710022002`
  去掉「未采集 s_v_web_id」的归因，改成「上游判定这次请求过频」，并提示降低测试频率。
- **`streamError` 解析 `extra.decision`**。新增 `verifyOf()` 判定 `type=verify` 并取出
  `subtype` / `log_id` / `verify_scene`，新增 `streamErrorHint()` 统一生成提示；
  `collectCompletion` 与 `runActiveTest` 都改用它，验证态的错误信息会带上游 `log_id`，
  可与浏览器里看到的验证码对账。

### 已知

- 该验证码是「语义选图九宫格 + 拖拽投放 + 严格时限」，脚本化拖拽实测会提示「验证
  超时，请根据提示重新操作」，只能由真人在浏览器里过一次；过掉之前该账号的每个请求
  都会被拦，切换账号是插件侧唯一有效的应对。
- 同一账号在真实网页的「工作」标签（`agent_mode=1` + DeepSeek 模型 key）也会被弹同一
  张验证码，所以当前失败与插件请求形状无关。

# Changelog

## 0.1.23 (2026-11-16)

### 修复

- **`-1 delegated input protocol violation` 的真因是「请求体比真实网页少了一批显式
  默认值字段」，0.1.22 的「账号未投放工作模式」定性已被推翻**。2026-11-16 用 CDP
  驱动真实 Chrome，在网页「新工作任务」入口填入 DeepSeek V4 Flash 发消息，抓到
  该账号（用户358840）成功出字的 `agent_mode=1` 请求体；与插件请求体逐字段 diff
  后确认：插件用 `omitempty` 省略了网页每次都会显式发送的
  `click_clear_context` / `connector_info_list` / `disable_sse_cache` /
  `edit_query_id` / `from_suggest` / `is_ai_playground` / `is_from_click_option` /
  `is_from_click_softlink` / `is_regen` / `is_replace` / `is_select_text` /
  `message_from` / `no_replace_for_regen` / `regen_instruction` /
  `regen_query_id` / `resend_for_regen` / `scene_type` / `select_text_action` /
  `shared_app_id` / `shared_app_name` / `related_deleted_message_ids` /
  `recovery_option` 共 22 项，以及 `client_meta.last_section_id` /
  `last_message_index` 两处续接点。逐项消融验证：只给插件原生请求体补上这些
  字段，上游错误码立刻从 `-1` 变成 `710022004`，协议层通过。现全部改为显式输出。
- **`a_bogus` 签名必须覆盖请求体**。三组对照（`{}` / 小 JSON / 真实 3.4KB body）
  得到三个互不相同的签名，而旧实现固定 `x.send('{}')`，签出的值与上游按真实
  body 校验的结果必然不一致。现由 `signChatCompletionURL(sa, url, body)` 把即将
  发送的请求体交给签名器。
- **工作模式请求改成网页形状**。网页「工作」标签发出的就是网页来源请求：
  `runtime_type=1`、`agent_task_param` 六字段全空、无 `client_option` /
  `task_input_json` / `sandbox_*`、`local_permissions.status=3`、扩展放**顶层**
  `ext`（8 键，含 `sub_conv_firstmet_type`）。旧实现按桌面客户端沙箱构造，
  会被上游判成来源不符。
- **删除 `doubao/web` 网页链路别名**。用户明确要求不要这个附加别名，目标是让
  `doubao/deepseek-v4-flash` 本身可用；该别名只是把「选不了模型」的问题藏起来，
  现已整体移除（含 `WebPipeline` 字段与两个相关用例），`-1` 的错误文案也不再
  引导用户改走它。

### 改进

- **面板测试与探活的提示词改为轮换**。此前固定发「hi」，同一账号反复发同一句会
  建出一串内容一致的会话，上游按重复度判定异常并触发风控（用户 2026-11-16 明确
  要求）。现从 5 条短提示词里按原子计数轮换，保证连续调用一定落在不同条目上。

### 已知

- 补齐默认值字段后两个生产账号仍返回 `710022004`（`semantic_reasoning` 风控验证），
  这是密集验证期间的临时限流窗口，与请求形状无关；冷却后应可正常出字。

## 0.1.22 (2026-11-16)

### 修复

- **`-1 delegated input protocol violation` 的真义是「该账号没有被投放到客户端
  （工作模式）链路」，不是请求体字段写错**。2026-11-15 深夜做五模型 × 双管线
  矩阵实测：同一个账号（用户358840）换 `doubao/auto` / `doubao-2.1-pro` /
  `doubao/deepseek-v4-flash` / `doubao/kimi-k3` / `doubao/glm-5.3` 五个模型，
  工作模式全部回 `-1`；而同一个账号切到网页技能管线（`agent_mode=2`）立刻出字；
  另一个账号（欧皇小德子）在同一工作模式下正常出字。因此这个码只跟账号走。
  0.1.20 / 0.1.21 的文案「实测多由工作模式 agent_mode=1 触发，请改用技能模式」
  会让用户误以为是请求写法问题，现改为直接说明账号未被投放，并给出两条出路。
- **新增显式网页链路别名 `doubao/web`（`agent_mode=2`）**，让这类账号有一条
  看得见的逃生通道。**刻意不做自动降级**：技能管线不认 `model_item_key`
  （补 `conversation_init_ext` 也无效，实测回复恒为 Seed 基座），静默降级会把
  用户选的 Kimi / GLM 换成上游默认模型，比直接报错更糟。

### 新增

- `modelAlias` 增加 `WebPipeline` 标记，`doubao/web` 走该分支；能力别名目录、
  可测试别名列表、面板展示列表都自动包含它（`testableAliases` 只排除生成类，
  网页链路别名不是生成类）。
- `workModeForAlias` 对网页链路别名显式清空 `ModelItemKey` / `MemoryProfile` /
  `TotalWindow` / `ReasoningEffort`：这几个字段在 `agent_mode=2` 下无效，留着
  只会让人误以为选了模型。
- 活跃测试成功消息对空 `model_item_key` 回落显示「网页链路默认模型」，不再拼出
  `→ 上游 ` 这样的空档。

### 验证

- 双管线对照（用户358840，45 秒间隔，连跑两轮一致）：

  | 管线 | `agent_mode` | 结果 |
  | --- | --- | --- |
  | 工作模式（`doubao/deepseek-v4-flash`） | 1 | `-1 delegated input protocol violation` |
  | 网页链路（`doubao/web`） | 2 | 出字 `"Hi"` |

- 四象限矩阵（45 秒间隔）：`OH_WORK` 出字 6.2s / `OH_SKILL` 出字 2.0s /
  `U358840_WORK` 623ms 回 `-1` / `U358840_SKILL` 出字 1.1s。
- 上游 `model_id` / `model_type` 字段复核：技能模式换 `model_item_key` 后上游仍
  恒报 `"38"`，无法区分模型；配合模型自述（`key=0 → Seed大模型`、
  `key=person-kimi-k3-261111 → Seed大模型基座`）确认技能管线不认该字段。
- 端到端（欧皇小德子，90 秒间隔）：`doubao/deepseek-v4-flash` 出字 8.08s、
  `doubao/web` 出字 1.49s，两条管线都正常。
- 哨兵（把 `if alias.WebPipeline {` 改成 `if false && alias.WebPipeline {`）：
  `TestWebPipelineAliasReachesTestableList` 失败并打印
  `实际 {Skill:0 Kind:text ModelItemKey:person-deepseek-v4-1-flash-261017 … WorkMode:true}`。

### 其它

- `TestStatusCodeHint` 的 `-1` 断言改为「没有被投放」，并新增守卫：提示里不得
  再出现 `agent_mode`，防止文案回退成「让用户改请求字段」。
- 新增 `TestModeForModelWebPipeline`（四种写法都命中、且不得带走其它模型）与
  `TestWebPipelineAliasReachesTestableList`（活跃测试也必须走网页链路）。

### 已知上限

- `doubao/web` 无法指定具体模型，由上游按网页端默认模型回复。这是上游
  `agent_mode=2` 的固有限制，不是插件可以绕过的。
- 账号是否被投放工作模式由上游决定，插件只能检测并给出口，不能改变投放状态。
- 该账号族在同一时段还会回 `710022004 semantic_reasoning`（设备指纹被判定不一致），
  需冷却后复验；与本次 `-1` 的修复相互独立。

## 0.1.21 (2026-11-15)

### 修复

- **推理请求被判风控的真因是设备指纹：`fp` 用了插件自己拼的假值**。上游对
  `/chat/completion` 做设备级归因，`fp`（浏览器 tea SDK 写入的 `s_v_web_id`）
  必须与账号在浏览器里真实用过的那一串一致。旧实现用 `"verify_"+deviceID`
  拼假指纹，等于每次请求都在告诉上游「这是一台没见过的设备」，
  因此自 0.1.2 起面板测试长期报 `710022004 semantic_reasoning`。
  现改为从账号字段 `s_v_web_id` 读取真实指纹，两条 query 管线
  （网页口径 `buildBusinessQuery` 与客户端口径 `buildClientQuery`）都带上它。
- **撤销 0.1.19 的 session 组出站过滤**。0.1.19 把 `sessionid` / `sessionid_ss` /
  `sid_tt` / `sid_guard` 当黑名单从推理请求里剔除，依据是「带上就回 `710022004`」。
  2026-11-15 四象限交替实测推翻了那个对照：真正让请求被判风控的是假 `fp`，
  会话组反而是出字的必要条件。当时的消融把「fp 真伪」与「有无会话组」两个变量
  混在一起改，所以把假 `fp` 的锅记到了会话组头上。现在会话组恢复原样出站，
  只保留「Jar 缺站点访客 Cookie 时补一条 `ttwid`」的兜底。

### 新增

- **授权链路采集设备指纹**。`s_v_web_id` 只在页面上下文可得，插件无法自行生成：
  `X-Doubao-SVWebID` 请求头 + 回调页 `svw` 片段 + `buildAuthScript` /
  `buildBookmarkletScript` 里的 `document.cookie` 读取，一并把值带回并写入账号文件
  （`StoredAuth.SVWebID`，JSON 键 `s_v_web_id`）。
  老账号没有该字段时 query 不带 `fp`（退回旧行为），重新点一次授权书签即可补采。

### 验证

- 四轮交替消融（全部保留真实 session 组，40 秒间隔）：

  | 用例 | `device_id` 等三项 | `fp` | 结果 |
  | --- | --- | --- | --- |
  | A | 插件派生假值 | 格式合法的随机串 | `710022004 semantic_reasoning` |
  | B | 插件派生假值 | 账号真实 `s_v_web_id` | 出字（3.9s / 29 事件） |
  | C | 浏览器真实值 | 账号真实 `s_v_web_id` | 出字（3.9s / 29 事件） |
  | D | 插件派生假值 | 账号真实 `s_v_web_id` | 出字（3.7s / 27 事件） |

  → `fp` 是必需项；`device_id` / `web_id` / `tea_uuid` 用插件派生的稳定值即可，
  `tz_name` 与 `pc_version` 非必需。因此 `deviceIDFor` 的派生逻辑保持不变。
- 逐项消融（保留 session 组）：只换三个标识不带 `fp` → `710022004`；
  补上 `fp` → 出字；再加 `tz_name` / `pc_version` → 仍出字。
- 四象限对照（设备指纹真伪 × 会话组有无）：

  | | 带 session 组 | 无 session 组 |
  | --- | --- | --- |
  | 真实 `fp` | 出字 | `710022002` |
  | 假 `fp` | `710022004 semantic_reasoning` | `710022002` |

- 哨兵 A（把 `fingerprintOf` 改回 `"verify_"+sa.DeviceID`）：
  `TestEndpointURLCarriesRealFingerprint` 与 `TestFingerprintOfReadsAccountField` 全部失败。
- 哨兵 B（在 `Cookies` 里重新剔除 session 组）：
  `TestCookiesKeepsSessionCookiesForSiteHost` 失败并打印缺失的 `"sessionid"`。

### 其它

- `statusCodeHint` 里 `710022004` / `710022002` 的文案同步改口径：从「人工验证码」改为
  「设备指纹与浏览器不一致 / 未采集 `s_v_web_id`」，并直接给出「重新点一次授权书签」的动作。
- `protocol.go` 文件头补一条：设备指纹是比 `msToken` / `a_bogus` 更前置的判据；
  真实 fp 下不带签名也能出字，因此旧的「签名必需」只在假 fp 前提下成立。
- `isRateLimit` 的分类不变（710022004 / 710022005 / HTTP 429 仍走账号冷却换号），
  只把注释里的成因从「人工验证码」改成「设备指纹不匹配」。

### 已知上限

- **必须重新授权一次**：`s_v_web_id` 只能从页面采集，未重新授权的账号仍不带 `fp`，
  依旧是 `710022002`。面板上暂时看不到该字段的采集状态（`signStateOf` 仍只判 `ms_token`）。
- 假 `device_id` + 真 `fp` 能出字只是当前观测结果，上游若改为强绑定 `device_id`，
  仍需把 `device_id` / `web_id` / `tea_uuid` 一并改成采集真实值。
- 0.1.20 把 `710022004` 归因为「人工语义选图验证码」，那是实验方法造成的假象：
  当时用全新 Chrome profile 注入账号 Cookie，浏览器指纹与账号不匹配才触发验证码；
  用户日常 profile 从不弹验证码。该段结论作废，以本段为准。

## 0.1.20 (2026-11-15)

### 修复

- **`710022004 semantic_reasoning` 的真义是上游下发的人工「语义选图」验证码，不是账号风控**。
  2026-11-15 深夜用真实 Chrome 通过 CDP 驱动网页 UI 取证：SSE 里
  `decision.subtype=semantic_reasoning`、`verify_scene=doubao_message_web`
  对应页面上弹出的九宫格验证码（标题形如「在卧室能看到的家具或生活用品」「可以旋转的东西有哪些」），
  且 `decision.log_id` 与验证码面板左下角打印的编号逐字符一致
  （实测 `2026111518494167B16C48BACA19D0275C`）。网页端能出字是因为有真人点图通过验证；
  插件没有任何人工环节，因此必然停在这一步。
- 0.1.19 的文案「上游按环境风险拒绝了该请求……说明请求带上了会话 Cookie 组」与事实不符，
  现改为引导用户在浏览器完成一次验证码：用同一账号登录 `www.doubao.com`，
  随便发一条消息，按九宫格提示把符合条件的图片拖到「拖拽到这里」后提交。
- `710022002` 的文案同步澄清：除了请求过快，它常与「上一次未完成的人机验证」同时出现，
  不再简单归因为频率限制。

### 其它

- `isRateLimit` 保留原有分类（710022004 / 710022005 / HTTP 429），但补注释说明其真实语义是
  「账号级、可换号绕过」，处理动作一致（冷却该账号、切号），只是原因不是限流。
- 验证码有时限：实测第二轮拖拽到一半即提示「验证超时，请根据提示重新操作」，
  因此不能靠慢速自动化补救，只能由人工在浏览器内完成。

## 0.1.19 (2026-11-15)

### 修复

- **出站 Cookie 过滤：4 条 session Cookie 会让推理请求被上游判风控**。`sessionid` /
  `sessionid_ss` / `sid_tt` / `sid_guard` 只要出现在发往 `www.doubao.com` 的请求里，
  上游就稳定回 `710022004 rate limited` 且 `decision.subtype=semantic_reasoning`，
  与请求内容无关。插件此前把导入的 Cookie 原样发给所有域名，导致整池账号 100% 失败。
  现按域名分流：站点域名剔除这 4 条，判据端点（`accounts.doubao.com`）与令牌兑换
  仍保留（它们靠 session 组识别账号身份）。

### 新增

- **替代会话 Cookie 自动预热**。实测只删 4 条 session 而不补 `ttwid` 会拿到
  **空响应**（0.16s、零事件），比报错更难排查。`newUpstreamClient` 在播种后检查
  Jar 是否已有 `ttwid`，没有就 GET 一次 `/chat/` 让站点下发并落盘；
  失败只记录不阻断，请求仍会发出。

### 验证

- 生产端 uTLS（Chrome 指纹 + HTTP/2）精确消融：24 条全量 → `710022004`；
  去掉 5 条 session 组 → `TEXT=true STREAM_CHUNK:14`；单条 `sessionid` 加回 12 条
  → `710022004`。`session_tlb_tag` 单独无害。
- 修复后插件真实路径出站 Cookie：9 条、`blocked=0`、含 `ttwid`；
  判据端点仍保留 session 组（`ACCT_COUNT=1` 只含预热得到的 ttwid 属预期，
  该端点主要看流量而非 Jar 内容）。
- 生产复测：出站 Cookie 修正前稳定 `710022004 semantic_reasoning`，
  修正后该错误码**不再出现**，改为 `710022002`（频率限制）或无错误仅心跳。
- 新增 7 个用例覆盖过滤、判据端点豁免、兜底注入、预热幂等与预热取回；
  哨兵验证（把 `filterHostCookies` 换成直通）触发 `TestCookiesFiltersSessionCookiesForSiteHost` 失败。

### 已知上限

- 修正后 4 条 session Cookie 不再触发 `semantic_reasoning`，但**单账号短时间多打仍会撞
  `710022002` 频率阈值**，端到端出字需在冷却窗口内单次请求。
- 技能模式（`agent_mode=2`）+ 网页 query 在浏览器页面上下文内可稳定出字；
  裸 HTTP 下仍只到心跳，页面环境以外的判据（未知）尚未完全分离。

## 0.1.18 (2026-11-15)

### Fix - 授权回调因判据端点 TLS 握手超时直接失败，一次性令牌被白白消耗

- 现象(用户 2026-11-15 13:10 截图 + 错误原文): 点「豆包一键授权」后浏览器跳到 `login/callback`，页面标题「授权失败」、正文「接收失败」，错误原文是 `判据请求失败：Get "https://accounts.doubao.com/passport/account/info/v2/?…": net/http: TLS handshake timeout`。生产日志显示该次回调耗时 **10.128s** —— 与 Go 默认 `http.Transport.TLSHandshakeTimeout = 10s` 完全吻合；12 个账号文件的 `updated_at` 全部未变、`ms_token len=0`，说明这次授权连账号都没落盘，不是「采到了写不进去」。
- 根因: 插件三处 HTTP 入口（判据 `probeAccountInfoAgainst`、令牌兑换 `doTokenBeat`、上游 `newUpstreamClient`）都写 `&http.Client{Timeout: …}` 字面量，Transport 为空 → 全部落到 `http.DefaultTransport`，握手超时被固定为 10s 且无法调整；同时判据请求**没有重试**。豆包 CDN（`accounts.doubao.com` 共 15 个 A 记录）存在节点级偶发黑洞：单次握手卡满 10s 即抛错，而一次性 `x-tt-token` 已被消耗、无法重来，整条授权就此判死。
- 排除过程(全部实测正常，排除链路故障): 宿主机 `curl` 判据端点 6 次全部 200，`tls` 0.085~1.19s，`remote_ip` 在 4 个节点间轮换；容器内 DNS 两个 A 记录解析 5~7ms；容器内 `openssl s_client` 握手 0.098s；自建 Go 探针把 15 个 A 记录逐个 `tcp`+`tls` 全测一遍 → **`BAD_COUNT=0/15`**，最慢节点 `120.221.208.214` 只是 `tcp=3.091s`；与插件同款「默认 Transport + `client.Timeout=30s`」串行 40 次 `ok=40 err=0`（仅 `#10 3.108s` 偏慢）、并发 10 路×4 轮 `ok=40`。结论：不是持续不可达，而是**单次握手的抖动窗口正好撞上 10s 硬上限**。
- 修复: 新增 `transport.go` 集中定义上游传输层——`sharedUpstreamTransport` 是独立 `*http.Transport` 实例（不复用 `http.DefaultTransport`，避免改它波及全进程），显式设置 `DialContext` 拨号超时 10s、`TLSHandshakeTimeout` **20s**（默认值的两倍，足以让慢节点跑完而不让黑洞无限挂）、`MaxIdleConns` / `IdleConnTimeout`；`newUpstreamHTTPClient(timeout)` 作为唯一构造入口，三处调用点全部改为使用它。
- 重试策略(区分幂等性，避免重复消费令牌): `doIdempotent` 用于判据 GET，传输层失败最多尝试 3 次、间隔 400ms；`doUnsentSafe` 用于令牌兑换 POST，**只在「请求肯定没发出去」时重试**（`requestNeverSent` 判定拨号失败与 TLS 握手超时），握手完成后的任何失败一次即放弃——无法判断上游是否已消费令牌时，宁可失败也不能重复打同一枚令牌。两者共用 `doWithRetry` 内核，带 `GetBody` 的请求在重试时重建读指针。
- 上层取消不重试: `context.Canceled` / `context.DeadlineExceeded` 立即返回，用户关页面或插件卸载时不做无谓等待。
- 测试: 新增 `transport_test.go` 六个用例——`TestSharedTransportHandshakeTimeoutWidened`（必须自建实例、握手超时必须严格大于默认 10s、必须自定义 `DialContext`）、`TestNewUpstreamHTTPClientUsesSharedTransport`、`TestDoIdempotentRetriesTransportError`（前两次握手失败第三次成功，尝试次数 == 3）、`TestDoUnsentSafeStopsAfterHandshakeDone`（连接重置只打 1 次）、`TestRequestNeverSentClassification`（五类错误分类）、`TestProbeUsesSharedTransportAgainstRealServer`（httptest 下判据语义未被破坏）。哨兵把握手超时改回 10s 后 `TestSharedTransportHandshakeTimeoutWidened` 如实 FAIL（`握手超时 10s 未超过默认 Transport 的 10s`），恢复后全绿。
- 兼容: 只放宽传输层参数与增加重试，不改任何请求内容、判据逻辑与落盘路径；`tokenbeat` 与 `completion` 的业务分支保持原样。
- 已知上限: 重试只能吸收「单次抖动」，若某个 CDN 节点持续 20s 以上不可用仍会失败；`requestNeverSent` 对 TLS 握手超时按 transport 的错误文本判定，Go 升级若改动该文案需同步维护。

## 0.1.17 (2026-11-15)

### Fix - 补齐 ms_token 后写盘必然失败，面板长期显示「请求签名未采集」

- 现象(用户 2026-11-15 12:16 截图 + 12:21 反馈「我们已经用了新的授权书签, 还是这样」): 用 0.1.16 的新书签重新授权，账号 `updated_at` 确实刷新到 `2026-11-15T12:16:27+08:00`（说明令牌兑换与落盘都成功了），但 `请求签名` 仍是「未采集（需重新授权）」，12 个账号无一例外。
- 生产日志(决定性证据): `2026/11/15 12:16:27 [doubao] save ms_token failed for doubao-adcd31d29873b4c8: 不安全的凭据文件路径：` —— 冒号后为空。据此可判定 `msToken` **确实取到了**（`if msToken != ""` 才进写分支），失败的是落盘路径。
- 根因: `login.go` 用 `writeAuthFileDirect(sa.PhysicalPath, sa)` 补写 msToken，而 `saveCredentials` 构造 `StoredAuth` 时从不给 `PhysicalPath` 赋值、落盘后也不回填，该字段恒为空串；空路径被 `isSafeDoubaoAuthPath` 判为不安全直接拒绝。0.1.16 新增的采集链路因此从头到尾没生效过。
- 第二层根因(更隐蔽): 即便路径不为空，插件写的是自己账号目录 `~/.antigravity_cockpit/doubao_accounts/`，而面板读的是**宿主凭据目录**（生产为 `/usr/local/src/cli-proxy-api/auths/`）。仅修空路径仍会出现「写成功但面板还是未采集」。
- 修复: 新增 `resolveAuthFilePath(authID, physicalPath)` 作为「凭据到底写哪儿」的唯一判据——显式路径 > 账号自带物理路径 > 按文件名向宿主 `host.auth.list` + `host.auth.get` 回查真实路径 > 退回插件账号目录；`writeAuthFileDirect` 在 `filePath` 为空时自动调用它，落盘后把真实路径回填进 `auth.PhysicalPath`，并把「宿主已注册」的物理路径也同步回填，保证后续追加写入（ms_token、Cookie 续期）打到同一份文件。`login.go` 补写 msToken 前显式 `resolveAuthFilePath(sa.AuthID, sa.PhysicalPath)`。
- 并发安全: 路径解析会走宿主 RPC 再间接调 `getAccountsDir()`（内部拿 `authFilesMu` 读锁），而写盘要拿写锁；`sync.RWMutex` 不可重入，先前的写法会自锁。本轮把路径解析与宿主同步移出临界区，写盘收敛到 `writeCredentialFile` 独占锁。
- 可观测性: 补写 msToken 增加成功日志（`ms_token saved for <id> (len=…, path=…)`），并对「msToken 为空」明确记日志指出多半是书签栏里仍挂着 0.1.16 之前的旧书签——原实现头为空时连日志都不打，故障静默。
- 兼容: 老账号无需改数据；重新点一次书签即可补齐。已采集过 `ms_token` 的账号不受影响（路径解析对非空路径直接透传）。
- 测试: 新增 `TestWriteAuthFileDirectResolvesEmptyPath`（显式传空路径必须落盘成功、`ms_token` 必须保留、物理路径必须回填、回填后二次写入不得产生第二份文件）；哨兵把路径解析换成固定非法串后该用例如实 FAIL，恢复后全绿；`python scripts/cgo-shim-build.py doubao` build / vet / test 全绿。
- 生产发布(2026-11-15 12:56): CI run `43678186057`（`Release` success，5 平台，`darwin/arm64` 按惯例 cancelled）→ `doubao-provider-v0.1.17` → registry `8a74c5d` → `plugin-store install` 200（第 7 次重试成功，前 6 次回 `502 plugin_manifest_invalid / direct plugin version "0.1.17" not found`，属 registry CDN 缓存尚未刷新）→ 热重载 `[2026-11-15 12:56:08] active_version=0.1.17 retired_version=0.1.16`；生产 `.so` sha256 `36be78f8c525aa72bd84f7658aca8a11f0ef289153737d7aeee86a4430fd0824`（17506880 B）与本地 zip 内逐字节一致；13 插件启用态快照 diff 为空（`STATE_UNCHANGED`）。
- 待用户补齐: 生产 12 个账号 `sign_state` 仍全为 `missing`（老账号未采过 `ms_token`），且此刻同时处于 `-1` / `semantic_reasoning` / `710022002` 三种上游风控分支；需用户再点一次授权书签，届时日志应出现 `[doubao] ms_token saved for <auth_id> (len=…, path=/usr/local/src/cli-proxy-api/auths/…)`。

## 0.1.16 (2026-11-15)

### Fix - `/chat/completion` 缺 msToken 与 a_bogus 签名，10 个账号对话被上游拒绝（用户反证推翻 0.1.15 定性）

- 现象(用户 2026-11-14 22:54 三张截图): 面板「测试」对 `号10` 测 `doubao/deepseek-v4-flash` 报 0.1.15 的「账号级频率限制」文案；但同一账号同一时刻在豆包网页版(`doubao.com/chat/38453820519002882`，模型「DeepSeek V4.1 Flash 高」，22:09)和 PC 客户端(22:11)输入 `hi` 都能正常回「Hi! What can I help you with today?」。用户据此指出「我们用网页版、pc端客户端没有任何问题, 说明插件有错误」——该判断成立，0.1.15 的「账号级拒绝」定性错误。
- 根因(逐项增删控制实验，同一 URL 与 body): `/chat/completion` 必须携带 `msToken` 与 `a_bogus` 两个 query 参数。都不带 → 秒回 `710022002`；只缺 `a_bogus` → `710022004 rate limited`；只缺 `msToken` → `710022002`；两者齐全且 `msToken` 真实 → `SSE_ACK` 正常出字。填随机值等同缺失，格式合法的伪造 base64 也不行。插件此前**从不发送这两个参数**（`grep` 全仓仅命中 `protocol.go` 一条注释），故对所有未获免签放行的账号必然失败。
- 已排除的旁路(全部实测无效): 出口 IP(生产与住宅 IP 同样失败)、query 参数集(`aid=497858`/`582478`、`device_platform`、`channel`、版本号四组)、body 结构(工作模式 vs 技能模式)、`ttwid`/`s_v_web_id` 等访客 Cookie、浏览器全部请求头、HTTP 版本(同为 h2)、Chrome TLS 指纹(curl_cffi 八种 `impersonate` + uTLS `HelloChrome_Auto` 走 h2 全部失败)、device_id(2×2 实验: 失败只跟随 Cookie/账号，与该账号 device_id 无关)。
- 签名事实(2026-11-15 实测): `msToken` 由 `mssdk.bytedance.com` 下发并写入页面 `localStorage.xmst`，有效期 7 天，**不在 `.doubao.com` 的 `document.cookie` 里**；`a_bogus` 由豆包官方 `bdms-sdk` 现算，有效窗口约 60~120 秒，不绑定 body 与 `web_tab_id`，跨账号也能用。`a_bogus` 生成算法跑在混淆解释器内且带 11 位环境探测位图(`dom/node/phantom/webdriver/hook/location/consistent/incognito/debugger/switch/test`)，静态复刻不可行（全站 383 个 JS 里 `a_bogus` 字面量只出现在一处 cache omit 名单里）。
- 修复(内嵌官方 SDK + goja 进程内还原): 新增 `sign.go`，用 `//go:embed` 内嵌 `assets/bdms-sdk.js`(163 KB 官方 SDK) + `assets/bdms-shim.js`(BOM 垫片) + `assets/bdms-xhr.js`(prototype 级 XHR 桩)，在 goja Runtime 里按页面真实配置调用 `bdms.init({aid:497858, pageId:26930, paths:{include:[…], exclude:[…]}, ic:13, ddrt:13})` 安装请求 hook，再由 SDK 自己把 `a_bogus` 追加到 URL 上。`btoa`/`atob` 走 Go 侧按 charCode 逐字节实现（用 Go `string` 直接传会破坏二进制语义）。签名器进程内单例，SDK 加载约 0.5 秒、单次签名约 200ms。
- msToken 采集(只能借用户浏览器): 插件自己换不出来——`POST https://mssdk.bytedance.com/web/r/token?ms_appid=497858` 需要 `strData`（设备侧加密串），缺它一律 `resultCode:-3`。故 `login.go` 的一次性授权脚本与固定书签脚本都改为在取 `x-tt-token` 的同时读取 `localStorage.xmst`，经 `X-Doubao-MsToken` 头随回调带回，`handleLoginCallback` 在 `saveFromToken` 成功后补写进账号文件；写失败只记日志、不阻断授权（账号本身可用，只是退回未签名分支）。
- 状态可见: `StoredAuth` 新增 `ms_token` 字段；`/accounts` 新增 `sign_state`（`ok` / `missing`）；面板在账号卡片「登录态」行下方对 `missing` 显示「未采集（需重新授权）」并给出补齐方式。
- 兼容与迁移: 老账号（0.1.15 及之前导入）没有 `ms_token`，需在豆包已登录页面上点一次固定书签（或重新走一次授权）补齐；未补齐前仍会被上游按未签名拒绝，行为与 0.1.15 一致，不会更差。
- 协议注释校正: `protocol.go` 文件头原写着「msToken 与 a_bogus 均不在必需参数中——上游接受无签名调用」，已改为实测结论并注明该记载的成因（当时样本账号恰好被投放为免签名）。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿；`sign_test.go` 三个用例用真实 SDK 断言「补齐 msToken + a_bogus」「连签两次 a_bogus 不同（防缓存短路）」「非上游地址不签名」；`panel.html` 两个 `<script>` 块 `node --check` 通过；哨兵把 `a_bogus=` 断言改为哨兵串后 `TestSignAddsMsTokenAndABogus` 如实 FAIL，恢复后全绿（证明新用例真实进编译）。
- 生产发布(2026-11-15 01:29): CI run `43620812289`（`Release` success，仅 darwin/arm64 cancelled）→ `doubao-provider-v0.1.16` → registry `00f0ef8` → `plugin-store install` 200 / 2.678s → 热重载 `active_version=0.1.16 retired_version=0.1.15`；生产 `.so` sha256 `8f7716a1f6bff620ba129e76773b3895278e822ef99882acdb1b83cbeb9e74b9`（17498688 B）与本地 zip 内逐字节一致；13 插件启用态与发布前逐字段一致。
- 生产签名链路验证(2026-11-15 01:35，临时注入 `ms_token` 后已回滚): 同一 body、同一 Cookie 下，带签名 → `http=200 ack=True`（错误码落到账号级 `-1` / `semantic_reasoning`），去掉签名 → `http=200 ack=False err=710022002`（未签名分支）。**插件侧生成的 `a_bogus` 已被上游接受**。12 个账号此刻仍在上游风控中，尚未观察到「补齐 ms_token 即成功出字」的成功态；验证用的临时 `ms_token` 已从三个账号文件恢复（`ms_token=None`、`cookies=11` 不变）。
- 已知上限: 12 个账号的 `sign_state` 仍为 `missing`（本轮未做真实书签授权闭环）；`msToken` 过期（7 天）后需重新点一次书签；goja Runtime 串行，单次签名约 200ms，高并发下签名是瓶颈。

## 0.1.15 (2026-11-14)

### Fix - 面板测试失败提示不再只回显上游原文（-1 与 710022002 给出账号级定位）

- 现象(用户 2026-11-14 21:15 面板截图): 点「测试」对 `用户766588` 测 `doubao/deepseek-v4-flash`，toast 只显示「测试失败（模型: doubao/deepseek-v4-flash）：上游错误 -1：delegated input protocol violation」，看不出是插件问题、账号问题还是模型问题。
- 定位(12 账号全矩阵 + 跨模型交叉 + 包体检索): 该串在上游 `error_code=-1` 时下发，插件只是经 `statusCodeHint` 兜底分支原样转述。同一账号换 `doubao/kimi-k3`、`doubao/auto` 仍报 `-1`，同一模型换 `欧皇小德子` / `用户158477` 则正常出字——**错误跟随账号、不跟随模型**。`delegated input protocol violation` / `protocol violation` 在豆包桌面端 `local_webcontents` 全量包体、插件仓库、宿主 checkout 三处均零命中，确认来自上游服务端。
- 分布(生产 `POST /test-active`，`doubao/deepseek-v4-flash` 连跑 3 轮): 2 个账号成功（`doubao-adcd31d29873b4c8` 5.2/7.3/4.9s、`doubao-c754e5d56ed62480` 7.1/6.3/4.3s），6 个回 `710022002 当前服务访问频繁`，4 个回 `-1`（`doubao-489b6e70ebde9be1` / `doubao-aecd88a5b0aad93a` / `doubao-c0b2098a84b94d69` / `doubao-f37929bdf3a6a7dc`）。12 个账号的 `bot_id` 与 11 个 Cookie 键名完全一致，逐字段 diff 只有身份与时间字段不同，不存在 Cookie 缺失或账号文件结构差异。
- 修复: `protocol.go` 的 `statusCodeHint` 为 `-1` 与 `710022002` 补可操作中文文案，明确指出「属账号级限制、Cookie 与登录态仍有效、换用其他账号即可恢复」，并区分于请求参数问题（`710020202`）与登录态失效（`710012001`）；未收录的码仍原样回显上游描述。`completion_test.go` 的 `TestStatusCodeHint` 补两条断言。
- 不动的部分(刻意): `isAccountFailure` / `shouldRotate` 白名单不变。实测这两个码会同时落在多个账号上（12 个里 6 个 `710022002`、4 个 `-1`），一旦计入阶梯冷却会把整池逐个锁死，只剩 2 个健康账号承载全部流量；宿主 scheduler 已在插件返回错误后自行换号重试（生产日志同一 request id 连续试号并以 `failed=false` 收尾），插件层不重复烧预算。`test-active` 保持「单账号严格探针」语义，不改成 failover 跑法。
- 真实可用性: 生产 `/v1/chat/completions` 对 `doubao/deepseek-v4-flash` 连测 6 次全部 `http=200` 且正文正确（`7`），单次 6.8~7.5s；面板「测试失败」只代表该单账号此刻不可用，不代表插件不可用。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿（`ok ... 5.7s`）；`gofmt -l` 对两个改动 `.go` 文件无输出；哨兵把 `-1` 期望改为「哨兵刻意失败串」后 `TestStatusCodeHint` 如实 FAIL，恢复后全绿，证明新断言真实进编译。

## 0.1.14 (2026-11-14)

### Feat - 签到状态与活动赠送额度改为账号卡片内联展示（取消独立区块）

- 需求(用户 2026-11-14): 签到与额度不再放在账号列表上方一个悬浮的大区块里，改为直接在每张账号卡片内展示；原来那种「一排账号 + 独立区块」的呈现与卡片信息割裂，用户需要在两处对照同一个账号。
- 面板改动: 删除 `#checkinBox` 独立容器、`.checkin` / `.checkin-hd` / `.checkin-list` / `.checkin-item` / `.ci-name` 五组 CSS 与整个 `renderCheckin()`；新增 `.ck-panel` 系列样式与 `checkinBlock(a)`，签到区块插在每张卡片的「更新时间」行之后、操作按钮之前，含状态徽章 + 连续天数 / 活动累计 / 今日可得 / 今日已签|未签 + 赠送额度窗口 + 上游提示。
- 首屏时序: `loadCheckin()` 改为只拉数据不渲染（按 `auth_id` 建索引写进模块级 `lastCheckin`），`load()` 走 `loadCheckin().then(()=>renderGrid())` —— 先出账号卡片再补签到数据，避免 12 个账号约 4 秒的上游查询把整块列表卡住；手动签到后 `await loadCheckin(); renderGrid();` 重绘。
- 文案校正(真实客户端取证 2026-11-14): 「需桌面端领取」改为「平台限桌面端」。原文案暗示用户可以自己去桌面端点一下就能领，实际不行——在真实豆包桌面端客户端 2.35.12 进程内用 CDP 对平台自己的「立即签到」按钮做可信点击（`Input.dispatchMouseEvent`），带平台自己生成的 `msToken` + `a_bogus`，请求与响应照样是 `{"data":null,"code":710010702}`，且点击后 `checked_in_today` 仍为 false、`progress.total_amount` 仍为 0。`checkinStateText` / `campaignCodeHint` / 面板 toast 三处文案统一改成「平台按账号投放策略拒绝」口径，不再引导用户去桌面端做无效操作。
- 未做兼容: `desktop_only` 状态枚举值不变，只改文案与提示，管理路由响应结构、定时器、额度递归、`protocol.go` / `models.go` / `completion.go` 零改动。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿（`ok ... 6.3s`，14 个用例）；`gofmt -l` 对两个改动 `.go` 文件无输出；`panel.html` 两个 `<script>` 块（44 行 + 904 行）经 `node --check` 通过；`checkinBlock` 运行时冒烟 13 项断言全过（加载态 / done 徽章与额度文案 / desktop_only 徽章与「平台限桌面端」/ quota_error 降级 / 缺失账号 / 全局错误 / 空额度）。

## 0.1.13 (2026-11-14)

### Fix - 桌面端渠道账号被误判为「签到活动已下线」，12 个账号里 10 个状态错误

- 缺陷(生产实测 2026-11-14 17:47): 0.1.12 上线后 `GET /checkin/status` 对生产 12 个账号只给出 2 个正确状态，其余 10 个（`号10` / `用户358840` / `用户288838x` / `第七` / `用户220908` / `用户766588` / `用户815917` / `用户825285` / `用户305270` / `用户224666`）全部 `state=error` + `message=读取签到活动失败：签到活动已下线或已换季`。
- 根因: 判据顺序错了。`checkinStatusFor` 与 `runCheckinForAccount` 都先查活动详情 `v3/detail`，详情回非 0 就整条失败；但**桌面端渠道账号在网页侧没有活动实例**，其 `v3/detail` 必然回 `{"data":null,"code":2,"message":"campaign not found"}`，而同一账号的 `landing/get` 是正常的 `{"chosen_type":3}`。于是「需桌面端领取」被错报成「活动已下线」，文案与真实原因完全不符，用户会被误导去排查活动是否换季。
- 上游取证: 生产宿主机直打两账号对比——`doubao-adcd31d29873b4c8.json`（欧皇小德子）`v3/detail` → `code=0`（含 tasks 与 example_cases）、`landing/get` → `{"chosen_type":0,"landing_cards":[]}`；`doubao-20fcbcfb49f40321.json`（号10）`v3/detail` → `{"data":null,"code":2,"msg":"","message":"campaign not found"}`、`landing/get` → `{"chosen_type":3,"landing_cards":[{"card_code":"web_pc_download_banner",...,"content":"下载桌面端"}]}`。`landing/get` 才是渠道判据的唯一真相源。
- 修复: `checkin.go` 两处判据顺序统一改为「登录态 → **落地页 `chosen_type` 先行** → 网页渠道才拉详情 → 三态判定 → 额度降级」。桌面端账号在落地页判定处直接返回 `desktop_only`，**完全不请求活动详情、不产生任何签到写请求**；步骤编号同步重排（`checkinStatusFor` 2/3/4/5、`runCheckinForAccount` 2/3/4/5）。
- 测试: 新增 `TestCheckinStatusForDesktopAccountIgnoresDetailError` 锁死本次生产缺陷——桩里 `v3/detail` 回 `code=2 campaign not found`、`landing/get` 回 `chosen_type=3`，要求判 `desktop_only` 且**不请求详情路径**、不请求签到路径；`TestRunCheckinSkipsWhenAlreadySigned` 补显式 `checkinLandingPath` 桩，避免新顺序下靠默认响应蒙对。用例数 13 → 14。
- 哨兵验证: 把落地页分支条件临时改成 `if false && chosenType != checkinChosenWeb` 后 `TestCheckinStatusForDesktopAccountIgnoresDetailError`（`checkin_test.go:203`）与 `TestRunCheckinSkipsDesktopChannel`（`checkin_test.go:230`）双双 FAIL，恢复后全绿——证明新用例真实进编译并守住了顺序。
- 影响范围: 仅 `checkin.go` 两个函数的判据顺序与 `checkin_test.go` 用例、`VERSION` / `main.go` 版本号；路由、面板、定时器、额度递归、`protocol.go` / `models.go` / `completion.go` 零改动。

## 0.1.12 (2026-11-14)

### Feat - 每日签到与活动赠送额度（面板可见 + 手动签到 + 每日两轮定时）

- 功能: 面板新增「今日签到与免费额度」区块与工具条「今日签到」按钮。每个账号一行：签到状态徽章（今日已签 / 今日未签可签到 / 需桌面端领取 / 登录态过期 / 签到失败）+ 连续天数 + 活动累计额度 + 今日可得额度 + 赠送额度窗口（已用/总量/百分比）。新增管理路由 `GET /checkin/status`（query `auth_index` 可只查单账号）与 `POST /checkin/run`（body `{auth_index?}`，缺省全部账号）。
- 定时: 每天 `10:00` 与 `22:00` 各跑一轮全账号签到（`checkinHours = []int{10, 22}`）。两轮是刻意冗余——上午拿当天额度，晚间兜住上午因上游抖动漏签；重复提交天然幂等（上游回 `idempotent=true`），不会重复发放。
- 契约: 签到 `POST /alice/ug/campaign/landing/checkin/` body `{"scene":"pc_incentive_task_mvp_v3"}`；活动详情 `POST /alice/ug/campaign/v3/detail/` body `{"campaign_group_code":"pc_incentive_task_mvp_v3"}`；落地页 `POST /alice/ug/campaign/landing/get`；额度 `POST /alice/commerce/sale/subscription/quota/summary/` body `{}`。四个端点全部复用既有 `postBusinessJSON`（同一套 19 键网页端 query + 网页端指纹），未新造请求栈。
- 渠道判据（关键）: 落地页响应的 `data.chosen_type` 决定该账号能走哪条入口。`chosen_type=0` 网页签到可成功（回执 `checked_in=true`、`reward_amount=200`）；`chosen_type=3` 被平台改投桌面端（`landing_cards[0].card_code="web_pc_download_banner"`、副标题「前往豆包桌面端即可领取」），任何参数组合都回 `710010702`。生产两账号实测确认，判据可靠。
- 渠道分流的硬约束: 只在 `chosen_type=0` 且今日未签时才提交签到；`chosen_type=3` 只上报「需桌面端领取」，**完全不产生签到写请求**。桌面端形态详情的 `tasks[]` 里根本没有 `daily_signin`（web 形态 7 条含它，客户端形态 5 条不含），说明平台按渠道裁剪活动，不是参数缺失。
- 成功判据: 单个 `code=0` 不算成功。提交后等 2s 回查详情，只有 `daily_checkin.checked_in_today=true` 才判 done，否则如实报「已提交但上游未确认到账」。该口径与 `L0NE-6/DoubaoWork-Daily`、`hiclaw168-163/auto-signin-kit` 两个开源实现一致。
- 风控红线: 不伪造 `biz_params` / `AgwCommonParam` / `a_bogus` 等 SDK 风控字段；不做任务奖励自动领取（`item/reward/claim` 对 `daily_signin` 只回 `biz_error.code=3 领取结果确认中`，不是有效签到路径）；不加后台探活。已签账号完全不发写请求，避免重复裸提交触发上游异常流量判定。
- 额度读法: `quota/summary` 的 `window_limits` 数组位置由上游决定，故按 key 名递归提取，读 `feature_group_name` / `total_amount` / `used_amount` / `used_percent` / `end_time`；数值兼容数字与带千分位/货币符号的字符串。已签账号才有活动赠送额度窗口（生产实测 200/200，到期 `1797237901995`）。
- 代码落点: 新增 `doubao/checkin.go`（接口调用、状态判定、递归额度提取、定时器）与 `doubao/checkin_test.go`（13 个用例）；`doubao/management.go` 登记 2 条路由 + 2 个分发分支；`doubao/main.go` 在 `configurePlugin` 末尾幂等拉起签到定时器；`doubao/panel.html` 新增签到区块与按钮。`protocol.go` / `models.go` / `completion.go` 零改动（`buildBusinessQuery` / `endpointURL` / `clientUserAgent` 全现成复用）。
- 定时器取舍: 与 qoder 系插件一致，故意不提供停止函数——宿主在自身运行时拆卸阶段调用插件 shutdown，此时从 c-shared 运行时触碰 `sync` 原语会让进程 SIGSEGV，故 `ensureCheckinScheduler()` 用 `checkinSchedulerOn` 布尔做幂等，重复 reconfigure 不叠加 goroutine。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿（`ok ... 8.2s`）；`panel.html` 两个 `<script>` 块经 `node --check` 语法校验通过。生产两账号真实签到实测：账号1 `chosen_type=0` 拿到幂等回执 `checked_in=true / reward_amount=200`，账号2 `chosen_type=3` 被正确识别为「需桌面端」且未提交。

## 0.1.11 (2026-11-14)

### Fix - 工作模式全模型 503：顶层 `option.conversation_mode` 写成字符串被上游拒绝

- 缺陷(用户反馈 2026-11-14): 通过别名 `deepseek-pro` 请求 `doubao/deepseek-v4-pro` 报 `503 auth_unavailable ... 上游拒绝了请求参数：请确认 Content-Type 为 application/json 且请求体结构完整`。实测不止 pro 别名——`doubao/deepseek-v4-flash`、`doubao/deepseek-v4-pro`、`doubao/default`、`doubao/auto` **四个工作模式模型全部同错**，且同一时刻两个账号各失败一次，说明是请求体被上游参数校验拒绝，不是模型档位或账号问题。
- 根因: 顶层 `option.conversation_mode` 被写成了字符串（`strconv.Itoa` → `"2"`），而上游对该键要求 **JSON 整数**。上游返回的是 HTTP 200 + SSE 错误帧 `{"error_code":710020202,"error_msg":"common invalid param"}`，心跳帧先到、错误随后，故失败耗时仅 130~190ms（正常生成 880ms~1.2s）。**同名不同型陷阱**：`aggregate_params.conversation_mode` 反而必须保持字符串 `"2"`，两处类型不同、不可互换。
- 定位过程: 在宿主机用同账号 Cookie 复刻请求直连 `https://www.doubao.com/chat/completion` 做逐级累加消融（S0→S9）。S0~S6 全 OK，从 S7（同时加入顶层 `conversation_mode="2"`、`message_storage_type=0`、`sse_recv_event_options`+`support_lazy_fetch_stream`）起 ERR；再拆分 S7 三字段，`allbutconv`（去掉 conversation_mode）OK、`conv2int`（写整数 2）OK，最终三变体决定性对比：`"2"` → ERR、`2` → OK、缺键 → OK。
- 修复: `completion.go` 的 `completionOption.ConversationMode` 类型由 `string` 改为 `int`（保留 `omitempty`），赋值由 `strconv.Itoa(workConversationMode)` 改为 `workConversationMode`；`aggregateParams.ConversationMode` 维持 `string` 不动。字段补注释写明该陷阱，避免后续再被"统一类型"改回字符串。
- 验证: `python scripts/cgo-shim-build.py doubao` → build / vet / test 全绿；整数写法下上游完整跑通，事件序列 `SSE_HEARTBEAT,SSE_ACK,FULL_MSG_NOTIFY,STREAM_TIMEOUT_CONTROL,STREAM_MSG_NOTIFY,STREAM_CHUNK,...,SSE_REPLY_END`，`deepseek-v4-pro` 与 `deepseek-v4-flash` 均返回正文（`1+1=?` → `2`），无 error 帧。
- 影响范围: 仅请求体该字段类型与赋值，以及 `workmode_test.go` 对应断言；路由、failover、SSE 解析、面板与账号逻辑零改动。

## 0.1.10 (2026-11-14)

### Fix - 面板搜索框被浏览器表单历史自动填回上次搜索词

- 缺陷(用户反馈 2026-11-14): 面板刷新后搜索框会自动填入上次的搜索词，账号列表被旧关键字过滤，看起来像账号丢失。根因是浏览器把 `type="search"` 输入框当成可记忆的表单字段，重载时用表单历史恢复 `value`，与 `autocomplete="off"` 无关——浏览器对同名字段的恢复不受该属性约束。
- 修复: `panel.html` 新增 `searchInputNoAutofill()` 自执行函数——加载时把输入框 `name` 改成每次随机的 `q<random>`，浏览器无法用固定字段历史命中它；同时补齐 `autocorrect="off"`/`autocapitalize="off"`/`spellcheck="false"`/`data-form-type="other"`，压掉移动端与密码管理器启发式填充。
- 放行真输入: 用 `pointerdown` / `keydown` 记录「用户确实操作过」，只有未操作过才在 `focus`、`pageshow` 与 0/60/200/600/1200/2500ms 六个短窗口内清空 `value` 并复位 `currentSearch` + 重渲染，避免浏览器在首帧之后才恢复历史值；用户主动输入的搜索词不会被清掉。
- 影响范围: 仅面板前端——搜索输入框属性与上述自执行清理函数，账号列表渲染、筛选语义、签到/导入/导出等全部链路零改动；后端与插件协议无变化。
- 验证: `python scripts/cgo-shim-build.py` build / vet / test 全绿；10 个插件面板同源改动逐一落地（`searchInputNoAutofill` 与 `data-form-type` 各命中 2 处）。

### Fix - 面板「测试模型」列表错乱：列出上游内部代号，且点任何代号实际都跑默认模型

- 缺陷(用户反馈 2026-11-14): 面板测试弹窗列的是 `doubao/4tbp`、`doubao/anceba`、`doubao/ances brs` 这类豆包内部代号，与宿主「认证文件管理 → 支持的模型」里的可读别名对不上；更严重的是点任何一个代号，「测试成功」提示里的模型名会回显代号，实际请求却回落到了 `deepseek-v4-flash`，测试结论与所选模型无关。
- 根因一(列表来源错): `handleModelsQuery` 直接把 `/alice/basic/launch` 返回的上游代号当模型列表下发，完全没有并入插件别名目录；宿主那侧走的是 `modelForAuth → catalogueWithUpstream`，别名在前、代号追加在后，所以宿主看着正常。
- 根因二(静默替换): `modeForModel` 对不认识的名字一律回落 `defaultWorkModelID`（正式对话链路需要这个宽松语义，避免客户端传怪名就 400），活跃测试复用了它，于是代号被悄悄换成默认模型，而成功消息只回显客户端传入的名字。
- 修复一: 新增 `testableAliases` / `testableModelIDs` / `testableModelDetails`，测试可选模型固定为插件别名目录（同 `modelForAuth` 同源），只排除 `image` / `video`——它们会真正触发上游生成，既耗额度又要等数十秒，失败原因还与账号不可用难区分。
- 修复二: `/models` 改为先返回别名目录（`source: aliases`，附 `details` 供面板显示可读名与「默认」标记），上游代号只作为 `upstream_models` 附带对照并给 `notice` 说明「代号无法映射到具体模型，故不参与测试」；上游不可达时只降级掉代号，别名目录照旧完整。
- 修复三: 新增 `aliasForTestModel` / `testModeForModel`，测试入口做严格解析——未命中别名直接返回「不支持的测试模型：…（请从模型列表中选择插件别名；上游内部代号无法映射到具体模型）」，不再静默替换；`handleTestActive` 缺省模型改为 `doubao/deepseek-v4-flash`。
- 修复四: 成功消息回显真正打到的上游主键——`活跃成功 (模型: doubao/glm-5.3 → 上游 person-glm-5-3-261111, 耗时: …)`，模型是否真的生效一眼可辨。
- 面板: 测试弹窗每项改为「可读名 + 默认标记 + 模型 id」两行显示（`.mi-name` / `.mi-id` / `.ref-tag`），鼠标悬停展示说明；`details` / `upstream_models` 解析全部对缺失字段做防御。
- 新增回归: `TestHandleModelsQueryKeepsAliasCatalogue`（上游不可达仍给完整别名目录且必含工作模式默认模型）、`TestHandleModelsQueryNeverListsUpstreamCodes`（代号不得进入可点击列表，只出现在 `upstream_models`）、`TestRunActiveTestRejectsUnknownModel`（未知模型名必须报错而非静默成功）。

## 0.1.9

- 账号授权改为「书签一键授权」：面板新增「豆包一键授权」入口，打开引导页后把授权书签拖进书签栏（只做一次），之后在豆包页面登录完点一下书签，插件即用 x-tt-token 换回完整登录态并落盘。豆包的 sessionid / sid_guard 是 HttpOnly，页面脚本读不到，此前必须让用户自己开开发者工具复制 Cookie。
- 新增令牌兑换链路 tokenbeat.go：凭据换发端点 /passport/token/beat/v2/（与扣子插件同一 passport 网关），令牌只兑换一次且立即消费，插件内存不保留；缺 flow_cur_user_sec_id 时用判据端点返回的 sec_user_id 补齐，再复用既有落盘链路，保证设备标识固化口径与手工导入一致。
- 新增授权回调资源 /login/callback：令牌经 URL fragment（浏览器不回传服务端）加同源请求头递交，不进 query，避免令牌落进宿主访问日志；回传成功后立即 history.replaceState 清掉 fragment。
- 授权会话支持失败原因回传：脚本取不到令牌时把真实原因写进会话，宿主轮询能显示「上次尝试失败：…」，不再只显示「等待中」。
- 授权链路对固定书签（无 state）与面板独立入口（无宿主弹窗）做兼容：认领等待中的会话让弹窗自动关闭，没有会话时提示回面板刷新。
- 手工粘贴 Cookie 降级为兜底通道（details 内），桥接页另一条兜底给出一次性脚本，供浏览器策略拦掉 javascript: 书签时使用。
- 新增回归：tokenbeat_test.go 覆盖令牌兑换成功/业务失败/零 Cookie/空令牌、令牌落盘、书签与脚本生成、公网来源取值优先级、回调页首开/失效 state/严格与宽松双模/重放幂等/脚本报错、回调路由注册与分发。

## 0.1.8

- 管理面板整页重写为与 workbuddy 同源形态：密钥零输入自动获取（`enc::v2::` /
  `enc::v1::` 双格式解码、`sessionStorage` 脏密文自愈清理、`?key=` 取后抹痕），
  401 累计 3 次才清 key 并进入 60 秒冷却，403 命中 IP 封禁时按上游给出的等待窗口
  退避，空响应 / 非 JSON / 坏 JSON 一律降级成 `{error}` 不再抛异常。
- 面板新增账号筛选（全部 / 可用 / 已禁用 / 过期）与汇总卡，排序支持创建倒序、
  账号标识升序、备注名升序；卡片展示备注名、昵称、手机号、脱敏账号标识、
  登录态、创建与更新时间，动作含测试 / 启停 / 删除（删除改为二次确认弹窗）。
- 导入改为弹窗批量导入：粘贴文本框一次可贴多个凭证，另支持多选 `.json` 文件
  （单文件上限 2MB），自动展开裸对象 / 数组 / 导出包装 `accounts[].credential`
  三种形态；逐条串行提交，命中上游限流自动重试 4 次，结束给出逐条结果。
- 新增导出能力两条管理路由：`GET /export` 输出单文件 JSON 汇总（含账号元信息与
  原始凭证），`GET /export/zip` 每账号一个 JSON 打包成 zip；导出内容原样取账号
  物理文件字节，保证「导出 → 导入」闭环不被重新序列化破坏。
- `POST /import` 载荷放宽：除原有 `{cookie, label}` 外，兼容 `{raw}`、`{json}`、
  导出汇总/单账号凭证对象，同一条导入链路可回灌导出文件。
- 新增回归：`export_test.go` 覆盖导入载荷三种形态、导出汇总路由、ZIP 路由与
  文件命名去重、空账号 404、导出路由注册。
## 0.1.7

- 新增工作模式管线（`agent_mode=1`）：按豆包 PC 客户端指纹直连
  `https://www.doubao.com/chat/completion`，请求体组装 `general_task_param` /
  `conversation_init_ext` / `option.ext` 三处工作模式参数，正文与推理内容按
  `STREAM_CHUNK` / `CHUNK_DELTA` 解析。技能管线（生图 / 生视频，
  `agent_mode=2`）保持原样，两条管线由 `generationMode.WorkMode` 分流。
- 账号指纹改为客户端形态：请求 query 用 19 键 PC 客户端参数集（`aid=582478`、
  `channel=win`、`client_platform=pc_client`、`web_platform=desktop`、
  `chromium_version=147.0.7727.149` 等），每次请求轮换 `web_tab_id`；请求头补
  `agw-js-conv: str, str` / `last-event-id: undefined` / `x-flow-trace`。
  业务端点（`/alice/*`）与模型发现仍走原网页端指纹，互不影响。
- `device_id` 由 Cookie 摘要派生并固化：导入账号时按 `sha256(CookieHeader+AuthID)`
  取前 16 字节逐位取模拼成 18 位数字串（前缀 `73`），同账号重复导入得到同一标识，
  避免被上游按多设备登录归因。`web_id` 缺失时同源补齐。
- 模型目录扩为两层：能力别名（default / pro / image / video）+ 工作模式模型
  （auto / doubao-2.1-lite / doubao-2.1-pro / deepseek-v4-flash / deepseek-v4-pro /
  kimi-k3 / kimi-k3.1-pro / kimi-k3.1-pro-fast / glm-5.3 / glm-5.3-flash）。每条
  工作模式模型锁定 `model_item_key` + `memory_profile` + `total_window_size`。
- 新增七项配置：`work_bot_id` / `work_mode_id` / `work_reasoning_effort` /
  `work_memory_profile` / `work_total_window` / `work_enable_sandbox` /
  `work_workspace`，均带默认值，缺省键保留原值不被热更新冲空。
- 新增回归：`workmode_test.go` 11 项，覆盖工作模式请求体契约、`model_item_key`
  三处一致、顶层 `ext` 必须为空、技能管线不被工作模式污染、客户端 query 形态与
  `web_tab_id` 轮换、客户端请求头指纹、`device_id` 派生稳定性。

## 0.1.5

- 修复导入一个账号后面板出现两条完全相同记录。宿主有两条注册路径：`host.auth.save`
  用文件相对路径派生 ID（带 `.json`），登录结果直接用 `AuthData.ID`。插件回传的 ID
  不带后缀，同一个物理文件因此被登记成 `doubao-xxx` 与 `doubao-xxx.json` 两条记录。
  现统一为宿主文件形态（`authFileNameOf`），`auth.parse` 也回传同名 ID；账号列表
  另按 `AuthID` 去重，兜住旧版本已写进宿主的历史重复项。
- 模型清单改为动态发现：新增 `model_discovery.go`，从 `/alice/basic/launch` 的
  `model_list` 拉取账号真实模型（实测 67 条 / 26 个去重代号），与 4 个能力别名合并
  后按账号返回。上游不可达时回落到能力别名并在面板显示原因，不会让目录变空。
  缓存按账号隔离、10 分钟 TTL，面板提供「刷新模型清单」强制刷新。
- 上游风控提示改为可操作文案：`710022004` 由 `shark_admin/verify/semantic_reasoning`
  下发，同一 Cookie 换出口 IP、换设备标识、换深度思考参数都照样触发，而账号信息端点
  仍返回 `success`——这不是网络抖动，等几分钟通常无效，只有换账号或等风控窗口解除。
- `doubao/pro`（深度思考）的说明如实标注：当前与 `default` 发出的上游请求相同，
  `need_deep_think` / `model_item_key` 的正确取值尚未验证（风控期无法做参数消融），
  深度思考尚未真正生效。
- 新增回归：`TestAuthDataIDMatchesHostFileName`、`TestListAllAuthFilesDedupesByAuthID`、
  `TestDiscoverUpstreamModels`（含同名去重）、`TestCachedUpstreamModelsReusesPerAccount`
  （缓存按账号隔离）、`TestModelForAuthMergesUpstreamAndAliases`、
  `TestHandleModelsQueryFallsBackToAliases`；面板回归补回落提示与动态清单两阶段。

## 0.1.4

- 登录桥接页不再要求管理密钥。0.1.3 的桥接页让浏览器直接请求
  `/v0/management/plugins/doubao-provider/import`，而 management 路由由宿主强制
  鉴权，用户必须再填一次管理密钥才能完成导入——与 traework 等插件的授权体验不一致。
  现改为提交到本插件自己的 resource 路由 `/login/import`（宿主对 resource 路由
  GET 免密钥放行），Cookie 放 `X-Doubao-Cookie` 请求头。安全性由 state 保证：
  state 在发起登录时随机生成、15 分钟过期，等于一次性短时凭证。
- 移除桥接页的密钥输入框、localStorage 密钥读取与 401 清 key 逻辑。
- `/import` 管理路由不再承担桥接页回传职责，删除已无调用方的 `login_state` 分支。
- 新增回归：`TestLoginBridgeImportWithoutManagementKey`（无 Authorization 也能导入
  且轮询拿到 success）、`TestLoginBridgeImportRejectsBadInput`、
  `TestLoginBridgeImportRouteIsDispatched`（路由被删/改 404 时用例必须失败）。

## 0.1.3

- 修复登录桥接页的假成功：0.1.2 用「账号集合变化 / UpdatedAt 变新」判定登录完成，
  但插件每次正常请求后会把滚动 Cookie 写回账号文件，那同样刷新 UpdatedAt。
  生产实测复现：用户尚未粘贴 Cookie，只要期间有任意请求经过，宿主轮询就会
  返回 `status: ok` 并显示认证成功。
  现改为**只认桥接页的显式导入信号**：`/import` 携带 `login_state`，导入成功后
  插件把账号标识登记到该会话，轮询只在该登记存在时返回 success。
  账号已落盘但会话过期时仍保留账号并提示重来，不丢用户已导入的凭据。
- 新增回归：`TestPollLoginIgnoresRefreshedAccounts`（账号被刷新仍须 pending）、
  桥接页断言「提交携带 login_state」。

## 0.1.2

- 账号卡片补齐「测试模型」（对齐 workbuddy 面板）：
  卡片新增「测试」按钮 → 弹窗选择模型 → 发起一次真实推理。
  测试走流式首帧即停（收到首个有效输出立即返回，不等全文），
  并拦截 HTTP 200 下的 `STREAM_ERROR` 帧（额度耗尽/限流不会被误判为活跃）；
  上游不回包由 20 秒上下文兜住。模型列表只含对话类别名——生图/生视频
  会真正触发上游生成，既耗额度又要等数十秒，且失败原因与账号不可用难区分。
- 修复宿主 OAuth 登录入口报 `failed to generate authorization url`：
  插件声明了 `auth_provider`，却只实现了 `auth.identifier`，宿主的
  `/v0/management/<provider>-auth-url` 调用 `auth.login.start` 拿到
  `unknown_method` 后统一回该错误。现补齐 `auth.login.start` /
  `auth.login.poll` / `auth.parse` / `auth.refresh`，并把宿主弹窗接到
  「浏览器 Cookie 导入」桥接页：豆包的登录态是 HttpOnly Cookie，只能由用户
  从浏览器导出，插件不实现一键授权（与既定认证路线一致）。
  登录成功的判据是「账号集合出现新成员」或「已有账号 UpdatedAt 晚于会话开始」，
  不使用宿主文件时间戳——插件刷新存量账号也会重置它，会造成未授权即成功。
- 新增回归：
  - Go 单测 20 个（首帧即停、错误帧拦截、空回复、超时兜底、登录起点/轮询/
    历史账号不误判、账号文件认领、刷新、方法分发、帧判定语义）；
  - 面板 `test/doubao/panel_test_model_repro.mjs`（19 断言）；
  - 桥接页 `test/doubao/login_bridge_repro.mjs`（18 断言）。

## 0.1.1

- 面板补齐 management key 鉴权（修复生产「导入失败：missing management key」）：
  宿主对 `/v0/management/*` 统一鉴权，不带 `Authorization` 时返回
  401 `{"error":"missing management key"}`。面板此前直接裸 fetch，
  导致导入、刷新账号、启停、删除全部被宿主拦断。
  现按既有 6 个插件的同构实现补齐三段式取 key：① CPA 主面板 iframe 内
  从 `localStorage["cli-proxy-auth"]` 解密（XOR+base64）；② URL `?key=`
  读取后从地址栏剔除并落 `sessionStorage`；③ 面板内手动填入。另补
  401/403 时清 key、重新弹框并给出可读提示，不再回显「未知错误」。
- 新增 `test/doubao/panel_auth_repro.mjs`（16 项 DOM 断言，覆盖三条兜底路径与 401 处理）

## 0.1.0

- 插件骨架与宿主注册：`doubao-provider` 能力声明、管理路由、内嵌面板页
- 登录态导入与有效性校验：面板粘贴浏览器 Cookie → 判据端点
  （`accounts.doubao.com/passport/account/info/v2/`）确认登录态 → 原子落盘
  账号文件；垃圾凭据明确拒绝，不落盘
- 账号管理：面板列出/启用/禁用/删除账号，登录态状态上报（`auth_state`）
- Cookie Jar 写回：捕获上游滚动下发的 `Set-Cookie`（如 `gd_random`）并持久化，
  写入前比对文件身份防止覆盖外部新导入的凭据
- 对话链路（`/chat/completion`）：按 `content_block` + `mode_id:"1"` 契约构造请求体，
  解析 SSE 多类增量事件（`STREAM_MSG_NOTIFY` / `STREAM_CHUNK` / `CHUNK_DELTA`），
  识别流内 `STREAM_ERROR` 并转成可操作中文提示；**关键修正**：`client_meta.local_conversation_id`
  为必填项，缺失或空串会被上游以 `710020202 common invalid param` 拒绝
- 模型目录：声明 4 个稳定语义别名（`doubao/default`、`doubao/pro`、`doubao/image`、
  `doubao/video`），`image`/`video` 携带 `skill` 3/5 激活生成路径；未知名原样透传；
  出口自行响应宿主 `oauth-excluded-models`
- executor 接入：非流式返回完整 OpenAI 响应，流式逐帧转 `chat.completion.chunk`
  （首帧带 `role`，末帧 `finish_reason=stop`），深度思考走 `reasoning_content`
- 生图生视频：解析 `CREATION` 块（`block_type=2074`），按 `id` 合并同一资源的
  多帧上报（首帧只有占位尺寸、后帧才带 `key` 与直链）；图片优先取 `image_ori`，
  再调 `/creativity/resource/get_without_watermark` 换无水印全尺寸直链（失败时
  回退流内缩略图，不失败整个请求）；回复转成 OpenAI 多模态 `image_url` + `media` 扩展
  - **实测修正**：`skill`（3/5）不是生成路径的必要条件——上游按提示词意图决定是否
    绘制，不带 `skill` 的「画一只猫」同样产出图片，带 `skill=3` 的闲聊仍只回文本
    （上游回显 `input_skill.skill_id="0"`）。注入保留为语义声明
- 多账号池与故障转移：账号启停/删除直写物理文件；429 / 402 / 401 / 403 / 5xx /
  传输错误按阶梯冷却（30s × 连续失败次数，封顶 10 分钟），业务 400 不计入（换号
  无意义且会误伤健康账号）；同请求内遇 429/额度耗尽/登录态失效立即换号并排除刚失败
  的账号，一次成功即清零；`scheduler.pick` 只在宿主候选集内取号，无可用候选时返回
  `Handled:false` 让位宿主跨 provider 轮换
- Cookie Jar 防覆盖判定改用 SHA-256 内容摘要：Windows 下文件索引可能复用、等长
  JSON 可能落在同一时间戳刻度，`os.SameFile + ModTime + Size` 三项同时失效会让旧
  快照覆盖用户刚重新导入的凭据
