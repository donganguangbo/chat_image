import { app } from "../../../scripts/app.js";

console.log("[PromptToolsPro] Stable Clean Mode - Pure prompt function without any LoRA logic");

// 常量定义
const GRID_NODE_NAMES = new Set(["prompt_grid_16_pro","PromptGrid16Pro","prompt_grid_16_classic","PromptGrid16Classic","prompt_grid_1"]);
const TEXT_NODE_NAMES = new Set(["prompt_text_tool_pro","PromptTextToolPro","prompt_text_tool"]);
const RECEIVER_NODE_NAMES = new Set(["prompt_text_receiver_pro","PromptTextReceiverPro","prompt_text_receiver"]);

const CELL_COUNT = 16;
const MIN_GRID_SIZE = 260;

// 全局扩展与弹框上下文缓存，隔离内存泄露
window.__pg_macro_rules = window.__pg_macro_rules || {};
window.__pg_fixed_prompt_data = window.__pg_fixed_prompt_data || { text: "", remove: [] };
window.__pg_editing_context = null;

// ----------------------------------------------------
// 🛠️ 纯净基础辅助函数区
// ----------------------------------------------------

function clamp(v, min, max) { 
    return Math.max(min, Math.min(max, v)); 
}

function markChanged(node) {
    node.setDirtyCanvas?.(true, true);
    node.graph?.setDirtyCanvas?.(true, true);
    node.graph?.change?.();
}

function getWidget(node, name) {
    return node.widgets?.find((w) => w.name === name) || node.__hidden_widgets?.find((w) => w.name === name) || null;
}

function hideWidget(w) {
    if (!w) return;
    w.type = "hidden"; w.hidden = true; w.disabled = true; w.y = -100000; w.last_y = -100000;
    w.options = w.options || {}; w.options.hidden = true;
    w.computeSize = () => [0, -4]; w.draw = () => {}; w.mouse = () => false;
    if (w.inputEl) { w.inputEl.style.display = "none"; w.inputEl.style.pointerEvents = "none"; }
    if (!w.serializeValue) w.serializeValue = function () { return this.value; };
}

function hideAllWidgets(node) {
    node.__hidden_widgets = node.__hidden_widgets || [];
    for (const w of node.widgets || []) {
        hideWidget(w);
        if (!node.__hidden_widgets.includes(w)) node.__hidden_widgets.push(w);
    }
    node.serialize_widgets = true;
    node.widgets_start_y = 0;
}

