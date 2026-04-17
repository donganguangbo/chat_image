# 红潮 Pose 工作流提示词参考（go_gateway）

本文档基于当前默认配置：

- `text_to_image -> t2i_red_pose`
- `image_edit -> inpaint_zimage_lock_red_pose`

适用目标：在红潮链路下，提高“姿势”和“服装”一致性，减少第 2 阶段重绘导致的跑偏。

## 1. 当前默认参数（已落地）

### 文生图 `t2i_red_pose`

- `red_pose_lora_strength_model: 0.4`
- `red_pose_lora_strength_clip: 0.35`
- `steps_stage1: 14`
- `cfg_stage1: 1.6`
- `steps_stage2: 12`
- `cfg_stage2: 1.1`
- `denoise_stage2: 0.24`

### 编辑图 `inpaint_zimage_lock_red_pose`

- `lora_strength_model: 0.4`
- `lora_strength_clip: 0.35`
- `lora_scope: before_only`
- `steps_stage2: 12`
- `cfg_stage2: 1.1`
- `denoise_stage2: 0.24`

说明：

- 这是一套“弱 LoRA + 低重绘”的保姿态配置。
- 姿势 LoRA 是软约束，不是硬约束（不是 OpenPose ControlNet）。

## 2. 提示词结构（推荐顺序）

建议把提示词按固定槽位写，减少信息互相打架：

1. 主体锚定：年龄段、体型、发型、肤色（稳定身份）
2. 姿势几何：躯干方向、骨盆高度、腿部角度、手臂支撑点（核心）
3. 构图与镜头：机位、焦段、景别、画幅比例（避免视角漂移）
4. 场景与光线：场景材质、主光方向、补光（提高可复现）
5. 服装约束：服装名称、材质、穿着状态、覆盖区域（核心）
6. 画质与风格：写实、景深、细节（最后再加）

## 3. 可复制模板

### 模板 A：姿势优先（文生图）

```text
[主体锚定],
pose: [躯干朝向] + [骨盆高度] + [腿部角度] + [手臂支撑点] + [头部方向],
camera: [rear three-quarter view], [50mm], [medium full body], [9:16], [centered],
scene: [环境描述], lighting: [主光方向+软硬程度],
wardrobe: [服装品类] made of [材质], [穿着状态], [覆盖区域],
photorealistic, sharp focus, natural skin texture
```

### 模板 B：服装优先（文生图）

```text
[主体锚定],
wardrobe priority: [服装名称1], [服装名称2], [材质], [颜色], [长度], [贴合程度],
pose: [简洁姿势描述，不要超过 2 句],
camera: [机位+焦段+景别+比例],
scene and light: [场景+光线],
photorealistic, professional photography
```

### 模板 C：编辑图（锁定生图）

```text
keep identity and face consistency,
keep original camera framing and perspective,
only adjust pose to: [目标姿势],
keep wardrobe as: [目标服装],
preserve background and lighting style,
photorealistic, high detail
```

## 4. 负面词策略（精简而明确）

建议分三组，不要无限堆叠：

1. 解剖错误：`deformed body, bad anatomy, extra limbs, fused limbs`
2. 构图错误：`perspective distortion, fisheye distortion, close-up distortion`
3. 质感错误：`lowres, blurry, jpeg artifacts, watermark, logo`

姿势特定反向词（按需加）：

- 如果总是“腿抬过高”：加 `legs straight up, vertical legs`
- 如果总是“坐姿/跪姿漂移”：加 `sitting, kneeling, squatting`

## 5. 症状 -> 调整动作

### 症状 1：腿部经常跑成竖直

1. 提示词里把角度写成“几何句子”，例如“thighs roughly parallel to tabletop, knees slightly bent”
2. `denoise_stage2` 再降一档（`0.24 -> 0.20`）
3. 负面词补 `vertical legs`

### 症状 2：服装经常被冲掉

