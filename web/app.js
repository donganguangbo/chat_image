const state = {
  tasks: new Map(),
  queue: { running: [], queued: [], queued_count: 0, total_inflight: 0 },
  eventSource: null,
  loras: [],
};

const DEFAULT_NEGATIVE_PROMPT_ZIMAGE_T2I =
  "丑陋, 畸形, 变异, 多余肢体, 缺失肢体, 多余手指, 融合手指, 手部糟糕, 脸部糟糕, 解剖错误, 比例错误, 长脖子, 模糊, 低质量, 最差质量, 水印, 文字, 签名, 标志, 审查, 马赛克, 黑边, 穿着衣服, 内衣, 胸罩, 内裤, 部分着衣, 儿童, 萝莉, 未成年, 老年, 肥胖, 男性化, 平胸, 小胸, 深色皮肤, 亚洲刻板印象, 卡通, 动漫, 3D渲染, 绘画, 过曝, 欠曝, 噪点, 像素化, 失焦, 克隆脸, 斗鸡眼, 无表情, 微笑, 天真脸, 害羞, 保守姿势, 双腿并拢, 手遮身体, 娃娃脸, 短发, 金发, 纹身, 浓妆, 巨大乳房, 巨大屁股, 骨瘦如柴, 皮肤瑕疵, 皱纹, 塑料皮肤";
const DEFAULT_NEGATIVE_PROMPT_ZIMAGE_LOCK =
  "畸形身体, 解剖错误, 比例错乱, 极端比例, 躯干扭曲, 骨盆错位, 脊柱扭曲, 断裂关节, 透视畸变, 鱼眼畸变, 近景畸变, 多余肢体, 缺失肢体, 融合肢体, 手部畸形, 脚部畸形, 多余手指, 融合手指, 缺失手指, 多余脚趾, 融合脚趾, 缺失脚趾, duplicate body, duplicate torso, merged body, 丑陋, 畸形, 变异, 多余肢体, 缺失肢体, 多余手指, 融合手指, 手部糟糕, 脸部糟糕, 解剖错误, 比例错误, 长脖子, 模糊, 低质量, 最差质量, 水印, 文字, 签名, 标志, 审查, 马赛克, 黑边, 穿着衣服, 内衣, 胸罩, 内裤, 部分着衣, 儿童, 萝莉, 未成年, 老年, 肥胖, 男性化, 平胸, 小胸, 深色皮肤, 亚洲刻板印象, 卡通, 动漫, 3D渲染, 绘画, 过曝, 欠曝, 噪点, 像素化, 失焦, 克隆脸, 斗鸡眼, 无表情, 微笑, 天真脸, 害羞, 保守姿势, 双腿并拢, 手遮身体, 娃娃脸, 短发, 金发, 纹身, 浓妆, 巨大乳房, 巨大屁股, 骨瘦如柴, 皮肤瑕疵, 皱纹, 塑料皮肤";
const DEFAULT_NEGATIVE_PROMPT_VIDEO =
  "lowres, blurry, out of focus, bad quality, jpeg artifacts, noisy, overexposed, underexposed, flicker, frame jitter, temporal inconsistency, identity drift, face morphing, duplicate face, deformed body, bad anatomy, bad hands, extra fingers, fused fingers, missing fingers, extra limbs, warped arms, text, watermark, logo";

const q = (sel) => document.querySelector(sel);
const qq = (sel) => Array.from(document.querySelectorAll(sel));

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function showMessage(text, type = "info") {
  const el = q("#message");
  if (!el) return;
  el.classList.remove("hidden", "ok", "error", "info");
  el.classList.add(type);
  el.textContent = text;
  window.clearTimeout(showMessage._timer);
  showMessage._timer = window.setTimeout(() => {
    el.classList.add("hidden");
  }, 4500);
}

async function requestJson(url, options = {}) {
  const resp = await fetch(url, options);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(text || `HTTP ${resp.status}`);
  }
  return resp.json();
}

function toInt(v, fallback = 0) {
  const n = parseInt(v, 10);
  return Number.isNaN(n) ? fallback : n;
}

function toFloat(v, fallback = 0) {
  const n = parseFloat(v);
  return Number.isNaN(n) ? fallback : n;
}

function toLocalTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  return d.toLocaleString();
}

