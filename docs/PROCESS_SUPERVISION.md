# 进程守护与自动重启

`scripts/start_multi_stack.sh` 使用项目私有的 Supervisor 实例管理以下进程：

- `llama`: 本地 `llama-server`
- `comfy_<port>`: `COMFY_PORTS` 中的每个 ComfyUI 实例
- `go_gateway`: 编译后的 Go Gateway
- `health_monitor`: HTTP 健康监控
- `memory_guard`: cgroup 内存软限制与 ComfyUI 空闲模型缓存回收

Supervisor 在进程退出后自动拉起服务。健康监控每 10 秒检查一次接口，连续 6 次失败后重启对应服务；启动期使用独立宽限时间，避免模型加载期间被误杀。

## 安装

服务器需要 Supervisor。GPUHub 当前镜像通常已安装，可先检查：

```bash
supervisord --version
supervisorctl --version
```

缺少时安装：

```bash
python3 -m pip install supervisor
```

## 操作

默认启动一个 ComfyUI，端口为 `6006`：

```bash
./scripts/start_multi_stack.sh
```

启动多个 ComfyUI：

```bash
COMFY_PORTS="6006 6009" ./scripts/start_multi_stack.sh
```

查看状态：

```bash
./scripts/start_multi_stack.sh status
```

重启单个服务或整个服务栈：

```bash
./scripts/start_multi_stack.sh restart comfy_6006
./scripts/start_multi_stack.sh restart go_gateway
./scripts/start_multi_stack.sh restart llama
./scripts/start_multi_stack.sh restart
```

停止全部服务以及项目 Supervisor：

```bash
./scripts/start_multi_stack.sh stop
```

## 日志

日志保存在 `logs/`：

- `supervisord.log`: 进程退出、拉起、重试和状态变化
- `health_monitor.log`: 健康失败及主动重启原因
- `memory_guard.log`: `memory.high` 设置与模型缓存回收记录
- `llama.log`
- `comfy_6006.log`（多实例时按端口生成）
- `go_gateway.log`

默认每个服务日志达到 50 MB 后轮转，保留 5 个备份。可在启动时覆盖：

```bash
LOG_MAX_BYTES=100MB LOG_BACKUPS=10 ./scripts/start_multi_stack.sh
```

实时查看：

```bash
tail -F logs/supervisord.log logs/health_monitor.log logs/memory_guard.log logs/comfy_6006.log logs/go_gateway.log
```

## 健康检查参数

可通过环境变量调整：

| 变量 | 默认值 | 说明 |
| --- | ---: | --- |
| `HEALTH_CHECK_INTERVAL` | `10` | 检查间隔，单位秒 |
| `HEALTH_FAILURE_THRESHOLD` | `6` | 连续失败多少次后重启 |
| `HEALTH_CHECK_TIMEOUT` | `5` | 单次 HTTP 超时，单位秒 |
| `HEALTH_RETRY_INTERVAL` | `30` | FATAL 服务重新尝试间隔，单位秒 |
| `LLAMA_WAIT_SEC` | `600` | llama 启动宽限时间 |
| `COMFY_WAIT_SEC` | `120` | ComfyUI 启动宽限时间 |
| `GO_WAIT_SEC` | `60` | Go Gateway 启动宽限时间 |

本地文字模型默认保留 thinking，并把单次推理预算限制为 `4096 token`。预算耗尽时 llama.cpp 会结束推理并继续生成最终答案；普通请求不需要增加额外字段。可通过以下变量调整：

| 变量 | 默认值 | 说明 |
| --- | ---: | --- |
| `LLAMA_REASONING_MODE` | `on` | llama.cpp reasoning 模式 |
| `LLAMA_REASONING_BUDGET_TOKENS` | `4096` | 单次 thinking token 上限；`-1` 表示不限制 |
| `LLAMA_REASONING_FORMAT` | `deepseek` | 将推理放在 `reasoning_content`，最终答案放在 `content` |
| `LLAMA_REASONING_BUDGET_MESSAGE` | 见 `.env.example` | 预算耗尽时注入的最终答案指令 |

## 内存保护

默认在启动时尝试把当前 cgroup 的 `memory.high` 设置为 `108 GiB`。如果平台不允许写入，服务仍会启动，并在 `memory_guard.log` 记录警告。已有的更低软限制不会被提高。

当 cgroup 内存达到 `105 GiB` 且所有 ComfyUI 的运行、等待队列均为空时，`memory_guard` 会：

1. 调用每个 ComfyUI 的 `/free` 卸载空闲模型。
2. 对 `models/` 中大于 `64 MiB` 的文件发送 `POSIX_FADV_DONTNEED`，释放文件页缓存。
3. 跳过 `models/llama/`，避免文字模型被迫重新读取。

默认回收冷却时间为 5 分钟。可通过环境变量调整：

| 变量 | 默认值 | 说明 |
| --- | ---: | --- |
| `MEMORY_GUARD_ENABLED` | `1` | 设置为 `0` 时关闭内存保护 |
| `CGROUP_DIR` | `/sys/fs/cgroup` | 当前容器的 cgroup v2 目录 |
| `CGROUP_MEMORY_HIGH_GIB` | `108` | `memory.high` 软限制，单位 GiB |
| `MEMORY_RECLAIM_THRESHOLD_GIB` | `105` | 触发空闲回收的内存阈值，单位 GiB |
| `MEMORY_RECLAIM_INTERVAL` | `10` | 内存检查间隔，单位秒 |
| `MEMORY_RECLAIM_COOLDOWN` | `300` | 两次回收之间的最短间隔，单位秒 |
| `MEMORY_RECLAIM_MIN_FILE_MIB` | `64` | 参与页缓存回收的最小文件大小，单位 MiB |

项目 Supervisor 在 SSH 断开后继续运行，并负责服务进程退出或卡死后的恢复。整个 GPUHub 容器被重建或关机后，仍需执行一次启动脚本，或在 GPUHub 控制台把该脚本配置为实例启动命令。
