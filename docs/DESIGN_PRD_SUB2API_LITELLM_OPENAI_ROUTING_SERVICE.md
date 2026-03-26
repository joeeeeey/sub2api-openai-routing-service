# Sub2API + LiteLLM + OpenAI Routing Service

本文档同时作为：

- Design Doc
- PRD
- DevOps deployment/operations note

适用范围：

- `sub2api` 作为 OpenAI-compatible routing service
- `litellm` 作为上游代理层
- `InterviewAI` / 其他客户端通过 OpenAI 兼容接口接入
- 上游真实请求最终走 ChatGPT Codex internal API

## 1. Ticket / Branch / Repo

### Linear ticket

- `DEVOPS-1196`
- 标题：
  `Deploy sub2api openai routing service to dev and validate litellm`

### 当前分支

- `sub2api`
  - repo: `my-project/sub2api`
  - branch: `feature/openai-routing-service-mvp`
  - 当前本地 HEAD: `e228ac65`
- `infra`
  - repo: `finalroundai/infra`
  - branch: `DEVOPS-1196`
  - 当前已推进到的最新提交：
    - `7f14cdb` `move sub2api routing to component and reduce log noise`
    - `9f3faa4` `relax litellm dev startup probes`
    - `f0cfc0b` `keep explicit codex routes with gpt-5 wildcard`
    - `0361cf3` `make gpt5 routes deterministic via sub2api first`
    - `e18745e` `drop unsupported params on sub2api gpt5 routes`
- `interviewai`
  - 本地联调使用过 `/tmp` clone 和分支 `feature/sub2api-openai-routing-local-v2`
  - 这个分支主要用于本地验证，不是 dev 环境上线依赖
  - dev 环境当前验证目标是不改 `latest dev` 业务代码

## 2. 当前目标

目标是把 `sub2api` 变成一个正式可部署的 OpenAI-compatible routing service，并放在 `litellm` 前面作为 GPT-5 family 的主上游：

```text
client (InterviewAI / curl / SDK)
  -> LiteLLM
  -> sub2api /openai-routing
  -> ChatGPT Codex internal API
```

要求：

- 对外暴露 OpenAI-compatible RESTful API
- 内部复用 `sub2api` 已有的：
  - OpenAI OAuth 登录态
  - 多账号池
  - 账号调度 / failover
  - refresh token
  - usage/billing/account routing
- 对 `GPT-5 family` 相关模型，默认优先走 `sub2api`
- 只有主路由失败时才 fallback 到原来的 Azure GPT 配置

## 3. Sub2API 当前架构

### 3.1 应用形态

`sub2api` 是一个 Go 服务，主体包含：

- HTTP server
- Admin/UI
- API key / user / group / account 管理
- 多上游账号路由
- billing / usage / dashboard
- OpenAI / Anthropic / Gemini / Antigravity 等平台网关

核心运行依赖：

- PostgreSQL
- Redis
- 可嵌入的前端资源

dev 部署里当前复用的是现有集群基础设施，但逻辑隔离：

- PostgreSQL host:
  `postgresql.dev-component.svc.cluster.local`
- 独立 DB:
  `sub2api_openai_routing`
- Redis host:
  `redis-master.dev-component.svc.cluster.local`
- 独立 Redis DB:
  `6`

### 3.2 当前对外服务接口

已接入正式 server 的 OpenAI-compatible 前缀：

- `GET /openai-routing/healthz`
- `GET /openai-routing/v1/models`
- `POST /openai-routing/v1/chat/completions`
- `POST /openai-routing/v1/responses`

路由注册位置：

- [openai_compat.go](/Users/joey/repos/my-project/sub2api/backend/internal/server/routes/openai_compat.go)

认证模式支持：

- `api_key`
- `static_key`
- `none`

当前 dev / formal 路径使用的是：

- `auth_mode=api_key`

也就是说，直接用 `sub2api` 自己 UI 创建的 API key 即可打 `/openai-routing/*`。

中间件位置：

- [openai_compat.go](/Users/joey/repos/my-project/sub2api/backend/internal/server/middleware/openai_compat.go)

### 3.3 sub2api 在当前方案里的职责

`sub2api` 在本方案里不是一个“简单转发代理”，而是负责：

- 接收 OpenAI-compatible 下游请求
- 认证 API key
- 校验 group 必须属于 OpenAI platform
- 从账号池里选取已登录的 OpenAI OAuth account
- 将 OpenAI-compatible 请求转换成内部/上游可接受格式
- 调用 ChatGPT Codex internal API
- 再把流式/非流式响应转换回 OpenAI-compatible 格式
- 记录 usage / billing / ops / dashboard

