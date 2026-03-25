# PRD_OPENAI_OAUTH_LOCAL_SERVER

## 1. 背景

当前仓库已经有一个本地 CLI：

- `bash scripts/openai-oauth-client.sh login`
- `bash scripts/openai-oauth-client.sh chat ...`

它可以：

- 通过 OpenAI OAuth 浏览器授权获取本地开发态 token
- 将请求直接发到 `https://chatgpt.com/backend-api/codex/responses`
- 自动在项目目录内保存和刷新 OAuth 状态

但它目前只有 CLI 调用模式，不支持作为本地 HTTP Server 暴露 OpenAI 兼容接口，因此无法直接给 OpenAI SDK / LiteLLM / InterviewAI 侧的 `chat.completions.create(...)` 调用。

目标是把它扩展成一个**本地开发用 OpenAI 兼容 Server**，优先服务于：

- `OpenAI(..., base_url="http://127.0.0.1:<PORT>/v1")`
- `client.chat.completions.create(...)`
- `curl` 手工联调
- `InterviewAI` 本地联调与 E2E 验收

参考接入点：

- `/Users/joey/repos/finalroundai/interviewai/interviewai/data_clients/llm_client.py`


## 2. 问题定义

InterviewAI 当前调用方式是 OpenAI SDK 风格，核心请求形态是：

- `POST /v1/chat/completions`
- `messages`
- `stream`
- `stream_options.include_usage`
- `tools`
- `tool_choice`
- `metadata`
- `extra_body`
- 某些场景会带 `system_prompt`
- 某些场景会带多模态 `image_url`

而当前 OpenAI OAuth 直连上游是 Codex internal API，核心语义是：

- 上游实际 endpoint 为 `chatgpt.com/backend-api/codex/responses`
- `system` 不能直接作为消息数组传递，需要转成顶层 `instructions`
- 请求主结构是 `responses/input[]`，不是 `chat.completions/messages[]`
- 对 `store`、`stream`、若干采样参数、事件流结构有额外约束

因此需要一个本地 server，帮开发环境自动完成：

1. OpenAI SDK `chat.completions` -> Responses 请求体转换
2. `system` -> `instructions`
3. OpenAI 兼容 SSE -> Codex internal SSE 的双向转换
4. OAuth token 自动刷新
5. 本地调试日志输出


## 3. 目标

### 3.1 P0 目标

- 为当前 `openai-oauth-client` 增加 `server` 模式
- 默认监听本地回环地址，例如 `127.0.0.1:38080`
- 暴露 OpenAI 兼容接口：
  - `GET /healthz`
  - `GET /v1/models`
  - `POST /v1/chat/completions`
- 无需任何 auth 即可调用
- 即使调用方带了 `Authorization: Bearer xxx`，也不校验，仅忽略或记录脱敏日志
- 自动使用项目内 `.openai-oauth-client/state.json` 中的 OAuth 状态
- token 过期时自动 refresh
- 支持非流式与流式两种 `chat.completions`
- 支持将 `messages` 中的 `system` 自动转成 `instructions`
- 支持 `messages[].content` 为 string 或 OpenAI 多模态数组
- 支持 `tools` / `tool_choice`
- 支持 `stream_options.include_usage=true`
- Debug 日志尽量详细，优先便于本地排障

### 3.2 P1 目标

- 额外暴露 `POST /v1/responses` 作为直接调试入口
- 支持更完整的多轮 `assistant` / `tool` / `function` 消息回放
- 支持请求/响应 debug dump 到本地文件（可开关）
- 提供一组 `make` 命令用于启动和 curl smoke test


## 4. 非目标

本期不做：

- 生产级鉴权、租户隔离、配额管理
- 公网暴露
- 多用户并发共享同一个本地 OAuth 状态
- 高可用、持久化队列、进程守护
- WebSocket 协议兼容
- 完整复刻 OpenAI 全部 endpoint


## 5. 用户故事

### 5.1 本地 curl 联调

作为开发者，我希望在本机启动一个本地 server，然后直接用 curl 发：

