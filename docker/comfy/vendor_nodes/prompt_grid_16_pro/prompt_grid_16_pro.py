import os
import json
import re
from aiohttp import web
from server import PromptServer

MACRO_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "lora_macro_rules.json")
FIXED_PROMPT_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "lora_global_fixed.json")

# ----------------- 基础配置加载 -----------------
def load_macro_rules():
    if os.path.exists(MACRO_FILE):
        try:
            with open(MACRO_FILE, 'r', encoding='utf-8') as f:
                return json.load(f)
        except:
            pass
    return {}

def save_macro_rules(rules):
    try:
        with open(MACRO_FILE, 'w', encoding='utf-8') as f:
            json.dump(rules, f, indent=4, ensure_ascii=False)
    except Exception as e:
        print(f"[PromptGrid16Pro] 保存宏参数规则出错: {e}")

def load_fixed_prompt():
    if os.path.exists(FIXED_PROMPT_FILE):
        try:
            with open(FIXED_PROMPT_FILE, 'r', encoding='utf-8') as f:
                return json.load(f)
        except:
            pass
    return {"text": "", "remove": []}

def save_fixed_prompt(data):
    try:
        with open(FIXED_PROMPT_FILE, 'w', encoding='utf-8') as f:
            json.dump(data, f, indent=4, ensure_ascii=False)
    except Exception as e:
        print(f"[PromptGrid16Pro] 保存全局固定提示词出错: {e}")

# ----------------- 标点与格式排版净化 -----------------
def clean_unbalanced_brackets(text):
    stack = []
    to_remove = set()
    for i, char in enumerate(text):
        if char == '(':
            stack.append(i)
        elif char == ')':
            if stack:
                stack.pop()
            else:
                to_remove.add(i)
    while stack:
        to_remove.add(stack.pop())
        
    stack_cjk = []
    for i, char in enumerate(text):
        if char == '（':
            stack_cjk.append(i)
        elif char == '）':
            if stack_cjk:
                stack_cjk.pop()
            else:
                to_remove.add(i)
    while stack_cjk:
        to_remove.add(stack_cjk.pop())
        
    return "".join([char for i, char in enumerate(text) if i not in to_remove])

def clean_delimiters(text, is_chinese=True):
    comma = "，" if is_chinese else ", "
    text = re.sub(r'[,，;；\s]*[,，;；][,，;；\s]*', ' #COMMA# ', text)
    text = re.sub(r'\s+', ' ', text)
    text = text.replace(' #COMMA# ', comma)
    text = re.sub(r'\s*\.\s*', '. ', text)
    text = re.sub(r'\s*。\s*', '。', text)
    text = re.sub(r'\s*' + re.escape(comma.strip()) + r'\s*', comma, text)
    return text.strip(' ,，;；')