## 4. 上游真实接口：Codex Internal API

### 4.1 真实上游不是标准 OpenAI public API

当前 `sub2api` 在 OAuth 场景里打的真实上游不是 `api.openai.com/v1/chat/completions`，而是 ChatGPT/Codex internal API。

当前关键上游形态是：

```text
https://chatgpt.com/backend-api/codex/responses
```

也就是说，形式上更接近 `Responses API`，但带有 Codex internal 的约束。

### 4.2 与标准 Chat Completions 的差异

最关键差异如下。

#### 1. `system` 不直接留在 messages 里

标准 `chat.completions`：

```json
{
  "messages": [
    { "role": "system", "content": "act as assistant" },
    { "role": "user", "content": "how to fish" }
  ]
}
```

上游 Codex internal：

- 不直接接受 `role=system` 放在 `input[]`
- `sub2api` 会把 `system` 提取成顶层 `instructions`

代码位置：

- [openai_codex_transform.go](/Users/joey/repos/my-project/sub2api/backend/internal/service/openai_codex_transform.go)

#### 2. 输入主体是 `input[]` / Responses 风格，不是只有一个 string

Codex internal 不是“只有 prompt string”。

它支持：

- `input[]`
- role-based message
- multimodal content

类型定义：

- [types.go](/Users/joey/repos/my-project/sub2api/backend/internal/pkg/apicompat/types.go)

#### 3. 一批标准 Chat 参数在上游被移除

`sub2api` 在 OAuth internal transform 时会主动 strip 这些参数：

- `max_output_tokens`
- `max_completion_tokens`
- `temperature`
- `top_p`
- `frequency_penalty`
- `presence_penalty`

代码位置：

- [openai_codex_transform.go](/Users/joey/repos/my-project/sub2api/backend/internal/service/openai_codex_transform.go)

原因：

- ChatGPT Codex internal upstream 不支持，或者语义与标准 public API 不兼容
- 不移除会直接报上游错误

#### 4. `store=false` / `stream=true` 是强约束

OAuth internal transform 会强制：

- `store = false`
- `stream = true`

原因：

- internal upstream 对这两个字段有明确要求

代码位置：

- [openai_codex_transform.go](/Users/joey/repos/my-project/sub2api/backend/internal/service/openai_codex_transform.go)

#### 5. `reasoning_effort` 最终会变成 Responses/Codex 风格

下游 `chat.completions` 侧：

- `reasoning_effort`

转换后：

- `reasoning.effort`
- `reasoning.summary="auto"`

代码位置：

- [chatcompletions_to_responses.go](/Users/joey/repos/my-project/sub2api/backend/internal/pkg/apicompat/chatcompletions_to_responses.go)
- [openai_compat_request.go](/Users/joey/repos/my-project/sub2api/backend/internal/handler/openai_compat_request.go)

### 4.3 当前对 `reasoning_effort` 的策略

当前 formal compat 路径的默认策略是：

- 下游没传：
  - 默认补成 `low`
- 下游传了：
  - 当前 compat 层会保留这个值

但是对于我们当前在 LiteLLM 上承接的 GPT-5 family 路由，团队现在接受的行为是：

- 即使客户端想表达 `reasoning_effort=none`
- 当前服务仍统一按 `low` 处理

原因：

- 这批模型里有些 reasoning 语义在真实上游并不是完全可关闭的
- 我们目前优先追求“链路稳定 + 路由稳定 + 参数兼容性”，而不是完全保真地透传 `none`

### 4.4 多模态支持

Codex internal / Responses 这层支持：

- 文本
- 图片输入

类型在：

- [types.go](/Users/joey/repos/my-project/sub2api/backend/internal/pkg/apicompat/types.go)

## 5. OpenAI-Compatible 输入与 Codex Internal 输入示例

### 5.1 下游 `chat.completions`

```json
{
  "model": "gpt-5.4",
  "messages": [
    { "role": "system", "content": "act as assistant" },
    { "role": "user", "content": "how to fish" }
  ],
  "stream": true
}
```

### 5.2 `sub2api` 内部转换后的 Responses/Codex 风格