function escapeRegExp(string) {
    if (!string) return "";
    return string.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function toText(v) {
    if (v === undefined || v === null) return "";
    if (typeof v === "string") return v === "[object Object]" ? "" : v;
    if (typeof v === "object") {
        let res = v.text || v.value || v.content || "";
        if (typeof res === "object") { try { res = JSON.stringify(res); } catch(e) { res = ""; } }
        return String(res) === "[object Object]" ? "" : String(res);
    }
    return String(v) === "[object Object]" ? "" : String(v);
}

function inRect(pos, r) { 
    return pos && r && pos[0] >= r.x && pos[0] <= r.x + r.w && pos[1] >= r.y && pos[1] <= r.y + r.h; 
}

function localPos(node, first, second, third) {
    if (Array.isArray(first)) return first;
    if (Array.isArray(second)) return second;
    const event = first instanceof Event ? first : (second instanceof Event ? second : null);
    const canvas = third || app.canvas;
    if (event && canvas?.convertEventToCanvasOffset) {
        const graphPos = canvas.convertEventToCanvasOffset(event);
        return [graphPos[0] - node.pos[0], graphPos[1] - node.pos[1]];
    }
    return null;
}

async function copyText(text) { try { await navigator.clipboard.writeText(String(text || "")); } catch (err) {} }
async function pasteText() { try { return await navigator.clipboard.readText(); } catch (err) { return ""; } }
function runWorkflow(node) {
    markChanged(node);
    app.queuePrompt(0);
}

function flashNodeButton(node, key) {
    node.__flash_button = key;
    node.setDirtyCanvas?.(true, true);
    setTimeout(() => {
        if (node.__flash_button === key) {
            node.__flash_button = null;
            node.setDirtyCanvas?.(true, true);
        }
    }, 180);
}

function roundRect(ctx, x, y, w, h, r) {
    r = Math.min(r, w / 2, h / 2); ctx.beginPath(); ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
}

function button(ctx, b) {
    ctx.save();
    let scaleVal = 1.0;
    if (b.flash) {
        scaleVal = 0.95; ctx.translate(b.x + b.w/2, b.y + b.h/2); ctx.scale(scaleVal, scaleVal); ctx.translate(-(b.x + b.w/2), -(b.y + b.h/2));
    }
    roundRect(ctx, b.x, b.y, b.w, b.h, Math.min(7, b.h / 2)); ctx.fillStyle = b.flash ? "#ffffff" : b.color; ctx.fill();
    if (b.flash) { ctx.strokeStyle = "#ffffff"; ctx.lineWidth = 2; ctx.stroke(); }
    ctx.fillStyle = b.flash ? "#111111" : "#fff"; ctx.font = `bold ${b.font || 12}px Arial`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
    ctx.fillText(b.label, b.x + b.w / 2, b.y + b.h / 2); ctx.restore();
}

function makeModalButton(label, color) {
    const btn = document.createElement("button"); btn.innerText = label;
    Object.assign(btn.style, { height: "30px", padding: "0 14px", border: "0", borderRadius: "6px", background: color, color: "#fff", cursor: "pointer", fontWeight: "bold", fontSize: "13px", boxSizing: "border-box" });
    return btn;
}

// ----------------------------------------------------
// 📝 限制触发词规则与固定前缀数据拉取
// ----------------------------------------------------
async function syncConfigFromBackend() {
    try {
        const r1 = await fetch("/prompt_grid/get_macros"); const d1 = await r1.json();
        window.__pg_macro_rules = d1.rules || {};
    } catch(e) {}
    try {
        const r2 = await fetch("/prompt_grid/get_fixed_prompt"); const d2 = await r2.json();
        window.__pg_fixed_prompt_data = d2 || { text: "", remove: [] };
    } catch(e) {}
}
syncConfigFromBackend();

function getExpandedFixedText(fixedText, mainText) {
    if (!fixedText || !fixedText.trim()) return "";
    const macroRules = window.__pg_macro_rules || {};
    let activeRules = [];
    for (let trigger in macroRules) {
        let pattern = new RegExp("(?<![a-zA-Z0-9_])" + escapeRegExp(trigger) + "(?![a-zA-Z0-9_])", "i");
        if (pattern.test(mainText)) activeRules.push(macroRules[trigger]);
    }
    
    let conflicts = [];
    activeRules.forEach(rule => {
        if (rule && rule.remove) {
            const arr = Array.isArray(rule.remove) ? rule.remove : (typeof rule.remove === 'string' ? [rule.remove] : []);
            arr.forEach(rm => {
                if (typeof rm === 'string') {
                    rm.split(/[,，.。;；]/).forEach(p => { if (p.trim()) conflicts.push(p.trim().toLowerCase()); });
                }
            });
        }
    });

    if (window.__pg_fixed_prompt_data && Array.isArray(window.__pg_fixed_prompt_data.remove)) {
        window.__pg_fixed_prompt_data.remove.forEach(rm => { if (rm.trim()) conflicts.push(rm.trim().toLowerCase()); });
    }

    let clauses = fixedText.split(/([,，.。;；\n\r]+)/);
    let finalClauses = [];
    clauses.forEach(clause => {
        if (/^[,，.。;；\n\r]+$/.test(clause)) return;
        let trimmed = clause.trim();
        if (!trimmed) return;
        let hasConflict = conflicts.some(conf => trimmed.toLowerCase().includes(conf));
        if (!hasConflict) {
            finalClauses.push(trimmed);
        }
    });
    let isChinese = /[\u4e00-\u9fff]/.test(fixedText), comma = isChinese ? "，" : ", ";
    return finalClauses.join(comma);
}

function getExpandedText(text) {
    if (typeof window.__pg_compileFinalPrompt === "function") {
        return window.__pg_compileFinalPrompt(text);
    }
    if (typeof window.__pg_getExpandedText === "function") {
        return window.__pg_getExpandedText(text);
    }
    return text;
}

// 🌟 重点核心：还原“宏与全局固定词冲突配置”界面
async function openMacroConfigEditor() {
    const old = document.getElementById("pg-macro-editor-modal");
    if (old) old.remove();

    const overlay = document.createElement("div");
    overlay.id = "pg-macro-editor-modal";
    Object.assign(overlay.style, {
        position: "fixed", left: "0", top: "0", right: "0", bottom: "0",
        background: "rgba(0,0,0,0.68)", zIndex: "1000000",
        display: "flex", alignItems: "center", justifyContent: "center"
    });

    const box = document.createElement("div");
    Object.assign(box.style, {
        width: "1100px", height: "820px", background: "#252528",
        border: "1px solid #4f4f52", borderRadius: "12px",
        display: "flex", flexDirection: "column", overflow: "hidden",
        boxShadow: "0 15px 45px rgba(0,0,0,0.65)", color: "#fff",
        fontFamily: "Arial, sans-serif"
    });

    // 1. 头标题栏
    const header = document.createElement("div");
    Object.assign(header.style, {
        height: "48px", display: "flex", alignItems: "center",
        justifyContent: "space-between", padding: "0 16px",
        background: "#2e2e31", color: "#fff", fontWeight: "bold",
        borderBottom: "1px solid #3c3c3f"
    });
    const title = document.createElement("div");
    title.innerText = "🛠️ 宏与全局固定词冲突配置";
    const close = makeModalButton("×", "#444");
    close.style.width = "30px"; close.style.height = "30px"; close.style.padding = "0"; close.style.fontSize = "22px";
    close.onclick = () => overlay.remove();
    header.appendChild(title); header.appendChild(close);
    box.appendChild(header);

    // 滚动主容器
    const container = document.createElement("div");
    Object.assign(container.style, {
        flex: "1", padding: "16px", overflowY: "auto", display: "flex", flexDirection: "column", gap: "12px"
    });
    box.appendChild(container);

    // 说明描述
    const desc = document.createElement("div");
    desc.innerText = "配置宏映射：当输入提示词含有触发字时，自动拦截拼接。置顶的全局固定前缀会自动加载在最终输出的最前位。";
    Object.assign(desc.style, { color: "#8a8a90", fontSize: "12px", borderLeft: "3px solid #e67e22", paddingLeft: "8px" });
    container.appendChild(desc);

    // 2. 📌 风格预设外边框盒子 (还原图2大橙边样式)
    const presetBox = document.createElement("div");
    Object.assign(presetBox.style, {
        border: "1.5px solid #d35400", borderRadius: "8px", padding: "14px",
        display: "flex", flexDirection: "column", gap: "10px", background: "#1b1b1d"
    });
    container.appendChild(presetBox);

    // 预设控制头部行
    const pHeader = document.createElement("div");
    Object.assign(pHeader.style, { display: "flex", alignItems: "center", gap: "10px" });
    
    const pinLabel = document.createElement("span");
    pinLabel.innerHTML = "📌 <span style='color:#ffaa66; font-weight:bold; font-size:13px;'>固定风格预设: (该项全局生效)</span>";

    const presetSelect = document.createElement("select");
    Object.assign(presetSelect.style, {
        background: "#101012", color: "#ffa502", border: "1px solid #7f8c8d",
        borderRadius: "4px", padding: "4px 8px", fontSize: "13px", outline: "none", cursor: "pointer"
    });

    const renameBtn = makeModalButton("更名", "#5f27cd");
    const newBtn = makeModalButton("新建", "#10ac84");
    const delBtn = makeModalButton("删除", "#ee5253");

    pHeader.appendChild(pinLabel);
    pHeader.appendChild(presetSelect);
    pHeader.appendChild(renameBtn);
    pHeader.appendChild(newBtn);
    pHeader.appendChild(delBtn);
    presetBox.appendChild(pHeader);

    // 两个大区域文本框（左配置前缀，右冲突拦截词）
    const taRow = document.createElement("div");
    Object.assign(taRow.style, { display: "flex", gap: "12px" });
    presetBox.appendChild(taRow);

    const leftTaBox = document.createElement("div");
    Object.assign(leftTaBox.style, { flex: "3", display: "flex", flexDirection: "column", gap: "4px" });
    const leftTaLabel = document.createElement("span");
    leftTaLabel.innerText = "📄 固定前缀主体 (全局生效)";
    leftTaLabel.style.fontSize = "12px"; leftTaLabel.style.color = "#ccc";
    const leftTa = document.createElement("textarea");
    leftTa.placeholder = "例如：写实照片，采用索尼A7R5相机，85mm镜头...";
    Object.assign(leftTa.style, {
        height: "90px", background: "#101012", color: "#fff", border: "1px solid #444",
        borderRadius: "4px", padding: "8px", resize: "none", outline: "none", fontSize: "13px", lineHeight: "1.5"
    });
    leftTaBox.appendChild(leftTaLabel); leftTaBox.appendChild(leftTa);

    const rightTaBox = document.createElement("div");
    Object.assign(rightTaBox.style, { flex: "2", display: "flex", flexDirection: "column", gap: "4px" });
    const rightTaLabel = document.createElement("span");
    rightTaLabel.innerText = "⚡ 冲突淘汰触发词 / 剔除词 (多词用逗号分隔)";
    rightTaLabel.style.fontSize = "12px"; rightTaLabel.style.color = "#ccc";
    const rightTa = document.createElement("textarea");
    rightTa.placeholder = "当触发上方指令宏时，需要被剔除的画面描述...";
    Object.assign(rightTa.style, {
        height: "90px", background: "#101012", color: "#fff", border: "1px solid #444",
        borderRadius: "4px", padding: "8px", resize: "none", outline: "none", fontSize: "13px", lineHeight: "1.5"
    });
    rightTaBox.appendChild(rightTaLabel); rightTaBox.appendChild(rightTa);

    taRow.appendChild(leftTaBox); taRow.appendChild(rightTaBox);

    // 3. 新建一条替换规则按钮
    const addRuleBtn = makeModalButton("➕ 新增一条替换规则", "#10ac84");
    addRuleBtn.style.alignSelf = "flex-start";
    addRuleBtn.style.height = "32px";
    addRuleBtn.style.fontSize = "13px";
    container.appendChild(addRuleBtn);

    // 4. 重构替换列表滚动区
    const listWrapper = document.createElement("div");
    Object.assign(listWrapper.style, { display: "flex", flexDirection: "column", gap: "6px" });
    container.appendChild(listWrapper);

    // 预设本地数据加载
    let presets = [];
    try { presets = JSON.parse(window.localStorage.getItem("pg_prompt_presets") || "[]"); } catch(e) {}
    
    // 如果无预设，初始化载入默认预设
    if (presets.length === 0) {
        presets = [
            { name: "生活写实手机拍摄 (Candid)", text: "写实照片，采用索尼A7R5相机，85mm镜头，f/1.8光圈，浅景深，自然的轻微胶片颗粒，原片直出无后期修饰。", remove: "INS风，陶瓷水嫩，冷白皮，美白" }
        ];
        window.localStorage.setItem("pg_prompt_presets", JSON.stringify(presets));
    }

    await syncConfigFromBackend();
    const activeText = (window.__pg_fixed_prompt_data?.text || "").trim();
    const activeRemove = Array.isArray(window.__pg_fixed_prompt_data?.remove) 
        ? window.__pg_fixed_prompt_data.remove.join(", ") 
        : (window.__pg_fixed_prompt_data?.remove || "");

    let activeIndex = presets.findIndex(p => p.text.trim() === activeText);

    function populatePresets() {
        presetSelect.innerHTML = "";
        const optNone = document.createElement("option");
        optNone.value = "-1"; optNone.innerText = "未使用固定风格 (自定义)";
        if (activeIndex === -1) optNone.selected = true;
        presetSelect.appendChild(optNone);

        presets.forEach((p, idx) => {
            const opt = document.createElement("option");
            opt.value = idx; opt.innerText = p.name;
            if (idx === activeIndex) opt.selected = true;
            presetSelect.appendChild(opt);
        });
    }

    function loadPresetData() {
        if (activeIndex !== -1 && presets[activeIndex]) {
            leftTa.value = presets[activeIndex].text || "";
            rightTa.value = Array.isArray(presets[activeIndex].remove) 
                ? presets[activeIndex].remove.join(", ") 
                : (presets[activeIndex].remove || "");
        } else {
            leftTa.value = activeText;
            rightTa.value = activeRemove;
        }
    }

    populatePresets();
    loadPresetData();

    presetSelect.onchange = () => {
        activeIndex = parseInt(presetSelect.value);
        loadPresetData();
    };

    const syncCurrentInput = () => {
        if (activeIndex !== -1 && presets[activeIndex]) {
            presets[activeIndex].text = leftTa.value;
            presets[activeIndex].remove = rightTa.value;
        }
    };
    leftTa.addEventListener("input", syncCurrentInput);
    rightTa.addEventListener("input", syncCurrentInput);

    renameBtn.onclick = () => {
        if (activeIndex === -1) { alert("请先在下拉框选择具体的固定风格预设再进行更名！"); return; }
        const oldName = presets[activeIndex].name;
        const newName = prompt("请输入预设新名称:", oldName);
        if (newName && newName.trim()) {
            presets[activeIndex].name = newName.trim();
            window.localStorage.setItem("pg_prompt_presets", JSON.stringify(presets));
            populatePresets();
        }
    };

    newBtn.onclick = () => {
        const name = prompt("请输入新预设名称:");
        if (name && name.trim()) {
            const newPreset = { name: name.trim(), text: "", remove: "" };
            presets.push(newPreset);
            window.localStorage.setItem("pg_prompt_presets", JSON.stringify(presets));
            activeIndex = presets.length - 1;
            populatePresets();
            loadPresetData();
        }
    };

    delBtn.onclick = () => {
        if (activeIndex === -1) { alert("无可选择删除的预设！"); return; }
        if (confirm(`确定删除风格预设 "${presets[activeIndex].name}" 吗？`)) {
            presets.splice(activeIndex, 1);
            window.localStorage.setItem("pg_prompt_presets", JSON.stringify(presets));
            activeIndex = presets.length > 0 ? 0 : -1;
            populatePresets();
            loadPresetData();
        }
    };

    // 规则行渲染加载
    const rules = JSON.parse(JSON.stringify(window.__pg_macro_rules || {}));

    function addRuleRow(trg = "", data = { insert: "", remove: [] }) {
        const row = document.createElement("div");
        Object.assign(row.style, {
            display: "flex", gap: "8px", alignItems: "center",
            padding: "6px 8px", background: "#1c1c1f", borderRadius: "6px",
            border: "1px solid #3c3c3f"
        });

        const trgInput = document.createElement("input");
        trgInput.placeholder = "触发词 (如: 全luo)";
        trgInput.value = trg;
        Object.assign(trgInput.style, {
            width: "180px", height: "32px", background: "#101012", color: "#fff",
            border: "1px solid #444", borderRadius: "4px", padding: "4px 8px", boxSizing: "border-box",
            fontSize: "13px"
        });

        const insInput = document.createElement("input");
        insInput.placeholder = "自动新增提示词列表";
        insInput.value = data.insert || "";
        Object.assign(insInput.style, {
            flex: "5", height: "32px", background: "#101012", color: "#fff",
            border: "1px solid #444", borderRadius: "4px", padding: "4px 8px", boxSizing: "border-box",
            fontSize: "13px"
        });

        const remInput = document.createElement("input");
        remInput.placeholder = "被淘汰/防冲突的冲突词表 (逗号分隔)";
        remInput.value = Array.isArray(data.remove) ? data.remove.join(", ") : (data.remove || "");
        Object.assign(remInput.style, {
            flex: "3", height: "32px", background: "#101012", color: "#fff",
            border: "1px solid #444", borderRadius: "4px", padding: "4px 8px", boxSizing: "border-box",
            fontSize: "13px"
        });

        const delRowBtn = document.createElement("button");
        delRowBtn.innerHTML = "🗑️";
        Object.assign(delRowBtn.style, {
            width: "32px", height: "32px", border: "none", borderRadius: "4px",
            background: "#c0392b", color: "#fff", cursor: "pointer", fontSize: "14px",
            display: "flex", alignItems: "center", justifyContent: "center"
        });
        delRowBtn.onclick = () => row.remove();

        row.appendChild(trgInput);
        row.appendChild(insInput);
        row.appendChild(remInput);
        row.appendChild(delRowBtn);

        row.getValues = () => {
            const t = trgInput.value.trim();
            const i = insInput.value.trim();
            const r = remInput.value.trim().split(/[,，]/).map(s => s.trim()).filter(Boolean);
            if (!t) return null;
            return { key: t, val: { insert: i, remove: r } };
        };
        listWrapper.appendChild(row);
    }

    Object.keys(rules).forEach(key => addRuleRow(key, rules[key]));
    addRuleBtn.onclick = () => addRuleRow("", { insert: "", remove: [] });

    // 底座保存/取消行
    const footer = document.createElement("div");
    Object.assign(footer.style, {
        height: "56px", display: "flex", alignItems: "center",
        justifyContent: "flex-end", padding: "0 16px",
        background: "#2e2e31", gap: "12px", borderTop: "1px solid #3c3c3f"
    });

    const cancel = makeModalButton("取消", "#555");
    cancel.onclick = () => overlay.remove();

    const save = makeModalButton("保存配置", "#e67e22");
    save.style.backgroundColor = "#e67e22";
    save.style.color = "#fff";
    save.onclick = async () => {
        const nextRules = {};
        Array.from(listWrapper.children).forEach(row => {
            if (typeof row.getValues === "function") {
                const item = row.getValues();
                if (item) nextRules[item.key] = item.val;
            }
        });

        const prefixText = leftTa.value.trim();
        const conflictArray = rightTa.value.trim().split(/[,，]/).map(s => s.trim()).filter(Boolean);

        try {
            const r1 = await fetch("/prompt_grid/save_macros", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ rules: nextRules })
            });

            const r2 = await fetch("/prompt_grid/save_fixed_prompt", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ text: prefixText, remove: conflictArray })
            });

            if (r1.ok && r2.ok) {
                window.__pg_macro_rules = nextRules;
                window.__pg_fixed_prompt_data = { text: prefixText, remove: conflictArray };
                window.localStorage.setItem("pg_prompt_presets", JSON.stringify(presets));

                const activeTa = document.querySelector(".pg-main-prompt-textarea");
                if (activeTa) {
                    activeTa.dispatchEvent(new Event("input", { bubbles: true }));
                }
                overlay.remove();
            } else {
                alert("保存失败，请检查 ComfyUI 控制台终端！");
            }
        } catch(e) {
            alert("保存配置发生通信异常: " + e.message);
        }
    };

    footer.appendChild(cancel);
    footer.appendChild(save);
    box.appendChild(footer);

    overlay.appendChild(box);
    document.body.appendChild(overlay);
}

