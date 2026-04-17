#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   1) If core models already uploaded, just run:
#      bash download_models.sh /path/to/models
#   2) Or provide URLs for missing core models:
#      REDCRAFT_URL='...' DARKBEAST_URL='...' REDZ15_LORA_URL='...' \
#      CIVITAI_TOKEN='...' HF_TOKEN='...' bash download_models.sh /path/to/models

MODELS_ROOT="${1:-${MODELS_ROOT:-$(pwd)/models}}"
HF_TOKEN="${HF_TOKEN:-${HUGGINGFACE_HUB_TOKEN:-}}"
CIVITAI_TOKEN="${CIVITAI_TOKEN:-}"

# Optional URLs for the 3 core models (can be Civitai/HF/direct links).
REDCRAFT_URL="${REDCRAFT_URL:-}"
DARKBEAST_URL="${DARKBEAST_URL:-}"
REDZ15_LORA_URL="${REDZ15_LORA_URL:-}"

HF_QWEN_URL="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/text_encoders/qwen_3_4b.safetensors"
HF_AE_URL="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors"

REDCRAFT_PATH="${MODELS_ROOT}/diffusion_models/redcraftFeb1126Latest_zibDistilledDX3Lucis.safetensors"
DARKBEAST_PATH="${MODELS_ROOT}/diffusion_models/darkbeastz6BlitzBF16.Vii6.safetensors"
REDZ15_LORA_PATH="${MODELS_ROOT}/loras/REDZ15_DetailDaemonZ_lora_v1.1.safetensors"
QWEN_PATH="${MODELS_ROOT}/text_encoders/qwen_3_4b.safetensors"
AE_PATH="${MODELS_ROOT}/vae/ae.safetensors"

echo "Models root: ${MODELS_ROOT}"
mkdir -p \
  "${MODELS_ROOT}/diffusion_models" \
  "${MODELS_ROOT}/text_encoders" \
  "${MODELS_ROOT}/vae" \
  "${MODELS_ROOT}/loras"

add_civitai_token() {
  local url="$1"
  if [[ -z "${url}" ]]; then
    echo ""
    return
  fi
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
  local url="$1"
  local out="$2"
  local tmp="${out}.part"
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

ensure_or_download() {
  local out="$1"
  local url="$2"
  if [[ -s "${out}" ]]; then
    echo "[ok] exists: ${out}"
    return 0
  fi
  if [[ -z "${url}" ]]; then
    echo "[missing] ${out}"
    return 1
  fi
  download_file "$(add_civitai_token "${url}")" "${out}"
}

# Always auto-download lightweight/official components.
download_file "${HF_QWEN_URL}" "${QWEN_PATH}"
download_file "${HF_AE_URL}" "${AE_PATH}"

# Core RedCraft/DarkBeast/REDZ15 can be pre-uploaded, or provided via URLs.
missing=0
ensure_or_download "${REDCRAFT_PATH}" "${REDCRAFT_URL}" || missing=1
ensure_or_download "${DARKBEAST_PATH}" "${DARKBEAST_URL}" || missing=1
ensure_or_download "${REDZ15_LORA_PATH}" "${REDZ15_LORA_URL}" || missing=1

if [[ ${missing} -ne 0 ]]; then
  echo ""
  echo "[error] Missing required core models."
  echo "Please either:"
  echo "  1) Upload these files manually:"
  echo "     - ${REDCRAFT_PATH}"
  echo "     - ${DARKBEAST_PATH}"
  echo "     - ${REDZ15_LORA_PATH}"
  echo "  2) Or rerun with URLs:"
  echo "     REDCRAFT_URL='...' DARKBEAST_URL='...' REDZ15_LORA_URL='...' bash download_models.sh ${MODELS_ROOT}"
  exit 2
fi

echo ""
echo "[done] zimage_edit (redcraft + darkbeastz6) model set is ready."
