# Go Gateway API

接口使用 v1 风格路由。

## Base URL

```text
http://127.0.0.1:8090
```

健康检查无需签名：

```bash
curl -s http://127.0.0.1:8090/healthz
```

除 `OPTIONS` 和 `/healthz` 外，请求需要 HMAC 头：

- `X-Gateway-Timestamp`
- `X-Gateway-Nonce`
- `X-Gateway-Signature`

## v1 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/v1/images/generations` | 文生图 |
| `POST` | `/v1/images/edits` | 图片编辑 |
| `POST` | `/v1/videos` | 文生视频；带 `source_image` 时走图生视频 |
| `GET` | `/v1/videos/{id}` | 查询视频任务 |
| `GET` | `/v1/videos/{id}/content` | 读取已完成视频内容，返回 `307` 到输出 URL |
| `DELETE` | `/v1/videos/{id}` | 取消排队中的视频任务 |
| `POST` | `/v1/chat/completions` | 文字对话，转发到本地 Qwen 服务 |

### 文生图

```bash
curl -s http://127.0.0.1:8090/v1/images/generations \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-image",
    "prompt": "masterpiece, cinematic portrait",
    "size": "1024x1024",
    "params": {
      "seed": -1
    }
  }'
```

### 图片编辑

```bash
curl -s http://127.0.0.1:8090/v1/images/edits \
  -F 'model=local-image-edit' \
  -F 'prompt=change background to neon city' \
  -F 'size=1024x1024' \
  -F 'image=@input.png'
```

也支持 JSON：

```json
{
  "prompt": "change background to neon city",
  "size": "1024x1024",
  "image_url": "https://example.com/input.png",
  "params": {}
}
```

### 视频

```bash
curl -s http://127.0.0.1:8090/v1/videos \
  -H 'Content-Type: application/json' \
  -d '{
    "motion_prompt": "cinematic close-up, natural motion",
    "motion_negative_prompt": "camera movement, shot change",
    "action_id": "cumshot",
    "size": "832x480",
    "seconds": 4
  }'
```

图生视频：

```json
{
  "motion_prompt": "cinematic close-up, natural motion",
  "motion_negative_prompt": "camera movement, shot change",
  "first_frame_prompt": "preserve identity and prepare the first frame",
  "first_frame_negative_prompt": "bad anatomy, fused bodies",
  "action_id": "cumshot",
  "size": "832x480",
  "seconds": 4,
  "source_image": {
    "image_url": "https://example.com/first_frame.png"
  }
}
```

`action_id` 是可选的 kebab-case 动作 ID。只有配置在
`app/configs/loras/*.yaml` 中的动作会加载动态 LoRA；字段缺失、空字符串或
未映射时都继续使用工作流原有的固定 LoRA，不返回错误。

响应是异步任务对象：

```json
{
  "id": "186a3f...",
  "object": "video",
  "created": 1782470000,
  "status": "queued",
  "model": "local-video"
}
```

### 文字对话

```bash
curl -s http://127.0.0.1:8090/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "messages": [
      {"role": "user", "content": "写一句中文欢迎语"}
    ],
    "temperature": 0.7
  }'
```

请求会转发到 `service.yaml` 的 `text.base_url`，路径固定为 `/v1/chat/completions`。请求未传 `model` 时使用 `text.model`。

## 工作流映射

`app/configs/service.yaml` 的 `default_workflows` 决定 V1 路由落到哪个内部 workflow：

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
