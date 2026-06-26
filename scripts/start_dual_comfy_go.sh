#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

HOST="${HOST:-0.0.0.0}"
COMFY_HOST="${COMFY_HOST:-0.0.0.0}"
GO_PORT="${GO_PORT:-6008}"
GO_COMFY_PORT="${GO_COMFY_PORT:-8188}"
UI_COMFY_PORT="${UI_COMFY_PORT:-6006}"
GO_WORKERS="${GO_WORKERS:-1}"
GO_BIN="${GO_BIN:-go}"

COMFY_ROOT="${COMFY_ROOT:-$PROJECT_ROOT/ComfyUI}"
CONFIGURE_COMFY_PATHS="${CONFIGURE_COMFY_PATHS:-1}"
COMFY_WAIT_SEC="${COMFY_WAIT_SEC:-120}"
GO_WAIT_SEC="${GO_WAIT_SEC:-60}"

LOG_DIR="${LOG_DIR:-$PROJECT_ROOT/logs}"
RUN_DIR="${RUN_DIR:-$PROJECT_ROOT/.run}"
COMFY_RUNTIME_ROOT="${COMFY_RUNTIME_ROOT:-$RUN_DIR/comfy_runtime}"
mkdir -p "$LOG_DIR" "$RUN_DIR" "$COMFY_RUNTIME_ROOT"

timestamp="$(date +%Y%m%d_%H%M%S)"

die() {
  echo "[error] $*" >&2
  exit 1
}

detect_comfy_root() {
  local candidates=(
    "$PROJECT_ROOT/ComfyUI"
    "/root/autodl-tmp/projects/chat_image/ComfyUI"
    "/root/autodl-tmp/projects/ComfyUI"
    "/root/autodl-tmp/ComfyUI"
    "/opt/ComfyUI"
    "$HOME/ComfyUI"
  )
  local d
  for d in "${candidates[@]}"; do
    if [[ -f "$d/main.py" ]]; then
      echo "$d"
      return 0
    fi
  done
  return 1
}

detect_python() {
  if [[ -n "${PYTHON_BIN:-}" ]]; then
    echo "$PYTHON_BIN"
    return 0
  fi
  if [[ -x "$PROJECT_ROOT/venv/bin/python" ]]; then
    echo "$PROJECT_ROOT/venv/bin/python"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    command -v python3
    return 0
  fi
  if command -v python >/dev/null 2>&1; then
    command -v python
    return 0
  fi
  return 1
}

if [[ -z "$COMFY_ROOT" || ! -f "$COMFY_ROOT/main.py" ]]; then
  COMFY_ROOT="$(detect_comfy_root || true)"
fi
if [[ -n "$COMFY_ROOT" && -d "$COMFY_ROOT" ]]; then
  COMFY_ROOT="$(cd "$COMFY_ROOT" && pwd)"
fi

PYTHON_BIN="$(detect_python || true)"

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

kill_pids() {
  local title="$1"
  shift
  local pids=("$@")
  if [[ "${#pids[@]}" -eq 0 ]]; then
    return 0
  fi
  echo "[stop] $title: ${pids[*]}"
  kill "${pids[@]}" 2>/dev/null || true
  sleep 1

  local alive=()
  local pid
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      alive+=("$pid")
    fi
  done
  if [[ "${#alive[@]}" -gt 0 ]]; then
    echo "[stop] force kill $title: ${alive[*]}"
    kill -9 "${alive[@]}" 2>/dev/null || true
  fi
}

kill_by_pattern() {
  local title="$1"
  local pattern="$2"
  local pids=()
  mapfile -t pids < <(pgrep -f "$pattern" 2>/dev/null || true)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    kill_pids "$title" "${pids[@]}"
  fi
}

