#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from model_manifest import list_profiles, select_specs


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Verify required model files in a models directory.")
    parser.add_argument(
        "--models-root",
        type=Path,
        default=None,
        help="Model directory root. Default: ./models",
    )
    parser.add_argument(
        "--comfy-root",
        type=Path,
        default=None,
        help="Deprecated compatibility option. Uses <comfy-root>/models when --models-root is omitted.",
    )
    parser.add_argument(
        "--profile",
        default="all",
        choices=list_profiles(),
        help="Profile to verify.",
    )
    return parser


def main() -> int:
    args, unknown = build_parser().parse_known_args()
    if unknown:
        print(f"Ignoring unknown args for verify: {' '.join(unknown)}")

    default_models_root = (SCRIPT_DIR.parent / "models").resolve()
    if args.models_root is not None:
        models_root = args.models_root.expanduser().resolve()
    elif args.comfy_root is not None:
        models_root = (args.comfy_root.expanduser().resolve() / "models").resolve()
    else:
        models_root = default_models_root

    specs = select_specs(args.profile)
    missing: list[Path] = []

    print(f"Checking profile={args.profile} at {models_root}")
    for spec in specs:
        target = models_root / spec.target_subdir / spec.resolved_name
        exists = target.exists() and target.is_file() and target.stat().st_size > 0
        if exists:
            print(f"[OK]     {target} ({target.stat().st_size} bytes)")
        else:
            print(f"[MISSING] {target}")
            missing.append(target)

    print("")
    if missing:
        print(f"Missing files: {len(missing)}")
        return 1
    print("All required files are present.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
