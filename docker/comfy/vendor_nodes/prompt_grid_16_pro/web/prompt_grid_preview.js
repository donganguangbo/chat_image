import { app } from "../../scripts/app.js";

const style = document.createElement("style");
style.innerHTML = `
    .pg-highlight-textarea { color: transparent !important; background: transparent !important; background-color: transparent !important; caret-color: #ffffff !important; }
    .pg-highlight-backdrop { border-color: transparent !important; background-color: transparent !important; color: #dcdcdc !important; user-select: text !important; -webkit-user-select: text !important; pointer-events: none !important; white-space: pre-wrap !important; word-wrap: break-word !important; }
    .pg-highlight-red-phrase { color: #ff5555 !important; background: none !important; border: none !important; padding: 0 !important; margin: 0 !important; text-decoration: line-through !important; display: inline !important; }
    .pg-highlight-trigger-badge { color: #70b5ff !important; background: none !important; border: none !important; padding: 0 !important; margin: 0 !important; display: inline !important; font-weight: bold; }
    .pg-highlight-green { color: #4ade80 !important; background: none !important; border: none !important; padding: 0 !important; margin: 0 !important; text-decoration: none !important; display: inline !important; font-weight: bold; }
    .pg-keyword-btn-active { background-color: #1e90ff !important; box-shadow: 0 0 10px rgba(30,144,255,0.8) !important; border: 1px solid #00bfff !important; color: #ffffff !important; transform: scale(1.05); font-weight: bold !important; }
    #pg16-editor button, #pttp-editor button, #ptr-editor button, #pg16-keyword-picker button { transition: all 0.1s ease-out !important; }
    #pg16-editor button:active, #pttp-editor button:active, #ptr-editor button:active, #pg16-keyword-picker button:active { filter: brightness(1.3) contrast(1.1) !important; transform: scale(0.95) !important; }
`;
document.head.appendChild(style);

let macroRules = {};
let fixedPromptData = { text: "", remove: [] };
let callingFromPreview = false;

window.__pg_macro_rules = window.__pg_macro_rules || {};
window.__pg_fixed_prompt_data = window.__pg_fixed_prompt_data || { text: "", remove: [] };

window.__pg_serializing = false;
window.__pg_temp_disable_expansion = false;
window.__pg_active_textarea = null;

async function fetchRules() {
    try {
        const res = await fetch("/prompt_grid/get_macros");
        const data = await res.json();
        macroRules = data.rules || {};
        window.__pg_macro_rules = macroRules;
    } catch (e) {}
    try {
        const res = await fetch("/prompt_grid/get_fixed_prompt");
        const data = await res.json();
        fixedPromptData = data || { text: "", remove: [] };
        window.__pg_fixed_prompt_data = fixedPromptData;
    } catch(e) {}
}

const originalFetch = window.fetch;
window.fetch = async function(...args) {
    const url = args[0];
    const response = await originalFetch.apply(this, args);
    if (typeof url === "string" && (url.includes("/prompt_grid/save_macros") || url.includes("/prompt_grid/save_fixed_prompt"))) {
        setTimeout(async () => {
            await fetchRules();
            const activeTa = findActiveEditingTextarea();
            if (activeTa) activeTa.dispatchEvent(new Event("input", { bubbles: true }));
        }, 100);
    }
    return response;
};

