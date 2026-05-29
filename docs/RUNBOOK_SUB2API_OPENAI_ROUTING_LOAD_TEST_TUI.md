# Sub2API OpenAI Routing Load Test TUI

## Goal

Provide a local live TUI view for subjective TTFT perception while requests are in flight.

This mode is intended for:

- visually observing stream start timing
- comparing models under the same prompt size
- watching rolling concurrency workers in real time

It is not a replacement for the in-cluster batch load runner. It is a complementary operator-facing view.

## Behavior

If concurrency is:

- `2` -> two live panes shown side by side
- `5` -> five live panes shown side by side

Panes are rendered horizontally as colored chat-like panels.

Each pane shows:

- current request number
- model
- prompt family
- system prompt preview
- user prompt preview
- live timer while waiting for first text token
- frozen TTFT once the first text token appears
- current stream status
- streamed response text as it arrives

When a worker finishes:

- the pane is reused for the next request
- completed state is briefly visible before the next request starts

## Important details

- requests are sent with:
  - `stream=true`
  - `reasoning_effort` is omitted by default and only sent when explicitly provided
- prompt generation uses the same 10 prompt families as the batch load test
- if `--user-prompt` is provided, the default user prompt families are replaced with that prompt for every request, and a neutral system prompt is used to avoid the built-in short-answer system prompts skewing the comparison
- report files are still written locally under `.loadtest-reports/`

## Run

Interactive:

```bash
cd /Users/joey/repos/my-project/sub2api
make openai-routing-loadtest-tui
```

Non-interactive example:

```bash
cd /Users/joey/repos/my-project/sub2api

OPENAI_ROUTING_LOADTEST_API_KEY='...' \
python3 tools/openai_routing_loadtest_tui.py \
  --requests 50 \
  --token-target 100 \
  --model gpt-5.3-codex-spark \
  --concurrency 2 \
  --user-prompt "Explain quantum mechanics in English." \
  --target-url https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions
```

Additional useful flags:

- `--reasoning-effort none|low|medium|high|xhigh` if you want to explicitly send it
- `--user-prompt "..."` for a custom prompt override

## Default target

Default local TUI target:

```text
https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions
```

You can override with:

- `--target-url`
- `--model-resolver backup-azure` if you want to resolve `gpt-*` or `azure/gpt-*` into LiteLLM hidden backup aliases

## Output

Local report directory:

```text
.loadtest-reports/<timestamp>-tui-<model>-r<requests>-t<tokens>-c<concurrency>/
```

Generated files:

- `config.json`
- `summary.json`
- `summary.md`
- `requests.jsonl`

`requests.jsonl` includes the full streamed `response_text` for each request, so you can diff outputs after the live run.

## Notes

- This mode is for operator perception and debugging.
- For statistically cleaner capacity tests, continue using:
  - `make openai-routing-loadtest-dev`
