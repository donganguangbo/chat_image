#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import io
import json
import mimetypes
import os
import re
import uuid
from contextlib import asynccontextmanager, suppress
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, AsyncIterator, Literal

import httpx
from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import RedirectResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, ConfigDict, Field

try:
    import websockets  # type: ignore
except Exception:  # pragma: no cover - optional dependency
    websockets = None

try:
    from PIL import Image  # type: ignore
except Exception:  # pragma: no cover - optional dependency
    Image = None  # type: ignore


ROOT_DIR = Path(os.getenv("GATEWAY_ROOT", Path.cwd())).resolve()
WORKFLOW_DIR = Path(os.getenv("WORKFLOW_DIR", ROOT_DIR / "workflows")).resolve()
UPLOAD_DIR = Path(os.getenv("UPLOAD_DIR", ROOT_DIR / "uploads")).resolve()
OUTPUT_DIR = Path(os.getenv("OUTPUT_DIR", ROOT_DIR / "outputs")).resolve()
IMAGE_OUTPUT_DIR = OUTPUT_DIR / "images"
VIDEO_OUTPUT_DIR = OUTPUT_DIR / "videos"
WEB_DIR = Path(os.getenv("WEB_DIR", ROOT_DIR / "web")).resolve()
PROJECT_MODELS_DIR = Path(os.getenv("PROJECT_MODELS_DIR", ROOT_DIR / "models")).expanduser().resolve()
LORA_DIR = Path(os.getenv("LORA_DIR", PROJECT_MODELS_DIR / "loras")).expanduser().resolve()

COMFY_URL = os.getenv("COMFY_URL", "http://127.0.0.1:8188").rstrip("/")
COMFY_TIMEOUT_SEC = int(os.getenv("COMFY_TIMEOUT_SEC", "1800"))
POLL_INTERVAL_SEC = float(os.getenv("POLL_INTERVAL_SEC", "1.0"))
IMAGE_OUTPUT_FORMAT = (os.getenv("IMAGE_OUTPUT_FORMAT", "image/avif") or "image/avif").strip().lower()

TaskType = Literal["t2i", "inpaint", "t2v", "i2v"]
TaskStatus = Literal["queued", "running", "completed", "failed"]
ZImageType = Literal["normal", "red", "futa", "furry"]
RedChainType = Literal["main", "side", "pose"]
WanRemixChainType = Literal["single", "first_last"]

ZIMAGE_T2I_WORKFLOW_MAP: dict[str, str] = {
    "normal": "t2i_zimage_darkbeast.json",
    "red": "t2i_zimage_darkbeast_red.json",
    "futa": "t2i_zimage_darkbeast_futa.json",
    "furry": "t2i_zimage_darkbeast_furry.json",
}
ZIMAGE_INPAINT_WORKFLOW_MAP: dict[str, str] = {
    "normal": "inpaint_zimage_lock.json",
    "red": "inpaint_zimage_lock_red.json",
    "futa": "inpaint_zimage_lock_futa.json",
    "furry": "inpaint_zimage_lock_furry.json",
}
WAN_REMIX_I2V_WORKFLOW_MAP: dict[str, str] = {
    "single": "i2v_wan_remix_single.json",
    "first_last": "i2v_wan_remix_first_last.json",
}
DEFAULT_VIDEO_NEGATIVE_PROMPT = (
    "lowres, blurry, out of focus, bad quality, jpeg artifacts, noisy, overexposed, underexposed, "
    "flicker, frame jitter, temporal inconsistency, identity drift, face morphing, duplicate face, "
    "deformed body, bad anatomy, bad hands, extra fingers, fused fingers, missing fingers, extra limbs, "
    "warped arms, text, watermark, logo"
)

FULL_VAR_PATTERN = re.compile(r"^\{\{\s*([a-zA-Z0-9_]+)\s*\}\}$")
INLINE_VAR_PATTERN = re.compile(r"\{\{\s*([a-zA-Z0-9_]+)\s*\}\}")
LORA_ALLOWED_EXTENSIONS = {".safetensors", ".pt", ".pth", ".ckpt", ".bin"}
MODEL_ALLOWED_EXTENSIONS = {".safetensors", ".ckpt", ".pt", ".pth", ".bin"}


def now_utc() -> datetime:
    return datetime.now(timezone.utc)


def normalize_seed(value: Any) -> int:
    try:
        seed = int(value)
    except Exception:
        seed = -1
    if seed >= 0:
        return seed
    return int.from_bytes(os.urandom(8), "big")


def clamp_float(value: Any, min_value: float, max_value: float, default: float) -> float:
    try:
        v = float(value)
    except Exception:
        return default
    if v < min_value:
        return min_value
    if v > max_value:
        return max_value
    return v


def parse_bool(value: Any, default: bool = False) -> bool:
    if isinstance(value, bool):
        return value
    if value is None:
        return default
    text = str(value).strip().lower()
    if text in {"1", "true", "yes", "on"}:
        return True
    if text in {"0", "false", "no", "off"}:
        return False
    return default


def normalize_zimage_type(value: Any) -> ZImageType:
    text = str(value or "").strip().lower()
    if text in {"red", "redcraft", "redzit", "红潮"}:
        return "red"
    if text in {"futa", "futanari", "扶她"}:
        return "futa"
    if text in {"furry", "anthro", "兽人"}:
        return "furry"
    return "normal"


def normalize_red_chain(value: Any) -> RedChainType:
    text = str(value or "").strip().lower()
    if text in {"pose", "lsp", "pose_lora", "姿势", "姿态"}:
        return "pose"
    if text in {"side", "aux", "branch", "侧链"}:
        return "side"
    return "main"


def normalize_wan_remix_chain(value: Any) -> WanRemixChainType:
    text = str(value or "").strip().lower()
    if text in {"first_last", "first-last", "firstlast", "double", "two", "首尾", "首尾帧"}:
        return "first_last"
    return "single"


def resolve_zimage_t2i_workflow_name(
    zimage_type: ZImageType,
    red_chain: RedChainType = "main",
) -> str:
    if zimage_type == "red" and red_chain == "pose":
        return "t2i_zimage_darkbeast_red_pose.json"
    if zimage_type == "red" and red_chain == "side":
        return "t2i_zimage_darkbeast_red_side.json"
    return ZIMAGE_T2I_WORKFLOW_MAP[zimage_type]


ZIMAGE_STRUCTURE_CONSTRAINT_PREFIX = (
    "single person, coherent full body anatomy, one head, one torso, "
    "two arms, two legs, two feet, natural joint connections, realistic limb proportions, "
    "legs connected to pelvis, arms connected to shoulders, hands connected to wrists, "
    "no duplicate limbs, no missing limbs, no detached limbs"
)


def has_similar_structure_constraints(prompt: str) -> bool:
    text = str(prompt or "").strip()
    if not text:
        return False
    lower = text.lower()

    # English cues
    has_arms = ("two arms" in lower) or ("2 arms" in lower)
    has_legs = ("two legs" in lower) or ("2 legs" in lower)
    has_integrity = any(
        token in lower
        for token in (
            "no duplicate limbs",
            "no extra limbs",
            "no missing limbs",
            "no detached limbs",
            "realistic anatomy",
            "coherent full body anatomy",
            "correct limb proportions",
            "natural joint connections",
        )
    )
    if has_arms and has_legs and has_integrity:
        return True

    # Chinese cues (fuzzy)
    zh_body_terms = (
        "单人",
        "一人",
        "一个人",
        "全身",
        "完整身体",
        "完整人体",
        "人体完整",
        "解剖正确",
        "解剖准确",
        "比例正确",
    )
    zh_arm_terms = ("双臂", "两臂", "两条手臂", "两只手臂")
    zh_leg_terms = ("双腿", "两腿", "两条腿", "双脚", "两只脚", "两脚")
    zh_integrity_terms = (
        "无多余肢体",
        "没有多余肢体",
        "无额外肢体",
        "无重复肢体",
        "无重复四肢",
        "无缺失肢体",
        "肢体完整",
        "四肢完整",
        "肢体连接自然",
        "关节连接正常",
    )

    body_hits = sum(1 for token in zh_body_terms if token in text)
    has_zh_arms = any(token in text for token in zh_arm_terms)
    has_zh_legs = any(token in text for token in zh_leg_terms)
    has_zh_integrity = any(token in text for token in zh_integrity_terms)

    return (has_zh_arms and has_zh_legs and has_zh_integrity) or (body_hits >= 2 and has_zh_integrity)


def apply_zimage_structure_constraints(params: dict[str, Any], model_source: str) -> None:
    """Decide whether Z-Image prompts need anatomy guardrails (without mutating prompt)."""
    if model_source not in {"zimage_darkbeast", "zimage_lock"}:
        return

    enabled = parse_bool(params.get("enable_structure_constraints", True), True)
    params["enable_structure_constraints"] = enabled

    prompt_raw = str(params.get("prompt_raw") or params.get("prompt") or "").strip()
    if not prompt_raw:
        params["structure_constraints_detected"] = False
        params["structure_constraints_applied"] = False
        params["structure_constraints_prefix"] = ""
        return
    params["prompt_raw"] = prompt_raw
    # Keep user prompt as-is for UI/task display.
    params["prompt"] = prompt_raw

    detected = has_similar_structure_constraints(prompt_raw)
    params["structure_constraints_detected"] = detected
    should_apply = enabled and (not detected)
    params["structure_constraints_applied"] = should_apply
    params["structure_constraints_prefix"] = ZIMAGE_STRUCTURE_CONSTRAINT_PREFIX if should_apply else ""


def apply_zimage_structure_constraints_to_clip(
    workflow: dict[str, Any],
    params: dict[str, Any],
    model_source: str,
) -> None:
    """Inject anatomy guardrails into positive CLIP text nodes only."""
    if model_source not in {"zimage_darkbeast", "zimage_lock"}:
        return
    if not parse_bool(params.get("enable_structure_constraints", True), True):
        return
    if not parse_bool(params.get("structure_constraints_applied", False), False):
        return

    prompt_raw = str(params.get("prompt_raw") or params.get("prompt") or "").strip()
    if not prompt_raw:
        return

    prefix = str(params.get("structure_constraints_prefix") or ZIMAGE_STRUCTURE_CONSTRAINT_PREFIX).strip()
    if not prefix:
        return
    final_prompt = f"{prefix}, {prompt_raw}"

    positive_node_ids: set[str] = set()
    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        if class_type in {"KSampler", "KSamplerAdvanced"}:
            positive = inputs.get("positive")
            if isinstance(positive, list) and len(positive) == 2:
                positive_node_ids.add(str(positive[0]))

    for node_id in positive_node_ids:
        node = workflow.get(node_id)
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        if class_type in {
            "CLIPTextEncode",
            "CLIPTextEncodeFlux",
            "CLIPTextEncodeSDXL",
            "CLIPTextEncodeSDXLRefiner",
        }:
            inputs["text"] = final_prompt

    params["prompt_for_clip"] = final_prompt


def apply_model_profile(params: dict[str, Any], task_type: TaskType) -> None:
    model_name = str(params.get("model_name") or "").strip().lower()
    if not model_name:
        return

    # GonzaLomo DMD family is highly sensitive to over-cooking.
    # Recommended ranges from model notes are low steps and low CFG.
    # We clamp aggressively to avoid the grotesque failures users observed.
    if "gonzalomo" in model_name and "dmd" in model_name:
        raw_steps = params.get("steps", 10)
        raw_cfg = params.get("cfg", 1.0)
        raw_lora_strength = params.get("lora_strength", 1.0)
        raw_denoise = params.get("denoise", 0.7)

        try:
            steps = int(raw_steps)
        except Exception:
            steps = 10
        steps = max(6, min(12, steps))

        cfg = clamp_float(raw_cfg, 1.0, 1.3, 1.0)
        lora_strength = clamp_float(raw_lora_strength, -4.0, 0.6, 0.45)

        params["steps"] = steps
        params["cfg"] = cfg
        params["lora_strength"] = lora_strength

        if task_type == "inpaint":
            denoise = clamp_float(raw_denoise, 0.45, 0.72, 0.68)
            params["denoise"] = denoise

        params["model_profile"] = {
            "name": "gonzalomo_dmd_safe",
            "applied": True,
            "steps": steps,
            "cfg": cfg,
            "lora_strength": lora_strength,
            "task_type": task_type,
        }


def sanitize_filename(name: str) -> str:
    return re.sub(r"[^a-zA-Z0-9._-]+", "_", name).strip("._") or "file.bin"


