#!/usr/bin/env bash
# Model manifest: the single source of truth for what a fresh deployment needs.
# Sourced by download_models.sh — not meant to run on its own.
#
# Derived from the workflows themselves (workflows/*.json,
# app/configs/workflows/*.yaml, app/configs/loras/ltx_2_3.yaml, .env.example), so
# it holds only what the current pipelines actually load — not the historical pile
# in the old server's models/ dir. See docs/MODELS.md.
#
# Entry format: "profiles|target_subdir|filename|bytes|url"
#   profiles      space-separated; a profile selects entries sharing any tag
#   target_subdir under MODELS_ROOT — matches docker/comfy/extra_model_paths.yaml
#   bytes         exact expected size, taken from the running production server
#                 and verified against each URL's Content-Length (byte-identical)
#   url           direct download; every URL below returned HTTP 200 with no
#                 token required at the time of writing
#
# Two LoRAs are stored under a name that differs from their upstream filename
# (renamed when first downloaded); their URLs point at the byte-identical
# upstream file and the script saves them under the name the workflows expect.

MODEL_SPECS=(
  # --- image edit ---
  "edit i2v|checkpoints|Qwen-Rapid-AIO-NSFW-v23.safetensors|28431840023|https://huggingface.co/Phr00t/Qwen-Image-Edit-Rapid-AIO/resolve/main/v23/Qwen-Rapid-AIO-NSFW-v23.safetensors"

  # --- text to image (Krea 2) ---
  "t2i|diffusion_models|krea2_turbo_fp8_scaled.safetensors|13141730784|https://huggingface.co/Comfy-Org/Krea-2/resolve/main/diffusion_models/krea2_turbo_fp8_scaled.safetensors"
  "t2i|text_encoders|qwen3vl_4b_fp8_scaled.safetensors|5242467968|https://huggingface.co/Comfy-Org/Qwen3-VL/resolve/main/text_encoders/qwen3vl_4b_fp8_scaled.safetensors"
  "t2i|vae|qwen_image_vae.safetensors|253806246|https://huggingface.co/Comfy-Org/Qwen-Image_ComfyUI/resolve/main/split_files/vae/qwen_image_vae.safetensors"

  # --- text / image to video (LTX 2.3 + Sulphur) ---
  "t2v i2v|checkpoints|sulphur_dev_bf16.safetensors|46139885414|https://huggingface.co/SulphurAI/Sulphur-2-base/resolve/main/sulphur_dev_bf16.safetensors"
  "t2v i2v|checkpoints|ltx-2.3-22b-dev-fp8.safetensors|29145431166|https://huggingface.co/Lightricks/LTX-2.3-fp8/resolve/main/ltx-2.3-22b-dev-fp8.safetensors"
  "t2v i2v|loras|ltx-2.3-22b-distilled-lora-384-1.1.safetensors|7605507256|https://huggingface.co/Lightricks/LTX-2.3/resolve/main/ltx-2.3-22b-distilled-lora-384-1.1.safetensors"
  "t2v i2v|latent_upscale_models|ltx-2.3-spatial-upscaler-x2-1.1.safetensors|995743560|https://huggingface.co/Lightricks/LTX-2.3/resolve/main/ltx-2.3-spatial-upscaler-x2-1.1.safetensors"
  "t2v i2v|text_encoders|gemma_3_12B_it_fp4_mixed.safetensors|9447702218|https://huggingface.co/Comfy-Org/ltx-2/resolve/main/split_files/text_encoders/gemma_3_12B_it_fp4_mixed.safetensors"
  "i2v|loras|sulphur_ltx23_better_nsfw_motion.safetensors|654446000|https://huggingface.co/TripleDug/my-awesome-models/resolve/main/Sulphur_LTX+2.3_better+_NSFW_motion.safetensors"

  # --- dynamic video LoRA (app/configs/loras/ltx_2_3.yaml) ---
  "video-lora|loras|ltx23-f4c3spl4sh-step8500-k3nk.safetensors|1713335432|https://huggingface.co/cdkkkk/setup/resolve/main/ltx2/ltx23-f4c3spl4sh-step8500-k3nk.safetensors"
  "video-lora|loras|ltx23_paizuri_titfuck_i2v_sulphur_v2.safetensors|654446008|https://huggingface.co/cdkkkk/setup/resolve/main/ltx2/LTX_2.3_-_POV_Paizuri_-_Titfuck_ThrustingSqueezing_I2V.safetensors"

  # --- llama.cpp text service (filenames pinned by .env.example) ---
  "llama|llama|Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-Q4_K_M.gguf|21166758016|https://huggingface.co/HauhauCS/Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive/resolve/main/Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-Q4_K_M.gguf"
  "llama|llama|mmproj-Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-f16.gguf|899283072|https://huggingface.co/HauhauCS/Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive/resolve/main/mmproj-Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-f16.gguf"
)

# profile -> matching tags
declare -A PROFILE_TAGS=(
  [all]="edit t2i t2v i2v video-lora llama"
  [comfy]="edit t2i t2v i2v video-lora"
  [edit]="edit"
  [t2i]="t2i"
  [t2v]="t2v"
  [i2v]="i2v video-lora"
  [video-lora]="video-lora"
  [llama]="llama"
)

list_profiles() { printf '%s\n' "${!PROFILE_TAGS[@]}" | sort | tr '\n' ' '; }

# select_specs <profile> -> prints matching MODEL_SPECS lines (deduped)
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
