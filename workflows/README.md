# Workflow Templates

This directory stores ComfyUI API JSON templates used by the Go gateway (referenced from `configs/workflows/*.yaml`).

## Placeholder Syntax

Use `{{variable_name}}` in any value. The gateway replaces placeholders before submitting the workflow.

Common variables:

- `prompt`
- `negative_prompt`
- `width`
- `height`
- `steps`
- `cfg`
- `seed`
- `denoise`
- `duration_sec`
- `fps`
- `fps_x2`
- `input_image`
- `input_mask`
- `lora_name`
- `lora_strength`
- `clip_name`
- `output_prefix`
- `reactor_enabled`
- `reactor_swap_model`
- `reactor_facedetection`
- `reactor_face_restore_model`
- `reactor_face_restore_visibility`
- `reactor_codeformer_weight`
- `reactor_detect_gender_input`
- `reactor_detect_gender_source`
- `reactor_input_faces_index`
- `reactor_source_faces_index`
- `reactor_console_log_level`

## Files

- `t2i_zimage_darkbeast.json`: `normal` type Z-Image two-stage workflow (RedCraft first pass + DarkBeast second pass).
- `t2i_zimage_darkbeast_red.json`: `red` type Z-Image main workflow.
- `t2i_zimage_darkbeast_red_side.json`: `red` type side-chain workflow (ModelOnly LoRA branch).
- `t2i_zimage_darkbeast_red_pose.json`: `red` type pose-priority workflow (LSP LoRA on model+clip).
- `t2i_zimage_darkbeast_simple_hybrid.json`: DarkBeast6 simple hybrid two-stage workflow with only the final pass saved.
- `inpaint_zimage_lock.json`: `normal` type Z-Image lock-generate workflow (DarkBeast ZiT two-stage + ReActor + FaceDetailer).
- `inpaint_zimage_lock_red.json`: `red` type Z-Image lock-generate workflow (preswap branch aligned to `t2i_zimage_darkbeast_red.json` main chain + ReActor + FaceDetailer).
- `t2v.json`: placeholder file, replace with your exported Wan/other T2V workflow.