def parse_lora_names(value: Any) -> list[str]:
    if value is None:
        return []

    raw_items: list[Any] = []
    parsed_from_json = False
    if isinstance(value, list):
        raw_items = value
    else:
        text = str(value).strip()
        if not text:
            return []
        with suppress(Exception):
            parsed = json.loads(text)
            if isinstance(parsed, list):
                raw_items = parsed
                parsed_from_json = True
            elif isinstance(parsed, str):
                raw_items = [parsed]
                parsed_from_json = True
        if parsed_from_json and not raw_items:
            return []
        if not raw_items:
            raw_items = [part.strip() for part in re.split(r"[,\n]", text)]

    out: list[str] = []
    seen: set[str] = set()
    for item in raw_items:
        name = str(item or "").strip()
        if not name or name in seen:
            continue
        seen.add(name)
        out.append(name)
    return out


def ensure_dirs() -> None:
    WORKFLOW_DIR.mkdir(parents=True, exist_ok=True)
    UPLOAD_DIR.mkdir(parents=True, exist_ok=True)
    IMAGE_OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    VIDEO_OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    WEB_DIR.mkdir(parents=True, exist_ok=True)
    LORA_DIR.mkdir(parents=True, exist_ok=True)


def json_default(value: Any) -> str:
    if isinstance(value, datetime):
        return value.isoformat()
    raise TypeError(f"Unsupported type for JSON serialization: {type(value)!r}")


def render_template(data: Any, variables: dict[str, Any]) -> Any:
    if isinstance(data, str):
        full_match = FULL_VAR_PATTERN.match(data)
        if full_match:
            return variables.get(full_match.group(1), data)

        def repl(match: re.Match[str]) -> str:
            key = match.group(1)
            return str(variables.get(key, match.group(0)))

        return INLINE_VAR_PATTERN.sub(repl, data)

    if isinstance(data, list):
        return [render_template(item, variables) for item in data]

    if isinstance(data, dict):
        return {k: render_template(v, variables) for k, v in data.items()}

    return data


def is_ui_workflow(data: Any) -> bool:
    return isinstance(data, dict) and isinstance(data.get("nodes"), list) and isinstance(data.get("links"), list)


class T2VRequest(BaseModel):
    prompt: str = Field(min_length=1)
    negative_prompt: str = DEFAULT_VIDEO_NEGATIVE_PROMPT
    width: int = Field(default=832, ge=64, le=4096)
    height: int = Field(default=480, ge=64, le=4096)
    duration_sec: int = Field(default=4, ge=1, le=60)
    fps: int = Field(default=16, ge=1, le=60)
    steps: int = Field(default=8, ge=1, le=200)
    cfg: float = Field(default=1.0, ge=0.0, le=50.0)
    seed: int = -1


class T2IZImageRequest(BaseModel):
    zimage_type: str = Field(default="normal")
    red_chain: str = Field(default="main")
    prompt: str = Field(min_length=1)
    negative_prompt: str = ""
    width: int = Field(default=1024, ge=64, le=4096)
    height: int = Field(default=1024, ge=64, le=4096)
    seed: int = -1
    preset: str = "1024x1024"
    steps_stage1: int = Field(default=5, ge=1, le=60)
    cfg_stage1: float = Field(default=1.0, ge=0.0, le=20.0)
    steps_stage2: int = Field(default=20, ge=1, le=120)
    cfg_stage2: float = Field(default=1.3, ge=0.0, le=20.0)
    denoise_stage2: float = Field(default=0.58, ge=0.0, le=1.0)
    upscale_by: float = Field(default=1.25, ge=1.0, le=4.0)
    lora_strength_aux: float = Field(default=1.25, ge=-4.0, le=4.0)
    lora_strength_main: float = Field(default=-1.5, ge=-4.0, le=4.0)


class TaskRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    task_type: TaskType
    status: TaskStatus
    progress: int = 0
    message: str = ""
    params: dict[str, Any] = Field(default_factory=dict)
    outputs: list[dict[str, Any]] = Field(default_factory=list)
    error: str | None = None
    created_at: datetime = Field(default_factory=now_utc)
    updated_at: datetime = Field(default_factory=now_utc)
    started_at: datetime | None = None
    finished_at: datetime | None = None


class GalleryItem(BaseModel):
    kind: Literal["image", "video"]
    filename: str
    abs_path: str
    url: str
    size_bytes: int
    modified_at: datetime


class LoraItem(BaseModel):
    name: str
    abs_path: str
    size_bytes: int
    modified_at: datetime


class ModelItem(BaseModel):
    name: str
    rel_path: str
    abs_path: str
    size_bytes: int
    modified_at: datetime


class ModelCatalog(BaseModel):
    diffusion_models: list[ModelItem] = Field(default_factory=list)
    controlnet: list[ModelItem] = Field(default_factory=list)
    pulid: list[ModelItem] = Field(default_factory=list)
    vae: list[ModelItem] = Field(default_factory=list)
    text_encoders: list[ModelItem] = Field(default_factory=list)
    loras: list[ModelItem] = Field(default_factory=list)


class LoraDownloadRequest(BaseModel):
    repo_id: str = Field(min_length=3)
    filename: str = Field(min_length=1)
    token: str | None = None
    target_name: str | None = None
    force: bool = False


class EventBus:
    def __init__(self) -> None:
        self._subscribers: set[asyncio.Queue[str]] = set()
        self._lock = asyncio.Lock()

    async def subscribe(self) -> asyncio.Queue[str]:
        queue: asyncio.Queue[str] = asyncio.Queue(maxsize=200)
        async with self._lock:
            self._subscribers.add(queue)
        return queue

    async def unsubscribe(self, queue: asyncio.Queue[str]) -> None:
        async with self._lock:
            self._subscribers.discard(queue)

    async def publish(self, payload: dict[str, Any]) -> None:
        line = f"data: {json.dumps(payload, default=json_default, ensure_ascii=False)}\n\n"
        stale: list[asyncio.Queue[str]] = []

        async with self._lock:
            for queue in self._subscribers:
                try:
                    queue.put_nowait(line)
                except asyncio.QueueFull:
                    stale.append(queue)
            for queue in stale:
                self._subscribers.discard(queue)


