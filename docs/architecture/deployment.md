# Deployment Architecture

The current deployment runs one Go Gateway, one local llama server, and one
isolated ComfyUI instance on a single GPUHub GPU container. Supervisor manages
all long-running processes.

```mermaid
flowchart LR
    Client["Upstream chat service / Web client"]

    subgraph GPUHub["GPUHub GPU container"]
        Start["scripts/start_multi_stack.sh"]

        subgraph Supervisor["Project Supervisor"]
            Gateway["Go Gateway<br/>Port 6008"]
            Llama["llama-server<br/>Qwen3.6 35B<br/>Port 8000"]
            Comfy["Isolated ComfyUI<br/>Port 6006"]
            Health["health_monitor"]
            Memory["memory_guard"]
        end

        Config["app/configs<br/>workflows"]
        Models["models<br/>LLM / Checkpoint / LoRA / VAE"]
        Runtime["isolated/comfy_6006/runtime<br/>input / output / temp"]
        Logs["logs<br/>Service and rotation logs"]
        CGroup["Container cgroup<br/>memory.high = 108 GiB"]
    end

    S3["Linode Object Storage<br/>S3"]
    Result["Signed file URL"]

    Client -->|"HMAC-signed requests<br/>Text / Image / Video"| Gateway

    Gateway -->|"/v1/chat/completions"| Llama
    Gateway -->|"/prompt / history / view<br/>upload image"| Comfy

    Config --> Gateway
    Config --> Comfy
    Models --> Llama
    Models --> Comfy
    Comfy <--> Runtime

    Gateway -->|"Upload final image or video"| S3
    S3 --> Result
    Result --> Client

    Start -->|"Render config and start"| Supervisor
    Supervisor --> Gateway
    Supervisor --> Llama
    Supervisor --> Comfy
    Supervisor --> Health
    Supervisor --> Memory

    Health -->|"Health check and restart"| Gateway
    Health -->|"Health check and restart"| Llama
    Health -->|"Health check and restart"| Comfy

    Memory -->|"Call /free while idle"| Comfy
    Memory -->|"Set soft memory limit"| CGroup
    Memory -->|"Release model page cache"| Models

    Gateway --> Logs
    Llama --> Logs
    Comfy --> Logs
    Health --> Logs
    Memory --> Logs
```

The Go Gateway is the single API entry point. Text requests are proxied to the
local llama server, while image and video tasks are submitted to ComfyUI. Final
media must be uploaded to Linode Object Storage, and callers receive signed
URLs. Supervisor restarts exited processes, while `health_monitor` handles
unhealthy processes and `memory_guard` controls idle model memory usage.