1. 把服装写到提示词前半段，并给“覆盖区域+材质+长度”
2. 降 `red_pose_lora_strength_model/clip`（例如 `0.35/0.30`）
3. 删除与服装冲突的描述词

### 症状 3：人物比例偶尔夸张

1. `cfg_stage2` 保持低值（`1.0~1.1`）
2. 负面词补 `extreme proportions`
3. 固定 seed 做对照，避免误判为随机波动

## 6. 测试方法（避免“看起来一样”）

固定实验法：

1. 固定 `seed`（不要 `-1`）
2. 固定 `width/height`
3. 每轮只改 1 个变量（提示词或 1 个参数）
4. 同一组至少跑 4 张再判断趋势

推荐先测的 3 档：

1. 保守：`lora 0.35/0.30`, `denoise_stage2 0.20`
2. 当前：`lora 0.40/0.35`, `denoise_stage2 0.24`
3. 激进：`lora 0.50/0.45`, `denoise_stage2 0.28`

## 7. go_gateway 调用示例

### 文生图（覆盖默认参数）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/t2i \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "按模板 A/B 写你的提示词",
    "width": 832,
    "height": 1536,
    "params": {
      "seed": 370061708175211,
      "red_pose_lora_strength_model": 0.4,
      "red_pose_lora_strength_clip": 0.35,
      "steps_stage2": 12,
      "cfg_stage2": 1.1,
      "denoise_stage2": 0.24
    }
  }' | jq
```

### 编辑图（锁定生图）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/edit \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "按模板 C 写你的提示词",
    "width": 832,
    "height": 1536,
    "params": {
      "input_image": "/root/autodl-tmp/projects/chat_image/uploads/input.png",
      "seed": 370061708175211,
      "lora_strength_model": 0.4,
      "lora_strength_clip": 0.35,
      "steps_stage2": 12,
      "cfg_stage2": 1.1,
      "denoise_stage2": 0.24
    }
  }' | jq
```

## 8. 预期边界

如果你已经按上面做了仍然持续“姿势跑偏”，根因通常是：

- LoRA 软约束上限已到

这时应升级为硬姿态控制链路（OpenPose/ControlNet），而不是继续无限调词。

## 9. 硬约束姿势链路（ControlNet + DWPose）

当第 8 节的软约束上限到头、或任务本身就需要严格锁定姿势时，使用 `inpaint_zimage_lock_red_pose_cn` 工作流。

### 9.1 工作流结构（单次 API 调用内部两段）

```
Pass A（姿势草图）：
  [prompt + LSP LoRA] → KSamplerAdvanced(10 steps) → VAEDecode →
  DWPreprocessor → 黑底骨架图 S
  （骨架图通过 SaveImage(_skeleton) 暴露，调试可取）

Pass B（最终渲染）：
  [prompt + LSP LoRA] + [骨架 S via FLUX ControlNet Union(openpose)] →
  KSamplerAdvanced(Stage1) → UltimateSDUpscale(Stage2) →
  ReActor FaceSwap(input_image 提脸) → FaceDetailer → 最终图
```

一次调用、一个 task_id、一条 SSE 进度流。两段共用 CheckpointLoader / LoraLoader / ControlNetLoader，模型加载只发生一次。

### 9.2 API 调用

`/api/v1/generate/edit` 接口**不变**，但它通过 `service.yaml` 的 `default_workflows.image_edit` 决定用哪个底层工作流。有两种方式切到 `_cn` 版本测试：

**方式 A：临时切默认（推荐，上线流程一致）**

编辑 `go_gateway/configs/service.yaml`：
```yaml
default_workflows:
  image_edit: inpaint_zimage_lock_red_pose_cn   # 从原 inpaint_zimage_lock_red_pose 改过来
```

重启 go_gateway，`/api/v1/generate/edit` 就会走硬约束链路：
```bash
curl -X POST http://127.0.0.1:8090/api/v1/generate/edit \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "按模板 C 写你的提示词（把重点放在姿势几何描述上）",
    "params": {
      "input_image": "/path/to/face_reference.png",
      "seed": 370061708175211,
      "controlnet_strength": 0.75,
      "controlnet_end_percent": 0.7
    }
  }' | jq
```