class ComfyClient:
    def __init__(self, base_url: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.http = httpx.AsyncClient(timeout=httpx.Timeout(60.0, read=60.0))

    async def close(self) -> None:
        await self.http.aclose()

    async def upload_image(self, local_path: Path, subfolder: str = "gateway_uploads") -> dict[str, str]:
        if not local_path.exists():
            raise FileNotFoundError(f"Input file not found: {local_path}")

        mime = mimetypes.guess_type(local_path.name)[0] or "application/octet-stream"
        with local_path.open("rb") as fh:
            response = await self.http.post(
                f"{self.base_url}/upload/image",
                data={"overwrite": "false", "subfolder": subfolder},
                files={"image": (local_path.name, fh, mime)},
            )

        try:
            response.raise_for_status()
        except httpx.HTTPStatusError as exc:
            raise RuntimeError(f"ComfyUI upload failed: {exc.response.text}") from exc

        data = response.json()
        name = data.get("name") or data.get("filename")
        if not name:
            raise RuntimeError(f"ComfyUI upload response missing filename: {data}")
        returned_subfolder = data.get("subfolder", subfolder) or ""
        combined = f"{returned_subfolder}/{name}" if returned_subfolder else name
        return {
            "name": name,
            "subfolder": returned_subfolder,
            "combined": combined,
        }

    async def queue_prompt(self, workflow: dict[str, Any], client_id: str) -> str:
        response = await self.http.post(
            f"{self.base_url}/prompt",
            json={"prompt": workflow, "client_id": client_id},
        )
        try:
            response.raise_for_status()
        except httpx.HTTPStatusError as exc:
            raise RuntimeError(f"ComfyUI prompt failed: {exc.response.text}") from exc

        data = response.json()
        prompt_id = data.get("prompt_id")
        if not prompt_id:
            raise RuntimeError(f"ComfyUI prompt response missing prompt_id: {data}")
        return prompt_id

    async def get_history(self, prompt_id: str) -> dict[str, Any]:
        response = await self.http.get(f"{self.base_url}/history/{prompt_id}")
        response.raise_for_status()
        data = response.json()
        if prompt_id in data:
            return data[prompt_id]
        if len(data) == 1:
            return next(iter(data.values()))
        return {}

    async def get_object_info(self) -> dict[str, Any]:
        response = await self.http.get(f"{self.base_url}/object_info")
        response.raise_for_status()
        data = response.json()
        if isinstance(data, dict):
            return data
        return {}

    async def convert_workflow(self, ui_workflow: dict[str, Any]) -> dict[str, Any]:
        class_type_aliases = {
            # ComfyUI converter may occasionally output UI display names instead of class names.
            "Save Image": "SaveImage",
            "Load Image": "LoadImage",
            "Empty Latent Image": "EmptyLatentImage",
            "VAE Decode": "VAEDecode",
            "VAE Encode": "VAEEncode",
            "CLIP Text Encode": "CLIPTextEncode",
            "Image Upscale with Model": "ImageUpscaleWithModel",
        }

        def normalize_class_types(api_workflow: dict[str, Any]) -> dict[str, Any]:
            for node in api_workflow.values():
                if not isinstance(node, dict):
                    continue
                class_type = str(node.get("class_type") or "")
                mapped = class_type_aliases.get(class_type)
                if mapped:
                    node["class_type"] = mapped
            return api_workflow

        def is_api_workflow_shape(obj: Any) -> bool:
            if not isinstance(obj, dict) or not obj:
                return False
            values = list(obj.values())
            return all(isinstance(v, dict) and "class_type" in v for v in values)

        def extract_api_workflow(payload: Any) -> dict[str, Any] | None:
            stack: list[Any] = [payload]
            seen_ids: set[int] = set()
            preferred_keys = (
                "prompt",
                "workflow",
                "api_workflow",
                "converted_workflow",
                "graph",
                "result",
                "data",
                "output",
            )

            while stack:
                cur = stack.pop()
                cur_id = id(cur)
                if cur_id in seen_ids:
                    continue
                seen_ids.add(cur_id)

                if is_api_workflow_shape(cur):
                    return cur

                if isinstance(cur, str):
                    text = cur.strip()
                    if text and text[0] in "{[":
                        with suppress(Exception):
                            stack.append(json.loads(text))
                    continue

                if isinstance(cur, dict):
                    for key in preferred_keys:
                        if key in cur:
                            stack.append(cur.get(key))
                    for value in cur.values():
                        if isinstance(value, (dict, list, str)):
                            stack.append(value)
                    continue

                if isinstance(cur, list):
                    for item in cur:
                        if isinstance(item, (dict, list, str)):
                            stack.append(item)
            return None

        tried: list[str] = []
        errors: list[str] = []
        endpoints = [
            "/workflow/convert",
            "/cpe/workflow/convert",
            "/api/cpe/workflow/convert",
        ]
        request_variants: list[tuple[str, dict[str, Any]]] = [
            ("raw", ui_workflow),
            ("workflow", {"workflow": ui_workflow}),
            ("prompt", {"prompt": ui_workflow}),
            ("graph", {"graph": ui_workflow}),
            ("data", {"data": ui_workflow}),
        ]

        for path in endpoints:
            url = f"{self.base_url}{path}"
            tried.append(path)
            for variant_name, req_body in request_variants:
                try:
                    response = await self.http.post(url, json=req_body)
                    status = response.status_code
                    tag = f"{path} [{variant_name}]"
                    if status >= 400:
                        errors.append(f"{tag} -> {status}: {response.text[:220]}")
                        continue
                    payload = response.json()
                    api_workflow = extract_api_workflow(payload)
                    if api_workflow:
                        return normalize_class_types(api_workflow)
                    payload_type = type(payload).__name__
                    payload_keys = list(payload.keys())[:8] if isinstance(payload, dict) else []
                    body_preview = response.text[:220].replace("\n", " ")
                    errors.append(
                        f"{tag} -> 200 non-API shape (type={payload_type}, keys={payload_keys}, body={body_preview!r})"
                    )
                except Exception as exc:
                    errors.append(f"{path} [{variant_name}] -> {exc}")

        raise RuntimeError(
            "ComfyUI workflow convert failed on all known endpoints. "
            f"Tried: {', '.join(tried)} | Details: {'; '.join(errors)}"
        )

    async def wait_for_completion(
        self,
        prompt_id: str,
        client_id: str,
        progress_cb,
        timeout_sec: int = COMFY_TIMEOUT_SEC,
    ) -> None:
        deadline = asyncio.get_running_loop().time() + timeout_sec

        used_websocket = False
        if websockets is not None:
            try:
                await self._wait_with_websocket(prompt_id, client_id, progress_cb, deadline)
                used_websocket = True
            except Exception:
                used_websocket = False

        if not used_websocket:
            await self._wait_with_polling(prompt_id, progress_cb, deadline)

    async def _wait_with_websocket(
        self,
        prompt_id: str,
        client_id: str,
        progress_cb,
        deadline: float,
    ) -> None:
        ws_base = self.base_url.replace("http://", "ws://").replace("https://", "wss://")
        ws_url = f"{ws_base}/ws?clientId={client_id}"

        await progress_cb(2, "connected")
        async with websockets.connect(ws_url, ping_interval=20, ping_timeout=20) as ws:  # type: ignore[union-attr]
            while True:
                remaining = deadline - asyncio.get_running_loop().time()
                if remaining <= 0:
                    raise TimeoutError(f"Task timed out after {COMFY_TIMEOUT_SEC}s")

                try:
                    raw = await asyncio.wait_for(ws.recv(), timeout=min(5, remaining))
                except asyncio.TimeoutError:
                    history = await self.get_history(prompt_id)
                    if history:
                        await progress_cb(98, "finalizing")
                        return
                    continue

                if isinstance(raw, bytes):
                    continue

                message = json.loads(raw)
                message_type = message.get("type")
                data = message.get("data", {})
                event_prompt_id = data.get("prompt_id")

                if event_prompt_id and event_prompt_id != prompt_id:
                    continue

                if message_type == "progress":
                    current = max(0, int(data.get("value", 0)))
                    total = max(1, int(data.get("max", 1)))
                    pct = 5 + int((current / total) * 90)
                    await progress_cb(min(max(pct, 5), 95), "running")

                if message_type == "executing":
                    if data.get("node") is None:
                        await progress_cb(98, "finalizing")
                        return

                if message_type in {"execution_error", "error"}:
                    detail = data.get("exception_message") or message.get("message") or "ComfyUI execution error"
                    raise RuntimeError(detail)

    async def _wait_with_polling(self, prompt_id: str, progress_cb, deadline: float) -> None:
        progress = 5
        await progress_cb(progress, "running")

        while True:
            remaining = deadline - asyncio.get_running_loop().time()
            if remaining <= 0:
                raise TimeoutError(f"Task timed out after {COMFY_TIMEOUT_SEC}s")

            history = await self.get_history(prompt_id)
            if history:
                await progress_cb(98, "finalizing")
                return

            progress = min(95, progress + 2)
            await progress_cb(progress, "running")
            await asyncio.sleep(POLL_INTERVAL_SEC)

    async def download_outputs(
        self,
        history: dict[str, Any],
        task_id: str,
        task_type: TaskType,
        workflow: dict[str, Any] | None = None,
    ) -> list[dict[str, Any]]:
        collected: list[dict[str, Any]] = []
        outputs = history.get("outputs", {})
        save_node_ids: set[str] = set()

        if isinstance(workflow, dict):
            for node_id, node in workflow.items():
                if not isinstance(node, dict):
                    continue
                class_type = str(node.get("class_type") or "")
                if class_type in {"SaveImage", "VHS_VideoCombine", "SaveAnimatedWEBP"}:
                    save_node_ids.add(str(node_id))

        for node_id, node_output in outputs.items():
            if save_node_ids and str(node_id) not in save_node_ids:
                continue
            if not isinstance(node_output, dict):
                continue
            collected.extend(await self._collect_kind(node_output.get("images", []), "image", task_id, task_type))
            collected.extend(await self._collect_kind(node_output.get("videos", []), "video", task_id, task_type))
            collected.extend(await self._collect_kind(node_output.get("gifs", []), "video", task_id, task_type))

            # Some custom nodes/formats (e.g. animated AVIF/WebP) may expose
            # outputs under non-standard keys. Collect those dynamically.
            for key, value in node_output.items():
                if key in {"images", "videos", "gifs"}:
                    continue
                if not isinstance(value, list) or not value:
                    continue
                if not any(isinstance(item, dict) and item.get("filename") for item in value):
                    continue
                key_lower = str(key).lower()
                inferred_kind: Literal["image", "video"] = "image"
                if (
                    "video" in key_lower
                    or "gif" in key_lower
                    or "anim" in key_lower
                    or "avif" in key_lower
                    or "webp" in key_lower
                ):
                    inferred_kind = "video"
                collected.extend(await self._collect_kind(value, inferred_kind, task_id, task_type))

        if collected:
            dedup: dict[tuple[str, str], dict[str, Any]] = {}
            for item in collected:
                source_filename = str(item.get("source_filename") or "")
                source_subfolder = str(item.get("source_subfolder") or "")
                dedup[(source_filename, source_subfolder)] = item
            collected = list(dedup.values())

        return collected

    async def _collect_kind(
        self,
        files: list[dict[str, Any]],
        kind: Literal["image", "video"],
        task_id: str,
        task_type: TaskType,
    ) -> list[dict[str, Any]]:
        result: list[dict[str, Any]] = []
        for item in files:
            filename = item.get("filename")
            if not filename:
                continue
            subfolder = item.get("subfolder", "")
            source_type = str(item.get("type", "output") or "output").strip().lower()
            # ComfyUI may echo uploaded source assets as type=input; skip them so the UI
            # only shows generated artifacts.
            if source_type == "input":
                continue

            response = await self.http.get(
                f"{self.base_url}/view",
                params={
                    "filename": filename,
                    "subfolder": subfolder,
                    "type": source_type,
                },
            )
            response.raise_for_status()

            ext = Path(filename).suffix or (".mp4" if kind == "video" else ".png")
            effective_kind: Literal["image", "video"] = kind
            if (
                kind == "image"
                and task_type in {"t2v", "i2v"}
                and ext.lower() in {".avif", ".webp", ".gif"}
            ):
                # Animated image containers from video pipelines should be treated as video outputs.
                effective_kind = "video"
            content = response.content
            if effective_kind == "image":
                content, ext = convert_image_bytes_if_needed(content, ext, task_type)
            stamp = now_utc().strftime("%Y%m%d_%H%M%S")
            target_name = f"{stamp}_{task_type}_{task_id[:8]}_{uuid.uuid4().hex[:6]}{ext}"
            target_dir = VIDEO_OUTPUT_DIR if effective_kind == "video" else IMAGE_OUTPUT_DIR
            target_path = target_dir / target_name
            target_path.write_bytes(content)

            result.append(
                {
                    "kind": effective_kind,
                    "filename": target_name,
                    "abs_path": str(target_path),
                    "url": f"/outputs/{'videos' if effective_kind == 'video' else 'images'}/{target_name}",
                    "source_filename": filename,
                    "source_subfolder": subfolder,
                }
            )
        return result


def extract_comfy_history_error(history: dict[str, Any]) -> str:
    status = history.get("status")
    if not isinstance(status, dict):
        return ""

    status_str = str(status.get("status_str") or "").strip().lower()
    messages = status.get("messages")
    details: list[str] = []
    if isinstance(messages, list):
        for item in messages[-8:]:
            if not isinstance(item, (list, tuple)) or len(item) < 2:
                continue
            event = str(item[0] or "").strip().lower()
            payload = item[1]
            if event not in {"execution_error", "error"}:
                continue
            if isinstance(payload, dict):
                text = (
                    payload.get("exception_message")
                    or payload.get("message")
                    or payload.get("exception_type")
                    or ""
                )
                if not text:
                    text = json.dumps(payload, ensure_ascii=False)
            else:
                text = str(payload)
            text = text.strip()
            if text:
                details.append(text)

    if details:
        return " | ".join(details)
    if status_str and status_str not in {"success", "completed"}:
        return f"status={status_str}"
    return ""


def has_inpaint_after_output(outputs: list[dict[str, Any]]) -> bool:
    for item in outputs or []:
        source = str(item.get("source_filename") or "").lower()
        if "_after" in source and "_after_swap" not in source and "_before" not in source:
            return True
    return False


def summarize_output_sources(outputs: list[dict[str, Any]]) -> str:
    if not outputs:
        return "none"
    parts: list[str] = []
    for item in outputs:
        src = str(item.get("source_filename") or "").strip()
        sub = str(item.get("source_subfolder") or "").strip()
        if not src:
            continue
        parts.append(f"{src}|{sub}")
    if not parts:
        return "none"
    return "; ".join(parts[:12])


tasks: dict[str, TaskRecord] = {}
tasks_lock = asyncio.Lock()
task_queue: asyncio.Queue[str] = asyncio.Queue()
event_bus = EventBus()
comfy_client = ComfyClient(COMFY_URL)
worker_task: asyncio.Task[None] | None = None


async def read_task(task_id: str) -> TaskRecord:
    async with tasks_lock:
        task = tasks.get(task_id)
        if not task:
            raise HTTPException(status_code=404, detail=f"Task not found: {task_id}")
        return task.model_copy(deep=True)


async def update_task(task_id: str, **changes: Any) -> TaskRecord:
    async with tasks_lock:
        task = tasks.get(task_id)
        if not task:
            raise HTTPException(status_code=404, detail=f"Task not found: {task_id}")
        for key, value in changes.items():
            setattr(task, key, value)
        task.updated_at = now_utc()
        snapshot = task.model_copy(deep=True)

    await event_bus.publish({"event": "task_update", "task": snapshot.model_dump(mode="json")})
    return snapshot


async def create_task(task_type: TaskType, params: dict[str, Any]) -> TaskRecord:
    task_id = uuid.uuid4().hex
    task = TaskRecord(
        id=task_id,
        task_type=task_type,
        status="queued",
        progress=0,
        message="queued",
        params=params,
    )

    async with tasks_lock:
        tasks[task_id] = task
        snapshot = task.model_copy(deep=True)

    await event_bus.publish({"event": "task_created", "task": snapshot.model_dump(mode="json")})
    await task_queue.put(task_id)
    return snapshot


def load_workflow_template(
    task_type: TaskType,
    model_source: str = "",
    zimage_type: Any = "normal",
    red_chain: Any = "main",
    wan_remix_chain: Any = "single",
) -> dict[str, Any]:
    normalized_type = normalize_zimage_type(zimage_type)
    normalized_red_chain = normalize_red_chain(red_chain)
    normalized_wan_remix_chain = normalize_wan_remix_chain(wan_remix_chain)
    if task_type == "i2v" and model_source != "wan_remix":
        raise RuntimeError("Legacy /api/i2v flow has been removed. Use /api/i2v_wan_remix.")
    workflow_name = f"{task_type}.json"
    if task_type == "inpaint" and model_source == "zimage_lock":
        workflow_name = ZIMAGE_INPAINT_WORKFLOW_MAP[normalized_type]
    if task_type == "t2i" and model_source == "zimage_darkbeast":
        workflow_name = resolve_zimage_t2i_workflow_name(normalized_type, normalized_red_chain)
    if task_type == "i2v" and model_source == "wan_remix":
        workflow_name = WAN_REMIX_I2V_WORKFLOW_MAP[normalized_wan_remix_chain]
    workflow_path = WORKFLOW_DIR / workflow_name

    if not workflow_path.exists():
        raise FileNotFoundError(
            f"Workflow template not found: {workflow_path}. "
            f"Create this file with exported ComfyUI JSON and placeholders like {{prompt}}."
        )
    with workflow_path.open("r", encoding="utf-8") as fh:
        workflow = json.load(fh)
    if workflow.get("__placeholder__"):
        raise RuntimeError(
            f"Workflow template {workflow_path} is still a placeholder. "
            "Replace it with an exported ComfyUI API JSON workflow."
        )
    return workflow


async def run_workflow_task(task: TaskRecord) -> list[dict[str, Any]]:
    params = dict(task.params)
    engine = str(params.get("engine") or params.get("model_source") or "").strip().lower()
    wan_remix_chain = normalize_wan_remix_chain(params.get("wan_remix_chain", "single"))
    if task.task_type == "t2i":
        auto_configure_t2i_zimage_darkbeast_bundle(params)
        model_source = "zimage_darkbeast"
        engine = "zimage_darkbeast"
    elif task.task_type == "inpaint":
        auto_configure_inpaint_zimage_lock_bundle(params)
        model_source = "zimage_lock"
        engine = "zimage_lock"
    elif task.task_type == "i2v":
        requested_model_source = str(params.get("model_source") or "").strip().lower()
        if requested_model_source == "wan_remix":
            model_source = "wan_remix"
            engine = "wan_remix"
        else:
            raise RuntimeError("Legacy /api/i2v flow has been removed. Please use /api/i2v_wan_remix.")
    else:
        model_source = ""
        engine = ""

    zimage_type = normalize_zimage_type(params.get("zimage_type", "normal"))
    red_chain = normalize_red_chain(params.get("red_chain", "main"))
    if zimage_type != "red":
        red_chain = "main"
    if model_source != "wan_remix":
        wan_remix_chain = "single"

    model_name = str(params.get("model_name") or "").strip()
    clip_name = str(params.get("clip_name") or "").strip()
    vae_name = str(params.get("vae_name") or "").strip()
    if model_source == "wan_remix":
        model_name = (
            "Wan2.2_Remix_NSFW_i2v_14b_high_lighting_v2.0.safetensors + "
            "Wan2.2_Remix_NSFW_i2v_14b_low_lighting_v2.0.safetensors"
        )
        clip_name = "umt5_xxl_fp16.safetensors"
        vae_name = "wan_2.1_vae.safetensors"
    params["seed"] = normalize_seed(params.get("seed", -1))
    params["engine"] = engine
    params["model_source"] = model_source
    params["zimage_type"] = zimage_type
    params["red_chain"] = red_chain
    params["wan_remix_chain"] = wan_remix_chain
    params["model_name"] = model_name
    params["clip_name"] = clip_name
    params["vae_name"] = vae_name
    params["ckpt_name"] = model_name
    params["unet_name"] = model_name
    if model_source == "wan_remix":
        params["unet_name_stage1"] = "Wan2.2_Remix_NSFW_i2v_14b_high_lighting_v2.0.safetensors"
        params["unet_name_stage2"] = "Wan2.2_Remix_NSFW_i2v_14b_low_lighting_v2.0.safetensors"
    try:
        fps_value = int(params.get("fps", 16))
    except Exception:
        fps_value = 16
    if fps_value < 1:
        fps_value = 1
    params["fps_x2"] = fps_value * 2
    try:
        duration_sec = int(params.get("duration_sec", 4))
    except Exception:
        duration_sec = 4
    duration_sec = max(1, min(60, duration_sec))
    params["duration_sec"] = duration_sec
    params["length_frames"] = max(2, duration_sec * fps_value + 1)
    try:
        width_value = int(params.get("width", 832))
    except Exception:
        width_value = 832
    try:
        height_value = int(params.get("height", 480))
    except Exception:
        height_value = 480
    params["size_long"] = max(64, min(4096, max(width_value, height_value)))

    if task.task_type in {"inpaint", "i2v"}:
        image_path = Path(params.get("image_path", ""))
        uploaded = await comfy_client.upload_image(image_path, subfolder="gateway_uploads")
        params["input_image"] = uploaded["combined"]
        params["input_image_name"] = uploaded["name"]
        params["input_image_subfolder"] = uploaded["subfolder"]
        if task.task_type == "i2v" and model_source == "wan_remix":
            if wan_remix_chain == "first_last":
                end_image_path = Path(str(params.get("image_end_path") or ""))
                if not end_image_path.exists():
                    raise RuntimeError("Wan Remix 首尾帧链路缺少第二张图片（image_end）。")
                uploaded_end = await comfy_client.upload_image(end_image_path, subfolder="gateway_uploads")
                params["input_image_end"] = uploaded_end["combined"]
                params["input_image_end_name"] = uploaded_end["name"]
                params["input_image_end_subfolder"] = uploaded_end["subfolder"]
            else:
                params["input_image_end"] = params["input_image"]

    apply_model_profile(params, task.task_type)
    apply_zimage_structure_constraints(params, model_source)

    # ReActor params for lock-generate workflow (T2I + face swap).
    if task.task_type == "inpaint":
        params["reactor_enabled"] = parse_bool(params.get("reactor_enabled", True), True)
        params["refine_enabled"] = parse_bool(params.get("refine_enabled", False), False)
        params["reactor_swap_model"] = str(params.get("reactor_swap_model") or "inswapper_128.onnx").strip()
        facedetection_default = "retinaface_resnet50"
        facedetection = str(params.get("reactor_facedetection") or facedetection_default).strip()
        if facedetection not in {"retinaface_resnet50", "retinaface_mobile0.25", "YOLOv5l", "YOLOv5n"}:
            facedetection = facedetection_default
        params["reactor_facedetection"] = facedetection
        params["reactor_face_restore_model"] = str(params.get("reactor_face_restore_model") or "codeformer-v0.1.0.pth").strip() or "codeformer-v0.1.0.pth"
        params["reactor_face_restore_visibility"] = clamp_float(
            params.get("reactor_face_restore_visibility", 0.8), 0.1, 1.0, 0.8
        )
        params["reactor_codeformer_weight"] = clamp_float(params.get("reactor_codeformer_weight", 0.5), 0.0, 1.0, 0.5)
        detect_gender_input = str(params.get("reactor_detect_gender_input") or "no").strip().lower()
        detect_gender_source = str(params.get("reactor_detect_gender_source") or "no").strip().lower()
        params["reactor_detect_gender_input"] = detect_gender_input if detect_gender_input in {"no", "female", "male"} else "no"
        params["reactor_detect_gender_source"] = (
            detect_gender_source if detect_gender_source in {"no", "female", "male"} else "no"
        )
        params["reactor_input_faces_index"] = str(params.get("reactor_input_faces_index") or "0").strip() or "0"
        params["reactor_source_faces_index"] = str(params.get("reactor_source_faces_index") or "0").strip() or "0"
        try:
            console_log_level = int(params.get("reactor_console_log_level", 1))
        except Exception:
            console_log_level = 1
        params["reactor_console_log_level"] = max(0, min(2, console_log_level))
        object_info: dict[str, Any] = {}
        with suppress(Exception):
            object_info = await comfy_client.get_object_info()
        if object_info and "ReActorFaceSwap" not in object_info:
            raise RuntimeError(
                "ComfyUI missing node: ReActorFaceSwap. Install ComfyUI-ReActor and restart ComfyUI."
            )

    template = load_workflow_template(
        task.task_type,
        model_source=model_source,
        zimage_type=zimage_type,
        red_chain=red_chain,
        wan_remix_chain=wan_remix_chain,
    )
    if is_ui_workflow(template):
        if task.task_type == "t2v":
            template = await comfy_client.convert_workflow(template)
            params["workflow_converted_from_ui"] = True
        else:
            raise RuntimeError(
                "Workflow is ComfyUI UI-format (nodes/links). "
                "Please provide API-format workflow JSON for this task."
            )

    output_prefix = f"{task.task_type}/{now_utc().strftime('%Y%m%d_%H%M%S')}_{task.id[:8]}"
    params["output_prefix"] = output_prefix
    params["task_id"] = task.id

    workflow = render_template(template, params)
    if model_source == "wan_remix":
        apply_wan_remix_runtime_fixes(workflow)
    if task.task_type == "t2v":
        apply_t2v_runtime_overrides(workflow, params)
    apply_zimage_structure_constraints_to_clip(workflow, params, model_source)
    if task.task_type == "inpaint" and model_source == "zimage_lock":
        apply_inpaint_zimage_refine_switch(workflow, parse_bool(params.get("refine_enabled", False), False))
    if model_source and model_source not in {"zimage_darkbeast", "zimage_lock", "wan_remix"}:
        apply_selected_model_name(workflow, model_name)
        apply_selected_clip_name(workflow, clip_name)
        apply_selected_vae(workflow, vae_name)

    lora_names = parse_lora_names(params.get("lora_names"))
    if model_source in {"zimage_darkbeast", "zimage_lock"}:
        lora_names = []
        params["lora_name"] = ""
    if not lora_names:
        single_lora_name = str(params.get("lora_name") or "").strip()
        if single_lora_name:
            lora_names = [single_lora_name]
    params["lora_names"] = lora_names
    if lora_names:
        params["lora_name"] = lora_names[0]

    requested_lora_strength = clamp_float(params.get("lora_strength", 1.0), -4.0, 4.0, 1.0)
    effective_lora_strength = requested_lora_strength
    if len(lora_names) > 1 and requested_lora_strength > 0:
        # Multi-LoRA guardrail: spread positive strength across selected LoRAs
        # to avoid overcooked outputs and anatomy collapse.
        effective_lora_strength = requested_lora_strength / float(len(lora_names))
        params["lora_strength_scaled_from"] = requested_lora_strength
    params["lora_strength_effective"] = effective_lora_strength

    lora_debug_list: list[dict[str, Any]] = []
    for lora_name in lora_names:
        lora_debug = apply_selected_lora(workflow, lora_name, effective_lora_strength)
        if lora_debug is not None:
            lora_debug_list.append(lora_debug)
    if lora_debug_list:
        params["lora_debug"] = lora_debug_list
    client_id = uuid.uuid4().hex
    prompt_id = await comfy_client.queue_prompt(workflow, client_id)
    await update_task(task.id, progress=3, message=f"submitted:{prompt_id}", params=params)

    async def progress_cb(progress: int, message: str) -> None:
        await update_task(task.id, progress=progress, message=message)

    await comfy_client.wait_for_completion(prompt_id, client_id, progress_cb, timeout_sec=COMFY_TIMEOUT_SEC)
    history = await comfy_client.get_history(prompt_id)
    if not history:
        raise RuntimeError(f"ComfyUI finished but no history found for prompt_id={prompt_id}")
    outputs = await comfy_client.download_outputs(history, task.id, task.task_type, workflow=workflow)
    if task.task_type == "inpaint" and not has_inpaint_after_output(outputs):
        fallback_workflow = json.loads(json.dumps(workflow))
        apply_inpaint_zimage_no_swap_fallback(fallback_workflow)
        fallback_client_id = uuid.uuid4().hex
        fallback_prompt_id = await comfy_client.queue_prompt(fallback_workflow, fallback_client_id)
        await update_task(task.id, progress=60, message=f"retry_no_swap:{fallback_prompt_id}")
        await comfy_client.wait_for_completion(
            fallback_prompt_id,
            fallback_client_id,
            progress_cb,
            timeout_sec=COMFY_TIMEOUT_SEC,
        )
        fallback_history = await comfy_client.get_history(fallback_prompt_id)
        if not fallback_history:
            raise RuntimeError(f"Inpaint fallback finished but no history found for prompt_id={fallback_prompt_id}")
        fallback_outputs = await comfy_client.download_outputs(
            fallback_history,
            task.id,
            task.task_type,
            workflow=fallback_workflow,
        )
        if has_inpaint_after_output(fallback_outputs):
            outputs = fallback_outputs
        else:
            raise RuntimeError(
                "Inpaint produced no '_after' output "
                f"(initial={summarize_output_sources(outputs)}; fallback={summarize_output_sources(fallback_outputs)})"
            )
    if not outputs:
        # Fallback: if save nodes produced nothing, try collecting any generated preview/temp outputs.
        outputs = await comfy_client.download_outputs(history, task.id, task.task_type, workflow=None)
    if not outputs:
        history_error = extract_comfy_history_error(history)
        if history_error:
            raise RuntimeError(f"ComfyUI produced no output files: {history_error}")
        output_keys: list[str] = []
        history_outputs = history.get("outputs")
        if isinstance(history_outputs, dict):
            for node_output in history_outputs.values():
                if isinstance(node_output, dict):
                    output_keys.extend(str(k) for k in node_output.keys())
        if output_keys:
            unique_keys = ", ".join(sorted(set(output_keys)))
            raise RuntimeError(
                "ComfyUI task completed but no output files were produced. "
                f"History output keys seen: {unique_keys}"
            )
        raise RuntimeError(
            "ComfyUI task completed but no output files were produced. "
            "Check SaveImage nodes and ComfyUI output directory permissions."
        )
    return outputs


async def worker_loop() -> None:
    while True:
        task_id = await task_queue.get()
        try:
            task = await read_task(task_id)
            await update_task(
                task.id,
                status="running",
                started_at=now_utc(),
                progress=1,
                message="starting",
                error=None,
            )
            outputs = await run_workflow_task(task)
            await update_task(
                task.id,
                status="completed",
                progress=100,
                message="done",
                outputs=outputs,
                finished_at=now_utc(),
            )
        except Exception as exc:
            await update_task(
                task_id,
                status="failed",
                message="failed",
                error=str(exc),
                finished_at=now_utc(),
            )
        finally:
            task_queue.task_done()


async def save_upload(upload: UploadFile, target_dir: Path) -> Path:
    safe_name = sanitize_filename(upload.filename or "upload.bin")
    file_name = f"{now_utc().strftime('%Y%m%d_%H%M%S')}_{uuid.uuid4().hex[:8]}_{safe_name}"
    target_path = target_dir / file_name
    with target_path.open("wb") as fh:
        while True:
            chunk = await upload.read(1024 * 1024)
            if not chunk:
                break
            fh.write(chunk)
    await upload.close()
    return target_path


def ensure_lora_extension(filename: str) -> None:
    ext = Path(filename).suffix.lower()
    if ext not in LORA_ALLOWED_EXTENSIONS:
        allowed = ", ".join(sorted(LORA_ALLOWED_EXTENSIONS))
        raise HTTPException(status_code=400, detail=f"Invalid LoRA extension: {ext}. Allowed: {allowed}")


def get_safe_lora_path(name: str) -> Path:
    safe = sanitize_filename(name)
    if safe != name:
        raise HTTPException(status_code=400, detail="Invalid LoRA filename.")
    if "/" in name or "\\" in name:
        raise HTTPException(status_code=400, detail="Invalid LoRA filename.")
    ensure_lora_extension(name)
    return (LORA_DIR / name).resolve()


def lora_item_from_path(path: Path) -> LoraItem:
    stat = path.stat()
    return LoraItem(
        name=path.name,
        abs_path=str(path.resolve()),
        size_bytes=stat.st_size,
        modified_at=datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
    )


def list_loras(limit: int) -> list[LoraItem]:
    items: list[LoraItem] = []
    for file_path in LORA_DIR.glob("*"):
        if not file_path.is_file():
            continue
        if file_path.suffix.lower() not in LORA_ALLOWED_EXTENSIONS:
            continue
        items.append(lora_item_from_path(file_path))
    items.sort(key=lambda item: item.modified_at, reverse=True)
    return items[: max(1, min(limit, 2000))]


def model_item_from_path(path: Path, root: Path) -> ModelItem:
    stat = path.stat()
    rel_path = str(path.relative_to(root)).replace("\\", "/")
    return ModelItem(
        name=path.name,
        rel_path=rel_path,
        abs_path=str(path.resolve()),
        size_bytes=stat.st_size,
        modified_at=datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
    )


def list_model_files(root: Path, limit: int) -> list[ModelItem]:
    if not root.exists() or not root.is_dir():
        return []

    items: list[ModelItem] = []
    for file_path in root.rglob("*"):
        if not file_path.is_file():
            continue
        if file_path.suffix.lower() not in MODEL_ALLOWED_EXTENSIONS:
            continue
        items.append(model_item_from_path(file_path, root))

    items.sort(key=lambda item: item.rel_path.lower())
    return items[: max(1, min(limit, 5000))]


def list_models(limit: int) -> ModelCatalog:
    return ModelCatalog(
        diffusion_models=list_model_files(PROJECT_MODELS_DIR / "diffusion_models", limit),
        controlnet=list_model_files(PROJECT_MODELS_DIR / "controlnet", limit),
        pulid=list_model_files(PROJECT_MODELS_DIR / "pulid", limit),
        vae=list_model_files(PROJECT_MODELS_DIR / "vae", limit),
        text_encoders=list_model_files(PROJECT_MODELS_DIR / "text_encoders", limit),
        loras=list_model_files(LORA_DIR, limit),
    )


def parse_preset_size(preset: Any, width: Any, height: Any) -> tuple[int, int]:
    default_width = 1024
    default_height = 1024
    try:
        default_width = int(width)
        default_height = int(height)
    except Exception:
        pass
    default_width = max(64, min(4096, default_width))
    default_height = max(64, min(4096, default_height))

    text = str(preset or "").strip()
    m = re.match(r"^(\d{2,5})[xX](\d{2,5})$", text)
    if not m:
        return default_width, default_height
    w = max(64, min(4096, int(m.group(1))))
    h = max(64, min(4096, int(m.group(2))))
    return w, h


def select_path_from_candidates(
    available_paths: list[str],
    exact_candidates: list[str],
    keyword_groups: list[list[str]],
) -> str:
    if not available_paths:
        return ""

    by_rel: dict[str, str] = {}
    by_base: dict[str, str] = {}
    for path in available_paths:
        lower_rel = path.lower()
        lower_base = Path(path).name.lower()
        by_rel.setdefault(lower_rel, path)
        by_base.setdefault(lower_base, path)

    for name in exact_candidates:
        lower = str(name or "").strip().lower()
        if not lower:
            continue
        if lower in by_rel:
            return by_rel[lower]
        if lower in by_base:
            return by_base[lower]

    for group in keyword_groups:
        tokens = [str(token).strip().lower() for token in group if str(token).strip()]
        if not tokens:
            continue
        for path in available_paths:
            lower = path.lower()
            if all(token in lower for token in tokens):
                return path
    return ""


def auto_configure_t2i_zimage_darkbeast_bundle(params: dict[str, Any]) -> None:
    zimage_type = normalize_zimage_type(params.get("zimage_type", "normal"))
    red_chain = normalize_red_chain(params.get("red_chain", "main"))
    if zimage_type != "red":
        red_chain = "main"
    params["zimage_type"] = zimage_type
    params["red_chain"] = red_chain
    width, height = parse_preset_size(params.get("preset"), params.get("width"), params.get("height"))
    params["width"] = width
    params["height"] = height

    try:
        steps_stage1 = int(params.get("steps_stage1", 5))
    except Exception:
        steps_stage1 = 5
    params["steps_stage1"] = max(1, min(60, steps_stage1))
    # KSamplerAdvanced uses [start_at_step, end_at_step] window.
    # Keep this aligned with the original REDZ workflow semantics.
    params["end_at_step_stage1"] = max(0, params["steps_stage1"] - 1)
    params["cfg_stage1"] = clamp_float(params.get("cfg_stage1", 1.5), 0.0, 3.0, 1.5)
    try:
        steps_stage2 = int(params.get("steps_stage2", 20))
    except Exception:
        steps_stage2 = 20
    params["steps_stage2"] = max(1, min(120, steps_stage2))
    params["cfg_stage2"] = clamp_float(params.get("cfg_stage2", 1.3), 0.0, 20.0, 1.3)
    params["denoise_stage2"] = clamp_float(params.get("denoise_stage2", 0.58), 0.0, 1.0, 0.58)
    params["upscale_by"] = clamp_float(params.get("upscale_by", 1.25), 1.0, 4.0, 1.25)

    # RED workflow is tuned for fixed canonical settings in original REDZ15AIO flow.
    # If caller still sends legacy generic defaults, auto-upgrade to RED defaults.
    if zimage_type == "red":
        red_default_like = (
            params["width"] == 1024
            and params["height"] == 1024
            and params["steps_stage1"] == 5
            and abs(float(params["cfg_stage1"]) - 1.5) < 1e-9
            and params["steps_stage2"] == 20
            and abs(float(params["cfg_stage2"]) - 1.3) < 1e-9
            and abs(float(params["denoise_stage2"]) - 0.58) < 1e-9
            and abs(float(params["upscale_by"]) - 1.25) < 1e-9
        )
        if red_default_like:
            params["width"] = 832
            params["height"] = 1536
            params["preset"] = "832x1536"
            if red_chain == "pose":
                # Pose-priority profile: strengthen semantic adherence in both stages.
                params["steps_stage1"] = 14
                params["cfg_stage1"] = 1.6
                params["steps_stage2"] = 10
                params["cfg_stage2"] = 1.35
                params["denoise_stage2"] = 0.45
            else:
                params["steps_stage1"] = 8
                params["cfg_stage1"] = 1.0
                params["steps_stage2"] = 4
                params["cfg_stage2"] = 1.0
                params["denoise_stage2"] = 0.33
            params["upscale_by"] = 2.0

    diffusion_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "diffusion_models", 5000)]
    checkpoint_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "checkpoints", 5000)]
    all_model_paths = checkpoint_paths + diffusion_paths
    workflow_name = resolve_zimage_t2i_workflow_name(zimage_type, red_chain)
    workflow_path = WORKFLOW_DIR / workflow_name
    uses_checkpoint_loader = False
    workflow_has_clip_loader = False
    workflow_has_vae_loader = False
    with suppress(Exception):
        workflow_data = json.loads(workflow_path.read_text(encoding="utf-8"))
        if isinstance(workflow_data, dict):
            uses_checkpoint_loader = any(
                isinstance(node, dict) and str(node.get("class_type") or "") == "CheckpointLoaderSimple"
                for node in workflow_data.values()
            )
            workflow_has_clip_loader = any(
                isinstance(node, dict) and str(node.get("class_type") or "") == "CLIPLoader"
                for node in workflow_data.values()
            )
            workflow_has_vae_loader = any(
                isinstance(node, dict) and str(node.get("class_type") or "") == "VAELoader"
                for node in workflow_data.values()
            )

    model_name = ""
    if zimage_type == "red":
        source_paths = checkpoint_paths if uses_checkpoint_loader else all_model_paths
        model_name = select_path_from_candidates(
            source_paths,
            ["redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors"],
            [["redcraft", "redzit15", "aio", "bf16"], ["redcraft", "redzit15"], ["redcraft"]],
        )
        if not model_name:
            raise RuntimeError(
                "red 类型缺少 checkpoint 模型，请放到 models/checkpoints: "
                "redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors"
            )
        if red_chain == "side":
            lora_paths = [item.rel_path for item in list_model_files(LORA_DIR, 5000)]
            red_side_lora_aux = select_path_from_candidates(
                lora_paths,
                ["ZPenisHelper.safetensors"],
                [["zpenishelper"], ["zpenis", "helper"]],
            )
            if not red_side_lora_aux:
                raise RuntimeError(
                    "red 侧链缺少 LoRA: ZPenisHelper.safetensors (请放到 models/loras)"
                )
            red_side_lora_main = select_path_from_candidates(
                lora_paths,
                ["REDZimage1.5_extracted_lora_rank_128.safetensors"],
                [["redzimage", "1.5", "extracted", "lora"], ["redzimage", "lora"]],
            )
            if not red_side_lora_main:
                raise RuntimeError(
                    "red 侧链缺少 LoRA: REDZimage1.5_extracted_lora_rank_128.safetensors (请放到 models/loras)"
                )
            params["red_side_lora_aux"] = red_side_lora_aux
            params["red_side_lora_main"] = red_side_lora_main
        elif red_chain == "pose":
            lora_paths = [item.rel_path for item in list_model_files(LORA_DIR, 5000)]
            red_pose_lora_name = select_path_from_candidates(
                lora_paths,
                [
                    str(params.get("red_pose_lora_name") or "").strip(),
                    "LovinSnuggly_Positions_LSP.safetensors",
                    "LSP.safetensors",
                ],
                [
                    ["lovinsnuggly", "positions"],
                    ["lsp", "position"],
                    ["lsp"],
                    ["position", "redcraft"],
                ],
            )
            if not red_pose_lora_name:
                raise RuntimeError(
                    "red 姿势链缺少 LoRA（LSP）。请放到 models/loras，"
                    "例如: LovinSnuggly_Positions_LSP.safetensors"
                )

            red_pose_lora_strength_model = clamp_float(
                params.get("red_pose_lora_strength_model", params.get("red_pose_lora_strength", 0.75)),
                0.0,
                2.0,
                0.75,
            )
            red_pose_lora_strength_clip = clamp_float(
                params.get("red_pose_lora_strength_clip", params.get("red_pose_lora_strength", 0.75)),
                0.0,
                2.0,
                0.75,
            )

            params["red_pose_lora_name"] = red_pose_lora_name
            params["red_pose_lora_strength_model"] = red_pose_lora_strength_model
            params["red_pose_lora_strength_clip"] = red_pose_lora_strength_clip
    elif zimage_type == "furry":
        source_paths = checkpoint_paths if uses_checkpoint_loader else all_model_paths
        model_name = select_path_from_candidates(
            source_paths,
            ["ratatoskrAnimalCreature_zImageVersionV10WIP.safetensors", "ratatoskr_zimage_v1_bf16.safetensors"],
            [["ratatoskr", "zimage"], ["ratatoskr"]],
        )
        if not model_name:
            raise RuntimeError(
                "兽人类型缺少 Ratatoskr 模型，请放到 models/checkpoints: "
                "ratatoskrAnimalCreature_zImageVersionV10WIP.safetensors"
            )
    elif zimage_type == "futa":
        source_paths = checkpoint_paths if uses_checkpoint_loader else all_model_paths
        model_name = select_path_from_candidates(
            source_paths,
            [
                "zimageTurboNSFWBy_2602NSFWFP8.safetensors",
                "zimageTurboNSFWBy_2602NSFWBF16.safetensors",
                "zimageTurboNSFWBy_2601Fp8.safetensors",
            ],
            [["zimage", "nsfw", "2602", "fp8"], ["zimage", "nsfw", "2602"], ["zimage", "nsfw"]],
        )
        if not model_name:
            raise RuntimeError(
                "扶她类型缺少 Zimage Turbo NSFW 模型，请放到 models/checkpoints: "
                "zimageTurboNSFWBy_2602NSFWFP8.safetensors"
            )
    else:
        if uses_checkpoint_loader:
            model_name = select_path_from_candidates(
                checkpoint_paths,
                [
                    str(params.get("model_name") or "").strip(),
                    "redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors",
                    "zimageTurboNSFWBy_2602NSFWFP8.safetensors",
                    "ratatoskr_zimage_v1_bf16.safetensors",
                ],
                [["redcraft"], ["zimage", "nsfw"], ["ratatoskr"]],
            )
            if not model_name and checkpoint_paths:
                model_name = checkpoint_paths[0]
            if not model_name:
                raise RuntimeError("normal 类型当前工作流是 checkpoint 流程，但 models/checkpoints 没有可用模型。")
        else:
            model_name = select_path_from_candidates(
                diffusion_paths,
                ["darkbeastz6BlitzBF16.Vii6.safetensors"],
                [["darkbeast", "z6", "blitz", "vii6"], ["darkbeast", "z6", "blitz"], ["darkbeast", "z6"]],
            )
            if not model_name:
                raise RuntimeError(
                    "normal 类型缺少 DarkBeast 模型，请放到 models/diffusion_models: "
                    "darkbeastz6BlitzBF16.Vii6.safetensors"
                )

    params["engine"] = "zimage_darkbeast"
    params["model_source"] = "zimage_darkbeast"
    params["model_name"] = model_name

    text_encoder_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "text_encoders", 5000)]
    vae_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "vae", 5000)]

    if uses_checkpoint_loader:
        # Some checkpoint workflows still include explicit CLIP/VAE loader nodes.
        # In that case, fill exact required defaults instead of leaving them blank.
        if workflow_has_clip_loader:
            clip_name = select_path_from_candidates(
                text_encoder_paths,
                ["qwen_3_4b.safetensors"],
                [["qwen", "3", "4b"]],
            )
            if not clip_name:
                raise RuntimeError(
                    "当前工作流包含 CLIPLoader，缺少文本编码器: qwen_3_4b.safetensors "
                    "(请放到 models/text_encoders)"
                )
            params["clip_name"] = clip_name
            params["clip_from_checkpoint"] = False
        else:
            params["clip_name"] = ""
            params["clip_from_checkpoint"] = True

        if workflow_has_vae_loader:
            vae_name = select_path_from_candidates(
                vae_paths,
                ["ae.safetensors"],
                [["ae"]],
            )
            if not vae_name:
                raise RuntimeError("当前工作流包含 VAELoader，缺少 VAE: ae.safetensors (请放到 models/vae)")
            params["vae_name"] = vae_name
            params["vae_from_checkpoint"] = False
        else:
            params["vae_name"] = ""
            params["vae_from_checkpoint"] = True
    else:
        params["clip_name"] = "qwen_3_4b.safetensors"
        params["vae_name"] = "ae.safetensors"
        params["clip_from_checkpoint"] = False
        params["vae_from_checkpoint"] = False
    params["lora_name"] = ""
    params["lora_names"] = []
    params["style_auto_selected"] = {
        "engine": "zimage_darkbeast",
        "workflow": workflow_name,
        "zimage_type": zimage_type,
        "red_chain": red_chain,
        "model_name": params["model_name"],
    }
    if zimage_type == "red" and red_chain == "side":
        params["style_auto_selected"]["red_side_lora_aux"] = params.get("red_side_lora_aux", "")
        params["style_auto_selected"]["red_side_lora_main"] = params.get("red_side_lora_main", "")
    if zimage_type == "red" and red_chain == "pose":
        params["style_auto_selected"]["red_pose_lora_name"] = params.get("red_pose_lora_name", "")
        params["style_auto_selected"]["red_pose_lora_strength_model"] = params.get(
            "red_pose_lora_strength_model", 0.75
        )
        params["style_auto_selected"]["red_pose_lora_strength_clip"] = params.get(
            "red_pose_lora_strength_clip", 0.75
        )


