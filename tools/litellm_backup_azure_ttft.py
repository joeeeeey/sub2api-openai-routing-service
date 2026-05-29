#!/usr/bin/env python3
from __future__ import annotations

import argparse
import asyncio
import getpass
import json
import os
import statistics
import sys
import time
from pathlib import Path
from typing import Any

import aiohttp
import tiktoken

from openai_routing_loadtest_runner import build_prompt, percentile


DEFAULT_BASE_URL = "https://dev-litellm.frai.pro/"
DEFAULT_REASONING_EFFORT = "low"


def resolve_backup_model(model: str) -> str:
    model = model.strip()
    if model.startswith("backup/azure/"):
        return model
    if model.startswith("azure/"):
        return "backup/" + model
    if model.startswith("gpt-"):
        return "backup/azure/" + model
    raise SystemExit(f"Cannot resolve backup alias for model: {model}")


def normalize_target_url(raw: str) -> str:
    raw = raw.strip()
    if not raw:
        raw = DEFAULT_BASE_URL
    if raw.endswith("/chat/completions"):
        return raw
    return raw.rstrip("/") + "/chat/completions"


async def execute_request(
    session: aiohttp.ClientSession,
    *,
    url: str,
    api_key: str,
    requested_model: str,
    resolved_model: str,
    family_idx: int,
    target_tokens: int,
    request_idx: int,
    enc: tiktoken.Encoding,
    request_timeout_seconds: int,
    reasoning_effort: str,
    user_prompt_override: str | None,
) -> dict[str, Any]:
    system_prompt, user_prompt, actual_tokens = build_prompt(
        enc,
        family_idx,
        target_tokens,
        user_prompt_override=user_prompt_override,
    )
    payload = {
        "model": resolved_model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "reasoning_effort": reasoning_effort,
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
    response_parts: list[str] = []

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
                                if content:
                                    response_parts.append(content)
                                    if first_content_at_ms is None:
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
        "requested_model": requested_model,
        "resolved_model": resolved_model,
        "prompt_family": family_idx,
        "target_input_tokens": target_tokens,
        "actual_input_tokens": actual_tokens,
        "reasoning_effort": reasoning_effort,
        "status_code": status_code,
        "success": bool(status_code == 200 and done),
        "ttft_ms": round(first_content_at_ms, 3) if first_content_at_ms is not None else None,
        "total_ms": round(total_ms, 3),
        "done_seen": done,
        "upstream_request_id": upstream_request_id,
        "error": error_message,
        "response_text": "".join(response_parts),
    }


