# i2v 多 LoRA 支持 — Gateway 改动设计

## 1. 背景

当前 i2v 管道走 WAN 2.2 单底模(`wan_remix` 或 AIO)。对"动作类"需求(后入 / 传教士 / 口交 / 女上位 等),底模自身能力不够稳定,需要外挂姿势/动作 LoRA。

上游(Next.js)的 prompt rewriter 已有成熟的"触发词白名单"机制(用于图像侧 LSP),可平行扩展到视频侧:LLM 按动作语义挑选触发词,服务端据此挂载对应 LoRA 文件。Gateway 侧目前不支持 i2v 动态挂载 LoRA,本文描述该扩展。

---

## 2. 现状

### 2.1 已有的 LoRA 挂载机制

`main.go:1715` `applyOptionalImageEditLora`:
- 根据 `params.lora_enabled` + `params.lora_name` 动态插入 `LoraLoader` / `LoraLoaderModelOnly` 节点
- 通过 `relinkNodeInputSourceForNodeIDs` 重连 `model` / `clip` 边
- 支持 `lora_scope: before_only` 排除特定分支(如 FaceDetailer)
- 调用点:`main.go:499`
  ```go
  if cfg.Mode == "image_edit" || cfg.Mode == "t2i" {
      if err := applyOptionalImageEditLora(rendered, params, cfg.Mode); err != nil { ... }
  }
  ```

已有用例:`configs/workflows/inpaint_zimage_lock_red_pose.yaml` 挂 `LovinSnuggly_Positions_LSP.safetensors`。

### 2.2 对 i2v 场景不足

三个缺口:

1. **Mode gate 排除 i2v** — line 499 不进入挂载逻辑
2. **单 LoRA 槽位** — 多姿势场景需同时挂 2~5 个(后入 + 口交 + 传教士 + 女上 等)
3. **WAN 2.2 双 stage UNET 未处理**
   - `workflows/i2v_wan_remix_single.json` 节点 `279` = stage1 UNET,节点 `280` = stage2 UNET
   - WAN 2.2 社区 LoRA 常成对发布(`-high.safetensors` → stage1,`-low.safetensors` → stage2)
   - 现有单槽机制只能挂一个 base node,无法同时覆盖双 stage

---

## 3. 改动范围(Gateway 侧)

改动全部落在 `go_gateway/` 目录,workflow JSON 不改,不影响已有 image_edit / t2i 行为。

### 3.1 打开 i2v 的 LoRA 调用开关

`main.go:499` 扩展 mode 判定:

```go
if cfg.Mode == "image_edit" || cfg.Mode == "t2i" || cfg.Mode == "i2v" {
    if err := applyOptionalLora(rendered, params, cfg.Mode); err != nil { ... }
}
```

函数名建议从 `applyOptionalImageEditLora` 改为 `applyOptionalLora`(向后改名,内部按 mode 分派),或新增 `applyOptionalVideoLora` 专管 i2v。选前者,逻辑复用度更高。

### 3.2 支持多 LoRA 数组

请求 payload 新增 `loras` 数组字段(优先级高于旧的单 `lora_name` 字段):

```json
{
  "prompt": "...",
  "loras": [
    {
      "name": "doggy_slider_high.safetensors",
      "attach": "stage1",
      "strength": 0.8
    },
    {
      "name": "doggy_slider_low.safetensors",
      "attach": "stage2",
      "strength": 0.8
    },
    {
      "name": "oral_insertion_low.safetensors",
      "attach": "stage2",
      "strength": 0.7
    }
  ]
}
```

字段定义:

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | ✅ | LoRA 文件名,相对 `models/loras/` |
| `attach` | string | ✅(i2v) | `stage1` / `stage2` / `both` — 对应 WAN 2.2 的两个 UNET;image_edit/t2i 模式下可省略 |
| `strength` | float | ❌ | 默认 0.8;CLIP 强度同此值 |
| `strength_model` | float | ❌ | 单独设定 model 强度,覆盖 `strength` |
| `strength_clip` | float | ❌ | 单独设定 clip 强度(i2v 通常无 CLIP,忽略) |

#### 兼容性

- 如果 `loras` 存在,忽略 `lora_name` / `lora_enabled`
- 如果 `loras` 缺失但 `lora_enabled=true`,走旧单槽逻辑
- 如果两者都缺失,不挂 LoRA(等同现状)

### 3.3 workflow YAML 声明 stage 节点 ID

在 i2v workflow 配置里加元数据,告诉 Gateway 哪两个节点是 stage1 / stage2 UNET 源。

文件:`configs/workflows/i2v_wan_remix_single.yaml`

```yaml
defaults:
  # ... 现有字段 ...

  # 新增:LoRA 挂载目标节点映射
  lora_stage_node_ids:
    stage1: "279"   # high-noise UNETLoader 节点 ID
    stage2: "280"   # low-noise UNETLoader 节点 ID
```

