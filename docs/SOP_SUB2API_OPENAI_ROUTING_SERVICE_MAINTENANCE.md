# Sub2API OpenAI Routing Service Maintenance SOP

本文档面向后续维护这个长期存在的 feature branch。

适用分支：

- `feature/openai-routing-service-mvp`

适用目标：

- 继续维护 `sub2api` 的 OpenAI routing service 能力
- 跟进 `main` 的更新
- 从 feature branch 构建镜像并发布到 dev
- 验证 `litellm -> sub2api -> Codex internal API` 链路

## 1. 当前状态

### 1.1 代码与部署现状

当前 dev 环境采用的是：

- `infra` GitOps 配置：
  - repo: `finalroundai/infra`
  - branch: `main`
- `sub2api` runtime image：
  - 仍然来自 feature branch 构建产物
  - 当前已部署镜像示例：
    - `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service:e850a601`

这意味着：

- 部署编排已经回归 `infra/main`
- 但应用代码本身还没有完全回归 `sub2api/main`

### 1.2 维护原则

在这条 feature branch 还长期存在的前提下，建议按下面原则维护：

- `sub2api` feature branch 继续承载 routing-service 代码
- 每次需要发布 dev 时，从这个 branch 构建镜像
- `infra/main` 继续引用一个明确的 image tag
- 后续和 `main` 做周期性同步，而不是无限拖延后一次性大 rebase

## 2. 哪些改动是高风险区域

后续从 `main` 同步时，优先审查这些文件：

- `backend/internal/config/config.go`
- `backend/internal/server/router.go`
- `backend/internal/server/http.go`
- `backend/internal/server/middleware/openai_compat.go`
- `backend/internal/server/routes/openai_compat.go`
- `backend/internal/handler/openai_compat_request.go`
- `backend/internal/handler/openai_chat_completions.go`
- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/service/openai_codex_transform.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_service.go`

这些文件直接决定：

- compat route 是否还挂着
- auth / group 校验是否正常
- `system -> instructions` 是否仍成立
- `reasoning_effort` 省略不注入、显式传递保留的语义是否仍正确
- OAuth internal upstream 变换是否仍兼容

## 3. 推荐同步策略

### 3.1 不要长期完全脱离 `main`

建议：

- 每次 `main` 有明显网关/配置/handler 变更后，就做一次小同步
- 不要等几周后再做一次巨大的 rebase

### 3.2 同步方式

推荐顺序：

```bash
cd /Users/joey/repos/my-project/sub2api

