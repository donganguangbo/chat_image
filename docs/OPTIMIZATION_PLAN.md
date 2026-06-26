# 优化修复计划（基于已验证的代码审查）

来源：6 维度审查 → 31 条发现 → 25 条经独立复核确认。本计划只覆盖**修法明确且安全**的发现，
按代码区域分批，避免并行编辑同一文件冲突；由 sonnet 子 agent 分批执行，主循环（Opus）逐批验收。

## 执行方式
- 基线：动手前 `git add -A` 把现有改动暂存为基线。
- 每批：派一个 sonnet-4.6 子 agent 执行 → 子 agent 自跑 `go build ./...` + `go test ./...` →
  主循环 `git diff` 审收（只看本批改动）→ 通过则 `git add -A` 折进基线，进入下一批；不通过则打回重做。
- 约束（所有子 agent）：最小 diff、贴合现有风格、不动无关代码、保持其它行为不变；改完报告“改了哪些行 + 前后对比 + build/test 结果”。

## 批次

### Batch 1 — 安全高危（区域 main.go ~2740–2911，图片输入处理）
1. **SSRF**（`downloadImageFromURL` main.go:2812）：改用一个**加固的包级 http.Client**——
   `Transport.DialContext` 解析后拒绝 loopback/私网/link-local/unspecified IP（`net.IP.IsLoopback/IsPrivate/IsLinkLocalUnicast/IsUnspecified`，含 169.254、::1、fc00::/7），
   并设 `CheckRedirect` 对每一跳重跑 host 校验（防 DNS-rebind/重定向绕过）。同时合并 #20（去掉每次 new client）。
2. **本地文件读取**（`resolveExistingPath` + `case raw != ""` 分支 main.go:2740/2783）：整段删除；
   非 URL/非 base64 的值走已有 else `return nil`，当作 ComfyUI 侧文件名透传。确认无其它调用方。
3. **临时文件泄漏**（main.go:2851 与 2902）：`tmp.Write` 失败分支补 `tmp.Close(); os.Remove(tmp.Name())`。

### Batch 2 — 任务队列 / worker 健壮性（server/worker 区域）
4. **worker 无 panic recovery**（`workerLoop` main.go:402）：包 `defer recover()` → `failTask(id, ...)` 并继续循环。
5. **createTask 队列满阻塞**（main.go:1289）：入队改非阻塞 `select{case s.queue<-id: default:}`，满则标记任务失败并返回可映射 503 的哨兵错误（别把 "queued" 残留任务留在 map）。
6. **tasks map 永不淘汰**（main.go:1313）：仿 `newNonceCache` 起一个 ticker，按 TTL（如 1h）删除终态（completed/failed/cancelled）任务。

### Batch 3 — 生成执行 / 输出正确性（executeTask / downloadOutputs / waitForPromptOutputs）
7. **findHistoryEntry 误返回**（main.go:1136）：删 fallback 遍历，只信 keyed 命中，否则 `return nil`。
8. **GetHistory 错误被吞**（main.go:674）：循环里记 `lastErr`，超时错误信息带上。
9. **落盘失败被吞**（main.go:816）：`os.WriteFile` 失败改 `return nil, fmt.Errorf("write output %s: %w", ...)`。
10. **seed 被 JSON round-trip 截断**（main.go:1256）：`rand.Int63()` → `rand.Int63n(1<<53)`。
11. **多余 os.Stat**（main.go:817）：`Size: int64(len(content))`，删 `os.Stat`/`ifSize`（若 ifSize 变孤儿一并删）。

### Batch 4 — SSE / HTTP 边界 / 简化删减
12. **SSE 终止事件丢失**（main.go:1414/1647）：`handleTaskStream` 每次唤醒（含心跳）重取 `getTask(id)` 快照下发，状态终止即返回。
13. **无 body 上限**（main.go:3109/3316）：HMAC 中间件读 body 前 `http.MaxBytesReader`，超限返回 413；裸 `http.ListenAndServe` 换显式 `&http.Server{ReadHeaderTimeout:...}`。（顺带覆盖 #25 base64 先解码后判大小）
14. 简化删减（纯删/等价替换，零行为变化）：
    - 删死方法 `getTaskRaw`（main.go:1307）
    - `template_format` 二次归一化删除，直接用 `cfg.TemplateFormat`（main.go:464）
    - 合并两个 `relinkNodeInputSourceForNodeIDs*`，保留 ExcludingClass 版、其余传 nil（main.go:2553）
    - `inferImageExt` 删外层 switch，只留 `mime.ParseMediaType` 分支（main.go:2913）
    - `materializeImageParam` 去掉 `cleanupPath` 并行变量，临时文件分支用 `defer os.Remove`（main.go:2765）
    - 手写 base64 兼容 → 依次试 `base64.RawStdEncoding` / `base64.RawURLEncoding`（**不要**只用 RawURLEncoding，会回退标准字母表无填充）（main.go:2883）

## 后续补做（已完成）
- **S3 明文密钥外置**：凭证改从 `S3_ACCESS_KEY` / `S3_SECRET_KEY` 环境变量读取，`service.yaml` 不再存密钥，启动脚本透传。⚠️ 已泄露进 git 历史的那对 Linode 密钥仍需**轮换**。
- **#18 输出内存多份拷贝 + #19 ffmpeg 产物回读**：转码器改为返回临时文件路径 + cleanup（不再 `os.ReadFile` 回内存）；`downloadOutputs` 用 `io.Copy` 落盘（跨盘安全，非 rename）、S3 从目标文件流式上传；`UploadPrivate` 改 `io.Reader`。注：`ViewFile` 仍把源文件读进内存，是固有成本，不在这两条范围内。
- **#22 cloneTask 去 JSON round-trip**：`cloneTask` 改手写拷贝（值字段直接复制 + `Params` 深拷贝 + `Outputs` 切片头复制 + subscribers 置 nil）；`cloneMap` 保持 JSON 深拷贝不变（`applyImageEditNoSwapFallback` 承重）。

## 验收口径
每批：`go build ./...` 干净；`go test ./...` 通过；`git diff` 只含本批意图内改动；关键点抽查
（如 Batch1 后 `grep resolveExistingPath` 应为空、加固 client 存在；Batch4 后 SSE 走快照、MaxBytesReader 就位）。
