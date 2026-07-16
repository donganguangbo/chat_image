# 进程守护与自动重启

`scripts/start_multi_stack.sh` 使用项目私有的 Supervisor 实例管理以下进程：

- `llama`: 本地 `llama-server`
- `comfy_<port>`: `COMFY_PORTS` 中的每个 ComfyUI 实例
- `go_gateway`: 编译后的 Go Gateway
- `health_monitor`: HTTP 健康监控

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
- `llama.log`
- `comfy_6006.log`（多实例时按端口生成）
- `go_gateway.log`

默认每个服务日志达到 50 MB 后轮转，保留 5 个备份。可在启动时覆盖：

```bash
LOG_MAX_BYTES=100MB LOG_BACKUPS=10 ./scripts/start_multi_stack.sh
```

实时查看：

```bash
tail -F logs/supervisord.log logs/health_monitor.log logs/comfy_6006.log logs/go_gateway.log
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

项目 Supervisor 在 SSH 断开后继续运行，并负责服务进程退出或卡死后的恢复。整个 GPUHub 容器被重建或关机后，仍需执行一次启动脚本，或在 GPUHub 控制台把该脚本配置为实例启动命令。
