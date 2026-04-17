#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MODELS_ROOT="${PROJECT_ROOT}/models"
ASSETS_ROOT="${PROJECT_ROOT}/bundles/zimage_type_assets"
SKIP_WORKFLOWS=0
SKIP_MODELS=0
SKIP_CORE=0
COMMUNITY_WORKFLOWS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --models-root)
      MODELS_ROOT="$2"
      shift 2
      ;;
    --assets-root)
      ASSETS_ROOT="$2"
      shift 2
      ;;
    --skip-workflows)
      SKIP_WORKFLOWS=1
      shift
      ;;
    --skip-models)
      SKIP_MODELS=1
      shift
      ;;
    --skip-core)
      SKIP_CORE=1
      shift
      ;;
    --community-workflows)
      COMMUNITY_WORKFLOWS=1
      shift
      ;;
    *)
      echo "Unknown arg: $1" >&2
      exit 2
      ;;
  esac
done

MODELS_ROOT="$(cd "$(dirname "${MODELS_ROOT}")" && pwd)/$(basename "${MODELS_ROOT}")"
ASSETS_ROOT="$(cd "$(dirname "${ASSETS_ROOT}")" && pwd)/$(basename "${ASSETS_ROOT}")"

CIVITAI_TOKEN="${CIVITAI_TOKEN:-}"
HF_TOKEN="${HF_TOKEN:-${HUGGINGFACE_HUB_TOKEN:-}}"

WORKFLOW_RAW_DIR="${ASSETS_ROOT}/workflows_raw"
WORKFLOW_EXTRACT_DIR="${ASSETS_ROOT}/workflows_extracted"
WORKFLOW_READY_DIR="${ASSETS_ROOT}/workflows_ready"
PROJECT_WORKFLOWS_DIR="${PROJECT_ROOT}/workflows"

mkdir -p \
  "${WORKFLOW_READY_DIR}" \
  "${MODELS_ROOT}/diffusion_models" \
  "${MODELS_ROOT}/checkpoints" \
  "${MODELS_ROOT}/loras" \
  "${MODELS_ROOT}/text_encoders" \
  "${MODELS_ROOT}/vae"

if [[ "${COMMUNITY_WORKFLOWS}" -eq 1 ]]; then
  mkdir -p "${WORKFLOW_RAW_DIR}" "${WORKFLOW_EXTRACT_DIR}"
fi

add_civitai_token() {
  local url="$1"
  if [[ "${url}" != *"civitai.com"* ]]; then
    echo "${url}"
    return
  fi
  if [[ -z "${CIVITAI_TOKEN}" ]]; then
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
  local final_url
  final_url="$(add_civitai_token "${url}")"

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
    "${final_url}"
    -o "${tmp}"
  )
  if [[ -n "${HF_TOKEN}" && "${final_url}" == *"huggingface.co"* ]]; then
    curl_args=(-H "Authorization: Bearer ${HF_TOKEN}" "${curl_args[@]}")
  fi

  if ! curl "${curl_args[@]}"; then
    echo "[error] failed: ${out}" >&2
    if [[ "${final_url}" == *"civitai.com"* && -z "${CIVITAI_TOKEN}" ]]; then
      echo "        CIVITAI_TOKEN is empty. Civitai may return 401 for this asset." >&2
    fi
    rm -f "${tmp}"
    return 1
  fi

  mv -f "${tmp}" "${out}"
  ls -lh "${out}"
}

extract_zip() {
  local zip_path="$1"
  local dir_name="$2"
  local target_dir="${WORKFLOW_EXTRACT_DIR}/${dir_name}"
  rm -rf "${target_dir}"
  mkdir -p "${target_dir}"
  if command -v unzip >/dev/null 2>&1; then
    unzip -oq "${zip_path}" -d "${target_dir}"
  else
    echo "[warn] unzip not found, keep raw archive only: ${zip_path}"
  fi
}

pick_first_json() {
  local dir="$1"
  find "${dir}" -type f -name "*.json" | head -n 1
}

copy_if_exists() {
  local src="$1"
  local dst="$2"
  if [[ -f "${src}" ]]; then
    cp -f "${src}" "${dst}"
    echo "[ready] ${dst}"
  fi
}

