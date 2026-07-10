# Krea2 Text-to-Image API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the configured text-to-image workflow with a backend-executable Krea2 API graph while keeping `/v1/images/generations` unchanged.

**Architecture:** Keep the existing `ichat_text_to_image` workflow ID and route mapping. Replace only its workflow JSON and YAML bindings with the active Krea2 generation chain extracted from the approved UI workflow.

**Tech Stack:** ComfyUI prompt API JSON, YAML workflow bindings, Go configuration tests.

## Global Constraints

- Do not modify the source UI workflow.
- Do not modify the image-edit workflow.
- Preserve Krea2 Turbo, Krea2T Enhancer, Qwen3VL, Qwen Image VAE, and RES4LYF sampler behavior.
- Exclude disabled and UI-only nodes.
- Produce one saved image output.

---

### Task 1: Switch the text-to-image workflow

**Files:**
- Modify: `test/config_files_test.go`
- Modify: `workflows/ichat_text_to_image.json`
- Modify: `app/configs/workflows/ichat_text_to_image.yaml`

**Interfaces:**
- Consumes: image generation parameters `prompt`, `width`, `height`, `seed`, `steps`, and `cfg`.
- Produces: the existing `ichat_text_to_image` workflow with one `SaveImage` output.

- [x] **Step 1: Extend the configuration test**

Update `TestTextToImageWorkflowHasNoReferenceImages` to require these API nodes and bindings:

```go
wantNodes := map[string]string{
    "2":   "UNETLoader",
    "4":   "ComfyUI-Krea2T-Enhancer",
    "5":   "VAELoader",
    "6":   "CLIPLoader",
    "11":  "EmptyLatentImage",
    "114": "ConditioningZeroOut",
    "115": "CLIPTextEncode",
    "116": "ClownsharKSampler_Beta",
    "113": "VAEDecode",
    "158": "SaveImage",
}
```

Also require bindings from `prompt` to `115.text`, dimensions to node `11`, and sampler settings to node `116`.

- [x] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./test -run TestTextToImageWorkflowHasNoReferenceImages -count=1
```

Expected: FAIL because the current workflow still uses `CheckpointLoaderSimple`, `KSampler`, and old node bindings.

- [x] **Step 3: Replace the workflow with the minimal Krea2 API graph**

Create direct API nodes for the approved chain:

```text
2 UNETLoader -> 4 ComfyUI-Krea2T-Enhancer
6 CLIPLoader -> 115 CLIPTextEncode -> 114 ConditioningZeroOut
11 EmptyLatentImage
4 + 115 + 114 + 11 -> 116 ClownsharKSampler_Beta
116 + 5 VAELoader -> 113 VAEDecode -> 158 SaveImage
```

Use the source defaults: enhancer strength `1.5`, sampler `linear/euler`, scheduler `simple`, `8` steps, CFG `1`, and resolution `1080x1920`.

- [x] **Step 4: Update YAML bindings**

Bind:

```yaml
prompt: 115.text
seed: 116.seed
steps: 116.steps
cfg: 116.cfg
width: 11.width
height: 11.height
```

- [x] **Step 5: Run focused and full verification**

Run:

```bash
go test ./test -run TestTextToImageWorkflowHasNoReferenceImages -count=1
go test ./...
jq empty workflows/ichat_text_to_image.json
git diff --check
```

Expected: all commands succeed.

- [x] **Step 6: Commit the implementation**

```bash
git add test/config_files_test.go workflows/ichat_text_to_image.json app/configs/workflows/ichat_text_to_image.yaml docs/superpowers/plans/2026-07-10-krea2-text-to-image-api.md
git commit -m "Switch text image generation to Krea2"
```
