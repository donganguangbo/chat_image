#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

HOST="${HOST:-0.0.0.0}"
WEB_PORT="${WEB_PORT:-6006}"
GO_PORT="${GO_PORT:-6008}"
COMFY_HOST="${COMFY_HOST:-0.0.0.0}"
COMFY_PORTS="${COMFY_PORTS:-8188 8189 8190}"

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
COMFY_WAIT_SEC="${COMFY_WAIT_SEC:-120}"
WEB_WAIT_SEC="${WEB_WAIT_SEC:-60}"
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

kill_by_port() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    mapfile -t pids < <(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
    if [[ "${#pids[@]}" -gt 0 ]]; then
      kill "${pids[@]}" 2>/dev/null || true
      sleep 1
      mapfile -t pids < <(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
      if [[ "${#pids[@]}" -gt 0 ]]; then
        kill -9 "${pids[@]}" 2>/dev/null || true
      fi
    fi
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
command -v "$GO_BIN" >/dev/null 2>&1 || die "Go not found: $GO_BIN"
if [[ "$COMFY_ISOLATED" == "1" ]]; then
  command -v rsync >/dev/null 2>&1 || die "rsync is required when COMFY_ISOLATED=1"
fi

BASE_VENV_DIR="$(cd "$(dirname "$PYTHON_BIN")/.." && pwd)"
[[ -x "$BASE_VENV_DIR/bin/python" ]] || die "Base venv invalid: $BASE_VENV_DIR/bin/python"

# Parse comfy ports
IFS=' ' read -r -a comfy_ports <<<"$COMFY_PORTS"
(( ${#comfy_ports[@]} >= 1 )) || die "COMFY_PORTS is empty"

# Default: web uses first comfy backend
WEB_COMFY_URL="${WEB_COMFY_URL:-http://127.0.0.1:${comfy_ports[0]}}"

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

echo "[1/7] stopping old processes..."
kill_by_pattern "[p]ython.*local_generation_gateway.py"
kill_by_pattern "[g]o run .*go_gateway"
kill_by_pattern "[g]o_gateway"
for p in "${comfy_ports[@]}"; do
  kill_by_pattern "[m]ain.py --listen .*--port ${p}"
done
kill_by_port "$WEB_PORT"
kill_by_port "$GO_PORT"
for p in "${comfy_ports[@]}"; do
  kill_by_port "$p"
done

if [[ "$COMFY_ISOLATED" == "1" ]]; then
  echo "[2/7] preparing isolated Comfy instances..."
  mkdir -p "$ISOLATED_ROOT"
  for p in "${comfy_ports[@]}"; do
    prepare_comfy_instance "$p"
  done
else
  echo "[2/7] isolated mode disabled (legacy shared Comfy root)"
fi

if [[ "$CONFIGURE_COMFY_PATHS" == "1" ]]; then
  echo "[3/7] configuring Comfy model paths..."
  if [[ "$COMFY_ISOLATED" == "1" ]]; then
    for p in "${comfy_ports[@]}"; do
      "$PYTHON_BIN" "$PROJECT_ROOT/scripts/configure_comfy_paths.py" \
        --models-root "$PROJECT_ROOT/models" \
        --comfy-root "${COMFY_APP_ROOTS[$p]}"
    done
  else
    "$PYTHON_BIN" "$PROJECT_ROOT/scripts/configure_comfy_paths.py" \
      --models-root "$PROJECT_ROOT/models" \
      --comfy-root "$COMFY_ROOT"
  fi
else
  echo "[3/7] skip model path configure"
fi

echo "[4/7] starting ComfyUI backends..."
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
      nohup "$PYTHON_BIN" -u main.py --listen "$COMFY_HOST" --port "$p" >"$log_file" 2>&1 &
      echo $! > "$RUN_DIR/comfy_${p}.pid"
    )
  fi
done

echo "[5/7] waiting ComfyUI health..."
for p in "${comfy_ports[@]}"; do
  wait_http_ok "http://127.0.0.1:${p}/system_stats" "$COMFY_WAIT_SEC" "comfy:${p}" || {
    tail -n 120 "$LOG_DIR/comfy_${p}_${timestamp}.log" || true
    die "comfy ${p} boot failed"
  }
done

echo "[6/7] starting web gateway (single comfy)..."
web_log="$LOG_DIR/web_${timestamp}.log"
nohup env HOST="$HOST" PORT="$WEB_PORT" COMFY_URL="$WEB_COMFY_URL" \
  "$PYTHON_BIN" -u "$PROJECT_ROOT/local_generation_gateway.py" >"$web_log" 2>&1 &
echo $! > "$RUN_DIR/web.pid"
wait_http_ok "http://127.0.0.1:${WEB_PORT}/healthz" "$WEB_WAIT_SEC" "web" || {
  tail -n 120 "$web_log" || true
  die "web gateway boot failed"
}

echo "[7/7] starting go gateway (multi comfy load-balance)..."
go_log="$LOG_DIR/go_gateway_${timestamp}.log"
nohup env LISTEN_ADDR=":${GO_PORT}" \
  WORKFLOW_CONFIG_DIR="$PROJECT_ROOT/go_gateway/configs/workflows" \
  OUTPUT_DIR="$PROJECT_ROOT/outputs" \
  COMFY_URLS="$GO_COMFY_URLS" \
  GO_WORKERS="$GO_WORKERS" \
  GO_GATEWAY_I2V_TRIM_HEAD_SEC="${GO_GATEWAY_I2V_TRIM_HEAD_SEC:-1}" \
  bash -lc "cd \"$PROJECT_ROOT/go_gateway\" && \"$GO_BIN\" run ." >"$go_log" 2>&1 &
echo $! > "$RUN_DIR/go_gateway.pid"
wait_http_ok "http://127.0.0.1:${GO_PORT}/healthz" "$GO_WAIT_SEC" "go_gateway" || {
  tail -n 120 "$go_log" || true
  die "go gateway boot failed"
}

echo ""
echo "[ok] all services started"
echo "Web UI:      http://127.0.0.1:${WEB_PORT}/web/"
echo "Go Health:   http://127.0.0.1:${GO_PORT}/healthz"
echo "Web->Comfy:  ${WEB_COMFY_URL}"
echo "Go->Comfys:  ${GO_COMFY_URLS}"
echo "Go Workers:  ${GO_WORKERS}"
echo "Isolated:    ${COMFY_ISOLATED} (${ISOLATED_ROOT})"
echo "Logs:        ${LOG_DIR}"
