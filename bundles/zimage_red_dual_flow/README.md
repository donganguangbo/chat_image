# Z-Image Red 双链路包

这个 bundle 只保留你现在要的 `red` 两条文生图链路。

- `t2i_zimage_darkbeast_red_main.json`：主链（checkpoint 直出 + UltimateSDUpscale）
- `t2i_zimage_darkbeast_red_side.json`：侧链（在主链基础上叠加两层 ModelOnly LoRA）

## 两条流差异总结

- 主链：
  - 节点核心：`CheckpointLoaderSimple -> KSamplerAdvanced -> UltimateSDUpscale -> SaveImage`
  - 依赖模型：
    - `models/checkpoints/redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors`
    - `models/upscale_models/4x-ClearRealityV1.pth`

- 侧链：
  - 相比主链，新增两层 LoRA：
    - `models/loras/ZPenisHelper.safetensors`
    - `models/loras/REDZimage1.5_extracted_lora_rank_128.safetensors`
  - 其余采样与放大链路一致

## 目录内容

- `workflows/t2i_zimage_darkbeast_red_main.json`
- `workflows/t2i_zimage_darkbeast_red_side.json`
- `scripts/download_models.sh`

## 拉取模型指令

进入 bundle 后执行：

```bash
cd /root/autodl-tmp/projects/chat_image/bundles/zimage_red_dual_flow
bash scripts/download_models.sh /root/autodl-tmp/projects/chat_image/models
```

如果需要传 token：

```bash
HF_TOKEN=你的_hf_token CIVITAI_TOKEN=你的_civitai_token \
  bash scripts/download_models.sh /root/autodl-tmp/projects/chat_image/models
```

只拉主链（跳过 side LoRA）：

```bash
MAIN_ONLY=1 bash scripts/download_models.sh /root/autodl-tmp/projects/chat_image/models
```

如果 `REDZimage1.5_extracted_lora_rank_128.safetensors` 没有公开直链，可手动上传到：

- `models/loras/REDZimage1.5_extracted_lora_rank_128.safetensors`

或执行时传入：

```bash
RED_SIDE_LORA_MAIN_URL='你的直链' \
  bash scripts/download_models.sh /root/autodl-tmp/projects/chat_image/models
```

## 安装工作流到项目

```bash
cp -f workflows/t2i_zimage_darkbeast_red_main.json /root/autodl-tmp/projects/chat_image/workflows/t2i_zimage_darkbeast_red.json
cp -f workflows/t2i_zimage_darkbeast_red_side.json /root/autodl-tmp/projects/chat_image/workflows/t2i_zimage_darkbeast_red_side.json
```
