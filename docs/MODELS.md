# 模型清单

新服务器部署只需下载本清单里的文件。清单**从工作流反推**得出（`workflows/*.json` +
`app/configs/workflows/*.yaml` + `app/configs/loras/*.yaml` + `.env.example`），
不是历史累积，因此比现有服务器上的 `models/` 目录小得多。

- **必需合计：14 个文件，约 156 GB**
- 现有生产机 `models/` 有 279 GB，其中约 120 GB 是**当前工作流已不引用**的历史遗留（见文末）

下载：`bash scripts/download_models.sh`（详见 [脚本说明](#下载脚本)）

## 必需模型

### ComfyUI（12 个，约 135 GB）

| 目标目录 | 文件 | 大小 |
|---|---|---|
| `models/checkpoints/` | `sulphur_dev_bf16.safetensors` | 43 G |
| `models/checkpoints/` | `ltx-2.3-22b-dev-fp8.safetensors` | 28 G |
| `models/checkpoints/` | `Qwen-Rapid-AIO-NSFW-v23.safetensors` | 27 G |
| `models/diffusion_models/` | `krea2_turbo_fp8_scaled.safetensors` | 13 G |
| `models/text_encoders/` | `gemma_3_12B_it_fp4_mixed.safetensors` | 8.8 G |
| `models/text_encoders/` | `qwen3vl_4b_fp8_scaled.safetensors` | 4.9 G |
| `models/loras/` | `ltx-2.3-22b-distilled-lora-384-1.1.safetensors` | 7.1 G |
| `models/loras/` | `ltx23-f4c3spl4sh-step8500-k3nk.safetensors` | 1.6 G |
| `models/loras/` | `sulphur_ltx23_better_nsfw_motion.safetensors` | 625 M |
| `models/loras/` | `ltx23_paizuri_titfuck_i2v_sulphur_v2.safetensors` | 625 M |
| `models/latent_upscale_models/` | `ltx-2.3-spatial-upscaler-x2-1.1.safetensors` | 950 M |
| `models/vae/` | `qwen_image_vae.safetensors` | 243 M |

### llama.cpp 文字服务（2 个，约 21 GB）

文件名由 `.env.example` 的 `LLAMA_MODEL_FILE` / `LLAMA_MMPROJ_FILE` 指定。

| 目标目录 | 文件 | 大小 |
|---|---|---|
| `models/llama/` | `Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-Q4_K_M.gguf` | 20 G |
| `models/llama/` | `mmproj-Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-f16.gguf` | 858 M |

## 来源如何验证的

每个 URL 的 `Content-Length` 都与生产服务器上对应文件的实际字节数**逐字节比对一致**，
14/14 全部吻合 —— 这排除了"文件名相似但内容不同"的情况。全部无需 HF token（非 gated）。

两个 LoRA 的上游文件名与本地名不同（当初下载后改过名），脚本按工作流期望的名字保存：

| 本地名（工作流引用） | 上游文件名 |
|---|---|
| `sulphur_ltx23_better_nsfw_motion.safetensors` | `Sulphur_LTX+2.3_better+_NSFW_motion.safetensors`（Civitai 2344781 / v2986751） |
| `ltx23_paizuri_titfuck_i2v_sulphur_v2.safetensors` | `LTX_2.3_-_POV_Paizuri_-_Titfuck_ThrustingSqueezing_I2V.safetensors`（Civitai 2364658 / v3019587） |

注意：`cdkkkk/setup`、`TripleDug/my-awesome-models` 是个人合集仓库而非原作者官方仓库，
长期可用性不如 Lightricks / Comfy-Org / SulphurAI 官方仓库，建议下载后自行备份这几个 LoRA。

## 按工作流分组（脚本的 `--profile`）

| profile | 工作流 | 模型 |
|---|---|---|
| `edit` | `ichat_image_edit` | Qwen-Rapid-AIO-NSFW-v23 |
| `t2i` | `ichat_text_to_image` | krea2_turbo_fp8 · qwen3vl_4b · qwen_image_vae |
| `t2v` | `ichat_text_to_video` | sulphur_dev_bf16 · ltx-2.3-22b-dev-fp8 · distilled-lora · spatial-upscaler · gemma_3_12B |
| `i2v` | `ichat_image_to_video` | `t2v` 全部 + Qwen-Rapid-AIO-NSFW-v23 + sulphur_ltx23_better_nsfw_motion |
| `video-lora` | `app/configs/loras/ltx_2_3.yaml` 动态 LoRA | ltx23-f4c3spl4sh-step8500-k3nk · ltx23_paizuri_titfuck_i2v_sulphur_v2 |
| `llama` | 文字服务 | 2 个 gguf |
| `comfy` | 除 llama 外全部 | 12 个文件，134 G |
| `all`（默认） | 以上全部 | 14 个文件，154 G |

实测各 profile 体积：`edit` 26.5G · `t2i` 17.4G · `t2v` 86.9G · `i2v` 116.2G ·
`video-lora` 2.2G · `llama` 20.6G · `comfy` 133.6G · `all` 154.1G。

## 下载脚本

```bash
bash scripts/download_models.sh                 # 全部（默认 all）
bash scripts/download_models.sh --profile t2i   # 只下某个 profile
bash scripts/download_models.sh --check         # 只校验缺哪些，不下载
bash scripts/download_models.sh --list          # 打印清单与来源
```

- **断点续传**（`curl -C -`）：中断后重跑即续传，已实测（截断到 100M 后重跑正确识别
  `[PARTIAL] 100M of 242M` 并补齐）。154 GB 下载中断是常态，随时可反复执行
- **完整性校验**：只有字节数与清单**完全一致**才算已下载，部分文件一律续传，不会把
  半个文件当成功
- 当前所有来源都**不需要** HF token；若将来换成 gated 源，`export HF_TOKEN=hf_xxx` 即可
- 清单单一来源在 `scripts/models_manifest.sh`，改模型只改那一处

## 当前工作流已不引用的历史模型（新机器无需下载，约 120 GB）

保留在旧服务器上，但 4 个工作流与 LoRA 配置均未引用：

- `models/foley/` 10.7 G（hunyuanvideo_foley 等 3 个）
- `models/prompt_generator/` 8.3 G
- `models/unet/` 21 G
- `models/clip_vision/` 1.2 G · `models/dwpose/` 335 M · `models/rife_models/` 21 M · `models/upscale_models/` 8.6 M
- `models/checkpoints/` 与 `models/loras/` 中除上表所列之外的其余文件

如需恢复某项旧能力（例如 Foley 音效、DWPose 姿态），再单独补下即可。