# 🌟 全新重构编译算法：新增词（绿色词）置于最前端加句号拦截，按LIFO倒序排列
def compile_final_prompt(text, rules, fixed_data, include_fixed_prefix=True):
    if not text or not isinstance(text, str):
        return ""

    # 1. 扫描匹配到的带有排序索引的触发词
    matched_rules = []
    for trigger, rule in rules.items():
        if not trigger:
            continue
        pattern = re.compile(rf"(?<![a-zA-Z0-9_]){re.escape(trigger)}(?![a-zA-Z0-9_])", re.IGNORECASE)
        for m in pattern.finditer(text):
            matched_rules.append({
                "trigger": trigger,
                "rule": rule,
                "index": m.start()
            })

    # 🌟 重点排序：按照索引位置倒序排列 (后输入的在数组前面, LIFO序列)
    matched_rules.sort(key=lambda x: x["index"], reverse=True)

    # 去除重复触发规则
    seen_trg = set()
    unique_matched = []
    for m in matched_rules:
        trg_lower = m["trigger"].lower()
        if trg_lower not in seen_trg:
            seen_trg.add(trg_lower)
            unique_matched.append(m)

    # 2. 依次提取匹配规则中的绿色添加词 
    green_phrases = []
    for m in unique_matched:
        rule = m["rule"]
        insert = rule.get("insert", "")
        if insert and isinstance(insert, str):
            for part in re.split(r'[,，.。;；]', insert):
                p_clean = part.strip()
                if p_clean and p_clean not in green_phrases:
                    green_phrases.append(p_clean)

    # 3. 收集所有的替换拦截以及冲突淘汰词
    conflicts = []
    for m in unique_matched:
        rule = m["rule"]
        remove = rule.get("remove", [])
        remove_list = remove if isinstance(remove, list) else ([remove] if isinstance(remove, str) else [])
        for rm in remove_list:
            if rm and isinstance(rm, str):
                for p in re.split(r'[,，.。;；]', rm):
                    p_clean = p.strip().lower()
                    if p_clean and p_clean not in conflicts:
                        conflicts.append(p_clean)

    fixed_remove = fixed_data.get("remove", [])
    fixed_remove_list = fixed_remove if isinstance(fixed_remove, list) else ([fixed_remove] if isinstance(fixed_remove, str) else [])
    for rm in fixed_remove_list:
        if rm and isinstance(rm, str):
            for p in re.split(r'[,，.。;；]', rm):
                p_clean = p.strip().lower()
                if p_clean and p_clean not in conflicts:
                    conflicts.append(p_clean)

    is_chinese = any('\u4e00' <= char <= '\u9fff' for char in text)
    comma = "，" if is_chinese else ", "

    # 4. 清理主句（拆分后过滤原触发词和淘汰冲突字词）
    clauses = re.split(r'([,，.。;；\n\r]+)', text)
    final_clauses = []
    active_triggers_lower = list(seen_trg)

    for clause in clauses:
        if not clause or re.match(r'^[,，.。;；\n\r]+$', clause):
            continue
        trimmed = clause.strip()
        if not trimmed:
            continue
        
        lower_clause = trimmed.lower()
        if any(conf in lower_clause for conf in conflicts):
            continue
        if any(lower_clause == trg for trg in active_triggers_lower):
            continue

        clean_clause = trimmed
        for trg in active_triggers_lower:
            clean_clause = re.sub(rf"(?:\s*[,，.。;；]?\s*){re.escape(trg)}(?:\s*[,，。;；]?\s*)", " ", clean_clause, flags=re.IGNORECASE).strip()
        
        if clean_clause:
            final_clauses.append(clean_clause)

    # 5. 可选注入全局固定前缀（过滤其中的淘汰防冲突词）
    resolved_style = ""
    if include_fixed_prefix:
        style_prefix = fixed_data.get("text", "").strip()
        if style_prefix:
            style_clauses = re.split(r'([,，.。;；\n\r]+)', style_prefix)
            valid_style_clauses = []
            for clause in style_clauses:
                if not clause or re.match(r'^[,，.。;；\n\r]+$', clause):
                    continue
                trimmed = clause.strip()
                if not trimmed:
                    continue
                if not any(conf in trimmed.lower() for conf in conflicts):
                    valid_style_clauses.append(trimmed)
            resolved_style = comma.join(valid_style_clauses)

    # 6. 装配和排版输出：前置关键词 + 句号。 + 全局固定风格 + 清理后的主要内容
    period = "。" if is_chinese else ". "
    green_string = (comma.join(green_phrases) + period) if green_phrases else ""
    main_prompt_string = comma.join(final_clauses)

    final_prompt = ""
    if green_string:
        final_prompt += green_string
    if resolved_style:
        final_prompt += resolved_style
        if main_prompt_string:
            final_prompt += comma + main_prompt_string
    else:
        if main_prompt_string:
            final_prompt += main_prompt_string

    final_prompt = clean_unbalanced_brackets(final_prompt)
    final_prompt = clean_delimiters(final_prompt, is_chinese)
    return final_prompt

def apply_prompt_macros_for_grid(text, fixed_remove):
    # 此函数为旧后端的快捷兼容封装，文本扩写与接收默认不混入全局前缀模板
    rules = load_macro_rules()
    fixed_data = {"text": "", "remove": fixed_remove}
    return compile_final_prompt(text, rules, fixed_data, include_fixed_prefix=False)

# ---- API 接口 -------------------
@PromptServer.instance.routes.get("/prompt_grid/get_loras")
async def get_loras(request):
    return web.json_response({"loras": [], "subdirs": [], "config": {}})

@PromptServer.instance.routes.post("/prompt_grid/save_config")
async def save_lora_config(request):
    return web.json_response({"status": "ok"})

@PromptServer.instance.routes.get("/prompt_grid/get_macros")
async def get_macros(request):
    rules = load_macro_rules()
    return web.json_response({"rules": rules})

@PromptServer.instance.routes.post("/prompt_grid/save_macros")
async def save_macros(request):
    post = await request.json()
    save_macro_rules(post.get("rules", {}))
    return web.json_response({"status": "ok"})

@PromptServer.instance.routes.get("/prompt_grid/get_fixed_prompt")
async def get_fixed_prompt_api(request):
    data = load_fixed_prompt()
    return web.json_response(data)

@PromptServer.instance.routes.post("/prompt_grid/save_fixed_prompt")
async def save_fixed_prompt_api(request):
    post = await request.json()
    save_fixed_prompt(post)
    return web.json_response({"status": "ok"})