def build_summary(config: dict[str, Any], rows: list[dict[str, Any]], started_at: float, finished_at: float) -> dict[str, Any]:
    success_rows = [r for r in rows if r["success"]]
    ttft_values = [float(r["ttft_ms"]) for r in rows if r["ttft_ms"] is not None]
    total_values = [float(r["total_ms"]) for r in rows]
    return {
        "config": config,
        "started_at_epoch": started_at,
        "finished_at_epoch": finished_at,
        "duration_seconds": round(finished_at - started_at, 3),
        "total_requests": len(rows),
        "completed_requests": len(success_rows),
        "completion_success_rate": round(len(success_rows) / len(rows), 4) if rows else 0.0,
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


def summary_markdown(summary: dict[str, Any]) -> str:
    cfg = summary["config"]
    return (
        "# LiteLLM Backup Azure TTFT Report\n\n"
        f"- requested_model: `{cfg['requested_model']}`\n"
        f"- resolved_model: `{cfg['resolved_model']}`\n"
        f"- requests: `{cfg['requests']}`\n"
        f"- target_input_tokens: `{cfg['target_input_tokens']}`\n"
        f"- concurrency: `{cfg['concurrency']}`\n"
        f"- reasoning_effort: `{cfg['reasoning_effort']}`\n"
        f"- user_prompt_override: `{cfg['user_prompt'] or '<default prompt families>'}`\n"
        f"- duration_seconds: `{summary['duration_seconds']}`\n\n"
        "| Metric | Value |\n"
        "|---|---:|\n"
        f"| completed_requests | {summary['completed_requests']} |\n"
        f"| completion_success_rate | {summary['completion_success_rate']:.2%} |\n"
        f"| ttft_p50_ms | {summary['ttft_ms']['p50']} |\n"
        f"| ttft_p95_ms | {summary['ttft_ms']['p95']} |\n"
        f"| ttft_p99_ms | {summary['ttft_ms']['p99']} |\n"
        f"| avg_ttft_ms | {summary['ttft_ms']['avg']} |\n"
        f"| avg_total_latency_ms | {summary['total_latency_ms']['avg']} |\n"
        f"| max_total_latency_ms | {summary['total_latency_ms']['max']} |\n"
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Measure TTFT for direct LiteLLM backup/azure model paths.")
    parser.add_argument("--model", required=False)
    parser.add_argument("--requests", type=int)
    parser.add_argument("--token-target", type=int)
    parser.add_argument("--concurrency", type=int)
    parser.add_argument("--base-url")
    parser.add_argument("--target-url")
    parser.add_argument("--user-prompt")
    parser.add_argument("--reasoning-effort")
    parser.add_argument("--request-timeout-seconds", type=int, default=1800)
    parser.add_argument("--report-dir")
    return parser.parse_args()


def prompt_with_default(label: str, default: str) -> str:
    value = input(f"{label} [{default}]: ").strip()
    return value or default


def ensure_inputs(args: argparse.Namespace) -> dict[str, Any]:
    interactive = sys.stdin.isatty()
    model = args.model
    if not model:
        if not interactive:
            raise SystemExit("--model is required in non-interactive mode")
        model = prompt_with_default("Model", "gpt-5.2")
    requests = args.requests
    if requests is None:
        if not interactive:
            raise SystemExit("--requests is required in non-interactive mode")
        requests = int(prompt_with_default("Requests", "20"))
    token_target = args.token_target
    if token_target is None:
        if not interactive:
            raise SystemExit("--token-target is required in non-interactive mode")
        token_target = int(prompt_with_default("Approx input tokens per request", "100"))
    concurrency = args.concurrency
    if concurrency is None:
        if not interactive:
            raise SystemExit("--concurrency is required in non-interactive mode")
        concurrency = int(prompt_with_default("Concurrency", "1"))

    reasoning_effort = (args.reasoning_effort or "").strip().lower()
    if not reasoning_effort:
        if interactive:
            reasoning_effort = prompt_with_default("Reasoning effort", DEFAULT_REASONING_EFFORT).strip().lower()
        else:
            reasoning_effort = DEFAULT_REASONING_EFFORT
    if reasoning_effort not in {"none", "low", "medium", "high", "xhigh"}:
        raise SystemExit("--reasoning-effort must be one of: none, low, medium, high, xhigh")

    user_prompt = (args.user_prompt or "").strip()
    if interactive and not user_prompt:
        user_prompt = input("Custom user prompt (optional): ").strip()

    target_url = args.target_url
    if not target_url:
        base_url = args.base_url
        if not base_url:
            if interactive:
                base_url = prompt_with_default("LiteLLM base URL", DEFAULT_BASE_URL)
            else:
                base_url = DEFAULT_BASE_URL
        target_url = normalize_target_url(base_url)
    else:
        target_url = normalize_target_url(target_url)

    api_key = (
        os.environ.get("LITELLM_API_KEY", "").strip()
        or os.environ.get("LITELLM_BACKUP_TTFT_API_KEY", "").strip()
    )
    if not api_key:
        if not interactive:
            raise SystemExit("LITELLM_API_KEY (or LITELLM_BACKUP_TTFT_API_KEY) is required in non-interactive mode")
        api_key = getpass.getpass("LiteLLM API key (input hidden): ").strip()
    if not api_key:
        raise SystemExit("API key is required")

    resolved_model = resolve_backup_model(model)
    return {
        "requested_model": model,
        "resolved_model": resolved_model,
        "requests": requests,
        "target_input_tokens": token_target,
        "concurrency": concurrency,
        "reasoning_effort": reasoning_effort,
        "user_prompt": user_prompt,
        "target_url": target_url,
        "request_timeout_seconds": args.request_timeout_seconds,
        "api_key": api_key,
    }


def create_report_dir(args: argparse.Namespace, requested_model: str, requests: int, token_target: int, concurrency: int) -> Path:
    if args.report_dir:
        report_dir = Path(args.report_dir).expanduser().resolve()
    else:
        stamp = time.strftime("%Y%m%d-%H%M%S")
        report_dir = Path(".loadtest-reports") / f"{stamp}-backup-azure-{requested_model.replace('/', '_')}-r{requests}-t{token_target}-c{concurrency}"
    report_dir.mkdir(parents=True, exist_ok=True)
    return report_dir


async def main_async() -> int:
    args = parse_args()
    values = ensure_inputs(args)
    api_key = values.pop("api_key")
    report_dir = create_report_dir(args, values["requested_model"], values["requests"], values["target_input_tokens"], values["concurrency"])
    enc = tiktoken.get_encoding("cl100k_base")

    connector = aiohttp.TCPConnector(limit=int(values["concurrency"]))
    started_at = time.time()
    async with aiohttp.ClientSession(connector=connector) as session:
        semaphore = asyncio.Semaphore(int(values["concurrency"]))

        async def worker(request_idx: int) -> dict[str, Any]:
            async with semaphore:
                return await execute_request(
                    session,
                    url=str(values["target_url"]),
                    api_key=str(api_key),
                    requested_model=str(values["requested_model"]),
                    resolved_model=str(values["resolved_model"]),
                    family_idx=request_idx % 10,
                    target_tokens=int(values["target_input_tokens"]),
                    request_idx=request_idx,
                    enc=enc,
                    request_timeout_seconds=int(values["request_timeout_seconds"]),
                    reasoning_effort=str(values["reasoning_effort"]),
                    user_prompt_override=(str(values["user_prompt"]) if values["user_prompt"] else None),
                )

        rows = await asyncio.gather(*(worker(i) for i in range(int(values["requests"]))))
    finished_at = time.time()

    summary = build_summary(values, rows, started_at, finished_at)
    (report_dir / "config.json").write_text(json.dumps(values, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (report_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (report_dir / "summary.md").write_text(summary_markdown(summary), encoding="utf-8")
    with (report_dir / "requests.jsonl").open("w", encoding="utf-8") as fp:
        for row in rows:
            fp.write(json.dumps(row, ensure_ascii=False) + "\n")

    print(summary_markdown(summary))
    print(f"\nReport directory: {report_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main_async()))