if [[ "${SKIP_WORKFLOWS}" -eq 0 ]]; then
  if [[ "${COMMUNITY_WORKFLOWS}" -eq 1 ]]; then
    echo ""
    echo "== Download workflows (Civitai archives) =="

    # Curated workflow packs (with pinned version IDs)
    WF_NORMAL_T2I_URL="https://civitai.com/api/download/models/2786477"
    WF_FUTA_T2I_URL="https://civitai.com/api/download/models/2758560"
    WF_FURRY_T2I_URL="https://civitai.com/api/download/models/2820672"
    WF_LOCK_URL="https://civitai.com/api/download/models/2786372"

    WF_NORMAL_T2I_ZIP="${WORKFLOW_RAW_DIR}/normal_t2i_moody_zib_zit_v4.zip"
    WF_FUTA_T2I_ZIP="${WORKFLOW_RAW_DIR}/futa_t2i_extreme_aesthetics_v1.zip"
    WF_FURRY_T2I_ZIP="${WORKFLOW_RAW_DIR}/furry_t2i_turbo_essentials_v4.zip"
    WF_NORMAL_LOCK_ZIP="${WORKFLOW_RAW_DIR}/normal_lock_character_head_swap_v1.zip"
    WF_FUTA_LOCK_ZIP="${WORKFLOW_RAW_DIR}/futa_lock_character_head_swap_v1.zip"
    WF_FURRY_LOCK_ZIP="${WORKFLOW_RAW_DIR}/furry_lock_character_head_swap_v1.zip"

    download_file "${WF_NORMAL_T2I_URL}" "${WF_NORMAL_T2I_ZIP}"
    download_file "${WF_FUTA_T2I_URL}" "${WF_FUTA_T2I_ZIP}"
    download_file "${WF_FURRY_T2I_URL}" "${WF_FURRY_T2I_ZIP}"
    download_file "${WF_LOCK_URL}" "${WF_NORMAL_LOCK_ZIP}"
    cp -f "${WF_NORMAL_LOCK_ZIP}" "${WF_FUTA_LOCK_ZIP}"
    cp -f "${WF_NORMAL_LOCK_ZIP}" "${WF_FURRY_LOCK_ZIP}"

    extract_zip "${WF_NORMAL_T2I_ZIP}" "normal_t2i"
    extract_zip "${WF_FUTA_T2I_ZIP}" "futa_t2i"
    extract_zip "${WF_FURRY_T2I_ZIP}" "furry_t2i"
    extract_zip "${WF_NORMAL_LOCK_ZIP}" "normal_lock"
    extract_zip "${WF_FUTA_LOCK_ZIP}" "futa_lock"
    extract_zip "${WF_FURRY_LOCK_ZIP}" "furry_lock"
  else
    echo ""
    echo "== Skip community workflow archives (default) =="
    echo "Use --community-workflows to download Civitai workflow zips."
  fi
fi

if [[ "${SKIP_MODELS}" -eq 0 ]]; then
  echo ""
  echo "== Download models =="

  # HF (shared text encoder + VAE)
  HF_QWEN_URL="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/text_encoders/qwen_3_4b.safetensors"
  HF_AE_URL="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors"
  download_file "${HF_QWEN_URL}" "${MODELS_ROOT}/text_encoders/qwen_3_4b.safetensors"
  download_file "${HF_AE_URL}" "${MODELS_ROOT}/vae/ae.safetensors"

  # Type-specific checkpoints / loras
  FUTA_CKPT_URL="https://civitai.com/api/download/models/2642303"
  FURRY_CKPT_URL="https://civitai.com/api/download/models/2535848"
  FURRY_LORA_1_URL="https://civitai.com/api/download/models/2566602"
  FURRY_LORA_2_URL="https://civitai.com/api/download/models/2479919"

  download_file "${FUTA_CKPT_URL}" "${MODELS_ROOT}/checkpoints/zimageTurboNSFWBy_2602NSFWFP8.safetensors"
  download_file "${FURRY_CKPT_URL}" "${MODELS_ROOT}/checkpoints/ratatoskr_zimage_v1_bf16.safetensors"
  download_file "${FURRY_LORA_1_URL}" "${MODELS_ROOT}/loras/willysZITAnthro_Experimental_v0.1.safetensors"
  download_file "${FURRY_LORA_2_URL}" "${MODELS_ROOT}/loras/BoerBara.safetensors"

  if [[ "${SKIP_CORE}" -eq 0 ]]; then
    echo ""
    echo "== Download core (current runtime stack) =="
    REDCRAFT_CORE_URL="${REDCRAFT_CORE_URL:-https://civitai.com/api/download/models/2680424?type=Model&format=SafeTensor&size=full&fp=fp16}"
    DARKBEAST_CORE_URL="${DARKBEAST_CORE_URL:-}"
    REDZ15_URL="${REDZ15_URL:-}"

    download_file "${REDCRAFT_CORE_URL}" "${MODELS_ROOT}/diffusion_models/redcraftFeb1126Latest_zibDistilledDX3Lucis.safetensors"

    if [[ -n "${DARKBEAST_CORE_URL}" ]]; then
      download_file "${DARKBEAST_CORE_URL}" "${MODELS_ROOT}/diffusion_models/darkbeastz6BlitzBF16.Vii6.safetensors"
    else
      echo "[skip] DARKBEAST_CORE_URL is empty, skip darkbeastz6 download."
    fi

    if [[ -n "${REDZ15_URL}" ]]; then
      download_file "${REDZ15_URL}" "${MODELS_ROOT}/loras/REDZ15_DetailDaemonZ_lora_v1.1.safetensors"
    else
      echo "[skip] REDZ15_URL is empty, skip REDZ15 download."
    fi
  fi
