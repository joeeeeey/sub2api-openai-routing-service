#!/usr/bin/env python3
from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


DEFAULT_BASE_URL = "http://127.0.0.1:8080/openai-routing"
DEFAULT_TEXT_MODELS = ["gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5"]
DEFAULT_IMAGE_TEXT_MODEL = "gpt-5.4-mini"
DEFAULT_IMAGE_MODEL = "gpt-image-2"
DEFAULT_EMBEDDINGS_MODEL = "text-embedding-3-small"
DEFAULT_IMAGE_PROMPT = "画一个时尚性感短裙丝袜的成年日本女性自拍照，不要只画红点，写实照片风格，时尚街拍，非裸露，非色情。"


class E2EFailure(Exception):
    pass


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run local OpenAI routing service E2E checks.")
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL)
    parser.add_argument("--api-key", default="")
    parser.add_argument("--text-model", action="append", dest="text_models", default=[])
    parser.add_argument("--skip-text", action="store_true")
    parser.add_argument("--skip-image", action="store_true")
    parser.add_argument("--skip-embeddings", action="store_true")
    parser.add_argument("--image-text-model", default=DEFAULT_IMAGE_TEXT_MODEL)
    parser.add_argument("--image-model", default=DEFAULT_IMAGE_MODEL)
    parser.add_argument("--image-prompt", default=DEFAULT_IMAGE_PROMPT)
    parser.add_argument("--image-size", default="1024x1024")
    parser.add_argument("--image-output-format", default="png")
    parser.add_argument("--image-quality", default="high")
    parser.add_argument("--image-background", default="auto")
    parser.add_argument("--image-output-file", default="/tmp/openai-routing-e2e-gpt-image-2.png")
    parser.add_argument("--embeddings-model", default=DEFAULT_EMBEDDINGS_MODEL)
    parser.add_argument("--timeout", type=int, default=360)
    parser.add_argument("--report-file", default="")
    return parser.parse_args()


def resolve_api_key(args: argparse.Namespace) -> str:
    for value in (
        args.api_key,
        os.environ.get("OPENAI_ROUTING_UI_API_KEY", ""),
        os.environ.get("OPENAI_COMPAT_SERVICE_API_KEY", ""),
        os.environ.get("OPENAI_ROUTING_LOADTEST_API_KEY", ""),
    ):
        value = value.strip()
        if value:
            return value
    raise SystemExit("API key is required via --api-key, OPENAI_ROUTING_UI_API_KEY, or OPENAI_COMPAT_SERVICE_API_KEY")


