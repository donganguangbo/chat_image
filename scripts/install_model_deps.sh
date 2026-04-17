#!/usr/bin/env bash
set -euo pipefail

python3 -m pip install --upgrade pip
python3 -m pip install --upgrade "huggingface_hub[hf_transfer]>=0.31.0"

echo "Model downloader deps installed."
