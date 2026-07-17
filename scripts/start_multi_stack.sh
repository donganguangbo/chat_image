#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

GO_PORT="${GO_PORT:-6008}"
LLAMA_BIN="${LLAMA_BIN:-/opt/llama.cpp/build/bin/llama-server}"
LLAMA_HOST="${LLAMA_HOST:-127.0.0.1}"
LLAMA_PORT="${LLAMA_PORT:-8000}"
LLAMA_MODEL="${LLAMA_MODEL:-$PROJECT_ROOT/models/llama/Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-Q4_K_M.gguf}"
LLAMA_MMPROJ="${LLAMA_MMPROJ:-$PROJECT_ROOT/models/llama/mmproj-Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-f16.gguf}"
LLAMA_CTX_SIZE="${LLAMA_CTX_SIZE:-16384}"
LLAMA_NGL="${LLAMA_NGL:-999}"
LLAMA_NO_CACHE_PROMPT="${LLAMA_NO_CACHE_PROMPT:-1}"
LLAMA_REASONING_MODE="${LLAMA_REASONING_MODE:-on}"
LLAMA_REASONING_BUDGET_TOKENS="${LLAMA_REASONING_BUDGET_TOKENS:-4096}"
LLAMA_REASONING_FORMAT="${LLAMA_REASONING_FORMAT:-deepseek}"
LLAMA_REASONING_BUDGET_MESSAGE="${LLAMA_REASONING_BUDGET_MESSAGE:-Reasoning budget reached. Stop analysis and output the final answer now, strictly following the requested format.}"
# DRY sampler: suppresses the degenerate repetition loops that make the reasoning
# model occasionally run away (generating to the context ceiling → truncated →
# empty answer). Only penalizes long verbatim repeats, so normal thinking is
# untouched. Set LLAMA_DRY_MULTIPLIER=0 to disable.
LLAMA_DRY_MULTIPLIER="${LLAMA_DRY_MULTIPLIER:-0.8}"
LLAMA_DRY_BASE="${LLAMA_DRY_BASE:-1.75}"
LLAMA_DRY_ALLOWED_LENGTH="${LLAMA_DRY_ALLOWED_LENGTH:-2}"
COMFY_HOST="${COMFY_HOST:-0.0.0.0}"
COMFY_PORTS="${COMFY_PORTS:-6006}"

# Comfy template source (read-only source tree)
COMFY_ROOT="${COMFY_ROOT:-$PROJECT_ROOT/ComfyUI}"
COMFY_LAUNCHER="$PROJECT_ROOT/scripts/comfy_cgroup_launcher.py"
PYTHON_BIN="${PYTHON_BIN:-$PROJECT_ROOT/venv/bin/python}"
GO_BIN="${GO_BIN:-go}"

# Isolation mode:
# 1 = each comfy port runs in its own isolated app+venv+runtime tree (recommended)
# 0 = legacy shared Comfy root mode
COMFY_ISOLATED="${COMFY_ISOLATED:-1}"
ISOLATED_ROOT="${ISOLATED_ROOT:-$PROJECT_ROOT/isolated}"
ISOLATED_VENV_MODE="${ISOLATED_VENV_MODE:-shared}" # shared|copy|create
COMFY_SYNC_APP="${COMFY_SYNC_APP:-0}"            # 1 => rsync template app into each isolated app on startup
COMFY_SYNC_VENV="${COMFY_SYNC_VENV:-0}"          # 1 => rebuild isolated venvs on startup

CONFIGURE_COMFY_PATHS="${CONFIGURE_COMFY_PATHS:-1}"
LLAMA_WAIT_SEC="${LLAMA_WAIT_SEC:-600}"
COMFY_WAIT_SEC="${COMFY_WAIT_SEC:-120}"
GO_WAIT_SEC="${GO_WAIT_SEC:-60}"

