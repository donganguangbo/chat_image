#!/usr/bin/env python3
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


@dataclass(frozen=True)
class ModelSpec:
    key: str
    repo_id: str
    filename: str
    target_subdir: str
    tags: tuple[str, ...]
    source_url: str
    gated: bool = False
    target_name: str | None = None

    @property
    def resolved_name(self) -> str:
        return self.target_name or Path(self.filename).name


MODEL_SPECS: tuple[ModelSpec, ...] = (
    # FLUX text/image stack
    ModelSpec(
        key="flux_clip_l",
        repo_id="comfyanonymous/flux_text_encoders",
        filename="clip_l.safetensors",
        target_subdir="text_encoders",
        tags=("all", "flux", "image"),
        source_url="https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors?download=true",
    ),
    ModelSpec(
        key="flux_t5xxl_fp16",
        repo_id="comfyanonymous/flux_text_encoders",
        filename="t5xxl_fp16.safetensors",
        target_subdir="text_encoders",
        tags=("all", "flux", "image"),
        source_url="https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp16.safetensors?download=true",
    ),
    ModelSpec(
        key="flux_ae",
        repo_id="black-forest-labs/FLUX.1-schnell",
        filename="ae.safetensors",
        target_subdir="vae",
        tags=("all", "flux", "image"),
        source_url="https://huggingface.co/black-forest-labs/FLUX.1-schnell/resolve/main/ae.safetensors?download=true",
        gated=False,
    ),
    ModelSpec(
        key="flux_dev_unet",
        repo_id="black-forest-labs/FLUX.1-dev",
        filename="flux1-dev.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "flux", "image"),
        source_url="https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors",
        gated=True,
    ),
    ModelSpec(
        key="flux_fill_unet",
        repo_id="black-forest-labs/FLUX.1-Fill-dev",
        filename="flux1-fill-dev.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "flux", "image", "inpaint"),
        source_url="https://huggingface.co/black-forest-labs/FLUX.1-Fill-dev/resolve/main/flux1-fill-dev.safetensors?download=true",
        gated=True,
    ),
    # Z-Image Turbo stack (ComfyUI single-file repack)
    ModelSpec(
        key="zimage_turbo_unet_bf16",
        repo_id="Comfy-Org/z_image_turbo",
        filename="split_files/diffusion_models/z_image_turbo_bf16.safetensors",
        target_subdir="diffusion_models",
        tags=("zimage",),
        source_url="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/diffusion_models/z_image_turbo_bf16.safetensors",
    ),
    ModelSpec(
        key="zimage_qwen_3_4b",
        repo_id="Comfy-Org/z_image_turbo",
        filename="split_files/text_encoders/qwen_3_4b.safetensors",
        target_subdir="text_encoders",
        tags=("zimage",),
        source_url="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/text_encoders/qwen_3_4b.safetensors",
    ),
    ModelSpec(
        key="zimage_vae",
        repo_id="Comfy-Org/z_image_turbo",
        filename="split_files/vae/ae.safetensors",
        target_subdir="vae",
        tags=("zimage",),
        source_url="https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors",
        target_name="z_ae.safetensors",
    ),
    # Wan2.2 14B T2V/I2V stack (ComfyUI repackaged paths)
    ModelSpec(
        key="wan_umt5_xxl_fp8",
        repo_id="Comfy-Org/Wan_2.1_ComfyUI_repackaged",
        filename="split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors",
        target_subdir="text_encoders",
        tags=("all", "wan", "video"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.1_ComfyUI_repackaged/resolve/main/split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors",
    ),
    ModelSpec(
        key="wan_vae_21",
        repo_id="Comfy-Org/Wan_2.2_ComfyUI_Repackaged",
        filename="split_files/vae/wan_2.1_vae.safetensors",
        target_subdir="vae",
        tags=("all", "wan", "video"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/vae/wan_2.1_vae.safetensors",
    ),
    ModelSpec(
        key="wan_t2v_high_14b_fp8_scaled",
        repo_id="Comfy-Org/Wan_2.2_ComfyUI_Repackaged",
        filename="split_files/diffusion_models/wan2.2_t2v_high_noise_14B_fp8_scaled.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "wan", "video", "t2v"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_t2v_high_noise_14B_fp8_scaled.safetensors",
    ),
    ModelSpec(
        key="wan_t2v_low_14b_fp8_scaled",
        repo_id="Comfy-Org/Wan_2.2_ComfyUI_Repackaged",
        filename="split_files/diffusion_models/wan2.2_t2v_low_noise_14B_fp8_scaled.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "wan", "video", "t2v"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_t2v_low_noise_14B_fp8_scaled.safetensors",
    ),
    ModelSpec(
        key="wan_i2v_high_14b_fp16",
        repo_id="Comfy-Org/Wan_2.2_ComfyUI_Repackaged",
        filename="split_files/diffusion_models/wan2.2_i2v_high_noise_14B_fp16.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "wan", "video", "i2v"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_high_noise_14B_fp16.safetensors",
    ),
    ModelSpec(
        key="wan_i2v_low_14b_fp16",
        repo_id="Comfy-Org/Wan_2.2_ComfyUI_Repackaged",
        filename="split_files/diffusion_models/wan2.2_i2v_low_noise_14B_fp16.safetensors",
        target_subdir="diffusion_models",
        tags=("all", "wan", "video", "i2v"),
        source_url="https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_low_noise_14B_fp16.safetensors",
    ),
)

PROFILE_TAGS: dict[str, tuple[str, ...]] = {
    "all": ("all",),
    "image": ("image", "flux"),
    "video": ("video", "wan"),
    "flux": ("flux",),
    "zimage": ("zimage",),
    "wan": ("wan",),
    "t2v": ("t2v",),
    "i2v": ("i2v",),
}


def list_profiles() -> list[str]:
    return sorted(PROFILE_TAGS.keys())


def select_specs(profile: str) -> list[ModelSpec]:
    if profile not in PROFILE_TAGS:
        raise ValueError(f"Unknown profile: {profile}")
    wanted = set(PROFILE_TAGS[profile])
    return [spec for spec in MODEL_SPECS if wanted.intersection(spec.tags)]


def expected_model_paths(models_root: Path, profile: str) -> Iterable[Path]:
    models_root = models_root.expanduser().resolve()
    for spec in select_specs(profile):
        yield models_root / spec.target_subdir / spec.resolved_name