function escapeHTML(str) {
    if (!str) return "";
    return str.replace(/[&<>'"]/g, tag => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[tag] || tag));
}

function escapeRegExp(string) { 
    if (!string) return "";
    return string.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); 
}

function isTargetTextarea(ta) {
    if (!ta) return false;
    if (ta.classList.contains("pg-main-prompt-textarea")) return true;
    if (ta.offsetWidth === 0 || ta.offsetHeight === 0) return false;
    let parent = ta.parentElement;
    while (parent && parent !== document.body) {
        if (parent.textContent && (parent.textContent.includes("指令") || parent.textContent.includes("关键词") || parent.id === "pg16-editor" || parent.id === "pttp-editor" || parent.id === "ptr-editor")) return true;
        parent = parent.parentElement;
    }
    return false;
}

function isPromptNode(type) {
    if (!type) return false;
    const t = String(type).toLowerCase();
    return t.includes("prompt_grid") || t.includes("promptgrid") || t.includes("prompt_text") || t.includes("prompttext");
}

function findActiveEditingTextarea() {
    const mainTa = document.querySelector(".pg-main-prompt-textarea");
    if (mainTa && mainTa.offsetWidth > 0) return mainTa;

    const editorTextareas = Array.from(document.querySelectorAll("#pg16-editor textarea, #pttp-editor textarea, #ptr-editor textarea"));
    const visible = editorTextareas.filter(ta => ta.offsetWidth > 0 && ta.offsetHeight > 0);
    if (visible.length > 0) {
        visible.sort((a, b) => b.offsetHeight - a.offsetHeight);
        return visible[0];
    }
    return null;
}

// 🛡️ 核心重构：前端统一构造编译函数
function compileFinalPrompt(text) {
    if (!text || !text.trim()) return text;
    try {
        const activeRulesMap = window.__pg_macro_rules || macroRules || {};
        const activeFixedData = window.__pg_fixed_prompt_data || fixedPromptData || { text: "", remove: [] };

        // 1. 获取匹配到的所有触发词，保留它们在文中的位置（Index）以判断“后输入的放前面”
        let matchedWithIndices = [];
        for (let trigger in activeRulesMap) {
            if (!trigger) continue;
            let rule = activeRulesMap[trigger];
            if (!rule) continue;
            let pattern = new RegExp("(?<![a-zA-Z0-9_])" + escapeRegExp(trigger) + "(?![a-zA-Z0-9_])", "gi");
            let match;
            while ((match = pattern.exec(text)) !== null) {
                matchedWithIndices.push({
                    trigger: trigger,
                    rule: rule,
                    index: match.index
                });
            }
        }

        // 🌟 核心：按在输入框的先后顺序（Index值）进行倒序排序。由于用户打字是从左往右，Index 值越高的就是越后面输入的，越要排在最前方（LIFO 触发）
        matchedWithIndices.sort((a, b) => b.index - a.index);

        // 去重触发词，避免相同词触发多次
        let uniqueMatched = [];
        let seen = new Set();
        for (let m of matchedWithIndices) {
            let trgLower = m.trigger.toLowerCase();
            if (!seen.has(trgLower)) {
                seen.add(trgLower);
                uniqueMatched.push(m);
            }
        }

        // 2. 收集需要按顺序填装的最前绿色关键词列表
        let greenPhrases = [];
        uniqueMatched.forEach(m => {
            let rule = m.rule;
            if (rule && rule.insert && typeof rule.insert === 'string') {
                rule.insert.split(/[,，.。;；]/).map(p => p.trim()).filter(Boolean).forEach(p => {
                    if (!greenPhrases.includes(p)) greenPhrases.push(p);
                });
            }
        });

        // 3. 拦截淘汰列表收集
        let conflicts = [];
        uniqueMatched.forEach(m => {
            let rule = m.rule;
            if (rule && rule.remove) {
                const removeArr = Array.isArray(rule.remove) ? rule.remove : (typeof rule.remove === 'string' ? [rule.remove] : []);
                removeArr.forEach(rm => {
                    if (rm && typeof rm === 'string') {
                        rm.split(/[,，.。;；]/).forEach(p => { if (p && p.trim()) conflicts.push(p.trim().toLowerCase()); });
                    }
                });
            }
        });

        if (activeFixedData && Array.isArray(activeFixedData.remove)) {
            activeFixedData.remove.forEach(rm => { if (rm && typeof rm === 'string' && rm.trim()) conflicts.push(rm.trim().toLowerCase()); });
        }

        // 4. 清理并拆解原始主句文本
        let isChinese = /[\u4e00-\u9fff]/.test(text);
        let comma = isChinese ? "，" : ", ";
        let clauses = text.split(/([,，.。;；\n\r]+)/);
        let finalClauses = [];
        let activeTriggersLower = Array.from(seen);

        for (let i = 0; i < clauses.length; i++) {
            let clause = clauses[i];
            if (/^[,，.。;；\n\r]+$/.test(clause)) continue;
            let trimmed = clause.trim();
            if (!trimmed) continue;

            let lowerClause = trimmed.toLowerCase();
            if (conflicts.some(conf => lowerClause.includes(conf))) continue;
            if (activeTriggersLower.some(trg => lowerClause === trg)) continue;

            let cleanClause = trimmed;
            activeTriggersLower.forEach(trg => {
                let trgRx = new RegExp("(?:\\s*[,，.。;；]?\\s*)" + escapeRegExp(trg) + "(?:\\s*[,痕;；]?\\s*)", "gi");
                cleanClause = cleanClause.replace(trgRx, " ").trim();
            });
            if (cleanClause) finalClauses.push(cleanClause);
        }

        // 5. 编译全局固定词，排除冲突淘汰词
        let stylePrefix = (activeFixedData.text || "").trim();
        let resolvedStyle = "";
        if (stylePrefix) {
            let styleClauses = stylePrefix.split(/([,，.。;；\n\r]+)/);
            let valid = [];
            styleClauses.forEach(clause => {
                if (/^[,，.。;；\n\r]+$/.test(clause)) return;
                let trimmed = clause.trim();
                if (!trimmed) return;
                if (!conflicts.some(conf => trimmed.toLowerCase().includes(conf))) {
                    valid.push(trimmed);
                }
            });
            resolvedStyle = valid.join(comma);
        }

        // 6. 进行最终的字符串编组输出: [绿色词群句号] + [风格前缀] + [清洗后的主体词列表]
        // 🌟 逗号取消，修改为句号结尾并自动连接
        let greenString = greenPhrases.length > 0 ? (greenPhrases.join(comma) + ". ") : "";
        let mainTextString = finalClauses.join(comma);

        let finalPrompt = "";
        if (greenString) finalPrompt += greenString;
        if (resolvedStyle) {
            finalPrompt += resolvedStyle;
            if (mainTextString) finalPrompt += comma + mainTextString;
        } else {
            if (mainTextString) finalPrompt += mainTextString;
        }

        return finalPrompt;
    } catch(err) {
        return text;
    }
}
window.__pg_compileFinalPrompt = compileFinalPrompt;

function getExpandedText(text) {
    return compileFinalPrompt(text);
}
window.__pg_getExpandedText = getExpandedText;

// 🌟 辅助高亮编辑器寻址核心定位
function findActiveWidget(textareaInitialValue) {
    const ctx = window.__pg_editing_context;
    if (ctx && ctx.node) {
        const node = ctx.node;
        if (ctx.type === "grid" && ctx.index !== undefined) {
            const widgetName = `prompt_${ctx.index}`;
            const w = node.widgets?.find(w => w.name === widgetName) || node.__hidden_widgets?.find(w => w.name === widgetName);
            if (w) return w;
        } else if (ctx.type === "text" || ctx.type === "receiver") {
            const w = node.widgets?.find(w => w.name === "text") || node.__hidden_widgets?.find(w => w.name === "text");
            if (w) return w;
        }
    }

    let idx = null, titleText = "";
    document.querySelectorAll("div").forEach(div => {
        if (div.offsetWidth > 0 && div.offsetHeight > 0 && div.textContent && div.textContent.includes("编辑") && div.textContent.includes("格提示词")) {
            titleText = div.textContent;
        }
    });
    if (titleText) {
        const match = titleText.match(/编辑第\s*(\d+)/);
        if (match) idx = match[1];
    }

    if (app && app.graph && app.graph._nodes) {
        let candidates = app.graph._nodes.filter(n => n.type && isPromptNode(n.type));
        if (idx !== null) {
            for (let node of candidates) {
                const widgetName = `prompt_${idx}`;
                const w = node.widgets?.find(w => w.name === widgetName) || node.__hidden_widgets?.find(w => w.name === widgetName);
                if (w && (node.selected || w.value === textareaInitialValue)) return w;
            }
        }
    }
    return null;
}

// 🌟 修复渲染器：确保输入框预览的实时渲染排序和句号结尾效果对应
function renderFormattedText(text) {
    if (!text) return "";
    try {
        const activeRulesMap = window.__pg_macro_rules || {};
        const activeFixedData = window.__pg_fixed_prompt_data || { text: "", remove: [] };

        let matchedWithIndices = [];
        for (let trigger in activeRulesMap) {
            if (!trigger) continue;
            let rule = activeRulesMap[trigger];
            if (!rule) continue;
            let pStr = "(?<![a-zA-Z0-9_])" + escapeRegExp(trigger) + "(?![a-zA-Z0-9_])";
            let pRx = new RegExp(pStr, "gi");
            let match;
            while ((match = pRx.exec(text)) !== null) {
                matchedWithIndices.push({ trigger: trigger, rule: rule, index: match.index });
            }
        }
        matchedWithIndices.sort((a, b) => b.index - a.index);

        let seen = new Set();
        let uniqueMatched = [];
        for (let m of matchedWithIndices) {
            let lower = m.trigger.toLowerCase();
            if (!seen.has(lower)) {
                seen.add(lower);
                uniqueMatched.push(m);
            }
        }

        let redWords = [];
        uniqueMatched.forEach(m => {
            let rule = m.rule;
            if (rule && rule.remove) {
                const arr = Array.isArray(rule.remove) ? rule.remove : (typeof rule.remove === 'string' ? [rule.remove] : []);
                arr.forEach(rm => { 
                    if (rm && typeof rm === 'string') {
                        rm.split(/[,，.。;；]/).forEach(p => { 
                            if (p && p.trim() && !redWords.includes(p.trim().toLowerCase())) {
                                redWords.push(p.trim().toLowerCase()); 
                            }
                        }); 
                    }
                });
            }
        });

        if (activeFixedData && Array.isArray(activeFixedData.remove)) {
            activeFixedData.remove.forEach(rm => { 
                if (rm && typeof rm === 'string' && rm.trim() && !redWords.includes(rm.trim().toLowerCase())) {
                    redWords.push(rm.trim().toLowerCase()); 
                } 
            });
        }

        let greenPhrases = [];
        uniqueMatched.forEach(m => {
            let rule = m.rule;
            if (rule && rule.insert && typeof rule.insert === 'string' && rule.insert.trim()) {
                rule.insert.split(/[,，.。;；]/).map(p => p.trim()).filter(Boolean).forEach(p => {
                    if (!greenPhrases.includes(p)) greenPhrases.push(p);
                });
            }
        });

        redWords.sort((a, b) => b.length - a.length);

        let isChinese = /[\u4e00-\u9fff]/.test(text);
        let comma = isChinese ? "，" : ", ";
        
        // 🌟 视觉效果高亮同步：排在最前头并追加点号
        let greenHtml = "";
        if (greenPhrases.length > 0) {
            let spanItems = greenPhrases.map(ph => `<span class="pg-highlight-green">${escapeHTML(ph)}</span>`);
            greenHtml = spanItems.join(comma) + ". ";
        }

        let segments = text.split(/([,，.。;；\n\r]+)/), absorbed = new Set(), formattedParts = [];

        for (let i = 0; i < segments.length; i++) {
            if (absorbed.has(i)) continue;
            let seg = segments[i]; if (!seg) continue;
            if (/^[,，.。;；\n\r]+$/.test(seg)) { formattedParts.push(escapeHTML(seg)); continue; }
            
            let trimmed = seg.trim(); if (!trimmed) { formattedParts.push(escapeHTML(seg)); continue; }
            
            let hasConflict = false;
            for (let rw of redWords) {
                if (trimmed.toLowerCase().includes(rw)) { hasConflict = true; break; }
            }

            if (hasConflict) {
                let nextIdx = i + 1, combinedText = seg;
                if (nextIdx < segments.length && /^[,，.。;；]+$/.test(segments[nextIdx])) {
                    combinedText += segments[nextIdx]; absorbed.add(nextIdx);
                }
                formattedParts.push(`<span class="pg-highlight-red-phrase">${escapeHTML(combinedText)}</span>`);
            } else {
                let tempHtml = escapeHTML(seg), triggerPlaceholders = [], tPid = 0;
                Array.from(seen).forEach(trg => {
                    let escapedTrg = escapeHTML(trg);
                    let trgRx = new RegExp("(?<![a-zA-Z0-9_])" + escapeRegExp(escapedTrg) + "(?![a-zA-Z0-9_])", "gi");
                    tempHtml = tempHtml.replace(trgRx, (match) => {
                        let placeholder = `___TRG_${tPid}___`;
                        triggerPlaceholders.push({ id: placeholder, html: `<span class="pg-highlight-trigger-badge">${match}</span>` });
                        tPid++; return placeholder;
                    });
                });
                triggerPlaceholders.forEach(item => { tempHtml = tempHtml.split(item.id).join(item.html); });
                formattedParts.push(tempHtml);
            }
        }

        let mainHtml = formattedParts.join("");
        return greenHtml + mainHtml + "\n";
    } catch(err) {
        return escapeHTML(text);
    }
}

let cachedKeywordModal = null;
let lastModalSearchTime = 0;
function updateButtonStates(textarea) {
    if (!textarea) return;
    let raw = textarea.value || "";
    const activeTriggers = [];
    Object.keys(macroRules).forEach(trg => {
        let pattern = new RegExp("(?<![a-zA-Z0-9_])" + escapeRegExp(trg) + "(?![a-zA-Z0-9_])", "i");
        if (pattern.test(raw)) activeTriggers.push(trg.toLowerCase());
    });

    const now = Date.now();
    if (!cachedKeywordModal || (now - lastModalSearchTime > 2000)) {
        lastModalSearchTime = now;
        cachedKeywordModal = document.getElementById("pg16-keyword-picker");
        if (!cachedKeywordModal) {
            for (let div of document.querySelectorAll("div")) {
                if (div.id === "pg16-keyword-picker" || (div.offsetWidth > 0 && div.offsetHeight > 0 && div.innerHTML && div.innerHTML.includes("点击加载关键词"))) {
                    cachedKeywordModal = div;
                    break;
                }
            }
        }
    }

    if (cachedKeywordModal && document.body.contains(cachedKeywordModal)) {
        cachedKeywordModal.querySelectorAll("button, div, span").forEach(tagEl => {
            const textVal = tagEl.innerText ? tagEl.innerText.trim().toLowerCase() : "";
            if (!textVal || textVal.length > 20 || ["x", "✕", "×", "close"].includes(textVal) || textVal.includes("点击加载")) return;
            if (activeTriggers.includes(textVal)) tagEl.classList.add("pg-keyword-btn-active");
            else tagEl.classList.remove("pg-keyword-btn-active");
        });
    }
}

function toggleKeywordInText(textarea, tag) {
    let raw = textarea.value || "";
    tag = tag.trim(); if (!tag) return;
    const allTriggers = Object.keys(macroRules).length > 0 ? Object.keys(macroRules) : [tag];
    let activeTriggersInText = [], cleanText = raw;

    for (let trg of allTriggers) {
        let pattern = new RegExp("(?<![a-zA-Z0-9_])" + escapeRegExp(trg) + "(?![a-zA-Z0-9_])", "i");
        if (pattern.test(raw)) {
            activeTriggersInText.push(trg); cleanText = cleanText.replace(pattern, "").trim();
        }
    }

    let matchedTrigger = allTriggers.find(t => t.toLowerCase() === tag.toLowerCase()) || tag;
    if (activeTriggersInText.some(t => t.toLowerCase() === tag.toLowerCase())) {
        activeTriggersInText = activeTriggersInText.filter(t => t.toLowerCase() !== tag.toLowerCase());
    } else {
        activeTriggersInText.push(matchedTrigger);
    }

    cleanText = cleanText.replace(/[,，\s*]+[,，]/g, ",");
    let clauses = cleanText.split(/[,，;；]/).map(c => c.trim()).filter(Boolean);
    let isChinese = /[\u4e00-\u9fff]/.test(raw) || /[\u4e00-\u9fff]/.test(tag), comma = isChinese ? "，" : ", ";
    let finalPrompt = clauses.join(comma);
    
    if (activeTriggersInText.length > 0) finalPrompt += (finalPrompt ? " " : "") + activeTriggersInText.join(" ");
    finalPrompt = isChinese ? finalPrompt.replace(/^[，\s]+|[，\s]+$/g, "") : finalPrompt.replace(/^[,\s]+|[,\s]+$/g, "");
    textarea.value = finalPrompt;

    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    textarea.dispatchEvent(new Event("change", { bubbles: true }));

    const container = textarea.parentNode, backdrop = container ? container.querySelector(".pg-highlight-backdrop") : null;
    if (backdrop) backdrop.innerHTML = renderFormattedText(textarea.value);
    updateButtonStates(textarea);
}

if (!window.__pgClickRegistered) {
    window.__pgClickRegistered = true;
    document.addEventListener("click", (e) => {
        let parent = e.target, isKeywordModal = false;
        while (parent && parent !== document.body) {
            if (parent.innerHTML && parent.innerHTML.includes("点击加载关键词")) { isKeywordModal = true; break; }
            parent = parent.parentElement;
        }

        if (isKeywordModal) {
            const isClose = e.target.closest(".close, .close-btn, .litegraph-modal-close");
            const btnText = (e.target.innerText || e.target.textContent || "").trim().toLowerCase();
            const isCloseSymbol = ["x", "✕", "×", "close", "cancel", "取消"].includes(btnText);
            if (isClose || isCloseSymbol) return;

            const word = e.target.innerText ? e.target.innerText.trim() : "";
            if (word && word.length > 0 && word.length < 30 && !word.includes("点击加载")) {
                e.preventDefault(); e.stopPropagation(); e.stopImmediatePropagation();
                const activeTextarea = findActiveEditingTextarea();
                if (activeTextarea) toggleKeywordInText(activeTextarea, word);
            }
        }
    }, true);
}

function initHighlight(textarea) {
    if (textarea.dataset.highlightInitialized) return;
    textarea.dataset.highlightInitialized = "true";
    window.__pg_active_textarea = textarea;

    const initialVal = textarea.value || ""; 
    
    const activeWidget = findActiveWidget(initialVal);
    if (activeWidget) {
        textarea._widget = activeWidget; 
        activeWidget._textarea = textarea;
    }

    Object.defineProperty(textarea, 'value', {
        get() { return this._raw_value ?? ""; },
        set(v) {
            this._raw_value = v ?? "";
            Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(this, this._raw_value);
            if (this._widget) {
                this._widget.value = this._raw_value;
            }
            handleInput();
        },
        configurable: true
    });

    textarea.addEventListener("input", (e) => {
        textarea._raw_value = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').get.call(textarea);
        if (textarea._widget) {
            textarea._widget.value = textarea._raw_value;
            if (textarea._widget.callback) textarea._widget.callback(textarea._raw_value);
        }
    });

    const parent = textarea.parentNode, styles = window.getComputedStyle(textarea);
    const container = document.createElement("div");
    container.className = "pg-highlight-container";
    
    Object.assign(container.style, { 
        position: "relative", 
        boxSizing: "border-box", 
        width: "100%", 
        height: "100%",
        display: "flex",
        flexDirection: "column",
        minHeight: "150px"
    });

    if (styles.flex) container.style.flex = styles.flex;
    if (styles.flexGrow) container.style.flexGrow = styles.flexGrow;
    if (styles.margin) container.style.margin = styles.margin;
    if (styles.padding) container.style.padding = "0";

    parent.insertBefore(container, textarea); 
    container.appendChild(textarea);

    const backdrop = document.createElement("div");
    backdrop.className = "pg-highlight-backdrop";
    Object.assign(backdrop.style, { 
        position: "absolute", 
        top: "0", 
        left: "0", 
        width: "100%", 
        height: "100%", 
        pointerEvents: "none", 
        boxSizing: "border-box", 
        overflowY: "auto", 
        overflowX: "hidden",
        zIndex: "1"
    });
    container.insertBefore(backdrop, textarea);

    function syncStyles() {
        const currentStyles = window.getComputedStyle(textarea);
        [ "fontFamily", "fontSize", "fontWeight", "lineHeight", "letterSpacing", "paddingTop", "paddingBottom", "paddingLeft", "paddingRight", "borderWidth", "borderStyle", "boxSizing", "whiteSpace", "wordBreak", "textAlign" ].forEach(p => {
            backdrop.style[p] = currentStyles[p];
        });
        
        textarea.classList.add("pg-highlight-textarea");
        Object.assign(textarea.style, { 
            position: "relative", 
            width: "100%", 
            height: "100%", 
            margin: "0", 
            zIndex: "2",
            boxSizing: "border-box"
        });
    }

    syncStyles();
    
    function handleInput() {
        callingFromPreview = true;
        try { 
            const formatted = renderFormattedText(textarea.value);
            backdrop.innerHTML = formatted; 
            textarea.style.setProperty("color", "transparent", "important");
            textarea.style.setProperty("caret-color", "#ffffff", "important");
        } catch (err) {
            textarea.style.setProperty("color", "#eeeeee", "important");
            backdrop.innerHTML = escapeHTML(textarea.value);
        } finally { 
            callingFromPreview = false; 
        }
        syncScroll();
    }

    function syncScroll() { 
        backdrop.scrollTop = textarea.scrollTop; 
        backdrop.scrollLeft = textarea.scrollLeft; 
    }
    
    textarea.addEventListener("input", handleInput); 
    textarea.addEventListener("scroll", syncScroll);
    
    new ResizeObserver(() => { 
        syncStyles(); 
        syncScroll(); 
    }).observe(textarea);

    textarea._raw_value = initialVal;
    handleInput(); 
    updateButtonStates(textarea);
}

fetchRules();

setInterval(() => {
    const textareas = document.querySelectorAll("textarea");
    let activeTa = null;
    textareas.forEach(ta => {
        if (ta && ta.offsetWidth > 0 && ta.offsetHeight > 0 && isTargetTextarea(ta)) {
            if (!ta.dataset.highlightInitialized) initHighlight(ta);
            activeTa = ta;
        }
    });

    if (activeTa) updateButtonStates(activeTa);
}, 300);

app.registerExtension({ name: "PromptGrid.VisualOverlayHighlight" });