**方式 B：启用 raw task 接口（需要改 service.yaml 另一个开关）**

如果想让旧工作流也继续可用（AB 对比测试场景），临时开启 raw task：
```yaml
expose_raw_task_create: true   # 原为 false
```

重启后可以直接指定 workflow_id：
```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "workflow_id": "inpaint_zimage_lock_red_pose_cn",
    "prompt": "...",
    "params": {"input_image": "...", "seed": 370061708175211}
  }' | jq
```

测完务必把 `expose_raw_task_create` 关回 `false`（`/api/v1/tasks` 会暴露内部工作流名，不适合长期对外开放）。

### 9.3 输出

响应的 `outputs[]` 会包含多张图：
- `<prefix>_skeleton_xxx.png`：Pass A 提取的骨架，用来核对姿势是否符合预期
- `<prefix>_before_xxx.png`：ReActor 换脸前
- `<prefix>_after_swap_xxx.png`：ReActor 换脸后
- `<prefix>_after_xxx.png`：FaceDetailer 处理后的最终输出

### 9.4 关键参数

| 参数 | 默认 | 作用 |
|------|------|------|
| `controlnet_name` | `flux-controlnet-union.safetensors` | ControlNet 模型文件 |
| `controlnet_strength` | `0.75` | 硬约束强度（0 ~ 1） |
| `controlnet_start_percent` | `0.0` | 开始应用 ControlNet 的步比例 |
| `controlnet_end_percent` | `0.7` | 停止应用的步比例（过高会过度约束） |
| `pass_a_steps` | `10` | 骨架生成的采样步数（越高越像姿势） |
| `pass_a_cfg` | `1.6` | Pass A 的 CFG |
| `dwpose_detect_hand` | `enable` | DWPose 是否识别手部 |
| `dwpose_detect_face` | `enable` | DWPose 是否识别脸部 |
| `dwpose_resolution` | `1024` | 骨架图分辨率 |

### 9.5 调参建议

| 症状 | 调整 |
|------|------|
| 姿势偶尔漂移（10% 左右跑偏） | `controlnet_strength` 0.75 → 0.85，`end_percent` 0.7 → 0.8 |
| 画面过于僵硬 / 姿势像"假人" | `controlnet_strength` → 0.55，`end_percent` → 0.5 |
| 骨架图本身就不对 | 改 `seed`；或提高 `pass_a_cfg` → 1.8、`pass_a_steps` → 14 |
| 骨架图关节缺失（缺手/缺脚） | 检查 `dwpose_detect_hand/body/face` 是否为 `enable` |
| 服装被冲刷 | 降 `lora_strength_model/clip` → 0.35/0.30 |
| 整体崩图 / 色彩异常 | redcraft 与 FLUX ControlNet 可能不兼容，见 9.5 |

### 9.6 应急回退

如果 `verify_controlnet.sh` 的兼容性微测试失败，或输出明显崩图，说明 redcraft 底模与 FLUX ControlNet Union 不兼容。最小代价的回退：

1. 复制 `go_gateway/configs/workflows/inpaint_zimage_lock_red_pose_cn.yaml`
2. 将 `model_name` 改为 `flux1-dev.safetensors`（如果还没有，跑 `scripts/download_all_models.sh`）
3. 工作流 JSON 无需修改

代价：放弃 redcraft 的画风，换取 ControlNet 硬约束能力。

### 9.7 切换为默认

当 `_cn` 版本在你的典型 case 下表现稳定后，编辑 `go_gateway/configs/service.yaml`：

```yaml
default_workflows:
  image_edit: inpaint_zimage_lock_red_pose_cn   # 原: inpaint_zimage_lock_red_pose
```

重启 go_gateway 即可。`/api/v1/generate/edit` 对外 API 完全不变，客户端感知不到区别（除了姿势更准了）。