- `POST /v1/chat/completions`
- `stream=true/false`
- `messages=[system,user]`

并收到标准 OpenAI Chat Completions 风格响应。

### 5.2 OpenAI SDK 兼容

作为开发者，我希望可以这样写：

```python
from openai import OpenAI

client = OpenAI(
    api_key="dev-local",
    base_url="http://127.0.0.1:38080/v1",
)

resp = client.chat.completions.create(
    model="gpt-5.4",
    messages=[
        {"role": "system", "content": "act as assistant"},
        {"role": "user", "content": "how to fishing"},
    ],
    stream=True,
)
```

并且无需知道后面实际上走的是 Codex internal API。

### 5.3 InterviewAI 本地联调

作为开发者，我希望将 `InterviewAI` 的 LiteLLM endpoint 指到本地 server 后，不修改其调用模型：

- `make pf-start`
- `make dev`
- interview copilot 正常走通
- 后续可继续跑 E2E 验证


## 6. 核心方案

### 6.1 新增命令

在当前 `backend/cmd/openai-oauth-client/main.go` 基础上新增：

- `openai-oauth-client server`

对应 shell 包装：

- `bash scripts/openai-oauth-client.sh server`

建议默认参数：

- `--listen 127.0.0.1:38080`
- `--state-file /Users/joey/repos/my-project/sub2api/.openai-oauth-client/state.json`
- `--log-level debug`

### 6.2 接口设计

#### `GET /healthz`

返回：

```json
{"ok": true}
```

用途：

- 启动探活
- Makefile smoke test

#### `GET /v1/models`

返回 OpenAI 兼容模型列表，优先复用仓库已有模型清单。

至少包含：

- `gpt-5.4`
- `gpt-5.4-mini`
- `gpt-5.4-nano`
- `gpt-5.3-codex`
- `gpt-5.2-codex`
- `gpt-5.1-codex`

#### `POST /v1/chat/completions`

这是本期主接口。

输入：

- OpenAI Chat Completions 标准 body

输出：

- 非流式：标准 `chat.completion`
- 流式：标准 `chat.completion.chunk` SSE

#### `POST /v1/responses`（P1）

直接接受 Responses 风格 body，便于手工调试与排障。


## 7. 字段与语义转换

### 7.1 `messages` -> `instructions + input[]`

输入示例：

```json
{
  "model": "gpt-5.4",
  "messages": [
    {"role": "system", "content": "act as assistant"},
    {"role": "user", "content": "how to fishing"}
  ],
  "stream": true
}
```

内部转换后目标语义：

```json
{
  "model": "gpt-5.4",
  "instructions": "act as assistant",
  "input": [
    {
      "role": "user",
      "content": [
        {"type": "input_text", "text": "how to fishing"}
      ]
    }
  ],
  "stream": true,
  "store": false
}
```

转换规则：

- `role=system` 不保留在消息数组里
- 所有 system message 合并到顶层 `instructions`
- 若没有 `system`，则填充默认 `instructions`

### 7.2 多模态支持

