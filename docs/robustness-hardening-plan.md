# chat_image (go-gateway) 健壮性加固计划

> 生成方式: 5 维度并行代码审查(44 条发现) + Top10 逐条对抗性核验(9 CONFIRMED / 1 PARTIAL / 0 REFUTED,行号已逐条比对)。
> 触发事故: ComfyUI `/upload/image` connection refused → 图片编辑任务直接 FAILED,且网关侧日志零记录(错误只回传到了 chat-web 的 job.error)。

## 实施状态(2026-07)

已按分批实施并经 **3 轮对抗式复核**(12 → 6 → 2 条发现,逐条修复,build/vet/`go test -race` 全绿):

- **可观测性**: failTask/worker panic(+堆栈)/cleanup panic 自恢复/ViewFile 下载失败/submit 关联/image_edit 兜底/HMAC 401(限速)全部落日志。
- **依赖韧性**: comfy 拨号超时 10s + 有界连接池 + 响应体 drain;QueuePrompt/Upload/View/postJSON 传输层重试(仅 **dial/DNS 阶段**可重试,严守 QueuePrompt 不双重排队红线);downloadImageFromURL 重试;轮询连续连接错误短路(不再空等满 timeout)。
- **内存/DoS(T9/T5)**: materialize 后从存储态 Params 剥离 base64(OOM 主因);HMAC 前置**字节预算**上限(限并发缓冲总字节,堵住 sub-threshold flood)+ idle 读死线(反 slow-loris)。
- **生命周期(T8)**: SIGTERM/SIGINT 优雅关闭——非终态任务批量 failTask + 并发 interrupt orphan GPU 任务(独立 5s deadline);启动清扫残留临时文件。**注: 覆盖正常重启,硬 SIGKILL 仍需持久化(见下,留作后续)。**
- **健康探测**: 新增 `/readyz`(≥1 后端 `/system_stats` 可达才 200),并发探测 + 2s TTL 缓存 + singleflight(防慢探测污染)。
- **数据竞争**: createTask 入队前快照(`go test -race` 通过)。

单测锁定: `isRetryableNetErr`(双重排队红线)、`byteBudget`(并发)。

**仍留作后续(结构改造,非本批)**: 任务持久化(硬崩溃 orphan)、后端健康感知+提交前跨节点 failover、per-backend 并发信号量、统一任务 deadline、history 竞态(仅 completed 才下载)、结构化日志/`/metrics`、multipart 流式上传、S3 失败本地降级、删 Subscribers 死代码。

## 一句话根因

调度层假设"依赖永远可用"(无健康感知/无 failover),失败层假设"失败即终点"(零重试/无兜底),观测层让这一切不可见(失败路径零日志)。本次事故 = 三者叠加。

## 修复优先级(核验后裁定)

| 优先级 | 项 | 理由 |
|---|---|---|
| P0 | T9 未认证内存耗尽(HMAC 先 ReadAll 160MB 再验签) | 核验后**升级**: 不知密钥的攻击者即可迫使服务端缓冲 160MB,属未认证 DoS 面 |
| P0 | T8 任务全内存、重启静默丢失 + Comfy orphan | 部署重启是常态,一次重启所有进行中任务失联 |
| P1 | T6+T7 后端容错(死节点钉死 worker 30min + 无健康摘除/failover) | 合并为同一项"后端容错"改造,是本次事故类问题的根治 |
| P1 | T5 每秒 ~134MB JSON 往返(base64 驻留 Params) | OOM 主因;修复靶点见下方"核验更正" |
| P1(先行) | T1+T3 可观测性(failTask/panic/ViewFile 零日志) | 成本最低,补上后一切故障立刻可见,建议第一批做 |
| P2 | T2 data race → T4 零重试 → T10 healthz 语义 | 按序推进 |

## 核验更正(引用本文档结论时以下方为准)

1. **T5 机制更正(重要,否则修复会指错位置)**: cloneTask 对 Task 是**结构体值拷贝**(workflow_render.go:69),并非"JSON 深拷贝整个 task";真正的 JSON Marshal+Unmarshal 往返发生在 **cloneMap 对 Params**(workflow_render.go:50-61)。因 base64 存于 Params 且占绝对体积,"每秒 ~134MB JSON 往返"的后果不变。另有两个加重因素: (a) 该克隆在 updateTask 的全局 `s.mu.Lock` 持锁期间执行(task_store.go:131-144),阻塞所有任务操作; (b) emitTo 每次更新对含 Params 的完整快照再做一次 json.Marshal,订阅者为零也执行(task_store.go:226-228)。
2. **T9 严重度上调**: ReadAll 发生在签名校验**之前**(http_middleware.go:166-175 前置门槛仅时间戳±300s+任意 nonce),构成**未认证内存耗尽向量**,且 msg 拼接带来 ~3-4 倍峰值放大。从"资源管理缺陷"重分类为"未认证拒绝服务面"。
3. T1 措辞: 非测试文件中导入 log 的还有 migration.go(一次性运维模式)与 lora.go:25,28(参数冲突警告);"任务生命周期内错误与 panic 零日志"结论不变。
4. T3: "no output files produced" 仅出现在非 image_edit 分支(output_service.go:233);image_edit 分支是 :219-231 的 "_after" 措辞,两分支同样把下载失败误报为无产出。
5. T4: ConvertWorkflow(comfy_client.go:192-218)是 3 端点 × 5 payload 的 15 组合**回退**而非重试——每组合单次尝试、无 backoff、同一 BaseURL。
6. T6: GetHistory 出错处为 `else { lastErr = err }` 落空继续循环(无字面 continue);"每秒轮询"限于 connection-refused,后端挂起/黑洞时受 120s 客户端超时制约(更糟)。
7. T8: Server 结构体 server_core.go:16-27、NewServer :29-50;重启后 404 落在 GET /v1/videos/{id}(v1_handler.go:172-176)。
8. T10 范围限定: comfy 容器各有独立 healthcheck(compose.yaml:71-76,115-120);盲区限于 gateway 容器状态与直接探测 /healthz 的外部监控。

## 分批建议

**第一批·快速止血(几行到几十行/条)**: failTask+panic+ViewFile+HMAC 401 日志、submit 关联日志(task↔prompt_id↔backend)、data race(入队前克隆)、Upload/Queue/Convert/View/下载 URL 重试退避、materialize 后剥离 Params 大字段、轮询连续连接错误短路、ffmpeg CommandContext 超时、comfy 拨号超时、清理协程 recover、启动清扫临时文件、响应体 drain、/readyz。

