#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import shutil
import sys
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = SCRIPT_DIR.parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from comfy_path_config import upsert_extra_model_paths
from model_manifest import ModelSpec, list_profiles, select_specs


def human_size(size: int) -> str:
    if size < 1024:
        return f"{size}B"
    if size < 1024**2:
        return f"{size / 1024:.1f}KB"
    if size < 1024**3:
        return f"{size / 1024**2:.1f}MB"
    return f"{size / 1024**3:.2f}GB"


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Download model files into project ./models and optionally write ComfyUI extra_model_paths.yaml."
    )
    parser.add_argument(
        "--models-root",
        type=Path,
        default=Path(os.getenv("MODELS_ROOT", PROJECT_ROOT / "models")).expanduser(),
        help="Model destination root. Default: ./models",
    )
    parser.add_argument(
        "--comfy-root",
        type=Path,
        default=Path(os.getenv("COMFYUI_ROOT", "~/ComfyUI")).expanduser(),
        help="ComfyUI root directory for writing extra_model_paths.yaml.",
    )
    parser.add_argument(
        "--config-name",
        default=os.getenv("COMFY_EXTRA_CONFIG_NAME", "chat_image_models"),
        help="YAML key name inserted into extra_model_paths.yaml.",
    )
    parser.add_argument(
        "--skip-comfy-config",
        action="store_true",
        help="Skip writing ComfyUI extra_model_paths.yaml.",
    )
    parser.add_argument(
        "--profile",
        default="all",
        choices=list_profiles(),
        help="Model subset to download.",
    )
    parser.add_argument(
        "--token",
        default=os.getenv("HF_TOKEN") or os.getenv("HUGGINGFACE_HUB_TOKEN"),
        help="Hugging Face token. Falls back to local `huggingface-cli login` token if omitted.",
    )
    parser.add_argument(
        "--cache-dir",
        type=Path,
        default=Path(os.getenv("HF_HOME", "~/.cache/huggingface")).expanduser(),
        help="Hugging Face cache directory.",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        help="Re-download and overwrite existing target files.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Only print what would be downloaded.",
    )
    parser.add_argument(
        "--local-files-only",
        action="store_true",
        help="Do not access network; only use local Hugging Face cache.",
    )
    parser.add_argument(
        "--disable-hf-transfer",
        action="store_true",
        help="Disable high-speed transfer backend even if installed.",
    )
    return parser


def link_or_copy(src: Path, dst: Path) -> tuple[str, int]:
    src_real = src.resolve(strict=True)
    dst.parent.mkdir(parents=True, exist_ok=True)
    if dst.exists() or dst.is_symlink():
        dst.unlink()

    action = "copied"
    try:
        os.link(src_real, dst)
        action = "hardlinked"
    except OSError:
        shutil.copy2(src_real, dst)
        action = "copied"

    # Some environments may produce a dangling link-like artifact after os.link
    # when source comes from HF cache indirections. Validate and fallback to copy.
    try:
        size = dst.stat().st_size
    except FileNotFoundError:
        if dst.exists() or dst.is_symlink():
            dst.unlink()
        shutil.copy2(src_real, dst)
        action = "copied"
        size = dst.stat().st_size

    return action, size


def print_plan(
    specs: list[ModelSpec],
    models_root: Path,
    comfy_root: Path,
    config_name: str,
    skip_comfy_config: bool,
) -> None:
    print(f"Models root: {models_root}")
    print(f"Total files: {len(specs)}")
    for spec in specs:
        target = models_root / spec.target_subdir / spec.resolved_name
        gated = " (gated)" if spec.gated else ""
        print(f"- {spec.key}{gated}")
        print(f"  from: {spec.repo_id}/{spec.filename}")
        print(f"  to  : {target}")

    if skip_comfy_config:
        print("ComfyUI config: skipped")
    else:
        print(f"ComfyUI config: {comfy_root / 'extra_model_paths.yaml'}")
        print(f"Config key    : {config_name}")


def is_probably_auth_error(exc: Exception) -> bool:
    text = str(exc).lower()
    signals = ("401", "403", "unauthorized", "forbidden", "gated", "access")
    return any(signal in text for signal in signals)


def download_one(
    spec: ModelSpec,
    models_root: Path,
    token: str | None,
    cache_dir: Path,
    force: bool,
    local_files_only: bool,
    hf_hub_download: Any,
) -> tuple[bool, str]:
    target = models_root / spec.target_subdir / spec.resolved_name
    if target.exists() and target.stat().st_size > 0 and not force:
        return True, f"skip (exists, {human_size(target.stat().st_size)})"

    try:
        cached_path = hf_hub_download(
            repo_id=spec.repo_id,
            filename=spec.filename,
            token=token,
            cache_dir=str(cache_dir),
            local_files_only=local_files_only,
        )
    except Exception as exc:
        if spec.gated and is_probably_auth_error(exc):
            return (
                False,
                "failed (gated repo; run `huggingface-cli login` and accept terms on model page first)",
            )
        return False, f"failed ({exc})"

    source = Path(cached_path)
    if not source.exists():
        return False, f"failed (download returned missing path: {source})"

    action, size = link_or_copy(source, target)
    return True, f"{action} ({human_size(size)})"


def main() -> int:
    args = build_parser().parse_args()
    models_root = args.models_root.expanduser().resolve()
    comfy_root = args.comfy_root.expanduser().resolve()
    specs = select_specs(args.profile)
    if not specs:
        print("No model files selected.")
        return 0

    if args.disable_hf_transfer:
        os.environ["HF_HUB_ENABLE_HF_TRANSFER"] = "0"
    else:
        os.environ.setdefault("HF_HUB_ENABLE_HF_TRANSFER", "1")

    print_plan(specs, models_root, comfy_root, args.config_name, args.skip_comfy_config)
    if args.dry_run:
        if not args.skip_comfy_config:
            print("Dry-run: would update ComfyUI extra_model_paths.yaml")
        return 0

    try:
        from huggingface_hub import hf_hub_download
    except ImportError:
        print("Missing dependency: huggingface_hub")
        print("Install with: python3 -m pip install -U \"huggingface_hub[hf_transfer]>=0.31.0\"")
        return 2

    success = 0
    failed = 0
    for idx, spec in enumerate(specs, start=1):
        target = models_root / spec.target_subdir / spec.resolved_name
        print(f"[{idx}/{len(specs)}] {spec.key}")
        print(f"  -> {target}")
        ok, message = download_one(
            spec=spec,
            models_root=models_root,
            token=args.token,
            cache_dir=args.cache_dir,
            force=args.force,
            local_files_only=args.local_files_only,
            hf_hub_download=hf_hub_download,
        )
        print(f"  {message}")
        if ok:
            success += 1
        else:
            failed += 1

    if not args.skip_comfy_config:
        config_file = upsert_extra_model_paths(
            comfy_root=comfy_root,
            config_name=args.config_name,
            models_root=models_root,
        )
        print(f"Updated ComfyUI config: {config_file}")

    print("")
    print(f"Done. success={success}, failed={failed}, profile={args.profile}")
    if failed > 0:
        print("Tip: for gated models, ensure Hugging Face account has accepted model terms.")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