OpenAI SDK 可能传：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "describe this image"},
    {
      "type": "image_url",
      "image_url": {
        "url": "data:image/png;base64,...",
        "detail": "high"
      }
    }
  ]
}
```

服务端应转换为 Responses/Codex input：

- `text` -> `input_text`
- `image_url` -> `input_image`

说明：

- 本期要求至少支持 InterviewAI 当前使用的 data URL 形式图片输入
- 图片内容可直接透传为 data URL，不要求本期做文件缓存

### 7.3 tools / tool_choice

服务端应支持：

- `tools`
- `tool_choice`
- 兼容 legacy `functions` / `function_call`（如果调用方仍在使用）

对于 Chat Completions SSE，需按 OpenAI SDK 习惯输出：

- `choices[].delta.tool_calls`

### 7.4 stream 与 usage

要求：

- 上游永远走流式，服务端负责：
  - 若客户端请求非流式，则在服务端收完整个上游 SSE 再组装返回
  - 若客户端请求流式，则服务端将上游 Responses/Codex SSE 转回 OpenAI chat chunk SSE
- 当客户端指定 `stream_options.include_usage=true` 时，服务端需在最终 chunk 输出 usage

### 7.5 容错字段

考虑到 OpenAI SDK / Langfuse / LiteLLM 兼容层可能透出以下字段：

- `metadata`
- `extra_body`
- `web_search_options`
- `langfuse_prompt`
- `user`
- `reasoning_effort`

本期策略：

- 不因为这些字段而 400
- 已知兼容字段尽量做映射
- 未识别字段记录 debug log 后忽略

特别说明：

- `reasoning_effort` 顶层字段应映射为 `reasoning.effort`
- `metadata` 至少应进入日志上下文
- `extra_body.user` 应进入日志上下文，并在可能时透传/映射


## 8. 与 InterviewAI 的兼容要求

目标接入文件：

- `/Users/joey/repos/finalroundai/interviewai/interviewai/data_clients/llm_client.py`

从该文件观察到的兼容要求：

- `client.chat.completions.create(...)`
- `stream=True/False`
- `stream_options={"include_usage": True}`
- `messages` 支持：
  - 仅 user
  - system + user
  - image mode
- `tools`
- `tool_choice="auto"`
- `metadata`
- `extra_body`

本地 server 在 P0 必须保证：

- 这些字段不会导致请求失败
- 主路径 copilot 文本对话可用
- stream 输出能被其现有 token 回调消费


## 9. 日志与调试要求

本期要求默认 debug log 较多，优先排障友好。

### 9.1 启动日志

至少打印：

- listen 地址
- state file 路径
- 当前账号 email
- plan type
- chatgpt_account_id
- token 是否即将过期

### 9.2 请求日志

每个请求至少打印：

- 本地 request_id
- method / path
- client ip
- stream=true/false
- model
- messages 数量
- 是否包含 system
- 是否包含 image
- tool 数量
- metadata.trace_id / session_id（若存在）
- extra_body.user（若存在）

注意：

- 不打印完整 access token
- Authorization 若存在，仅打印是否存在或前缀脱敏
- 图片 data URL 只打印长度，不打印内容

### 9.3 转换日志

至少打印：

- system 是否被提取为 instructions
- instructions 长度
- 最终上游 model
- 是否启用 store=false
- 是否强制 stream=true
- 是否忽略未知字段

### 9.4 上游日志

至少打印：

- token refresh 是否发生
- 上游 URL
- 上游状态码
- 上游 request id
- 首 token 延迟
- 结束原因


## 10. Makefile 需求

建议新增以下 target：

### 根目录 Makefile

- `make oauth-local-server`
  - 启动本地 OpenAI 兼容 server

- `make oauth-local-healthcheck`
  - curl `GET /healthz`

- `make oauth-local-chat-test`
  - curl 一个非流式 chat.completions 请求

- `make oauth-local-chat-stream-test`
  - curl 一个流式 chat.completions 请求

### 推荐命令定义（示意）

```make
oauth-local-server:
	@bash scripts/openai-oauth-client.sh server --listen 127.0.0.1:38080

oauth-local-healthcheck:
	@curl -s http://127.0.0.1:38080/healthz

oauth-local-chat-test:
	@curl -s http://127.0.0.1:38080/v1/chat/completions \
		-H 'Content-Type: application/json' \
		-d '{...}'

oauth-local-chat-stream-test:
	@curl -N http://127.0.0.1:38080/v1/chat/completions \
		-H 'Content-Type: application/json' \
		-d '{...}'
```


## 11. curl 验收用例

### 11.1 healthz

```bash
curl -s http://127.0.0.1:38080/healthz
```

期望：

- `200`
- `{"ok":true}`

### 11.2 models

```bash
curl -s http://127.0.0.1:38080/v1/models
```

期望：

- `200`
- 返回 OpenAI 兼容 models 列表

### 11.3 非流式 chat.completions

```bash
curl -s http://127.0.0.1:38080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      {"role": "system", "content": "act as assistant"},
      {"role": "user", "content": "how to fishing"}
    ],
    "stream": false
  }'
