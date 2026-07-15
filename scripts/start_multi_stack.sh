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
# DRY sampler: suppresses the degenerate repetition loops that make the reasoning
# model occasionally run away (generating to the context ceiling → truncated →
# empty answer). Only penalizes long verbatim repeats, so normal thinking is
# untouched. Set LLAMA_DRY_MULTIPLIER=0 to disable.
LLAMA_DRY_MULTIPLIER="${LLAMA_DRY_MULTIPLIER:-0.8}"
LLAMA_DRY_BASE="${LLAMA_DRY_BASE:-1.75}"
LLAMA_DRY_ALLOWED_LENGTH="${LLAMA_DRY_ALLOWED_LENGTH:-2}"
COMFY_HOST="${COMFY_HOST:-0.0.0.0}"
# VRAM strategy for ComfyUI. Default "highvram": keep models resident in the
# (large, mostly-idle) GPU instead of offloading the 40GB video model + VAE to
# the container's limited CPU RAM — that offloading was getting the process
# OOM-killed (SIGKILL) during the video VAE decode while 74GB of VRAM sat free.
# Set to "" for ComfyUI's auto default, or "gpu-only"/"normalvram" to change.
COMFY_VRAM_MODE="${COMFY_VRAM_MODE:-highvram}"
COMFY_PORTS="${COMFY_PORTS:-6006 6009}"

# Comfy template source (read-only source tree)
COMFY_ROOT="${COMFY_ROOT:-$PROJECT_ROOT/ComfyUI}"
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
mkdir -p "$LOG_DIR" "$RUN_DIR"

timestamp="$(date +%Y%m%d_%H%M%S)"

die() {
  echo "[error] $*" >&2
  exit 1
}

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

[[ -f "$COMFY_ROOT/main.py" ]] || die "ComfyUI not found: $COMFY_ROOT/main.py"
[[ -x "$PYTHON_BIN" ]] || die "Python not executable: $PYTHON_BIN"
[[ -x "$LLAMA_BIN" ]] || die "llama-server not executable: $LLAMA_BIN"
[[ -f "$LLAMA_MODEL" ]] || die "llama model not found: $LLAMA_MODEL"
[[ -z "$LLAMA_MMPROJ" || -f "$LLAMA_MMPROJ" ]] || die "llama mmproj not found: $LLAMA_MMPROJ"
command -v "$GO_BIN" >/dev/null 2>&1 || die "Go not found: $GO_BIN"
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

echo "[1/8] stopping old processes..."
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
  echo "[2/8] preparing isolated Comfy instances..."
  mkdir -p "$ISOLATED_ROOT"
  for p in "${comfy_ports[@]}"; do
    prepare_comfy_instance "$p"
  done
else
  echo "[2/8] isolated mode disabled (legacy shared Comfy root)"
fi

if [[ "$CONFIGURE_COMFY_PATHS" == "1" ]]; then
  echo "[3/8] configuring Comfy model paths..."
  if [[ "$COMFY_ISOLATED" == "1" ]]; then
    for p in "${comfy_ports[@]}"; do
      configure_comfy_paths "$PROJECT_ROOT/models" "${COMFY_APP_ROOTS[$p]}"
    done
  else
    configure_comfy_paths "$PROJECT_ROOT/models" "$COMFY_ROOT"
  fi
else
  echo "[3/8] skip model path configure"
fi

