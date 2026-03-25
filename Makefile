.PHONY: build build-backend build-frontend build-datamanagementd test test-backend test-frontend test-datamanagementd secret-scan oauth-local-server oauth-local-healthcheck oauth-local-chat-test oauth-local-chat-stream-test

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
