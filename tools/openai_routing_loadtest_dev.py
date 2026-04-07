#!/usr/bin/env python3
from __future__ import annotations

import argparse
import getpass
import json
import os
import shlex
import subprocess
import sys
import tempfile
import textwrap
import time
from pathlib import Path


DEFAULT_MODELS = ["gpt-5.2", "gpt-5.3-codex-spark", "gpt-5.4"]
DEFAULT_CONTEXT = "dev-us2"
DEFAULT_NAMESPACE = "component"
DEFAULT_URL = "http://sub2api-openai-routing-service.component.svc.cluster.local:8080/openai-routing/v1/chat/completions"
DEFAULT_IMAGE = "python:3.11-slim"
DEFAULT_CONCURRENCY = 10
DEFAULT_TIMEOUT_SECONDS = 1800
def run(cmd: list[str], *, input_text: str | None = None, check: bool = True, capture_output: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        input=input_text,
        text=True,
        check=check,
        capture_output=capture_output,
    )


def prompt_with_default(label: str, default: str) -> str:
    value = input(f"{label} [{default}]: ").strip()
    return value or default


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run in-cluster load tests against dev Sub2API OpenAI routing service.")
    parser.add_argument("--requests-per-model", type=int)
    parser.add_argument("--token-target", type=int)
    parser.add_argument("--model-mode", choices=["all", "single"])
    parser.add_argument("--model")
    parser.add_argument("--concurrency", type=int, default=None)
    parser.add_argument("--user-prompt")
    parser.add_argument("--reasoning-effort")
    parser.add_argument("--context", default=DEFAULT_CONTEXT)
    parser.add_argument("--namespace", default=DEFAULT_NAMESPACE)
    parser.add_argument("--target-url", default=DEFAULT_URL)
    parser.add_argument("--image", default=DEFAULT_IMAGE)
    parser.add_argument("--request-timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    parser.add_argument("--keep-resources", action="store_true")
    parser.add_argument("--report-dir")
    return parser.parse_args()


def ensure_inputs(args: argparse.Namespace) -> dict[str, object]:
    interactive = sys.stdin.isatty()

    requests_per_model = args.requests_per_model
    if requests_per_model is None:
        if not interactive:
            raise SystemExit("--requests-per-model is required in non-interactive mode")
        requests_per_model = int(prompt_with_default("Requests per model", "100"))

    token_target = args.token_target
    if token_target is None:
        if not interactive:
            raise SystemExit("--token-target is required in non-interactive mode")
        token_target = int(prompt_with_default("Approx input tokens per request", "100"))

    model_mode = args.model_mode
    if model_mode is None:
        if not interactive:
            raise SystemExit("--model-mode is required in non-interactive mode")
        model_mode = prompt_with_default("Model mode (all/single)", "all")

    if model_mode == "single":
        model = args.model
        if not model:
            if not interactive:
                raise SystemExit("--model is required when --model-mode=single")
            model = prompt_with_default("Model", DEFAULT_MODELS[0])
        models = [model]
    else:
        models = list(DEFAULT_MODELS)

    concurrency = args.concurrency or DEFAULT_CONCURRENCY
    if interactive and args.concurrency is None:
        concurrency = int(prompt_with_default("Concurrency", str(DEFAULT_CONCURRENCY)))

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
        "requests_per_model": requests_per_model,
        "target_input_tokens": token_target,
        "models": models,
        "concurrency": concurrency,
        "reasoning_effort": reasoning_effort,
        "user_prompt": user_prompt,
        "target_url": args.target_url,
        "request_timeout_seconds": args.request_timeout_seconds,
        "api_key": api_key,
    }


def create_report_dir(args: argparse.Namespace, models: list[str], requests_per_model: int, token_target: int) -> Path:
    if args.report_dir:
        report_dir = Path(args.report_dir).expanduser().resolve()
    else:
        stamp = time.strftime("%Y%m%d-%H%M%S")
        model_label = "all-models" if len(models) > 1 else models[0].replace("/", "_")
        report_dir = Path(".loadtest-reports") / f"{stamp}-{model_label}-r{requests_per_model}-t{token_target}"
    report_dir.mkdir(parents=True, exist_ok=True)
    return report_dir


def build_pod_manifest(name: str, namespace: str, image: str, configmap_name: str, secret_name: str) -> str:
    return textwrap.dedent(
        f"""
        apiVersion: v1
        kind: Pod
        metadata:
          name: {name}
          namespace: {namespace}
          labels:
            app.kubernetes.io/name: openai-routing-loadtest
            app.kubernetes.io/instance: {name}
        spec:
          restartPolicy: Never
          containers:
            - name: runner
              image: {image}
              imagePullPolicy: IfNotPresent
              command: ["/bin/sh", "-lc"]
              args:
                - >-
                  code=0;
                  python -m pip install --quiet --no-cache-dir aiohttp tiktoken || code=$?;
                  if [ "$code" -eq 0 ]; then
                    python /runner/runner.py /runner/config.json /output || code=$?;
                  fi;
                  printf "%s" "$code" > /output/exit_code;
                  touch /output/done;
                  sleep 3600;
              env:
                - name: OPENAI_ROUTING_API_KEY
                  valueFrom:
                    secretKeyRef:
                      name: {secret_name}
                      key: api_key
              resources:
                requests:
                  cpu: "250m"
                  memory: "256Mi"
                limits:
                  cpu: "1"
                  memory: "1Gi"
              volumeMounts:
                - name: runner-config
                  mountPath: /runner
                  readOnly: true
                - name: output
                  mountPath: /output
          volumes:
            - name: runner-config
              configMap:
                name: {configmap_name}
            - name: output
              emptyDir: {{}}
        """
    ).strip() + "\n"


