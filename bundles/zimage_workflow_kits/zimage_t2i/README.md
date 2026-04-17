# zimage_t2i Kit

Workflow:

- `workflows/t2i_zimage_darkbeast.json`

Download models:

```bash
cd /root/autodl-tmp/projects/chat_image/bundles/zimage_workflow_kits/zimage_t2i
bash download_models.sh /root/autodl-tmp/projects/chat_image/models
```

Required model outputs:

- `models/diffusion_models/redcraftFeb1126Latest_zibDistilledDX3Lucis.safetensors`
- `models/diffusion_models/darkbeastz6BlitzBF16.Vii6.safetensors`
- `models/text_encoders/qwen_3_4b.safetensors`
- `models/vae/ae.safetensors`
- `models/loras/REDZ15_DetailDaemonZ_lora_v1.1.safetensors`

Notes:

- Script auto-downloads `qwen_3_4b.safetensors` and `ae.safetensors` from Hugging Face.
- `redcraft/darkbeastz6/REDZ15` can be:
  - pre-uploaded to the required paths, or
  - downloaded by passing URLs:

```bash
REDCRAFT_URL='...' DARKBEAST_URL='...' REDZ15_LORA_URL='...' \
CIVITAI_TOKEN='your_token' HF_TOKEN='your_hf_token' \
bash download_models.sh /root/autodl-tmp/projects/chat_image/models
```