LOG_DIR="${LOG_DIR:-$PROJECT_ROOT/logs}"
RUN_DIR="${RUN_DIR:-$PROJECT_ROOT/.run}"
SUPERVISORD_BIN="${SUPERVISORD_BIN:-supervisord}"
SUPERVISORCTL_BIN="${SUPERVISORCTL_BIN:-supervisorctl}"
SUPERVISOR_PYTHON="${SUPERVISOR_PYTHON:-python3}"
SUPERVISOR_CONFIG="$RUN_DIR/supervisord.conf"
SUPERVISOR_SOCKET="$RUN_DIR/supervisor.sock"
SUPERVISOR_PID_FILE="$RUN_DIR/supervisord.pid"
HEALTH_CHECK_INTERVAL="${HEALTH_CHECK_INTERVAL:-10}"
HEALTH_FAILURE_THRESHOLD="${HEALTH_FAILURE_THRESHOLD:-6}"
HEALTH_CHECK_TIMEOUT="${HEALTH_CHECK_TIMEOUT:-5}"
HEALTH_RETRY_INTERVAL="${HEALTH_RETRY_INTERVAL:-30}"
MEMORY_GUARD_ENABLED="${MEMORY_GUARD_ENABLED:-1}"
CGROUP_DIR="${CGROUP_DIR:-/sys/fs/cgroup}"
CGROUP_MEMORY_HIGH_GIB="${CGROUP_MEMORY_HIGH_GIB:-108}"
MEMORY_RECLAIM_THRESHOLD_GIB="${MEMORY_RECLAIM_THRESHOLD_GIB:-105}"
MEMORY_RECLAIM_INTERVAL="${MEMORY_RECLAIM_INTERVAL:-10}"
MEMORY_RECLAIM_COOLDOWN="${MEMORY_RECLAIM_COOLDOWN:-300}"
MEMORY_RECLAIM_MIN_FILE_MIB="${MEMORY_RECLAIM_MIN_FILE_MIB:-64}"
LOG_MAX_BYTES="${LOG_MAX_BYTES:-50MB}"
LOG_BACKUPS="${LOG_BACKUPS:-5}"
mkdir -p "$LOG_DIR" "$RUN_DIR"

die() {
  echo "[error] $*" >&2
  exit 1
}

supervisor_is_running() {
  [[ -S "$SUPERVISOR_SOCKET" && -f "$SUPERVISOR_CONFIG" ]] || return 1
  "$SUPERVISORCTL_BIN" -c "$SUPERVISOR_CONFIG" pid >/dev/null 2>&1
}

stop_project_supervisor() {
  local pid=""
  [[ -f "$SUPERVISOR_PID_FILE" ]] && pid="$(cat "$SUPERVISOR_PID_FILE" 2>/dev/null || true)"
  if supervisor_is_running; then
    "$SUPERVISORCTL_BIN" -c "$SUPERVISOR_CONFIG" shutdown >/dev/null 2>&1 || true
  fi

  for _ in {1..30}; do
    [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null || break
    sleep 1
  done
  if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -f "$SUPERVISOR_SOCKET" "$SUPERVISOR_PID_FILE"
}

usage() {
  cat <<'EOF'
Usage:
  scripts/start_multi_stack.sh                  Start or replace the supervised stack
  scripts/start_multi_stack.sh status           Show managed process status
  scripts/start_multi_stack.sh stop             Stop the full stack and supervisor
  scripts/start_multi_stack.sh restart           Rebuild and restart the full stack
  scripts/start_multi_stack.sh restart SERVICE   Restart one managed service
EOF
}

action="${1:-start}"
case "$action" in
  status)
    if supervisor_is_running; then
      exec "$SUPERVISORCTL_BIN" -c "$SUPERVISOR_CONFIG" status
    fi
    echo "[stopped] project supervisor is not running"
    exit 1
    ;;
  stop)
    stop_project_supervisor
    echo "[ok] supervised stack stopped"
    exit 0
    ;;
  restart)
    if [[ -n "${2:-}" ]]; then
      supervisor_is_running || die "project supervisor is not running"
      exec "$SUPERVISORCTL_BIN" -c "$SUPERVISOR_CONFIG" restart "$2"
    fi
    ;;
  start)
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

