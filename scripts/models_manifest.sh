#!/usr/bin/env bash
# Single source of truth for downloadable model files.
# Sourced by download_models.sh and verify_models.sh — not meant to run alone.
#
# Each MODEL_SPECS entry: "tags|target_subdir|target_name|gated|source_url"
#   tags          space-separated; a profile selects specs whose tags intersect.
#   target_subdir under MODELS_ROOT (text_encoders / vae / diffusion_models ...)
#   target_name   final filename on disk
#   gated         1 = needs HF account + accepted model terms (HF_TOKEN required)
#   source_url    direct HuggingFace `resolve/` URL (curl-downloadable)

MODEL_SPECS=(
  # FLUX text/image stack
  "all flux image|text_encoders|clip_l.safetensors|0|https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors?download=true"
  "all flux image|text_encoders|t5xxl_fp16.safetensors|0|https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp16.safetensors?download=true"
  "all flux image|vae|ae.safetensors|0|https://huggingface.co/black-forest-labs/FLUX.1-schnell/resolve/main/ae.safetensors?download=true"
  "all flux image|diffusion_models|flux1-dev.safetensors|1|https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors"
  "all flux image inpaint|diffusion_models|flux1-fill-dev.safetensors|1|https://huggingface.co/black-forest-labs/FLUX.1-Fill-dev/resolve/main/flux1-fill-dev.safetensors?download=true"
  # Z-Image Turbo stack (ComfyUI single-file repack)
  "zimage|diffusion_models|z_image_turbo_bf16.safetensors|0|https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/diffusion_models/z_image_turbo_bf16.safetensors"
  "zimage|text_encoders|qwen_3_4b.safetensors|0|https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/text_encoders/qwen_3_4b.safetensors"
  "zimage|vae|z_ae.safetensors|0|https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors"
  # Wan2.2 14B T2V/I2V stack (ComfyUI repackaged paths)
  "all wan video|text_encoders|umt5_xxl_fp8_e4m3fn_scaled.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.1_ComfyUI_repackaged/resolve/main/split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors"
  "all wan video|vae|wan_2.1_vae.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/vae/wan_2.1_vae.safetensors"
  "all wan video t2v|diffusion_models|wan2.2_t2v_high_noise_14B_fp8_scaled.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_t2v_high_noise_14B_fp8_scaled.safetensors"
  "all wan video t2v|diffusion_models|wan2.2_t2v_low_noise_14B_fp8_scaled.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_t2v_low_noise_14B_fp8_scaled.safetensors"
  "all wan video i2v|diffusion_models|wan2.2_i2v_high_noise_14B_fp16.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_high_noise_14B_fp16.safetensors"
  "all wan video i2v|diffusion_models|wan2.2_i2v_low_noise_14B_fp16.safetensors|0|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_low_noise_14B_fp16.safetensors"
)

# profile -> matching tags (a spec is selected if it shares any tag with the profile)
declare -A PROFILE_TAGS=(
  [all]="all"
  [image]="image flux"
  [video]="video wan"
  [flux]="flux"
  [zimage]="zimage"
  [wan]="wan"
  [t2v]="t2v"
  [i2v]="i2v"
)

list_profiles() { printf '%s\n' "${!PROFILE_TAGS[@]}" | sort | tr '\n' ' '; }

# select_specs <profile> -> prints matching MODEL_SPECS lines (one per line)
select_specs() {
  local profile="$1"
  local wanted="${PROFILE_TAGS[$profile]:-}"
  if [[ -z "$wanted" ]]; then
    echo "Unknown profile: $profile (known: $(list_profiles))" >&2
    return 2
  fi
  local spec spec_tags t w
  for spec in "${MODEL_SPECS[@]}"; do
    spec_tags="${spec%%|*}"
    for t in $spec_tags; do
      for w in $wanted; do
        if [[ "$t" == "$w" ]]; then
          echo "$spec"
          break 2
        fi
      done
    done
  done
}
