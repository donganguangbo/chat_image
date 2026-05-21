# Go Gateway RESTful 接入文档（黑盒模式）

本文档面向外部调用方。默认不暴露工作流细节，只暴露四个业务类别接口。

## 1. 服务地址

- Base URL：`http://127.0.0.1:8090`
- 健康检查：`GET /healthz`

```bash
curl -s http://127.0.0.1:8090/healthz
```

## 2. 黑盒类别接口

## 2.1 查询类别

`GET /api/v1/generate/categories`

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/categories | jq
```

## 2.2 创建任务

- `POST /api/v1/generate/t2i` 文生图
- `POST /api/v1/generate/t2i-darkbeast6` DarkBeast6 简易混合文生图
- `POST /api/v1/generate/edit` 编辑图（锁定生图）
- `POST /api/v1/generate/t2v` 文生视频
- `POST /api/v1/generate/i2v` 图生视频

统一请求体：

```json
{
  "prompt": "masterpiece, cinematic portrait",
  "negative_prompt": "lowres, blurry",
  "width": 1024,
  "height": 1024,
  "input_image": "/abs/path/a.png",
  "input_image_url": "https://example.com/a.png",
  "input_image_base64": "data:image/png;base64,...",
  "input_image_end": "/abs/path/b.png",
  "input_image_end_url": "https://example.com/b.png",
  "input_image_end_base64": "data:image/png;base64,...",
  "params": {
    "seed": -1,
    "steps": 8,
    "lora_enabled": true,
    "lora_name": "REDZ15_DetailDaemonZ_lora_v1.1.safetensors",
    "lora_strength_model": 0.8,
    "lora_strength_clip": 0.8,
    "detail_targets": ["pussy", "nipple", "breast"],
    "detail_bbox_models": ["bbox/pussyV2.pt", "bbox/nipple.pt", "bbox/female_breast_v3.pt"]
  }
}
```

字段说明：

- `prompt`：必填
- `negative_prompt`：可选
- `width` / `height`：建议填写
- `input_image`：`edit`/`i2v` 常用（本地路径）
- `input_image_url`：远程图片 URL
- `input_image_base64`：Base64 或 data URL 图片
- `input_image_end` / `input_image_end_url` / `input_image_end_base64`：`i2v` 首尾帧流程
- `params`：可选，覆盖默认参数

说明：

- `seed < 0` 会自动随机。
- `input_image` 本地路径、`input_image_url`、`input_image_base64` 都会先转成 Comfy 可读输入并自动上传。
- 视频请求若未传 `size_long`，服务端会按 `max(width,height)` 自动推导。

编辑图（`/api/v1/generate/edit`）常用参数补充：

- `params.lora_enabled`：是否启用 LoRA 注入（方案 C）
- `params.lora_name`：LoRA 文件名（来自 `models/loras`，如 `REDZ15_DetailDaemonZ_lora_v1.1.safetensors`）
- `params.lora_strength_model` / `params.lora_strength_clip`：LoRA 强度（建议 `0.7~0.9`）
- `params.detail_target`：单个便捷目标检测器（会自动映射 `detail_bbox_model`）
  - 可选值：`face` / `pussy` / `nipple` / `breast` / `female_breast` / `penis`
- `params.detail_targets`：多个便捷目标检测器（数组或逗号分隔字符串），会串行生效
- `params.detail_bbox_models`：多个检测器模型（数组或逗号分隔字符串），会串行生效
- `params.detail_bbox_model`：直接指定检测器模型名（如 `bbox/pussyV2.pt` 或 `pussyV2.pt`）
  - 若同时传单个和多个参数，会合并去重后按顺序执行（`detail_targets` / `detail_target` 在前）
  - 检测器越多，速度会越慢

DarkBeast6 简易混合文生图（`/api/v1/generate/t2i-darkbeast6`）默认参数：

- 固定 workflow：`t2i_darkbeast6_simple_hybrid`
- 默认尺寸：`768x1440`
- `params.steps_stage1`：`5`
- `params.steps_stage2`：`15`
- `params.cfg_stage1` / `params.cfg_stage2`：`1.0`
- `params.denoise_stage2`：`0.75`
- `params.upscale_by`：`1.25`
- 默认模型：`DarkBeastZ6-BlitZ-F32-ComfyUI.safetensors`
- 默认文本编码器：`zImageTurbo_textEncoder.safetensors`
- 默认 VAE：`ae.safetensors`

返回：`201 Created`

```json
{
  "id": "186a3f...",
  "workflow_id": "t2i_normal",
  "backend": "http://127.0.0.1:8189",
  "status": "queued",
  "progress": 0,
  "message": "queued",
  "params": {}
}
```

## 3. 任务查询与进度

## 3.1 获取任务详情

`GET /api/v1/tasks/{task_id}`

```bash
curl -s http://127.0.0.1:8090/api/v1/tasks/<task_id> | jq
```

## 3.2 获取任务列表

`GET /api/v1/tasks`

```bash
curl -s http://127.0.0.1:8090/api/v1/tasks | jq
```

## 3.3 SSE 流式进度

`GET /api/v1/tasks/{task_id}/stream`

```bash
curl -N http://127.0.0.1:8090/api/v1/tasks/<task_id>/stream
```

事件示例：

```text
event: task_update
data: {"event":"task_update","task":{...}}
```

## 4. 输出文件

任务完成后读取 `task.outputs`：

- 图片：`/outputs/images/<filename>`
- 视频：`/outputs/videos/<filename>`

```bash
curl -I http://127.0.0.1:8090/outputs/images/<filename>
```

## 5. 默认工作流映射（配置）

文件：`go_gateway/configs/service.yaml`

```yaml
expose_workflow_catalog: false
expose_raw_task_create: false

default_workflows:
  text_to_image: t2i_normal
  image_edit: inpaint_zimage_lock
  text_to_video: t2v_default
  image_to_video: i2v_wan_remix_single
```

含义：

- 外部请求只选类别，不传 `workflow_id`
- 类别到真实 workflow 的映射在服务端配置
- `t2i-darkbeast6` 是固定 workflow 接口，不受 `default_workflows` 影响

## 6. 内部接口（默认关闭）

以下接口用于内部调试，可在 `service.yaml` 打开：

- `GET /api/v1/workflows`（`expose_workflow_catalog`）
- `POST /api/v1/tasks`（`expose_raw_task_create`）

## 7. 错误码

- `200` 查询成功
- `201` 任务创建成功
- `400` 参数错误
- `404` 资源不存在/接口关闭
- `405` 方法不允许
- `500` 服务内部错误

错误响应通常为纯文本字符串（`http.Error`）。
