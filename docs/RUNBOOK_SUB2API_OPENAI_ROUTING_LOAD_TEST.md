# Sub2API OpenAI Routing Load Test Runbook

## Goal

Run controlled load tests against the dev `sub2api-openai-routing-service` instance from inside the dev Kubernetes cluster.

Traffic shape:

```text
Temporary pod in dev cluster
  -> sub2api-openai-routing-service.component.svc.cluster.local:8080
  -> /openai-routing/v1/chat/completions
```

## Supported scenarios

This tool supports the matrices requested for:

- request count per model:
  - `100`
  - `1000`
- approximate input token target:
  - `100`
  - `1000`
  - `2000`
- models:
  - `gpt-5.2`
  - `gpt-5.3-codex-spark`
  - `gpt-5.4`
  - or all three
- concurrency:
  - default `10`

Important:

- `requests_per_model` means exactly that: per selected model.
- If you select `all models` and choose `1000`, the total request count becomes `3000`.

## Request shape

Each request:

- uses `stream=true`
- sends `reasoning_effort` only when explicitly provided
- includes both `system` and `user` messages
- uses one of 10 prompt families
- expands prompt length to the requested approximate token target
- if `--user-prompt` is provided, the default user prompt families are replaced with that prompt for every request, and a neutral system prompt is used to avoid the built-in short-answer system prompts skewing the comparison

## Metrics captured

Per request:

- model
- prompt family index
- target input tokens
- actual input tokens
- HTTP status
- total latency
- TTFT
- whether `[DONE]` was observed
- error message (if any)

Per model summary:

- total requests
- completed requests
- completion success rate
- TTFT available count
- TTFT success rate
- TTFT p50 / p90 / p95 / p99 / avg / min / max
- total latency avg / p95
- effective throughput

## Output

Local report directory:

```text
.loadtest-reports/<timestamp>-<models>-r<requests>-t<tokens>/
```

Files written locally:

- `config.json`
- `summary.json`
- `summary.md`
- `requests.jsonl`
- `pod.log`

`requests.jsonl` includes the full streamed `response_text` for each request, so you can compare outputs across models and reasoning settings.

## Required input

You need a valid Sub2API API key for the dev service.

Preferred way:

- export `OPENAI_ROUTING_LOADTEST_API_KEY`

or the tool will prompt you securely.

## Run

Interactive:

```bash
cd /Users/joey/repos/my-project/sub2api
make openai-routing-loadtest-dev
```

Non-interactive example:

```bash
cd /Users/joey/repos/my-project/sub2api

OPENAI_ROUTING_LOADTEST_API_KEY='...' \
python3 tools/openai_routing_loadtest_dev.py \
  --requests-per-model 100 \
  --token-target 1000 \
  --model-mode single \
  --model gpt-5.4 \
  --concurrency 10 \
  --user-prompt "Explain quantum mechanics in English."
```

Additional useful flags:

- `--reasoning-effort none|low|medium|high|xhigh` if you want to explicitly send it
- `--user-prompt "..."` for a custom prompt override

## Defaults

- kubectl context: `dev-us2`
- namespace: `component`
- in-cluster target URL:
  - `http://sub2api-openai-routing-service.component.svc.cluster.local:8080/openai-routing/v1/chat/completions`
- pod image:
  - `python:3.11-slim`

## Cleanup

By default the local tool deletes the temporary:

- pod
- configmap
- secret

after report collection.

If you want to keep them for debugging:

```bash
python3 tools/openai_routing_loadtest_dev.py --keep-resources
```