wait_http_ok() {
  local url="$1"
  local timeout_sec="$2"
  local name="$3"
  local start now elapsed
  start="$(date +%s)"
  while true; do
    if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      echo "[ok] $name ready: $url"
      return 0
    fi
    now="$(date +%s)"
    elapsed=$((now - start))
    if (( elapsed >= timeout_sec )); then
      echo "[error] $name not ready in ${timeout_sec}s: $url"
      return 1
    fi
    sleep 1
  done
}

avif_encoder_available() {
  command -v avifenc >/dev/null 2>&1 && return 0
  ffmpeg -hide_banner -muxers 2>/dev/null | awk '$2 == "avif" { found=1 } END { exit !found }'
}

kill_by_pattern() {
  local pattern="$1"
  mapfile -t pids < <(pgrep -f "$pattern" 2>/dev/null || true)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    kill "${pids[@]}" 2>/dev/null || true
    sleep 1
    mapfile -t pids < <(pgrep -f "$pattern" 2>/dev/null || true)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill -9 "${pids[@]}" 2>/dev/null || true
    fi
  fi
}

kill_by_pid_file() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  local pid
  pid="$(cat "$file" 2>/dev/null || true)"
  [[ "$pid" =~ ^[0-9]+$ ]] || {
    rm -f "$file"
    return 0
  }
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    sleep 1
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null || true
    fi
  fi
  rm -f "$file"
}

list_port_pids() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true
  fi
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltnp "sport = :$port" 2>/dev/null | sed -nE 's/.*pid=([0-9]+).*/\1/p' || true
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$port" <<'PY' 2>/dev/null || true
import os, sys

port = int(sys.argv[1])
inodes = set()
for table in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        lines = open(table).read().splitlines()[1:]
    except OSError:
        continue
    for line in lines:
        parts = line.split()
        local = parts[1]
        state = parts[3]
        inode = parts[9]
        if state != "0A":
            continue
        if int(local.rsplit(":", 1)[1], 16) == port:
            inodes.add(inode)

seen = set()
for name in os.listdir("/proc"):
    if not name.isdigit():
        continue
    fd_dir = f"/proc/{name}/fd"
    try:
        fds = os.listdir(fd_dir)
    except OSError:
        continue
    for fd in fds:
        try:
            target = os.readlink(f"{fd_dir}/{fd}")
        except OSError:
            continue
        if target.startswith("socket:[") and target[8:-1] in inodes and name not in seen:
            seen.add(name)
            print(name)
PY
  fi
}

kill_by_port() {
  local port="$1"
  local -a pids
  mapfile -t pids < <(list_port_pids "$port" | sort -u)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    kill "${pids[@]}" 2>/dev/null || true
    sleep 1
    mapfile -t pids < <(list_port_pids "$port" | sort -u)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill -9 "${pids[@]}" 2>/dev/null || true
    fi
  elif command -v fuser >/dev/null 2>&1; then
    fuser -k -TERM -n tcp "$port" >/dev/null 2>&1 || true
    sleep 1
    fuser -k -KILL -n tcp "$port" >/dev/null 2>&1 || true
  fi
}

sync_tree() {
  local src="$1"
  local dst="$2"
  mkdir -p "$dst"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete \
      --exclude ".git" \
      --exclude "__pycache__" \
      --exclude ".run" \
      --exclude "logs" \
      --exclude "/models" \
      --exclude "/input" \
      --exclude "/output" \
      --exclude "/temp" \
      --exclude "/user" \
      "$src/" "$dst/"
    return 0
  fi
  rm -rf "$dst"
  mkdir -p "$dst"
  (cd "$src" && tar cf - .) | (cd "$dst" && tar xf -)
}