**第二批·结构改造**: 后端健康感知+提交前跨节点 failover、任务持久化+优雅关闭兜底(failTask+interrupt orphan)、内存背压(在途并发信号量/字节配额/multipart 流式)、统一任务 deadline、history 竞态修复(仅 completed 才下载)、观测体系(结构化日志+阶段耗时+/metrics)、S3 失败本地降级、删 Subscribers 死代码。

---

# 完整问题清单(5 维度 × 44 条,含重叠交叉引用)

> 注: 各维度独立审查,少数问题被多个维度命中(如 无 failover / ViewFile 静默 / healthz / 30min 钉死 / Subscribers 死代码),视为同一问题的不同视角。Top10 已核验条目对应: T1=可观测#1#2, T2=并发#3, T3=容错#4/可观测#3/生命周期#2, T4=容错#2#3, T5=并发#1(按上方更正理解), T6=启动#2/容错#8, T7=启动#1/容错#1, T8=生命周期#1, T9=并发#2, T10=启动#4/可观测#6。


### 启动时序与依赖就绪（ComfyUI backend 健康探测/故障处理/多节点调度）  (6 findings)

**[HIGH]** 1. 多 backend 完全没有故障剔除/健康感知，ComfyPool.Next() 是纯轮询。某个 Comfy 崩溃或未就绪时，被轮到该节点的任务会立即失败，即使池中还有健康节点在空闲。dead 节点永不被移除，healthy 节点永不被优先——这正是本次 connection refused 场景直接导致用户请求失败的根因。
   - 位置: `app/service/comfy_client.go:49 (ComfyPool.Next) 与 app/service/task_runner.go:23-27,136-140`
   - 为何危险: executeTask 通过 comfy := s.comfyPool.Next() 盲选一个节点，随后 comfy.QueuePrompt 一旦返回 err（connection refused）就直接 s.failTask(taskID, err) 返回，不做任何跨节点重试。配置了 2 个 backend、其中 1 个宕机时，约 50% 的任务会被派发到死节点并秒失败，而健康节点仍然空闲。ComfyPool 结构体里没有任何 healthy 标志位/失败计数/摘除逻辑。
   - 修复: 为每个 ComfyClient 增加健康状态（连续失败计数 + 冷却时间戳），Next() 跳过处于冷却期的节点；QueuePrompt/ConvertWorkflow/UploadImage 失败（尤其是拨号类错误）时把当前任务重新在其它健康节点上重试（对 QueuePrompt 这类幂等提交前的步骤可安全重试）；后台加一个 /system_stats 探测 goroutine 定期把恢复的节点重新标记为 healthy。

**[HIGH]** 2. 任务提交成功后 Comfy 崩溃，waitForPromptOutputs 会对着死掉的 /history 端点每秒轮询直到 timeoutSec（默认 1800s = 30 分钟）才失败，不会快速失败、不会换节点重跑。worker 被钉死 30 分钟，Comfy 抖动可级联耗尽队列。
   - 位置: `app/service/task_poll.go:34-66 与 app/service/task_runner.go:148-163`
   - 为何危险: 轮询循环里 GetHistory 出错只是 lastErr = err 后 continue，唯一退出是 ctx.Done()（timeoutSec，默认 1800）。所以后端中途崩溃的任务要空转到 30 分钟超时，报 'timeout waiting comfy history... (last poll error: connection refused)'。期间该 worker 无法处理其它任务；worker 数默认只等于 backend 数（GO_WORKERS 默认 len(URLs)，compose 里=2），几个死节点任务就能把所有 worker 钉住，进而 CreateTask 触发 ErrQueueFull → 503（task_store.go:106-113，队列 cap 256）。
   - 修复: 在轮询中对连续的拨号/连接错误设一个短的 fail-fast 阈值（如连续 N 次 connection refused 就判定后端不可达并快速失败/改派），而不是一律等满 timeoutSec；并将该节点标记为不健康。

**[MEDIUM]** 3. Go 应用层在启动时对 ComfyUI 没有任何就绪/可达性探测。NewComfyPool 只是把 URL 字符串包成 struct，不发起任何连接；len(URLs)==0 只校验“配置了没有”，不校验“连得上没有”。worker 与 HTTP 监听在 backend 是否就绪未知的情况下立即开工。
   - 位置: `app/service/app.go:58-61,132 与 app/service/comfy_client.go:37-47 与 app/service/server_core.go:43-48`
   - 为何危险: 启动就绪唯一保障来自 compose.yaml:148-154 的 depends_on: condition: service_healthy（Comfy 用 /system_stats 探针）。但据项目记忆 gateway 部署在远程服务器、Comfy 也在服务器上，一旦 gateway 以裸机/独立方式运行、或 COMFY_URL 指向远端主机，compose 的 depends_on 完全不生效，此时没有任何启动门禁——刚启动就收到的请求会直接打到还没起来的 Comfy 上秒失败。
   - 修复: 启动时对每个 backend 做一次 /system_stats 探测并打印就绪日志（可带重试/超时），至少对全部 backend 都不可达时告警；不要 log.Fatal（避免误杀），但要把结果并入上面提到的 per-node 健康状态，让 Next() 一开始就避开尚未就绪的节点。

**[MEDIUM]** 4. /healthz 无条件返回 200，与 ComfyUI 是否可达完全解耦。所有 backend 都宕机时，gateway 对 compose healthcheck 和外部监控依然显示健康，仍继续接收注定失败的请求。
   - 位置: `app/api/router.go:22-26 与 compose.yaml:157-162`
   - 为何危险: compose 的 depends_on 只在“首次启动”门禁，启动后 Comfy 崩溃不会反映到 gateway 的健康状态；gateway healthcheck 打的是自身 /healthz，永远 200。结果是运行期 Comfy 全挂时上游/监控无法通过 gateway 健康探针发现问题，也没有任何自愈触发。
   - 修复: 增加一个反映后端可达性的探针（如 /readyz：至少一个 backend 的 /system_stats 通过才 200），或让 /healthz 汇总后端健康摘要；把它接到 compose/监控上，才能感知“运行期全后端不可用”。

