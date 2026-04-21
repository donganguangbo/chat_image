#!/usr/bin/env bash
#
# Fetch WAN 2.2 i2v NSFW LoRA set from HuggingFace into ComfyUI's models/loras dir.
#
# Usage:
#   ./fetch_video_loras.sh [TARGET_DIR]
#
# If TARGET_DIR omitted, falls back to env COMFY_LORA_DIR, then to ~/ComfyUI/models/loras.
#
# Auth: if the repo is gated, export HF_TOKEN=hf_xxx before running.
#
# Requires: huggingface_hub client.
#   pip install -U huggingface_hub
#
set -euo pipefail

REPO="lkzd7/WAN2.2_LoraSet_NSFW"
TARGET_DIR="${1:-${COMFY_LORA_DIR:-$HOME/ComfyUI/models/loras}}"

echo "==> Repo:   ${REPO}"
echo "==> Target: ${TARGET_DIR}"
mkdir -p "${TARGET_DIR}"

# Prefer the new `hf` command; fall back to the legacy `huggingface-cli`.
if command -v hf >/dev/null 2>&1; then
  HF_CMD=(hf download)
elif command -v huggingface-cli >/dev/null 2>&1; then
  HF_CMD=(huggingface-cli download)
else
  echo "ERROR: neither 'hf' nor 'huggingface-cli' is installed." >&2
  echo "       Install it first: pip install -U huggingface_hub" >&2
  exit 1
fi

echo "==> Downloading (this pulls the full repo; safetensors only)..."
"${HF_CMD[@]}" "${REPO}" \
  --local-dir "${TARGET_DIR}" \
  --include "*.safetensors" \
  --repo-type model

echo ""
echo "==> LoRA files now in ${TARGET_DIR}:"
# Portable (macOS + Linux) file listing
find "${TARGET_DIR}" -maxdepth 3 -type f -name '*.safetensors' \
  | sed 's|^.*/||' \
  | sort

echo ""
echo "==> Done."
echo "    Next: populate VIDEO_LORA_ROSTER_JSON in the chat app's .env.local"
echo "    using the filenames above + design-doc section 10.4 as a template."
