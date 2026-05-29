# Sub2API OpenAI Routing Service Branch

This README is intentionally branch-specific for `feature/openai-routing-service-mvp`.
It is not meant to match upstream `main`.

If you are an AI tool or a human operator landing on this branch, start here.

## What This Branch Is For

This branch turns part of Sub2API into a formal OpenAI-compatible routing service with a stable REST surface under `/openai-routing`.

Current branch focus:

- `/openai-routing/v1/*` OpenAI-compatible endpoints
- Responses-first support for:
  - `gpt-5.2`
  - `gpt-5.3`
  - `gpt-5.4`
  - `gpt-5.5`
  - `gpt-image-2`
- local load-test / TUI tooling
- routing-service image build and deploy workflow

## Routes That Matter

Primary endpoints on this branch:

- `GET /openai-routing/healthz`
- `GET /openai-routing/v1/models`
- `POST /openai-routing/v1/chat/completions`
- `POST /openai-routing/v1/images/generations`
- `POST /openai-routing/v1/responses`

For image generation, the preferred path on this branch is:

- `POST /openai-routing/v1/responses`
- top-level text model is Responses-capable
- image generation happens through `tools[].type=image_generation`

## Behavior To Preserve

These semantics are intentional and must survive future rebases:

- If a compat client omits `reasoning_effort`, do not inject a default value.
- `/openai-routing/v1/*` routes must remain registered and working.
- Non-stream `/v1/responses` JSON must still reconstruct or supplement terminal `output` from SSE item events when the terminal payload is incomplete.
- `gpt-image-2` must stay usable through `/v1/responses`.

## Important Files

Routing-service implementation:

- `backend/internal/server/routes/openai_compat.go`
- `backend/internal/handler/openai_chat_completions.go`
- `backend/internal/handler/openai_images.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_images.go`
- `backend/internal/service/openai_images_responses.go`
- `backend/internal/service/openai_codex_transform.go`

Validation and tooling:

- `backend/internal/server/routes/openai_compat_integration_test.go`
- `backend/internal/server/routes/openai_compat_images_integration_test.go`
- `backend/internal/service/openai_gateway_service_test.go`
- `backend/internal/service/openai_codex_transform_test.go`
- `tools/openai_responses_image_demo.py`
- `tools/openai_routing_loadtest_dev.py`
- `tools/openai_routing_loadtest_runner.py`
- `tools/openai_routing_loadtest_tui.py`

Branch-specific docs:

- `AGENTS.md`
- `CLAUDE.md`
- `docs/RUNBOOK_SUB2API_OPENAI_RESPONSES_IMAGE_GENERATION.md`
- `docs/CHANGELOG_OPENAI_ROUTING_V0117_REBASE.md`
- `docs/SOP_SUB2API_OPENAI_ROUTING_SERVICE_MAINTENANCE.md`

## Quick Start

Run from repo root unless noted.

### Option A: Local Service With Dockerized Dependencies

This is the simplest local setup for this branch.

```bash
make openai-routing-deps-up
make openai-routing-provision-local
make openai-routing-service-local
```

Health check:

```bash
make openai-routing-healthcheck
```

UI mode with embedded frontend:

```bash
make openai-routing-ui-local
```

### Option B: Linux Non-Containerized Service Startup

Use this when PostgreSQL and Redis are already running outside Docker.

Minimum environment:

```bash
export DATABASE_HOST=127.0.0.1
export DATABASE_PORT=5432
export DATABASE_USER=postgres
export DATABASE_PASSWORD=postgres
export DATABASE_DBNAME=sub2api
export DATABASE_SSLMODE=disable

export REDIS_HOST=127.0.0.1
export REDIS_PORT=6379
export REDIS_PASSWORD=
export REDIS_DB=0

export SERVER_HOST=0.0.0.0
export SERVER_PORT=8080
export SERVER_MODE=debug
export LOG_LEVEL=debug

export JWT_SECRET="$(openssl rand -hex 32)"
export TOTP_ENCRYPTION_KEY="$(openssl rand -hex 32)"

export OPENAI_COMPAT_SERVICE_ENABLED=true
export OPENAI_COMPAT_SERVICE_AUTH_MODE=api_key
export OPENAI_COMPAT_SERVICE_PATH_PREFIX=/openai-routing
export OPENAI_COMPAT_SERVICE_DEFAULT_REASONING_EFFORT=low
```

