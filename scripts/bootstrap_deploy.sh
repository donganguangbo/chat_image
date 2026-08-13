#!/usr/bin/env bash
# Prepare a fresh server for `docker compose up`: create the mount points compose
# expects, and check the pieces that must exist before the stack can start.
#
#   bash scripts/bootstrap_deploy.sh
#
# It does NOT download models (that's scripts/download_models.sh, ~154 GB) and it
# does NOT build images. Run order on a new box:
#
#   1. bash scripts/bootstrap_deploy.sh
#   2. bash scripts/download_models.sh
#   3. docker compose build && docker compose up -d
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

echo "[1/4] creating mount points..."
# Model dirs must match docker/comfy/extra_model_paths.yaml.
for d in checkpoints clip clip_vision controlnet diffusion_models latent_upscale_models \
         llama loras model_patches text_encoders unet upscale_models vae; do
  mkdir -p "models/$d"
done
# ComfyUI runtime scratch — pure runtime state, safe to start empty.
for port in 6006 6009; do
  for d in input output temp user cache pycache logs; do
    mkdir -p "isolated/comfy_$port/runtime/$d"
  done
done
mkdir -p hf_cache outputs uploads logs
echo "      models/, isolated/comfy_{6006,6009}/runtime/, hf_cache/, outputs/"

echo "[2/4] checking .env..."
if [[ ! -f .env ]]; then
  cp .env.example .env
  secret="$(openssl rand -hex 32 2>/dev/null || head -c32 /dev/urandom | xxd -p -c32)"
  # Fill in the one value that has no safe default.
  if grep -q '^GATEWAY_SECRET=' .env; then
    tmp="$(mktemp)"
    sed "s|^GATEWAY_SECRET=.*|GATEWAY_SECRET=$secret|" .env > "$tmp" && mv "$tmp" .env
  fi
  echo "      created .env with a fresh GATEWAY_SECRET"
  echo "      TODO: set S3_ACCESS_KEY / S3_SECRET_KEY in .env if you use object storage"
else
  echo "      .env already present (left untouched)"
fi

echo "[3/4] checking custom node pins..."
pins="docker/comfy/node_pins.txt"
vendor="docker/comfy/vendor_nodes"
if [[ -f "$pins" ]]; then
  n=$(grep -vc '^#' "$pins" || true)
  v=$(find "$vendor" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
  echo "      $n pinned nodes + $v vendored"
  echo "      (verify upstream availability: bash scripts/verify_node_pins.sh)"
else
  echo "      MISSING: $pins — the comfy image can't be built without it" >&2
fi

echo "[4/4] checking models..."
if bash scripts/download_models.sh --check >/dev/null 2>&1; then
  echo "      all models present"
else
  echo "      models incomplete — run: bash scripts/download_models.sh"
fi

echo ""
echo "Next:"
echo "  bash scripts/download_models.sh      # ~154 GB, resumable"
echo "  docker compose build                 # comfy + go-gateway (llama pulls upstream)"
echo "  docker compose up -d"
