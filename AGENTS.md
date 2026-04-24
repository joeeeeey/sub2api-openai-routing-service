# AGENTS.md

This file is for future Codex / agent work in this repository.

## Scope

- Repository: `sub2api`
- Current long-lived feature branch: `feature/openai-routing-service-mvp`
- Primary feature area on this branch:
  - formal `openai-routing-service`
  - OpenAI-compatible `/openai-routing/v1/*` endpoints
  - benchmark / TUI tooling
  - LiteLLM comparison tooling

## Repository Layout

- `backend/`
  - Go backend
  - main gateway logic, routing, billing, scheduling, compat transforms
- `frontend/`
  - Vue frontend
- `docs/`
  - design docs, runbooks, SOPs
- `tools/`
  - local benchmark, TUI, and comparison scripts
- `deploy/`
  - image build scripts and deployment helpers

Important files for the routing-service work:

- `backend/internal/server/routes/openai_compat.go`
- `backend/internal/handler/openai_chat_completions.go`
- `backend/internal/handler/openai_compat_request.go`
- `backend/internal/server/routes/openai_compat_integration_test.go`
- `tools/openai_routing_loadtest_dev.py`
- `tools/openai_routing_loadtest_runner.py`
- `tools/openai_routing_loadtest_tui.py`
- `tools/litellm_backup_azure_ttft.py`

## Current Behavior To Preserve

### OpenAI compat reasoning behavior

Current branch behavior is:

- If client explicitly sends `reasoning_effort`, preserve and forward it.
- If client omits `reasoning_effort`, do **not** inject a default value.
- This is intentional and matches the current dev rollout semantics.

Do not reintroduce unconditional compat-side default injection unless explicitly requested.

Related files:

- `backend/internal/handler/openai_compat_request.go`
- `backend/internal/handler/openai_compat_request_test.go`
- `backend/internal/server/routes/openai_compat_integration_test.go`

### Benchmark / TUI behavior

Current benchmark scripts support:

- custom `--user-prompt`
- optional `--reasoning-effort`
- omission of `reasoning_effort` when not provided
- full streamed `response_text` persisted into `requests.jsonl`
- TUI mode for both:
  - direct `openai-routing-service`
  - LiteLLM backup Azure path

Related docs:

- `docs/RUNBOOK_SUB2API_OPENAI_ROUTING_LOAD_TEST.md`
- `docs/RUNBOOK_SUB2API_OPENAI_ROUTING_LOAD_TEST_TUI.md`
- `docs/RUNBOOK_LITELLM_BACKUP_AZURE_TTFT.md`

## Common Commands

Run from repo root unless noted.

### Fast Start For Agents

If you need the routing service running quickly on this branch:

```bash
make openai-routing-deps-up
make openai-routing-provision-local
make openai-routing-service-local
```

If you need the embedded UI too:

```bash
make openai-routing-ui-local
```

Branch-specific overview docs:

- `README.md`
- `CLAUDE.md`

### Backend tests

```bash
cd backend
go test ./internal/handler ./internal/server/routes -run 'TestApplyOpenAICompatReasoningDefaults|TestOpenAICompat' -v
```

### Frontend / backend build helpers

```bash
make build-backend
make build-frontend
```

### Local routing service

```bash
make openai-routing-service-local
make openai-routing-ui-local
```

### Linux Non-Containerized Routing Service

Use this when PostgreSQL and Redis are already running outside Docker.

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

Foreground with log capture:

```bash
cd backend
go run ./cmd/server 2>&1 | tee /tmp/sub2api-routing-service.log
```

Background with log file on Linux:

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

Health check:

```bash
curl -s http://127.0.0.1:8080/openai-routing/healthz
```

### Benchmark / TUI

```bash
make openai-routing-loadtest-dev
make openai-routing-loadtest-tui
make litellm-backup-azure-ttft
make litellm-backup-azure-tui
```

### Static script validation

```bash
python3 -m py_compile \
  tools/openai_routing_loadtest_dev.py \
  tools/openai_routing_loadtest_runner.py \
  tools/openai_routing_loadtest_tui.py \
  tools/litellm_backup_azure_ttft.py
```

### Routing service image

```bash
make routing-service-image-build
make routing-service-image-push
```

## Git / Push Rules

- This branch is intentionally maintained on the `private` remote:
  - `private -> https://github.com/joeeeeey/sub2api-openai-routing-service.git`
- Do **not** push this feature work to `origin` unless explicitly asked.
- When pushing branch work for this feature, use `private`.

Useful commands:

```bash
git push private HEAD:feature/openai-routing-service-mvp
```

## Infra / Cross-Repo Rules

If the work requires changes in:

- `finalroundai/infra`
- `finalroundai/interviewai`
- `finalroundai/frai-website`
- `finalroundai/frai_js_server`

do **not** modify local reference checkouts in place.

Use a new `/tmp/...` clone for those repos and follow the temporary clone workflow.

This repository may contain local references to those repos for read-only inspection, but write changes should happen in `/tmp` clones.

## Deployment Context Notes

Recent routing-service deployment work used:

- ArgoCD context: `finalround-aks`
- Dev workload context: `dev-us2`
- Dev namespace for routing service: `component`

If Argo appears not to sync, verify first whether the app is:

- actually auto-sync enabled
- blocked by `ComparisonError` / manifest generation failure

