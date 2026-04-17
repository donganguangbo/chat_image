#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

GATEWAY_PORT="${GATEWAY_PORT:-6006}"
COMFY_PORT="${COMFY_PORT:-8188}"
RUN_DIR="${RUN_DIR:-$PROJECT_ROOT/.run}"

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

kill_by_pidfile() {
  local file="$1"
  local title="$2"
  if [[ -f "$file" ]]; then
    local pid
    pid="$(cat "$file" 2>/dev/null || true)"
    if [[ -n "$pid" ]] && [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill_pids "$title(pidfile)" "$pid"
    fi
    rm -f "$file"
  fi
}

kill_by_pattern() {
  local title="$1"
  local pattern="$2"
  mapfile -t pids < <(pgrep -f "$pattern" || true)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    kill_pids "$title(pattern)" "${pids[@]}"
  fi
}

kill_by_port() {
  local port="$1"
  local title="$2"
  if command -v lsof >/dev/null 2>&1; then
    mapfile -t pids < <(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill_pids "$title(port:$port)" "${pids[@]}"
    fi
  fi
}

mkdir -p "$RUN_DIR"

echo "[1/3] stop by pid files..."
kill_by_pidfile "$RUN_DIR/gateway.pid" "gateway"
kill_by_pidfile "$RUN_DIR/comfyui.pid" "comfyui"

echo "[2/3] stop by process pattern..."
kill_by_pattern "gateway" "[p]ython.*local_generation_gateway.py"
kill_by_pattern "gateway" "[u]vicorn.*local_generation_gateway"
kill_by_pattern "comfyui" "[p]ython.*main.py.*--port[ =]${COMFY_PORT}"
kill_by_pattern "comfyui" "[p]ython.*main.py.*--listen"

echo "[3/3] stop by occupied ports..."
kill_by_port "$GATEWAY_PORT" "gateway"
kill_by_port "$COMFY_PORT" "comfyui"

echo "[ok] services stopped (if running)."
