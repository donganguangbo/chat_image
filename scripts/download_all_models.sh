#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "${SCRIPT_DIR}/download_models.sh" --profile all "$@"

echo ""
echo "Installing ControlNet + DWPose chain..."
bash "${SCRIPT_DIR}/install_controlnet.sh"

echo ""
echo "Verifying..."
bash "${SCRIPT_DIR}/verify_models.sh" --profile all "$@"
