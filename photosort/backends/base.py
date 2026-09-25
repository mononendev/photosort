from __future__ import annotations
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional

# Interactive $/1M tokens (input, output). Batch is half. Checked 2026-09-25 against provider pricing pages.
PRICES: dict[str, tuple[float, float]] = {
    "gemini-2.5-flash-lite": (0.10, 0.40),
    "gemini-2.5-flash": (0.30, 2.50),
    "gemini-3.1-flash-lite": (0.25, 1.50),
    "gemini-3.5-flash-lite": (0.30, 2.50),
    "gemini-3.5-flash": (1.50, 9.00),
    "gemini-3.8-flash": (0.75, 3.75),
    "claude-haiku-4-5": (1.00, 5.00),
    "claude-sonnet-5": (2.00, 10.00),
    "claude-opus-5": (5.00, 25.00),
}
PROMPT_TOKENS = 900       # system prompt + detector context, approx
OUTPUT_TOKENS = 350       # JSON with keywords/remarks, approx


def image_tokens(model: str, w: int, h: int) -> int:
    """Approximate image token cost for one image at w x h pixels."""
    if model.startswith("gemini-3"):
        return 1120  # flat per image at MEDIA_RESOLUTION_HIGH; 560 at MEDIUM
    if model.startswith("gemini"):
        if max(w, h) <= 384:
            return 258
        unit = max(1, int(min(w, h) / 1.5))
        return 258 * (-(-w // unit)) * (-(-h // unit))
    if model.startswith("claude"):
        t = (w * h) / 750
        cap = 1600 if "haiku" in model else 4784
        return int(min(t, cap))
    return int(w * h / 750)


@dataclass
class Item:
    key: str
    frame: bytes
    crop: Optional[bytes]
    context: str


@dataclass
class Result:
    key: str
    data: Optional[dict] = None
    usage: Optional[dict] = None
    error: Optional[str] = None


class Backend(ABC):
    name: str
    default_model: str
    max_batch: int = 1000
    sync: bool = False   # True: classify() one item at a time instead of batch submit/poll

    @abstractmethod
    def submit(self, items: list[Item], model: str, cfg: dict, workdir) -> str: ...

    @abstractmethod
    def status(self, batch_id: str) -> str:
        """'running' | 'ended' | 'failed:...'"""

    @abstractmethod
    def fetch(self, batch_id: str) -> list[Result]: ...

    def estimate(self, model: str, n_with_crop: int, n_without: int, cfg: dict) -> dict:
        fw = cfg["frame_long_edge"]
        frame_t = image_tokens(model, fw, int(fw * 2 / 3))
        crop_t = image_tokens(model, cfg["crop_size"], cfg["crop_size"])
        if model.startswith("gemini-3"):
            crop_t = 560  # crops are sent at MEDIUM on Gemini 3
        inp = n_with_crop * (frame_t + crop_t + PROMPT_TOKENS) + n_without * (frame_t + PROMPT_TOKENS)
        out = (n_with_crop + n_without) * OUTPUT_TOKENS
        pi, po = PRICES.get(model, (0, 0))
        cost = (inp * pi + out * po) / 1e6
        return {"model": model, "input_tokens": inp, "output_tokens": out,
                "interactive_usd": round(cost, 2), "batch_usd": round(cost / 2, 2), "priced": model in PRICES}