git checkout feature/openai-routing-service-mvp
git fetch origin
git rebase origin/main
```

如果 rebase 冲突：

- 优先看第 2 节列出的高风险文件
- 次优先看：
  - `deploy/*`
  - `Makefile`
  - `docs/*`

### 3.3 rebase 后必须做的事

至少做 3 类验证：

1. 编译 / 单测
2. formal `/openai-routing` 定向测试
3. dev 环境一条真实请求 smoke test

## 4. 当前必须保住的行为

后续改动时，以下行为属于不能 silently break 的核心契约。

### 4.1 formal route

这些 endpoint 必须保持可用：

- `GET /openai-routing/healthz`
- `GET /openai-routing/v1/models`
- `POST /openai-routing/v1/chat/completions`
- `POST /openai-routing/v1/responses`

### 4.2 compat 语义

这些行为必须保持：

- `chat.completions` 可以被下游标准 OpenAI SDK 调用
- `system` 消息在 OAuth/Codex internal 路径中会被提取为 `instructions`
- compat 路径如果客户端省略 `reasoning_effort`，必须保持省略，不做默认注入
- compat 路径如果客户端显式传 `reasoning_effort`，必须保留并转发
- `/v1/responses` 如果客户端省略 `reasoning.effort`，必须保持省略，不做默认注入
- `/v1/responses` 如果客户端显式传 `reasoning.effort`，缺少 `reasoning.summary` 时可补 `auto`
- stream 请求能返回标准 chat chunk / `[DONE]`

### 4.3 上游协议变换

OAuth internal 路径必须继续保证：

- `store=false`
- `stream=true`
- strip unsupported params:
  - `temperature`
  - `top_p`
  - `max_output_tokens`
  - `max_completion_tokens`
  - `frequency_penalty`
  - `presence_penalty`

### 4.4 GPT-5 family 路由策略

当前设计要求：

- GPT-5 family 先走 `sub2api`
- Azure 只作为 fallback

如果 LiteLLM config 或 upstream 映射有变更，这条规则必须重新验证。

## 5. 当前已有测试护栏

### 5.1 已有 formal route integration tests

当前 branch 已新增：

- `backend/internal/server/routes/openai_compat_integration_test.go`

覆盖：

- `/openai-routing/v1/chat/completions` 正常返回
- `system -> instructions`
- 省略 `reasoning_effort` 时不注入默认值
- 显式 `reasoning_effort` 保留
- 非 OpenAI group 被拒绝
- stream chunk 行为
- `/openai-routing/v1/responses` 省略 reasoning 时保持省略
- `/openai-routing/v1/responses` 显式 reasoning + string input 转换

### 5.2 已有 compat/unit tests

还包括：

- `backend/internal/handler/openai_compat_request_test.go`
- `backend/internal/pkg/apicompat/chatcompletions_responses_test.go`
- `backend/cmd/openai-oauth-client/server_test.go`

这些测试一起构成当前 feature branch 的核心护栏。

## 6. 本地测试 SOP

### 6.1 必跑单测

```bash
cd /Users/joey/repos/my-project/sub2api/backend

go test ./internal/server/routes -run 'TestOpenAICompat' -v
go test ./internal/server/routes ./internal/handler -run 'Test(OpenAICompat|GatewayRoutesOpenAIResponsesCompactPathIsRegistered|ApplyOpenAICompatReasoningDefaults)' -v
go test ./cmd/openai-oauth-client -v
```

如果你改了更底层 gateway 逻辑，建议额外补跑：

```bash
go test ./internal/pkg/apicompat -v
go test ./internal/service -run 'TestOpenAI|TestGateway|TestChatCompletions' -v
```

### 6.2 本地服务 smoke test

如果要用本机 OAuth 状态直测：

```bash
cd /Users/joey/repos/my-project/sub2api

make openai-routing-deps-up
bash scripts/openai-oauth-client.sh provision-sub2api-local
make openai-routing-ui-local
```

然后测：

```bash
curl http://127.0.0.1:8080/openai-routing/healthz
curl http://127.0.0.1:8080/openai-routing/v1/models -H 'Authorization: Bearer <key>'
```

### 6.3 与 InterviewAI 本地联调

如果要验证：

```text
InterviewAI local :5002 -> Apollo/dev LiteLLM -> sub2api -> Codex internal
```

参考：

- [RUNBOOK_INTERVIEWAI_DEV_LITELLM_SUB2API_E2E.md](/Users/joey/repos/my-project/sub2api/docs/RUNBOOK_INTERVIEWAI_DEV_LITELLM_SUB2API_E2E.md)

## 7. 从 feature branch 构建并发布 image 的 SOP

### 7.1 何时需要重建镜像

以下情况都应该重建：

- `sub2api` 代码有改动
- gateway / compat / transform / logging 有改动
- frontend/UI 嵌入资源有改动
- Dockerfile / runtime config / deploy config.example 有改动

### 7.2 本地构建

```bash
cd /Users/joey/repos/my-project/sub2api

make routing-service-image-build
```

### 7.3 推送到 ECR

```bash
cd /Users/joey/repos/my-project/sub2api

make routing-service-image-push
```

默认会：

- 使用当前 git short hash 作为 tag
- push 到：
  - `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service:<hash>`

相关脚本：

- `deploy/build_push_routing_service_image.sh`

### 7.4 发布到 dev

更新 `infra` 里的 image tag 后，通过 ArgoCD sync 到 dev。

推荐流程：

1. 在 `infra` 新 `/tmp` clone 上改 image tag
2. push branch / merge
3. 确认 Argo app sync
4. 再做 smoke test

## 8. dev 发布后的验证 SOP

### 8.1 基础健康检查

```bash
curl http://dev-sub2api.frai.pro/openai-routing/healthz
curl https://dev-sub2api.frai.pro/openai-routing/healthz
```

### 8.2 模型列表

```bash
curl https://dev-sub2api.frai.pro/openai-routing/v1/models \
  -H "Authorization: Bearer <SUB2API_UI_KEY>"
```

### 8.3 chat.completions

```bash
curl https://dev-sub2api.frai.pro/openai-routing/v1/chat/completions \
  -H "Authorization: Bearer <SUB2API_UI_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"gpt-5.4",
    "messages":[
      {"role":"system","content":"act as assistant"},
      {"role":"user","content":"say hello in 5 words"}
    ],
    "stream":false
  }'
```

### 8.4 `temperature=0.7` 兼容验证

这是当前非常关键的一条 smoke：

```bash
curl https://dev-litellm.frai.pro/chat/completions \
  -H "Authorization: Bearer <LITELLM_MASTER_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"azure/gpt-5.2",
    "messages":[{"role":"user","content":"Reply with exactly OK."}],
    "temperature":0.7,
    "stream":false
  }'
```

期望：

- 200
- 不触发 LiteLLM fallback
- `sub2api` 日志里能看到 `/openai-routing/v1/chat/completions`

## 9. LiteLLM 相关维护注意事项

### 9.1 不要轻易把 GPT-5 路由重新改回通配 Azure 竞争模型

之前出现“部分请求绕过 sub2api”的根因就是：

- 同一批外部模型名同时配置了 `sub2api` 路由和 Azure route/wildcard

后果：

- LiteLLM 不一定严格先走 `sub2api`
- 可能随机部分请求直接去 Azure

### 9.2 当前推荐策略

当前推荐保持：

- 外部可见 GPT-5 family 模型名 -> `sub2api`
- Azure direct route -> hidden `backup/...` alias
- `fallbacks` -> 指向 hidden backup alias

### 9.3 不支持参数兼容

当前建议保持：

- `drop_params: true`

至少对承接 GPT-5 family 的主 route 和 backup alias 都保留。

原因：

- 下游（例如 InterviewAI Copilot）会带 `temperature=0.7`
- LiteLLM 否则会在本地先报 `UnsupportedParamsError`
- 请求还没到 sub2api 就会 fallback

## 10. 日志维护注意事项

### 10.1 LiteLLM

当前建议：

- `LITELLM_LOG=INFO`
- `turn_off_message_logging=true`

### 10.2 sub2api

除了 deployment 里的 `LOG_LEVEL=info`，还要注意：

- dev 实例里存在 DB-backed runtime log config
- 这会覆盖 deployment env

如果看到 dev 上仍然刷 `DEBUG`，先检查：

- `/api/v1/admin/ops/runtime/logging`

必要时 reset：

- `/api/v1/admin/ops/runtime/logging/reset`

## 11. infra / Argo 维护注意事项

### 11.1 `targetRevision`

如果 dev 临时为了 feature 验证改过：

- `litellm-dev`
- `sub2api-openai-routing-service-dev`

验证完成后要记得切回：

- `targetRevision: main`

### 11.2 新 infra 改动工作流

对 `finalroundai/infra`：

- 一律新建 `/tmp` clone
- 基于 `main`
- 改动后发 PR

## 12. 什么时候应该考虑结束这个 feature branch

如果出现下面任一情况，说明应该考虑把核心代码并回主线，而不是继续长期漂着：

1. `main` 高频改到 config/router/openai handler
2. 这个 routing service 开始承担更多正式流量
3. dev / staging / prod 都开始依赖同一条能力
4. 每次 rebase 的人工成本明显上升

## 13. 最短维护清单

每次 feature branch 有改动并准备发 dev：

1. rebase `origin/main`
2. 跑 formal `/openai-routing` 定向测试
3. 构建并 push image
4. 更新 infra image tag
5. 等 Argo sync
6. 验证：
   - `/openai-routing/healthz`
   - `/v1/models`
   - `chat/completions`
   - GPT-5 family through LiteLLM
   - `temperature=0.7` compatibility
7. 检查日志：
   - LiteLLM 无异常 fallback
   - sub2api 不刷意外 DEBUG

## 14. 一句话原则

这条 feature branch 的维护核心不是“尽量少动”，而是：

```text
小步同步 main
保持 formal route 测试绿
每次发版都走 image + Argo + smoke 验证
不要让 LiteLLM 的 GPT-5 路由重新出现主备竞争
```
