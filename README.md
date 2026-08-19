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
- `LORA_CONFIG_DIR` (默认 `./app/configs/loras`)
- `SERVICE_CONFIG_FILE` (默认 `./app/configs/service.yaml`)
- `COMFY_URL` (可选，单地址或作为主地址)
- `COMFY_URLS` (可选，多地址，逗号分隔；Go 网关会轮询负载均衡)
- `GO_WORKERS` (可选，默认等于 Comfy 后端数量)
- `LISTEN_ADDR` (默认 `:8090`)
- `OUTPUT_DIR` (默认 `./outputs`)
- `TEXT_BASE_URL` (可选，覆盖 `service.yaml` 里的文字模型服务地址)
- `TEXT_MODEL` (可选，覆盖默认文字模型名)
- `TEXT_API_KEY` (可选，转发给文字模型服务的 Bearer token)
- `GATEWAY_SECRET` (必填，HMAC 请求签名的共享密钥)
- `GATEWAY_BEARER_TOKEN` (可选，见下面「上游网关接入」)

## 上游网关接入（Bearer）

默认所有接口都要 HMAC 签名（`X-Gateway-Timestamp/Nonce/Signature`）。但 new-api
这类模型网关**没法按请求算签名** —— 它一个渠道只能配一个静态 token。

配上 `GATEWAY_BEARER_TOKEN` 之后，请求可以改用：

```
Authorization: Bearer <token>
```

跳过 HMAC。两种方式**同时生效**，原有签名客户端不受影响，所以启用它不是一次迁移。

代价说清楚：**bearer token 被抓到就能重放**，而 HMAC 不能（nonce 一次性 + ±300s 窗口）。
所以这条路只给「网关 → 本服务」的内网链路用，**公网可达的实例不要开**。

不开这个的替代方案是在前面加一个加签代理，那意味着多一个要部署、要监控、要跟着
升级的进程，只为把一种鉴权翻译成另一种 —— 不划算。

## 运行

```bash
cd /Volumes/kk/work/openclaw/chat_image

go mod tidy
go run .
```

新服务器需要先下载模型（约 154 GB，可断点续传，反复执行安全）：

```bash
bash scripts/download_models.sh          # 全部
bash scripts/download_models.sh --check  # 只看缺哪些
```

清单与来源见 [docs/MODELS.md](docs/MODELS.md)。

GPUHub 这类不能运行 Docker 的容器使用项目 Supervisor 守护：

```bash
apt-get update && apt-get install -y ffmpeg libavif-bin
./scripts/start_multi_stack.sh
./scripts/start_multi_stack.sh status
```

进程退出、HTTP 卡死重启和日志轮转见 [docs/PROCESS_SUPERVISION.md](docs/PROCESS_SUPERVISION.md)。支持 Docker 的服务器也可以按 [docs/DOCKER_COMPOSE.md](docs/DOCKER_COMPOSE.md) 部署。

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