// ----------------------------------------------------
// 🎛️ 16 宫格核心配置读取 
// ----------------------------------------------------

function getGridSelected(node) {
    const w = getWidget(node, "selected"); const v = parseInt(w?.value ?? node.properties?.pg16_selected ?? node.__pg16_selected ?? 1);
    return clamp(Number.isFinite(v) ? v : 1, 1, CELL_COUNT);
}

function setGridSelected(node, index) {
    const v = clamp(parseInt(index || 1), 1, CELL_COUNT); const w = getWidget(node, "selected"); if (w) w.value = v;
    node.__pg16_selected = v; node.properties = node.properties || {}; node.properties.pg16_selected = v;
    markChanged(node);
}

function getGridPrompt(node, index) {
    const w = getWidget(node, `prompt_${index}`);
    if (w) return toText(w.value);
    return toText(node.properties?.pg16_prompts?.[index - 1]);
}

function setGridPrompt(node, index, text) {
    const value = toText(text); const w = getWidget(node, `prompt_${index}`); if (w) w.value = value;
    node.properties = node.properties || {};
    if (!Array.isArray(node.properties.pg16_prompts)) node.properties.pg16_prompts = new Array(CELL_COUNT).fill("");
    node.properties.pg16_prompts[index - 1] = value;
    markChanged(node);
}

function ensureGridNotes(node) {
    node.properties = node.properties || {};
    if (!Array.isArray(node.properties.pg16_notes)) node.properties.pg16_notes = new Array(CELL_COUNT).fill("");
    return node.properties.pg16_notes;
}

function getGridNote(node, index) { return toText(ensureGridNotes(node)[index - 1]); }
function setGridNote(node, index, text) { const notes = ensureGridNotes(node); notes[index - 1] = toText(text).replace(/\r?\n/g, " ").trim(); markChanged(node); }
function gridNoteRect(c) { return { x: c.x + 24, y: c.y + 2, w: Math.max(20, c.w - 48), h: 20 }; }
function findGridNoteCell(node, pos) { for (const c of node.__pg16_cells || []) { if (inRect(pos, gridNoteRect(c))) return c; } return null; }
function openGridNoteEditor(node, index) { const oldValue = getGridNote(node, index); const value = window.prompt(`修改第 ${index} 格备注`, oldValue); if (value === null) return; setGridNote(node, index, value); setGridSelected(node, index); }

function restoreGrid(node) {
    node.properties = node.properties || {};
    if (!Array.isArray(node.properties.pg16_prompts)) node.properties.pg16_prompts = new Array(CELL_COUNT).fill("");
    ensureGridNotes(node);
    for (let i = 1; i <= CELL_COUNT; i++) {
        const w = getWidget(node, `prompt_${i}`); let saved = toText(node.properties.pg16_prompts[i - 1]);
        if (w && saved !== "") w.value = saved;
    }
    const sw = getWidget(node, "selected"); if (sw && node.properties.pg16_selected !== undefined) { sw.value = clamp(parseInt(node.properties.pg16_selected) || 1, 1, CELL_COUNT); }
}

// 🌟 核心改动：点击“清空”时，连带 16 宫格的备注（Title）一起全部彻底清除
function clearGrid(node) {
    for (let i = 1; i <= CELL_COUNT; i++) {
        setGridPrompt(node, i, "");
        setGridNote(node, i, ""); 
    }
    setGridSelected(node, 1);
    markChanged(node);
}

function exportGrid(node) {
    const prompts = []; for (let i = 1; i <= CELL_COUNT; i++) prompts.push(getGridPrompt(node, i));
    const data = { type: node.type, selected: getGridSelected(node), prompts, notes: [...ensureGridNotes(node)] };
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob), a = document.createElement("a"); a.href = url; a.download = `${node.type.toLowerCase()}.json`;
    document.body.appendChild(a); a.click(); setTimeout(() => { a.remove(); URL.revokeObjectURL(url); }, 100);
}

