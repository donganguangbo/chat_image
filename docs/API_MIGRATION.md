# API Migration: `/api/v1` to OpenAI-compatible `/v1`

旧的 `/api/v1/generate/*` 外部生成接口已移除。新客户端应改用 OpenAI 兼容路由。

## 路由对照

| 旧接口 | 新接口 | 说明 |
|---|---|---|
| `GET /api/v1/generate/categories` | 无 | 不再暴露类别发现；客户端直接调用 OpenAI 兼容端点 |
| `POST /api/v1/generate/t2i` | `POST /v1/images/generations` | 文生图 |
| `POST /api/v1/generate/edit` | `POST /v1/images/edits` | 图片编辑；推荐 `multipart/form-data` |
| `POST /api/v1/generate/t2v` | `POST /v1/videos` | 文生视频 |
| `POST /api/v1/generate/i2v` | `POST /v1/videos` | 图生视频；传 `input_reference` |
| `GET /api/v1/tasks/{id}` | `GET /v1/videos/{id}` | 视频任务查询；图片仍返回创建时的异步任务 ID |
| `POST /api/v1/tasks/{id}/cancel` | `DELETE /v1/videos/{id}` | 取消排队中的视频任务 |
| 输出 URL | `GET /v1/videos/{id}/content` | 已完成视频返回 `307` 跳转到输出 URL |

`/api/v1/tasks*` 和 `/api/v1/workflows` 不再注册 HTTP 路由。

## 请求字段迁移

### 文生图

旧：

```json
{
  "prompt": "masterpiece",
  "width": 1024,
  "height": 1024,
  "params": {
    "seed": -1
  }
}
```

新：

```json
{
  "model": "local-image",
  "prompt": "masterpiece",
  "size": "1024x1024",
  "params": {
    "seed": -1
  }
}
```

### 图片编辑

旧 JSON：

```json
{
  "prompt": "change background",
  "input_image_url": "https://example.com/input.png",
  "width": 1024,
  "height": 1024
}
```

新 multipart：

```bash
curl -s http://127.0.0.1:8090/v1/images/edits \
  -F 'model=local-image-edit' \
  -F 'prompt=change background' \
  -F 'size=1024x1024' \
  -F 'image=@input.png'
```

新 JSON 也可用：

```json
{
  "prompt": "change background",
  "size": "1024x1024",
  "image_url": "https://example.com/input.png"
}
```

### 文生视频

旧：

```json
{
  "prompt": "cinematic motion",
  "width": 832,
  "height": 480,
  "params": {
    "duration_sec": 4
  }
}
```

新：

```json
{
  "model": "local-video",
  "prompt": "cinematic motion",
  "size": "832x480",
  "seconds": 4
}
```

### 图生视频

旧：

```json
{
  "prompt": "cinematic motion",
  "width": 832,
  "height": 480,
  "params": {
    "input_image_url": "https://example.com/frame.png",
    "duration_sec": 4
  }
}
```

新：

```json
{
  "model": "local-video",
  "prompt": "cinematic motion",
  "size": "832x480",
  "seconds": 4,
  "input_reference": {
    "image_url": "https://example.com/frame.png"
  }
}
```

## 响应变化

旧接口返回内部 `Task`：

```json
{
  "id": "task-id",
  "workflow_id": "wf",
  "status": "queued",
  "progress": 0
}
```

新接口返回 OpenAI 兼容异步对象：

```json
{
  "id": "task-id",
  "object": "video",
  "created": 1782470000,
  "status": "queued",
  "model": "local-video"
}
```

状态映射：

| 旧状态 | 新状态 |
|---|---|
| `queued` | `queued` |
| `running` | `in_progress` |
| `completed` | `completed` |
| `failed` | `failed` |
| `cancelled` | `cancelled` |

## 配置不变

`app/configs/service.yaml` 仍控制 OpenAI 路由落到哪个内部 workflow：

```yaml
default_workflows:
  text_to_image: your_t2i_workflow
  image_edit: your_edit_workflow
  text_to_video: your_t2v_workflow
  image_to_video: your_i2v_workflow
```