def auto_configure_inpaint_zimage_lock_bundle(params: dict[str, Any]) -> None:
    zimage_type = normalize_zimage_type(params.get("zimage_type", "normal"))
    params["zimage_type"] = zimage_type
    width, height = parse_preset_size(params.get("preset"), params.get("width"), params.get("height"))
    params["width"] = width
    params["height"] = height

    try:
        steps_stage1 = int(params.get("steps_stage1", 4))
    except Exception:
        steps_stage1 = 4
    params["steps_stage1"] = max(1, min(60, steps_stage1))
    params["cfg_stage1"] = clamp_float(params.get("cfg_stage1", 1.0), 0.0, 20.0, 1.0)

    try:
        steps_stage2 = int(params.get("steps_stage2", 14))
    except Exception:
        steps_stage2 = 14
    params["steps_stage2"] = max(1, min(80, steps_stage2))
    params["cfg_stage2"] = clamp_float(params.get("cfg_stage2", 1.3), 0.0, 20.0, 1.3)
    params["denoise_stage2"] = clamp_float(params.get("denoise_stage2", 0.58), 0.0, 1.0, 0.58)
    params["upscale_by"] = clamp_float(params.get("upscale_by", 1.5), 1.0, 4.0, 1.5)
    params["final_scale"] = clamp_float(params.get("final_scale", 0.75), 0.1, 1.0, 0.75)
    params["aura_shift"] = clamp_float(params.get("aura_shift", 1.15), 0.1, 4.0, 1.15)
    params["lora_strength_aux"] = clamp_float(params.get("lora_strength_aux", 1.25), -4.0, 4.0, 1.25)
    params["lora_strength_main"] = clamp_float(params.get("lora_strength_main", -1.5), -4.0, 4.0, -1.5)
    params["end_at_step_stage1"] = max(0, params["steps_stage1"] - 1)

    workflow_name = ZIMAGE_INPAINT_WORKFLOW_MAP[zimage_type]
    workflow_path = WORKFLOW_DIR / workflow_name
    uses_checkpoint_loader = False
    with suppress(Exception):
        workflow_data = json.loads(workflow_path.read_text(encoding="utf-8"))
        if isinstance(workflow_data, dict):
            uses_checkpoint_loader = any(
                isinstance(node, dict) and str(node.get("class_type") or "") == "CheckpointLoaderSimple"
                for node in workflow_data.values()
            )

    # red 锁定生图新版：换脸前主链完全对齐 red 主链（CheckpointLoaderSimple）
    if zimage_type == "red" and uses_checkpoint_loader:
        red_default_like = (
            params["width"] == 576
            and params["height"] == 1088
            and params["steps_stage1"] == 4
            and abs(float(params["cfg_stage1"]) - 1.0) < 1e-9
            and params["steps_stage2"] == 14
            and abs(float(params["cfg_stage2"]) - 1.3) < 1e-9
            and abs(float(params["denoise_stage2"]) - 0.58) < 1e-9
            and abs(float(params["upscale_by"]) - 1.5) < 1e-9
        )
        if red_default_like:
            params["width"] = 832
            params["height"] = 1536
            params["preset"] = "832x1536"
            params["steps_stage1"] = 8
            params["cfg_stage1"] = 1.0
            params["steps_stage2"] = 8
            params["cfg_stage2"] = 1.25
            params["denoise_stage2"] = 0.40
            params["upscale_by"] = 2.0
            params["end_at_step_stage1"] = max(0, params["steps_stage1"] - 1)

        checkpoint_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "checkpoints", 5000)]
        model_name = select_path_from_candidates(
            checkpoint_paths,
            ["redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors"],
            [["redcraft", "redzit15", "aio", "bf16"], ["redcraft", "redzit15"], ["redcraft"]],
        )
        if not model_name:
            raise RuntimeError(
                "red 锁定生图缺少 checkpoint 模型，请放到 models/checkpoints: "
                "redcraftMar0826LatestZib_redzit15AIO_full_bf16.safetensors"
            )

        params["engine"] = "zimage_lock"
        params["model_source"] = "zimage_lock"
        params["model_name"] = model_name
        params["unet_name"] = model_name
        # CheckpointLoaderSimple 内含 text encoder / vae，避免外部 loader 误注入
        params["clip_name"] = ""
        params["vae_name"] = ""
        params["clip_from_checkpoint"] = True
        params["vae_from_checkpoint"] = True
        params["lora_name_aux"] = ""
        params["lora_name_main"] = ""
        params["lora_name"] = ""
        params["lora_names"] = []
        params["style_auto_selected"] = {
            "engine": "zimage_lock",
            "workflow": workflow_name,
            "zimage_type": zimage_type,
            "model_name": model_name,
            "uses_checkpoint_loader": True,
        }
        return

    diffusion_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "diffusion_models", 5000)]
    unet_name_stage1 = select_path_from_candidates(
        diffusion_paths,
        ["redcraftFeb1126Latest_zibDistilledDX3Lucis.safetensors"],
        [["redcraft", "zib", "distilled", "dx3", "lucis"], ["redcraft", "zib", "dx3"], ["redcraft"]],
    )
    if not unet_name_stage1:
        raise RuntimeError(
            "Z-IMAGE锁定生图缺少 Stage1 模型: redcraftFeb1126Latest_zibDistilledDX3Lucis.safetensors "
            "(请放到 models/diffusion_models)"
        )

    unet_name_stage2 = select_path_from_candidates(
        diffusion_paths,
        ["darkbeastz6BlitzBF16.Vii6.safetensors"],
        [["darkbeast", "z6", "blitz", "vii6"], ["darkbeast", "z6", "blitz"], ["darkbeast", "z6"]],
    )
    if not unet_name_stage2:
        raise RuntimeError(
            "Z-IMAGE锁定生图缺少 Stage2 模型: darkbeastz6BlitzBF16.Vii6.safetensors "
            "(请放到 models/diffusion_models)"
        )

    text_encoder_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "text_encoders", 5000)]
    clip_name = select_path_from_candidates(
        text_encoder_paths,
        ["qwen_3_4b.safetensors", "qwen_3_8b_fp8mixed.safetensors"],
        [["qwen", "3", "4b"], ["qwen", "3", "8b"]],
    )
    if not clip_name:
        raise RuntimeError(
            "Z-IMAGE锁定生图缺少文本编码器: qwen_3_4b.safetensors "
            "(请放到 models/text_encoders)"
        )

    vae_paths = [item.rel_path for item in list_model_files(PROJECT_MODELS_DIR / "vae", 5000)]
    vae_name = select_path_from_candidates(
        vae_paths,
        ["ae.safetensors"],
        [["ae"]],
    )
    if not vae_name:
        raise RuntimeError("Z-IMAGE锁定生图缺少 VAE: ae.safetensors (请放到 models/vae)")

    lora_paths = [item.rel_path for item in list_model_files(LORA_DIR, 5000)]
    lora_main = select_path_from_candidates(
        lora_paths,
        ["REDZ15_DetailDaemonZ_lora_v1.1.safetensors"],
        [["redz15", "detaildaemonz"], ["detaildaemonz"], ["redz15"]],
    )
    if not lora_main:
        raise RuntimeError(
            "Z-IMAGE锁定生图缺少 LoRA: REDZ15_DetailDaemonZ_lora_v1.1.safetensors "
            "(请放到 models/loras)"
        )
    lora_aux = lora_main

    params["engine"] = "zimage_lock"
    params["model_source"] = "zimage_lock"
    params["model_name"] = unet_name_stage2
    params["unet_name"] = unet_name_stage2
    params["unet_name_stage1"] = unet_name_stage1
    params["unet_name_stage2"] = unet_name_stage2
    params["clip_name"] = clip_name
    params["vae_name"] = vae_name
    params["lora_name_aux"] = lora_aux
    params["lora_name_main"] = lora_main

    # Disable generic LoRA injection for this fixed Z-Image lock workflow.
    params["lora_name"] = ""
    params["lora_names"] = []

    params["style_auto_selected"] = {
        "engine": "zimage_lock",
        "workflow": ZIMAGE_INPAINT_WORKFLOW_MAP[zimage_type],
        "zimage_type": zimage_type,
        "model_name": unet_name_stage2,
        "unet_name_stage1": unet_name_stage1,
        "unet_name_stage2": unet_name_stage2,
        "clip_name": clip_name,
        "vae_name": vae_name,
        "lora_name_aux": lora_aux,
        "lora_name_main": lora_main,
    }


