#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_PARENT="$(cd "$PROJECT_ROOT/.." && pwd)"
cd "$PROJECT_ROOT"

FISH_HOST="${FISH_HOST:-0.0.0.0}"
FISH_PORT="${FISH_PORT:-6006}"
SGLANG_OMNI_ROOT="${SGLANG_OMNI_ROOT:-$PROJECT_PARENT/sglang-omni}"
SGLANG_VENV="${SGLANG_VENV:-$SGLANG_OMNI_ROOT/.venv}"
SGLANG_BIN="${SGLANG_BIN:-$SGLANG_VENV/bin/sgl-omni}"
FISH_MODEL_PATH="${FISH_MODEL_PATH:-$PROJECT_ROOT/models/fish/s2-pro}"
FISH_CONFIG="${FISH_CONFIG:-$SGLANG_OMNI_ROOT/examples/configs/s2pro_tts.yaml}"
FISH_WAIT_SEC="${FISH_WAIT_SEC:-600}"

LOG_DIR="${LOG_DIR:-$PROJECT_ROOT/logs}"
RUN_DIR="${RUN_DIR:-$PROJECT_ROOT/.run}"
mkdir -p "$LOG_DIR" "$RUN_DIR"

timestamp="$(date +%Y%m%d_%H%M%S)"
fish_log="$LOG_DIR/fish_speech_${timestamp}.log"

die() {
  echo "[error] $*" >&2
  exit 1
}

list_port_pids() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true
  fi
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltnp "sport = :$port" 2>/dev/null | sed -nE 's/.*pid=([0-9]+).*/\1/p' || true
  fi
}

kill_by_port() {
  local port="$1"
  local -a pids
  mapfile -t pids < <(list_port_pids "$port" | sort -u)
  if [[ "${#pids[@]}" -gt 0 ]]; then
    echo "[info] stopping processes on port ${port}: ${pids[*]}"
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

wait_fish_ready() {
  local start now elapsed
  start="$(date +%s)"
  while true; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${FISH_PORT}/health" >/dev/null 2>&1 ||
      curl -fsS --max-time 3 "http://127.0.0.1:${FISH_PORT}/v1/models" >/dev/null 2>&1 ||
      curl -fsS --max-time 3 "http://127.0.0.1:${FISH_PORT}/docs" >/dev/null 2>&1; then
      return 0
    fi
    now="$(date +%s)"
    elapsed=$((now - start))
    if (( elapsed >= FISH_WAIT_SEC )); then
      return 1
    fi
    sleep 1
  done
}

[[ -d "$SGLANG_OMNI_ROOT" ]] || die "sglang-omni not found: $SGLANG_OMNI_ROOT"
[[ -x "$SGLANG_BIN" ]] || die "sgl-omni not executable: $SGLANG_BIN"
[[ -d "$FISH_MODEL_PATH" ]] || die "Fish model dir not found: $FISH_MODEL_PATH"
[[ -f "$FISH_CONFIG" ]] || die "Fish config not found: $FISH_CONFIG"

echo "[1/3] stopping old service on port ${FISH_PORT}..."
kill_by_port "$FISH_PORT"

fish_args=(
  serve
  --model-path "$FISH_MODEL_PATH"
  --config "$FISH_CONFIG"
  --host "$FISH_HOST"
  --port "$FISH_PORT"
)
if [[ -n "${FISH_EXTRA_ARGS:-}" ]]; then
  read -r -a extra_fish_args <<<"$FISH_EXTRA_ARGS"
  fish_args+=("${extra_fish_args[@]}")
fi

echo "[2/3] starting Fish Speech service..."
(
  cd "$SGLANG_OMNI_ROOT"
  nohup "$SGLANG_BIN" "${fish_args[@]}" >"$fish_log" 2>&1 &
  echo $! > "$RUN_DIR/fish_speech.pid"
)

echo "[3/3] waiting service..."
if ! wait_fish_ready; then
  tail -n 160 "$fish_log" || true
  die "Fish Speech service not ready in ${FISH_WAIT_SEC}s"
fi

cat <<EOF
[ok] Fish Speech service ready
  API:  http://127.0.0.1:${FISH_PORT}/v1/audio/speech
  Docs: http://127.0.0.1:${FISH_PORT}/docs
  Log:  ${fish_log}
EOF