对 `image_edit` / `t2i` 保持现有 `lora_base_node_id` 单字段(不需要 stage 概念)。

### 3.4 多 LoRA 链式叠加

同一 stage 挂多个 LoRA 时,后挂的 model 输入接到前一个 LoRA 节点的输出,不是原 UNET。ComfyUI 标准链式接法。

伪代码:

```go
for stage, loras := range grouped_by_stage {
    baseNodeID := cfg.LoRAStageNodeIDs[stage]
    currentSource := baseNodeID     // 初始源
    currentOutputIdx := 0

    for _, lora := range loras {
        newNodeID := allocateNodeID()
        prompt[newNodeID] = LoraLoaderModelOnly{
            model: [currentSource, currentOutputIdx],
            lora_name: lora.Name,
            strength_model: lora.Strength,
        }
        currentSource = newNodeID
        currentOutputIdx = 0
    }

    // 把原 UNET 的所有下游消费者重连到链末端
    relinkNodeInputSourceForNodeIDs(prompt, originalNodeIDs, baseNodeID, 0, currentSource, 0)
}
```

复用已有的 `relinkNodeInputSourceForNodeIDs` 工具函数,不增加新的图遍历逻辑。

### 3.5 i2v 使用 `LoraLoaderModelOnly`

WAN i2v 的 CLIP(UMT5)是独立节点,LoRA 通常只修 UNET。因此 i2v 模式下:

- 不走 `LoraLoader`(需要 clip 输入)
- 统一走 `LoraLoaderModelOnly`(只要 model 输入)

代码层面:`applyOptionalLora` 对 `cfg.Mode == "i2v"` 强制 `modelOnly=true`,忽略请求里的 `strength_clip`。

---

## 4. API 契约(给 Next.js 侧参考)

### 请求示例

```http
POST /api/v1/generate/i2v
Content-Type: application/json

{
  "prompt": "The woman bends forward... doggystyle... the man positions himself directly behind her...",
  "negative_prompt": "...",
  "width": 432,
  "height": 768,
  "input_image_url": "https://...",
  "params": {
    "duration_sec": 5,
    "resolution": "480p",
    "steps": 8,
    "cfg": 1.0,
    "loras": [
      { "name": "doggy_slider_high.safetensors", "attach": "stage1", "strength": 0.8 },
      { "name": "doggy_slider_low.safetensors",  "attach": "stage2", "strength": 0.8 }
    ]
  }
}
```

### 响应

与现有 i2v 响应完全一致(task_id / status / poll 方式不变)。

### 错误

- `loras[i].name` 文件不存在:任务失败,`error.reason` 返回具体文件名
- `loras[i].attach` 非法值:任务失败,`error.reason` 列出合法值
- `lora_stage_node_ids.stage1` 或 `stage2` 在 workflow 里找不到对应节点:任务失败

---

## 5. 向后兼容

### 5.1 现有 i2v 请求(不带 loras)

- `loras` 数组缺失 → 不挂任何 LoRA → 行为与现状 100% 一致
- 旧的 `lora_enabled` / `lora_name` 字段在 i2v 模式下**新路径不读**,但不删除字段(image_edit 仍用)

### 5.2 image_edit / t2i 模式

- 这两个模式不读 `loras` 数组,仅读现有的 `lora_name` 字段
- 函数 `applyOptionalLora` 内部按 mode 分派,上层调用方式不变

### 5.3 workflow 文件

- JSON 模板无需变动(LoRA 节点仍是运行时动态插入,不预埋占位)
- YAML 配置新增 `lora_stage_node_ids` 是可选字段,缺失时 i2v LoRA 挂载直接失败并返回明确错误

---

## 6. 测试计划

### 6.1 单元/集成

1. **不带 loras 的 i2v 请求**:输出与改动前字节级一致(无回归)
2. **单 LoRA 挂 stage2**:workflow 渲染后包含一个 `LoraLoaderModelOnly` 节点,`model` 输入指向节点 280,下游节点 331 的 `model` 输入指向新 LoRA 节点
3. **双 LoRA 都挂 stage2**:渲染后两个 LoRA 节点链式相连,下游节点 331 指向链末端
4. **LoRA 挂 stage1 + stage2**:两条独立链,节点 279/280 下游分别重连
5. **非法 attach 值**:任务立即失败,错误信息清晰
6. **LoRA 文件不存在**:ComfyUI 执行时报错,Gateway 回传具体文件名

### 6.2 端到端

1. `doggy_slider` 双变体挂载 + 后入 prompt:对比不挂 LoRA 的同 prompt 输出,观察第二人是否稳定出现
2. 三个 LoRA 同时挂(后入 + 口交 + 传教士):各 prompt 轮流跑,验证同一 workflow 能区分触发(由 prompt 里的触发词决定哪个生效强)
3. `image_edit` 模式现有请求:无回归

