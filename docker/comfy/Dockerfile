# ComfyUI image for chat_image.
#
# Build from the repo root (needs docker/comfy/*):
#   docker build -f docker/comfy/Dockerfile -t chat-image-comfy:local .
#
# ComfyUI core and all 40 public custom nodes are cloned at build time, each
# pinned to the exact commit the production server runs. Those commits were
# recovered by matching the git blob SHA of every node's __init__.py against
# upstream history — production is provably NOT on upstream latest (RES4LYF runs
# 419de2d7 from 2026-06-15, whose __init__.py differs from v1.1.0 and main), and
# it provides ClownsharKSampler_Beta, which every workflow uses. Pins live in
# docker/comfy/node_pins.txt; verify them with scripts/verify_node_pins.sh.
#
# prompt_grid_16_pro has no public upstream (private node), so it is the one
# node vendored as source under docker/comfy/vendor_nodes/.
#
# Python deps come from requirements.lock.txt — a pip freeze of the production
# venv (302 packages), i.e. a combination known to work with all these nodes.
#
# Weights are NOT baked in: mount ./models at /models (see compose.yaml and
# docs/MODELS.md). Node-bundled checkpoints (controlnet_aux etc.) download on
# first use into the mounted /runtime.

ARG CUDA_IMAGE=nvidia/cuda:13.0.2-cudnn-devel-ubuntu24.04
FROM ${CUDA_IMAGE}

ARG COMFYUI_REF=v0.26.0
# torch must come from the CUDA 13 index; the lockfile pins bare versions
# (torch==2.12.1) and plain PyPI would resolve those to CPU builds.
ARG TORCH_INDEX_URL=https://download.pytorch.org/whl/cu130

ENV DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/venv/bin:$PATH \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONUNBUFFERED=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        ffmpeg \
        git \
        libgl1 \
        libglib2.0-0 \
        python3 \
        python3-dev \
        python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/venv

# ComfyUI core at a pinned tag. All LTXV nodes the workflows use
# (LTXVConditioning, LTXVImgToVideoInplace, LTXVLatentUpsampler, …) ship with
# core in comfy_extras/, so they need no custom node.
RUN git clone --depth 1 --branch ${COMFYUI_REF} \
        https://github.com/comfyanonymous/ComfyUI.git /opt/ComfyUI

WORKDIR /opt/ComfyUI

# Custom nodes, each at its pinned commit. Fetching just that commit keeps the
# layer small; --depth 1 on a specific rev needs the fetch form, not clone.
COPY docker/comfy/node_pins.txt /tmp/node_pins.txt
RUN set -eu; \
    grep -v '^#' /tmp/node_pins.txt | grep -v '^[[:space:]]*$' | \
    while IFS='|' read -r dir repo commit; do \
        echo "==> $dir @ ${commit%%[!0-9a-f]*}"; \
        mkdir -p "custom_nodes/$dir"; \
        cd "custom_nodes/$dir"; \
        git init -q .; \
        git remote add origin "https://github.com/$repo.git"; \
        git fetch -q --depth 1 origin "$commit"; \
        git checkout -q FETCH_HEAD; \
        rm -rf .git; \
        cd /opt/ComfyUI; \
    done; \
    rm /tmp/node_pins.txt; \
    # Upstream repos ship demo galleries and tokenizer data that the runtime
    # never loads — RES4LYF's example_workflows alone is ~215 MB, LTXVideo's
    # gemma_configs ~37 MB, plus ~140 MB of README gifs/screenshots. Node-bundled
    # checkpoints (controlnet_aux ckpts) are dropped too; they re-download into
    # the mounted /runtime on first use. Functional resources stay: *.cube LUTs,
    # haarcascade XML and vocab.txt are all read by node code.
    cd /opt/ComfyUI/custom_nodes; \
    rm -rf ComfyUI-LTXVideo/gemma_configs \
           ComfyUI-PainterI2V/image \
           ComfyUI_LayerStyle/image; \
    find . -maxdepth 2 -type d \( -name example_workflows -o -name examples -o -name ckpts \) \
        -exec rm -rf {} + 2>/dev/null || true; \
    find . -type f \( -name '*.gif' -o -name 'tokenizer.json' -o -name 'tokenizer.model' \) \
        -delete 2>/dev/null || true; \
    find . -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true

# The one node with no public upstream.
COPY docker/comfy/vendor_nodes/ /opt/ComfyUI/custom_nodes/

# torch first, from the CUDA index, so the lockfile step can't pull a CPU build.
RUN pip install --no-cache-dir --upgrade pip setuptools wheel \
    && pip install --no-cache-dir --index-url ${TORCH_INDEX_URL} \
        torch==2.12.1 torchvision==0.27.1 torchaudio==2.11.0

# Everything else, pinned to the production freeze. --no-deps stops pip from
# re-resolving (and re-downloading a CPU torch); the lock is already complete.
COPY docker/comfy/requirements.lock.txt /tmp/requirements.lock.txt
RUN pip install --no-cache-dir --no-deps -r /tmp/requirements.lock.txt \
    && rm /tmp/requirements.lock.txt

# Model paths: everything resolves under the mounted /models.
COPY docker/comfy/extra_model_paths.yaml /opt/ComfyUI/extra_model_paths.yaml

EXPOSE 8188
CMD ["python", "-u", "main.py", "--listen", "0.0.0.0"]