**[MEDIUM]** 5. 对 Comfy 的所有交互（QueuePrompt、ConvertWorkflow、UploadImage、GetHistory）在瞬时错误上零重试。单次 connection refused / 瞬断即等于任务硬失败，连同一节点的即时重试都没有。
   - 位置: `app/service/comfy_client.go:73-100,192-218 与 app/service/image_input.go (UploadImage 调用路径 task_runner.go:103-108)`
   - 为何危险: QueuePrompt/postJSON/UploadImage 里 HTTP.Do 一旦 err 即向上返回，executeTask 直接 failTask。Comfy 重启窗口内（compose restart: unless-stopped 会拉起，但有秒级空窗）或网络瞬断，都会把本可自愈的请求直接判死，用户侧只能看到失败任务。
   - 修复: 对提交前的幂等步骤（ConvertWorkflow、UploadImage、QueuePrompt 提交成功前）加入有限次带退避的重试，并结合跨节点改派；注意 QueuePrompt 一旦拿到 prompt_id 就不可盲目重试（会重复排队/重复扣 GPU）。

**[LOW]** 6. ComfyClient 使用默认 Transport、仅设 http.Client.Timeout=120s，没有独立的拨号超时。connection refused 会快速返回，但目标主机在线却端口被防火墙丢包（SYN 无响应）时，拨号会一直挂到 120s 才超时，长时间占用 worker。
   - 位置: `app/service/comfy_client.go:30-35`
   - 为何危险: 对比 image_input.go:34-52 的 safeHTTPClient 明确设置了 30s 拨号超时，comfy 客户端没有等价配置。远程部署（gateway 与 Comfy 跨主机）下更容易遇到‘连接挂起’而非‘连接拒绝’，此时 120s 的粗粒度超时会放大 worker 被占用的时间。
   - 修复: 给 comfy 的 http.Client 配置带显式 DialContext 超时的 Transport（如拨号 5-10s），把‘连不上’与‘生成慢’两类超时区分开，避免拨号阶段长时间占用 worker。


### 外部依赖容错 (external-dependency fault tolerance)  (10 findings)

**[HIGH]** 1. 任务在开始时用轮询 comfyPool.Next() 选定一个 ComfyUI backend，之后整条链路(ConvertWorkflow/UploadImage/QueuePrompt/GetHistory/ViewFile)全部固定打到这一个 backend,任何一步失败都直接 failTask,永不切换到池中其他健康 backend。池里有 N 个 backend,只要被选中的那个抖一下或宕机,任务就死,其余 N-1 个健康节点完全用不上。这是所有'依赖抖一下就死'的根因。
   - 位置: `app/service/task_runner.go:23 (comfy := s.comfyPool.Next()),配合 comfy_client.go:49-61 (Next 纯轮询)`
   - 为何危险: ComfyPool.Next() 只做无状态轮询,不做存活探测、不剔除故障节点、不在失败时换节点。executeTask 一次性绑定单 backend,后续调用无 failover。单点故障被放大成全量任务失败。
   - 修复: 给 ComfyPool 增加故障转移:1) 在 executeTask 里把'选 backend + 提交 prompt(convert/upload/queue)'包成可重试单元,失败时依次尝试池中其他 backend;2) 给 ComfyClient 加被动健康标记(连续失败 N 次标记 unhealthy 并短暂熔断,Next() 跳过 unhealthy),配合后台 /healthz 探测恢复;3) prompt 已提交后(GetHistory/ViewFile 阶段)无法换 backend,这部分只能靠对同一 backend 的重试。

**[HIGH]** 2. materializeImageParam→UploadImage POST /upload/image 无任何重试/退避。ComfyUI connection refused 或瞬时 5xx,立即 return err → failTask,整个图片编辑任务 FAILED。(已知实锤)
   - 位置: `app/service/image_input.go:103 → app/service/comfy_client.go:160-172 (HTTP.Do 后不区分错误类型直接返回)`
   - 为何危险: c.HTTP.Do 的网络错误(拨号失败/reset/超时)和 5xx 都被一次性透传。上传只是准备阶段、幂等可重试,却没有任何 backoff 重试,ComfyUI 抖一下即死。
   - 修复: 对 UploadImage 增加带指数退避的有限重试(如 3 次,间隔 0.5/1/2s),仅对可重试错误(网络错误、5xx、429)重试,4xx 不重试;并纳入上面的多 backend failover(上传是提交前阶段,可安全换节点)。

**[HIGH]** 3. QueuePrompt POST /prompt 无重试。瞬时网络抖动或 5xx 直接 failTask,任务永久失败且不会重新入队。
   - 位置: `app/service/task_runner.go:136 与 168 → app/service/comfy_client.go:81-89`
   - 为何危险: 提交 prompt 是纯网络调用,ComfyUI 重启窗口/短暂 502 就会让任务死。失败后 task 被标记 failed 永不 requeue,调用方需重新发起。
   - 修复: QueuePrompt 增加对可重试错误的有限退避重试;配合 failover 换 backend 重试。注意 /prompt 非严格幂等(会真的排队作业),重试仅限'确定未成功'的网络/连接错误,已返回 2xx 但解析失败的情况不要重试。

**[HIGH]** 4. 下载产物时 ViewFile /view 出错被静默 continue 跳过,导致已经花了 GPU 算力生成的产物丢失,并最终以误导性的 'no output files produced' / 'no image_edit _after output produced' 失败,掩盖真实原因(ComfyUI /view 抖动)。
   - 位置: `app/service/output_service.go:135-138 (content, err := comfy.ViewFile(...); if err != nil { continue })`
   - 为何危险: 这是任务最昂贵阶段之后:图已生成、GPU 已消耗,却因为一次 /view 瞬时失败被丢弃且不重试。若所有输出节点的 /view 都恰好抖动,任务报'没有产物',把最贵的工作白白浪费,且错误信息误导排障。
   - 修复: 对 ViewFile 增加有限退避重试;当 /view 失败(而非产物本身缺失)时,应作为可重试的下载错误上抛并保留原始错误,不要静默 continue 成'无产物';至少区分'节点无该产物' vs 'download 失败'两类,后者要重试/报明确错误。