```json
{
  "model": "gpt-5.4",
  "instructions": "act as assistant",
  "input": [
    {
      "role": "user",
      "content": [
        { "type": "input_text", "text": "how to fish" }
      ]
    }
  ],
  "stream": true,
  "store": false,
  "reasoning": {
    "effort": "low",
    "summary": "auto"
  }
}
```

## 6. 当前 LLM 调用链路

### 6.1 正式链路

```text
client (InterviewAI / curl / OpenAI SDK)
  -> LiteLLM
  -> sub2api /openai-routing/v1/chat/completions
  -> ChatGPT Codex internal API
```

### 6.2 每一层职责

#### Client / InterviewAI

职责：

- 继续按 OpenAI-compatible 方式发起请求
- 对 InterviewAI 来说，仍然是走它当前的 LiteLLM endpoint
- 当前 dev 环境无需改 latest dev 业务代码

#### LiteLLM

职责：

- 暴露统一 `/chat/completions`
- 做 provider routing
- 做 fallback
- 管理上游 provider key / config

#### sub2api

职责：

- 承接 GPT-5 family 主流量
- 用 OpenAI OAuth account pool 转发到 ChatGPT internal API
- 做 account routing / usage / billing / dashboard / UI

#### ChatGPT Codex internal API

职责：

- 真实生成

## 7. 当前 GPT-5 路由与 Fallback 策略

### 7.1 设计原则

对我们显式声明支持的 Azure/OpenAI 相关 GPT-5 模型：

- 正常情况：
  - 100% 优先走 `sub2api`
- 主路由失败：
  - 才 fallback 到 Azure backup route

### 7.2 为什么之前会出现“部分直接走 Azure”

根因不是 `InterviewAI` 有特殊逻辑，而是 LiteLLM config 层有竞争路由：

- `sub2api` 主路由
- 同名或通配的 Azure 路由

LiteLLM 会把它们都当成同一 model group 的候选，结果不是严格“先主路由，失败再 fallback”，而是可能直接命中 Azure。

### 7.3 当前已落地的修正

当前 dev LiteLLM 的 GPT-5 family 路由已经改成：

- 外部可见模型名：
  - 只保留 `sub2api` 主路由
- Azure 旧配置：
  - 改成隐藏 `backup/azure/...` alias
- LiteLLM `fallbacks`：
  - 明确指向这些 hidden backup alias

这样正常情况下：

- 请求只会先匹配到 `sub2api`
- 只有主路由失败时，才进入 Azure backup

### 7.4 当前显式承接的模型

- `gpt-5`
- `gpt-5.1`
- `gpt-5.1-codex`
- `gpt-5.1-codex-max`
- `gpt-5.1-codex-mini`
- `gpt-5.2`
- `gpt-5.2-codex`
- `gpt-5.3-codex`
- `gpt-5.3-codex-spark`
- `gpt-5.4`
- `gpt-5.4-mini`
- `gpt-5.4-nano`
- 以及对应 `azure/...`

不在这一组里的模型，例如：

- `azure/gpt-5.4-pro`

仍保持 Azure 直连语义，不纳入“100% 先 sub2api”这一规则。

## 8. 为什么 InterviewAI Copilot 会触发 LiteLLM fallback

### 8.1 当前真实原因

`InterviewAI` latest dev 的 copilot 路径默认会带：

- `temperature = 0.7`

代码位置：

- [copilot.py](/tmp/interviewai-openai-routing-local-20260325192614/interviewai/v2/services/agent/orchestrated/copilot.py)

而 GPT-5 family 在 LiteLLM provider 校验里：

- 对某些模型不接受 `temperature=0.7`
- 再叠加我们 route 里固定了 `reasoning_effort: low`
- LiteLLM 会先在本地报 `UnsupportedParamsError`

这一步发生在发到 `sub2api` 之前。

所以表现上就是：

- LiteLLM 先报错
- 然后 fallback 到 backup Azure

### 8.2 这不是 InterviewAI 特殊 hack

从 `InterviewAI` 代码角度看，它只是正常发了：

- `temperature=0.7`
- `model=azure/gpt-5.2`

真正导致 fallback 的，是 LiteLLM 本地 provider 校验，而不是 `InterviewAI` 自己在应用层主动切换模型。

### 8.3 当前解决方式

当前在 LiteLLM config 已经做了：

- 对所有 sub2api GPT-5 主路由加 `drop_params: true`
- 对 hidden backup alias 也加同样设置

目的：

- 当下游带来 `temperature=0.7` / `top_p` 这类 GPT-5 不支持参数时
- LiteLLM 直接丢掉不支持参数
- 请求继续发往 sub2api
- 而不是先在 LiteLLM 本地报错再 fallback