def post_json(url: str, api_key: str, payload: dict[str, Any], timeout: int) -> tuple[int, dict[str, Any], str]:
    req = urllib.request.Request(
        url,
        data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", "replace")
            status_code = resp.status
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")
        status_code = exc.code

    try:
        obj = json.loads(body)
    except json.JSONDecodeError as exc:
        raise E2EFailure(f"{url} returned non-JSON HTTP {status_code}: {body[:300]}") from exc

    return status_code, obj, body


def response_input(prompt: str) -> list[dict[str, Any]]:
    return [
        {
            "type": "message",
            "role": "user",
            "content": [{"type": "input_text", "text": prompt}],
        }
    ]


def extract_response_text(obj: dict[str, Any]) -> str:
    if isinstance(obj.get("output_text"), str):
        return obj["output_text"]

    parts: list[str] = []
    for item in obj.get("output") or []:
        if not isinstance(item, dict):
            continue
        for content in item.get("content") or []:
            if not isinstance(content, dict):
                continue
            text = content.get("text")
            if isinstance(text, str):
                parts.append(text)
            output_text = content.get("output_text")
            if isinstance(output_text, str):
                parts.append(output_text)
    return "\n".join(parts)


def require_http_ok(name: str, status_code: int, obj: dict[str, Any]) -> None:
    if status_code >= 400:
        error = obj.get("error") if isinstance(obj, dict) else None
        raise E2EFailure(f"{name} failed with HTTP {status_code}: {error or obj}")


def run_text_model(base_url: str, api_key: str, model: str, timeout: int) -> dict[str, Any]:
    token_model = re.sub(r"[^A-Za-z0-9]+", "_", model).strip("_").upper()
    nonce = f"SUB2API_E2E_{token_model}_{int(time.time())}"
    payload = {
        "model": model,
        "input": response_input(f"Reply with exactly this token and no other text: {nonce}"),
        "stream": False,
    }
    status_code, obj, _ = post_json(f"{base_url}/v1/responses", api_key, payload, timeout)
    require_http_ok(f"responses:{model}", status_code, obj)

    response_status = obj.get("status")
    if response_status and response_status != "completed":
        raise E2EFailure(f"responses:{model} returned status={response_status!r}")

    text = extract_response_text(obj)
    if nonce not in text:
        raise E2EFailure(f"responses:{model} did not echo nonce {nonce!r}; text={text[:300]!r}")

    return {
        "check": f"responses:{model}",
        "http": status_code,
        "status": response_status,
        "text": text.strip()[:160],
    }


def extract_image_result(obj: dict[str, Any]) -> str:
    for item in obj.get("output") or []:
        if isinstance(item, dict) and item.get("type") == "image_generation_call" and item.get("result"):
            return str(item["result"])
    for item in obj.get("data") or []:
        if isinstance(item, dict) and item.get("b64_json"):
            return str(item["b64_json"])
    return ""


def looks_like_image(raw: bytes) -> bool:
    return (
        raw.startswith(b"\x89PNG\r\n\x1a\n")
        or raw.startswith(b"\xff\xd8\xff")
        or (len(raw) >= 12 and raw[:4] == b"RIFF" and raw[8:12] == b"WEBP")
    )


def run_image_generation(base_url: str, api_key: str, args: argparse.Namespace) -> dict[str, Any]:
    tool: dict[str, Any] = {
        "type": "image_generation",
        "model": args.image_model,
        "size": args.image_size,
        "output_format": args.image_output_format,
        "quality": args.image_quality,
        "background": args.image_background,
    }
    payload = {
        "model": args.image_text_model,
        "input": response_input(args.image_prompt),
        "tool_choice": {"type": "image_generation"},
        "tools": [tool],
        "stream": False,
    }
    status_code, obj, _ = post_json(f"{base_url}/v1/responses", api_key, payload, args.timeout)
    require_http_ok(f"image:{args.image_model}", status_code, obj)

    response_status = obj.get("status")
    if response_status and response_status != "completed":
        raise E2EFailure(f"image:{args.image_model} returned status={response_status!r}")

    image_b64 = extract_image_result(obj)
    if not image_b64:
        raise E2EFailure(f"image:{args.image_model} returned no image_generation result")

    try:
        raw = base64.b64decode(image_b64, validate=True)
    except ValueError as exc:
        raise E2EFailure(f"image:{args.image_model} returned invalid base64") from exc

    if len(raw) < 256 or not looks_like_image(raw):
        raise E2EFailure(f"image:{args.image_model} returned non-image payload, bytes={len(raw)}")

    output_file = Path(args.image_output_file)
    output_file.parent.mkdir(parents=True, exist_ok=True)
    output_file.write_bytes(raw)

    return {
        "check": f"image:{args.image_model}",
        "http": status_code,
        "status": response_status,
        "bytes": len(raw),
        "output_file": str(output_file),
    }


def run_embeddings(base_url: str, api_key: str, model: str, timeout: int) -> dict[str, Any]:
    payload = {
        "model": model,
        "input": ["sub2api embeddings e2e", "verify vector payload"],
        "encoding_format": "float",
    }
    status_code, obj, _ = post_json(f"{base_url}/v1/embeddings", api_key, payload, timeout)
    require_http_ok(f"embeddings:{model}", status_code, obj)

    if obj.get("object") != "list":
        raise E2EFailure(f"embeddings:{model} returned object={obj.get('object')!r}")

    data = obj.get("data")
    if not isinstance(data, list) or not data:
        raise E2EFailure(f"embeddings:{model} returned empty data")

    embedding = data[0].get("embedding") if isinstance(data[0], dict) else None
    if not isinstance(embedding, list) or not embedding:
        raise E2EFailure(f"embeddings:{model} returned empty embedding vector")
    if not all(isinstance(value, (int, float)) for value in embedding[:8]):
        raise E2EFailure(f"embeddings:{model} returned non-numeric embedding values")

    return {
        "check": f"embeddings:{model}",
        "http": status_code,
        "object": obj.get("object"),
        "vectors": len(data),
        "dimensions": len(embedding),
    }


def main() -> int:
    args = parse_args()
    api_key = resolve_api_key(args)
    base_url = args.base_url.rstrip("/")
    text_models = args.text_models or list(DEFAULT_TEXT_MODELS)

    checks: list[dict[str, Any]] = []
    failures: list[str] = []

    work: list[tuple[str, Any]] = []
    if not args.skip_text:
        work.extend((f"responses:{model}", lambda model=model: run_text_model(base_url, api_key, model, args.timeout)) for model in text_models)
    if not args.skip_image:
        work.append((f"image:{args.image_model}", lambda: run_image_generation(base_url, api_key, args)))
    if not args.skip_embeddings:
        work.append((f"embeddings:{args.embeddings_model}", lambda: run_embeddings(base_url, api_key, args.embeddings_model, args.timeout)))

    for name, fn in work:
        print(f"[e2e] running {name}", file=sys.stderr)
        try:
            result = fn()
        except Exception as exc:
            failures.append(f"{name}: {exc}")
            print(f"[e2e] FAIL {name}: {exc}", file=sys.stderr)
            continue
        checks.append(result)
        print(f"[e2e] PASS {name}", file=sys.stderr)

    summary = {
        "base_url": base_url,
        "passed": len(checks),
        "failed": len(failures),
        "checks": checks,
        "failures": failures,
    }

    if args.report_file:
        Path(args.report_file).write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))

    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
