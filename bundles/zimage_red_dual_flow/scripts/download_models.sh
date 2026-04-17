#!/usr/bin/env bash
set -euo pipefail

# Red dual-flow model downloader
# Usage:
#   bash scripts/download_models.sh /root/autodl-tmp/projects/chat_image/models
#
# Optional env:
#   HF_TOKEN=...
#   CIVITAI_TOKEN=...
#   MAIN_ONLY=1
#   RED_CKPT_URL=...                # optional override
#   RED_SIDE_LORA_AUX_URL=...       # optional override for ZPenisHelper
#   RED_SIDE_LORA_MAIN_URL=...      # required when side-lora file not pre-uploaded

MODELS_ROOT="${1:-${MODELS_ROOT:-$(pwd)/models}}"
HF_TOKEN="${HF_TOKEN:-${HUGGINGFACE_HUB_TOKEN:-}}"
CIVITAI_TOKEN="${CIVITAI_TOKEN:-}"
MAIN_ONLY="${MAIN_ONLY:-0}"

# Default: Red full checkpoint (you can override by RED_CKPT_URL)
RED_CKPT_URL="${RED_CKPT_URL:-https://civitai.com/api/download/models/2462789?type=Model&format=SafeTensor&size=full&fp=bf16}"
# Default: known HF mirror for ZPenisHelper
RED_SIDE_LORA_AUX_URL="${RED_SIDE_LORA_AUX_URL:-https://huggingface.co/andrewwe/zitLoras/resolve/main/ZPenisHelper.safetensors}"
# No stable public default guaranteed for main side-lora; keep override-friendly.
RED_SIDE_LORA_MAIN_URL="${RED_SIDE_LORA_MAIN_URL:-}"

UPSCALE_URL="https://huggingface.co/skbhadra/ClearRealityV1/resolve/main/4x-ClearRealityV1.pth"

RED_CKPT_PATH="${MODELS_ROOT}/checkpoints/redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors"
UPSCALE_PATH="${MODELS_ROOT}/upscale_models/4x-ClearRealityV1.pth"
RED_SIDE_LORA_AUX_PATH="${MODELS_ROOT}/loras/ZPenisHelper.safetensors"
RED_SIDE_LORA_MAIN_PATH="${MODELS_ROOT}/loras/REDZimage1.5_extracted_lora_rank_128.safetensors"

mkdir -p \
  "${MODELS_ROOT}/checkpoints" \
  "${MODELS_ROOT}/upscale_models" \
  "${MODELS_ROOT}/loras"

echo "Models root: ${MODELS_ROOT}"

add_civitai_token() {
  local url="$1"
  if [[ "${url}" != *"civitai.com"* || -z "${CIVITAI_TOKEN}" ]]; then
    echo "${url}"
    return
  fi
  if [[ "${url}" == *"token="* ]]; then
    echo "${url}"
  elif [[ "${url}" == *"?"* ]]; then
    echo "${url}&token=${CIVITAI_TOKEN}"
  else
    echo "${url}?token=${CIVITAI_TOKEN}"
  fi
}

download_file() {
  local raw_url="$1"
  local out="$2"
  local tmp="${out}.part"
  local url
  url="$(add_civitai_token "${raw_url}")"

  if [[ -s "${out}" ]]; then
    echo "[ok] exists: ${out}"
    return 0
  fi

  echo "[download] ${out}"

  local -a curl_args
  curl_args=(
    -fL
    --retry 8
    --retry-delay 3
    --retry-all-errors
    -C -
    "${url}"
    -o "${tmp}"
  )
  if [[ -n "${HF_TOKEN}" && "${url}" == *"huggingface.co"* ]]; then
    curl_args=(-H "Authorization: Bearer ${HF_TOKEN}" "${curl_args[@]}")
  fi

  curl "${curl_args[@]}"
  mv -f "${tmp}" "${out}"
  ls -lh "${out}"
}

missing=0

# required for both main/side
if [[ -n "${RED_CKPT_URL}" ]]; then
  download_file "${RED_CKPT_URL}" "${RED_CKPT_PATH}" || missing=1
else
  [[ -s "${RED_CKPT_PATH}" ]] || { echo "[missing] ${RED_CKPT_PATH}"; missing=1; }
fi
download_file "${UPSCALE_URL}" "${UPSCALE_PATH}" || missing=1

# side-only models
if [[ "${MAIN_ONLY}" != "1" ]]; then
  if [[ -n "${RED_SIDE_LORA_AUX_URL}" ]]; then
    download_file "${RED_SIDE_LORA_AUX_URL}" "${RED_SIDE_LORA_AUX_PATH}" || missing=1
  else
    [[ -s "${RED_SIDE_LORA_AUX_PATH}" ]] || { echo "[missing] ${RED_SIDE_LORA_AUX_PATH}"; missing=1; }
  fi

  if [[ -n "${RED_SIDE_LORA_MAIN_URL}" ]]; then
    download_file "${RED_SIDE_LORA_MAIN_URL}" "${RED_SIDE_LORA_MAIN_PATH}" || missing=1
  else
    [[ -s "${RED_SIDE_LORA_MAIN_PATH}" ]] || {
      echo "[missing] ${RED_SIDE_LORA_MAIN_PATH}"
      echo "[hint] set RED_SIDE_LORA_MAIN_URL or upload this file manually."
      missing=1
    }
  fi
fi

if [[ "${missing}" -ne 0 ]]; then
  echo ""
  echo "[error] model set incomplete."
  exit 2
fi

echo ""
echo "[done] Red dual-flow models ready."