kill_by_port() {
  local port="$1"
  local title="$2"
  local pids=()
  if command -v lsof >/dev/null 2>&1; then
    mapfile -t pids < <(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill_pids "$title" "${pids[@]}"
    fi
  fi
}

start_comfy() {
  local role="$1"
  local port="$2"
  local runtime_root="$COMFY_RUNTIME_ROOT/$role"
  local log_file="$LOG_DIR/comfy_${role}_${port}_${timestamp}.log"
  local pid_file="$RUN_DIR/comfy_${role}.pid"

  mkdir -p "$runtime_root"/{input,output,temp,user,cache,logs,pycache}
  echo "[start] ComfyUI ${role} on :${port}"
  (
    cd "$COMFY_ROOT"
    nohup env \
      PYTHONPYCACHEPREFIX="$runtime_root/pycache" \
      XDG_CACHE_HOME="$runtime_root/cache" \
      HF_HOME="$runtime_root/cache/hf" \
      TORCH_HOME="$runtime_root/cache/torch" \
      "$PYTHON_BIN" -u main.py \
        --listen "$COMFY_HOST" \
        --port "$port" \
        --input-directory "$runtime_root/input" \
        --output-directory "$runtime_root/output" \
        --temp-directory "$runtime_root/temp" \
        --user-directory "$runtime_root/user" \
        >"$log_file" 2>&1 &
    echo $! > "$pid_file"
  )

  wait_http_ok "http://127.0.0.1:${port}/system_stats" "$COMFY_WAIT_SEC" "comfy:${role}" || {
    tail -n 120 "$log_file" || true
    die "ComfyUI ${role} failed to boot"
  }
}

[[ -n "$COMFY_ROOT" && -f "$COMFY_ROOT/main.py" ]] || die "ComfyUI not found. Set COMFY_ROOT=/path/to/ComfyUI"
[[ -n "$PYTHON_BIN" && -x "$PYTHON_BIN" ]] || die "Python not executable. Set PYTHON_BIN=/path/to/python"
command -v "$GO_BIN" >/dev/null 2>&1 || die "Go not found. Set GO_BIN=/path/to/go"

if [[ "$GO_COMFY_PORT" == "$UI_COMFY_PORT" ]]; then
  die "GO_COMFY_PORT and UI_COMFY_PORT must be different"
fi
if [[ "$GO_PORT" == "$GO_COMFY_PORT" || "$GO_PORT" == "$UI_COMFY_PORT" ]]; then
  die "GO_PORT must be different from both ComfyUI ports"
fi

echo "[1/5] stopping old go_gateway and ComfyUI processes..."
kill_by_pattern "go gateway" "[g]o run ."
kill_by_pattern "comfyui" "[p]ython.*main.py.*--listen"
kill_by_pattern "comfyui" "[p]ython.*ComfyUI.*main.py"

for port in "$GO_PORT" "$GO_COMFY_PORT" "$UI_COMFY_PORT" 6006 6008 8188 8189 8190; do
  kill_by_port "$port" "port-${port}"
done

if [[ "$CONFIGURE_COMFY_PATHS" == "1" ]]; then
  echo "[2/5] configuring ComfyUI model paths..."
  bash "$PROJECT_ROOT/scripts/configure_comfy_paths.sh" \
    --models-root "$PROJECT_ROOT/models" \
    --comfy-root "$COMFY_ROOT"
else
  echo "[2/5] skip ComfyUI model path configure"
fi

echo "[3/5] starting ComfyUI instances..."
start_comfy "go" "$GO_COMFY_PORT"
start_comfy "ui" "$UI_COMFY_PORT"

echo "[4/5] starting go_gateway..."
go_log="$LOG_DIR/go_gateway_${timestamp}.log"
go_comfy_url="http://127.0.0.1:${GO_COMFY_PORT}"
nohup env \
  LISTEN_ADDR=":${GO_PORT}" \
  WORKFLOW_CONFIG_DIR="$PROJECT_ROOT/configs/workflows" \
  SERVICE_CONFIG_FILE="$PROJECT_ROOT/configs/service.yaml" \
  OUTPUT_DIR="$PROJECT_ROOT/outputs" \
  COMFY_URL="$go_comfy_url" \
  GO_WORKERS="$GO_WORKERS" \
  GO_GATEWAY_I2V_TRIM_HEAD_SEC="${GO_GATEWAY_I2V_TRIM_HEAD_SEC:-1}" \
  bash -lc "cd \"$PROJECT_ROOT\" && \"$GO_BIN\" run ." >"$go_log" 2>&1 &
echo $! > "$RUN_DIR/go_gateway.pid"

wait_http_ok "http://127.0.0.1:${GO_PORT}/healthz" "$GO_WAIT_SEC" "go_gateway" || {
  tail -n 120 "$go_log" || true
  die "go_gateway failed to boot"
}

echo "[5/5] ready"
echo "ComfyUI for browser: http://127.0.0.1:${UI_COMFY_PORT}/"
echo "go_gateway API:      http://127.0.0.1:${GO_PORT}/"
echo "go_gateway -> Comfy: ${go_comfy_url}"
echo "Logs:                $LOG_DIR"
