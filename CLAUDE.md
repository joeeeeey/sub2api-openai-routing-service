# CLAUDE.md

This file is a short operating guide for AI tools working on
`feature/openai-routing-service-mvp`.

For more detail, read:

- `README.md`
- `AGENTS.md`

## Branch Intent

This branch is not generic upstream Sub2API work.
It is specifically for the OpenAI routing service surface under `/openai-routing`.

Primary focus:

- `gpt-5.2`
- `gpt-5.3`
- `gpt-5.4`
- `gpt-5.5`
- `gpt-image-2`

Primary endpoint family:

- `/openai-routing/v1/*`

## Fast Start

### Local deps via Docker, service via Go

```bash
make openai-routing-deps-up
make openai-routing-provision-local
make openai-routing-service-local
```

### Embedded UI

```bash
make openai-routing-ui-local
```

### Health check

```bash
curl -s http://127.0.0.1:8080/openai-routing/healthz
```

## Linux Non-Containerized Startup

Use this when PostgreSQL and Redis already exist outside Docker.

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

Foreground with logs:

```bash
cd backend
go run ./cmd/server 2>&1 | tee /tmp/sub2api-routing-service.log
```

Background with logs:

```bash
cd backend
nohup go run ./cmd/server > /tmp/sub2api-routing-service.log 2>&1 &
tail -f /tmp/sub2api-routing-service.log
```

Embedded UI on Linux:

```bash
make openai-routing-ui-build
cd backend
go run -tags embed ./cmd/server 2>&1 | tee /tmp/sub2api-routing-ui.log
```

## Must-Preserve Behavior

- Do not inject compat-side `reasoning_effort` when omitted by the client.
- Keep `/openai-routing/v1/*` routes working.
- Keep `gpt-image-2` working through `/v1/responses` + `tools[].type=image_generation`.
- Keep non-stream Responses able to supplement empty terminal `output` from SSE item events.

## Required Validation

```bash
cd backend
GOCACHE=/tmp/sub2api-go-cache go test ./internal/service ./internal/handler ./internal/server/routes -count=1
GOCACHE=/tmp/sub2api-go-cache go test ./cmd/openai-oauth-client -count=1
```

Local current-branch E2E that should pass after rebases:

- `gpt-5.2`
- `gpt-5.3`
- `gpt-5.4`
- `gpt-5.5`
- `gpt-image-2`
- `text-embedding-3-small` or another OpenAI-compatible embeddings model through `/openai-routing/v1/embeddings`

Helper:

```bash
set -a; . ./.openai-routing-service/dev.env; set +a
python3 tools/openai_routing_local_e2e.py \
  --api-key "$OPENAI_COMPAT_SERVICE_API_KEY" \
  --report-file /tmp/openai-routing-local-e2e.json
```

Local base URL:

- `http://127.0.0.1:8080`

Public dev is for post-image-deploy regression only:

- `https://dev-sub2api.frai.pro`

## Push Rules

Push this branch to:

- `private/feature/openai-routing-service-mvp`

Do not push to `origin` unless explicitly asked.

After release rebases, expect to use:

```bash
git push --force-with-lease private HEAD:feature/openai-routing-service-mvp
```