function importGrid(node) {
    const input = document.createElement("input"); input.type = "file"; input.accept = ".json,.txt";
    input.onchange = () => {
        const file = input.files?.[0]; if (!file) return; const reader = new FileReader();
        reader.onload = () => {
            const content = String(reader.result || ""); let prompts = null, notes = null, selected = null;
            try {
                const data = JSON.parse(content);
                if (Array.isArray(data)) prompts = data;
                else if (Array.isArray(data?.prompts)) { prompts = data.prompts; notes = data.notes; selected = data.selected; }
            } catch (_) {}
            if (!prompts) prompts = content.includes("\n---\n") ? content.split(/\n-{3,}\n/g) : content.split(/\r?\n/g);
            for (let i = 1; i <= CELL_COUNT; i++) { setGridPrompt(node, i, prompts[i - 1] || ""); if (notes) setGridNote(node, i, notes[i - 1] || ""); }
            if (selected) setGridSelected(node, selected); markChanged(node);
        };
        reader.readAsText(file, "utf-8");
    };
    input.click();
}

function gridLayout(node) {
    const w = node.size[0], h = node.size[1];
    const top = clamp(Math.floor(h * 0.11), 30, 38), margin = 8, gap = 6;
    const gx = margin, gy = top + margin, gw = w - margin * 2, gh = h - top - margin * 2;
    const cw = (gw - gap * 3) / 4, ch = (gh - gap * 3) / 4; const cells = [];
    for (let i = 0; i < CELL_COUNT; i++) { cells.push({ index: i + 1, x: gx + (i % 4) * (cw + gap), y: gy + Math.floor(i / 4) * (ch + gap), w: cw, h: ch }); }
    return { w, h, top, margin, gap, cells };
}

function drawGridCellPreview(ctx, text, c, isHover) {
    const raw = toText(text).replace(/\r/g, "").trim(); if (!raw) return;
    const paddingX = 7, topPad = 25, bottomPad = isHover ? 28 : 8;
    const x = c.x + paddingX, y = c.y + topPad, w = c.w - paddingX * 2, h = c.h - topPad - bottomPad;
    if (w <= 20 || h <= 12) return;

    const fontSize = c.w < 120 ? 9 : 10, lineHeight = fontSize + 4, maxLines = Math.max(1, Math.floor(h / lineHeight));
    ctx.save(); ctx.beginPath(); ctx.rect(c.x + 4, c.y + 20, c.w - 8, c.h - 24); ctx.clip();
    ctx.fillStyle = "#eeeeee"; ctx.font = `${fontSize}px Arial`; ctx.textAlign = "left"; ctx.textBaseline = "top";

    const lines = [], paragraphs = raw.split(/\n+/);
    for (const para of paragraphs) {
        let current = "";
        for (const ch of para) {
            const test = current + ch; if (ctx.measureText(test).width > w && current) { lines.push(current); current = ch; if (lines.length >= maxLines) break; } else { current = test; }
        }
        if (lines.length >= maxLines) break; if (current) lines.push(current); if (lines.length >= maxLines) break;
    }

    for (let i = 0; i < Math.min(lines.length, maxLines); i++) {
        let line = lines[i]; if (i === maxLines - 1 && lines.length >= maxLines) { while (ctx.measureText(line + "...").width > w && line.length > 0) line = line.slice(0, -1); line += "..."; }
        ctx.fillText(line, x, y + i * lineHeight);
    }
    ctx.restore();
}

function drawGrid(ctx, node) {
    const l = gridLayout(node), selected = getGridSelected(node);
    node.__pg16_cells = l.cells; node.__pg16_buttons = [];

    ctx.save(); ctx.beginPath(); ctx.rect(0, 0, l.w, l.h); ctx.clip();
    ctx.fillStyle = "#07130d"; ctx.fillRect(0, 0, l.w, l.h);
    ctx.fillStyle = "#9fffc9"; ctx.font = "bold 12px Arial"; ctx.textAlign = "left"; ctx.textBaseline = "middle";

    const bh = 24, by = 5;
    const runBtn = { name: "run", label: "运行", x: l.w / 2 - 32, y: by, w: 64, h: bh, color: "#00a86b", flashColor: "#9dffd0", flash: node.__flash_button === "grid_run", font: 12 };
    node.__pg16_buttons.push(runBtn); button(ctx, runBtn);

    let x = l.w - 8 - 46 * 3 - 12;
    const rightBtns = [ ["clear_all", "清空", "#a33b3b"], ["import", "导入", "#3d6fb6"], ["export", "导出", "#a77522"] ];
    for (const [name, label, color] of rightBtns) {
        const b = { name, label, x, y: by, w: 46, h: bh, color, flashColor: "#ffffff", flash: node.__flash_button === `grid_${name}`, font: 11 };
        node.__pg16_buttons.push(b); button(ctx, b); x += 52;
    }

    for (const c of l.cells) {
        const text = getGridPrompt(node, c.index), isSelected = selected === c.index, isHover = node.__pg16_hover === c.index, hasText = text.trim() !== "";
        ctx.lineWidth = isSelected ? 2.2 : (isHover ? 1.8 : 1);
        ctx.strokeStyle = isSelected ? "#00ff91" : (isHover ? "#4a90e2" : "#3e5b49");
        ctx.fillStyle = isSelected ? "#103b26" : (hasText ? "#181818" : "#0f0f0f");

        roundRect(ctx, c.x, c.y, c.w, c.h, 8); ctx.fill(); ctx.stroke();
        ctx.fillStyle = isSelected ? "#8dffc5" : "#ffeaa0"; ctx.font = "bold 11px Arial"; ctx.textAlign = "left"; ctx.textBaseline = "top";
        ctx.fillText(String(c.index), c.x + 6, c.y + 5);

        const note = getGridNote(node, c.index);
        if (note) {
            const nr = gridNoteRect(c); ctx.save(); ctx.beginPath(); ctx.rect(nr.x, nr.y, nr.w, nr.h); ctx.clip();
            ctx.fillStyle = isSelected ? "#ffd36a" : "#d6a8ff"; ctx.font = "bold 10px Arial"; ctx.textAlign = "center"; ctx.textBaseline = "top";
            let noteText = note; while (ctx.measureText(noteText).width > nr.w && noteText.length > 0) noteText = noteText.slice(0, -1);
            ctx.fillText(noteText, c.x + c.w / 2, c.y + 5); ctx.restore();
        }

        if (isSelected) { ctx.fillStyle = "#00ff91"; ctx.font = "bold 9px Arial"; ctx.textAlign = "right"; ctx.fillText("输出", c.x + c.w - 6, c.y + 6); }
        drawGridCellPreview(ctx, text, c, isHover);

        if (isHover && c.w > 90 && c.h > 55) {
            const defs = [ ["copy", "复制", "#3d6fb6"], ["edit", "编辑", "#8a55cc"], ["paste", "粘贴", "#17864a"], ["clear", "清空", "#a33b3b"] ];
            const pad = 4, gap = 3, bw = (c.w - pad * 2 - gap * 3) / 4, bh2 = 18, by2 = c.y + c.h - bh2 - pad;
            for (let i = 0; i < defs.length; i++) {
                const [name, label, color] = defs[i];
                const b = { name, label, index: c.index, x: c.x + pad + i * (bw + gap), y: by2, w: bw, h: bh2, color, flashColor: "#ffffff", flash: node.__flash_button === `grid_${name}_${c.index}`, font: 10 };
                node.__pg16_buttons.push(b); button(ctx, b);
            }
        }
    }
    ctx.restore();
}

function findGridCell(node, pos) { return node.__pg16_cells?.find(c => inRect(pos, c)) || null; }
function findGridButton(node, pos) { return node.__pg16_buttons?.find(b => inRect(pos, b)) || null; }

