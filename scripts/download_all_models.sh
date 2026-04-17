#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "${SCRIPT_DIR}/install_model_deps.sh"
python3 "${SCRIPT_DIR}/download_models.py" --profile all "$@"

echo ""
echo "Verifying..."
python3 "${SCRIPT_DIR}/verify_models.py" --profile all "$@"
