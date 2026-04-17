# Model Download Scripts

These scripts download all model files needed by this project's PRD stack into this project directory (`./models`) and automatically update ComfyUI `extra_model_paths.yaml`.

- FLUX.1-dev (T2I)
- FLUX.1-Fill-dev (Inpainting)
- Wan2.2 14B T2V + I2V (ComfyUI repackaged files)
- Z-Image Turbo (ComfyUI single-file repack)

## 1) One-command full download

```bash
bash /Volumes/kk/work/openclaw/chat_image/scripts/download_all_models.sh --comfy-root /path/to/ComfyUI
```

Default download destination is:

```text
/Volumes/kk/work/openclaw/chat_image/models
```

## 2) Manual step-by-step

```bash
bash /Volumes/kk/work/openclaw/chat_image/scripts/install_model_deps.sh
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile all --comfy-root /path/to/ComfyUI
python3 /Volumes/kk/work/openclaw/chat_image/scripts/verify_models.py --profile all --models-root /Volumes/kk/work/openclaw/chat_image/models
```

## 3) Optional subsets

```bash
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile image --comfy-root /path/to/ComfyUI
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile video --comfy-root /path/to/ComfyUI
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile t2v --comfy-root /path/to/ComfyUI
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile i2v --comfy-root /path/to/ComfyUI
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile zimage --comfy-root /path/to/ComfyUI
```

Or one-shot Z-Image:

```bash
bash /Volumes/kk/work/openclaw/chat_image/scripts/download_z_image_models.sh --comfy-root /path/to/ComfyUI
```

Or pull curated Z-Image type assets (normal/futa/furry workflows + related models):

```bash
CIVITAI_TOKEN=your_civitai_token \
HF_TOKEN=your_hf_token \
bash /Volumes/kk/work/openclaw/chat_image/scripts/download_zimage_type_assets.sh \
  --models-root /Volumes/kk/work/openclaw/chat_image/models
```

By default, this script does **not** download workflow zip archives. It only prepares ready-to-run workflow JSON files.
If you want to keep Civitai workflow archives/extracted files for reference, add:

```bash
--community-workflows
```

After completion, gateway-compatible workflow files are available at:

```text
/Volumes/kk/work/openclaw/chat_image/bundles/zimage_type_assets/workflows_ready
```

Install them into runtime workflow directory:

```bash
cp -f /Volumes/kk/work/openclaw/chat_image/bundles/zimage_type_assets/workflows_ready/*.json \
  /Volumes/kk/work/openclaw/chat_image/workflows/
```

Use custom destination if needed:

```bash
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile all --models-root /data/chat_image/models --comfy-root /path/to/ComfyUI
```

## 4) Hugging Face token

Some FLUX repositories are gated. Before download:

1. Use your browser to accept model terms on corresponding Hugging Face pages.
2. Login locally:

```bash
huggingface-cli login
```

Or pass token directly:

```bash
python3 /Volumes/kk/work/openclaw/chat_image/scripts/download_models.py --profile all --token hf_xxx --comfy-root /path/to/ComfyUI
```

## 5) Only regenerate ComfyUI path config

If models are already copied (for example after packing this project to a server), run:

```bash
python3 /Volumes/kk/work/openclaw/chat_image/scripts/configure_comfy_paths.py --models-root /Volumes/kk/work/openclaw/chat_image/models --comfy-root /path/to/ComfyUI
```
