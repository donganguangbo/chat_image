import os
from aiohttp import web
from server import PromptServer

from .prompt_grid_16_pro import (
    PromptGrid16Pro,
    PromptGrid16Classic,  # 引入经典版
    PromptTextToolPro,
    PromptTextReceiverPro,
)

NODE_CLASS_MAPPINGS = {
    "prompt_grid_16_pro": PromptGrid16Pro,
    "prompt_grid_16_classic": PromptGrid16Classic,  # 注册经典版节点
    "prompt_text_tool_pro": PromptTextToolPro,
    "prompt_text_receiver_pro": PromptTextReceiverPro,
}

NODE_DISPLAY_NAME_MAPPINGS = {
    "prompt_grid_16_pro": "16宫格提示词 Pro",
    "prompt_grid_16_classic": "16宫格提示词 Classic",  # 显示为经典版
    "prompt_text_tool_pro": "提示词扩写文本工具 Pro",
    "prompt_text_receiver_pro": "接收文本工具 Pro",
}

WEB_DIRECTORY = "web"

@PromptServer.instance.routes.get("/prompt_grid_16_pro.js")
async def prompt_grid_16_pro_js(request):
    current_dir = os.path.dirname(os.path.abspath(__file__))
    js_path = os.path.join(current_dir, "web", "prompt_grid_16_pro.js")
    return web.FileResponse(js_path)

__all__ = [
    "NODE_CLASS_MAPPINGS",
    "NODE_DISPLAY_NAME_MAPPINGS",
    "WEB_DIRECTORY",
]