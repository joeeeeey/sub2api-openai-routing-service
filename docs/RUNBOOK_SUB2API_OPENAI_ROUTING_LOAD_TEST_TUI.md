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
  - `reasoning_effort=low`
- prompt generation uses the same 10 prompt families as the batch load test
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
  --target-url https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions
```

## Default target

Default local TUI target:

```text
https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions
```

You can override with:

- `--target-url`

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

## Notes

- This mode is for operator perception and debugging.
- For statistically cleaner capacity tests, continue using:
  - `make openai-routing-loadtest-dev`
