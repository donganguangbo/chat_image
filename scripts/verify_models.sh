#!/usr/bin/env bash
# Verify required model files exist (and are non-empty). Replaces verify_models.py.
#
# Usage: verify_models.sh [--profile all|zimage|...] [--models-root DIR] [--comfy-root DIR]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/models_manifest.sh
source "$SCRIPT_DIR/models_manifest.sh"

PROFILE="all"
MODELS_ROOT=""
COMFY_ROOT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)     PROFILE="$2"; shift 2 ;;
    --models-root) MODELS_ROOT="$2"; shift 2 ;;
    --comfy-root)  COMFY_ROOT="$2"; shift 2 ;;
    *) echo "Ignoring unknown args for verify: $1" >&2; shift ;;
  esac
done

if [[ -n "$MODELS_ROOT" ]]; then
  :
elif [[ -n "$COMFY_ROOT" ]]; then
  MODELS_ROOT="$COMFY_ROOT/models"
else
  MODELS_ROOT="$PROJECT_ROOT/models"
fi
# Resolve to absolute when the dir exists; otherwise report as-is.
[[ -d "$MODELS_ROOT" ]] && MODELS_ROOT="$(cd "$MODELS_ROOT" && pwd)"

mapfile -t SPECS < <(select_specs "$PROFILE")

echo "Checking profile=$PROFILE at $MODELS_ROOT"
missing=0
for spec in "${SPECS[@]}"; do
  IFS='|' read -r _tags subdir name _gated _url <<<"$spec"
  target="$MODELS_ROOT/$subdir/$name"
  if [[ -f "$target" && -s "$target" ]]; then
    echo "[OK]      $target ($(wc -c <"$target" | tr -d ' ') bytes)"
  else
    echo "[MISSING] $target"
    missing=$((missing + 1))
  fi
done

echo ""
if [[ "$missing" -gt 0 ]]; then
  echo "Missing files: $missing"
  exit 1
fi
echo "All required files are present."
