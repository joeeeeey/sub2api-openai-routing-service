#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import json
import math
import os
import statistics
import sys
import time
from pathlib import Path
from typing import Any

import aiohttp
import tiktoken


SYSTEM_PROMPTS = [
    "You are a concise backend reliability assistant. Answer in one short sentence.",
    "You are a senior platform engineer. Answer in one short sentence.",
    "You are a pragmatic incident responder. Answer in one short sentence.",
    "You are a distributed systems reviewer. Answer in one short sentence.",
    "You are an API design assistant. Answer in one short sentence.",
    "You are a production debugging assistant. Answer in one short sentence.",
    "You are a cost optimization assistant. Answer in one short sentence.",
    "You are a safe rollout advisor. Answer in one short sentence.",
    "You are a capacity planning assistant. Answer in one short sentence.",
    "You are a concise technical writing assistant. Answer in one short sentence.",
]

USER_PROMPTS = [
    "Summarize the impact of a slow upstream dependency on application latency.",
    "Explain how to reduce noisy retry behavior during partial outages.",
    "Give one practical suggestion to improve queue processing stability.",
    "Summarize the tradeoff between throughput and response quality.",
    "Describe one safe way to roll out a new gateway policy.",
    "Summarize why observability data should include request IDs.",
    "Explain one reason to separate billing from request admission checks.",
    "Describe one benefit of deterministic fallback routing.",
    "Summarize why stream responses are useful for user experience.",
    "Explain one reason to preserve simple operational runbooks.",
]

FILLER_SENTENCES = [
    "Use precise operational wording and avoid unnecessary variation.",
    "Focus on production behavior, request flow, and observable outcomes.",
    "Keep the answer short, stable, and technically grounded.",
    "Assume this is a real system with concurrency, retries, and fallbacks.",
    "Prefer concrete reasoning over abstract motivational language.",
    "Assume this request is part of a controlled engineering validation run.",
    "Use clean technical language suitable for infrastructure teams.",
    "Do not mention this is a benchmark or a test unless explicitly asked.",
    "Avoid tool use and avoid structured output formats.",
    "Respond as if the reader is already familiar with modern LLM systems.",
]


def percentile(values: list[float], p: float) -> float | None:
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    rank = (len(ordered) - 1) * p
    lower = math.floor(rank)
    upper = math.ceil(rank)
    if lower == upper:
        return ordered[lower]
    weight = rank - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def build_prompt(enc: tiktoken.Encoding, family_idx: int, target_tokens: int) -> tuple[str, str, int]:
    system_prompt = SYSTEM_PROMPTS[family_idx % len(SYSTEM_PROMPTS)]
    user_prompt = USER_PROMPTS[family_idx % len(USER_PROMPTS)]
    filler = FILLER_SENTENCES[family_idx % len(FILLER_SENTENCES)]

    while True:
        actual_tokens = len(enc.encode(system_prompt)) + len(enc.encode(user_prompt))
        if actual_tokens >= target_tokens:
            return system_prompt, user_prompt, actual_tokens
        user_prompt += " " + filler


async def execute_request(
    session: aiohttp.ClientSession,
    *,
    url: str,
    api_key: str,
    model: str,
    family_idx: int,
    target_tokens: int,
    request_idx: int,
    enc: tiktoken.Encoding,
    request_timeout_seconds: int,
) -> dict[str, Any]:
    system_prompt, user_prompt, actual_tokens = build_prompt(enc, family_idx, target_tokens)
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "reasoning_effort": "low",
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
    }

    started = time.perf_counter()
    first_content_at_ms: float | None = None
    done = False
    status_code: int | None = None
    error_message: str | None = None
    upstream_request_id: str | None = None

    try:
        timeout = aiohttp.ClientTimeout(total=request_timeout_seconds)
        async with session.post(url, json=payload, headers=headers, timeout=timeout) as resp:
            status_code = resp.status
            upstream_request_id = resp.headers.get("x-request-id")
            if resp.status != 200:
                error_message = f"HTTP {resp.status}: {(await resp.text())[:500]}"
            else:
                buffer = ""
                async for chunk in resp.content.iter_chunked(4096):
                    buffer += chunk.decode("utf-8", "replace")
                    while "\n\n" in buffer:
                        event_block, buffer = buffer.split("\n\n", 1)
                        if not event_block.strip():
                            continue
                        for line in event_block.splitlines():
                            if not line.startswith("data: "):
                                continue
                            data = line[6:]
                            if data == "[DONE]":
                                done = True
                                break
                            try:
                                payload_obj = json.loads(data)
                            except json.JSONDecodeError:
                                continue
                            for choice in payload_obj.get("choices", []) or []:
                                delta = choice.get("delta") or {}
                                content = delta.get("content")
                                if content and first_content_at_ms is None:
                                    first_content_at_ms = (time.perf_counter() - started) * 1000.0
                        if done:
                            break
                    if done:
                        break
                if status_code == 200 and not done and error_message is None:
                    if first_content_at_ms is None:
                        error_message = "stream_timeout_no_first_token"
                    else:
                        error_message = "stream_timeout_no_done"
    except Exception as exc:  # noqa: BLE001
        error_message = repr(exc)

    total_ms = (time.perf_counter() - started) * 1000.0
    return {
        "request_index": request_idx,
        "model": model,
        "prompt_family": family_idx,
        "target_input_tokens": target_tokens,
        "actual_input_tokens": actual_tokens,
        "status_code": status_code,
        "success": bool(status_code == 200 and done),
        "ttft_ms": round(first_content_at_ms, 3) if first_content_at_ms is not None else None,
        "total_ms": round(total_ms, 3),
        "done_seen": done,
        "upstream_request_id": upstream_request_id,
        "error": error_message,
    }