fi

echo ""
echo "== Prepare ready-to-use workflow files =="

# Gateway-compatible runtime workflows.
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/t2i_zimage_darkbeast.json" "${WORKFLOW_READY_DIR}/t2i_zimage_darkbeast.json"
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/t2i_zimage_darkbeast_futa.json" "${WORKFLOW_READY_DIR}/t2i_zimage_darkbeast_futa.json"
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/t2i_zimage_darkbeast_furry.json" "${WORKFLOW_READY_DIR}/t2i_zimage_darkbeast_furry.json"
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/inpaint_zimage_lock.json" "${WORKFLOW_READY_DIR}/inpaint_zimage_lock.json"
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/inpaint_zimage_lock_futa.json" "${WORKFLOW_READY_DIR}/inpaint_zimage_lock_futa.json"
copy_if_exists "${PROJECT_WORKFLOWS_DIR}/inpaint_zimage_lock_furry.json" "${WORKFLOW_READY_DIR}/inpaint_zimage_lock_furry.json"

# Keep one extracted community workflow json per type for reference.
if [[ "${COMMUNITY_WORKFLOWS}" -eq 1 ]]; then
  if [[ -d "${WORKFLOW_EXTRACT_DIR}/normal_t2i" ]]; then
    src="$(pick_first_json "${WORKFLOW_EXTRACT_DIR}/normal_t2i" || true)"
    [[ -n "${src:-}" ]] && copy_if_exists "${src}" "${WORKFLOW_READY_DIR}/community_normal_t2i.json"
  fi
  if [[ -d "${WORKFLOW_EXTRACT_DIR}/futa_t2i" ]]; then
    src="$(pick_first_json "${WORKFLOW_EXTRACT_DIR}/futa_t2i" || true)"
    [[ -n "${src:-}" ]] && copy_if_exists "${src}" "${WORKFLOW_READY_DIR}/community_futa_t2i.json"
  fi
  if [[ -d "${WORKFLOW_EXTRACT_DIR}/furry_t2i" ]]; then
    src="$(pick_first_json "${WORKFLOW_EXTRACT_DIR}/furry_t2i" || true)"
    [[ -n "${src:-}" ]] && copy_if_exists "${src}" "${WORKFLOW_READY_DIR}/community_furry_t2i.json"
  fi
  if [[ -d "${WORKFLOW_EXTRACT_DIR}/normal_lock" ]]; then
    src="$(pick_first_json "${WORKFLOW_EXTRACT_DIR}/normal_lock" || true)"
    [[ -n "${src:-}" ]] && copy_if_exists "${src}" "${WORKFLOW_READY_DIR}/community_normal_lock.json"
  fi
fi

echo ""
echo "[done] zimage type assets downloaded."
if [[ "${COMMUNITY_WORKFLOWS}" -eq 1 ]]; then
  echo "workflows(raw): ${WORKFLOW_RAW_DIR}"
  echo "workflows(extracted): ${WORKFLOW_EXTRACT_DIR}"
fi
echo "workflows(ready): ${WORKFLOW_READY_DIR}"
echo "Install ready workflows to project:"
echo "  cp -f ${WORKFLOW_READY_DIR}/*.json ${PROJECT_WORKFLOWS_DIR}/"
echo "models: ${MODELS_ROOT}"
