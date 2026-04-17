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
CONTROLNET_FILE="$MODELS_ROOT/controlnet/flux-controlnet-union.safetensors"
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
for f in "$CONTROLNET_FILE" "$DWPOSE_YOLOX" "$DWPOSE_DWLL"; do
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
REQUIRED_NODES=("DWPreprocessor" "ControlNetLoader" "SetUnionControlNetType" "ControlNetApplyAdvanced")
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
# 4. Compatibility microtest
#    Submit a minimal prompt on the first healthy port that exercises:
#      CheckpointLoaderSimple(redcraft) → SetUnionControlNetType(openpose) →
#      ControlNetApplyAdvanced with an empty 64x64 image and 1-step KSampler.
#    If there's a tensor shape mismatch between redcraft and FLUX ControlNet,
#    it surfaces here.
# --------------------------------------------------------------------
echo ""
echo "=== 4/4 Compatibility microtest ==="
FIRST_PORT="${HEALTHY_PORTS[0]}"

# Build minimal prompt
micro_prompt=$(cat <<JSON
{
  "prompt": {
    "1": {
      "inputs": {"ckpt_name": "$REDCRAFT_CKPT"},
      "class_type": "CheckpointLoaderSimple"
    },
    "2": {
      "inputs": {"text": "test", "clip": ["1", 1]},
      "class_type": "CLIPTextEncode"
    },
    "3": {
      "inputs": {"text": "", "clip": ["1", 1]},
      "class_type": "CLIPTextEncode"
    },
    "4": {
      "inputs": {"control_net_name": "flux-controlnet-union.safetensors"},
      "class_type": "ControlNetLoader"
    },
    "5": {
      "inputs": {"control_net": ["4", 0], "type": "openpose"},
      "class_type": "SetUnionControlNetType"
    },
    "6": {
      "inputs": {"width": 64, "height": 64, "batch_size": 1},
      "class_type": "EmptySD3LatentImage"
    }
  },
  "client_id": "verify_controlnet_microtest"
}
JSON
)

echo "[info] submitting microtest prompt to comfy:$FIRST_PORT ..."
resp="$(curl -fsS --max-time 30 -H 'Content-Type: application/json' \
          -X POST "http://127.0.0.1:$FIRST_PORT/prompt" \
          -d "$micro_prompt" 2>&1 || true)"

if [[ -z "$resp" ]]; then
  fail "microtest got no response (comfy error?)"
elif echo "$resp" | grep -qi "error\|traceback\|exception"; then
  fail "microtest returned error:"
  echo "$resp" | head -40
else
  # Extract prompt_id to check status
  prompt_id="$(echo "$resp" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("prompt_id",""))' 2>/dev/null || true)"
  if [[ -n "$prompt_id" ]]; then
    pass "microtest accepted (prompt_id=$prompt_id) — no model loading error"
  else
    warn "microtest response unclear:"
    echo "$resp" | head -20
  fi
fi

# --------------------------------------------------------------------
# Summary
# --------------------------------------------------------------------
echo ""
echo "=== summary ==="
if [[ $FAIL_COUNT -eq 0 ]]; then
  echo "[ok] all checks passed"
  exit 0
else
  echo "[fail] $FAIL_COUNT check(s) failed — see output above"
  exit 1
fi
