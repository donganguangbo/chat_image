#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "${SCRIPT_DIR}/download_models.sh" --profile zimage "$@"

echo ""
echo "Verifying..."
bash "${SCRIPT_DIR}/verify_models.sh" --profile zimage "$@"
