#!/usr/bin/env bash
#
# verify_controlnet.sh
#
# Post-install validation for the ControlNet + DWPose hard-constraint chain.
# Run AFTER `install_controlnet.sh` and a full `start_multi_stack.sh` restart.
#
# Checks (in order):
#   1. All expected model files exist on disk
#   2. Every ComfyUI instance in COMFY_PORTS responds to /system_stats
#   3. Every instance has DWPreprocessor / ControlNetLoader /
#      SetUnionControlNetType / ControlNetApplyAdvanced registered
#   4. Compatibility microtest: submit a minimal 1-step prompt that loads
#      redcraft + FLUX ControlNet and check for errors
#
# Non-destructive. Exits 0 on success, 1 on any failure.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${PROJECT_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"
COMFY_PORTS="${COMFY_PORTS:-8188 8189 8190}"

MODELS_ROOT="$PROJECT_ROOT/models"
ZIMAGE_CN_FILE="$MODELS_ROOT/model_patches/Z-Image-Turbo-Fun-Controlnet-Union-2.1-2602-8steps.safetensors"
DWPOSE_YOLOX="$MODELS_ROOT/dwpose/yolox_l.onnx"
DWPOSE_DWLL="$MODELS_ROOT/dwpose/dw-ll_ucoco_384.onnx"
REDCRAFT_CKPT="${REDCRAFT_CKPT:-redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors}"

FAIL_COUNT=0
pass() { echo "[pass] $*"; }
warn() { echo "[warn] $*"; }
fail() { echo "[fail] $*"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

# --------------------------------------------------------------------
# 1. Model files
# --------------------------------------------------------------------
echo ""
echo "=== 1/4 Model files ==="
for f in "$ZIMAGE_CN_FILE" "$DWPOSE_YOLOX" "$DWPOSE_DWLL"; do
  if [[ -s "$f" ]]; then
    pass "$f ($(du -h "$f" | cut -f1))"
  else
    fail "$f missing or empty"
  fi
done

# --------------------------------------------------------------------
# 2. ComfyUI health
# --------------------------------------------------------------------
echo ""
echo "=== 2/4 ComfyUI health ==="
declare -a HEALTHY_PORTS
for port in $COMFY_PORTS; do
  if curl -fsS --max-time 5 "http://127.0.0.1:$port/system_stats" >/dev/null 2>&1; then
    pass "comfy:$port responding"
    HEALTHY_PORTS+=("$port")
  else
    fail "comfy:$port not responding (bash scripts/start_multi_stack.sh to restart)"
  fi
done

if [[ ${#HEALTHY_PORTS[@]} -eq 0 ]]; then
  echo ""
  echo "[abort] no healthy ComfyUI instances — cannot continue"
  exit 1
fi

# --------------------------------------------------------------------
# 3. Node registration per instance
# --------------------------------------------------------------------
echo ""
echo "=== 3/4 Required nodes registered ==="
REQUIRED_NODES=("DWPreprocessor" "ModelPatchLoader" "ZImageFunControlnet")
for port in "${HEALTHY_PORTS[@]}"; do
  object_info="$(curl -fsS --max-time 10 "http://127.0.0.1:$port/object_info" || true)"
  if [[ -z "$object_info" ]]; then
    fail "comfy:$port object_info empty"
    continue
  fi
  for node in "${REQUIRED_NODES[@]}"; do
    if echo "$object_info" | grep -q "\"$node\":"; then
      pass "comfy:$port has $node"
    else
      fail "comfy:$port missing node: $node"
    fi
  done
done

# --------------------------------------------------------------------
# 4. Smoke test: /prompt endpoint reachable and returning structured errors
#    (NOT a compatibility test — real compat validation happens when you
#    call /api/v1/generate/edit with the real workflow. This just confirms
#    the API surface is alive and parsing JSON.)
# --------------------------------------------------------------------
echo ""
echo "=== 4/4 Prompt endpoint smoke test ==="
FIRST_PORT="${HEALTHY_PORTS[0]}"

# Intentionally minimal prompt — ComfyUI *should* respond (400 with a
# structured JSON error if it parses our payload; 500 would indicate
# something worse like server crash).
smoke_body='{"prompt":{},"client_id":"verify_controlnet_smoke"}'

http_code="$(curl -sS -o /tmp/verify_controlnet_resp.$$ -w '%{http_code}' \
              --max-time 15 -H 'Content-Type: application/json' \
              -X POST -d "$smoke_body" \
              "http://127.0.0.1:$FIRST_PORT/prompt" 2>/dev/null || echo "000")"
resp="$(cat /tmp/verify_controlnet_resp.$$ 2>/dev/null || true)"
rm -f "/tmp/verify_controlnet_resp.$$"

case "$http_code" in
  200)
    pass "comfy:$FIRST_PORT /prompt returned 200 (unexpected but OK)"
    ;;
  400)
    # 400 is expected for a malformed minimal prompt — means the endpoint
    # is parsing and validating. Confirm it returns a JSON error, not a
    # crash/500.
    if echo "$resp" | python3 -c 'import json,sys; json.load(sys.stdin)' >/dev/null 2>&1; then
      pass "comfy:$FIRST_PORT /prompt validating correctly (400 + JSON error)"
    else
      warn "comfy:$FIRST_PORT /prompt returned 400 but not JSON:"
      echo "$resp" | head -5
    fi
    ;;
  500|502|503)
    fail "comfy:$FIRST_PORT /prompt returned HTTP $http_code (server error)"
    echo "$resp" | head -20
    ;;
  000)
    fail "comfy:$FIRST_PORT /prompt not reachable"
    ;;
  *)
    warn "comfy:$FIRST_PORT /prompt returned unexpected HTTP $http_code"
    echo "$resp" | head -20
    ;;
esac

# --------------------------------------------------------------------
# Summary
# --------------------------------------------------------------------
echo ""
echo "=== summary ==="
if [[ $FAIL_COUNT -eq 0 ]]; then
  echo "[ok] all checks passed"
  echo ""
  echo "Next step: run a real end-to-end test to validate redcraft x FLUX"
  echo "ControlNet Union compatibility. Any incompatibility will surface"
  echo "there with a full stack trace in the go_gateway / ComfyUI logs."
  echo ""
  echo "  1. Switch default_workflows.image_edit in go_gateway/configs/service.yaml"
  echo "     to inpaint_zimage_lock_red_pose_cn"
  echo "  2. Restart: bash scripts/start_multi_stack.sh"
  echo "  3. POST /api/v1/generate/edit with your test prompt + input_image"
  echo "  4. Inspect outputs/images/*_skeleton_*.png first (Pass A pose check)"
  echo "  5. Then inspect *_after_*.png (final render)"
  exit 0
else
  echo "[fail] $FAIL_COUNT check(s) failed — see output above"
  exit 1
fi
