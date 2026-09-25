"""Gemini Batch API backend (google-genai SDK, JSONL via Files API)."""
from __future__ import annotations
import json
import time
from pathlib import Path

from .base import Backend, Item, Result
from .. import schema


class GeminiBackend(Backend):
    name = "gemini"
    default_model = "gemini-3.5-flash-lite"
    max_batch = 2000  # 2 GB file limit; ~0.6 MB per request

    def __init__(self):
        self._client = None

    @property
    def client(self):
        if self._client is None:
            from google import genai
            self._client = genai.Client()  # GEMINI_API_KEY / GOOGLE_API_KEY from env
        return self._client

    @staticmethod
    def build_request(item: Item, model: str, cfg: dict) -> dict:
        from google.genai import types
        g3 = model.startswith("gemini-3")

        def img(b: bytes, level: str):
            p = types.Part(inline_data=types.Blob(data=b, mime_type="image/jpeg"))
            if g3:
                p.media_resolution = types.PartMediaResolution(level=level)
            return p

        parts = [types.Part.from_text(text="Image 1: full frame."), img(item.frame, "MEDIA_RESOLUTION_HIGH")]
        if item.crop:
            parts += [types.Part.from_text(text="Image 2: native-resolution crop of the primary subject's head and upper body."),
                      img(item.crop, "MEDIA_RESOLUTION_MEDIUM")]
        parts.append(types.Part.from_text(text=item.context + "\n\nAnalyze the photo and return the JSON object."))

        gen = types.GenerationConfig(
            response_mime_type="application/json",
            response_json_schema=schema.json_schema(strict=False),
            max_output_tokens=cfg["max_output_tokens"],
        )
        if g3:
            gen.thinking_config = types.ThinkingConfig(thinking_level=cfg.get("gemini_thinking_level", "LOW"))
        else:
            gen.thinking_config = types.ThinkingConfig(thinking_budget=0)
        return {
            "key": item.key,
            "request": {
                "system_instruction": {"parts": [{"text": schema.SYSTEM_PROMPT}]},
                "contents": [types.Content(role="user", parts=parts).to_json_dict()],
                "generation_config": gen.to_json_dict(),
            },
        }

    def write_jsonl(self, items: list[Item], model: str, cfg: dict, path: Path) -> Path:
        with path.open("w") as f:
            for it in items:
                f.write(json.dumps(self.build_request(it, model, cfg)) + "\n")
        return path

    def submit(self, items: list[Item], model: str, cfg: dict, workdir: Path) -> str:
        from google.genai import types
        bdir = workdir / "batches"
        bdir.mkdir(exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        jsonl = self.write_jsonl(items, model, cfg, bdir / f"gemini-{stamp}.jsonl")
        up = self.client.files.upload(file=str(jsonl), config=types.UploadFileConfig(display_name=jsonl.stem, mime_type="jsonl"))
        job = self.client.batches.create(model=model, src=up.name, config={"display_name": f"photosort-{stamp}"})
        return job.name

    def status(self, batch_id: str) -> str:
        job = self.client.batches.get(name=batch_id)
        st = job.state.name
        if st in ("JOB_STATE_SUCCEEDED", "JOB_STATE_PARTIALLY_SUCCEEDED"):
            return "ended"
        if st in ("JOB_STATE_FAILED", "JOB_STATE_CANCELLED", "JOB_STATE_EXPIRED"):
            return f"failed:{st}:{job.error}"
        return "running"

    def fetch(self, batch_id: str) -> list[Result]:
        job = self.client.batches.get(name=batch_id)
        out: list[Result] = []
        if job.dest and job.dest.file_name:
            raw = self.client.files.download(file=job.dest.file_name).decode("utf-8")
            lines = [json.loads(l) for l in raw.splitlines() if l.strip()]
        elif job.dest and job.dest.inlined_responses:
            lines = [{"key": r.metadata.get("key") if r.metadata else None,
                      "response": r.response.to_json_dict() if r.response else None,
                      "error": r.error.to_json_dict() if r.error else None} for r in job.dest.inlined_responses]
        else:
            return out
        for ln in lines:
            key = ln.get("key")
            if ln.get("error"):
                out.append(Result(key, error=f"gemini: {ln['error']}"))
                continue
            resp = ln.get("response") or {}
            usage = resp.get("usageMetadata") or resp.get("usage_metadata") or {}
            try:
                cands = resp["candidates"]
                parts = cands[0]["content"]["parts"]
                text = "".join(p.get("text", "") for p in parts if not p.get("thought"))
                data = schema.validate(json.loads(text))
                out.append(Result(key, data=data, usage={
                    "in": usage.get("promptTokenCount", usage.get("prompt_token_count")),
                    "out": usage.get("candidatesTokenCount", usage.get("candidates_token_count")),
                    "thought": usage.get("thoughtsTokenCount", usage.get("thoughts_token_count"))}))
            except Exception as e:
                fr = (resp.get("candidates") or [{}])[0].get("finishReason")
                out.append(Result(key, error=f"gemini parse: {type(e).__name__}: {e} (finish={fr})"))
        return out
