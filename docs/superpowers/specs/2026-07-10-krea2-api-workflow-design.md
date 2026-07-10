# Krea2 API Workflow Conversion

## Goal

Convert `krea2SFWNSFWUncensoredImageTo_v10 (1).json` from ComfyUI's UI graph format into a prompt API graph that the Go gateway can load.

## Scope

Preserve the currently active image-generation path and its effective defaults:

- Krea2 Turbo FP8 diffusion model
- Krea2T model enhancer
- Qwen3VL Krea2 text encoder
- Qwen Image VAE
- RES4LYF `ClownsharKSampler_Beta`
- Prompt, width, height, seed, steps, and CFG inputs
- One saved image output

Do not include disabled or UI-only features: image-to-prompt, prompt enhancement, second pass, grain, color correction, sharpening, SeedVR2 upscaling, CivitAI metadata, comparisons, notifications, labels, bookmarks, group controls, or disabled LoRAs.

## Output

Create `workflows/krea2_text_to_image_api.json` without modifying the source workflow or replacing an existing configured workflow.

The graph must contain only backend-executable node classes, use direct API links, and expose literal defaults that can later be bound by a workflow YAML file.

## Verification

- Parse the result with `jq`.
- Verify every linked source node exists.
- Verify no UI graph fields, UUID subgraph classes, or virtual routing nodes remain.
- Run the existing Go test suite to ensure the new unconfigured workflow file does not affect current routes.
