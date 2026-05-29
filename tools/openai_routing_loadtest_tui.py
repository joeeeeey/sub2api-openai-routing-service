#!/usr/bin/env python3
from __future__ import annotations

import argparse
import asyncio
import getpass
import json
import math
import os
import statistics
import sys
import textwrap
import time
from collections import Counter
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import aiohttp
import tiktoken

try:
    from rich.console import Console, Group
    from rich.layout import Layout
    from rich.live import Live
    from rich.panel import Panel
    from rich.text import Text
except ImportError as exc:  # pragma: no cover - runtime guard
    print(
        "Missing dependency 'rich'. Install with:\n"
        "  python3 -m pip install --user rich aiohttp tiktoken",
        file=sys.stderr,
    )
    raise SystemExit(2) from exc


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

CUSTOM_USER_SYSTEM_PROMPT = "You are a helpful assistant. Answer clearly in English."

DEFAULT_TARGET_URL = "https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions"
WORKER_COLORS = ["cyan", "magenta", "green", "yellow", "blue", "bright_cyan", "bright_magenta", "bright_green"]


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


def truncate_text(text: str, width: int) -> str:
    text = " ".join(text.split())
    if len(text) <= width:
        return text
    return text[: max(0, width - 1)] + "…"


def resolve_backup_model(model: str) -> str:
    model = model.strip()
    if model.startswith("backup/azure/"):
        return model
    if model.startswith("azure/"):
        return "backup/" + model
    if model.startswith("gpt-"):
        return "backup/azure/" + model
    raise SystemExit(f"Cannot resolve backup alias for model: {model}")


def build_prompt(
    enc: tiktoken.Encoding,
    family_idx: int,
    target_tokens: int,
    *,
    user_prompt_override: str | None = None,
) -> tuple[str, str, int]:
    if user_prompt_override:
        system_prompt = CUSTOM_USER_SYSTEM_PROMPT
        user_prompt = user_prompt_override.strip()
    else:
        system_prompt = SYSTEM_PROMPTS[family_idx % len(SYSTEM_PROMPTS)]
        user_prompt = USER_PROMPTS[family_idx % len(USER_PROMPTS)]
    filler = FILLER_SENTENCES[family_idx % len(FILLER_SENTENCES)]

    while True:
        actual_tokens = len(enc.encode(system_prompt)) + len(enc.encode(user_prompt))
        if actual_tokens >= target_tokens:
            return system_prompt, user_prompt, actual_tokens
        user_prompt += " " + filler


@dataclass
class WorkerView:
    worker_id: int
    request_index: int | None = None
    model: str = ""
    family_idx: int | None = None
    system_prompt: str = ""
    user_prompt: str = ""
    started_at: float | None = None
    first_text_at: float | None = None
    done: bool = False
    status_code: int | None = None
    state: str = "idle"
    response_text: str = ""
    error: str | None = None
    total_ms: float | None = None
    upstream_request_id: str | None = None