def _coerce_numeric_like(value: Any) -> Any:
    if isinstance(value, str):
        text = value.strip()
        if not text:
            return value
        with suppress(Exception):
            if "." in text:
                number = float(text)
                return int(number) if number.is_integer() else number
            return int(text)
    return value


def convert_image_bytes_if_needed(content: bytes, ext: str, task_type: TaskType) -> tuple[bytes, str]:
    # Only convert still-image tasks to AVIF. Video tasks keep original encoders/containers.
    if task_type not in {"t2i", "inpaint"}:
        return content, ext
    if IMAGE_OUTPUT_FORMAT not in {"image/avif", "avif"}:
        return content, ext
    if ext.lower() == ".avif":
        return content, ext
    if Image is None:
        raise RuntimeError("Pillow is not installed, cannot convert generated images to AVIF.")

    try:
        image = Image.open(io.BytesIO(content))
        image.load()
        out = io.BytesIO()
        image.save(out, format="AVIF", quality=80)
        return out.getvalue(), ".avif"
    except Exception as e:
        raise RuntimeError(f"Failed to convert generated image to AVIF: {e}") from e


def apply_wan_remix_runtime_fixes(workflow: dict[str, Any]) -> None:
    # Keep Wan Remix workflows compatible with slightly different custom-node
    # versions by filling required fields when they are absent.
    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")

        if class_type == "Int":
            if "Number" not in inputs and "value" in inputs:
                inputs["Number"] = inputs["value"]
            if "value" not in inputs and "Number" in inputs:
                inputs["value"] = inputs["Number"]
            if "Number" in inputs:
                inputs["Number"] = _coerce_numeric_like(inputs["Number"])
            if "value" in inputs:
                inputs["value"] = _coerce_numeric_like(inputs["value"])
        elif class_type == "LayerUtility: ImageScaleByAspectRatio V2":
            if not str(inputs.get("background_color") or "").strip():
                inputs["background_color"] = "#000000"
        elif class_type == "ModelSamplingSD3":
            if "shift" not in inputs:
                inputs["shift"] = 5.0
        elif class_type == "RIFE VFI":
            if "dtype" not in inputs:
                inputs["dtype"] = "float32"
            if "batch_size" not in inputs:
                inputs["batch_size"] = 1
            if "torch_compile" not in inputs:
                inputs["torch_compile"] = False
            # Keep interpolation quality/stability profile consistent.
            inputs["multiplier"] = 2
            inputs["fast_mode"] = False
            inputs["ensemble"] = True