function applyDefaultNegativePrompt() {
  const zimageArea = q('#t2i-zimage-form textarea[name="negative_prompt"]');
  if (zimageArea && !zimageArea.value.trim()) {
    zimageArea.value = DEFAULT_NEGATIVE_PROMPT_ZIMAGE_T2I;
  }

  const inpaintZImageArea = q('#inpaint-zimage-form textarea[name="negative_prompt"]');
  if (inpaintZImageArea && !inpaintZImageArea.value.trim()) {
    inpaintZImageArea.value = DEFAULT_NEGATIVE_PROMPT_ZIMAGE_LOCK;
  }

  const t2vArea = q('#t2v-form textarea[name="negative_prompt"]');
  if (t2vArea && !t2vArea.value.trim()) {
    t2vArea.value = DEFAULT_NEGATIVE_PROMPT_VIDEO;
  }

  const i2vRemixArea = q('#i2v-remix-form textarea[name="negative_prompt"]');
  if (i2vRemixArea && !i2vRemixArea.value.trim()) {
    i2vRemixArea.value = DEFAULT_NEGATIVE_PROMPT_VIDEO;
  }
}

function randomSeedValue() {
  if (window.crypto?.getRandomValues) {
    const parts = new Uint32Array(2);
    window.crypto.getRandomValues(parts);
    return (parts[0] & 0x1fffff) * 4294967296 + parts[1];
  }
  return Math.floor(Math.random() * Number.MAX_SAFE_INTEGER);
}

function bindSeedButtons() {
  qq(".seed-random-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const wrap = btn.closest(".seed-control");
      const input = wrap?.querySelector('input[name="seed"]');
      if (!input) return;
      input.value = String(randomSeedValue());
    });
  });
}

function setActiveTab(tab) {
  qq(".tab-btn").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.tab === tab);
  });
  qq(".pane").forEach((pane) => {
    pane.classList.toggle("active", pane.id === `pane-${tab}`);
  });
}

function initTabs() {
  qq(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      setActiveTab(btn.dataset.tab);
      if (btn.dataset.tab === "gallery") {
        refreshGallery();
      }
      if (btn.dataset.tab === "lora") {
        refreshLoras();
      }
    });
  });
}

function syncPreset() {
  const bindPreset = (presetSel, formSel) => {
    const preset = q(presetSel);
    const form = q(formSel);
    if (!preset || !form) return;
    const apply = () => {
      const [w, h] = String(preset.value).split("x");
      const wInput = form.querySelector('input[name="width"]');
      const hInput = form.querySelector('input[name="height"]');
      if (wInput) wInput.value = w;
      if (hInput) hInput.value = h;
    };
    preset.addEventListener("change", apply);
    apply();
  };
  bindPreset("#t2i-zimage-preset", "#t2i-zimage-form");
  bindPreset("#inpaint-zimage-preset", "#inpaint-zimage-form");
}