class LiveDashboard:
    def __init__(self, total_requests: int, concurrency: int, model: str, title: str):
        self.console = Console()
        self.total_requests = total_requests
        self.concurrency = concurrency
        self.model = model
        self.title = title
        self.started_at = time.time()
        self.completed = 0
        self.success = 0
        self.failed = 0
        self.views = [WorkerView(worker_id=i) for i in range(concurrency)]

    def _timer_label(self, view: WorkerView) -> str:
        if view.started_at is None:
            return "-"
        if view.first_text_at is None:
            return f"{(time.time() - view.started_at) * 1000:.0f}ms"
        return f"{(view.first_text_at - view.started_at) * 1000:.0f}ms"

    def _state_color(self, view: WorkerView) -> str:
        if view.error:
            return "red"
        if view.done:
            return "green"
        if view.state == "streaming":
            return "yellow"
        if view.state in {"requesting", "http_error", "timeout", "exception", "incomplete"}:
            return "cyan"
        return WORKER_COLORS[view.worker_id % len(WORKER_COLORS)]

    def _worker_panel(self, view: WorkerView) -> Panel:
        title = f"Worker {view.worker_id + 1}"
        request_label = f"request {view.request_index + 1}/{self.total_requests}" if view.request_index is not None else "idle"
        status_label = f"status={view.status_code}" if view.status_code is not None else "status=-"
        timer_label = self._timer_label(view)
        subtitle = f"{request_label}  |  {status_label}  |  state={view.state}  |  ttft={timer_label}"

        system_preview = truncate_text(view.system_prompt, 220) if view.system_prompt else "-"
        user_preview = truncate_text(view.user_prompt, 220) if view.user_prompt else "-"
        assistant_text = view.response_text or ""
        assistant_preview = assistant_text if assistant_text else "…waiting for stream…"

        body = Group(
            Text("System", style="bold bright_black"),
            Text(system_preview, style="white"),
            Text(""),
            Text("User", style="bold cyan"),
            Text(user_preview, style="white"),
            Text(""),
            Text("Assistant", style="bold green"),
            Text(assistant_preview, style="white"),
            Text(""),
            Text(f"model={view.model}" if view.model else "", style="dim"),
            Text(f"family={view.family_idx}" if view.family_idx is not None else "", style="dim"),
            Text(f"upstream_request_id={view.upstream_request_id}" if view.upstream_request_id else "", style="dim"),
            Text(f"error={view.error}" if view.error else "", style="bold red"),
        )

        return Panel(
            body,
            title=title,
            subtitle=subtitle,
            border_style=self._state_color(view),
            padding=(1, 2),
        )

    def make_renderable(self) -> Layout:
        elapsed = time.time() - self.started_at
        header = Panel(
            Group(
                Text(self.title, style="bold"),
                Text(
                    f"model={self.model}  total={self.total_requests}  concurrency={self.concurrency}  "
                    f"elapsed={elapsed:.1f}s  completed={self.completed}  success={self.success}  failed={self.failed}",
                    style="white",
                ),
            ),
            border_style="bright_white",
            padding=(0, 1),
        )
        layout = Layout()
        body = Layout(name="body")
        body.split_row(*(Layout(self._worker_panel(view), ratio=1) for view in self.views))
        layout.split_column(
            Layout(header, size=4),
            body,
        )
        return layout


async def render_loop(board: LiveDashboard, stop_event: asyncio.Event) -> None:
    with Live(board.make_renderable(), console=board.console, refresh_per_second=10, screen=True, transient=False) as live:
        while not stop_event.is_set():
            live.update(board.make_renderable())
            await asyncio.sleep(0.1)
        live.update(board.make_renderable())


async def run_request(
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
    view: WorkerView,
    reasoning_effort: str,
    user_prompt_override: str | None,
) -> dict[str, Any]:
    system_prompt, user_prompt, actual_tokens = build_prompt(
        enc,
        family_idx,
        target_tokens,
        user_prompt_override=user_prompt_override,
    )
    view.request_index = request_idx
    view.model = model
    view.family_idx = family_idx
    view.system_prompt = system_prompt
    view.user_prompt = user_prompt
    view.started_at = time.time()
    view.first_text_at = None
    view.done = False
    view.status_code = None
    view.state = "requesting"
    view.response_text = ""
    view.error = None
    view.total_ms = None
    view.upstream_request_id = None

    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    if reasoning_effort:
        payload["reasoning_effort"] = reasoning_effort
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
            view.status_code = status_code
            view.upstream_request_id = upstream_request_id
            if resp.status != 200:
                error_message = f"HTTP {resp.status}: {(await resp.text())[:500]}"
                view.error = error_message
                view.state = "http_error"
            else:
                view.state = "streaming"
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
                                view.done = True
                                view.state = "completed"
                                break
                            try:
                                payload_obj = json.loads(data)
                            except json.JSONDecodeError:
                                continue
                            for choice in payload_obj.get("choices", []) or []:
                                delta = choice.get("delta") or {}
                                content = delta.get("content")
                                if content:
                                    view.response_text += content
                                    if first_content_at_ms is None:
                                        first_content_at_ms = (time.perf_counter() - started) * 1000.0
                                        view.first_text_at = time.time()
                        if done:
                            break
                    if done:
                        break
                if status_code == 200 and not done and error_message is None:
                    if first_content_at_ms is None:
                        error_message = "stream_timeout_no_first_token"
                    else:
                        error_message = "stream_timeout_no_done"
                    view.error = error_message
                    view.state = "timeout"
    except Exception as exc:  # noqa: BLE001
        error_message = repr(exc)
        view.error = error_message
        view.state = "exception"

    total_ms = (time.perf_counter() - started) * 1000.0
    view.total_ms = total_ms
    if not done and error_message is None:
        view.state = "incomplete"
    return {
        "request_index": request_idx,
        "model": model,
        "prompt_family": family_idx,
        "target_input_tokens": target_tokens,
        "actual_input_tokens": actual_tokens,
        "reasoning_effort": reasoning_effort or "",
        "status_code": status_code,
        "success": bool(status_code == 200 and done),
        "ttft_ms": round(first_content_at_ms, 3) if first_content_at_ms is not None else None,
        "total_ms": round(total_ms, 3),
        "done_seen": done,
        "upstream_request_id": upstream_request_id,
        "error": error_message,
        "response_text": view.response_text,
    }


