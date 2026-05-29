# OpenAI Routing Branch Rebase to `v0.1.117`

This document is a short changelog and PR description reference for rebasing
`feature/openai-routing-service-mvp` onto upstream `Wei-Shaw/sub2api` release
`v0.1.117`.

## Summary

- Rebases the routing-service branch onto upstream `v0.1.117`
- Keeps the branch-specific `/openai-routing/v1/*` REST surface intact
- Preserves the current compat behavior where omitted `reasoning_effort` stays omitted
- Keeps the branch's OpenAI image generation support centered on `/v1/responses`
- Aligns tests and helper ownership with upstream `v0.1.117`

## What Changed

- Rebased branch history from the long-lived routing-service branch onto upstream tag `v0.1.117`
- Kept the OpenAI routing service feature set:
  - `/openai-routing/v1/models`
  - `/openai-routing/v1/chat/completions`
  - `/openai-routing/v1/images/generations`
  - `/openai-routing/v1/responses`
- Preserved image generation support for:
  - simplified REST image generation
  - Responses-style image generation using `tools[].type=image_generation`
- Kept normalized model coverage for the branch focus area:
  - `gpt-5.2`
  - `gpt-5.3`
  - `gpt-5.4`
  - `gpt-image-2`
- Updated route and service tests to match upstream constructor and interface changes
- Removed the branch-local `openai_responses_sse_aggregate.go` helper because upstream `v0.1.117` already provides the final-response flow needed by this branch
- Adjusted SSE-related tests so they match the current upstream responsibility split:
  - terminal response extraction
  - SSE output supplementation

## Important Preservation Rules

These branch-specific behaviors must continue to hold after future rebases:

- Do not inject a default compat-side `reasoning_effort` when the client omits it
- Keep `/openai-routing/v1/*` routes registered and working
- Keep `/v1/responses` image generation exposed through REST
- Keep image generation using:
  - top-level Responses-capable text model
  - `tools[].type=image_generation`
  - `tools[].model=gpt-image-2`
- Keep non-stream Responses JSON able to reconstruct or supplement empty terminal `output` from SSE item events

## Validation Performed

### Go / Local Validation

- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./internal/service ./internal/handler ./internal/server/routes -count=1`
- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./cmd/openai-oauth-client -count=1`

### Public Dev E2E Validation

Validated against:

- base URL: `https://dev-sub2api.frai.pro`
- endpoint family: `/openai-routing/v1/responses`

Passed:

- `gpt-5.2` text response
- `gpt-5.3` text response
- `gpt-5.4` text response
- `gpt-image-2` image generation through Responses `image_generation`

Observed result shape:

- text requests returned `HTTP 200` and `status=completed`
- image generation returned `HTTP 200`, `status=completed`, and a non-empty `image_generation_call.result`
- generated image payload was decoded and saved locally as a real PNG for verification

## Suggested PR Description

```md
## Summary

- rebase `feature/openai-routing-service-mvp` onto upstream `v0.1.117`
- preserve `/openai-routing/v1/*` REST endpoints
- preserve current compat reasoning behavior where omitted `reasoning_effort` stays omitted
- keep Responses-based `gpt-image-2` support working

## Notable Alignment Work

- updated route/service tests for upstream constructor and interface changes
- removed branch-local duplicate SSE aggregation helper now superseded by upstream flow
- kept non-stream Responses output supplementation for image/text outputs

## Validation

- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./internal/service ./internal/handler ./internal/server/routes -count=1`
- `cd backend && GOCACHE=/tmp/sub2api-go-cache go test ./cmd/openai-oauth-client -count=1`
- public dev E2E passed for:
  - `gpt-5.2`
  - `gpt-5.3`
  - `gpt-5.4`
  - `gpt-image-2` via `/openai-routing/v1/responses`
```

## Notes For Future Rebases

- This branch is expected to rebase regularly onto newer upstream releases
- Constructor drift in tests is common after release syncs
- Duplicate branch-local helpers should be removed if upstream has absorbed the same responsibility
- After a history-rewriting rebase, push to `private` with `--force-with-lease`