def _set_number_like_inputs(inputs: dict[str, Any], value: Any) -> None:
    for key in ("Number", "value", "int", "number", "float"):
        if key in inputs:
            inputs[key] = value


def apply_t2v_runtime_overrides(workflow: dict[str, Any], params: dict[str, Any]) -> None:
    prompt = str(params.get("prompt") or "").strip()
    negative_prompt = str(params.get("negative_prompt") or "").strip()
    output_prefix = str(params.get("output_prefix") or "").strip()

    seed = normalize_seed(params.get("seed", -1))
    try:
        width = int(params.get("width", 832))
    except Exception:
        width = 832
    try:
        height = int(params.get("height", 480))
    except Exception:
        height = 480
    try:
        steps = int(params.get("steps", 8))
    except Exception:
        steps = 8
    cfg = clamp_float(params.get("cfg", 1.0), 0.0, 50.0, 1.0)
    try:
        fps = int(params.get("fps", 16))
    except Exception:
        fps = 16
    fps = max(1, fps)
    try:
        duration_sec = int(params.get("duration_sec", 4))
    except Exception:
        duration_sec = 4
    duration_sec = max(1, min(60, duration_sec))
    # Keep Duration(s) as final output duration even when interpolation nodes
    # (e.g. RIFE VFI multiplier=2) are present in the workflow.
    interpolation_multiplier = 1
    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        if str(node.get("class_type") or "") != "RIFE VFI":
            continue
        node_inputs = node.get("inputs")
        if not isinstance(node_inputs, dict):
            continue
        # Force RIFE runtime profile.
        node_inputs["multiplier"] = 2
        node_inputs["fast_mode"] = False
        node_inputs["ensemble"] = True
        m = 2
        if m > interpolation_multiplier:
            interpolation_multiplier = m

    base_fps = max(1, int(round(fps / max(1, interpolation_multiplier))))
    length_frames = max(2, duration_sec * base_fps + 1)

    for node_id, node in workflow.items():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        nid = str(node_id)

        # Keep node-id based mapping for 1:1 restored Wan T2V workflow.
        if class_type == "CLIPTextEncode":
            if nid == "380" and prompt and "text" in inputs:
                inputs["text"] = prompt
            elif nid == "348" and negative_prompt and "text" in inputs:
                inputs["text"] = negative_prompt

        if class_type in {"Int", "ImpactInt", "INTConstant", "Integer", "PrimitiveInt"}:
            if nid == "381":
                _set_number_like_inputs(inputs, width)
            elif nid == "382":
                _set_number_like_inputs(inputs, height)
            elif nid == "360":
                _set_number_like_inputs(inputs, length_frames)
            elif nid == "361":
                _set_number_like_inputs(inputs, steps)

        if class_type == "easy float":
            if nid == "217":
                _set_number_like_inputs(inputs, float(base_fps))
            elif nid == "209":
                _set_number_like_inputs(inputs, float(duration_sec))

        if class_type == "WanMoeKSampler":
            if "seed" in inputs:
                inputs["seed"] = seed
            if "noise_seed" in inputs:
                inputs["noise_seed"] = seed
            if "steps" in inputs:
                inputs["steps"] = steps
            if "cfg" in inputs:
                inputs["cfg"] = cfg
            if "cfg_high_noise" in inputs:
                inputs["cfg_high_noise"] = cfg
            if "cfg_low_noise" in inputs:
                inputs["cfg_low_noise"] = cfg

        if class_type == "VHS_VideoCombine":
            # Avoid accidental long-loop outputs from workflow defaults (e.g. loop_count=69).
            # For MP4 generation we always want a single pass timeline.
            if "loop_count" in inputs:
                inputs["loop_count"] = 0
            if "frame_rate" in inputs:
                inputs["frame_rate"] = float(fps)
            if output_prefix and "filename_prefix" in inputs:
                if nid == "379":
                    inputs["filename_prefix"] = f"{output_prefix}_silent"
                elif nid == "218":
                    inputs["filename_prefix"] = f"{output_prefix}_audio"
                else:
                    inputs["filename_prefix"] = output_prefix


