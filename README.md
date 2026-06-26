# Go Workflow Gateway

本仓库的唯一服务：一个 Go REST 网关，对接 ComfyUI，支持：

- 每个工作流一个 YAML 参数配置
- 用户只传 `prompt` + `width/height`（以及少量可选参数）
- 异步任务队列
- SSE 流式进度
- 自动下载 ComfyUI 输出到 `outputs/images` / `outputs/videos`

## 目录

- `main.go`: 网关服务
- `configs/workflows/*.yaml`: 每个工作流的参数配置

## 环境变量

- `WORKFLOW_CONFIG_DIR` (默认 `./configs/workflows`)
- `SERVICE_CONFIG_FILE` (默认 `./configs/service.yaml`)
- `COMFY_URL` (可选，单地址或作为主地址)
- `COMFY_URLS` (可选，多地址，逗号分隔；Go 网关会轮询负载均衡)
- `GO_WORKERS` (可选，默认等于 Comfy 后端数量)
- `LISTEN_ADDR` (默认 `:8090`)
- `OUTPUT_DIR` (默认 `./outputs`)

## 运行

```bash
cd /Volumes/kk/work/openclaw/chat_image

go mod tidy
go run .
```

多 Comfy 示例：

```bash
COMFY_URLS="http://127.0.0.1:8188,http://127.0.0.1:8189,http://127.0.0.1:8190" \
go run .
```

## API

### 黑盒类别接口（推荐）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/categories | jq
```

固定类别接口：

- `POST /api/v1/generate/t2i`（文生图）
- `POST /api/v1/generate/t2i-darkbeast6`（DarkBeast6 简易混合文生图）
- `POST /api/v1/generate/edit`（编辑图，对应锁定生图）
- `POST /api/v1/generate/t2v`（文生视频）
- `POST /api/v1/generate/i2v`（图生视频）

说明：`t2i` / `edit` / `t2v` / `i2v` 到底落到哪个具体 workflow，由
`configs/service.yaml` 的 `default_workflows` 决定。`t2i-darkbeast6`
固定使用 `t2i_darkbeast6_simple_hybrid`，不受默认映射影响。

### 创建任务示例（文生图）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/t2i \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "masterpiece, cinematic portrait, ultra detailed",
    "negative_prompt": "lowres, blurry",
    "width": 1024,
    "height": 1024,
    "params": {
      "steps_stage1": 5,
      "cfg_stage1": 1.5,
      "steps_stage2": 20,
      "cfg_stage2": 1.3
    }
  }' | jq
```

返回里有 `id`（任务 ID）。

### 创建任务示例（DarkBeast6 简易混合文生图）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/t2i-darkbeast6 \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "masterpiece, cinematic portrait, ultra detailed",
    "width": 768,
    "height": 1440,
    "params": {
      "seed": -1
    }
  }' | jq
```

默认参数：`steps_stage1=5`、`steps_stage2=15`、`cfg_stage1=1.0`、
`cfg_stage2=1.0`、`denoise_stage2=0.75`、`upscale_by=1.25`。

### 创建任务示例（图生视频）

```bash
curl -s http://127.0.0.1:8090/api/v1/generate/i2v \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "cinematic close-up, natural motion",
    "width": 832,
    "height": 480,
    "params": {
      "input_image_url": "https://example.com/first_frame.png",
      "duration_sec": 4,
      "fps": 8
    }
  }' | jq
```

`input_image` 支持三种形式：

- 服务器本地路径（例如 `/root/.../a.png`）
- 远程 URL（`input_image_url` / `input_image_end_url`）
- Base64（`input_image_base64` / `input_image_end_base64`，支持 data URL）

### 查询任务状态

```bash
curl -s http://127.0.0.1:8090/api/v1/tasks/<task_id> | jq
```

### SSE 流式进度

```bash
curl -N http://127.0.0.1:8090/api/v1/tasks/<task_id>/stream
```

### 内部原始接口（默认关闭）

- `GET /api/v1/workflows`
- `POST /api/v1/tasks`

是否开放由 `configs/service.yaml` 控制：

- `expose_workflow_catalog`
- `expose_raw_task_create`

## 输出文件

完成后任务响应里会有：

- `outputs[].url` 如 `/outputs/images/xxx.png` 或 `/outputs/videos/xxx.mp4`

可直接访问：

```bash
curl -I http://127.0.0.1:8090/outputs/images/<filename>
```

## YAML 配置说明

示例：

```yaml
id: t2i_normal
name: Z-Image Normal T2I
template: ../../workflows/t2i_zimage_darkbeast.json
template_format: api
mode: t2i
output_kind: image
timeout_sec: 1800
required: [prompt, width, height]
defaults:
  seed: -1
  width: 1024
  height: 1024
  steps_stage1: 5
```

字段说明：

- `template`: 工作流 JSON 模板路径（可相对 YAML 文件）
- `template_format`: `api` / `ui` / `auto`
  - `api`: 直接按 API workflow 提交
  - `ui`: 先调用 Comfy `/workflow/convert` 转换
  - `auto`: 自动判断（推荐）
- `required`: 任务必须参数
- `defaults`: 默认参数（可被请求里的 `params` 覆盖）
- `bindings`: 可选，把参数强制写入指定 node/input（用于无占位符模板）

当前仓库还提供了姿势优先配置：

- `configs/workflows/t2i_red_pose.yaml`（LSP LoRA，model+clip 双分支）
- `PROMPT_WORKFLOW_REFERENCE.md`（红潮 pose 链路提示词优化参考）

### service.yaml（类别默认工作流）

当前仓库默认配置（见 `configs/service.yaml`）：

```yaml
expose_workflow_catalog: false
expose_raw_task_create: false
default_workflows:
  text_to_image: t2i_red_pose
  image_edit: inpaint_zimage_lock_red_pose
  text_to_video: t2v_default
  image_to_video: i2v_wan_remix_single
```

如果你希望切回普通链路，可改成：

```yaml
default_workflows:
  text_to_image: t2i_normal
  image_edit: inpaint_zimage_lock
  text_to_video: t2v_default
  image_to_video: i2v_wan_remix_single
```

如果你希望切回 red 主链（非 pose）可改成：

```yaml
default_workflows:
  text_to_image: t2i_red_main
  image_edit: inpaint_zimage_lock_red
  text_to_video: t2v_default
  image_to_video: i2v_wan_remix_single
```

## 图生视频补充

`i2v_wan_remix_single` / `i2v_wan_remix_first_last` 已内置配置。

- `input_image` / `input_image_end` 可传本地路径（存在则自动上传到 ComfyUI）
- 也可直接传 ComfyUI input 目录里的文件名
- 传了 `width/height` 时，会自动推导 `size_long=max(width,height)`（除非你在 `params.size_long` 手动指定）
