# Go Gateway API

接口使用 OpenAI 兼容路由。

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

## OpenAI 兼容接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/v1/images/generations` | 文生图 |
| `POST` | `/v1/images/edits` | 图片编辑 |
| `POST` | `/v1/videos` | 文生视频；带 `input_reference` 时走图生视频 |
| `GET` | `/v1/videos/{id}` | 查询视频任务 |
| `GET` | `/v1/videos/{id}/content` | 读取已完成视频内容，返回 `307` 到输出 URL |
| `DELETE` | `/v1/videos/{id}` | 取消排队中的视频任务 |

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
    "model": "local-video",
    "prompt": "cinematic close-up, natural motion",
    "size": "832x480",
    "seconds": 4
  }'
```

图生视频：

```json
{
  "model": "local-video",
  "prompt": "cinematic close-up, natural motion",
  "size": "832x480",
  "seconds": 4,
  "input_reference": {
    "image_url": "https://example.com/first_frame.png"
  }
}
```

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

## 工作流映射

`configs/service.yaml` 的 `default_workflows` 决定 OpenAI 路由落到哪个内部 workflow：

```yaml
default_workflows:
  text_to_image: your_t2i_workflow
  image_edit: your_edit_workflow
  text_to_video: your_t2v_workflow
  image_to_video: your_i2v_workflow
```
