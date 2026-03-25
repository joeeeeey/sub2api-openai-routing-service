# REQUIREMENTS_SUB2API_OPENAI_ROUTING_SERVICE

## 1. 背景

当前 `sub2api` 已经具备以下现成能力：

- OpenAI Codex / ChatGPT OAuth 登录态管理
- 多 OpenAI OAuth 账号持久化存储
- 账号级 token refresh
- 多账号路由、失败切换、会话粘连
- OpenAI `chat.completions` / `responses` / Codex internal API 兼容转换

当前新增的 `oauth-local-server` 证明了一点：

- 可以对外暴露 OpenAI 兼容的 RESTful endpoint
- 可以服务于 OpenAI SDK / LiteLLM / InterviewAI 的 `chat.completions`

但这个本地 server 仍然是“单机、单状态文件、开发联调工具”，并没有复用 `sub2api` 的账号池与路由能力。

因此下一阶段需求是：

> 在 `sub2api` 里新增一个可对后端测试暴露的 OpenAI 兼容 RESTful 服务能力，复用现有账号管理、登录状态与多账号路由能力，但不以 credit / billing / end-user quota 作为第一优先级。


## 2. 目标

### 2.1 P0 目标

- 基于现有 `sub2api` 服务端暴露 OpenAI 兼容接口
- 支持：
  - `GET /healthz`
  - `GET /v1/models`
  - `POST /v1/chat/completions`
  - `POST /v1/responses`
- 请求进入后，复用 `sub2api` 现有的：
  - OpenAI OAuth 账号池
  - 多账号调度
  - token refresh
  - failover
  - 会话粘连 / session stickiness
- 支持下游用 OpenAI SDK 直接调用
- 支持后端测试场景，优先保证可用性和集成效率

### 2.2 P0.5 目标

- `reasoning_effort` / `reasoning.effort` 明确策略统一
- 无论下游传不传，服务端总能得到一个最终 effort 值
- 默认值固定为 `low`
- 日志中可明确看到：
  - 下游原始值
  - 最终值
  - 值来源（request / extra_body / default）

### 2.3 P1 目标

- 提供可配置 auth（静态 key 或内部网关鉴权）
- 提供可配置 group / route policy
- 可选跳过 billing / usage persistence
- 可选按部署环境隔离一组“测试专用 OpenAI OAuth 账号”


## 3. 非目标

本阶段不做：

- 面向公网的开放平台产品化
- 自助用户管理 / 多租户控制台
- 完整 OpenAI API 全接口复刻
- 完整计费体系重构
- 与现有 Sub2API 用户 credit 体系强绑定


## 4. 使用场景

### 4.1 后端测试

后端服务可直接配置：

```python
from openai import OpenAI

client = OpenAI(
    api_key="internal-test-key",
    base_url="https://<sub2api-host>/v1",
)
```

调用：

- `client.chat.completions.create(...)`

### 4.2 LiteLLM / InterviewAI

可将：

- `LITELLM_API_ENDPOINT`

指向该服务，实现：

- `chat.completions`
- 流式 token 输出
- system -> instructions 转换
- OpenAI 兼容 SSE

### 4.3 内部模型联调

开发者可快速验证：

- 多账号路由是否正确
- Codex OAuth 账号是否仍可服务普通 RESTful OpenAI 客户端
- thinking / reasoning 参数是否对齐


## 5. 核心需求

### 5.1 接口兼容

必须支持：

- OpenAI Chat Completions 请求/响应
- OpenAI Responses 请求/响应
- OpenAI SSE chunk 风格流式输出

### 5.2 路由与账号管理

必须复用现有 `sub2api` 的：

- 账号表 / 凭证存储
- OpenAI OAuth token refresh
- 多账号选择器
- 上游 429 / 5xx failover
- 会话粘连

不接受重新实现一套独立账号池和刷新逻辑。

### 5.3 billing / usage

第一阶段优先级：

- **路由与可用性 > 计费**

要求：

- 可以不做完整 credit 扣减
- 可以不走现有 end-user billing 流程
- 但建议仍保留最小 usage log / debug trace，便于排障

建议策略：

- 新 endpoint 默认 `skip_billing=true`
- 可配置 `persist_usage_log=true/false`

### 5.4 认证

阶段性要求：

- P0 可先支持无 auth 或静态开发 key
- 必须可通过配置关闭无 auth 模式
- 生产上线前至少应支持：
  - 静态 internal key
  - 或现有 API key / gateway auth 复用

### 5.5 reasoning / thinking

统一策略：

- `chat.completions`：
  - 若下游传 `reasoning_effort`，保留
  - 若没传，默认 `low`
- `responses`：
  - 若下游传 `reasoning.effort`，保留
  - 若没传，默认 `low`
- 服务端日志必须打印：
  - `downstream_reasoning_effort`
  - `effective_reasoning_effort`
  - `reasoning_effort_source`

### 5.6 可观测性

必须有：

- request_id
- route path
- selected account / group
- upstream request id
- model
- stream=true/false
- reasoning effort
- first token latency
- failover / retry 记录


## 6. 配置需求

建议新增一组配置：

```yaml
openai_compat_service:
  enabled: true
  auth_mode: none | static_key | api_key
  static_key: ""
  route_group_id: <group_id>
  skip_billing: true
  persist_usage_log: true
  default_reasoning_effort: low
  listen_path_prefix: /v1
```

最重要的是：

- `route_group_id`

因为这是复用现有多账号调度能力的关键入口。


## 7. 推荐验收标准

### 7.1 基础接口

- `GET /healthz` 返回 200
- `GET /v1/models` 返回可用模型列表
- `POST /v1/chat/completions` 非流式返回 200
- `POST /v1/chat/completions` 流式返回标准 chunk + `[DONE]`
- `POST /v1/responses` 非流式返回 200

### 7.2 路由能力

- 在配置了多个 OpenAI OAuth 账号时，请求能正常被调度
- 某一账号失效或上游报错时，请求可切到其他账号
- 同一会话请求能表现出稳定路由行为

### 7.3 OAuth 能力

- access token 过期时自动 refresh
- refresh 后后续请求继续成功

### 7.4 reasoning 行为

- 下游没传时，上游明确收到 `low`
- 下游显式传 `high` 时，上游收到 `high`
- 日志中能看到 source=`default` 或 `request`

### 7.5 InterviewAI 验收

- `Live Interview` 场景下
- `chat.completions` 可以真正返回流式 token
- 不因 usage-only chunk 崩溃
- OpenAI SDK 能正常消费 stream


## 8. 成功标准

如果以下条件同时满足，则认为方案成立：

1. 内部后端服务可以把 `base_url` 指向 `sub2api`
2. 无需单独维护第二套 OpenAI OAuth 账号状态
3. 多账号路由与 refresh 逻辑沿用现有实现
4. reasoning effort 行为一致且可观测
5. 不被 credit / billing 逻辑阻塞上线节奏

