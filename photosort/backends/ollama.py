"""Ollama backend: synchronous requests to a local/cluster Ollama server (no API cost).

Works with any vision model Ollama serves (qwen3-vl:4b, qwen3-vl:8b, gemma3, ...). Uses the
native /api/chat endpoint with `format=<json schema>` for constrained JSON output.
"""
from __future__ import annotations
import base64
import json
import os
import time
import urllib.request
import urllib.error

from .base import Backend, Item, Result
from .. import schema


class OllamaBackend(Backend):
    name = "ollama"
    default_model = "qwen3-vl:4b-instruct"
    sync = True
    max_batch = 10**9

    def __init__(self, base_url: str | None = None):
        self.base_url = (base_url or os.environ.get("OLLAMA_HOST") or "http://127.0.0.1:11434").rstrip("/")
        if not self.base_url.startswith("http"):
            self.base_url = "http://" + self.base_url

    def _post(self, path: str, body: dict, timeout: float = 600) -> dict:
        req = urllib.request.Request(self.base_url + path, data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"}, method="POST")
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())

    @staticmethod
    def _shrink(jpeg: bytes, long_edge: int) -> bytes:
        import io
        from PIL import Image
        im = Image.open(io.BytesIO(jpeg))
        if max(im.size) <= long_edge:
            return jpeg
        from .. import images as I
        return I.to_jpeg(I.resize_long_edge(im.convert("RGB"), long_edge), 82)

    def build_request(self, item: Item, model: str, cfg: dict) -> dict:
        frame = self._shrink(item.frame, cfg.get("ollama_frame_long_edge", 1024))
        images = [base64.standard_b64encode(frame).decode()]
        text = "Image 1: full frame."
        if item.crop:
            images.append(base64.standard_b64encode(item.crop).decode())
            text += "\nImage 2: native-resolution crop of the primary subject's head and upper body."
        text += "\n\n" + item.context + "\n\nAnalyze the photo and return the JSON object."
        return {
            "model": model,
            "messages": [{"role": "system", "content": schema.SYSTEM_PROMPT},
                         {"role": "user", "content": text, "images": images}],
            "format": schema.json_schema(strict=False, max_lengths=cfg.get("ollama_schema_max_lengths", True)),
            "stream": False,
            "think": False,      # qwen3-vl defaults to thinking, which eats num_predict and leaves content empty
            "keep_alive": "30m",
            # Qwen3 instruct recommended sampling; greedy decoding makes small models loop in free-text fields.
            "options": {"temperature": 0.7, "top_p": 0.8, "top_k": 20, "repeat_penalty": 1.1, "repeat_last_n": 128,
                        "num_predict": cfg.get("ollama_num_predict", 1536), "num_ctx": cfg.get("ollama_num_ctx", 8192)},
        }

    def classify(self, item: Item, model: str, cfg: dict) -> Result:
        t = time.time()
        try:
            resp = self._post("/api/chat", self.build_request(item, model, cfg))
        except urllib.error.HTTPError as e:
            return Result(item.key, error=f"ollama http {e.code}: {e.read().decode()[:300]}")
        except Exception as e:
            return Result(item.key, error=f"ollama: {type(e).__name__}: {e}")
        text = (resp.get("message") or {}).get("content", "")
        ns = lambda k: (resp.get(k) or 0) / 1e9
        usage = {"in": resp.get("prompt_eval_count"), "out": resp.get("eval_count"),
                 "seconds": round(time.time() - t, 2), "model": resp.get("model"),
                 "prefill_s": round(ns("prompt_eval_duration"), 2), "decode_s": round(ns("eval_duration"), 2),
                 "tok_s": round((resp.get("eval_count") or 0) / ns("eval_duration"), 1) if ns("eval_duration") else None}
        if not text and (resp.get("message") or {}).get("thinking"):
            return Result(item.key, error="ollama: model spent its output budget thinking; content empty", usage=usage)
        try:
            return Result(item.key, data=schema.validate(json.loads(text)), usage=usage)
        except Exception as e:
            return Result(item.key, error=f"ollama parse: {type(e).__name__}: {e}: {text[:200]}", usage=usage)

    # Batch-API interface is not used for sync backends.
    def submit(self, items, model, cfg, workdir):
        raise NotImplementedError("ollama backend is synchronous; use classify()")

    def status(self, batch_id):
        return "ended"

    def fetch(self, batch_id):
        return []

    def estimate(self, model, n_with_crop, n_without, cfg):
        n = n_with_crop + n_without
        fw = cfg["frame_long_edge"]
        # Qwen3-VL: one token per 32x32 px after patch merging
        img = (fw * fw * 2 // 3) // 1024 + (cfg["crop_size"] ** 2 // 1024 if n_with_crop else 0)
        return {"model": model, "input_tokens": n * (img + 900), "output_tokens": n * 350,
                "interactive_usd": 0.0, "batch_usd": 0.0, "priced": True}
