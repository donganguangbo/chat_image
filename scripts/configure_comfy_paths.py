#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = SCRIPT_DIR.parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from comfy_path_config import upsert_extra_model_paths


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Write/update ComfyUI extra_model_paths.yaml for this project models directory."
    )
    parser.add_argument(
        "--models-root",
        type=Path,
        default=Path(os.getenv("MODELS_ROOT", PROJECT_ROOT / "models")).expanduser(),
        help="Project models root. Default: ./models",
    )
    parser.add_argument(
        "--comfy-root",
        type=Path,
        default=Path(os.getenv("COMFYUI_ROOT", "~/ComfyUI")).expanduser(),
        help="ComfyUI root directory (contains extra_model_paths.yaml).",
    )
    parser.add_argument(
        "--config-name",
        default=os.getenv("COMFY_EXTRA_CONFIG_NAME", "chat_image_models"),
        help="YAML top-level key used in extra_model_paths.yaml.",
    )
    return parser


def main() -> int:
    args = build_parser().parse_args()
    models_root = args.models_root.expanduser().resolve()
    comfy_root = args.comfy_root.expanduser().resolve()

    models_root.mkdir(parents=True, exist_ok=True)
    target = upsert_extra_model_paths(
        comfy_root=comfy_root,
        config_name=args.config_name,
        models_root=models_root,
    )
    print(f"Updated: {target}")
    print(f"Models root: {models_root}")
    print(f"Config key : {args.config_name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
