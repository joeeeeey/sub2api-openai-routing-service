.PHONY: build build-backend build-frontend build-datamanagementd test test-backend test-frontend test-frontend-critical test-datamanagementd secret-scan oauth-local-server oauth-local-healthcheck oauth-local-chat-test oauth-local-chat-stream-test openai-routing-deps-up openai-routing-deps-down openai-routing-deps-logs openai-routing-provision-local openai-routing-service-local openai-routing-service-local-static openai-routing-ui-build openai-routing-ui-local openai-routing-healthcheck openai-routing-chat-test openai-routing-chat-test-static

FRONTEND_CRITICAL_VITEST := \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 编译 datamanagementd（宿主机数据管理进程）
build-datamanagementd:
	@cd datamanagement && go build -o datamanagementd ./cmd/datamanagementd

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)

test-datamanagementd:
	@cd datamanagement && go test ./...

secret-scan:
	@python3 tools/secret_scan.py

oauth-local-server:
	@cd backend && go run ./cmd/openai-oauth-client server --listen 127.0.0.1:38080

oauth-local-healthcheck:
	@curl -s http://127.0.0.1:38080/healthz

oauth-local-chat-test:
	@curl -s http://127.0.0.1:38080/v1/chat/completions \
		-H 'Content-Type: application/json' \
		-d '{"model":"gpt-5.4","messages":[{"role":"system","content":"act as assistant"},{"role":"user","content":"say hello in 5 words"}],"stream":false}'

oauth-local-chat-stream-test:
	@curl -N http://127.0.0.1:38080/v1/chat/completions \
		-H 'Content-Type: application/json' \
		-d '{"model":"gpt-5.4","messages":[{"role":"system","content":"act as assistant"},{"role":"user","content":"say hello in 5 words"}],"stream":true,"stream_options":{"include_usage":true}}'

openai-routing-service-local:
	@test -f .openai-routing-service/dev.env || (echo ".openai-routing-service/dev.env not found, run 'make openai-routing-provision-local' first" >&2; exit 1)
	@set -a; . ./.openai-routing-service/dev.env; set +a; \
	cd backend && \
	LOG_LEVEL=debug \
	SERVER_MODE=debug \
	TOTP_ENCRYPTION_KEY=$${TOTP_ENCRYPTION_KEY} \
	OPENAI_COMPAT_SERVICE_ENABLED=true \
	OPENAI_COMPAT_SERVICE_AUTH_MODE=api_key \
	OPENAI_COMPAT_SERVICE_PATH_PREFIX=/openai-routing \
	OPENAI_COMPAT_SERVICE_DEFAULT_REASONING_EFFORT=low \
	go run ./cmd/server

openai-routing-service-local-static:
	@test -f .openai-routing-service/dev.env || (echo ".openai-routing-service/dev.env not found, run 'make openai-routing-provision-local' first" >&2; exit 1)
	@set -a; . ./.openai-routing-service/dev.env; set +a; \
	cd backend && \
	LOG_LEVEL=debug \
	SERVER_MODE=debug \
	TOTP_ENCRYPTION_KEY=$${TOTP_ENCRYPTION_KEY} \
	OPENAI_COMPAT_SERVICE_ENABLED=true \
	OPENAI_COMPAT_SERVICE_AUTH_MODE=static_key \
	OPENAI_COMPAT_SERVICE_STATIC_KEY=$${OPENAI_COMPAT_STATIC_KEY} \
	OPENAI_COMPAT_SERVICE_SERVICE_API_KEY=$${OPENAI_COMPAT_SERVICE_API_KEY} \
	OPENAI_COMPAT_SERVICE_PATH_PREFIX=/openai-routing \
	OPENAI_COMPAT_SERVICE_DEFAULT_REASONING_EFFORT=low \
	go run ./cmd/server

openai-routing-ui-build:
	@if [ ! -d frontend/node_modules ]; then pnpm --dir frontend install; fi
	@pnpm --dir frontend build

openai-routing-ui-local:
	@test -f .openai-routing-service/dev.env || (echo ".openai-routing-service/dev.env not found, run 'make openai-routing-provision-local' first" >&2; exit 1)
	@$(MAKE) openai-routing-ui-build
	@set -a; . ./.openai-routing-service/dev.env; set +a; \
	cd backend && \
	LOG_LEVEL=debug \
	SERVER_MODE=debug \
	TOTP_ENCRYPTION_KEY=$${TOTP_ENCRYPTION_KEY} \
	OPENAI_COMPAT_SERVICE_ENABLED=true \
	OPENAI_COMPAT_SERVICE_AUTH_MODE=api_key \
	OPENAI_COMPAT_SERVICE_PATH_PREFIX=/openai-routing \
	OPENAI_COMPAT_SERVICE_DEFAULT_REASONING_EFFORT=low \
	go run -tags embed ./cmd/server

openai-routing-healthcheck:
	@curl -s http://127.0.0.1:8080/openai-routing/healthz

openai-routing-chat-test:
	@test -n "$$OPENAI_ROUTING_UI_API_KEY" || (echo "OPENAI_ROUTING_UI_API_KEY is required" >&2; exit 1)
	@curl -s http://127.0.0.1:8080/openai-routing/v1/chat/completions \
		-H "Authorization: Bearer $$OPENAI_ROUTING_UI_API_KEY" \
		-H 'Content-Type: application/json' \
		-d '{"model":"gpt-5.4","messages":[{"role":"system","content":"act as assistant"},{"role":"user","content":"say hello in 5 words"}],"stream":false}'

openai-routing-chat-test-static:
	@test -f .openai-routing-service/dev.env || (echo ".openai-routing-service/dev.env not found, run 'make openai-routing-provision-local' first" >&2; exit 1)
	@set -a; . ./.openai-routing-service/dev.env; set +a; \
	curl -s http://127.0.0.1:8080/openai-routing/v1/chat/completions \
		-H "Authorization: Bearer $$OPENAI_COMPAT_STATIC_KEY" \
		-H 'Content-Type: application/json' \
		-d '{"model":"gpt-5.4","messages":[{"role":"system","content":"act as assistant"},{"role":"user","content":"say hello in 5 words"}],"stream":false}'

openai-routing-deps-up:
	@mkdir -p deploy/routing_postgres_data deploy/routing_redis_data
	@cd deploy && docker compose -f docker-compose.openai-routing-dev.yml up -d

openai-routing-deps-down:
	@cd deploy && docker compose -f docker-compose.openai-routing-dev.yml down

openai-routing-deps-logs:
	@cd deploy && docker compose -f docker-compose.openai-routing-dev.yml logs -f

openai-routing-provision-local:
	@cd backend && go run ./cmd/openai-oauth-client provision-sub2api-local