Do not assume "no rollout" means auto-sync is disabled.

## Validation Checklist Before Commit

For routing-service or benchmark changes, prefer this minimum checklist:

- `python3 -m py_compile ...` for changed Python tools
- backend targeted tests for compat behavior
- if changing routing semantics, verify tests covering:
  - omitted reasoning
  - explicit reasoning preserved
  - stream chat chunk behavior

### Release Rebase Checklist

When rebasing `feature/openai-routing-service-mvp` onto a newer upstream release tag:

- Rebase onto the upstream release tag first, then replay branch-specific commits.
- Prefer upstream implementations when the release now contains an equivalent helper or bridge.
- Remove branch-local duplicate helpers if they collide with upstream and keep only one code path.
- Expect test-only constructor signature drift after release syncs, especially in:
  - `backend/internal/server/routes/*integration_test.go`
  - gateway service constructor sites
  - TLS upstream mock interfaces
- Preserve the branch-specific OpenAI routing behavior below:
  - `/openai-routing/v1/*` routes stay registered
  - omitted `reasoning_effort` must remain omitted
  - `/v1/responses` image generation keeps using a Responses-capable text model plus `tools[].type=image_generation`
  - non-stream Responses JSON must still supplement empty terminal `output` from SSE item events when needed
- After a history-rewriting rebase, push to `private` with `--force-with-lease`, not a blind force push.

### Required Rebase Validation

For any release rebase touching the OpenAI routing branch, do not stop at compile/test-only validation.
The minimum acceptance bar is:

- `python3 -m py_compile` for changed scripts under `tools/`
- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./internal/service ./internal/handler ./internal/server/routes -count=1`
- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./cmd/openai-oauth-client -count=1`
- Public dev E2E against `https://dev-sub2api.frai.pro/openai-routing/v1/responses` for:
  - `gpt-5.2`
  - `gpt-5.3`
  - `gpt-5.4`
  - `gpt-image-2` via `tools[].type=image_generation`

### Public Dev E2E Expectations

For this branch, a release rebase should not be treated as complete until the following are confirmed end-to-end:

- text response path returns `HTTP 200` with `status=completed`
- `gpt-5.2`, `gpt-5.3`, and `gpt-5.4` each return the expected assistant text
- image generation returns `HTTP 200`, `status=completed`, and a non-empty `image_generation_call.result`
- at least one generated image is decoded and saved locally to verify the payload is a real image, not only JSON-shaped success

## Notes For Future Agents

- There are many benchmark-related changes on this branch; check `git status` carefully before mixing unrelated work.
- This repository root is **not** a Go module; run Go tests from `backend/`.
- When the user asks for "latest dev deployment state", verify actual Argo / Kubernetes state rather than assuming merged PRs are already live.
- The dev routing-service image currently uses hash tags in ECR; keep image tags consistent with project conventions.
- The `private` remote for this branch will often require `--force-with-lease` after upstream release rebases because the branch history is intentionally rewritten.

## Claude Code Rewrite Work

This repository now also contains an Anthropic-side integration effort for:

- Claude Code CLI -> `sub2api` -> Anthropic upstream
- preserving `sub2api` multi-account Anthropic routing
- adding a Go rewrite module whose request-rewrite semantics are primarily based on:
  - `/Users/joey/repos/finalroundai/cc-gateway`

### Cross-Repo Relationship

- `sub2api` is the implementation target and runtime.
- `cc-gateway` is a reference implementation for rewrite semantics only.
- Do **not** try to move `cc-gateway` runtime architecture into `sub2api`.
- Do **not** replace `sub2api` account pool, sticky routing, failover, or token refresh with `cc-gateway` logic.

### Anthropic Rewrite Scope

For this work, prioritize the real Claude Code CLI path:

- `/v1/messages`
- `/v1/messages/count_tokens`

Do **not** treat `/openai-routing/*` as the target path for this effort unless explicitly asked.

### Behavior To Preserve

- `GatewayHandler` account selection flow
- `SelectAccountWithLoadAwareness`
- sticky session behavior
- Anthropic multi-account failover
- `ClaudeTokenProvider` / Anthropic OAuth refresh
- existing `IdentityService` ownership of:
  - `metadata.user_id` format compatibility
  - session masking
  - account-scoped fingerprint caching

### Rewrite Ownership Boundary

The new rewrite module should own:

- system prompt env/path rewriting
- `<system-reminder>` rewriting
- billing-header stripping in request body
- top-level leak-field cleanup (`baseUrl`, `base_url`, `gateway`)
- final upstream header cleanup relevant to Claude Code rewrite

The existing `IdentityService` should remain the source of truth for:

- generating / formatting `metadata.user_id`
- session-id masking
- account-scoped canonical fingerprint state

### Suggested Files For This Work

- `docs/DESIGN_CLAUDECODE_REWRITE_INTEGRATION.md`
- `backend/internal/service/claude_code_rewrite_service.go`
- `backend/internal/service/claude_code_rewrite_service_test.go`
- `backend/internal/service/gateway_service.go`

### Testing Notes

- Run Go tests from `backend/`.
- Prefer targeted service tests first.
- If modifying Anthropic gateway request behavior, verify both:
  - real Claude Code request path behavior changes as expected
  - non-Claude-Code request behavior stays unchanged