function openGridEditor(node, index) {
    if (window.__pg_editor_opening) return; window.__pg_editor_opening = true;
    try {
        const old = document.getElementById("pg16-editor"); if (old) old.remove();
        const selected = clamp(index, 1, CELL_COUNT); syncConfigFromBackend();

        window.__pg_editing_context = { node: node, index: selected, type: "grid" };

        const overlay = document.createElement("div"); overlay.id = "pg16-editor";
        Object.assign(overlay.style, { position: "fixed", left: "0", top: "0", right: "0", bottom: "0", background: "rgba(0,0,0,0.58)", zIndex: "999999", display: "flex", alignItems: "center", justifyContent: "center" });

        const box = document.createElement("div");
        Object.assign(box.style, { width: "min(1080px, 88vw)", height: "min(720px, 84vh)", background: "#202020", border: "1px solid #5a5a5a", borderRadius: "12px", display: "flex", flexDirection: "column", overflow: "hidden" });

        const header = document.createElement("div");
        Object.assign(header.style, { height: "48px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c", color: "#fff", fontSize: "16px", fontWeight: "bold" });
        header.appendChild(document.createTextNode(`编辑第 ${selected} 格提示词`));

        const leftHeaderGroup = document.createElement("div"); Object.assign(leftHeaderGroup.style, { display: "flex", alignItems: "center", gap: "10px", marginLeft: "25px" });
        const pinLabel = document.createElement("span"); pinLabel.innerText = "📌 固定风格词预设:"; pinLabel.style.color = "#ffaa66"; pinLabel.style.fontSize = "13px"; pinLabel.style.fontWeight = "bold";
        const styleSelectDropdown = document.createElement("select");
        Object.assign(styleSelectDropdown.style, { background: "#140f0c", color: "#ffaa66", border: "1px solid #8e624c", borderRadius: "4px", padding: "4px 8px", fontSize: "13px", fontWeight: "bold", cursor: "pointer" });
        
        leftHeaderGroup.appendChild(pinLabel); leftHeaderGroup.appendChild(styleSelectDropdown); header.appendChild(leftHeaderGroup);

        const close = makeModalButton("×", "#444"); Object.assign(close.style, { width: "36px", padding: "0", fontSize: "24px" });
        close.onclick = () => overlay.remove(); header.appendChild(close); box.appendChild(header);

        const textarea = document.createElement("textarea"); textarea.classList.add("pg-main-prompt-textarea"); textarea.value = getGridPrompt(node, selected);
        Object.assign(textarea.style, { flex: "1", padding: "14px", resize: "none", outline: "none", border: "1px solid #5c5c5c", borderRadius: "10px", background: "#101010", color: "#ffffff", fontSize: "16px", lineHeight: "1.55", margin: "14px" });

        let presets = [];
        try { presets = JSON.parse(window.localStorage.getItem("pg_prompt_presets") || "[]"); } catch(e) {}
        
        let activeIndex = -1; const currentActiveVal = (window.__pg_fixed_prompt_data?.text || "").trim();
        if (currentActiveVal) {
            const found = presets.findIndex(p => p.text.trim() === currentActiveVal); if (found !== -1) activeIndex = found;
        }

        const renderPresets = () => {
            styleSelectDropdown.innerHTML = "";
            const optNone = document.createElement("option"); optNone.value = "-1"; optNone.innerText = "未使用固定风格";
            if (activeIndex === -1) optNone.selected = true; styleSelectDropdown.appendChild(optNone);

            presets.forEach((p, idx) => {
                const opt = document.createElement("option"); opt.value = idx; opt.innerText = p.name;
                if (idx === activeIndex) opt.selected = true; styleSelectDropdown.appendChild(opt);
            });
        };
        renderPresets();

        styleSelectDropdown.onchange = async () => {
            activeIndex = parseInt(styleSelectDropdown.value);
            let selectedText = ""; let selectedConflicts = [];
            if (activeIndex !== -1 && presets[activeIndex]) {
                selectedText = presets[activeIndex].text || "";
                selectedConflicts = presets[activeIndex].remove ? presets[activeIndex].remove.split(/[，,。\.；;]/).map(s => s.trim()).filter(Boolean) : [];
            }
            window.__pg_fixed_prompt_data.text = selectedText; window.__pg_fixed_prompt_data.remove = selectedConflicts;
            try { await fetch("/prompt_grid/save_fixed_prompt", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text: selectedText, remove: selectedConflicts }) }); } catch(e) {}
            textarea.dispatchEvent(new Event("input", { bubbles: true }));
        };

        const footer = document.createElement("div"); Object.assign(footer.style, { height: "58px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c", position: "relative" });

        const copy = makeModalButton("复制", "#3d6fb6");
        // 🌟 修复复写逻辑：双击复制将以绿色字前置的最终形式输出
        copy.onclick = () => {
            const rawVal = textarea.value; 
            const compiledVal = getExpandedText(rawVal);
            copyText(compiledVal);
        };

        const paste = makeModalButton("粘贴", "#17864a"); paste.onclick = async () => { textarea.value = await pasteText(); textarea.focus(); };
        const clear = makeModalButton("清空", "#a33b3b"); clear.onclick = () => { textarea.value = ""; textarea.focus(); };

        const leftGroup = document.createElement("div"); Object.assign(leftGroup.style, { position: "absolute", left: "0", top: "0", width: "40%", height: "100%", display: "flex", alignItems: "center", paddingLeft: "14px", boxSizing: "border-box", gap: "8px" });
        leftGroup.appendChild(copy); leftGroup.appendChild(paste); leftGroup.appendChild(clear);

        const spacer = document.createElement("div"); Object.assign(spacer.style, { flex: "1", display: "flex", justifyContent: "center", alignItems: "center", gap: "6px" });
        const macroSettingBtn = makeModalButton("指令", "#e67e22"); macroSettingBtn.onclick = () => openMacroConfigEditor();
        spacer.appendChild(macroSettingBtn); leftGroup.appendChild(spacer);

        const centerGroup = document.createElement("div"); Object.assign(centerGroup.style, { position: "absolute", left: "50%", top: "0", transform: "translateX(-50%)", height: "100%", display: "flex", alignItems: "center", justifyContent: "center" });
        const run = makeModalButton("运行", "#00a86b"); run.style.minWidth = "86px";
        run.onclick = () => { setGridPrompt(node, selected, textarea.value); setGridSelected(node, selected); runWorkflow(node); };
        centerGroup.appendChild(run);

        const rightGroup = document.createElement("div"); Object.assign(rightGroup.style, { position: "absolute", right: "0", top: "0", width: "40%", height: "100%", display: "flex", alignItems: "center", justifyContent: "flex-end", paddingRight: "14px", boxSizing: "border-box", pointerEvents: "none" });
        const rightInner = document.createElement("div"); Object.assign(rightInner.style, { display: "flex", gap: "9px", pointerEvents: "auto" });

        const keywordBtn = makeModalButton("关键词", "#2980b9"); keywordBtn.style.minWidth = "86px";
        keywordBtn.onclick = () => openKeywordPicker(textarea);

        const cancel = makeModalButton("取消", "#606060"); cancel.onclick = () => overlay.remove();
        const save = makeModalButton("保存", "#a77522");
        save.onclick = () => { setGridPrompt(node, selected, textarea.value); setGridSelected(node, selected); overlay.remove(); };

        rightInner.appendChild(keywordBtn); rightInner.appendChild(cancel); rightInner.appendChild(save); rightGroup.appendChild(rightInner);
        footer.appendChild(leftGroup); footer.appendChild(centerGroup); footer.appendChild(rightGroup);

        box.appendChild(textarea); box.appendChild(footer); overlay.appendChild(box); document.body.appendChild(overlay);
        
        if (typeof window.__pg_trigger_scan === "function") { window.__pg_trigger_scan(); }
        setTimeout(() => textarea.focus(), 50);
    } finally {
        window.__pg_editor_opening = false;
    }
}

// ----------------------------------------------------
// 📝 提示词接收/扩写与工具逻辑
// ----------------------------------------------------

function getToolText(node) {
    const w = getWidget(node, "text"); 
    return w && w.value !== undefined && w.value !== null ? toText(w.value) : toText(node.properties?.pttp_text);
}

function setToolText(node, text) {
    const value = toText(text), w = getWidget(node, "text"); if (w) w.value = value;
    node.properties = node.properties || {}; node.properties.pttp_text = value; markChanged(node);
}

function tuneTextToolWidget(node) {
    const w = node.widgets?.find((w) => w.name === "text"); if (!w) return;
    w.options = w.options || {}; w.options.minHeight = 130;
    w.computeSize = function (width) { const nodeH = node.size?.[1] || 250; return [Math.max(300, width - 20), Math.max(130, nodeH - 70)]; };
}

function openTextToolEditor(node) {
    const old = document.getElementById("pttp-editor"); if (old) old.remove();
    window.__pg_editing_context = { node: node, type: "text" };

    const overlay = document.createElement("div"); overlay.id = "pttp-editor";
    Object.assign(overlay.style, { position: "fixed", left: "0", top: "0", right: "0", bottom: "0", background: "rgba(0,0,0,0.48)", zIndex: "999999", display: "flex", alignItems: "center", justifyContent: "center" });

    const box = document.createElement("div");
    Object.assign(box.style, { width: "min(900px, 82vw)", height: "min(560px, 72vh)", background: "#202020", border: "1px solid #5a5a5a", borderRadius: "12px", display: "flex", flexDirection: "column", overflow: "hidden", boxShadow: "0 10px 45px rgba(0,0,0,0.65)" });

    const header = document.createElement("div"); Object.assign(header.style, { height: "46px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c", color: "#fff", fontWeight: "bold" });
    const title = document.createElement("div"); title.innerText = "编辑提示词扩写";
    const close = makeModalButton("×", "#444"); close.style.width = "36px"; close.style.padding = "0"; close.style.fontSize = "24px";
    close.onclick = () => overlay.remove(); header.appendChild(title); header.appendChild(close);

    const textarea = document.createElement("textarea"); textarea.value = getToolText(node);
    Object.assign(textarea.style, { flex: "1", margin: "14px", padding: "14px", resize: "none", outline: "none", border: "1px solid #5c5c5c", borderRadius: "10px", background: "#101010", color: "#ffffff", fontSize: "15px", lineHeight: "1.55" });

    const footer = document.createElement("div"); Object.assign(footer.style, { height: "56px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c" });
    const left = document.createElement("div"); left.style.display = "flex"; left.style.gap = "9px";
    const center = document.createElement("div"); center.style.display = "flex"; center.style.gap = "9px";
    const right = document.createElement("div"); right.style.display = "flex"; right.style.gap = "9px";

    const copy = makeModalButton("复制", "#3d6fb6"); copy.onclick = () => copyText(textarea.value);
    const paste = makeModalButton("粘贴", "#17864a"); paste.onclick = async () => { textarea.value = await pasteText(); textarea.focus(); };
    const clear = makeModalButton("清空", "#a33b3b"); clear.onclick = () => { textarea.value = ""; textarea.focus(); };

    const run = makeModalButton("运行", "#8a55cc"); run.onclick = () => { setToolText(node, textarea.value); runWorkflow(node); };
    const cancel = makeModalButton("取消", "#606060"); cancel.onclick = () => overlay.remove();
    const save = makeModalButton("保存", "#a77522"); save.onclick = () => { setToolText(node, textarea.value); overlay.remove(); };

    left.appendChild(copy); left.appendChild(paste); left.appendChild(clear); center.appendChild(run); right.appendChild(cancel); right.appendChild(save);
    footer.appendChild(left); footer.appendChild(center); footer.appendChild(right); box.appendChild(header); box.appendChild(textarea); box.appendChild(footer); overlay.appendChild(box);
    document.body.appendChild(overlay);
    
    if (typeof window.__pg_trigger_scan === "function") { window.__pg_trigger_scan(); }
    setTimeout(() => textarea.focus(), 50);
}