**[MEDIUM]** 5. ffmpeg 子进程无超时。convertImageBytesToAVIF/convertVideoBytesToAV1MP4 用 exec.Command 直接跑,无 context 超时;卡死或异常慢的 ffmpeg 会无限期占住一个 worker。同时 ffmpeg 是硬依赖,二进制缺失或全部 codec 不可用时所有图/视频任务永久失败,无'转码失败则回退保存原始文件'的降级。
   - 位置: `app/service/media_convert.go:133 与 195 (exec.Command("ffmpeg", ...).CombinedOutput())`
   - 为何危险: 没有 exec.CommandContext + timeout,hung ffmpeg 直接吃掉 worker;worker 数≈backend 数,几个卡死的转码就能耗尽处理能力。多 codec 尝试只是兼容性容错,不覆盖'ffmpeg 不存在/进程挂死'。
   - 修复: 改用 exec.CommandContext 加合理超时(如 image 60s、video 几分钟),超时 kill 进程;启动时做一次 ffmpeg 可用性自检并在日志/健康检查暴露;考虑转码彻底失败时降级为直接存原始产物,避免因转码依赖抖动而丢失已生成内容。

**[MEDIUM]** 6. downloadImageFromURL 拉取用户提供的远程图片 URL 无重试。远端图床瞬时 5xx/超时/reset 直接 failTask。
   - 位置: `app/service/image_input.go:127 (safeHTTPClient.Do(req),单次尝试,60s 超时)`
   - 为何危险: 用户远程 URL 是最不可控的外部依赖,瞬时抖动很常见,却一次失败即杀任务。
   - 修复: 对下载增加有限退避重试(仅对网络错误/5xx/429,保留 SSRF 校验与大小限制不变);4xx 不重试。

**[MEDIUM]** 7. 文本后端(LLM)/v1/chat/completions 无重试。瞬时 502/连接 reset 直接把错误透传给调用方。
   - 位置: `app/service/text_client.go:59-63 (client.Do(req),单次尝试)`
   - 为何危险: 上游 LLM 网关短暂抖动/滚动重启会直接表现为用户请求失败。虽为同步请求不占 worker,但仍是'抖一下就失败'。
   - 修复: 对非流式请求增加幂等安全的有限退避重试(网络错误/5xx/429);流式请求已开始写响应后不宜重试,可在建连阶段失败时重试。

**[MEDIUM]** 8. ComfyUI 宕机时 worker 被 30 分钟的必败轮询长期占用,形成级联拥塞。waitForPromptOutputs 在 backend 挂掉时把 GetHistory 错误存进 lastErr 一直轮询直到 timeoutSec(默认 1800s)。worker 数默认等于 backend 数,若后端集体不可用,所有 worker 会各自空转 30 分钟,新任务在 256 长度队列里堆积直至 ErrQueueFull。
   - 位置: `app/service/task_poll.go:34-53 (GetHistory 失败仅记 lastErr 继续轮询) 与 app/service/server_core.go:36 (queue 容量 256)、app.go:62 (workerCount 默认=backend 数)`
   - 为何危险: 轮询循环无'连续失败快速失败'的短路:即使 /history 连续 N 次 connection refused(明显 backend 已死),仍傻等满 timeout。这把单点宕机放大为整池长时间不可用。
   - 修复: 在轮询循环里对连续网络级失败设一个更短的熔断阈值(如连续失败 > K 次且持续 > M 秒即判定 backend 不可达,提前失败并把 backend 标记 unhealthy),区别对待'backend 不可达'与'作业还在跑'两种情况,避免 worker 被必败任务长期占用。

**[LOW]** 9. S3 上传失败会连带丢弃已生成产物且无本地降级。downloadOutputs 在 storage_mode=s3 下 UploadPrivate 失败(5min 超时后)直接 return err → failTask,已生成的图/视频丢失。AWS SDK 默认有 3 次标准重试,故短抖动可扛,但整段 S3 不可用仍会让昂贵产物白丢。
   - 位置: `app/service/output_service.go:188-196 (UploadPrivate 失败即 return err)`
   - 为何危险: S3 是唯一自带 SDK 重试的依赖(相对健壮),但对象存储整体故障时没有回退到本地磁盘的降级路径,GPU 产物直接损失。
   - 修复: S3 上传彻底失败时考虑降级为本地保存并返回本地 URL(或落盘待后续 migrate),而非直接失败丢产物;或至少把 UploadPrivate 单独重试与超时参数化,便于按部署调优。PresignGetObject 是本地签名(无网络),失败概率低,可保持现状。

**[LOW]** 10. 所有 ComfyUI 调用不传 context,无法随任务取消/客户端断开而中止在途请求;客户端 HTTP.Timeout 固定 120s 对大图上传/大 history 可能偏紧,且无自定义 Transport(连接池、空闲连接、拨号超时全用默认)。
   - 位置: `app/service/comfy_client.go:33 (固定 120s Timeout) 及 76/104/126/160 (http.NewRequest 无 ctx)`
   - 为何危险: QueuePrompt/GetHistory/ViewFile/UploadImage 用不带 context 的请求,任务 cancel 或上游断开时在途调用不会被取消,浪费连接与后端资源;固定超时也不便按调用类型区分(上传大文件 vs 快速轮询)。
   - 修复: 改用 http.NewRequestWithContext 并把任务级/轮询级 ctx 传下去;为不同调用设置差异化超时;配置自定义 http.Transport(MaxIdleConnsPerHost、拨号超时等)以提升抖动下的连接复用与快速失败。


### 可观测性与可诊断性 (Observability & Diagnosability) — chat_image / go-gateway  (11 findings)