def apply_selected_model_name(workflow: dict[str, Any], model_name: str) -> None:
    if not model_name:
        return

    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        # Only override model loader nodes. Some preprocessors also use `ckpt_name`
        # (for example DepthAnythingV2Preprocessor), and those must keep their own value.
        if class_type == "CheckpointLoaderSimple":
            inputs["ckpt_name"] = model_name
        if class_type == "UNETLoader":
            inputs["unet_name"] = model_name


def apply_selected_vae(workflow: dict[str, Any], vae_name: str) -> None:
    if not vae_name:
        return

    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        if class_type == "VAELoader":
            inputs["vae_name"] = vae_name


def apply_selected_clip_name(workflow: dict[str, Any], clip_name: str) -> None:
    if not clip_name:
        return

    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        if class_type == "CLIPLoader":
            inputs["clip_name"] = clip_name


def apply_inpaint_zimage_refine_switch(workflow: dict[str, Any], refine_enabled: bool) -> None:
    """Toggle FaceDetailer branch for zimage_lock workflow at runtime.

    When disabled:
    - Route final output ("after") to ReActor output (node 50).
    - Drop intermediate "after_swap" save node to avoid duplicate outputs.
    - FaceDetailer node becomes orphaned and will not execute.
    """
    if refine_enabled:
        return

    node_after = workflow.get("8")
    if isinstance(node_after, dict):
        inputs = node_after.get("inputs")
        if isinstance(inputs, dict):
            inputs["images"] = ["50", 0]

    node_after_swap = workflow.get("80")
    if isinstance(node_after_swap, dict):
        workflow.pop("80", None)


def apply_inpaint_zimage_no_swap_fallback(workflow: dict[str, Any]) -> None:
    """Force stable after output path without swap/detail branch."""
    node_swap = workflow.get("50")
    if isinstance(node_swap, dict):
        inputs = node_swap.get("inputs")
        if isinstance(inputs, dict):
            inputs["enabled"] = False

    node_after = workflow.get("8")
    if isinstance(node_after, dict):
        inputs = node_after.get("inputs")
        if isinstance(inputs, dict):
            source_node = "39" if "39" in workflow else "50"
            inputs["images"] = [source_node, 0]

    workflow.pop("80", None)


def apply_selected_lora(workflow: dict[str, Any], lora_name: str, lora_strength: Any) -> dict[str, Any] | None:
    name = str(lora_name or "").strip()
    if not name:
        return None

    strength = clamp_float(lora_strength, -4.0, 4.0, 1.0)
    debug_info: dict[str, Any] = {
        "requested_name": name,
        "requested_strength": strength,
        "applied": False,
    }

    model_link: list[Any] | None = None
    clip_link: list[Any] | None = None

    # Prefer current active links so multiple LoRAs can be chained in order.
    for node in workflow.values():
        if not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        class_type = str(node.get("class_type") or "")
        if model_link is None and class_type != "LoraLoader":
            m = inputs.get("model")
            if isinstance(m, list) and len(m) == 2:
                model_link = [m[0], m[1]]
        if clip_link is None and class_type in {
            "CLIPTextEncode",
            "CLIPTextEncodeFlux",
            "CLIPTextEncodeSDXL",
            "CLIPTextEncodeSDXLRefiner",
        }:
            c = inputs.get("clip")
            if isinstance(c, list) and len(c) == 2:
                clip_link = [c[0], c[1]]

    # Fallback to model/clip loader nodes for templates where active links were not found.
    if model_link is None or clip_link is None:
        for node_id, node in workflow.items():
            if not isinstance(node, dict):
                continue
            class_type = str(node.get("class_type") or "")
            if model_link is None and class_type in {"UNETLoader", "CheckpointLoaderSimple"}:
                model_link = [str(node_id), 0]
            if clip_link is None:
                if class_type in {"DualCLIPLoader", "CLIPLoader"}:
                    clip_link = [str(node_id), 0]
                elif class_type == "CheckpointLoaderSimple":
                    clip_link = [str(node_id), 1]

    if model_link is None or clip_link is None:
        debug_info["reason"] = "model_or_clip_loader_not_found"
        return debug_info

    lora_node_id = "9000"
    while lora_node_id in workflow:
        lora_node_id = str(int(lora_node_id) + 1)

    workflow[lora_node_id] = {
        "inputs": {
            "lora_name": name,
            "strength_model": strength,
            "strength_clip": strength,
            "model": model_link,
            "clip": clip_link,
        },
        "class_type": "LoraLoader",
    }

    patched_model_link = [lora_node_id, 0]
    patched_clip_link = [lora_node_id, 1]

    for node_id, node in workflow.items():
        if node_id == lora_node_id or not isinstance(node, dict):
            continue
        inputs = node.get("inputs")
        if not isinstance(inputs, dict):
            continue
        for key, value in list(inputs.items()):
            if value == model_link:
                inputs[key] = patched_model_link
            elif value == clip_link:
                inputs[key] = patched_clip_link

    debug_info.update(
        {
            "applied": True,
            "node_id": lora_node_id,
            "class_type": "LoraLoader",
            "strength_model": strength,
            "strength_clip": strength,
            "model_input": model_link,
            "clip_input": clip_link,
        }
    )
    return debug_info