function drawTextToolButtons(ctx, node) {
    const w = node.size[0], h = node.size[1]; node.__text_buttons = [];
    const margin = 18, gap = 12, count = 4, bh = 26, y = h - 38;
    const bw = Math.max(70, (w - margin * 2 - gap * (count - 1)) / count);

    const defs = [ ["copy", "复制", "#3d6fb6", "#9fc7ff"], ["paste", "粘贴", "#17864a", "#8dffbd"], ["clear", "清空", "#a33b3b", "#ff9b9b"], ["run", "运行", "#8a55cc", "#d6b3ff"] ];
    let x = margin;
    for (const [name, label, color, flashColor] of defs) {
        const b = { name, label, x, y, w: bw, h: bh, color, flashColor, flash: node.__flash_button === `text_${name}`, font: 12 };
        node.__text_buttons.push(b); button(ctx, b); x += bw + gap;
    }
}

function drawTextTool(ctx, node) {
    const w = node.size[0], h = node.size[1]; node.__text_buttons = []; node.__text_box = null;
    ctx.save(); ctx.beginPath(); ctx.rect(0, 0, w, h); ctx.clip(); ctx.fillStyle = "#11101a"; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = "#d8caff"; ctx.font = "bold 13px Arial"; ctx.textAlign = "left"; ctx.textBaseline = "middle";
    ctx.fillText("提示词扩写文本工具", 10, 22);

    const box = { x: 10, y: 42, w: w - 20, h: h - 88 }; node.__text_box = box;
    roundRect(ctx, box.x, box.y, box.w, box.h, 8); ctx.fillStyle = "#141414"; ctx.fill(); ctx.strokeStyle = "#5b4d87"; ctx.lineWidth = 1; ctx.stroke();
    drawTextBoxPreview(ctx, getToolText(node), box); drawTextToolButtons(ctx, node); ctx.restore();
}

function drawTextBoxPreview(ctx, text, rect) {
    const raw = toText(text).replace(/\r/g, "");
    const padding = 10, lineHeight = 17, maxLines = Math.max(1, Math.floor((rect.h - padding * 2) / lineHeight));

    ctx.save(); ctx.beginPath(); ctx.rect(rect.x + 4, rect.y + 4, rect.w - 8, rect.h - 8); ctx.clip();
    ctx.fillStyle = "#eeeeee"; ctx.font = "12px Arial"; ctx.textAlign = "left"; ctx.textBaseline = "top";

    const maxWidth = rect.w - padding * 2, lines = [], paragraphs = raw.split("\n");
    for (const para of paragraphs) {
        let current = "";
        for (const ch of para) {
            const test = current + ch; if (ctx.measureText(test).width > maxWidth && current) { lines.push(current); current = ch; if (lines.length >= maxLines) break; } else { current = test; }
        }
        if (lines.length >= maxLines) break; if (current) lines.push(current); if (lines.length >= maxLines) break;
    }

    for (let i = 0; i < Math.min(lines.length, maxLines); i++) {
        let line = lines[i]; if (i === maxLines - 1 && lines.length >= maxLines) { while (ctx.measureText(line + "...").width > maxWidth && line.length > 0) line = line.slice(0, -1); line += "..."; }
        ctx.fillText(line, rect.x + padding, rect.y + padding + i * lineHeight);
    }
    ctx.restore();
}

function getReceiverText(node) { return toText(node.properties?.ptr_text); }
function setReceiverText(node, text) { node.properties = node.properties || {}; node.properties.ptr_text = toText(text); markChanged(node); }

function openReceiverEditor(node) {
    const old = document.getElementById("ptr-editor"); if (old) old.remove();
    window.__pg_editing_context = { node: node, type: "receiver" };

    const overlay = document.createElement("div"); overlay.id = "ptr-editor";
    Object.assign(overlay.style, { position: "fixed", left: "0", top: "0", right: "0", bottom: "0", background: "rgba(0,0,0,0.48)", zIndex: "999999", display: "flex", alignItems: "center", justifyContent: "center" });

    const box = document.createElement("div");
    Object.assign(box.style, { width: "min(900px, 82vw)", height: "min(560px, 72vh)", background: "#202020", border: "1px solid #5a5a5a", borderRadius: "12px", display: "flex", flexDirection: "column", overflow: "hidden", boxShadow: "0 10px 45px rgba(0,0,0,0.65)" });

    const header = document.createElement("div"); Object.assign(header.style, { height: "46px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c", color: "#fff", fontWeight: "bold" });
    const title = document.createElement("div"); title.innerText = "编辑接收文本";
    const close = makeModalButton("×", "#444"); close.style.width = "36px"; close.style.padding = "0"; close.style.fontSize = "24px";
    close.onclick = () => overlay.remove(); header.appendChild(title); header.appendChild(close);

    const textarea = document.createElement("textarea"); textarea.value = getReceiverText(node);
    Object.assign(textarea.style, { flex: "1", margin: "14px", padding: "14px", resize: "none", outline: "none", border: "1px solid #5c5c5c", borderRadius: "10px", background: "#101010", color: "#ffffff", fontSize: "15px", lineHeight: "1.55" });

    const footer = document.createElement("div"); Object.assign(footer.style, { height: "56px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 14px", background: "#2c2c2c" });
    const left = document.createElement("div"); left.style.display = "flex"; left.style.gap = "9px";
    const right = document.createElement("div"); right.style.display = "flex"; right.style.gap = "9px";

    const copy = makeModalButton("复制", "#3d6fb6"); copy.onclick = () => copyText(textarea.value);
    const clear = makeModalButton("清空", "#a33b3b"); clear.onclick = () => { textarea.value = ""; textarea.focus(); };
    const cancel = makeModalButton("取消", "#606060"); cancel.onclick = () => overlay.remove();
    const save = makeModalButton("保存", "#a77522"); save.onclick = () => { setReceiverText(node, textarea.value); overlay.remove(); };

    left.appendChild(copy); left.appendChild(clear); right.appendChild(cancel); right.appendChild(save);
    footer.appendChild(left); footer.appendChild(right); box.appendChild(header); box.appendChild(textarea); box.appendChild(footer); overlay.appendChild(box);
    document.body.appendChild(overlay);

    if (typeof window.__pg_trigger_scan === "function") { window.__pg_trigger_scan(); }
    setTimeout(() => textarea.focus(), 50);
}

function drawReceiver(ctx, node) {
    const w = node.size[0], h = node.size[1]; node.__receiver_buttons = [];
    ctx.save(); ctx.beginPath(); ctx.rect(0, 0, w, h); ctx.clip(); ctx.fillStyle = "#101820"; ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = "#b8e7ff"; ctx.font = "bold 13px Arial"; ctx.textAlign = "left"; ctx.textBaseline = "middle";
    ctx.fillText("接收文本工具", 10, 22);

    const box = { x: 10, y: 42, w: w - 20, h: h - 88 };
    roundRect(ctx, box.x, box.y, box.w, box.h, 8); ctx.fillStyle = "#141414"; ctx.fill(); ctx.strokeStyle = "#447087"; ctx.lineWidth = 1; ctx.stroke();
    drawTextBoxPreview(ctx, getReceiverText(node), box);

    const bw = 64, bh = 26, y = h - 38;
    const buttons = [
        { name: "edit", label: "编辑", x: 18, y, w: bw, h: bh, color: "#a77522", flashColor: "#ffd58b", flash: node.__flash_button === "receiver_edit", font: 12 },
        { name: "copy", label: "复制", x: w / 2 - bw / 2, y, w: bw, h: bh, color: "#3d6fb6", flashColor: "#9fc7ff", flash: node.__flash_button === "receiver_copy", font: 12 },
        { name: "clear", label: "清空", x: w - 18 - bw, y, w: bw, h: bh, color: "#a33b3b", flashColor: "#ff9b9b", flash: node.__flash_button === "receiver_clear", font: 12 }
    ];
    for (const b of buttons) { node.__receiver_buttons.push(b); button(ctx, b); } ctx.restore();
}

function findButton(list, pos) { return list?.find(b => inRect(pos, b)) || null; }