def build_summary(config: dict[str, Any], per_request: list[dict[str, Any]], started_at: float, finished_at: float) -> dict[str, Any]:
    per_model: dict[str, list[dict[str, Any]]] = {}
    for item in per_request:
        per_model.setdefault(item["model"], []).append(item)

    model_summaries: dict[str, Any] = {}
    for model, rows in per_model.items():
        success_rows = [r for r in rows if r["success"]]
        ttft_values = [float(r["ttft_ms"]) for r in rows if r["ttft_ms"] is not None]
        total_values = [float(r["total_ms"]) for r in rows]
        model_summaries[model] = {
            "total_requests": len(rows),
            "completed_requests": len(success_rows),
            "completion_success_rate": round(len(success_rows) / len(rows), 4) if rows else 0.0,
            "ttft_available_count": len(ttft_values),
            "ttft_success_rate": round(len(ttft_values) / len(rows), 4) if rows else 0.0,
            "ttft_ms": {
                "min": min(ttft_values) if ttft_values else None,
                "avg": round(statistics.mean(ttft_values), 3) if ttft_values else None,
                "p50": round(percentile(ttft_values, 0.50), 3) if ttft_values else None,
                "p90": round(percentile(ttft_values, 0.90), 3) if ttft_values else None,
                "p95": round(percentile(ttft_values, 0.95), 3) if ttft_values else None,
                "p99": round(percentile(ttft_values, 0.99), 3) if ttft_values else None,
                "max": max(ttft_values) if ttft_values else None,
            },
            "total_latency_ms": {
                "avg": round(statistics.mean(total_values), 3) if total_values else None,
                "p95": round(percentile(total_values, 0.95), 3) if total_values else None,
                "max": max(total_values) if total_values else None,
            },
        }

    return {
        "config": config,
        "started_at_epoch": started_at,
        "finished_at_epoch": finished_at,
        "duration_seconds": round(finished_at - started_at, 3),
        "model_summaries": model_summaries,
    }


def summary_markdown(summary: dict[str, Any]) -> str:
    lines = []
    cfg = summary["config"]
    lines.append("# OpenAI Routing Load Test Report")
    lines.append("")
    lines.append(f"- requests_per_model: `{cfg['requests_per_model']}`")
    lines.append(f"- target_input_tokens: `{cfg['target_input_tokens']}`")
    lines.append(f"- concurrency: `{cfg['concurrency']}`")
    lines.append(f"- models: `{', '.join(cfg['models'])}`")
    lines.append(f"- duration_seconds: `{summary['duration_seconds']}`")
    lines.append("")
    lines.append("| Model | Total | Completed | Success Rate | TTFT p50 | TTFT p95 | TTFT p99 | Avg TTFT | Avg Total | Max Total |")
    lines.append("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for model, item in summary["model_summaries"].items():
        lines.append(
            "| {model} | {total} | {completed} | {rate:.2%} | {p50} | {p95} | {p99} | {avg_ttft} | {avg_total} | {max_total} |".format(
                model=model,
                total=item["total_requests"],
                completed=item["completed_requests"],
                rate=item["completion_success_rate"],
                p50=item["ttft_ms"]["p50"],
                p95=item["ttft_ms"]["p95"],
                p99=item["ttft_ms"]["p99"],
                avg_ttft=item["ttft_ms"]["avg"],
                avg_total=item["total_latency_ms"]["avg"],
                max_total=item["total_latency_ms"]["max"],
            )
        )
    lines.append("")
    return "\n".join(lines) + "\n"


async def main() -> int:
    if len(sys.argv) != 3:
        print("usage: openai_routing_loadtest_runner.py <config.json> <output-dir>", file=sys.stderr)
        return 2

    config_path = Path(sys.argv[1])
    output_dir = Path(sys.argv[2])
    output_dir.mkdir(parents=True, exist_ok=True)
    config = json.loads(config_path.read_text())
    api_key = os.environ.get("OPENAI_ROUTING_API_KEY", "").strip()
    if not api_key:
        print("OPENAI_ROUTING_API_KEY is required", file=sys.stderr)
        return 2

    enc = tiktoken.get_encoding("cl100k_base")
    all_results: list[dict[str, Any]] = []
    started_at = time.time()

    connector = aiohttp.TCPConnector(limit=config["concurrency"])
    async with aiohttp.ClientSession(connector=connector) as session:
        for model in config["models"]:
            semaphore = asyncio.Semaphore(config["concurrency"])

            async def worker(request_idx: int) -> dict[str, Any]:
                async with semaphore:
                    family_idx = request_idx % 10
                    return await execute_request(
                        session,
                        url=config["target_url"],
                        api_key=api_key,
                        model=model,
                        family_idx=family_idx,
                        target_tokens=config["target_input_tokens"],
                        request_idx=request_idx,
                        enc=enc,
                        request_timeout_seconds=config["request_timeout_seconds"],
                    )

            tasks = [worker(i) for i in range(config["requests_per_model"])]
            model_results = await asyncio.gather(*tasks)
            all_results.extend(model_results)

    finished_at = time.time()
    summary = build_summary(config, all_results, started_at, finished_at)

    (output_dir / "config.resolved.json").write_text(json.dumps(config, indent=2, ensure_ascii=False) + "\n")
    (output_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")
    (output_dir / "summary.md").write_text(summary_markdown(summary))
    with (output_dir / "requests.jsonl").open("w", encoding="utf-8") as fp:
        for row in all_results:
            fp.write(json.dumps(row, ensure_ascii=False) + "\n")

    print(summary_markdown(summary))
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