Foreground run with logs:

```bash
cd backend
go run ./cmd/server 2>&1 | tee /tmp/sub2api-routing-service.log
```

Background run with logs on Linux:

```bash
cd backend
nohup go run ./cmd/server > /tmp/sub2api-routing-service.log 2>&1 &
tail -f /tmp/sub2api-routing-service.log
```

Embedded UI build plus Linux startup:

```bash
make openai-routing-ui-build
cd backend
go run -tags embed ./cmd/server 2>&1 | tee /tmp/sub2api-routing-ui.log
```

Health check after startup:

```bash
curl -s http://127.0.0.1:8080/openai-routing/healthz
```

## Local Smoke Tests

Text:

```bash
curl -s http://127.0.0.1:8080/openai-routing/v1/responses \
  -H "Authorization: Bearer $OPENAI_ROUTING_UI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4",
    "stream":false,
    "input":[
      {
        "type":"message",
        "role":"user",
        "content":[
          {"type":"input_text","text":"Reply with exactly local-ok"}
        ]
      }
    ]
  }'
```

Image generation through Responses:

```bash
curl -s http://127.0.0.1:8080/openai-routing/v1/responses \
  -H "Authorization: Bearer $OPENAI_ROUTING_UI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4-mini",
    "stream":false,
    "input":[
      {
        "type":"message",
        "role":"user",
        "content":[
          {"type":"input_text","text":"A minimal red square centered on a white background."}
        ]
      }
    ],
    "tool_choice":{"type":"image_generation"},
    "tools":[
      {
        "type":"image_generation",
        "model":"gpt-image-2",
        "size":"1024x1024",
        "output_format":"png"
      }
    ]
  }'
```

Or use the helper script:

```bash
API_HOST='http://127.0.0.1:8080' \
SUB2API_API_KEY="$OPENAI_ROUTING_UI_API_KEY" \
PROMPT='A minimal red square centered on a white background.' \
/tmp/run_openai_responses_image_generation.sh
```

## Required Validation

Minimum checks before considering this branch healthy after meaningful changes:

```bash
python3 -m py_compile \
  tools/openai_routing_loadtest_dev.py \
  tools/openai_routing_loadtest_runner.py \
  tools/openai_routing_loadtest_tui.py \
  tools/litellm_backup_azure_ttft.py \
  tools/openai_responses_image_demo.py
```

```bash
cd backend
GOCACHE=/tmp/sub2api-go-cache go test ./internal/service ./internal/handler ./internal/server/routes -count=1
GOCACHE=/tmp/sub2api-go-cache go test ./cmd/openai-oauth-client -count=1
```

## Required Local Current-Branch E2E

A release rebase on this branch is not considered complete until the current
local branch is started and these local E2E checks pass against
`http://127.0.0.1:8080/openai-routing/v1/responses`:

- `gpt-5.2`
- `gpt-5.3`
- `gpt-5.4`
- `gpt-5.5`
- `gpt-image-2` through `/openai-routing/v1/responses`

And against `http://127.0.0.1:8080/openai-routing/v1/embeddings`:

- `text-embedding-3-small` or another OpenAI-compatible embeddings model

Public dev is a post-image-deploy regression target, not the source of truth
for validating an unpushed local rebase:

- `https://dev-sub2api.frai.pro`

## Release Rebase Notes

When syncing this branch to a newer upstream release:

- Rebase onto the release tag first.
- Prefer upstream implementations if the release absorbed an equivalent helper.
- Remove duplicate branch-only helpers instead of keeping two competing code paths.
- Expect route test constructor drift and mock interface drift after release syncs.
- Push rewritten history to `private` with `--force-with-lease`.

See:

- `docs/CHANGELOG_OPENAI_ROUTING_V0117_REBASE.md`
- `AGENTS.md`

## Image Build / Push

Build locally:

```bash
make routing-service-image-build
```

Push to ECR:

```bash
make routing-service-image-push
```

The push helper currently publishes to:

- `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service`

## Branch Remote Rules

This branch is maintained on:

- `private -> https://github.com/joeeeeey/sub2api-openai-routing-service.git`

Do not push this branch to `origin` unless explicitly asked.

Typical push:

```bash
git push private HEAD:feature/openai-routing-service-mvp
```

After a rebase:

```bash
git push --force-with-lease private HEAD:feature/openai-routing-service-mvp
```