function bindZImageTypePresetProfile() {
  const form = q("#t2i-zimage-form");
  if (!form) return;
  const typeSel = form.querySelector('select[name="zimage_type"]');
  const redChainWrap = form.querySelector("#t2i-red-chain-wrap");
  const redChainSel = form.querySelector('select[name="red_chain"]');
  const loraStrengthRow = form.querySelector("#t2i-lora-strength-row");
  const setNum = (name, val) => {
    const el = form.querySelector(`input[name="${name}"]`);
    if (el) el.value = String(val);
  };
  const setPreset = (val) => {
    const preset = form.querySelector('select[name="preset"]');
    if (!preset) return;
    const hasOption = Array.from(preset.options || []).some((opt) => String(opt.value) === String(val));
    if (hasOption) {
      preset.value = val;
    }
    preset.dispatchEvent(new Event("change"));
  };

  const applyProfile = () => {
    if (!typeSel) return;
    const currentType = String(typeSel.value);
    const isRed = currentType === "red";
    const redChain = String(redChainSel?.value || "main");
    const isFurry = currentType === "furry";
    const isFuta = currentType === "futa";
    if (redChainWrap) {
      redChainWrap.classList.toggle("hidden", !isRed);
    }
    if (loraStrengthRow) {
      loraStrengthRow.classList.toggle("hidden", isRed || isFurry || isFuta);
    }

    const genericProfile = {
      preset: "1024x1024",
      width: 1024,
      height: 1024,
      steps_stage1: 5,
      cfg_stage1: 1.5,
      steps_stage2: 20,
      cfg_stage2: 1.3,
      denoise_stage2: 0.58,
      upscale_by: 1.25,
      lora_strength_aux: 1.25,
      lora_strength_main: -1.5,
    };
    const redMainProfile = {
      preset: "832x1536",
      width: 832,
      height: 1536,
      steps_stage1: 8,
      cfg_stage1: 1.0,
      steps_stage2: 4,
      cfg_stage2: 1.0,
      denoise_stage2: 0.33,
      upscale_by: 2.0,
      lora_strength_aux: 1.25,
      lora_strength_main: -1.5,
    };
    const redPoseProfile = {
      preset: "832x1536",
      width: 832,
      height: 1536,
      steps_stage1: 14,
      cfg_stage1: 1.6,
      steps_stage2: 10,
      cfg_stage2: 1.35,
      denoise_stage2: 0.45,
      upscale_by: 2.0,
      lora_strength_aux: 1.25,
      lora_strength_main: -1.5,
    };
    const furryProfile = {
      preset: "1024x1024",
      width: 1024,
      height: 1024,
      steps_stage1: 8,
      cfg_stage1: 1.25,
      steps_stage2: 22,
      cfg_stage2: 1.55,
      denoise_stage2: 0.5,
      upscale_by: 1.4,
      lora_strength_aux: 1.25,
      lora_strength_main: -1.5,
    };
    const futaProfile = {
      preset: "1024x1024",
      width: 1024,
      height: 1024,
      steps_stage1: 7,
      cfg_stage1: 1.2,
      steps_stage2: 20,
      cfg_stage2: 1.45,
      denoise_stage2: 0.55,
      upscale_by: 1.35,
      lora_strength_aux: 1.25,
      lora_strength_main: -1.5,
    };

    const redProfile = redChain === "pose" ? redPoseProfile : redMainProfile;
    const profile = isRed ? redProfile : isFurry ? furryProfile : isFuta ? futaProfile : genericProfile;
    setPreset(profile.preset);
    setNum("width", profile.width);
    setNum("height", profile.height);
    setNum("steps_stage1", profile.steps_stage1);
    setNum("cfg_stage1", profile.cfg_stage1);
    setNum("steps_stage2", profile.steps_stage2);
    setNum("cfg_stage2", profile.cfg_stage2);
    setNum("denoise_stage2", profile.denoise_stage2);
    setNum("upscale_by", profile.upscale_by);
    setNum("lora_strength_aux", profile.lora_strength_aux);
    setNum("lora_strength_main", profile.lora_strength_main);

    if (!isRed && redChainSel) {
      redChainSel.value = "main";
    }
  };

  if (typeSel) {
    typeSel.addEventListener("change", applyProfile);
    redChainSel?.addEventListener("change", applyProfile);
    applyProfile();
  }
}

