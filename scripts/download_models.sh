#!/usr/bin/env bash
# Download model files into ./models via curl (resumable), then point ComfyUI's
# extra_model_paths.yaml at them. Pure-shell replacement for download_models.py.
#
# Usage: download_models.sh [--profile all|zimage|image|video|flux|wan|t2v|i2v]
#                           [--models-root DIR] [--comfy-root DIR] [--config-name KEY]
#                           [--skip-comfy-config] [--force] [--dry-run]
# HF gated repos: export HF_TOKEN (or HUGGINGFACE_HUB_TOKEN) after accepting terms.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/models_manifest.sh
source "$SCRIPT_DIR/models_manifest.sh"

PROFILE="all"
MODELS_ROOT="${MODELS_ROOT:-$PROJECT_ROOT/models}"
COMFY_ROOT="${COMFYUI_ROOT:-$HOME/ComfyUI}"
CONFIG_NAME="${COMFY_EXTRA_CONFIG_NAME:-chat_image_models}"
HF_TOKEN="${HF_TOKEN:-${HUGGINGFACE_HUB_TOKEN:-}}"
SKIP_COMFY_CONFIG=0
FORCE=0
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)            PROFILE="$2"; shift 2 ;;
    --models-root)        MODELS_ROOT="$2"; shift 2 ;;
    --comfy-root)         COMFY_ROOT="$2"; shift 2 ;;
    --config-name)        CONFIG_NAME="$2"; shift 2 ;;
    --token)              HF_TOKEN="$2"; shift 2 ;;
    --skip-comfy-config)  SKIP_COMFY_CONFIG=1; shift ;;
    --force)              FORCE=1; shift ;;
    --dry-run)            DRY_RUN=1; shift ;;
    *) echo "[download_models] ignoring unknown arg: $1" >&2; shift ;;
  esac
done

mapfile -t SPECS < <(select_specs "$PROFILE")
if [[ "${#SPECS[@]}" -eq 0 ]]; then
  echo "No model files selected for profile=$PROFILE"
  exit 0
fi

mkdir -p "$MODELS_ROOT"
MODELS_ROOT="$(cd "$MODELS_ROOT" && pwd)"

echo "Models root: $MODELS_ROOT"
echo "Profile    : $PROFILE"
echo "Total files: ${#SPECS[@]}"
for spec in "${SPECS[@]}"; do
  IFS='|' read -r _tags subdir name gated url <<<"$spec"
  g=""; [[ "$gated" == "1" ]] && g=" (gated)"
  echo "- ${subdir}/${name}${g}"
done
if [[ "$SKIP_COMFY_CONFIG" -eq 1 ]]; then
  echo "ComfyUI config: skipped"
else
  echo "ComfyUI config: $COMFY_ROOT/extra_model_paths.yaml (key=$CONFIG_NAME)"
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "Dry-run: nothing downloaded."
  exit 0
fi

# Resumable HF download (same pattern as download_zimage_type_assets.sh).
download_file() {
  local url="$1" out="$2" gated="$3"
  local tmp="${out}.part"
  if [[ -s "$out" && "$FORCE" -ne 1 ]]; then
    echo "  skip (exists, $(wc -c <"$out" | tr -d ' ') bytes)"
    return 0
  fi
  mkdir -p "$(dirname "$out")"
  local -a curl_args=(-fL --retry 8 --retry-delay 3 --retry-all-errors -C - "$url" -o "$tmp")
  if [[ -n "$HF_TOKEN" && "$url" == *"huggingface.co"* ]]; then
    curl_args=(-H "Authorization: Bearer ${HF_TOKEN}" "${curl_args[@]}")
  fi
  if ! curl "${curl_args[@]}"; then
    rm -f "$tmp"
    if [[ "$gated" == "1" ]]; then
      echo "  failed (gated repo; accept terms on the model page and export HF_TOKEN)" >&2
    else
      echo "  failed (download error)" >&2
    fi
    return 1
  fi
  mv -f "$tmp" "$out"
  echo "  downloaded ($(wc -c <"$out" | tr -d ' ') bytes)"
}

success=0
failed=0
idx=0
for spec in "${SPECS[@]}"; do
  idx=$((idx + 1))
  IFS='|' read -r _tags subdir name gated url <<<"$spec"
  target="$MODELS_ROOT/$subdir/$name"
  echo "[$idx/${#SPECS[@]}] $subdir/$name"
  echo "  -> $target"
  if download_file "$url" "$target" "$gated"; then
    success=$((success + 1))
  else
    failed=$((failed + 1))
  fi
done

if [[ "$SKIP_COMFY_CONFIG" -ne 1 ]]; then
  "$SCRIPT_DIR/configure_comfy_paths.sh" \
    --models-root "$MODELS_ROOT" \
    --comfy-root "$COMFY_ROOT" \
    --config-name "$CONFIG_NAME"
fi

echo ""
echo "Done. success=$success, failed=$failed, profile=$PROFILE"
if [[ "$failed" -gt 0 ]]; then
  echo "Tip: for gated models, accept the model terms on Hugging Face and export HF_TOKEN."
  exit 1
fi
