# Docker Compose 部署

Compose 启动四个独立服务：

- `llama`：本地 Qwen，Compose 内网端口 `8000`
- `comfy-6006`：ComfyUI，宿主机端口 `6006`
- `comfy-6009`：ComfyUI，宿主机端口 `6009`
- `go-gateway`：Go API，宿主机端口 `6008`

所有服务都配置了 `restart: unless-stopped`。进程异常退出或 Docker daemon 重启后会自动拉起；GPU OOM、驱动故障和云平台停止实例不由 Compose 修复。

## 前提

服务器必须允许运行 Docker daemon，并已安装 NVIDIA Container Toolkit。先验证 GPU 容器：

```bash
docker run --rm --gpus all \
  nvidia/cuda:13.0.2-base-ubuntu24.04 nvidia-smi
```

如果这里失败，先处理服务器的 Docker/GPU 运行时；项目容器也无法使用 GPU。

## 目录要求

Compose 按当前服务器目录直接使用以下路径：

```text
chat_image/
├── isolated/comfy_6006/app
├── isolated/comfy_6006/runtime
├── isolated/comfy_6009/app
├── isolated/comfy_6009/runtime
├── models
├── hf_cache
└── workflows
```

两个 ComfyUI 镜像分别从各自的 `app` 构建，因此各自安装自己的自定义节点和 Python 依赖。`models`、`hf_cache` 和 `runtime` 通过挂载保留，不会写入镜像。

## 启动

在服务器项目根目录执行：

```bash
cp .env.example .env
openssl rand -hex 32
```

把生成值写入 `.env` 的 `GATEWAY_SECRET`，并填写 `S3_ACCESS_KEY`、`S3_SECRET_KEY`。然后启动：

第一次从 `scripts/start_multi_stack.sh` 切换到 Compose 时，先停止旧进程：

```bash
for pid_file in \
  .run/go_gateway.pid \
  .run/llama.pid \
  .run/comfy_6006.pid \
  .run/comfy_6009.pid; do
  [ -s "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true
done
rm -f .run/go_gateway.pid .run/llama.pid .run/comfy_6006.pid .run/comfy_6009.pid
sleep 5
```

不要同时运行旧启动脚本和 Compose。确认旧进程已停后启动容器：

```bash
docker compose up -d --build
docker compose ps
```

首次构建会安装两套 ComfyUI 依赖，耗时较长。后续普通重启不需要重新构建：

```bash
docker compose restart
```

拉取代码或修改 ComfyUI、自定义节点依赖后重新构建：

```bash
git pull
docker compose up -d --build
```

## 查看状态和日志

```bash
docker compose ps
docker compose logs -f --tail=200
docker compose logs -f --tail=200 comfy-6006
docker compose logs -f --tail=200 go-gateway
```

健康检查：

```bash
curl -fsS http://127.0.0.1:6006/system_stats
curl -fsS http://127.0.0.1:6009/system_stats
curl -fsS http://127.0.0.1:6008/healthz
```

停止并保留模型、缓存和运行数据：

```bash
docker compose down
```

不要加 `-v`；本项目当前使用宿主机挂载，但养成不删除卷的操作习惯更安全。

## GPU 说明

三个推理服务都使用同一张分配给当前实例的 GPU。容器内部通常把它显示为 GPU `0`，不要根据宿主机设备节点 `/dev/nvidia4` 设置 `CUDA_VISIBLE_DEVICES=4`。GPU 显存仍由三个服务共享，超出显存时仍会 OOM。