def build_summary(config: dict[str, Any], rows: list[dict[str, Any]], started_at: float, finished_at: float) -> dict[str, Any]:
    success_rows = [r for r in rows if r["success"]]
    ttft_values = [float(r["ttft_ms"]) for r in rows if r["ttft_ms"] is not None]
    total_values = [float(r["total_ms"]) for r in rows]
    error_counter = Counter((r.get("error") or "NONE") for r in rows if not r["success"])
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
        "error_distribution": dict(error_counter),
    }


def summary_markdown(summary: dict[str, Any]) -> str:
    cfg = summary["config"]
    lines = [
        "# OpenAI Routing Load Test TUI Report",
        "",
        f"- requests: `{cfg['requests']}`",
        f"- target_input_tokens: `{cfg['target_input_tokens']}`",
        f"- concurrency: `{cfg['concurrency']}`",
        f"- requested_model: `{cfg.get('requested_model', cfg['model'])}`",
        f"- model: `{cfg['model']}`",
        f"- reasoning_effort: `{cfg['reasoning_effort'] or '<omitted>'}`",
        (
            f"- user_prompt_override: `{cfg['user_prompt']}`"
            if cfg.get("user_prompt")
            else "- user_prompt_override: `<default prompt families>`"
        ),
        f"- duration_seconds: `{summary['duration_seconds']}`",
        f"- completion_success_rate: `{summary['completion_success_rate']:.2%}`",
        "",
        "| Metric | Value |",
        "|---|---:|",
        f"| completed_requests | {summary['completed_requests']} |",
        f"| ttft_p50_ms | {summary['ttft_ms']['p50']} |",
        f"| ttft_p95_ms | {summary['ttft_ms']['p95']} |",
        f"| ttft_p99_ms | {summary['ttft_ms']['p99']} |",
        f"| avg_ttft_ms | {summary['ttft_ms']['avg']} |",
        f"| avg_total_latency_ms | {summary['total_latency_ms']['avg']} |",
        f"| max_total_latency_ms | {summary['total_latency_ms']['max']} |",
        "",
        "## Error Distribution",
        "",
    ]
    if summary["error_distribution"]:
        for key, value in summary["error_distribution"].items():
            lines.append(f"- `{key}`: {value}")
    else:
        lines.append("- none")
    lines.append("")
    return "\n".join(lines)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Local TUI load test for Sub2API OpenAI routing service.")
    parser.add_argument("--requests", type=int)
    parser.add_argument("--token-target", type=int)
    parser.add_argument("--model")
    parser.add_argument("--concurrency", type=int, default=None)
    parser.add_argument("--user-prompt")
    parser.add_argument("--reasoning-effort")
    parser.add_argument("--model-resolver", choices=["none", "backup-azure"], default="none")
    parser.add_argument("--target-url", default=DEFAULT_TARGET_URL)
    parser.add_argument("--request-timeout-seconds", type=int, default=1800)
    parser.add_argument("--report-dir")
    return parser.parse_args()


def prompt_with_default(label: str, default: str) -> str:
    value = input(f"{label} [{default}]: ").strip()
    return value or default


