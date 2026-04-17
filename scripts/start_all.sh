#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

HOST="${HOST:-0.0.0.0}"
GATEWAY_PORT="${GATEWAY_PORT:-6006}"
COMFY_HOST="${COMFY_HOST:-0.0.0.0}"
COMFY_PORT="${COMFY_PORT:-8188}"
COMFY_ROOT="${COMFY_ROOT:-$PROJECT_ROOT/ComfyUI}"
COMFY_URL="${COMFY_URL:-http://127.0.0.1:${COMFY_PORT}}"
GATEWAY_PYTHON="${GATEWAY_PYTHON:-python3}"
COMFY_PYTHON="${COMFY_PYTHON:-python3}"
AUTO_CONFIG_COMFY_PATHS="${AUTO_CONFIG_COMFY_PATHS:-1}"
SKIP_COMFY="${SKIP_COMFY:-0}"
COMFY_WAIT_SEC="${COMFY_WAIT_SEC:-120}"
GATEWAY_WAIT_SEC="${GATEWAY_WAIT_SEC:-45}"

LOG_DIR="${LOG_DIR:-$PROJECT_ROOT/logs}"
RUN_DIR="${RUN_DIR:-$PROJECT_ROOT/.run}"
mkdir -p "$LOG_DIR" "$RUN_DIR"

timestamp="$(date +%Y%m%d_%H%M%S)"
COMFY_LOG="$LOG_DIR/comfyui_${timestamp}.log"
GATEWAY_LOG="$LOG_DIR/gateway_${timestamp}.log"

detect_comfy_root() {
  local candidates=(
    "$PROJECT_ROOT/ComfyUI"
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

if [[ -z "$COMFY_ROOT" || ! -f "$COMFY_ROOT/main.py" ]]; then
  COMFY_ROOT="$(detect_comfy_root || true)"
fi
if [[ -n "$COMFY_ROOT" && -d "$COMFY_ROOT" ]]; then
  COMFY_ROOT="$(cd "$COMFY_ROOT" && pwd)"
fi

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
  mapfile -t pids < <(pgrep -f "$pattern" || true)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    kill_pids "$title" "${pids[@]}"
  fi
}

kill_by_port() {
  local port="$1"
  local title="$2"
  if command -v lsof >/dev/null 2>&1; then
    mapfile -t pids < <(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill_pids "$title" "${pids[@]}"
    fi
  fi
}

wait_http_ok() {
  local url="$1"
  local timeout_sec="$2"
  local name="$3"
  local start
  start="$(date +%s)"
  while true; do
    if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      echo "[ok] $name ready: $url"
      return 0
    fi
    local now elapsed
    now="$(date +%s)"
    elapsed=$((now - start))
    if (( elapsed >= timeout_sec )); then
      echo "[error] $name not ready in ${timeout_sec}s: $url"
      return 1
    fi
    sleep 1
  done
}

echo "[1/4] stopping old processes..."
kill_by_pattern "gateway" "[p]ython.*local_generation_gateway.py"
kill_by_pattern "gateway" "[u]vicorn.*local_generation_gateway"
kill_by_pattern "comfyui" "[p]ython.*main.py.*--port[ =]${COMFY_PORT}"
kill_by_port "$GATEWAY_PORT" "port-${GATEWAY_PORT}"
kill_by_port "$COMFY_PORT" "port-${COMFY_PORT}"

if [[ "$SKIP_COMFY" != "1" ]]; then
  if [[ -z "$COMFY_ROOT" || ! -f "$COMFY_ROOT/main.py" ]]; then
    echo "[error] ComfyUI path invalid. Please set COMFY_ROOT explicitly."
    echo "Example:"
    echo "  COMFY_ROOT=/root/autodl-tmp/projects/chat_image/ComfyUI bash scripts/start_all.sh"
    exit 1
  fi
fi

if [[ "$AUTO_CONFIG_COMFY_PATHS" == "1" && -d "$COMFY_ROOT" ]]; then
  echo "[2/4] configuring ComfyUI model paths..."
  "$GATEWAY_PYTHON" "$PROJECT_ROOT/scripts/configure_comfy_paths.py" \
    --models-root "$PROJECT_ROOT/models" \
    --comfy-root "$COMFY_ROOT"
else
  echo "[2/4] skip ComfyUI path configure"
fi

if [[ "$SKIP_COMFY" != "1" ]]; then
  if [[ ! -d "$COMFY_ROOT" ]]; then
    echo "[error] COMFY_ROOT not found: $COMFY_ROOT"
    exit 1
  fi
  echo "[3/4] starting ComfyUI..."
  (
    cd "$COMFY_ROOT"
    nohup "$COMFY_PYTHON" -u main.py --listen "$COMFY_HOST" --port "$COMFY_PORT" >"$COMFY_LOG" 2>&1 &
    echo $! > "$RUN_DIR/comfyui.pid"
  )
  if ! wait_http_ok "${COMFY_URL}/system_stats" "$COMFY_WAIT_SEC" "comfyui"; then
    echo "[error] ComfyUI failed to boot, tail log:"
    tail -n 120 "$COMFY_LOG" || true
    exit 1
  fi
else
  echo "[3/4] skip ComfyUI start (SKIP_COMFY=1)"
fi

echo "[4/4] starting gateway..."
nohup env HOST="$HOST" PORT="$GATEWAY_PORT" COMFY_URL="$COMFY_URL" \
  "$GATEWAY_PYTHON" -u "$PROJECT_ROOT/local_generation_gateway.py" >"$GATEWAY_LOG" 2>&1 &
echo $! > "$RUN_DIR/gateway.pid"
if [[ ! -f "$RUN_DIR/gateway.pid" ]] || ! kill -0 "$(cat "$RUN_DIR/gateway.pid")" 2>/dev/null; then
  echo "[error] gateway start failed, tail log:"
  tail -n 80 "$GATEWAY_LOG" || true
  exit 1
fi
if ! wait_http_ok "http://127.0.0.1:${GATEWAY_PORT}/healthz" "$GATEWAY_WAIT_SEC" "gateway"; then
  echo "[error] Gateway failed to boot, tail log:"
  tail -n 120 "$GATEWAY_LOG" || true
  exit 1
fi
echo "[ok] gateway started on :$GATEWAY_PORT"

if [[ "$SKIP_COMFY" != "1" && -f "$RUN_DIR/comfyui.pid" ]] && kill -0 "$(cat "$RUN_DIR/comfyui.pid")" 2>/dev/null; then
  echo "[ok] comfyui started on :$COMFY_PORT"
fi

echo ""
echo "Gateway URL: http://127.0.0.1:${GATEWAY_PORT}/web/"
echo "ComfyUI URL: http://127.0.0.1:${COMFY_PORT}/"
echo "Gateway Log: $GATEWAY_LOG"
if [[ "$SKIP_COMFY" != "1" ]]; then
  echo "ComfyUI Log: $COMFY_LOG"
fi
