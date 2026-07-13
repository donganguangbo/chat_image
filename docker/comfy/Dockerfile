ARG CUDA_IMAGE=nvidia/cuda:13.0.2-cudnn-devel-ubuntu24.04
FROM ${CUDA_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/venv/bin:$PATH \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONUNBUFFERED=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        curl \
        ffmpeg \
        git \
        libgl1 \
        libglib2.0-0 \
        python3 \
        python3-dev \
        python3-pip \
        python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/venv

WORKDIR /opt/ComfyUI
COPY . .

RUN pip install --no-cache-dir --upgrade pip setuptools wheel \
    && pip install --no-cache-dir -r requirements.txt \
    && for requirements in custom_nodes/*/requirements.txt; do \
         [ ! -f "$requirements" ] || pip install --no-cache-dir -r "$requirements"; \
       done

CMD ["python", "-u", "main.py", "--listen", "0.0.0.0"]
