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

## Notes For Future Agents

- There are many benchmark-related changes on this branch; check `git status` carefully before mixing unrelated work.
- This repository root is **not** a Go module; run Go tests from `backend/`.
- When the user asks for "latest dev deployment state", verify actual Argo / Kubernetes state rather than assuming merged PRs are already live.
- The dev routing-service image currently uses hash tags in ECR; keep image tags consistent with project conventions.

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