# ----------------- 节点类结构 -----------------
class PromptGrid16Pro:
    @classmethod
    def INPUT_TYPES(cls):
        required = {
            "clip": ("CLIP",)
        }
        optional = {
            "selected": ("INT", {"default": 1, "min": 1, "max": 16, "step": 1}),
            "global_fixed_prompt": ("STRING", {"multiline": True, "default": ""}),
        }
        for i in range(1, 17):
            optional[f"prompt_{i}"] = ("STRING", {"multiline": True, "default": ""})
        return {"required": required, "optional": optional}

    RETURN_TYPES = ("CONDITIONING",)
    RETURN_NAMES = ("CONDITIONING",)
    FUNCTION = "run"
    CATEGORY = "提示词工具"

    def run(self, clip, selected=1, global_fixed_prompt="", **kwargs):
        try:
            index = int(selected)
        except Exception:
            index = 1

        index = max(1, min(16, index))
        text = kwargs.get(f"prompt_{index}", "") or ""
        
        rules = load_macro_rules()
        fixed_data = load_fixed_prompt()

        combined_text = compile_final_prompt(text, rules, fixed_data, include_fixed_prefix=True)

        print(f"\n--- [PromptGrid 后端拦截器/Pro] ---")
        print(f"【最终输出提示词】: {combined_text}")
        print(f"-----------------------------------------\n")

        tokens = clip.tokenize(combined_text)
        cond, pooled = clip.encode_from_tokens(tokens, return_pooled=True)
        conditioning = [[cond, {"pooled_output": pooled}]]

        return (conditioning,)

class PromptGrid16Classic:
    @classmethod
    def INPUT_TYPES(cls):
        optional = {
            "selected": ("INT", {"default": 1, "min": 1, "max": 16, "step": 1}),
            "global_fixed_prompt": ("STRING", {"multiline": True, "default": ""}),
        }
        for i in range(1, 17):
            optional[f"prompt_{i}"] = ("STRING", {"multiline": True, "default": ""})
        return {"required": {}, "optional": optional}

    RETURN_TYPES = ("STRING",)
    RETURN_NAMES = ("text",)
    FUNCTION = "run"
    CATEGORY = "提示词工具"

    def run(self, selected=1, global_fixed_prompt="", **kwargs):
        try:
            index = int(selected)
        except Exception:
            index = 1
        index = max(1, min(16, index))
        text = kwargs.get(f"prompt_{index}", "") or ""
        
        rules = load_macro_rules()
        fixed_data = load_fixed_prompt()

        combined_text = compile_final_prompt(text, rules, fixed_data, include_fixed_prefix=True)
        return (combined_text,)

class PromptTextToolPro:
    @classmethod
    def INPUT_TYPES(cls):
        return {
            "required": {"text": ("STRING", {"multiline": True, "default": ""})},
            "optional": {"input_text": ("STRING", {"forceInput": True, "default": ""})},
        }
    RETURN_TYPES = ("STRING",)
    RETURN_NAMES = ("text",)
    FUNCTION = "run"
    CATEGORY = "提示词工具"
    def run(self, text="", input_text=None):
        raw = text
        if input_text is not None and str(input_text).strip() != "":
            raw = str(input_text)
        rules = load_macro_rules()
        fixed_data = load_fixed_prompt()
        return (compile_final_prompt(raw, rules, fixed_data, include_fixed_prefix=False),)

class PromptTextReceiverPro:
    @classmethod
    def INPUT_TYPES(cls):
        return {"required": {"input_text": ("STRING", {"forceInput": True, "default": ""})}}
    RETURN_TYPES = ("STRING",)
    RETURN_NAMES = ("text",)
    FUNCTION = "run"
    CATEGORY = "提示词工具"
    OUTPUT_NODE = True
    def run(self, input_text=""):
        text = input_text or ""
        return {"ui": {"text": [text]}, "result": (text,)}

NODE_CLASS_MAPPINGS = {
    "prompt_grid_16_pro": PromptGrid16Pro,
    "prompt_grid_16_classic": PromptGrid16Classic,
    "prompt_text_tool_pro": PromptTextToolPro,
    "prompt_text_receiver_pro": PromptTextReceiverPro
}

NODE_DISPLAY_NAME_MAPPINGS = {
    "prompt_grid_16_pro": "16宫格提示词 Pro",
    "prompt_grid_16_classic": "16宫格提示词 Classic",
    "prompt_text_tool_pro": "提示词扩写文本工具",
    "prompt_text_receiver_pro": "接收文本工具"
}