## 9. 当前 dev 部署架构

### 9.1 sub2api

部署方式：

- ECR image
- ArgoCD application
- namespace: `component`

镜像仓库：

- `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service`

ArgoCD application：

- `sub2api-openai-routing-service-dev`

### 9.2 LiteLLM

部署方式：

- infra repo 里的现有 `litellm-dev`
- target branch currently points to `DEVOPS-1196`

ArgoCD application：

- `litellm-dev`

### 9.3 当前 cluster DNS

- LiteLLM:
  - `litellm.component.svc.cluster.local:4000`
- sub2api routing service:
  - `sub2api-openai-routing-service.component.svc.cluster.local:8080`

## 10. 本次改动对 main 的解耦程度

### 10.1 结论

整体上是 **相对解耦、可同步** 的，但不是“零冲突风险”。

### 10.2 为什么说相对解耦

`sub2api` 相对 `main` 的改动，大部分是新增或旁路式接入：

- 新的 compat route
- 新 middleware
- 新 handler helper
- 新部署文件
- 新 Dockerfile / build script
- 新本地 helper / local oauth tool
- 新文档

真正会和主线高频冲突的文件不多，主要是：

- `backend/internal/config/config.go`
- `backend/internal/server/router.go`
- `backend/internal/server/http.go`
- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/handler/openai_chat_completions.go`

### 10.3 为什么说不是零风险

因为这条能力还是直接接进了正式服务骨架：

- config
- server route
- gateway handler

如果 `main` 在这些区域继续大改，rebase/merge 时还是需要人工看。

### 10.4 当前建议的同步策略

建议后续同步 `main` 时按两层处理：

#### 第一层：prod-formal 相关核心改动

优先关注这些文件：

- `backend/internal/config/config.go`
- `backend/internal/server/router.go`
- `backend/internal/server/http.go`
- `backend/internal/server/middleware/openai_compat.go`
- `backend/internal/server/routes/openai_compat.go`
- `backend/internal/handler/openai_compat_request.go`

#### 第二层：本地工具与验证资产

这些通常冲突风险较小，可后处理：

- `backend/cmd/openai-oauth-client/*`
- `deploy/*`
- `docs/*`

### 10.5 一个现实判断

如果未来只想保正式服务，不想长期保本地 MVP/tooling：

- 可以把 `openai-oauth-client` 本地验证工具和 formal route 更明确拆层
- 但现阶段不建议为了“更漂亮的结构”先做大重构
- 当前目标是先稳住服务和链路

## 11. 当前已确认的 dev 行为

### 11.1 LiteLLM 日志

已调整为：

- `INFO`
- `turn_off_message_logging=true`

### 11.2 sub2api 日志

deployment ConfigMap 已是：

- `LOG_LEVEL=info`

但 dev 实例里还有一套 DB-backed runtime log config，会覆盖环境变量。

本次已经 reset 到 baseline `info`。

这件事非常重要，因为后续如果又看到 `DEBUG`，不一定是 Deployment 配错，更可能是 runtime config 被人改过。

## 12. 当前推荐运维结论

### 12.1 对团队共享时的结论

可以把当前方案理解为：

- `sub2api` 是 GPT-5 family 的正式主路由
- `LiteLLM` 只负责统一入口和 fallback
- `Azure backup` 是兜底，不是主流量路径

### 12.2 对产品/业务侧的结论

不需要修改 `InterviewAI latest dev` 业务代码，也可以接入这条链路。

但要接受当前 GPT-5 route 的两项兼容策略：

- 不支持参数由 LiteLLM 直接丢弃
- `reasoning_effort` 当前统一按 `low` 处理

### 12.3 对后续工程化的建议

后续建议继续补两件事：

1. 把当前 GPT-5 显式支持列表同步到 `applications/litellm/expected_models.yaml`
2. 把“runtime log config 会覆盖 deployment 的 LOG_LEVEL”写进运维 runbook

## 13. 一句话总结

当前方案已经从“本地实验性 proxy”推进成了“可部署、可观测、可 fallback 的正式架构”：

```text
client
  -> LiteLLM
  -> sub2api
  -> ChatGPT Codex internal API
```

其中：

- `sub2api` 负责账号池、OAuth、路由、usage、UI
- `LiteLLM` 负责统一入口和 fallback
- `Azure backup` 只作为失败兜底