---

## 7. 非目标

- ❌ 为 titjob / spanking 提供 LoRA — 社区目前无公开模型,超出本次改动范围
- ❌ 修改 Next.js 侧的 prompt rewriter — 另行在应用仓库处理(见应用仓库 `generation-orchestrator-service.ts` 的平行改动)
- ❌ 改 workflow JSON 预埋 LoRA 节点 — 保持当前"运行时动态插入"策略,避免 JSON 模板分裂
- ❌ 训练自研 LoRA — 独立方向,不影响本改动

---

## 8. 执行顺序

建议先 Gateway、后 Next.js:

1. Gateway 按本文改,**保持向后兼容**(缺 `loras` 字段时行为 100% 等同现状)
2. 下载社区 LoRA 文件到 `models/loras/`,用 curl 构造带 `loras` 数组的 i2v 请求手测
3. Next.js 侧加 env 配置、rewriter 触发词白名单、payload 组装 `loras`(应用仓库另行实施)
4. 开 env 开关端到端跑"后入式"等用例,与无 LoRA 基线对比

每一步都可独立验证,不产生"空转"(Next.js 改好但 Gateway 不认 / Gateway 改好但 Next.js 没发)。

---

## 9. 相关文件

- `go_gateway/main.go`:`applyOptionalImageEditLora` (line 1715),调用点 line 499
- `go_gateway/configs/workflows/i2v_wan_remix_single.yaml`:需加 `lora_stage_node_ids`
- `workflows/i2v_wan_remix_single.json`:UNETLoader 节点 `279` / `280`(不改 JSON,只在 YAML 声明)

---

## 10. 为什么"动作 → LoRA 文件"的映射放客户端(Next.js),不放 Gateway

本节固化职责边界,避免后续读到设计文档的人重新讨论一次。

### 10.1 结论

- **Gateway 只认 LoRA 文件名**(`loras: [{name, attach, strength}]`),不理解"后入 / 乳交"这种动作语义
- **动作 → LoRA 文件的映射表** 放在 Next.js(env 驱动),跟现有 LSP 的"rewriter 决定触发词 + 挂哪个文件"模式完全一致

### 10.2 理由

1. **职责单一**:Gateway 是 ComfyUI workflow 执行器,业务语义(动作分类)应留在业务层
2. **迭代速度**:新增 LoRA / 换版本 / 调强度是周级事件,放 Next.js env 可热更;放 Gateway 要改 Go 代码并重启服务
3. **LLM 路径天然**:Rewriter 已在识别动作,Next.js 拿结果查表→文件名是一步到位,无需把"动作"语义再传一次给 Gateway 让它做第二次映射
4. **与 LSP 架构一致**:`image_edit` 的 LSP 已是"Gateway 只认 `lora_name`,Next.js 决定用哪个 + 触发词"。多 LoRA 延续同一模式,不分裂架构
5. **调试友好**:效果不对时想临时换版本,只改一处 env,不跨仓库不重启

### 10.3 Gateway 职责(清单)

**做**:
1. 接收 `loras` 数组并按 `attach` 挂到 YAML 声明的 `lora_stage_node_ids`
2. 同 stage 多 LoRA 链式串联(`LoraLoaderModelOnly`)
3. 基本校验:`attach` 合法值、`strength` 范围、文件不存在时透传 ComfyUI 错误

**不做**:
- 动作词表 / 触发词白名单 / 动作→文件映射表
- 根据 prompt 内容猜测该挂哪个 LoRA
- 按文件名后缀(`_HIGH` / `_LOW`)自动推断 stage(保持客户端显式传 `attach`,更可读)

### 10.4 客户端侧形状(参考)

```ts
// env(JSON 字符串,热更新)
VIDEO_LORA_ROSTER_JSON = {
  "doggystyle": [
    { "name": "iGoon_Blink_Back_Doggystyle_I2V_HIGH.safetensors", "attach": "stage1", "strength": 0.8 },
    { "name": "iGoon_Blink_Back_Doggystyle_I2V_LOW.safetensors",  "attach": "stage2", "strength": 0.8 }
  ],
  "titjob": [ ... ]
}

// 组装 payload
const loras = actions.flatMap(a => roster[a] ?? [])
payload.params.loras = loras
```

维护动作:
- 新增动作 → 加一条 env
- 换 LoRA 版本 → 改 env 里的 `name`
- 调强度 → 改 env 里的 `strength`
- Gateway 完全不动

### 10.5 接受的 tradeoff

客户端必须知道文件的 HIGH / LOW 身份(由命名约定或 roster 里显式标注解决)。不把这层抽象放 Gateway,是因为它属于"业务配置",不属于"执行器契约"。
