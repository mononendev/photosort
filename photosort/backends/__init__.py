from __future__ import annotations
from .base import Backend, Item, Result, PRICES, image_tokens

def get(name: str, base_url: str | None = None) -> Backend:
    if name == "gemini":
        from .gemini import GeminiBackend
        return GeminiBackend()
    if name == "anthropic":
        from .anthropic_ import AnthropicBackend
        return AnthropicBackend()
    if name == "ollama":
        from .ollama import OllamaBackend
        return OllamaBackend(base_url)
    raise SystemExit(f"unknown backend {name!r} (gemini | anthropic | ollama)")
