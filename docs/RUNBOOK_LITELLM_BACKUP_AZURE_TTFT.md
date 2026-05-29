# LiteLLM Backup Azure TTFT Runbook

## Goal

Measure TTFT for the direct:

```text
client -> dev-litellm -> backup/azure/*
```

route without going through `sub2api`.

This is useful when comparing:

- `litellm -> sub2api -> Codex internal`
- versus
- `litellm -> Azure backup alias`

## Default behavior

The script:

- defaults to LiteLLM base URL `https://dev-litellm.frai.pro/`
- sends requests to `https://dev-litellm.frai.pro/chat/completions`
- forces `stream=true`
- sends configurable `reasoning_effort` (default `low`)
- does **not** send `temperature` by default
- resolves common model names to hidden backup aliases
- if `--user-prompt` is provided, the default prompt families are replaced with that prompt for every request, and a neutral system prompt is used to avoid the built-in short-answer prompt families skewing the comparison

Examples:

- `gpt-5.2` -> `backup/azure/gpt-5.2`
- `azure/gpt-5.2` -> `backup/azure/gpt-5.2`
- `backup/azure/gpt-5.2` -> unchanged

## Run

Interactive:

```bash
cd /Users/joey/repos/my-project/sub2api
make litellm-backup-azure-ttft
```

Non-interactive:

```bash
cd /Users/joey/repos/my-project/sub2api

LITELLM_API_KEY='...' \
make litellm-backup-azure-ttft
```

Explicit script usage:

```bash
cd /Users/joey/repos/my-project/sub2api

LITELLM_API_KEY='...' \
python3 tools/litellm_backup_azure_ttft.py \
  --model gpt-5.2 \
  --requests 20 \
  --concurrency 1 \
  --token-target 100 \
  --reasoning-effort low \
  --user-prompt "Explain quantum mechanics in English."
```

## Output

Reports are written locally under:

```text
.loadtest-reports/<timestamp>-backup-azure-<model>-r<requests>-t<tokens>-c<concurrency>/
```

Files:

- `config.json`
- `summary.json`
- `summary.md`
- `requests.jsonl`

`requests.jsonl` includes the full streamed `response_text` for each request, so you can compare answers between different reasoning settings.

## Notes

- This script intentionally measures the hidden Azure fallback alias path.
- If you want an exact backup alias, pass it directly:

```bash
python3 tools/litellm_backup_azure_ttft.py --model backup/azure/gpt-5.3-codex-spark
```

- You can also override the base URL:

```bash
python3 tools/litellm_backup_azure_ttft.py \
  --base-url https://dev-litellm.frai.pro/ \
  --model gpt-5.2 \
  --requests 20 \
  --concurrency 1 \
  --token-target 100
```

Additional useful flags:

- `--reasoning-effort none|low|medium|high|xhigh`
- `--user-prompt "..."` for a custom prompt override

## TUI with LiteLLM

You can reuse the generic chat TUI against LiteLLM directly:

```bash
cd /Users/joey/repos/my-project/sub2api

OPENAI_ROUTING_LOADTEST_API_KEY="$LITELLM_API_KEY" \
python3 tools/openai_routing_loadtest_tui.py \
  --target-url https://dev-litellm.frai.pro/chat/completions \
  --model gpt-5.2 \
  --model-resolver backup-azure \
  --requests 20 \
  --concurrency 2 \
  --token-target 100 \
  --reasoning-effort low \
  --user-prompt "Explain quantum mechanics in English."
```
