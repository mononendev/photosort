"""Anthropic Message Batches backend."""
from __future__ import annotations
import base64
import json
from pathlib import Path

from .base import Backend, Item, Result
from .. import schema


class AnthropicBackend(Backend):
    name = "anthropic"
    default_model = "claude-opus-5"
    max_batch = 350  # 256 MB batch limit; ~0.6 MB per request after base64

    def __init__(self):
        self._client = None

    @property
    def client(self):
        if self._client is None:
            import anthropic
            self._client = anthropic.Anthropic()
        return self._client

    @staticmethod
    def build_params(item: Item, model: str, cfg: dict) -> dict:
        def img(b: bytes):
            return {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg",
                                                "data": base64.standard_b64encode(b).decode()}}
        content = [{"type": "text", "text": "Image 1: full frame."}, img(item.frame)]
        if item.crop:
            content += [{"type": "text", "text": "Image 2: native-resolution crop of the primary subject's head and upper body."}, img(item.crop)]
        content.append({"type": "text", "text": item.context + "\n\nAnalyze the photo and return the JSON object."})
        params = {
            "model": model,
            "max_tokens": cfg["max_output_tokens"],
            # Stable system prompt first so it is a cacheable prefix across the whole batch.
            "system": [{"type": "text", "text": schema.SYSTEM_PROMPT, "cache_control": {"type": "ephemeral"}}],
            "messages": [{"role": "user", "content": content}],
            "output_config": {"format": {"type": "json_schema", "schema": schema.json_schema(strict=True)}},
        }
        if "haiku" not in model:  # effort is not accepted on Haiku 4.5
            params["output_config"]["effort"] = cfg.get("anthropic_effort", "low")
        return params

    def submit(self, items: list[Item], model: str, cfg: dict, workdir: Path) -> str:
        from anthropic.types.message_create_params import MessageCreateParamsNonStreaming
        from anthropic.types.messages.batch_create_params import Request
        reqs = [Request(custom_id=it.key, params=MessageCreateParamsNonStreaming(**self.build_params(it, model, cfg))) for it in items]
        batch = self.client.messages.batches.create(requests=reqs)
        return batch.id

    def status(self, batch_id: str) -> str:
        b = self.client.messages.batches.retrieve(batch_id)
        return "ended" if b.processing_status == "ended" else "running"

    def fetch(self, batch_id: str) -> list[Result]:
        out: list[Result] = []
        for r in self.client.messages.batches.results(batch_id):
            key = r.custom_id
            t = r.result.type
            if t != "succeeded":
                err = getattr(r.result, "error", None)
                out.append(Result(key, error=f"anthropic {t}: {err}"))
                continue
            msg = r.result.message
            if msg.stop_reason == "refusal":
                out.append(Result(key, error=f"anthropic refusal: {getattr(msg, 'stop_details', None)}"))
                continue
            try:
                text = next(b.text for b in msg.content if b.type == "text")
                data = schema.validate(json.loads(text))
                u = msg.usage
                out.append(Result(key, data=data, usage={
                    "in": u.input_tokens, "out": u.output_tokens,
                    "cache_read": getattr(u, "cache_read_input_tokens", None),
                    "cache_write": getattr(u, "cache_creation_input_tokens", None)}))
            except Exception as e:
                out.append(Result(key, error=f"anthropic parse: {type(e).__name__}: {e} (stop={msg.stop_reason})"))
        return out
