#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   bash scripts/disable_reactor_nsfw.sh
#   bash scripts/disable_reactor_nsfw.sh /path/to/reactor_sfw.py
#
# This script does steps 1/2/3 only:
# 1) locate file
# 2) backup file
# 3) patch nsfw_image() to always return False
# It does NOT restart ComfyUI.

DEFAULT_FILE="/root/autodl-tmp/projects/chat_image/ComfyUI/custom_nodes/ComfyUI-ReActor/scripts/reactor_sfw.py"
FILE="${1:-${REACTOR_SFW_FILE:-$DEFAULT_FILE}}"

if [[ ! -f "$FILE" ]]; then
  echo "[error] file not found: $FILE"
  echo "[hint] pass explicit path:"
  echo "  bash scripts/disable_reactor_nsfw.sh /your/ComfyUI/custom_nodes/ComfyUI-ReActor/scripts/reactor_sfw.py"
  exit 1
fi

ts="$(date +%Y%m%d_%H%M%S)"
BAK="${FILE}.bak.${ts}"
cp "$FILE" "$BAK"
echo "[ok] backup created: $BAK"

python3 - "$FILE" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
needle = "def nsfw_image(img_data, model_path: str):\n"
inject = "def nsfw_image(img_data, model_path: str):\n    return False  # disabled by operator\n"

if inject in s:
    print("[ok] already patched")
    raise SystemExit(0)

if needle not in s:
    print("[error] function header not found: nsfw_image(img_data, model_path: str)", file=sys.stderr)
    raise SystemExit(2)

s = s.replace(needle, inject, 1)
p.write_text(s, encoding="utf-8")
print("[ok] patch applied")
PY

echo "[done] ReActor NSFW filter disabled in file."
echo "[note] ComfyUI restart was intentionally skipped."