async function submitT2IZImage(event) {
  event.preventDefault();
  const form = event.currentTarget;
  if (form.seed) {
    form.seed.value = String(randomSeedValue());
  }
  const payload = {
    zimage_type: form.zimage_type?.value || "normal",
    red_chain: form.red_chain?.value || "main",
    prompt: form.prompt.value.trim(),
    negative_prompt: form.negative_prompt.value.trim() || DEFAULT_NEGATIVE_PROMPT_ZIMAGE_T2I,
    width: toInt(form.width?.value, 1024),
    height: toInt(form.height?.value, 1024),
    seed: toInt(form.seed?.value, -1),
    preset: form.preset?.value || "1024x1024",
    steps_stage1: toInt(form.steps_stage1?.value, 5),
    cfg_stage1: toFloat(form.cfg_stage1?.value, 1.5),
    steps_stage2: toInt(form.steps_stage2?.value, 20),
    cfg_stage2: toFloat(form.cfg_stage2?.value, 1.3),
    denoise_stage2: toFloat(form.denoise_stage2?.value, 0.58),
    upscale_by: toFloat(form.upscale_by?.value, 1.25),
    lora_strength_aux: toFloat(form.lora_strength_aux?.value, 1.25),
    lora_strength_main: toFloat(form.lora_strength_main?.value, -1.5),
  };
  if (!payload.prompt) {
    showMessage("Prompt 不能为空", "error");
    return;
  }
  const task = await requestJson("/api/t2i_zimage", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  upsertTask(task);
  showMessage(`Z-Image 任务已入队: ${task.id} | seed=${payload.seed}`, "ok");
}

function initInpaintZImageForm() {
  const fileInput = q("#inpaint-zimage-image-file");
  let imageLoaded = false;

  fileInput?.addEventListener("change", () => {
    imageLoaded = !!fileInput.files?.[0];
  });

  function setFaceImageBlob(blob, filename) {
    if (!fileInput) return;
    const dt = new DataTransfer();
    dt.items.add(new File([blob], filename || "face_ref.png", { type: blob.type || "image/png" }));
    fileInput.files = dt.files;
    fileInput.dispatchEvent(new Event("change"));
  }

  return {
    isReady: () => imageLoaded && !!fileInput?.files?.[0],
    imageFile: () => fileInput?.files?.[0] ?? null,
    setFaceImageBlob,
  };
}

async function submitInpaintZImage(event, inpaintFormState) {
  event.preventDefault();
  const form = event.currentTarget;
  if (!inpaintFormState.isReady()) {
    showMessage("请先上传脸部参考图", "error");
    return;
  }
  const image = inpaintFormState.imageFile();
  if (!image) {
    showMessage("脸部参考图读取失败", "error");
    return;
  }

  if (form.seed) {
    form.seed.value = String(randomSeedValue());
  }

  const fd = new FormData();
  fd.append("zimage_type", form.zimage_type?.value || "normal");
  fd.append("prompt", form.prompt.value.trim());
  fd.append("negative_prompt", form.negative_prompt.value.trim());
  fd.append("preset", form.preset?.value || "576x1088");
  fd.append("width", String(toInt(form.width?.value, 576)));
  fd.append("height", String(toInt(form.height?.value, 1088)));
  fd.append("seed", String(toInt(form.seed?.value, -1)));
  fd.append("steps_stage1", String(toInt(form.steps_stage1?.value, 4)));
  fd.append("cfg_stage1", String(toFloat(form.cfg_stage1?.value, 1.0)));
  fd.append("steps_stage2", String(toInt(form.steps_stage2?.value, 14)));
  fd.append("cfg_stage2", String(toFloat(form.cfg_stage2?.value, 1.3)));
  fd.append("denoise_stage2", String(toFloat(form.denoise_stage2?.value, 0.58)));
  fd.append("upscale_by", String(toFloat(form.upscale_by?.value, 1.5)));
  fd.append("final_scale", String(toFloat(form.final_scale?.value, 0.75)));
  fd.append("aura_shift", String(toFloat(form.aura_shift?.value, 1.15)));
  fd.append("refine_enabled", form.refine_enabled?.value || "false");

  fd.append("reactor_enabled", form.reactor_enabled?.value || "true");
  fd.append("reactor_swap_model", form.reactor_swap_model?.value?.trim?.() || "inswapper_128.onnx");
  fd.append("reactor_facedetection", form.reactor_facedetection?.value?.trim?.() || "retinaface_resnet50");
  fd.append("reactor_face_restore_model", form.reactor_face_restore_model?.value?.trim?.() || "codeformer-v0.1.0.pth");
  fd.append(
    "reactor_face_restore_visibility",
    String(toFloat(form.reactor_face_restore_visibility?.value, 0.8))
  );
  fd.append("reactor_codeformer_weight", String(toFloat(form.reactor_codeformer_weight?.value, 0.5)));
  fd.append("reactor_detect_gender_input", form.reactor_detect_gender_input?.value || "no");
  fd.append("reactor_detect_gender_source", form.reactor_detect_gender_source?.value || "no");
  fd.append("reactor_input_faces_index", form.reactor_input_faces_index?.value?.trim?.() || "0");
  fd.append("reactor_source_faces_index", form.reactor_source_faces_index?.value?.trim?.() || "0");
  fd.append("reactor_console_log_level", String(toInt(form.reactor_console_log_level?.value, 1)));
  fd.append("image", image, image.name);

  const task = await requestJson("/api/inpaint_zimage", { method: "POST", body: fd });
  upsertTask(task);
  showMessage(`Z-IMAGE 锁定生图任务已入队: ${task.id}`, "ok");
}

async function submitT2V(event) {
  event.preventDefault();
  const form = event.currentTarget;
  if (form.seed) {
    form.seed.value = String(randomSeedValue());
  }
  const payload = {
    prompt: form.prompt.value.trim(),
    negative_prompt: form.negative_prompt.value.trim() || DEFAULT_NEGATIVE_PROMPT_VIDEO,
    width: toInt(form.width.value, 832),
    height: toInt(form.height.value, 480),
    duration_sec: toInt(form.duration_sec.value, 4),
    fps: toInt(form.fps.value, 16),
    steps: toInt(form.steps.value, 8),
    cfg: toFloat(form.cfg.value, 1.0),
    seed: toInt(form.seed.value, -1),
  };
  if (!payload.prompt) {
    showMessage("Prompt 不能为空", "error");
    return;
  }
  const task = await requestJson("/api/t2v", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  upsertTask(task);
  showMessage(`视频任务已入队: ${task.id}`, "ok");
}

function initI2VRemixForm() {
  const form = q("#i2v-remix-form");
  if (!form) return;
  const chainSel = form.querySelector('select[name="chain"]');
  const endWrap = form.querySelector("#i2v-remix-end-wrap");
  const endInput = form.querySelector('input[name="image_end"]');
  const apply = () => {
    const chain = chainSel?.value || "single";
    const needEnd = chain === "first_last";
    if (endWrap) {
      endWrap.classList.toggle("hidden", !needEnd);
    }
    if (endInput) {
      endInput.required = needEnd;
    }
  };
  chainSel?.addEventListener("change", apply);
  apply();
}

async function submitI2VRemix(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const chain = form.chain?.value || "single";
  const image = form.image.files?.[0];
  const imageEnd = form.image_end?.files?.[0];
  if (!image) {
    showMessage("请上传首帧图片", "error");
    return;
  }
  if (chain === "first_last" && !imageEnd) {
    showMessage("首尾帧链路需要上传尾帧图片", "error");
    return;
  }
  if (form.seed) {
    form.seed.value = String(randomSeedValue());
  }

  const fd = new FormData();
  fd.append("chain", chain);
  fd.append("prompt", form.prompt.value.trim());
  fd.append("negative_prompt", form.negative_prompt.value.trim() || DEFAULT_NEGATIVE_PROMPT_VIDEO);
  fd.append("width", String(toInt(form.width.value, 832)));
  fd.append("height", String(toInt(form.height.value, 480)));
  fd.append("duration_sec", String(toInt(form.duration_sec.value, 4)));
  fd.append("fps", String(toInt(form.fps.value, 16)));
  fd.append("steps", String(toInt(form.steps.value, 8)));
  fd.append("cfg", String(toFloat(form.cfg.value, 1.0)));
  fd.append("seed", String(toInt(form.seed.value, -1)));
  fd.append("image", image, image.name);
  if (imageEnd) {
    fd.append("image_end", imageEnd, imageEnd.name);
  }

  const task = await requestJson("/api/i2v_wan_remix", { method: "POST", body: fd });
  upsertTask(task);
  showMessage(`Wan Remix I2V 任务已入队: ${task.id}`, "ok");
}

function upsertTask(task) {
  state.tasks.set(task.id, task);
  renderTasks();
  renderQueue();
}

function taskStatusClass(status) {
  if (status === "completed") return "ok";
  if (status === "failed") return "error";
  if (status === "running") return "running";
  return "queued";
}

function renderTaskOutputs(task) {
  if (!task.outputs || task.outputs.length === 0) return "";
  const stageRank = (out) => {
    if (task?.task_type !== "inpaint") return 99;
    const source = String(out?.source_filename || "").toLowerCase();
    if (source.includes("_before")) return 0;
    if (source.includes("_after_swap")) return 1;
    if (source.includes("_after")) return 2;
    return 3;
  };
  const outputs = [...task.outputs].sort((a, b) => stageRank(a) - stageRank(b));
  return outputs
    .map((out) => {
      const url = escapeHtml(out.url || "");
      const sourceUrl = String(out.url || "").toLowerCase();
      const isAnimatedImage = sourceUrl.endsWith(".avif") || sourceUrl.endsWith(".webp") || sourceUrl.endsWith(".gif");
      const source = String(out?.source_filename || "").toLowerCase();
      const stageLabel =
        task?.task_type === "inpaint"
          ? source.includes("_before")
            ? "换脸前"
            : source.includes("_after_swap")
              ? "换脸后(未精修)"
              : source.includes("_after")
                ? "换脸后"
                : ""
          : "";
      if (out.kind === "video") {
        if (isAnimatedImage) {
          return `<img src="${url}" alt="output" loading="lazy" />`;
        }
        return `<video src="${url}" controls preload="metadata"></video>`;
      }
      return `
        <div class="output-wrap">
          ${stageLabel ? `<div class="output-stage">${escapeHtml(stageLabel)}</div>` : ""}
          <img src="${url}" alt="output" loading="lazy" />
          <button type="button" class="ghost use-as-face-btn" data-url="${url}">用作换脸参考</button>
        </div>`;
    })
    .join("");
}

function renderTaskLoraInfo(task) {
  const params = task?.params || {};
  const debug = params.lora_debug;
  const selectedNames = Array.isArray(params.lora_names)
    ? params.lora_names.map((item) => String(item || "").trim()).filter(Boolean)
    : [];
  const selectedName = String(params.lora_name || "").trim();
  const displayNames = selectedNames.length ? selectedNames : selectedName ? [selectedName] : [];
  if (!debug && displayNames.length === 0) {
    return "";
  }

  if (Array.isArray(debug)) {
    const parts = debug.map((item) => {
      const name = String(item?.requested_name || "");
      if (item?.applied) {
        const s = String(item?.strength_model ?? item?.requested_strength ?? "");
        const nodeId = String(item?.node_id || "?");
        return `${name}@${s} (#${nodeId})`;
      }
      return `${name}(failed:${item?.reason || "not_applied"})`;
    });
    return `<div class="task-meta">LoRA: ${escapeHtml(parts.join(" | "))}</div>`;
  }

  if (debug && debug.applied) {
    const name = escapeHtml(debug.requested_name || selectedName || "(unknown)");
    const nodeId = escapeHtml(debug.node_id || "?");
    const sm = escapeHtml(String(debug.strength_model ?? debug.requested_strength ?? ""));
    const sc = escapeHtml(String(debug.strength_clip ?? debug.requested_strength ?? ""));
    return `<div class="task-meta">LoRA: ${name} | node #${nodeId} (model=${sm}, clip=${sc})</div>`;
  }

  if (debug && debug.applied === false) {
    const name = escapeHtml(debug.requested_name || selectedName || "(unknown)");
    const reason = escapeHtml(debug.reason || "not_applied");
    return `<div class="task-meta">LoRA: ${name} | 注入失败: ${reason}</div>`;
  }

  return `<div class="task-meta">LoRA: ${escapeHtml(displayNames.join(", "))} | waiting</div>`;
}

function renderTaskModelProfile(task) {
  const profile = task?.params?.model_profile;
  if (!profile || !profile.applied) return "";
  const name = escapeHtml(String(profile.name || "profile"));
  const steps = escapeHtml(String(profile.steps ?? ""));
  const cfg = escapeHtml(String(profile.cfg ?? ""));
  const lora = escapeHtml(String(profile.lora_strength ?? ""));
  return `<div class="task-meta">Model Profile: ${name} | steps=${steps}, cfg=${cfg}, lora=${lora}</div>`;
}

function renderTaskModelInfo(task) {
  const params = task?.params || {};
  const engine = String(params.engine || "").trim();
  const source = String(params.model_source || "").trim();
  const zimageType = String(params.zimage_type || "").trim();
  const modelName = String(params.model_name || "").trim();
  const unetStage1 = String(params.unet_name_stage1 || "").trim();
  const unetStage2 = String(params.unet_name_stage2 || "").trim();
  const clipName = String(params.clip_name || "").trim();
  const vaeName = String(params.vae_name || "").trim();
  const clipFromCkpt = Boolean(params.clip_from_checkpoint);
  const vaeFromCkpt = Boolean(params.vae_from_checkpoint);
  if (!engine && !source && !zimageType && !modelName && !clipName && !vaeName && !unetStage1 && !unetStage2 && !clipFromCkpt && !vaeFromCkpt) return "";

  const pieces = [];
  if (engine) pieces.push(`engine=${engine}`);
  if (source) pieces.push(`source=${source}`);
  if (zimageType) pieces.push(`type=${zimageType}`);
  if (modelName) pieces.push(`model=${modelName}`);
  if (unetStage1) pieces.push(`stage1=${unetStage1}`);
  if (unetStage2) pieces.push(`stage2=${unetStage2}`);
  if (clipFromCkpt) {
    pieces.push("clip=from_checkpoint");
  } else if (clipName) {
    pieces.push(`clip=${clipName}`);
  }
  if (vaeFromCkpt) {
    pieces.push("vae=from_checkpoint");
  } else if (vaeName) {
    pieces.push(`vae=${vaeName}`);
  }
  return `<div class="task-meta">Model: ${escapeHtml(pieces.join(" | "))}</div>`;
}

function renderTasks() {
  const wrap = q("#task-list");
  const items = Array.from(state.tasks.values()).sort(
    (a, b) => new Date(b.created_at) - new Date(a.created_at)
  );
  if (items.length === 0) {
    wrap.innerHTML = `<p class="muted">暂无任务</p>`;
    return;
  }
  wrap.innerHTML = items
    .slice(0, 80)
    .map((task) => {
      const prompt = escapeHtml(task.params?.prompt || "");
      return `
      <article class="task-card">
        <div class="task-head">
          <span class="task-type">${escapeHtml(task.task_type)}</span>
          <span class="status ${taskStatusClass(task.status)}">${escapeHtml(task.status)}</span>
        </div>
        <div class="task-meta">${escapeHtml(task.id)} | ${toLocalTime(task.created_at)}</div>
        <div class="task-prompt">${prompt ? prompt.slice(0, 160) : "(no prompt)"}</div>
        ${renderTaskModelInfo(task)}
        ${renderTaskModelProfile(task)}
        ${renderTaskLoraInfo(task)}
        <div class="task-progress">
          <div class="task-progress-bar" style="width:${Math.max(0, Math.min(100, task.progress || 0))}%"></div>
        </div>
        <div class="task-error">${task.error ? escapeHtml(task.error) : ""}</div>
        <div class="task-outputs">${renderTaskOutputs(task)}</div>
      </article>`;
    })
    .join("");
}

function renderQueue() {
  const summary = q("#queue-summary");
  const detail = q("#queue-detail");
  const bar = q("#queue-progress");

  const inflight = state.queue.total_inflight || 0;
  summary.textContent = `Queue: running ${state.queue.running.length} | queued ${state.queue.queued_count} | inflight ${inflight}`;

  let runningProgress = 0;
  if (state.queue.running.length > 0) {
    const runningTask = state.tasks.get(state.queue.running[0]);
    runningProgress = runningTask?.progress ?? 0;
    detail.textContent = runningTask
      ? `${runningTask.task_type} ${runningTask.id.slice(0, 8)} (${runningTask.progress}%)`
      : "running...";
  } else {
    detail.textContent = state.queue.queued_count > 0 ? "等待执行..." : "队列空闲";
  }
  bar.style.width = `${Math.max(0, Math.min(100, runningProgress))}%`;
}

async function refreshQueue() {
  try {
    state.queue = await requestJson("/api/queue");
    renderQueue();
  } catch (err) {
    showMessage(`队列刷新失败: ${err.message}`, "error");
  }
}

async function refreshTasks() {
  try {
    const list = await requestJson("/api/tasks?limit=200");
    list.forEach((task) => state.tasks.set(task.id, task));
    renderTasks();
  } catch (err) {
    showMessage(`任务刷新失败: ${err.message}`, "error");
  }
}

function renderGallery(items) {
  const grid = q("#gallery-grid");
  if (!items || items.length === 0) {
    grid.innerHTML = `<p class="muted">画廊暂无产物</p>`;
    return;
  }
  grid.innerHTML = items
    .map((item) => {
      const url = escapeHtml(item.url);
      const meta = `${escapeHtml(item.kind)} | ${toLocalTime(item.modified_at)}`;
      const sourceUrl = String(item.url || "").toLowerCase();
      const isAnimatedImage = sourceUrl.endsWith(".avif") || sourceUrl.endsWith(".webp") || sourceUrl.endsWith(".gif");
      const media =
        item.kind === "video"
          ? isAnimatedImage
            ? `<img src="${url}" alt="${escapeHtml(item.filename)}" loading="lazy" />`
            : `<video src="${url}" controls preload="metadata"></video>`
          : `<img src="${url}" alt="${escapeHtml(item.filename)}" loading="lazy" />`;
      return `<article class="gallery-card">${media}<div class="gallery-meta">${meta}</div></article>`;
    })
    .join("");
}

async function refreshGallery() {
  try {
    const kind = q("#gallery-kind").value;
    const list = await requestJson(`/api/gallery?kind=${encodeURIComponent(kind)}&limit=200`);
    renderGallery(list);
  } catch (err) {
    showMessage(`画廊刷新失败: ${err.message}`, "error");
  }
}

function renderLoraList() {
  const wrap = q("#lora-list");
  if (!wrap) return;
  if (!state.loras || state.loras.length === 0) {
    wrap.innerHTML = `<p class="muted">LoRA 目录为空</p>`;
    return;
  }
  wrap.innerHTML = state.loras
    .map((item) => {
      const mb = (item.size_bytes / (1024 * 1024)).toFixed(2);
      return `
        <article class="lora-item">
          <div class="lora-main">
            <div class="lora-name">${escapeHtml(item.name)}</div>
            <div class="lora-meta">${mb} MB | ${toLocalTime(item.modified_at)}</div>
          </div>
          <button type="button" class="ghost danger-lite" data-del-lora="${escapeHtml(item.name)}">删除</button>
        </article>
      `;
    })
    .join("");
}

async function refreshLoras() {
  try {
    const list = await requestJson("/api/loras?limit=1000");
    state.loras = list;
    renderLoraList();
  } catch (err) {
    showMessage(`LoRA 列表刷新失败: ${err.message}`, "error");
  }
}

async function uploadLora(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const file = form.file.files?.[0];
  if (!file) {
    showMessage("请选择 LoRA 文件", "error");
    return;
  }
  const fd = new FormData();
  fd.append("file", file, file.name);
  fd.append("overwrite", form.overwrite.checked ? "true" : "false");
  const item = await requestJson("/api/loras/upload", { method: "POST", body: fd });
  showMessage(`LoRA 上传完成: ${item.name}`, "ok");
  form.reset();
  await refreshLoras();
}

async function downloadLora(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const payload = {
    repo_id: form.repo_id.value.trim(),
    filename: form.filename.value.trim(),
    target_name: form.target_name.value.trim() || null,
    token: form.token.value.trim() || null,
    force: !!form.force.checked,
  };
  if (!payload.repo_id || !payload.filename) {
    showMessage("repo_id 和 filename 必填", "error");
    return;
  }
  const item = await requestJson("/api/loras/download", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  showMessage(`LoRA 下载完成: ${item.name}`, "ok");
  await refreshLoras();
}

async function deleteLora(name) {
  const ok = window.confirm(`确认删除 LoRA: ${name} ?`);
  if (!ok) return;
  await requestJson(`/api/loras/${encodeURIComponent(name)}`, { method: "DELETE" });
  showMessage(`已删除: ${name}`, "ok");
  await refreshLoras();
}

function startEvents() {
  if (state.eventSource) {
    state.eventSource.close();
  }
  const es = new EventSource("/api/tasks/stream");
  state.eventSource = es;
  es.onmessage = (event) => {
    if (!event.data) return;
    let payload = null;
    try {
      payload = JSON.parse(event.data);
    } catch (_) {
      return;
    }
    if (payload?.task?.id) {
      const task = payload.task;
      state.tasks.set(task.id, task);
      renderTasks();
      renderQueue();
      if (task.status === "completed") {
        refreshGallery();
      }
      if (task.status === "failed") {
        showMessage(`任务失败 ${task.id.slice(0, 8)}: ${task.error || ""}`, "error");
      }
    }
  };
  es.onerror = () => {
    es.close();
    window.setTimeout(startEvents, 2500);
  };
}

function bindButtons() {
  q("#refresh-tasks")?.addEventListener("click", refreshTasks);
  q("#refresh-gallery")?.addEventListener("click", refreshGallery);
  q("#gallery-refresh")?.addEventListener("click", refreshGallery);
}

function bindForms() {
  q("#t2i-zimage-form")?.addEventListener("submit", (e) => {
    submitT2IZImage(e).catch((err) => showMessage(`提交失败: ${err.message}`, "error"));
  });
  q("#t2v-form")?.addEventListener("submit", (e) => {
    submitT2V(e).catch((err) => showMessage(`提交失败: ${err.message}`, "error"));
  });
  const inpaintZImageFormState = initInpaintZImageForm();
  q("#inpaint-zimage-form")?.addEventListener("submit", (e) => {
    submitInpaintZImage(e, inpaintZImageFormState).catch((err) =>
      showMessage(`提交失败: ${err.message}`, "error")
    );
  });

  q("#task-list")?.addEventListener("click", (e) => {
    const btn = e.target.closest(".use-as-face-btn");
    if (!btn) return;
    const url = btn.dataset.url;
    if (!url) return;
    btn.textContent = "加载中...";
    btn.disabled = true;
    fetch(url)
      .then((r) => r.blob())
      .then((blob) => {
        inpaintZImageFormState.setFaceImageBlob(blob, "face_ref.png");
        setActiveTab("inpaint-zimage");
        showMessage("已填入换脸参考图，补完提示词后可直接提交", "ok");
        btn.textContent = "已填入";
        btn.disabled = false;
      })
      .catch(() => {
        btn.textContent = "用作换脸参考";
        btn.disabled = false;
        showMessage("图片加载失败", "error");
      });
  });

  q("#i2v-remix-form")?.addEventListener("submit", (e) => {
    submitI2VRemix(e).catch((err) => showMessage(`提交失败: ${err.message}`, "error"));
  });

  q("#lora-upload-form")?.addEventListener("submit", (e) => {
    uploadLora(e).catch((err) => showMessage(`LoRA 上传失败: ${err.message}`, "error"));
  });
  q("#lora-download-form")?.addEventListener("submit", (e) => {
    downloadLora(e).catch((err) => showMessage(`LoRA 下载失败: ${err.message}`, "error"));
  });
  q("#lora-refresh")?.addEventListener("click", () => {
    refreshLoras().catch((err) => showMessage(`LoRA 刷新失败: ${err.message}`, "error"));
  });
  q("#lora-list")?.addEventListener("click", (event) => {
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;
    const name = target.getAttribute("data-del-lora");
    if (!name) return;
    deleteLora(name).catch((err) => showMessage(`LoRA 删除失败: ${err.message}`, "error"));
  });
}

async function bootstrap() {
  applyDefaultNegativePrompt();
  initTabs();
  syncPreset();
  bindZImageTypePresetProfile();
  initI2VRemixForm();
  bindSeedButtons();
  bindButtons();
  bindForms();
  await Promise.all([refreshQueue(), refreshTasks(), refreshGallery(), refreshLoras()]);
  startEvents();
  window.setInterval(refreshQueue, 3000);
  window.setInterval(refreshTasks, 15000);
}

bootstrap().catch((err) => showMessage(`初始化失败: ${err.message}`, "error"));