def ensure_inputs(args: argparse.Namespace) -> dict[str, Any]:
    interactive = sys.stdin.isatty()
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
    model = args.model
    if not model:
        if not interactive:
            raise SystemExit("--model is required in non-interactive mode")
        model = prompt_with_default("Model", "gpt-5.3-codex-spark")
    requested_model = model
    if args.model_resolver == "backup-azure":
        model = resolve_backup_model(model)
    concurrency = args.concurrency or 2
    if interactive and args.concurrency is None:
        concurrency = int(prompt_with_default("Concurrency", "2"))

    reasoning_effort = (args.reasoning_effort or "").strip().lower()
    if not reasoning_effort and interactive:
        reasoning_effort = input("Reasoning effort (optional): ").strip().lower()
    if reasoning_effort and reasoning_effort not in {"none", "low", "medium", "high", "xhigh"}:
        raise SystemExit("--reasoning-effort must be one of: none, low, medium, high, xhigh")

    user_prompt = (args.user_prompt or "").strip()
    if interactive and not user_prompt:
        user_prompt = input("Custom user prompt (optional): ").strip()

    api_key = os.environ.get("OPENAI_ROUTING_LOADTEST_API_KEY", "").strip()
    if not api_key:
        if not interactive:
            raise SystemExit("OPENAI_ROUTING_LOADTEST_API_KEY is required in non-interactive mode")
        api_key = getpass.getpass("Sub2API API key (input hidden): ").strip()
    if not api_key:
        raise SystemExit("API key is required")
    return {
        "requests": requests,
        "target_input_tokens": token_target,
        "requested_model": requested_model,
        "model": model,
        "concurrency": concurrency,
        "reasoning_effort": reasoning_effort,
        "user_prompt": user_prompt,
        "model_resolver": args.model_resolver,
        "target_url": args.target_url,
        "request_timeout_seconds": args.request_timeout_seconds,
        "api_key": api_key,
    }


def create_report_dir(args: argparse.Namespace, model: str, requests: int, token_target: int, concurrency: int) -> Path:
    if args.report_dir:
        report_dir = Path(args.report_dir).expanduser().resolve()
    else:
        stamp = time.strftime("%Y%m%d-%H%M%S")
        report_dir = Path(".loadtest-reports") / f"{stamp}-tui-{model.replace('/', '_')}-r{requests}-t{token_target}-c{concurrency}"
    report_dir.mkdir(parents=True, exist_ok=True)
    return report_dir


async def main_async() -> int:
    args = parse_args()
    values = ensure_inputs(args)
    api_key = values.pop("api_key")
    report_dir = create_report_dir(args, values["model"], values["requests"], values["target_input_tokens"], values["concurrency"])
    enc = tiktoken.get_encoding("cl100k_base")

    title_parts = ["OpenAI Chat Load Test TUI"]
    if values.get("model_resolver") == "backup-azure":
        title_parts.append("LiteLLM Backup Azure")
    board = LiveDashboard(
        total_requests=int(values["requests"]),
        concurrency=int(values["concurrency"]),
        model=str(values["model"]),
        title=" | ".join(title_parts),
    )
    stop_event = asyncio.Event()
    render_task = asyncio.create_task(render_loop(board, stop_event))

    started_at = time.time()
    results: list[dict[str, Any]] = []
    queue: asyncio.Queue[int] = asyncio.Queue()
    for i in range(int(values["requests"])):
        queue.put_nowait(i)

    connector = aiohttp.TCPConnector(limit=int(values["concurrency"]))
    async with aiohttp.ClientSession(connector=connector) as session:
        async def worker(worker_idx: int) -> None:
            view = board.views[worker_idx]
            while True:
                try:
                    request_idx = queue.get_nowait()
                except asyncio.QueueEmpty:
                    view.state = "idle"
                    return
                row = await run_request(
                    session,
                    url=str(values["target_url"]),
                    api_key=str(api_key),
                    model=str(values["model"]),
                    family_idx=request_idx % 10,
                    target_tokens=int(values["target_input_tokens"]),
                    request_idx=request_idx,
                    enc=enc,
                    request_timeout_seconds=int(values["request_timeout_seconds"]),
                    view=view,
                    reasoning_effort=str(values["reasoning_effort"]),
                    user_prompt_override=(str(values["user_prompt"]) if values["user_prompt"] else None),
                )
                results.append(row)
                board.completed += 1
                if row["success"]:
                    board.success += 1
                else:
                    board.failed += 1
                await asyncio.sleep(0.75)

        workers = [asyncio.create_task(worker(i)) for i in range(int(values["concurrency"]))]
        await asyncio.gather(*workers)

    finished_at = time.time()
    stop_event.set()
    await render_task

    summary = build_summary(values, results, started_at, finished_at)
    (report_dir / "config.json").write_text(json.dumps(values, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (report_dir / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (report_dir / "summary.md").write_text(summary_markdown(summary), encoding="utf-8")
    with (report_dir / "requests.jsonl").open("w", encoding="utf-8") as fp:
        for row in results:
            fp.write(json.dumps(row, ensure_ascii=False) + "\n")

    print("\n" + summary_markdown(summary))
    print(f"\nReport directory: {report_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main_async()))
