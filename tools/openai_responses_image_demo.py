#!/usr/bin/env python3
import argparse
import base64
import json
import mimetypes
import sys
import urllib.error
import urllib.request
from pathlib import Path


def file_to_data_url(path: str) -> str:
    file_path = Path(path)
    raw = file_path.read_bytes()
    mime_type, _ = mimetypes.guess_type(file_path.name)
    if not mime_type:
        mime_type = "application/octet-stream"
    return f"data:{mime_type};base64,{base64.b64encode(raw).decode()}"


def build_payload(args: argparse.Namespace) -> dict:
    content = [{"type": "input_text", "text": args.prompt}]
    for ref in args.ref:
        content.append({"type": "input_image", "image_url": file_to_data_url(ref)})

    tool = {
        "type": "image_generation",
        "model": args.image_model,
    }
    if args.size:
        tool["size"] = args.size
    if args.output_format:
        tool["output_format"] = args.output_format
    if args.quality:
        tool["quality"] = args.quality
    if args.background:
        tool["background"] = args.background
    if args.style:
        tool["style"] = args.style
    if args.moderation:
        tool["moderation"] = args.moderation
    if args.output_compression is not None:
        tool["output_compression"] = args.output_compression
    if args.partial_images is not None:
        tool["partial_images"] = args.partial_images

    return {
        "model": args.model,
        "input": [
            {
                "type": "message",
                "role": "user",
                "content": content,
            }
        ],
        "tool_choice": {"type": "image_generation"},
        "tools": [tool],
        "stream": args.stream,
    }


def extract_image_items(response_obj: dict) -> list[dict]:
    items = []
    for item in response_obj.get("output") or []:
        if item.get("type") == "image_generation_call" and item.get("result"):
            items.append(item)
    if items:
        return items
    data = response_obj.get("data") or []
    for item in data:
        if item.get("b64_json"):
            items.append(item)
    return items


def main() -> int:
    parser = argparse.ArgumentParser(description="Call /v1/responses image_generation and optionally save the image.")
    parser.add_argument("--api-url", default="http://127.0.0.1:8080/openai-routing/v1/responses")
    parser.add_argument("--api-key", required=True)
    parser.add_argument("--model", default="gpt-5.4-mini")
    parser.add_argument("--image-model", default="gpt-image-2")
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--ref", action="append", default=[], help="Reference image filepath; can be passed multiple times.")
    parser.add_argument("--size", default="1024x1024")
    parser.add_argument("--output-format", default="png")
    parser.add_argument("--quality", default="high")
    parser.add_argument("--background", default="auto")
    parser.add_argument("--style", default="")
    parser.add_argument("--moderation", default="")
    parser.add_argument("--output-compression", type=int, default=None)
    parser.add_argument("--partial-images", type=int, default=None)
    parser.add_argument("--stream", action="store_true")
    parser.add_argument("--response-file", default="")
    parser.add_argument("--output-file", default="")
    args = parser.parse_args()

    payload = build_payload(args)
    req = urllib.request.Request(
        args.api_url,
        data=json.dumps(payload).encode(),
        headers={
            "Authorization": f"Bearer {args.api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=300) as resp:
            body = resp.read().decode("utf-8")
            status_code = resp.status
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")
        status_code = exc.code

    if args.response_file:
        Path(args.response_file).write_text(body)

    try:
        response_obj = json.loads(body)
    except json.JSONDecodeError:
        print(body)
        return 1

    if status_code >= 400:
        print(json.dumps({"http": status_code, "error": response_obj.get("error"), "raw": response_obj}, ensure_ascii=False))
        return 1

    output = response_obj.get("output") or []
    print(json.dumps({
        "http": status_code,
        "status": response_obj.get("status"),
        "output_count": len(output),
        "outputs": [
            {
                "index": idx,
                "type": item.get("type"),
                "result_len": len(item.get("result", "") or ""),
                "revised_prompt": item.get("revised_prompt"),
            }
            for idx, item in enumerate(output)
        ],
    }, ensure_ascii=False))

    if args.output_file:
        image_items = extract_image_items(response_obj)
        if not image_items:
            raise SystemExit(f"No image payload found in response JSON")

        saved = []
        out_path = Path(args.output_file)
        for idx, item in enumerate(image_items, start=1):
            image_b64 = item.get("result") or item.get("b64_json")
            raw = base64.b64decode(image_b64)
            if len(image_items) == 1:
                target = out_path
            else:
                target = out_path.with_name(f"{out_path.stem}.{idx}{out_path.suffix}")
            target.write_bytes(raw)
            saved.append({
                "path": str(target),
                "bytes": len(raw),
                "revised_prompt": item.get("revised_prompt"),
            })

        print(json.dumps({"saved_images": saved}, ensure_ascii=False))
    else:
        print(json.dumps(response_obj, ensure_ascii=False))

    return 0


if __name__ == "__main__":
    sys.exit(main())