def wait_for_runner_done(context: str, namespace: str, pod_name: str, timeout_seconds: int) -> int:
    deadline = time.time() + timeout_seconds
    last_phase = None
    while time.time() < deadline:
        phase = run(
            ["kubectl", "--context", context, "-n", namespace, "get", "pod", pod_name, "-o", "jsonpath={.status.phase}"],
            check=False,
        )
        current = (phase.stdout or "").strip()
        if current and current != last_phase:
            print(f"[loadtest] pod phase: {current}")
            last_phase = current
        if current in {"Failed", "Succeeded"}:
            break
        done_probe = run(
            ["kubectl", "--context", context, "-n", namespace, "exec", pod_name, "--", "sh", "-lc", "test -f /output/done"],
            check=False,
        )
        if done_probe.returncode == 0:
            code_read = run(
                ["kubectl", "--context", context, "-n", namespace, "exec", pod_name, "--", "cat", "/output/exit_code"],
                check=False,
            )
            try:
                return int((code_read.stdout or "1").strip())
            except ValueError:
                return 1
        time.sleep(5)
    raise SystemExit(f"Timed out waiting for pod {pod_name} to produce artifacts")


def read_remote_file(context: str, namespace: str, pod_name: str, remote_path: str) -> str | None:
    proc = run(
        ["kubectl", "--context", context, "-n", namespace, "exec", pod_name, "--", "cat", remote_path],
        check=False,
    )
    if proc.returncode != 0:
        return None
    return proc.stdout


def main() -> int:
    args = parse_args()
    values = ensure_inputs(args)
    models = values["models"]
    requests_per_model = int(values["requests_per_model"])
    token_target = int(values["target_input_tokens"])
    api_key = str(values.pop("api_key"))
    report_dir = create_report_dir(args, models, requests_per_model, token_target)

    config_path = report_dir / "config.json"
    config_path.write_text(json.dumps(values, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    runner_path = Path(__file__).resolve().parent / "openai_routing_loadtest_runner.py"
    stamp = time.strftime("%Y%m%d%H%M%S")
    resource_base = f"openai-routing-loadtest-{stamp}"
    configmap_name = f"{resource_base}-cfg"
    secret_name = f"{resource_base}-secret"
    pod_name = f"{resource_base}-pod"

    cleanup_cmds = []
    try:
        print(f"[loadtest] report dir: {report_dir}")
        print(f"[loadtest] models: {', '.join(models)}")
        print(f"[loadtest] requests_per_model: {requests_per_model}")
        print(f"[loadtest] target_input_tokens: {token_target}")
        print(f"[loadtest] concurrency: {values['concurrency']}")
        print(f"[loadtest] reasoning_effort: {values['reasoning_effort'] or '<omitted>'}")
        if values["user_prompt"]:
            print(f"[loadtest] user_prompt override: {values['user_prompt']}")

        cm_yaml = run(
            [
                "kubectl", "--context", args.context, "-n", args.namespace,
                "create", "configmap", configmap_name,
                f"--from-file=runner.py={runner_path}",
                f"--from-file=config.json={config_path}",
                "--dry-run=client", "-o", "yaml",
            ]
        ).stdout
        run(["kubectl", "--context", args.context, "apply", "-f", "-"], input_text=cm_yaml)
        cleanup_cmds.append(["kubectl", "--context", args.context, "-n", args.namespace, "delete", "configmap", configmap_name, "--ignore-not-found=true"])

        secret_yaml = run(
            [
                "kubectl", "--context", args.context, "-n", args.namespace,
                "create", "secret", "generic", secret_name,
                f"--from-literal=api_key={api_key}",
                "--dry-run=client", "-o", "yaml",
            ]
        ).stdout
        run(["kubectl", "--context", args.context, "apply", "-f", "-"], input_text=secret_yaml)
        cleanup_cmds.append(["kubectl", "--context", args.context, "-n", args.namespace, "delete", "secret", secret_name, "--ignore-not-found=true"])

        manifest = build_pod_manifest(pod_name, args.namespace, args.image, configmap_name, secret_name)
        (report_dir / "pod.yaml").write_text(manifest, encoding="utf-8")
        run(["kubectl", "--context", args.context, "apply", "-f", "-"], input_text=manifest)
        cleanup_cmds.append(["kubectl", "--context", args.context, "-n", args.namespace, "delete", "pod", pod_name, "--ignore-not-found=true"])

        exit_code = wait_for_runner_done(args.context, args.namespace, pod_name, args.request_timeout_seconds)
        print(f"[loadtest] runner exit_code={exit_code}")

        pod_log = run(["kubectl", "--context", args.context, "-n", args.namespace, "logs", pod_name], check=False).stdout
        (report_dir / "pod.log").write_text(pod_log or "", encoding="utf-8")

        for remote_name in ["config.resolved.json", "summary.json", "summary.md", "requests.jsonl"]:
            content = read_remote_file(args.context, args.namespace, pod_name, f"/output/{remote_name}")
            if content is not None:
                (report_dir / remote_name).write_text(content, encoding="utf-8")

        summary_md = report_dir / "summary.md"
        if summary_md.exists():
            print("\n" + summary_md.read_text(encoding="utf-8"))
        else:
            print("[loadtest] summary.md not found; inspect pod.log")

        return 0 if exit_code == 0 else exit_code
    finally:
        if not args.keep_resources:
            for cmd in cleanup_cmds:
                run(cmd, check=False)


if __name__ == "__main__":
    raise SystemExit(main())
