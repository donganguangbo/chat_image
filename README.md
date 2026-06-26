# Go Workflow Gateway

本仓库的唯一服务：一个 Go REST 网关，对接 ComfyUI，支持：

- 每个工作流一个 YAML 参数配置
- 用户只传 `prompt` + `width/height`（以及少量可选参数）
- 异步任务队列
- SSE 流式进度
- 输出存储必须在配置文件里显式选择 `local` 或 `s3`

## 目录

- `main.go`: 网关服务
- `app/configs/workflows/*.yaml`: 每个工作流的参数配置

## 环境变量

- `WORKFLOW_CONFIG_DIR` (默认 `./app/configs/workflows`)
- `SERVICE_CONFIG_FILE` (默认 `./app/configs/service.yaml`)
- `COMFY_URL` (可选，单地址或作为主地址)
- `COMFY_URLS` (可选，多地址，逗号分隔；Go 网关会轮询负载均衡)
- `GO_WORKERS` (可选，默认等于 Comfy 后端数量)
- `LISTEN_ADDR` (默认 `:8090`)
- `OUTPUT_DIR` (默认 `./outputs`)
- `TEXT_BASE_URL` (可选，覆盖 `service.yaml` 里的文字模型服务地址)
- `TEXT_MODEL` (可选，覆盖默认文字模型名)
- `TEXT_API_KEY` (可选，转发给文字模型服务的 Bearer token)

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

外部接口使用 v1 风格路由：

- `POST /v1/images/generations`（文生图）
- `POST /v1/images/edits`（图片编辑）
- `POST /v1/videos`（文生视频；带 `input_reference` 时走图生视频）
- `GET /v1/videos/{id}`（查询视频任务）
- `GET /v1/videos/{id}/content`（读取已完成视频内容）
- `DELETE /v1/videos/{id}`（取消排队中的视频任务）
- `POST /v1/chat/completions`（文字对话，转发到本地 Qwen 服务）

说明：这些路由到底落到哪个具体 workflow，由 `app/configs/service.yaml` 的
`default_workflows` 决定。旧接口迁移见 `docs/API_MIGRATION.md`。

### 创建任务示例（文生图）

```bash
curl -s http://127.0.0.1:8090/v1/images/generations \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-image",
    "prompt": "masterpiece, cinematic portrait, ultra detailed",
    "size": "1024x1024",
    "params": {
      "seed": -1
    }
  }' | jq
```

返回里有 `id`（内部任务 ID）和 `status`。

### 创建任务示例（图生视频）

```bash
curl -s http://127.0.0.1:8090/v1/videos \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-video",
    "prompt": "cinematic close-up, natural motion",
    "size": "832x480",
    "seconds": 4,
    "input_reference": {
      "image_url": "https://example.com/first_frame.png"
    }
  }' | jq
```

`input_reference` 支持 `image`、`image_url`、`image_base64`。

### 查询视频状态

```bash
curl -s http://127.0.0.1:8090/v1/videos/<video_id> | jq
```

### 文字对话示例

本地 Qwen 服务需要暴露 `POST /v1/chat/completions`。网关会把请求转发过去；请求里不传 `model` 时使用 `app/configs/service.yaml` 的 `text.model`。

```bash
curl -s http://127.0.0.1:8090/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "messages": [
      {"role": "user", "content": "写一句中文欢迎语"}
    ],
    "temperature": 0.7
  }' | jq
```

## 输出文件

视频完成后可通过 v1 内容接口读取：

```bash
curl -I http://127.0.0.1:8090/v1/videos/<video_id>/content
```

`storage_mode: s3` 时，生成文件必须上传到 S3/Linode，返回签名 URL；上传或签名失败则任务失败。

`storage_mode: local` 时，底层输出文件由网关保存在：

- `/outputs/images/<filename>`
- `/outputs/videos/<filename>`

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

### service.yaml（类别默认工作流）

当前仓库默认配置（见 `app/configs/service.yaml`）：

```yaml
expose_workflow_catalog: false
expose_raw_task_create: false
storage_mode: s3
default_workflows: {}
```

挂载外部 workflow 配置后，按需填写：

```yaml
default_workflows:
  text_to_image: your_t2i_workflow
  image_edit: your_edit_workflow
  text_to_video: your_t2v_workflow
  image_to_video: your_i2v_workflow

text:
  base_url: http://127.0.0.1:8000
  model: qwen-local
```