async def download_hf_to_lora(req: LoraDownloadRequest) -> LoraItem:
    requested_name = req.target_name or Path(req.filename).name
    target_name = sanitize_filename(requested_name)
    ensure_lora_extension(target_name)
    if not target_name:
        raise HTTPException(status_code=400, detail="Invalid target filename.")

    target_path = (LORA_DIR / target_name).resolve()
    if target_path.parent != LORA_DIR.resolve():
        raise HTTPException(status_code=400, detail="Invalid target path.")
    if target_path.exists() and not req.force:
        raise HTTPException(status_code=409, detail=f"LoRA exists: {target_name}")

    repo_id = req.repo_id.strip().strip("/")
    filename = req.filename.strip().lstrip("/")
    if not repo_id or not filename:
        raise HTTPException(status_code=400, detail="repo_id and filename are required.")

    url = f"https://huggingface.co/{repo_id}/resolve/main/{filename}"
    params = {"download": "true"}
    headers: dict[str, str] = {}
    token = req.token or os.getenv("HF_TOKEN") or os.getenv("HUGGINGFACE_HUB_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"

    tmp_path = target_path.with_suffix(target_path.suffix + ".part")
    if tmp_path.exists():
        tmp_path.unlink()

    async with httpx.AsyncClient(timeout=None, follow_redirects=True) as client:
        try:
            async with client.stream("GET", url, headers=headers, params=params) as response:
                if response.status_code >= 400:
                    body = (await response.aread()).decode(errors="ignore")[:400]
                    raise HTTPException(
                        status_code=response.status_code,
                        detail=f"Hugging Face download failed: {body or response.reason_phrase}",
                    )

                content_type = (response.headers.get("content-type") or "").lower()
                if "text/html" in content_type:
                    body = (await response.aread()).decode(errors="ignore")[:250]
                    raise HTTPException(
                        status_code=400,
                        detail=(
                            "Received HTML instead of model file. "
                            "Check repo/filename and ensure token/terms access is granted. "
                            f"Body: {body}"
                        ),
                    )

                with tmp_path.open("wb") as fh:
                    async for chunk in response.aiter_bytes(chunk_size=2 * 1024 * 1024):
                        if chunk:
                            fh.write(chunk)
        except HTTPException:
            tmp_path.unlink(missing_ok=True)
            raise
        except Exception as exc:
            tmp_path.unlink(missing_ok=True)
            raise HTTPException(status_code=500, detail=f"LoRA download error: {exc}") from exc

    if not tmp_path.exists() or tmp_path.stat().st_size == 0:
        raise HTTPException(status_code=500, detail="Downloaded file is empty.")
    tmp_path.replace(target_path)
    return lora_item_from_path(target_path)


def list_gallery(kind: Literal["all", "image", "video"], limit: int) -> list[GalleryItem]:
    items: list[GalleryItem] = []

    dirs: list[tuple[Literal["image", "video"], Path]] = []
    if kind in {"all", "image"}:
        dirs.append(("image", IMAGE_OUTPUT_DIR))
    if kind in {"all", "video"}:
        dirs.append(("video", VIDEO_OUTPUT_DIR))

    for file_kind, directory in dirs:
        for file_path in directory.glob("*"):
            if not file_path.is_file():
                continue
            stat = file_path.stat()
            url = f"/outputs/{'videos' if file_kind == 'video' else 'images'}/{file_path.name}"
            items.append(
                GalleryItem(
                    kind=file_kind,
                    filename=file_path.name,
                    abs_path=str(file_path.resolve()),
                    url=url,
                    size_bytes=stat.st_size,
                    modified_at=datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
                )
            )

    items.sort(key=lambda item: item.modified_at, reverse=True)
    return items[: max(1, limit)]


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    global worker_task
    ensure_dirs()
    worker_task = asyncio.create_task(worker_loop(), name="gateway-worker")
    yield
    if worker_task:
        worker_task.cancel()
        with suppress(asyncio.CancelledError):
            await worker_task
    await comfy_client.close()


app = FastAPI(
    title="Local ComfyUI Gateway",
    version="1.0.0",
    description="Lightweight queue gateway for T2I/Inpaint/T2V/I2V workflows.",
    lifespan=lifespan,
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.mount("/outputs", StaticFiles(directory=str(OUTPUT_DIR)), name="outputs")
app.mount("/web", StaticFiles(directory=str(WEB_DIR), html=True), name="web")


@app.get("/", include_in_schema=False)
async def root() -> RedirectResponse:
    return RedirectResponse(url="/web/", status_code=307)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/api/t2i_zimage", response_model=TaskRecord)
async def enqueue_t2i_zimage(request: T2IZImageRequest) -> TaskRecord:
    params = request.model_dump()
    params["zimage_type"] = normalize_zimage_type(params.get("zimage_type", "normal"))
    params["red_chain"] = normalize_red_chain(params.get("red_chain", "main"))
    params["engine"] = "zimage_darkbeast"
    return await create_task("t2i", params)


@app.post("/api/t2v", response_model=TaskRecord)
async def enqueue_t2v(request: T2VRequest) -> TaskRecord:
    return await create_task("t2v", request.model_dump())


@app.post("/api/inpaint_zimage", response_model=TaskRecord)
async def enqueue_inpaint_zimage(
    zimage_type: str = Form("normal"),
    prompt: str = Form(...),
    negative_prompt: str = Form(""),
    width: int = Form(576),
    height: int = Form(1088),
    seed: int = Form(-1),
    preset: str = Form("576x1088"),
    steps_stage1: int = Form(4),
    cfg_stage1: float = Form(1.0),
    steps_stage2: int = Form(14),
    cfg_stage2: float = Form(1.3),
    denoise_stage2: float = Form(0.58),
    upscale_by: float = Form(1.5),
    final_scale: float = Form(0.75),
    aura_shift: float = Form(1.15),
    refine_enabled: bool = Form(False),
    reactor_enabled: bool = Form(True),
    reactor_swap_model: str = Form("inswapper_128.onnx"),
    reactor_facedetection: str = Form("retinaface_resnet50"),
    reactor_face_restore_model: str = Form("codeformer-v0.1.0.pth"),
    reactor_face_restore_visibility: float = Form(0.8),
    reactor_codeformer_weight: float = Form(0.5),
    reactor_detect_gender_input: str = Form("no"),
    reactor_detect_gender_source: str = Form("no"),
    reactor_input_faces_index: str = Form("0"),
    reactor_source_faces_index: str = Form("0"),
    reactor_console_log_level: int = Form(1),
    image: UploadFile = File(...),
) -> TaskRecord:
    image_path = await save_upload(image, UPLOAD_DIR)
    params = {
        "zimage_type": normalize_zimage_type(zimage_type),
        "prompt": prompt,
        "prompt_raw": prompt,
        "negative_prompt": negative_prompt,
        "width": width,
        "height": height,
        "seed": seed,
        "preset": preset,
        "steps_stage1": steps_stage1,
        "cfg_stage1": cfg_stage1,
        "steps_stage2": steps_stage2,
        "cfg_stage2": cfg_stage2,
        "denoise_stage2": denoise_stage2,
        "upscale_by": upscale_by,
        "final_scale": final_scale,
        "aura_shift": aura_shift,
        "refine_enabled": refine_enabled,
        "model_source": "zimage_lock",
        "engine": "zimage_lock",
        "reactor_enabled": reactor_enabled,
        "reactor_swap_model": reactor_swap_model.strip(),
        "reactor_facedetection": reactor_facedetection.strip(),
        "reactor_face_restore_model": reactor_face_restore_model.strip(),
        "reactor_face_restore_visibility": reactor_face_restore_visibility,
        "reactor_codeformer_weight": reactor_codeformer_weight,
        "reactor_detect_gender_input": reactor_detect_gender_input.strip(),
        "reactor_detect_gender_source": reactor_detect_gender_source.strip(),
        "reactor_input_faces_index": reactor_input_faces_index.strip(),
        "reactor_source_faces_index": reactor_source_faces_index.strip(),
        "reactor_console_log_level": reactor_console_log_level,
        "image_path": str(image_path),
    }
    return await create_task("inpaint", params)


@app.post("/api/i2v_wan_remix", response_model=TaskRecord)
async def enqueue_i2v_wan_remix(
    chain: str = Form("single"),
    prompt: str = Form(...),
    negative_prompt: str = Form(DEFAULT_VIDEO_NEGATIVE_PROMPT),
    width: int = Form(832),
    height: int = Form(480),
    duration_sec: int = Form(4, ge=1, le=60),
    fps: int = Form(16),
    steps: int = Form(6),
    cfg: float = Form(1.0),
    seed: int = Form(-1),
    image: UploadFile = File(...),
    image_end: UploadFile | None = File(None),
) -> TaskRecord:
    chain_normalized = normalize_wan_remix_chain(chain)
    if chain_normalized == "first_last" and image_end is None:
        raise HTTPException(status_code=400, detail="first_last 链路需要第二张图片（image_end）。")

    image_path = await save_upload(image, UPLOAD_DIR)
    image_end_path = ""
    if image_end is not None:
        end_path = await save_upload(image_end, UPLOAD_DIR)
        image_end_path = str(end_path)

    params = {
        "model_source": "wan_remix",
        "engine": "wan_remix",
        "wan_remix_chain": chain_normalized,
        "prompt": prompt,
        "negative_prompt": negative_prompt,
        "width": width,
        "height": height,
        "duration_sec": duration_sec,
        "fps": fps,
        "steps": steps,
        "cfg": cfg,
        "seed": seed,
        "image_path": str(image_path),
        "image_end_path": image_end_path,
    }
    return await create_task("i2v", params)


@app.get("/api/loras", response_model=list[LoraItem])
async def api_list_loras(limit: int = 500) -> list[LoraItem]:
    return list_loras(limit)


@app.get("/api/models", response_model=ModelCatalog)
async def api_list_models(limit: int = 2000) -> ModelCatalog:
    return list_models(limit)


@app.post("/api/loras/upload", response_model=LoraItem)
async def api_upload_lora(file: UploadFile = File(...), overwrite: bool = Form(False)) -> LoraItem:
    original = file.filename or "lora.safetensors"
    target_name = sanitize_filename(Path(original).name)
    if not target_name:
        raise HTTPException(status_code=400, detail="Invalid LoRA filename.")
    ensure_lora_extension(target_name)

    target_path = (LORA_DIR / target_name).resolve()
    if target_path.parent != LORA_DIR.resolve():
        raise HTTPException(status_code=400, detail="Invalid LoRA path.")
    if target_path.exists() and not overwrite:
        raise HTTPException(status_code=409, detail=f"LoRA exists: {target_name}")

    with target_path.open("wb") as fh:
        while True:
            chunk = await file.read(2 * 1024 * 1024)
            if not chunk:
                break
            fh.write(chunk)
    await file.close()

    if target_path.stat().st_size == 0:
        target_path.unlink(missing_ok=True)
        raise HTTPException(status_code=400, detail="Uploaded file is empty.")
    return lora_item_from_path(target_path)


@app.post("/api/loras/download", response_model=LoraItem)
async def api_download_lora(req: LoraDownloadRequest) -> LoraItem:
    return await download_hf_to_lora(req)


@app.delete("/api/loras/{name}")
async def api_delete_lora(name: str) -> dict[str, str]:
    target_path = get_safe_lora_path(name)
    if not target_path.exists():
        raise HTTPException(status_code=404, detail=f"LoRA not found: {name}")
    target_path.unlink()
    return {"deleted": name}


@app.get("/api/tasks", response_model=list[TaskRecord])
async def api_list_tasks(limit: int = 200) -> list[TaskRecord]:
    async with tasks_lock:
        values = [task.model_copy(deep=True) for task in tasks.values()]
    values.sort(key=lambda item: item.created_at, reverse=True)
    return values[: max(1, min(limit, 1000))]


@app.get("/api/tasks/stream")
async def api_stream_tasks() -> StreamingResponse:
    queue = await event_bus.subscribe()

    async def event_generator() -> AsyncIterator[str]:
        try:
            yield "event: ready\ndata: {}\n\n"
            while True:
                data = await queue.get()
                yield data
        finally:
            await event_bus.unsubscribe(queue)

    return StreamingResponse(event_generator(), media_type="text/event-stream")


@app.get("/api/tasks/{task_id}", response_model=TaskRecord)
async def api_get_task(task_id: str) -> TaskRecord:
    return await read_task(task_id)


@app.get("/api/queue")
async def api_queue() -> dict[str, Any]:
    async with tasks_lock:
        values = [task.model_copy(deep=True) for task in tasks.values()]
    values.sort(key=lambda item: item.created_at)
    running = [item.id for item in values if item.status == "running"]
    queued = [item.id for item in values if item.status == "queued"]
    return {
        "running": running[:1],
        "queued": queued,
        "queued_count": len(queued),
        "total_inflight": len(queued) + len(running),
    }


@app.get("/api/gallery", response_model=list[GalleryItem])
async def api_gallery(
    kind: Literal["all", "image", "video"] = "all",
    limit: int = 200,
) -> list[GalleryItem]:
    return list_gallery(kind, min(max(limit, 1), 1000))


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(
        app,
        host=os.getenv("HOST", "0.0.0.0"),
        port=int(os.getenv("PORT", "8000")),
        log_level=os.getenv("LOG_LEVEL", "info"),
    )