configure_comfy_paths() {
  local models_root="$1"
  local comfy_root="$2"
  local config_name="${COMFY_EXTRA_CONFIG_NAME:-chat_image_models}"

  mkdir -p "$models_root" "$comfy_root"
  models_root="$(cd "$models_root" && pwd)"
  comfy_root="$(cd "$comfy_root" && pwd)"

  local target="$comfy_root/extra_model_paths.yaml"
  local start="# >>> ${config_name} (managed by chat_image) >>>"
  local end="# <<< ${config_name} (managed by chat_image) <<<"
  local q="${models_root//\'/\'\'}"
  local current cleaned

  if [[ -f "$target" ]]; then
    current="$(cat "$target")"
  else
    current="# Auto-generated by chat_image scripts"
  fi

  cleaned="$(printf '%s\n' "$current" \
    | awk -v s="$start" -v e="$end" '$0==s{skip=1} skip==0{print} $0==e{skip=0}' \
    | awk 'NF{nb=NR} {a[NR]=$0} END{for(i=1;i<=nb;i++)print a[i]}')"

  {
    [[ -n "$cleaned" ]] && printf '%s\n\n' "$cleaned"
    cat <<EOF
${start}
${config_name}:
    base_path: '${q}'
    is_default: true
    diffusion_models: |
        diffusion_models
        unet
    loras: loras
    vae: vae
    text_encoders: text_encoders
    checkpoints: checkpoints
    clip: clip
    clip_vision: clip_vision
    controlnet: controlnet
    pulid: pulid
    embeddings: embeddings
    upscale_models: upscale_models
    latent_upscale_models: latent_upscale_models
    model_patches: model_patches
${end}
EOF
  } > "$target"
}

declare -A COMFY_APP_ROOTS
declare -A COMFY_RUNTIME_ROOTS
declare -A COMFY_PY_BINS

prepare_comfy_instance() {
  local port="$1"
  local instance_root="$ISOLATED_ROOT/comfy_${port}"
  local app_root="$instance_root/app"
  local venv_root="$instance_root/venv"
  local runtime_root="$instance_root/runtime"

  mkdir -p "$runtime_root"/{input,output,temp,user,cache,logs,pycache}

  if [[ ! -f "$app_root/main.py" || "$COMFY_SYNC_APP" == "1" ]]; then
    echo "  - syncing app for comfy:${port}"
    sync_tree "$COMFY_ROOT" "$app_root"
  fi

  if [[ "$COMFY_SYNC_VENV" == "1" && -e "$venv_root" ]]; then
    echo "  - rebuilding venv for comfy:${port}"
    rm -rf "$venv_root"
  fi

  if [[ ! -x "$venv_root/bin/python" ]]; then
    case "$ISOLATED_VENV_MODE" in
      shared)
        echo "  - linking shared venv for comfy:${port}"
        mkdir -p "$instance_root"
        ln -sfn "$BASE_VENV_DIR" "$venv_root"
        ;;
      copy)
        echo "  - creating venv(copy) for comfy:${port}"
        sync_tree "$BASE_VENV_DIR" "$venv_root"
        ;;
      create)
        echo "  - creating venv(create) for comfy:${port}"
        python3 -m venv "$venv_root"
        "$venv_root/bin/pip" install -U pip wheel setuptools
        "$venv_root/bin/pip" install -r "$app_root/requirements.txt"
        ;;
      *)
        die "invalid ISOLATED_VENV_MODE: $ISOLATED_VENV_MODE (expected: shared|copy|create)"
        ;;
    esac
  fi

  COMFY_APP_ROOTS["$port"]="$app_root"
  COMFY_RUNTIME_ROOTS["$port"]="$runtime_root"
  COMFY_PY_BINS["$port"]="$venv_root/bin/python"
}

supervisor_command() {
  "$SUPERVISOR_PYTHON" - "$@" <<'PY'
import shlex
import sys

print(shlex.join(sys.argv[1:]).replace("%", "%%"))
PY
}