**[HIGH]** 1. failTask 只把错误写入 task.Error 回传给调用方,从不打日志。这正是报告现象的根因:任务失败时错误经 GetTask/响应返回给 chat-web,但 go-gateway 侧日志完全静默,运维在网关侧'看不到任何异常'。整个提交/上传/渲染/下载/失败生命周期(task_runner.go、task_store.go、task_poll.go、comfy_client.go、output_service.go、image_input.go、api/*)零日志,全部 log.Printf 只在启动阶段(app.go)。
   - 位置: `app/service/task_store.go:152-159 (failTask);全生命周期文件零 log 调用(已 grep 确认 NONE)`
   - 为何危险: 调用方拿到完整错误链,网关却无任何记录,导致'错误已回传但网关无异常'的信息不对称,失败无法在网关侧定位、无法按时间/量级聚合。
   - 修复: 在 failTask 里 log.Printf("[task fail] task=%s workflow=%s prompt_id=%s backend=%s err=%v", ...) 记录 taskID/workflowID/promptID/backend + 完整错误;并在 executeTask 关键阶段(queued→running、submitted、completed)各打一条带 taskID 的日志。

**[HIGH]** 2. worker 协程 panic 被 recover 后仅调用 failTask(存进 task.Error),既不打日志也不捕获堆栈(debug.Stack)。一次 nil 解引用/越界 panic 在日志里完全不可见,堆栈永久丢失,无法定位崩溃点。
   - 位置: `app/service/server_core.go:74-85 (workerLoop 的 recover)`
   - 为何危险: panic 是最需要堆栈的故障类型,当前实现把它降级成一条普通 task.Error 字符串且不落盘,诊断信息归零。
   - 修复: recover 分支加 log.Printf("[worker panic] task=%s: %v\n%s", id, r, debug.Stack()) 后再 failTask。

**[HIGH]** 3. 下载产物时 ViewFile 失败被 `continue` 静默吞掉,随后被泛化错误 'no output files produced' 掩盖。真正的下载失败原因(comfy /view 报错、网络、文件类型)彻底丢失,调用方只看到一个无信息量的兜底错误。
   - 位置: `app/service/output_service.go:135-138 (content, err := comfy.ViewFile(...); if err != nil { continue })`
   - 为何危险: 现象:渲染其实成功但取回失败,却报成'没产出',误导排查方向;根因错误既不落日志也不进 task.Error。
   - 修复: 记录并累积每次 ViewFile 错误(至少 log.Printf 带 filename/subfolder/promptID),在最终 'no output files' 错误里附带首个下载错误摘要。

**[MEDIUM]** 4. 没有任何请求级访问日志/中间件。withCORS、withHMACAuth 及 router 都不记录 method/path/status/耗时/taskID。无法确认请求是否到达网关、返回了什么状态码、耗时多少。
   - 位置: `app/service/http_middleware.go:22 (withCORS) / :115 (withHMACAuth);app/api/router.go`
   - 为何危险: 关键路径(提交/查询/取消)缺少最基本的访问日志,连'请求有没有进来'都无法判断,与失败静默叠加使端到端不可观测。
   - 修复: 加一个 logging 中间件包裹 mux,记录 method、path、status、duration、以及从响应体或 handler 透出的 taskID/promptID。

**[MEDIUM]** 5. 无阶段耗时度量。进度是 task_poll.go 里用'已过秒数'合成的假进度(progressBase+elapsed 封顶),并非真实测量。排队等待、UI→API 转换、comfy 入队、渲染等待、产物下载、S3 上传各阶段的实际耗时全都未采集、未记录,慢在哪一段无从判断。
   - 位置: `app/service/task_poll.go:54-64 (合成进度);app/service/task_runner.go(各阶段无计时)`
   - 为何危险: 缺阶段耗时,性能回归和卡顿(如 comfy 慢 vs S3 上传慢)无法区分定位。
   - 修复: 在 executeTask 各阶段用 time.Since 计时并在完成/失败时汇总一条 stage timings 日志(如 queue_wait/convert/queue/render/download/upload 各 ms)。

**[MEDIUM]** 6. /healthz 是纯 liveness,无条件返回 {ok:true},不检查 comfy 后端与 S3 依赖可达性;无 readiness 端点。compose 用 /healthz 做健康检查,于是即便所有 comfy 后端宕机或 S3 配置错误、生成 100% 失败,容器仍报 healthy。
   - 位置: `app/api/router.go:22-26 (/healthz);compose.yaml:157-162`
   - 为何危险: 依赖故障时健康检查仍绿,故障被掩盖,编排层不会重启/告警,加剧'无异常'的错觉。
   - 修复: 增加 readiness(检查 comfy Next()/system_stats 可达、storage 可用),或让 /healthz 反映依赖状态供监控区分 live 与 ready。

**[MEDIUM]** 7. 缺乏 taskId ↔ promptId ↔ comfy client_id 的关联日志。QueuePrompt 已用 taskID 作为 comfy 的 client_id(良好的关联点),但这条对应关系从不落日志;跨 chat-web→gateway→comfy 也无 request/trace id 透传。出问题时无法把三方日志串起来。
   - 位置: `app/service/comfy_client.go:73-99 (QueuePrompt);app/service/task_runner.go:136-146`
   - 为何危险: 已有天然关联键却不输出,跨服务排障只能靠时间猜测,定位成本高。
   - 修复: 提交成功后 log.Printf("[submit] task=%s prompt_id=%s backend=%s client_id=%s", ...);可选支持透传 X-Request-Id 并写入每条日志。

**[LOW]** 8. 无 metrics / 计数端点。没有 Prometheus 或任何计数器暴露:任务总数、成功/失败数、按 workflow/mode 的失败率、队列深度(queue 容量 256)、在飞任务数、各阶段耗时分位。只能靠翻日志,无法做趋势与告警。
   - 位置: `app/api/router.go(仅 /healthz,无 /metrics);app/service/server_core.go(queue 深度无暴露)`
   - 为何危险: 无聚合指标就无法在失败率上升或队列打满(触发 ErrQueueFull)前预警,只能事后个案排查。
   - 修复: 暴露 /metrics(即便先用简单计数器 JSON):tasks_total{status,workflow}、queue_depth、inflight、stage_duration。

**[LOW]** 9. HMAC 鉴权失败(缺头/时间戳越窗/nonce 重放/签名不匹配)全部 http.Error 返回 401,但不打任何日志。重放攻击、时钟漂移、密钥不一致等问题在网关侧完全不可见。
   - 位置: `app/service/http_middleware.go:126-175 (withHMACAuth 各 401 分支)`
   - 为何危险: 鉴权类故障(如 chat-web 与网关密钥/时钟不一致导致整批请求 401)无日志,排查时误判为网络或服务未启动。
   - 修复: 各 401 分支加限速日志,记录原因、remote addr、path(注意勿记录 secret/签名明文)。

**[LOW]** 10. image_edit 的 no_swap 兜底路径:主渲染失败后走兜底,兜底成功则任务标记 completed('done_fallback_no_swap'),但主路径的原始失败原因从不落日志。主路径静默降级,运维看不到'其实主流程一直在失败、每次都靠兜底救回'的信号。
   - 位置: `app/service/task_runner.go:164-200`
   - 为何危险: 静默降级掩盖了持续性主路径故障,问题长期累积却无告警。
   - 修复: 进入兜底时 log.Printf("[image_edit fallback] task=%s primary_err=%v", taskID, runErr),无论兜底成败都记录一次。

**[LOW]** 11. 使用 stdlib log 的 Printf,无日志级别、无结构化(JSON)字段,且实际有效日志几乎只在启动阶段。运行期日志无法按 level 过滤、无法按 taskID 字段检索,不利于集中式日志聚合。
   - 位置: `app/service/app.go(全部 log.Printf 均为启动期);全项目无 slog/结构化日志`
   - 为何危险: 非结构化 + 仅启动期日志,使得即便补日志也难以在聚合系统中按 task/level/stage 查询。
   - 修复: 改用 log/slog 输出结构化字段(level、task_id、workflow、stage、duration_ms),运行期关键路径统一走结构化日志。


### 任务生命周期与错误传播 (task_runner.go / task_poll.go / task_store.go)  (8 findings)

**[HIGH]** 1. 任务状态全部只存在于内存 (server_core.go 的 tasks map + queue channel),没有任何持久化。进程崩溃/重启/重新部署时,所有 queued 和 running 的任务被静默丢弃,客户端后续轮询只会拿到 404;而已经提交给 Comfy 的 running 任务仍在 GPU 上继续跑,变成无主任务(orphan),既浪费算力又永远不会回写状态。这就是最典型的'静默丢任务 + 卡在中间态永不结束'。
   - 位置: `app/service/server_core.go:16-42 (Server 结构与 NewServer);app/service/task_store.go:101-114 (createTask 入队)`
   - 为何危险: 重启是常态(README/部署在远程服务器)。一次重启即让所有进行中的生成请求消失且无终态,调用方永远等不到结果,同时 Comfy 侧任务空转。
   - 修复: 为任务状态引入持久层(至少把 queued/running/terminal 落到 Redis/DB 或本地文件),启动时恢复;或在关闭时对 running 任务标记为 failed('server restarted')并对 Comfy 发 interrupt。若短期不做持久化,至少在优雅关闭钩子里把内存中非终态任务批量 failTask,避免客户端无限等待。

**[MEDIUM]** 2. downloadOutputs 对每个产物文件的下载错误被吞掉:comfy.ViewFile 失败时只 `continue`(output_service.go:135-138),不记录也不上抛。若 Comfy 的 /view 出现瞬时网络错误导致所有文件都下载失败,out 为空,最终返回的却是误导性的终态错误 'no output files produced for workflow=X'(output_service.go:211-234),把'下载失败'错报成'工作流没产出'。运维据此排查会被带偏,且没有任何重试。
   - 位置: `app/service/output_service.go:135-138 与 211-234`
   - 为何危险: 错误被吞 + 错误信息失真:真实根因(网络/Comfy 抖动)丢失,只留下'无产出'这一错误结论,无法回传给调用方也无法定位。
   - 修复: 记录并累积 ViewFile 的错误,当 out 为空时把最后一次(或全部)ViewFile 错误拼进返回的 error;或对 ViewFile 做有限次重试后再判定'无产出'。

**[MEDIUM]** 3. 轮询把'history 里出现该 prompt_id 的条目'直接当作终态并立即下载(task_poll.go:41-49)。而 extractComfyHistoryError 在 status_str 为空时返回 ''(视为无错误,output_history.go:108-112),不区分'仍在执行'与'已成功'。若 Comfy 在 outputs 尚未写完时就已能查到 history 条目(版本/时序差异),downloadOutputs 会立刻返回 'comfy history has no outputs' / 'no output files produced' → 直接 failTask,没有回到轮询循环重试,造成偶发的假失败。
   - 位置: `app/service/task_poll.go:41-49;app/service/output_history.go:102-118`
   - 为何危险: history 条目出现与 outputs 落盘之间存在竞态窗口;当前逻辑一旦看到条目就一锤定音,窗口内命中即被判死刑且无兜底重试。
   - 修复: 只有在 Comfy 明确处于成功/完成状态(status.completed==true 或 status_str in {success,completed})时才进入 downloadOutputs;status 未知/为空时继续轮询,直到成功、明确失败或 ctx 超时。

**[MEDIUM]** 4. 任务事件订阅/推送机制是死代码:全代码库没有任何地方向 t.Subscribers 注册 channel(仅在 createTask 初始化空 map),因此 emit/emitTo 以及 updateTask 每次进度 tick 里做的 clone+扇出都是无效功。整个生命周期没有 push 通道,客户端只能靠轮询 GetTask。叠加 taskRetention=1h 的清理(server_core.go:60-72)与上面的无持久化:客户端只要错过轮询窗口(任务被 GC 或重启丢失),就再也观察不到终态。
   - 位置: `app/service/task_store.go:130-146,210-235;app/model/task.go:36;清理:app/service/server_core.go:60-72`
   - 为何危险: 唯一的状态获取途径是轮询,而终态任务 1 小时后被删除;客户端断连/延迟轮询/重启后即拿不到结果,失败也无法被感知。同时每秒一次的订阅扇出全程空转,属无谓开销。
   - 修复: 要么真正接入 SSE/回调把 updateTask 事件推给订阅者,要么删除死掉的 Subscribers/emit 机制并明确文档化为轮询模型,同时把终态任务保留时间调长或提供最终结果的持久查询,避免终态被清理后无法回查。

**[LOW]** 5. image_edit 的 no_swap 回退路径会让单个任务的总超时预算翻倍:主流程调用 waitForPromptOutputs 传入完整 timeoutSec(task_runner.go:153-163),回退再次调用又拿到一份全新的 timeoutSec(task_runner.go:177-187),没有跨整个任务的统一 deadline。最坏情况一个任务可跑约 2×timeoutSec(默认 2×1800s)。
   - 位置: `app/service/task_runner.go:148-204;app/service/task_poll.go:20-27`
   - 为何危险: 外层(网关/调用方)若按单份 timeoutSec 预期,回退路径会显著超时;worker 也被单任务长时间占用,拖慢队列。
   - 修复: 在 executeTask 起点创建一个覆盖整个任务的 context.WithTimeout,两段 waitForPromptOutputs 共享同一 deadline(把剩余时间传下去),而不是各自重新计时。

**[LOW]** 6. 临时文件/目录仅在硬崩溃时泄漏。正常与错误路径都已正确清理:materializeImageParam 对 URL/base64 分支用 defer os.Remove(image_input.go:81,88),decodeBase64ImageToTemp/downloadImageFromURL 在写失败时自清(image_input.go:210-215,155-160),downloadOutputs 的 S3 临时目录 defer os.RemoveAll(output_service.go:78-82)。但 os.CreateTemp/os.MkdirTemp 落在系统 TMPDIR,进程被 SIGKILL/崩溃于 upload 之前时会残留 go_gateway_img_* / chat-image-output-* 等孤儿文件,且启动时没有任何清扫。结合本项目频繁重启,会缓慢累积。
   - 位置: `app/service/image_input.go:150,205;app/service/output_service.go:78`
   - 为何危险: 任务问及的 decodeBase64ImageToTemp 临时文件在常规/失败路径均已清理(无泄漏);唯一泄漏面是异常终止残留 + 无启动兜底清扫。
   - 修复: 启动时清扫 TMPDIR 下 go_gateway_img_*、go_gateway_img_b64_*、chat-image-output-* 前缀的陈旧临时文件;或改用进程专属子目录并在启动时整目录清空。

**[LOW]** 7. 非终态任务永不被垃圾回收:cleanupFinishedTasks 只删除 completed/failed/cancelled(server_core.go:64-71)。若 comfyPool 为 nil 则根本不启动 worker 也不启动清理循环(server_core.go:43-48),此时 createTask 仍可入队,任务永远停在 queued,map 只增不减。另 taskCleanupLoop 自身没有 recover,一旦 cleanupFinishedTasks panic,清理协程静默死亡,终态任务从此不再回收(内存泄漏)。worker 循环本身的 recover 是完备的(server_core.go:74-85),这点没问题。
   - 位置: `app/service/server_core.go:43-48,52-72`
   - 为何危险: 配置缺失(comfyPool nil)或清理协程崩溃时,tasks map 单调增长且无告警;worker 有 recover,但清理协程无。
   - 修复: 给 taskCleanupLoop 加 defer/recover 并在崩溃后重启循环;对长期停留在 queued/running 且超出合理时长的任务加超时兜底(标记为 failed);comfyPool 为 nil 时应拒绝建任务或明确报错,而非静默入队。

**[LOW]** 8. createTask 在入队前就 emit 了一个 'queued' 事件(task_store.go:104),随后若队列已满(default 分支)才 delete 任务并返回 ErrQueueFull(task_store.go:106-113)。于是可能先推出一个 queued 事件、紧接着任务消失且没有任何终态事件跟进——'queued 有头无尾'。当前轮询模型下创建时尚无订阅者故无实害,但顺序脆弱。
   - 位置: `app/service/task_store.go:101-114`
   - 为何危险: emit-先于-入队校验的顺序,使失败创建仍产生一次瞬时 queued 事件;若将来接入 push 通道即成为脏事件。
   - 修复: 先做入队(select 到 queue)成功后再写入 tasks map 并 emit;队列满时直接返回 ErrQueueFull,不产生任何事件。


### 并发与资源管理 (Concurrency & Resource Management) — chat_image / go_gateway  (9 findings)

**[HIGH]** 1. 大 base64 入参被长期驻留在 task.Params 中,且在每次状态读写时通过 JSON 全量深拷贝。cloneTask() 调用 workflow_render.go 的 cloneMap()(json.Marshal+Unmarshal),而 cloneTask 会在 updateTask/emit/getTask 每次都被调用。i2v 任务的两张图 input_image_base64 + input_image_end_base64 约 134MB,轮询期间进度 ticker 每 1 秒触发一次 updateTask(最长 1800 秒),客户端每次 GET 状态又触发一次 getTask —— 即每秒对 134MB 做一次完整 JSON 序列化+反序列化,单个 30 分钟视频任务累计分配可达数百 GB,瞬时驻留 3×134MB,多任务并发极易 OOM。base64 在任务完成后仍随 taskRetention=1h 保留。
   - 位置: `app/service/workflow_render.go:50-77 (cloneMap/cloneTask); app/service/task_store.go:117-146,210-235 (getTask/updateTask/emit); app/service/task_poll.go:59-64 (每秒进度 tick); app/service/constants.go:11 (taskRetention=1h)`
   - 为何危险: materializeImageParam 用完 base64 后并未从 task.Params 剥离,而 cloneTask 每次都用 JSON 往返深拷贝整个 Params。这是最主要的内存放大与 OOM 来源。
   - 修复: materialize 之后立即从存储态 task.Params 删除 input_image_base64 / input_image_end_base64 / mask_base64 /*_url(它们仅在入队执行时用一次);或把原始 base64 存到不参与快照的独立字段。让 cloneTask 不再对大字段做 JSON 往返(scalar 字段已是值拷贝,Params 可浅拷贝或按需省略)。

**[HIGH]** 2. 缺乏基于内存的背压:队列上限是 256 个 task ID(仅计数,不计字节),每个任务在 s.tasks 里可持有约 134MB base64 —— 最坏情况约 34GB 常驻后才返回 503。同时 HMAC 中间件为验签必须把整个请求体(上限 160MB)一次性 io.ReadAll 进内存,而对并发进行中的大请求没有任何数量上限;随后 JSON 解码又复制一份 base64 字符串。多个大 body 并发上传即可打爆内存,队列的计数上限根本拦不住。
   - 位置: `app/service/server_core.go:36 (queue 容量 256); app/service/http_middleware.go:147-160 (全量缓冲 body); app/service/constants.go:19 (maxRequestBody=160MB); app/api/v1_handler.go / v1_text_handler.go (JSON 解码复制)`
   - 为何危险: 背压只在“任务计数”维度存在,且发生在 body 缓冲+JSON 解码+建任务之后;真正的内存峰值(HMAC 缓冲、解码副本、入队保留)完全不受 256 这个阈值约束。
   - 修复: 对大 body 端点增加全局在途并发信号量(限制同时缓冲/解码的大请求数);把队列深度按“单任务内存占用”下调或改为按字节配额;鼓励改用 *_url 输入而非内联 base64,避免整段驻留。

**[HIGH]** 3. task_store 存在数据竞争:createTask 在把 task.ID 推入 s.queue(第 107 行)之后,于第 114 行 return cloneTask(task) —— 此处读取 task 的 Status/Progress/Message/Backend/Params 等字段却未持有 s.mu。而 worker 可能已从队列取出该 ID 并在 executeTask→updateTask 中(持锁)写 t.Status='running'、t.Progress、t.Backend 等同一个 *Task。同一对象上的无同步读(out := *t、json.Marshal(t.Params))与写并发 = 真实 data race,go test -race 可检出,并可能返回撕裂/不一致的快照。
   - 位置: `app/service/task_store.go:101-114 (先入队 line107,再无锁 cloneTask line114); 冲突写发生在 app/service/task_runner.go:37-46 经由 task_store.go:130-146 updateTask`
   - 为何危险: 入队即意味着另一 goroutine 可立刻访问同一个 *Task,而返回值的克隆没有走锁保护路径(与 getTask/emit 的持锁克隆不同)。
   - 修复: 在推入队列之前先 snapshot := cloneTask(task) 并返回它;或将第 114 行的 cloneTask 放在 s.mu.RLock 保护下执行。

**[MEDIUM]** 4. 没有真正的 GPU/ComfyUI 后端并发控制,只有 worker 数量这一粗粒度上限。ComfyPool.Next() 是无状态盲轮询,不感知后端是否繁忙;GO_WORKERS 可被配置得大于后端数量,直接对同一 GPU 过量投递;即便默认 workerCount=后端数,一个跑得快的 worker 也可能连续 Next() 把两个并发任务打到同一后端,而另一后端空闲。无 per-backend 信号量/背压,导致 GPU 争抢与延迟抖动。
   - 位置: `app/service/comfy_client.go:49-61 (ComfyPool.Next 盲轮询); app/service/app.go:62 (workerCount 默认=后端数,可被 GO_WORKERS 覆盖); app/service/task_runner.go:23 (executeTask 取后端)`
   - 为何危险: 调度基于“调用 Next 的先后顺序”而非“后端是否空闲”,轮询不能保证并发任务在后端间均匀分布,也不能防止超额投递。
   - 修复: 为每个后端加并发信号量(如每后端最多 N 个在跑),或改成可用性感知调度;并对 GO_WORKERS 相对后端数做上限校验。

**[MEDIUM]** 5. 图像编辑 multipart 路径内存放大严重:ParseMultipartForm(160MB) 先在内存缓冲,随后 io.ReadAll(f) 把整张图再次读入内存,再 base64 编码(×1.33)存入 params —— 一个 50MB 文件在单请求内会有多份完整副本同时驻留,且对并发请求数无限制。叠加 HMAC 中间件此前已把整个 body 读进内存,等于重复缓冲。
   - 位置: `app/api/v1_parse.go:16 (ParseMultipartForm 160MB), 41-45 (image ReadAll+base64), 53-57 (mask ReadAll+base64)`
   - 为何危险: 上传文件被反复整体读入内存并再编码为 base64,而不是流式落盘/直传 Comfy。
   - 修复: 把上传文件流式写入临时文件或直接转发给 comfy.UploadImage,不要在内存里 ReadAll 再 base64;并调低 ParseMultipartForm 的内存预算让超限部分落盘。

**[LOW]** 6. safeHTTPClient 的 Transport 只设置了 DialContext,其余字段取零值:IdleConnTimeout=0(空闲连接永不超时回收)、MaxIdleConns=0(总空闲连接无上限)。该客户端会去连接用户提供的任意外部主机(图片下载),空闲连接对众多不同 host 持续累积且不过期,长期运行会缓慢泄漏 socket/fd 及每连接的 readLoop goroutine。
   - 位置: `app/service/image_input.go:34-52 (safeHTTPClient 的 Transport 配置)`
   - 为何危险: 面向任意外部 host 的下载客户端未配置空闲连接回收,连接池随访问主机数量单调增长。
   - 修复: 显式设置 IdleConnTimeout(如 30-90s)、MaxIdleConns、MaxIdleConnsPerHost、ExpectContinueTimeout,并考虑 ForceAttemptHTTP2。

**[LOW]** 7. 下载 Comfy 产物时 ViewFile 用 io.ReadAll 把整个输出文件读入内存(content []byte),视频产物可达数十 MB,再交给 ffmpeg 转码;并发任务叠加会抬高内存峰值。
   - 位置: `app/service/comfy_client.go:120-136 (ViewFile 全量 ReadAll); app/service/output_service.go:135 (调用点)`
   - 为何危险: 产物先整体入内存再落盘转码,而非流式写临时文件。
   - 修复: 将产物流式写入临时文件(io.Copy 到 os.CreateTemp)再转码,避免整段驻留内存。

**[LOW]** 8. 任务级 SSE 订阅机制是死代码:Task.Subscribers 只在建任务时初始化为空 map,全代码库没有任何地方向其写入订阅者,emit/emitTo 每次都遍历一个恒空集合。若日后接线启用,当前实现没有在客户端断开时关闭/移除 channel 的路径,存在潜在的 goroutine/channel 泄漏隐患。
   - 位置: `app/model/task.go:36 (Subscribers 定义); app/service/task_store.go:98 (仅初始化为空), 210-235 (emit/emitTo 遍历空集)`
   - 为何危险: 订阅集永不被填充,相关克隆/遍历逻辑纯属无效开销,且缺少注册/注销与断连清理。
   - 修复: 删除未使用的订阅相关代码;若确需 SSE,补上 subscribe/unsubscribe 且在连接断开(r.Context().Done())时移除并关闭 channel。

**[LOW]** 9. 对 Comfy 后端的多个 HTTP 调用(QueuePrompt/GetHistory/postJSON)在用 json.Decoder 解出一个 JSON 值后直接 Close,未排空剩余 body,导致底层连接不满足 keep-alive 复用条件,高并发下增加 TCP/TLS 建连开销。
   - 位置: `app/service/comfy_client.go:81-95 (QueuePrompt), 102-118 (GetHistory), 220-250 (postJSON)`
   - 为何危险: 未 drain 到 EOF 的响应体会让 http.Transport 放弃复用该连接。
   - 修复: Close 前对 resp.Body 执行 io.Copy(io.Discard, resp.Body) 以促成连接复用。

TOTAL: 44 findings