function openKeywordPicker(targetTextarea) {
    const old = document.getElementById("pg16-keyword-picker"); if (old) old.remove();
    const overlay = document.createElement("div"); overlay.id = "pg16-keyword-picker";
    Object.assign(overlay.style, { position: "fixed", left: "0", top: "0", right: "0", bottom: "0", background: "rgba(0,0,0,0.4)", zIndex: "999999", display: "flex", alignItems: "center", justifyContent: "center" });

    const box = document.createElement("div");
    Object.assign(box.style, { width: "400px", background: "#222226", border: "1px solid #3498db", borderRadius: "10px", display: "flex", flexDirection: "column", overflow: "hidden", boxShadow: "0 8px 30px rgba(0,0,0,0.5)" });

    const header = document.createElement("div");
    Object.assign(header.style, { height: "40px", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "0 12px", background: "#2c2c22", color: "#3498db", fontSize: "14px", fontWeight: "bold" });
    header.innerText = "🏷️ 点击加载关键词进提示词";

    const close = makeModalButton("×", "#444"); Object.assign(close.style, { width: "28px", height: "28px", padding: "0", fontSize: "18px" });
    close.onclick = () => overlay.remove(); header.appendChild(close);

    const container = document.createElement("div"); Object.assign(container.style, { padding: "14px", maxHeight: "240px", overflowY: "auto", display: "flex", flexWrap: "wrap", gap: "8px" });
    box.appendChild(header); box.appendChild(container); overlay.appendChild(box); document.body.appendChild(overlay);

    const rules = window.__pg_macro_rules || {}; const keys = Object.keys(rules);
    if (keys.length === 0) {
        container.innerHTML = "<span style='color:#777; width:100%; text-align:center;'>尚未配置任何关键词规则</span>"; return;
    }
    keys.forEach(k => {
        const btn = document.createElement("button"); btn.innerText = k;
        Object.assign(btn.style, { background: "#2a3b4c", color: "#fff", border: "1px solid #1a2b3c", borderRadius: "4px", padding: "6px 12px", cursor: "pointer", fontSize: "12px", fontWeight: "bold" });
        btn.onclick = () => {
            const text = targetTextarea.value, start = targetTextarea.selectionStart, end = targetTextarea.selectionEnd;
            const isChinese = /[\u4e00-\u9fff]/.test(text), comma = isChinese ? "，" : ", ";
            let added = k;
            if (start > 0 && text[start - 1] !== ' ' && text[start - 1] !== ',' && text[start - 1] !== '，') added = comma + added;
            if (end < text.length && text[end] !== ' ' && text[end] !== ',' && text[end] !== '，') added = added + comma;
            targetTextarea.value = text.substring(0, start) + added + text.substring(end);
            targetTextarea.focus(); targetTextarea.dispatchEvent(new Event('input')); overlay.remove();
        };
        container.appendChild(btn);
    });
}

// ----------------------------------------------------
// 🔌 ComfyUI 节点注册生命周期注册区
// ----------------------------------------------------

