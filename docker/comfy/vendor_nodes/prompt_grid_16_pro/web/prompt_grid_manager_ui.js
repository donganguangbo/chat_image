import { app } from "../../../scripts/app.js";

// 调用 rgthree 的 C站 对话框
async function openCivitaiInfo(loraName) {
    try {
        const mod = await import("/extensions/rgthree-comfy/dialog_info.js");
        if (mod && mod.RgthreeLoraInfoDialog) {
            const dialog = new mod.RgthreeLoraInfoDialog(loraName);
            dialog.loraNameForCaching = loraName;
            dialog.show();
        } else {
            alert("未检测到本地有 rgthree-comfy。请确保已在 custom_nodes 中装好该插件。");
        }
    } catch (err) {
        console.error("跨插件动态拉取详情失败:", err);
        alert("无法打开 C 站浮窗，请确保 rgthree 插件正常运行且能显示其他 LoRA 信息。");
    }
}

// 绑定全局管理浮窗方法
window.showLoraManagerDialog = async function() {
    const resp = await fetch("/prompt_grid/get_loras");
    const data = await resp.json();
    const loras = data.loras;
    const subdirs = data.subdirs;
    const config = data.config || {};

    const overlay = document.createElement("div");
    // ✅ 强力改写：将 z-index 更新为 CSS 允许的最大范围值 2147483647，保证盖在一切弹窗的上方
    overlay.style = `
        position: fixed; top: 0; left: 0; width: 100vw; height: 100vh;
        background: rgba(0,0,0,0.75); z-index: 2147483647; display: flex;
        justify-content: center; align-items: center; font-family: sans-serif;
    `;

    const box = document.createElement("div");
    box.style = `
        width: 82%; height: 82%; background: #222; color: #fff;
        border-radius: 8px; padding: 20px; display: flex; flex-direction: column;
        border: 1px solid #444; box-shadow: 0 4px 20px rgba(0,0,0,0.6);
    `;

    let activeDir = localStorage.getItem("prompt_grid_last_subdir") || "";

    const header = document.createElement("div");
    header.style = "display:flex; justify-content:space-between; margin-bottom:15px; align-items:center; gap: 15px;";
    
    const titleSec = document.createElement("h3");
    titleSec.innerText = "LoRA 自动化匹配管理器";
    titleSec.style.margin = "0";

    const filterSec = document.createElement("div");
    filterSec.style = "display: flex; gap: 10px; align-items: center; flex: 1; justify-content: flex-end;";

    const dirSelect = document.createElement("select");
    dirSelect.style = "padding:6px; background:#333; border:1px solid #555; color:#fff; border-radius:4px; outline:none; max-width:260px;";
    
    const optAll = document.createElement("option");
    optAll.value = "";
    optAll.innerText = "📁 全部 LoRA 目录";
    dirSelect.appendChild(optAll);

    subdirs.forEach(s => {
        const opt = document.createElement("option");
        opt.value = s;
        opt.innerText = "📁 " + s;
        dirSelect.appendChild(opt);
    });

    if (subdirs.includes(activeDir)) {
        dirSelect.value = activeDir;
    }

    const searchInput = document.createElement("input");
    searchInput.type = "text";
    searchInput.placeholder = "搜索 LoRA 文件名...";
    searchInput.style = "padding:6px; background:#333; border:1px solid #555; color:#fff; border-radius:4px; outline:none; width: 220px;";

    filterSec.appendChild(dirSelect);
    filterSec.appendChild(searchInput);
    header.appendChild(titleSec);
    header.appendChild(filterSec);
    box.appendChild(header);

    const listContainer = document.createElement("div");
    listContainer.style = "flex: 1; overflow-y: auto; border: 1px solid #3d3d3d; padding: 12px; background: #1a1a1a; border-radius:4px;";
    box.appendChild(listContainer);

    const renderList = () => {
        listContainer.innerHTML = "";
        const currentDir = dirSelect.value;
        const searchVal = searchInput.value.toLowerCase().replace(/\\/g, "/");

        loras.forEach(path => {
            const normPath = path.replace(/\\/g, "/");
            
            if (currentDir) {
                const normDir = currentDir.replace(/\\/g, "/");
                if (!normPath.startsWith(normDir + "/")) return;
            }
            
            if (searchVal && !normPath.toLowerCase().includes(searchVal)) return;

            const itemConf = config[path] || { triggers: [], model_val: 1.0, clip_val: 1.0, enabled: false };
            const baseName = path.split(/[\\/]/).pop();

            const itemDiv = document.createElement("div");
            itemDiv.style = "display: flex; gap: 10px; align-items: center; background: #2d2d2d; padding: 8px 12px; margin-bottom: 6px; border-radius: 4px; border: 1px solid #3d3d3d;";
            itemDiv.className = "lora-item";
            itemDiv.dataset.path = path;

            const displayedTriggers = itemConf.triggers.join('，');

            itemDiv.innerHTML = `
                <input type="checkbox" class="lora-enable" ${itemConf.enabled ? 'checked' : ''} style="transform: scale(1.28); cursor:pointer; margin-right:8px;">
                <div style="width: 250px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size:13px; color:#ddd; display: flex; align-items: center;" title="${path}">
                    <span style="flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">${baseName}</span>
                    <button class="civitai-info-btn" title="查看 C站 资料" style="
                        background: #2a74c3;
                        color: white;
                        border: none;
                        border-radius: 50%;
                        width: 18px;
                        height: 18px;
                        font-size: 11px;
                        font-weight: bold;
                        display: inline-flex;
                        align-items: center;
                        justify-content: center;
                        cursor: pointer;
                        margin-left: 6px;
                        margin-right: 6px;
                        flex-shrink: 0;
                        font-family: inherit;
                    ">i</button>
                </div>
                <input type="text" class="lora-triggers" placeholder="触发词 (输入中英文逗号皆可分隔)" value="${displayedTriggers}" style="flex:1; padding:6px; background:#1e1e1e; border:1px solid #555; color:#fff; border-radius:3px; outline:none;">
                <span style="font-size:12px; color:#aaa; margin-left: 10px;">模型:</span>
                <input type="number" class="lora-model-val" step="0.05" value="${itemConf.model_val}" style="width:55px; padding:6px; background:#1e1e1e; border:1px solid #555; color:#fff; border-radius:3px; outline:none;">
                <span style="font-size:12px; color:#aaa; margin-left: 5px;">CLIP:</span>
                <input type="number" class="lora-clip-val" step="0.05" value="${itemConf.clip_val}" style="width:55px; padding:6px; background:#1e1e1e; border:1px solid #555; color:#fff; border-radius:3px; outline:none;">
            `;

            itemDiv.querySelector(".civitai-info-btn").onclick = (e) => {
                e.preventDefault();
                e.stopPropagation();
                openCivitaiInfo(path);
            };

            listContainer.appendChild(itemDiv);
        });
    };

    dirSelect.addEventListener("change", (e) => {
        localStorage.setItem("prompt_grid_last_subdir", e.target.value);
        renderList();
    });
    searchInput.addEventListener("input", renderList);

    renderList();

    const footer = document.createElement("div");
    footer.style = "display: flex; justify-content: flex-end; gap: 10px; margin-top: 15px;";

    const closeBtn = document.createElement("button");
    closeBtn.innerText = "取消";
    closeBtn.style = "padding:8px 18px; background:#555; color:#fff; border:none; border-radius:4px; cursor:pointer;";
    closeBtn.onclick = () => document.body.removeChild(overlay);

    const saveBtn = document.createElement("button");
    saveBtn.innerText = "保存匹配规则";
    saveBtn.style = "padding:8px 18px; background:#8a55cc; color:#fff; border:none; border-radius:4px; cursor:pointer; font-weight:bold;";
    saveBtn.onclick = async () => {
        const items = listContainer.querySelectorAll(".lora-item");
        const newConfig = { ...config };

        items.forEach(item => {
            const path = item.dataset.path;
            const enabled = item.querySelector(".lora-enable").checked;
            
            const rawVal = item.querySelector(".lora-triggers").value;
            const triggers = rawVal.split(/[,\uff0c]/).map(t => t.trim()).filter(Boolean);
            
            const modelVal = parseFloat(item.querySelector(".lora-model-val").value) || 1.0;
            const clipVal = parseFloat(item.querySelector(".lora-clip-val").value) || 1.0;

            if (enabled || triggers.length > 0) {
                newConfig[path] = {
                    enabled,
                    triggers,
                    model_val: modelVal,
                    clip_val: clipVal
                };
            } else {
                delete newConfig[path];
            }
        });

        const saveResp = await fetch("/prompt_grid/save_config", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ config: newConfig })
        });
        const saveRes = await saveResp.json();
        if (saveRes.status === "ok") {
            document.body.removeChild(overlay);
        } else {
            alert("保存至本地失败，请检查控制台。");
        }
    };

    footer.appendChild(closeBtn);
    footer.appendChild(saveBtn);
    box.appendChild(footer);
    overlay.appendChild(box);
    document.body.appendChild(overlay);
};