append_supervisor_program() {
  local name="$1"
  local directory="$2"
  local command="$3"
  local log_file="$4"
  local start_secs="$5"
  local stop_wait_secs="$6"
  local priority="$7"
  local environment="${8:-}"

  cat >>"$SUPERVISOR_CONFIG" <<EOF

[program:${name}]
directory=${directory}
command=${command}
priority=${priority}
autostart=true
autorestart=true
startsecs=${start_secs}
startretries=10
stopsignal=TERM
stopwaitsecs=${stop_wait_secs}
stopasgroup=true
killasgroup=true
redirect_stderr=true
stdout_logfile=${log_file}
stdout_logfile_maxbytes=${LOG_MAX_BYTES}
stdout_logfile_backups=${LOG_BACKUPS}
EOF
  if [[ -n "$environment" ]]; then
    printf 'environment=%s\n' "$environment" >>"$SUPERVISOR_CONFIG"
  fi
}

[[ -f "$COMFY_ROOT/main.py" ]] || die "ComfyUI not found: $COMFY_ROOT/main.py"
[[ -x "$PYTHON_BIN" ]] || die "Python not executable: $PYTHON_BIN"
[[ -x "$LLAMA_BIN" ]] || die "llama-server not executable: $LLAMA_BIN"
[[ -f "$LLAMA_MODEL" ]] || die "llama model not found: $LLAMA_MODEL"
[[ -z "$LLAMA_MMPROJ" || -f "$LLAMA_MMPROJ" ]] || die "llama mmproj not found: $LLAMA_MMPROJ"
command -v "$GO_BIN" >/dev/null 2>&1 || die "Go not found: $GO_BIN"
command -v ffmpeg >/dev/null 2>&1 || die "ffmpeg not found (install: apt-get install ffmpeg)"
avif_encoder_available || die "AVIF encoder not found (install: apt-get install libavif-bin, or use FFmpeg with AVIF muxer support)"
command -v "$SUPERVISORD_BIN" >/dev/null 2>&1 || die "supervisord not found (install: python3 -m pip install supervisor)"
command -v "$SUPERVISORCTL_BIN" >/dev/null 2>&1 || die "supervisorctl not found (install: python3 -m pip install supervisor)"
command -v "$SUPERVISOR_PYTHON" >/dev/null 2>&1 || die "Supervisor Python not found: $SUPERVISOR_PYTHON"
"$SUPERVISOR_PYTHON" -c 'import supervisor' >/dev/null 2>&1 || die "Python cannot import supervisor: $SUPERVISOR_PYTHON"
if [[ "$COMFY_ISOLATED" == "1" ]]; then
  command -v rsync >/dev/null 2>&1 || die "rsync is required when COMFY_ISOLATED=1"
fi

BASE_VENV_DIR="$(cd "$(dirname "$PYTHON_BIN")/.." && pwd)"
[[ -x "$BASE_VENV_DIR/bin/python" ]] || die "Base venv invalid: $BASE_VENV_DIR/bin/python"

# Parse comfy ports
IFS=' ' read -r -a configured_comfy_ports <<<"$COMFY_PORTS"
comfy_ports=()
for p in "${configured_comfy_ports[@]}"; do
  [[ -n "$p" ]] && comfy_ports+=("$p")