app.registerExtension({
    name: "PromptToolsPro.Stable",

    async beforeRegisterNodeDef(nodeType, nodeData) {
        const nodeName = String(nodeData.name || ""), nodeDisplay = String(nodeData.display_name || nodeData.displayName || nodeData.title || "");
        const isGrid = GRID_NODE_NAMES.has(nodeName) || nodeDisplay.includes("16宫格提示词");
        const isText = TEXT_NODE_NAMES.has(nodeName) || nodeDisplay.includes("提示词扩写文本工具");
        const isReceiver = RECEIVER_NODE_NAMES.has(nodeName) || nodeDisplay.includes("接收文本工具");

        if (!isGrid && !isText && !isReceiver) return;

        const oldCreated = nodeType.prototype.onNodeCreated;
        const oldConfigure = nodeType.prototype.onConfigure;
        const oldSerialize = nodeType.prototype.onSerialize;
        const oldResize = nodeType.prototype.onResize;
        const oldMenu = nodeType.prototype.getExtraMenuOptions;

        if (isGrid) {
            if (nodeData.input && nodeData.input.required) delete nodeData.input.required.model;
            if (nodeData.input && nodeData.input.optional) delete nodeData.input.optional.model;
            if (Array.isArray(nodeData.output)) {
                let idx = -1; while ((idx = nodeData.output.indexOf("MODEL")) !== -1) {
                    nodeData.output.splice(idx, 1); if (Array.isArray(nodeData.output_name)) nodeData.output_name.splice(idx, 1);
                }
            }
        }

        if (isGrid) {
            nodeType.prototype.onNodeCreated = function () {
                const r = oldCreated?.apply(this, arguments);
                this.size = [360, 360]; this.resizable = true; this.color = "#0c2e1b"; this.bgcolor = "#07130d";
                
                hideAllWidgets(this);

                this.properties = this.properties || {};
                this.properties.pg16_prompts = this.properties.pg16_prompts || new Array(CELL_COUNT).fill("");
                this.properties.pg16_notes = this.properties.pg16_notes || new Array(CELL_COUNT).fill("");
                this.properties.pg16_selected = this.properties.pg16_selected || 1;
                this.properties.disabled_loras = {};
                this.properties.last_enforced_prompts = {};

                restoreGrid(this);
                this.onDrawBackground = (ctx) => { if (!this.flags?.collapsed) drawGrid(ctx, this); };
                markChanged(this); return r;
            };

            nodeType.prototype.onConfigure = function () {
                const r = oldConfigure?.apply(this, arguments);
                
                hideAllWidgets(this);
                
                this.properties = this.properties || {};
                this.properties.pg16_prompts = this.properties.pg16_prompts || new Array(CELL_COUNT).fill("");
                this.properties.pg16_notes = this.properties.pg16_notes || new Array(CELL_COUNT).fill("");
                this.properties.disabled_loras = {};

                restoreGrid(this);
                const s = Math.max(MIN_GRID_SIZE, this.size?.[0] || 360, this.size?.[1] || 360);
                this.size[0] = s; this.size[1] = s;
                markChanged(this); return r;
            };

            nodeType.prototype.onSerialize = function (o) {
                const r = oldSerialize?.apply(this, arguments); if (!o) return r;
                try {
                    this.properties = this.properties || {}; o.properties = o.properties || {};
                    this.properties.pg16_prompts = this.properties.pg16_prompts || new Array(CELL_COUNT).fill("");

                    for (let i = 1; i <= CELL_COUNT; i++) { this.properties.pg16_prompts[i - 1] = getGridPrompt(this, i); }
                    this.properties.pg16_selected = getGridSelected(this); ensureGridNotes(this);

                    o.properties.pg16_prompts = [...this.properties.pg16_prompts];
                    o.properties.pg16_notes = [...(this.properties.pg16_notes || new Array(CELL_COUNT).fill(""))];
                    o.properties.pg16_selected = this.properties.pg16_selected;
                    o.properties.disabled_loras = {}; o.properties.last_enforced_prompts = {};

                    if (Array.isArray(o.widgets_values)) {
                        for (let i = 1; i <= CELL_COUNT; i++) {
                            const wName = `prompt_${i}`; const wIdx = this.widgets?.findIndex(w => w && w.name === wName);
                            if (wIdx !== undefined && wIdx !== -1) {
                                const rawVal = this.properties.pg16_prompts[i - 1] || "";
                                try { o.widgets_values[wIdx] = getExpandedText(rawVal); } catch(err) { o.widgets_values[wIdx] = rawVal; }
                            }
                        }
                    }
                } catch(e) { console.error("[PromptGrid] grid serialize error:", e); }
                return r;
            };

            nodeType.prototype.computeSize = function () { return [MIN_GRID_SIZE, MIN_GRID_SIZE]; };
            nodeType.prototype.onResize = function (size) { if (!size) return; const s = Math.max(MIN_GRID_SIZE, size[0], size[1]); size[0] = s; size[1] = s; if (oldResize) oldResize.call(this, size); };
            nodeType.prototype.onMouseMove = function (first, second, third) { const pos = localPos(this, first, second, third); if (!pos) return false; const cell = findGridCell(this, pos), hover = cell ? cell.index : null; if (this.__pg16_hover !== hover) { this.__pg16_hover = hover; this.setDirtyCanvas?.(true, false); } return false; };
            nodeType.prototype.onMouseLeave = function () { this.__pg16_hover = null; this.setDirtyCanvas?.(true, false); return false; };

            nodeType.prototype.onMouseDown = function (first, second, third) {
                const pos = localPos(this, first, second, third); if (!pos) return false; const b = findGridButton(this, pos);
                if (b) {
                    flashNodeButton(this, b.index ? `grid_${b.name}_${b.index}` : `grid_${b.name}`);
                    if (b.name === "run") { setGridSelected(this, getGridSelected(this)); runWorkflow(this); }
                    else if (b.name === "clear_all") clearGrid(this);
                    else if (b.name === "import") importGrid(this);
                    else if (b.name === "export") exportGrid(this);
                    else if (b.name === "copy") { setGridSelected(this, b.index); copyText(getGridPrompt(this, b.index)); }
                    else if (b.name === "edit") { setGridSelected(this, b.index); openGridEditor(this, b.index); }
                    else if (b.name === "paste") { setGridSelected(this, b.index); pasteText().then(t => setGridPrompt(this, b.index, t)); }
                    else if (b.name === "clear") { setGridSelected(this, b.index); setGridPrompt(this, b.index, ""); } return true;
                }
                const cell = findGridCell(this, pos); if (cell) { setGridSelected(this, cell.index); return true; } return false;
            };

            nodeType.prototype.onDblClick = function (first, second, third) {
                const pos = localPos(this, first, second, third); const noteCell = pos ? findGridNoteCell(this, pos) : null;
                if (noteCell) { openGridNoteEditor(this, noteCell.index); return true; }
                const cell = pos ? findGridCell(this, pos) : null;
                if (cell) { setGridSelected(this, cell.index); openGridEditor(this, cell.index); return true; }
                openGridEditor(this, getGridSelected(this)); return true;
            };

            nodeType.prototype.getExtraMenuOptions = function (_, options) {
                oldMenu?.apply(this, arguments); options.push(null);
                options.push({ content: "运行当前格", callback: () => runWorkflow(this) });
                options.push({ content: "编辑当前格", callback: () => openGridEditor(this, getGridSelected(this)) });
                options.push({ content: "清空全部", callback: () => clearGrid(this) });
                options.push({ content: "重置节点尺寸(360x360)", callback: () => { this.size = [360, 360]; if (this.setSize) this.setSize([360, 360]); markChanged(this); } });
            };
        }

        if (isText) {
            nodeType.prototype.onNodeCreated = function () {
                const r = oldCreated?.apply(this, arguments); this.size = [460, 250]; this.resizable = true; this.color = "#2c2050"; this.bgcolor = "#11101a";
                this.properties = this.properties || {}; this.properties.pttp_text = this.properties.pttp_text || "";
                const textWidget = this.widgets?.find((w) => w && w.name === "text"); if (textWidget && this.properties.pttp_text) textWidget.value = this.properties.pttp_text;

                tuneTextToolWidget(this);
                this.onDrawBackground = (ctx) => { if (!this.flags?.collapsed) drawTextTool(ctx, this); };
                markChanged(this); return r;
            };

            nodeType.prototype.onConfigure = function () {
                const r = oldConfigure?.apply(this, arguments);
                this.properties = this.properties || {}; this.properties.pttp_text = this.properties.pttp_text || "";
                const w = this.widgets?.find((w) => w && w.name === "text"); if (w && this.properties.pttp_text !== undefined) w.value = this.properties.pttp_text;
                this.size[0] = Math.max(430, this.size[0]); this.size[1] = Math.max(230, this.size[1]);
                tuneTextToolWidget(this); markChanged(this); return r;
            };

            nodeType.prototype.onSerialize = function (o) {
                const r = oldSerialize?.apply(this, arguments); if (!o) return r;
                try {
                    this.properties = this.properties || {}; const w = this.widgets?.find((w) => w && w.name === "text");
                    this.properties.pttp_text = toText(w?.value ?? this.properties.pttp_text ?? "");
                    o.properties = o.properties || {}; o.properties.pttp_text = this.properties.pttp_text;

                    if (Array.isArray(o.widgets_values)) {
                        const wIdx = this.widgets?.findIndex(w => w && w.name === "text");
                        if (wIdx !== undefined && wIdx !== -1) {
                            try { o.widgets_values[wIdx] = getExpandedText(this.properties.pttp_text || ""); } catch(err) { o.widgets_values[wIdx] = this.properties.pttp_text || ""; }
                        }
                    }
                } catch(e) {} return r;
            };

            nodeType.prototype.computeSize = function () { return [430, 230]; };
            nodeType.prototype.onResize = function () { oldResize?.apply(this, arguments); this.size[0] = Math.max(430, this.size[0]); this.size[1] = Math.max(230, this.size[1]); tuneTextToolWidget(this); };

            nodeType.prototype.onMouseDown = function (first, second, third) {
                const pos = localPos(this, first, second, third); if (!pos) return false; const b = findButton(this.__text_buttons, pos); if (!b) return false;
                const textVal = toText(this.widgets?.find((w) => w.name === "text")?.value ?? this.properties?.pttp_text ?? "");
                const setTextVal = (value) => {
                    const text = toText(value), w = this.widgets?.find((w) => w.name === "text"); if (w) w.value = text; this.properties = this.properties || {}; this.properties.pttp_text = text; markChanged(this);
                };
                if (b.name === "copy") { flashNodeButton(this, "text_copy"); copyText(textVal); }
                else if (b.name === "paste") { flashNodeButton(this, "text_paste"); pasteText().then(t => setTextVal(t)); }
                else if (b.name === "clear") { flashNodeButton(this, "text_clear"); setTextVal(""); }
                else if (b.name === "run") { flashNodeButton(this, "text_run"); runWorkflow(this); } return true;
            };
            nodeType.prototype.onDblClick = function () { openTextToolEditor(this); return true; };

            nodeType.prototype.getExtraMenuOptions = function (_, options) {
                oldMenu?.apply(this, arguments);
                const textVal = () => toText(this.widgets?.find((w) => w.name === "text")?.value ?? this.properties?.pttp_text ?? "");
                const setTextVal = (value) => {
                    const text = toText(value), w = this.widgets?.find((w) => w.name === "text"); if (w) w.value = text; this.properties = this.properties || {}; this.properties.pttp_text = text; markChanged(this);
                };
                options.push(null); options.push({ content: "运行扩写", callback: () => runWorkflow(this) }); options.push({ content: "复制文本", callback: () => copyText(textVal()) }); options.push({ content: "粘贴文本", callback: async () => setTextVal(await pasteText()) }); options.push({ content: "清空文本", callback: () => setTextVal("") });
            };
        }

        if (isReceiver) {
            nodeType.prototype.onNodeCreated = function () {
                const r = oldCreated?.apply(this, arguments); this.size = [460, 250]; this.resizable = true; this.color = "#173040"; this.bgcolor = "#101820";
                hideAllWidgets(this); this.properties = this.properties || {}; this.properties.ptr_text = this.properties.ptr_text || "";
                this.onDrawBackground = (ctx) => { if (!this.flags?.collapsed) drawReceiver(ctx, this); };
                markChanged(this); return r;
            };

            nodeType.prototype.onConfigure = function () {
                const r = oldConfigure?.apply(this, arguments); hideAllWidgets(this); this.properties = this.properties || {}; this.properties.ptr_text = this.properties.ptr_text || "";
                this.size[0] = Math.max(430, this.size[0]); this.size[1] = Math.max(230, this.size[1]); markChanged(this); return r;
            };

            nodeType.prototype.onSerialize = function (o) {
                const r = oldSerialize?.apply(this, arguments); if (!o) return r;
                try {
                    this.properties = this.properties || {}; o.properties = o.properties || {};
                    this.properties.ptr_text = getReceiverText(this); o.properties.ptr_text = this.properties.ptr_text;
                    const all = [...(this.__hidden_widgets || []), ...(this.widgets || [])];
                    o.widgets_values = all.filter(Boolean).map((w, idx) => { return w.serializeValue ? w.serializeValue(this, idx) : w.value; });
                } catch(e) { console.error("[PromptReceiver] receiver serialize error:", e); } return r;
            };

            nodeType.prototype.computeSize = function () { return [430, 230]; };
            nodeType.prototype.onResize = function () { oldResize?.apply(this, arguments); this.size[0] = Math.max(430, this.size[0]); this.size[1] = Math.max(230, this.size[1]); };

            nodeType.prototype.onMouseDown = function (first, second, third) {
                const pos = localPos(this, first, second, third); if (!pos) return false; const b = findButton(this.__receiver_buttons, pos); if (!b) return false;
                if (b.name === "copy") { flashNodeButton(this, "receiver_copy"); copyText(getReceiverText(this)); }
                else if (b.name === "edit") { flashNodeButton(this, "receiver_edit"); openReceiverEditor(this); }
                else if (b.name === "clear") { flashNodeButton(this, "receiver_clear"); setReceiverText(this, ""); } return true;
            };
            nodeType.prototype.onDblClick = function () { openReceiverEditor(this); return true; };

            nodeType.prototype.getExtraMenuOptions = function (_, options) {
                oldMenu?.apply(this, arguments); options.push(null); options.push({ content: "复制接收文本", callback: () => copyText(getReceiverText(this)) }); options.push({ content: "清空接收文本", callback: () => setReceiverText(this, "") });
            };
        }
    },
});

// 🛡️ API 提交双向拦截器
const originalGraphToPrompt = app.graphToPrompt;
app.graphToPrompt = async function() {
    const res = await originalGraphToPrompt.apply(this, arguments);
    if (res && res.output) {
        const gridNodeIds = new Set();
        for (const id in res.output) {
            const classType = res.output[id].class_type;
            if (classType && (classType === "prompt_grid_16_pro" || classType === "PromptGrid16Pro")) {
                gridNodeIds.add(id);
            }
        }
        for (const id in res.output) {
            const nodeInfo = res.output[id];
            if (nodeInfo && nodeInfo.inputs) {
                for (const inputKey in nodeInfo.inputs) {
                    const connection = nodeInfo.inputs[inputKey];
                    if (Array.isArray(connection) && connection.length === 2) {
                        const originNodeId = String(connection[0]);
                        const originSlot = Number(connection[1]);
                        if (gridNodeIds.has(originNodeId) && originSlot === 1) {
                            connection[1] = 0; 
                        }
                    }
                }
            }
        }
    }
    return res;
};