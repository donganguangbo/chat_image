# 镜像构建与新机部署

三个镜像，模型与运行时数据全部走挂载，不进镜像层。

| 镜像 | 来源 | 大小量级 | 说明 |
|---|---|---|---|
| **go-gateway** | `docker/gateway/Dockerfile` | 几十 MB | 多阶段构建，静态编译 + ffmpeg |
| **comfyui** | `docker/comfy/Dockerfile` | 数 GB（含 CUDA torch） | ComfyUI 固定 `v0.26.0` + 固化的自定义节点快照；两个实例（6006/6009）共用同一镜像 |
| **llama.cpp** | 上游 `ghcr.io/ggml-org/llama.cpp:server-cuda` | — | **零构建**，直接 pull，直接吃 `models/llama/*.gguf` |

挂载（不进镜像）：`models/` 154 GB · `hf_cache/` · `isolated/comfy_*/runtime/` · `outputs/`

## 新机器部署三步

```bash
bash scripts/bootstrap_deploy.sh     # 建挂载点、生成 .env（含随机 GATEWAY_SECRET）
bash scripts/download_models.sh      # 下模型 ~154 GB，可断点续传
docker compose build && docker compose up -d
```

模型清单见 [MODELS.md](MODELS.md)，Compose 细节见 [DOCKER_COMPOSE.md](DOCKER_COMPOSE.md)。

## 自定义节点：构建时 clone，但 pin 到生产实际运行的 commit

40 个公开节点在构建时 `git clone`，每个都钉死在**生产服务器实际运行的那个 commit**，
清单见 [`docker/comfy/node_pins.txt`](../docker/comfy/node_pins.txt)。仓库因此不需要携带节点源码。

**为什么不能简单 clone 最新版**：生产装的并不是上游最新。例如 `RES4LYF` 生产版
`__init__.py` 的 git blob 是 `4693a51a…`，对应 commit `419de2d7`（2026-06-15），
而上游 `v1.1.0` 与 `main` 的同名文件都是 `5dbdb531…`。它提供的 `ClownsharKSampler_Beta`
是全部 4 个工作流都在用的采样器，clone 最新版会静默改变出图行为。

**commit 是怎么找到的**：节点的 `.git` 目录在服务器上已被剥离，`pyproject.toml` 里的
`version` 字段也不足以定位 commit。做法是取每个节点 `__init__.py` 的 git blob SHA，
再到上游历史里找 blob 相同的提交：

```bash
git clone --bare --filter=blob:none --no-checkout https://github.com/OWNER/REPO.git
git log --all --format=%H -- __init__.py        # 改动过该文件的提交
git rev-parse <commit>:__init__.py              # 取该提交下的 blob，逐个比对
```

`--filter=blob:none` 下 tree 对象仍然完整，所以不下载文件内容也能拿到 blob SHA ——
这样 40 个仓库总共只需约 18 MB 传输，且完全不碰 GitHub API（匿名限流 60 次/小时根本跑不完）。
40 个节点全部精确命中，无一需要人工兜底。

**核对 pin**（不必完整构建）：

```bash
bash scripts/verify_node_pins.sh            # 仓库/commit 是否还在（快）
bash scripts/verify_node_pins.sh --blobs    # 逐个比对内容是否仍等于生产
```

参照基准是 [`docker/comfy/node_blobs.txt`](../docker/comfy/node_blobs.txt)（生产 blob SHA 列表）。

**唯一的例外**：`prompt_grid_16_pro` 在互联网上查不到任何上游（私有节点），
以源码形式放在 `docker/comfy/vendor_nodes/`，仅 224 KB / 8 个文件。

节点自带的权重（`comfyui_controlnet_aux/ckpts` 等约 1.6 GB）不进镜像，首次使用时下载到挂载的 `runtime/`。

## Python 依赖为什么锁死

`docker/comfy/requirements.lock.txt` 是生产 venv 的 `pip freeze`（302 个包），
即"已知能与全部节点协同工作"的组合。让 pip 现场解析 24 个节点各自的
`requirements.txt` 会因上游更新而装出不同版本组合。

生产环境实测：**Python 3.12.13 / torch 2.12.1+cu130 / CUDA 13.0**，
与基础镜像 `nvidia/cuda:13.0.2-cudnn-devel-ubuntu24.04`（自带 Python 3.12）一致。

⚠️ **torch 必须从 CUDA 13 wheel 源装**：lock 文件里是 `torch==2.12.1`（无 `+cu130` 后缀），
直接走 PyPI 会解析成 CPU 版。Dockerfile 已先用
`--index-url https://download.pytorch.org/whl/cu130` 安装 torch 三件套，再用
`--no-deps` 装其余依赖，避免被回退覆盖。

## ⚠️ 在 Apple Silicon 上构建要指定平台

目标服务器是 **x86_64**，而 CUDA 版 torch 只有 amd64 wheel。在 arm64 Mac 上必须交叉构建：

```bash
docker buildx build --platform linux/amd64 \
  -f docker/comfy/Dockerfile -t chat-image-comfy:local .
```

QEMU 模拟下装 302 个包（含 torch）会很慢，预计 1 小时以上。若要频繁构建，
更实际的做法是在一台 amd64 机器（或新服务器本身）上 `docker compose build`。

`go-gateway` 镜像是纯 Go 静态编译，交叉构建很快，无此问题。

## 推镜像仓库（可选）

不想在新机现场构建，可以推到仓库后 `pull`：

```bash
docker tag chat-image-comfy:local <registry>/chat-image-comfy:<tag>
docker push <registry>/chat-image-comfy:<tag>
```

新机在 `.env` 里设 `COMFY_IMAGE=<registry>/chat-image-comfy:<tag>`，
`docker compose up -d` 即直接拉取，无需 build。