echo "[4/8] starting llama..."
llama_log="$LOG_DIR/llama_${timestamp}.log"
llama_args=(
  -m "$LLAMA_MODEL"
  --host "$LLAMA_HOST"
  --port "$LLAMA_PORT"
  -c "$LLAMA_CTX_SIZE"
  -ngl "$LLAMA_NGL"
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
nohup "$LLAMA_BIN" "${llama_args[@]}" >"$llama_log" 2>&1 &
echo $! > "$RUN_DIR/llama.pid"

echo "[5/8] waiting llama health..."
wait_http_ok "$LLAMA_HEALTH_URL" "$LLAMA_WAIT_SEC" "llama" || {
  tail -n 120 "$llama_log" || true
  die "llama boot failed"
}

echo "[6/8] starting ComfyUI backends..."
comfy_vram_args=()
[[ -n "$COMFY_VRAM_MODE" ]] && comfy_vram_args+=("--$COMFY_VRAM_MODE")
for p in "${comfy_ports[@]}"; do
  log_file="$LOG_DIR/comfy_${p}_${timestamp}.log"
  if [[ "$COMFY_ISOLATED" == "1" ]]; then
    app_root="${COMFY_APP_ROOTS[$p]}"
    runtime_root="${COMFY_RUNTIME_ROOTS[$p]}"
    comfy_python="${COMFY_PY_BINS[$p]}"
    (
      cd "$app_root"
      nohup env \
        PYTHONPYCACHEPREFIX="$runtime_root/pycache" \
        XDG_CACHE_HOME="$runtime_root/cache" \
        HF_HOME="$runtime_root/cache/hf" \
        TORCH_HOME="$runtime_root/cache/torch" \
        "$comfy_python" -u main.py \
          --listen "$COMFY_HOST" \
          --port "$p" \
          "${comfy_vram_args[@]}" \
          --input-directory "$runtime_root/input" \
          --output-directory "$runtime_root/output" \
          --temp-directory "$runtime_root/temp" \
          --user-directory "$runtime_root/user" \
          >"$log_file" 2>&1 &
      echo $! > "$RUN_DIR/comfy_${p}.pid"
    )
  else
    (
      cd "$COMFY_ROOT"
      nohup "$PYTHON_BIN" -u main.py --listen "$COMFY_HOST" --port "$p" "${comfy_vram_args[@]}" >"$log_file" 2>&1 &
      echo $! > "$RUN_DIR/comfy_${p}.pid"
    )
  fi
done

echo "[7/8] waiting ComfyUI health..."
for p in "${comfy_ports[@]}"; do
  wait_http_ok "http://127.0.0.1:${p}/system_stats" "$COMFY_WAIT_SEC" "comfy:${p}" || {
    tail -n 120 "$LOG_DIR/comfy_${p}_${timestamp}.log" || true
    die "comfy ${p} boot failed"
  }
done

echo "[8/8] starting go gateway (multi comfy load-balance)..."
go_log="$LOG_DIR/go_gateway_${timestamp}.log"
go_gateway_bin="$RUN_DIR/go_gateway"
"$GO_BIN" build -o "$go_gateway_bin" .
nohup env LISTEN_ADDR=":${GO_PORT}" \
  WORKFLOW_CONFIG_DIR="$PROJECT_ROOT/app/configs/workflows" \
  SERVICE_CONFIG_FILE="$PROJECT_ROOT/app/configs/service.yaml" \
  OUTPUT_DIR="/root/autodl-fs" \
  S3_ACCESS_KEY="${S3_ACCESS_KEY:-}" \
  S3_SECRET_KEY="${S3_SECRET_KEY:-}" \
  TEXT_BASE_URL="${TEXT_BASE_URL:-http://127.0.0.1:${LLAMA_PORT}}" \
  TEXT_MODEL="${TEXT_MODEL:-}" \
  TEXT_API_KEY="${TEXT_API_KEY:-}" \
  COMFY_URLS="$GO_COMFY_URLS" \
  GO_WORKERS="$GO_WORKERS" \
  "$go_gateway_bin" >"$go_log" 2>&1 &
echo $! > "$RUN_DIR/go_gateway.pid"
wait_http_ok "http://127.0.0.1:${GO_PORT}/healthz" "$GO_WAIT_SEC" "go_gateway" || {
  tail -n 120 "$go_log" || true
  die "go gateway boot failed"
}

echo ""
echo "[ok] all services started"
echo "Llama:       http://127.0.0.1:${LLAMA_PORT}/health"
echo "ComfyUI UI:  http://127.0.0.1:${comfy_ports[0]}/"
echo "Go Health:   http://127.0.0.1:${GO_PORT}/healthz"
echo "Go->Comfys:  ${GO_COMFY_URLS}"
echo "Go Workers:  ${GO_WORKERS}"
echo "Isolated:    ${COMFY_ISOLATED} (${ISOLATED_ROOT})"
echo "Logs:        ${LOG_DIR}"
