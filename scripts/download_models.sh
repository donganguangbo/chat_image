#!/usr/bin/env bash
# Download every model a fresh deployment needs into ./models, laid out the way
# ComfyUI expects (see docker/comfy/extra_model_paths.yaml). Resumable: rerun it
# after an interrupted transfer and it picks up where it left off.
#
#   bash scripts/download_models.sh                  # everything (~156 GB)
#   bash scripts/download_models.sh --profile t2i    # one pipeline only
#   bash scripts/download_models.sh --check          # report what's missing
#   bash scripts/download_models.sh --list           # print the manifest
#
# Profiles: all comfy edit t2i t2v i2v video-lora llama
# Optional: HF_TOKEN=hf_xxx (none of the current sources require it)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/models_manifest.sh
source "$SCRIPT_DIR/models_manifest.sh"

PROFILE="all"
MODELS_ROOT="${MODELS_ROOT:-$PROJECT_ROOT/models}"
HF_TOKEN="${HF_TOKEN:-${HUGGINGFACE_HUB_TOKEN:-}}"
MODE="download"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)     PROFILE="$2"; shift 2 ;;
    --models-root) MODELS_ROOT="$2"; shift 2 ;;
    --check)       MODE="check"; shift ;;
    --list)        MODE="list"; shift ;;
    -h|--help)     sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "[download_models] unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "${PROFILE_TAGS[$PROFILE]:-}" ]]; then
  echo "Unknown profile: $PROFILE (known: $(list_profiles))" >&2
  exit 2
fi
mapfile -t SPECS < <(select_specs "$PROFILE")
if [[ "${#SPECS[@]}" -eq 0 ]]; then
  echo "No models selected for profile=$PROFILE"
  exit 0
fi

human() { # bytes -> GiB/MiB
  local b="$1"
  if [[ "$b" -ge 1073741824 ]]; then awk -v b="$b" 'BEGIN{printf "%.1fG", b/1073741824}'
  else awk -v b="$b" 'BEGIN{printf "%.0fM", b/1048576}'; fi
}

total=0
for spec in "${SPECS[@]}"; do
  IFS='|' read -r _tags _sub _name bytes _url <<<"$spec"
  total=$((total + bytes))
done

echo "Models root : $MODELS_ROOT"
echo "Profile     : $PROFILE"
echo "Files       : ${#SPECS[@]}  ($(human "$total") total)"
echo ""

if [[ "$MODE" == "list" ]]; then
  for spec in "${SPECS[@]}"; do
    IFS='|' read -r _tags sub name bytes url <<<"$spec"
    printf "%-8s %s/%s\n         %s\n" "$(human "$bytes")" "$sub" "$name" "$url"
  done
  exit 0
fi

# A file counts as present only when its size matches the manifest exactly;
# anything else is a partial/corrupt transfer and gets resumed or refetched.
missing=0
present=0
for spec in "${SPECS[@]}"; do
  IFS='|' read -r _tags sub name bytes _url <<<"$spec"
  target="$MODELS_ROOT/$sub/$name"
  if [[ -f "$target" ]]; then
    actual=$(wc -c <"$target" | tr -d ' ')
    if [[ "$actual" == "$bytes" ]]; then
      present=$((present + 1))
      [[ "$MODE" == "check" ]] && printf "  [OK]      %s/%s\n" "$sub" "$name"
      continue
    fi
    missing=$((missing + 1))
    printf "  [PARTIAL] %s/%s (%s of %s)\n" "$sub" "$name" "$(human "$actual")" "$(human "$bytes")"
  else
    missing=$((missing + 1))
    [[ "$MODE" == "check" ]] && printf "  [MISSING] %s/%s (%s)\n" "$sub" "$name" "$(human "$bytes")"
  fi
done

if [[ "$MODE" == "check" ]]; then
  echo ""
  echo "present=$present missing=$missing"
  [[ "$missing" -gt 0 ]] && exit 1
  echo "All models for profile=$PROFILE are present."
  exit 0
fi

if [[ "$missing" -eq 0 ]]; then
  echo "All ${#SPECS[@]} files already present — nothing to do."
  exit 0
fi

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }

fetch() { # url target expected_bytes
  local url="$1" target="$2" bytes="$3"
  mkdir -p "$(dirname "$target")"
  local -a args=(-fL --retry 8 --retry-delay 3 --retry-all-errors -C - --progress-bar -o "$target" "$url")
  [[ -n "$HF_TOKEN" && "$url" == *"huggingface.co"* ]] && args=(-H "Authorization: Bearer ${HF_TOKEN}" "${args[@]}")
  if ! curl "${args[@]}"; then
    echo "  failed: $url" >&2
    return 1
  fi
  local actual
  actual=$(wc -c <"$target" | tr -d ' ')
  if [[ "$actual" != "$bytes" ]]; then
    echo "  size mismatch: got $actual, expected $bytes (rerun to resume)" >&2
    return 1
  fi
  echo "  ok ($(human "$actual"))"
}

ok=0
failed=0
idx=0
for spec in "${SPECS[@]}"; do
  idx=$((idx + 1))
  IFS='|' read -r _tags sub name bytes url <<<"$spec"
  target="$MODELS_ROOT/$sub/$name"
  if [[ -f "$target" ]] && [[ "$(wc -c <"$target" | tr -d ' ')" == "$bytes" ]]; then
    continue
  fi
  echo "[$idx/${#SPECS[@]}] $sub/$name ($(human "$bytes"))"
  if fetch "$url" "$target" "$bytes"; then
    ok=$((ok + 1))
  else
    failed=$((failed + 1))
  fi
done

echo ""
echo "Done. downloaded=$ok failed=$failed profile=$PROFILE"
if [[ "$failed" -gt 0 ]]; then
  echo "Rerun the same command to resume the failed transfers."
  exit 1
fi