done
(( ${#comfy_ports[@]} >= 1 )) || die "COMFY_PORTS is empty"
[[ "$LLAMA_PORT" != "$GO_PORT" ]] || die "LLAMA_PORT and GO_PORT must be different"
for p in "${comfy_ports[@]}"; do
  [[ "$p" != "$GO_PORT" ]] || die "COMFY_PORTS contains GO_PORT: $p"
  [[ "$p" != "$LLAMA_PORT" ]] || die "COMFY_PORTS contains LLAMA_PORT: $p"
done

# Default: go uses all comfy backends in COMFY_PORTS
if [[ -z "${GO_COMFY_URLS:-}" ]]; then
  urls=()
  for p in "${comfy_ports[@]}"; do
    urls+=("http://127.0.0.1:${p}")
  done
  GO_COMFY_URLS="$(IFS=,; echo "${urls[*]}")"
fi

GO_WORKERS="${GO_WORKERS:-${#comfy_ports[@]}}"
if [[ -z "$GO_WORKERS" ]] || ! [[ "$GO_WORKERS" =~ ^[0-9]+$ ]] || (( GO_WORKERS < 1 )); then
  GO_WORKERS=1
fi
LLAMA_HEALTH_URL="${LLAMA_HEALTH_URL:-http://127.0.0.1:${LLAMA_PORT}/health}"

echo "[1/9] stopping old supervised and legacy processes..."
stop_project_supervisor
kill_by_pid_file "$RUN_DIR/go_gateway.pid"
kill_by_pid_file "$RUN_DIR/llama.pid"
kill_by_pattern "[g]o run ."
kill_by_pattern "[l]lama-server .*--port ${LLAMA_PORT}"
for p in "${comfy_ports[@]}"; do
  kill_by_pid_file "$RUN_DIR/comfy_${p}.pid"
  kill_by_pattern "[m]ain.py --listen .*--port ${p}"
done
kill_by_port "$LLAMA_PORT"
kill_by_port "$GO_PORT"
for p in "${comfy_ports[@]}"; do
  kill_by_port "$p"
done

if [[ "$COMFY_ISOLATED" == "1" ]]; then
  echo "[2/9] preparing isolated Comfy instances..."
  mkdir -p "$ISOLATED_ROOT"
  for p in "${comfy_ports[@]}"; do
    prepare_comfy_instance "$p"
  done
else
  echo "[2/9] isolated mode disabled (legacy shared Comfy root)"
fi

if [[ "$CONFIGURE_COMFY_PATHS" == "1" ]]; then
  echo "[3/9] configuring Comfy model paths..."
  if [[ "$COMFY_ISOLATED" == "1" ]]; then
    for p in "${comfy_ports[@]}"; do
      configure_comfy_paths "$PROJECT_ROOT/models" "${COMFY_APP_ROOTS[$p]}"
    done
  else
    configure_comfy_paths "$PROJECT_ROOT/models" "$COMFY_ROOT"
  fi
else
  echo "[3/9] skip model path configure"
fi

echo "[4/9] building go gateway..."
go_gateway_bin="$RUN_DIR/go_gateway"
"$GO_BIN" build -o "$go_gateway_bin" .

echo "[5/9] rendering supervisor config..."
llama_args=(
  -m "$LLAMA_MODEL"
  --host "$LLAMA_HOST"
  --port "$LLAMA_PORT"
  -c "$LLAMA_CTX_SIZE"
  -ngl "$LLAMA_NGL"
  --reasoning "$LLAMA_REASONING_MODE"
  --reasoning-budget "$LLAMA_REASONING_BUDGET_TOKENS"
  --reasoning-format "$LLAMA_REASONING_FORMAT"
  --reasoning-budget-message "$LLAMA_REASONING_BUDGET_MESSAGE"
)
if [[ -n "$LLAMA_MMPROJ" ]]; then
  llama_args+=(--mmproj "$LLAMA_MMPROJ")
fi
if [[ "$LLAMA_NO_CACHE_PROMPT" == "1" ]]; then
  llama_args+=(--no-cache-prompt)
fi
if [[ "$LLAMA_DRY_MULTIPLIER" != "0" ]]; then
  llama_args+=(--dry-multiplier "$LLAMA_DRY_MULTIPLIER" --dry-base "$LLAMA_DRY_BASE" --dry-allowed-length "$LLAMA_DRY_ALLOWED_LENGTH")
fi
if [[ -n "${LLAMA_EXTRA_ARGS:-}" ]]; then
  read -r -a extra_llama_args <<<"$LLAMA_EXTRA_ARGS"
  llama_args+=("${extra_llama_args[@]}")
fi

cat >"$SUPERVISOR_CONFIG" <<EOF
[unix_http_server]
file=${SUPERVISOR_SOCKET}
chmod=0700

[supervisord]
logfile=${LOG_DIR}/supervisord.log
logfile_maxbytes=20MB
logfile_backups=${LOG_BACKUPS}
pidfile=${SUPERVISOR_PID_FILE}
childlogdir=${LOG_DIR}
nodaemon=false
minfds=1024
minprocs=200
strip_ansi=true

[rpcinterface:supervisor]
supervisor.rpcinterface_factory = supervisor.rpcinterface:make_main_rpcinterface

[supervisorctl]
serverurl=unix://${SUPERVISOR_SOCKET}
EOF
chmod 600 "$SUPERVISOR_CONFIG"

llama_command="$(supervisor_command "$LLAMA_BIN" "${llama_args[@]}")"
append_supervisor_program \
  "llama" "$PROJECT_ROOT" "$llama_command" "$LOG_DIR/llama.log" 5 30 10

if [[ "$MEMORY_GUARD_ENABLED" == "1" ]]; then
  memory_guard_args=(
    "$SUPERVISOR_PYTHON" -u "$PROJECT_ROOT/scripts/memory_guard.py"
    --cgroup-dir "$CGROUP_DIR"
    --memory-high-gib "$CGROUP_MEMORY_HIGH_GIB"
    --reclaim-threshold-gib "$MEMORY_RECLAIM_THRESHOLD_GIB"
    --interval "$MEMORY_RECLAIM_INTERVAL"
    --cooldown "$MEMORY_RECLAIM_COOLDOWN"
    --startup-grace "$COMFY_WAIT_SEC"
    --request-timeout "$HEALTH_CHECK_TIMEOUT"
    --models-root "$PROJECT_ROOT/models"
    --exclude-root "$PROJECT_ROOT/models/llama"
    --min-file-size-mib "$MEMORY_RECLAIM_MIN_FILE_MIB"
  )
  for p in "${comfy_ports[@]}"; do
    memory_guard_args+=(--comfy-url "http://127.0.0.1:${p}")
  done
  memory_guard_command="$(supervisor_command "${memory_guard_args[@]}")"
  append_supervisor_program \
    "memory_guard" "$PROJECT_ROOT" "$memory_guard_command" "$LOG_DIR/memory_guard.log" 1 10 5
fi

monitor_args=(
  "$SUPERVISOR_PYTHON" -u "$PROJECT_ROOT/scripts/health_monitor.py"
  --interval "$HEALTH_CHECK_INTERVAL"
  --failures "$HEALTH_FAILURE_THRESHOLD"
  --timeout "$HEALTH_CHECK_TIMEOUT"
  --retry-interval "$HEALTH_RETRY_INTERVAL"
  --target "llama=$LLAMA_HEALTH_URL"
  --grace "llama=$LLAMA_WAIT_SEC"
)

for p in "${comfy_ports[@]}"; do
  if [[ "$COMFY_ISOLATED" == "1" ]]; then
    app_root="${COMFY_APP_ROOTS[$p]}"
    runtime_root="${COMFY_RUNTIME_ROOTS[$p]}"
    comfy_python="${COMFY_PY_BINS[$p]}"
    comfy_command="$(supervisor_command \
      "$comfy_python" -u "$COMFY_LAUNCHER" \
      --listen "$COMFY_HOST" \
      --port "$p" \
      --input-directory "$runtime_root/input" \
      --output-directory "$runtime_root/output" \
      --temp-directory "$runtime_root/temp" \
      --user-directory "$runtime_root/user")"
    comfy_environment="PYTHONPYCACHEPREFIX=\"$runtime_root/pycache\",XDG_CACHE_HOME=\"$runtime_root/cache\",HF_HOME=\"$runtime_root/cache/hf\",TORCH_HOME=\"$runtime_root/cache/torch\""
  else
    app_root="$COMFY_ROOT"
    comfy_command="$(supervisor_command "$PYTHON_BIN" -u "$COMFY_LAUNCHER" --listen "$COMFY_HOST" --port "$p")"
    comfy_environment=""
  fi
  append_supervisor_program \
    "comfy_${p}" "$app_root" "$comfy_command" "$LOG_DIR/comfy_${p}.log" 5 60 10 "$comfy_environment"
  monitor_args+=(
    --target "comfy_${p}=http://127.0.0.1:${p}/system_stats"
    --grace "comfy_${p}=$COMFY_WAIT_SEC"
  )
done

export LISTEN_ADDR=":${GO_PORT}"
export WORKFLOW_CONFIG_DIR="$PROJECT_ROOT/app/configs/workflows"
export SERVICE_CONFIG_FILE="$PROJECT_ROOT/app/configs/service.yaml"
export OUTPUT_DIR="${OUTPUT_DIR:-/root/autodl-fs}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-}"
export TEXT_BASE_URL="${TEXT_BASE_URL:-http://127.0.0.1:${LLAMA_PORT}}"
export TEXT_MODEL="${TEXT_MODEL:-}"
export TEXT_API_KEY="${TEXT_API_KEY:-}"
export COMFY_URLS="$GO_COMFY_URLS"
export GO_WORKERS

gateway_command="$(supervisor_command "$go_gateway_bin")"
append_supervisor_program \
  "go_gateway" "$PROJECT_ROOT" "$gateway_command" "$LOG_DIR/go_gateway.log" 5 30 10
monitor_args+=(
  --target "go_gateway=http://127.0.0.1:${GO_PORT}/healthz"
  --grace "go_gateway=$GO_WAIT_SEC"
)

monitor_command="$(supervisor_command "${monitor_args[@]}")"
append_supervisor_program \
  "health_monitor" "$PROJECT_ROOT" "$monitor_command" "$LOG_DIR/health_monitor.log" 1 10 20 \
  "SUPERVISOR_SERVER_URL=\"unix://$SUPERVISOR_SOCKET\""

echo "[6/9] starting project supervisor..."
rm -f "$SUPERVISOR_SOCKET" "$SUPERVISOR_PID_FILE"
"$SUPERVISORD_BIN" -c "$SUPERVISOR_CONFIG"
for _ in {1..30}; do
  supervisor_is_running && break
  sleep 1
done
supervisor_is_running || die "project supervisor failed to start; see $LOG_DIR/supervisord.log"
"$SUPERVISORCTL_BIN" -c "$SUPERVISOR_CONFIG" status

echo "[7/9] waiting llama health..."
wait_http_ok "$LLAMA_HEALTH_URL" "$LLAMA_WAIT_SEC" "llama" || {
  tail -n 120 "$LOG_DIR/llama.log" || true
  die "llama not ready; supervisor will keep retrying"
}

echo "[8/9] waiting ComfyUI health..."
for p in "${comfy_ports[@]}"; do
  wait_http_ok "http://127.0.0.1:${p}/system_stats" "$COMFY_WAIT_SEC" "comfy:${p}" || {
    tail -n 120 "$LOG_DIR/comfy_${p}.log" || true
    die "comfy ${p} not ready; supervisor will keep retrying"
  }
done

echo "[9/9] waiting go gateway health..."
wait_http_ok "http://127.0.0.1:${GO_PORT}/healthz" "$GO_WAIT_SEC" "go_gateway" || {
  tail -n 120 "$LOG_DIR/go_gateway.log" || true
  die "go gateway not ready; supervisor will keep retrying"
}

echo ""
echo "[ok] all services are supervised"
echo "Llama:       http://127.0.0.1:${LLAMA_PORT}/health"
echo "ComfyUI UI:  http://127.0.0.1:${comfy_ports[0]}/"
echo "Go Health:   http://127.0.0.1:${GO_PORT}/healthz"
echo "Go->Comfys:  ${GO_COMFY_URLS}"
echo "Go Workers:  ${GO_WORKERS}"
echo "Isolated:    ${COMFY_ISOLATED} (${ISOLATED_ROOT})"
echo "Logs:        ${LOG_DIR}"
echo "Status:      scripts/start_multi_stack.sh status"