```

期望：

- `200`
- 返回标准 `chat.completion`

### 11.4 流式 chat.completions

```bash
curl -N http://127.0.0.1:38080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      {"role": "system", "content": "act as assistant"},
      {"role": "user", "content": "say hello in 5 words"}
    ],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'
```

期望：

- `200`
- SSE 输出为 OpenAI Chat Completions chunk 风格
- 最终 chunk 含 usage（若请求了 `include_usage`）

### 11.5 多模态 image

```bash
curl -s http://127.0.0.1:38080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "describe the image"},
          {
            "type": "image_url",
            "image_url": {
              "url": "data:image/png;base64,<BASE64>",
              "detail": "high"
            }
          }
        ]
      }
    ],
    "stream": false
  }'
```

期望：

- `200`
- 请求不会因多模态结构而 400


## 12. InterviewAI 联调验收

### 12.1 接入方式

将 InterviewAI 的 LiteLLM/OpenAI endpoint 指向：

- `LITELLM_API_ENDPOINT=http://127.0.0.1:38080/v1`

并允许任意 dummy key，例如：

- `LITELLM_API_KEY=dev-local`

### 12.2 本地验收流程

1. 在 `sub2api` 仓库执行：
   - `bash scripts/openai-oauth-client.sh login`
   - `make oauth-local-server`

2. 在 `interviewai` 仓库中强制本地 endpoint：
   - `LITELLM_API_ENDPOINT=http://127.0.0.1:38080/v1`
   - `LITELLM_API_KEY=dev-local`

3. 启动 InterviewAI：
   - `make pf-start`
   - `make dev`

4. 跑 interview copilot / 本地 E2E

### 12.3 验收标准

- InterviewAI 发出的 `chat.completions.create(...)` 请求可成功返回
- stream 模式下 token 能正常逐步输出
- 非 stream 模式下能正常拿到 `response.choices[0].message.content`
- system prompt 能正确生效
- image mode 请求不会因为结构不兼容而失败
- tools 请求不会因为字段不兼容而失败
- 服务器日志能清楚看到请求与转换过程


## 13. 实现建议

### 13.1 代码组织

建议将当前 CLI 扩展为：

- `login`
- `chat`
- `server`
- `show-state`
- `logout`

服务端逻辑建议抽到独立文件，而不是全部堆在 `main.go` 中。

建议新增：

- `backend/cmd/openai-oauth-client/server.go`
- `backend/cmd/openai-oauth-client/handlers_chat_completions.go`
- `backend/cmd/openai-oauth-client/handlers_models.go`
- `backend/cmd/openai-oauth-client/logging.go`

### 13.2 可复用仓库现有逻辑

优先复用现有实现，而不是重新造轮子：

- Chat Completions -> Responses 转换
- Responses -> Chat Completions 响应转换
- Codex OAuth request transform
- model normalization
- usage 提取

优先复用代码来源：

- `backend/internal/pkg/apicompat/*`
- `backend/internal/service/openai_codex_transform.go`
- `backend/internal/service/openai_gateway_chat_completions.go`


## 14. 风险

- OpenAI SDK / Langfuse / LiteLLM 可能附带额外字段，本地 server 若解析过严会出现兼容性问题
- ChatGPT internal API 对字段要求比标准 Responses 更严格，尤其是：
  - `instructions`
  - `store=false`
  - `stream=true`
- 多模态和 tool call 的流式事件映射如果不完整，InterviewAI 可能出现 silent failure
- 本地开发无 auth 模式必须限制在 loopback 地址监听，避免误暴露


## 15. 交付定义

本 PRD 对应的实现完成后，应至少交付：

- `openai-oauth-client server` 子命令
- `scripts/openai-oauth-client.sh server`
- `GET /healthz`
- `GET /v1/models`
- `POST /v1/chat/completions`
- `Makefile` 启动和 curl 测试 target
- Debug 日志
- README / docs 中补充本地使用说明

