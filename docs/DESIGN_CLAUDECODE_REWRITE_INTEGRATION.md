# Claude Code Rewrite Integration for Sub2API

## 1. 背景

当前 `sub2api` 已经具备以下能力：

- Anthropic 多账号池
- 粘性会话 / 会话摘要路由
- 账号级 failover
- Anthropic OAuth token refresh
- Claude Code 客户端识别
- `metadata.user_id` 重写
- 账号级 header fingerprint / TLS fingerprint

与此同时，参考仓库：

- `/Users/joey/repos/finalroundai/cc-gateway`

已经实现了一套更激进、以 Claude Code 请求重写为中心的逻辑，重点包括：

- system prompt / `<system-reminder>` 中的环境文本改写
- `x-anthropic-billing-header` 的 system block 清理
- 路径、工作目录、home 目录前缀重写
- `metadata.user_id` 内 `device_id` 的规范化
- `x-anthropic-billing-header` HTTP header 清理

本次工作的目标不是迁移 `cc-gateway` 的单账号代理模型，而是：

> 保留 `sub2api` 现有多 Anthropic 账号池、路由、并发控制、failover、token refresh，
> 仅把 `cc-gateway` 的 rewrite request 逻辑作为一个新的 Go 模块接入，
> 并让 rewrite 语义以 `cc-gateway` 为主。


## 2. 目标

### 2.1 核心目标

- 保留 `sub2api` 当前的多 Anthropic 账号池与调度实现
- 保留 `SelectAccountWithLoadAwareness`、sticky session、failover
- 新增一个独立的 Go rewrite 模块
- 对 Claude Code CLI 直连 `sub2api` 的请求，在转发到 Anthropic 上游前执行 rewrite
- rewrite 规则优先对齐 `cc-gateway`

### 2.2 本阶段覆盖范围

仅覆盖 Claude Code CLI 直连场景的关键链路：

- `POST /v1/messages`
- `POST /v1/messages/count_tokens`

补充说明：

- `POST /api/event_logging/batch` 当前在 `sub2api` 中已经被本地吞掉并直接返回 `200`
- 因此不需要把 `cc-gateway` 的 event logging rewrite 逻辑照搬进上游转发链路


## 3. 非目标

本阶段不做：

- 改写 `sub2api` 的账号池 / 调度 / failover 逻辑
- 改写 `/openai-routing/*`
- 改写 OpenAI gateway / Gemini gateway / Antigravity gateway
- 重新设计 `metadata.user_id` 的编码格式
- 接入 `platform.claude.com` 的额外代理逻辑
- 把 `cc-gateway` 的 Node/TS 工程结构迁移到 Go


## 4. 两个项目的关系

### 4.1 `cc-gateway` 的角色

`cc-gateway` 在这项工作中是：

- rewrite 语义参考实现
- regex / prompt rewrite 规则来源
- billing header 处理策略来源

它不是：

- 最终运行时
- Anthropic 多账号池
- 账号调度器

### 4.2 `sub2api` 的角色

`sub2api` 是最终运行时，继续负责：

- API key 鉴权
- Anthropic 多账号池
- 会话路由
- failover
- token refresh
- usage / billing / ops

结论：

- `cc-gateway` 提供 rewrite 规则
- `sub2api` 提供生产级账号池与路由


## 5. 当前 Claude Code 链路

Claude Code CLI 直连 `sub2api` 时，当前主链路是：

```text
Claude Code CLI
  -> /v1/messages
  -> GatewayHandler.Messages
  -> ParseGatewayRequest
  -> Claude Code detection
  -> session hash
  -> SelectAccountWithLoadAwareness
  -> GatewayService.Forward
  -> buildUpstreamRequest
  -> api.anthropic.com/v1/messages
```

`count_tokens` 链路：

```text
Claude Code CLI
  -> /v1/messages/count_tokens
  -> GatewayHandler.CountTokens
  -> ParseGatewayRequest
  -> SelectAccountForModel
  -> GatewayService.ForwardCountTokens
  -> buildCountTokensRequest
  -> api.anthropic.com/v1/messages/count_tokens
```

这意味着：

- 账号选择先完成
- rewrite 应该发生在“账号已选定”之后，“真正发上游请求”之前


## 6. 设计原则

### 6.1 路由与 rewrite 解耦

路由层不改，rewrite 层新增。

也就是：

- handler 层继续负责解析请求、检测 Claude Code、选账号、控制并发
- rewrite 模块只处理 body/header

### 6.2 `metadata.user_id` 采用混合策略

这里不能简单把 `cc-gateway` 原逻辑原封不动搬过来。

原因：

- `sub2api` 已支持 Claude Code 新旧两种 `metadata.user_id` 格式
- `sub2api` 已有 `session_id_masking_enabled`
- `sub2api` 已有基于账号的 `account_uuid` / `claude_user_id` 适配

因此采用混合策略：

- `metadata.user_id` 的“生成 / 格式化 / masking”继续由 `sub2api` 现有 `IdentityService` 负责
- 在此基础上，引入 `cc-gateway` 风格的补充 rewrite：
  - 若 `metadata.user_id` 是 JSON 格式，则将其中 `device_id` 规范到 canonical device id
  - 保留 `account_uuid` / `session_id`

### 6.3 rewrite 按账号生效，不是全局单配置

`cc-gateway` 的默认模型是“全局单 canonical identity”。

`sub2api` 不是这样。

在 `sub2api` 中，canonical profile 应按账号构建：

- 一个上游 Anthropic 账号，对应一个 canonical fingerprint / device profile
- 不同账号之间可以不同
- 同一账号下的不同 client 请求，在 rewrite 后尽量一致


## 7. 新模块设计

建议新增模块：

- `backend/internal/service/claude_code_rewrite_service.go`

### 7.1 模块职责

负责：

- Claude Code request body rewrite
- Claude Code upstream header rewrite
- 构造 canonical rewrite profile

不负责：

- 账号选择
- 并发控制
- failover
- token refresh

### 7.2 建议接口

建议包含以下概念：

- `ClaudeCodeRewriteProfile`
- `BuildClaudeCodeRewriteProfile(account, fingerprint)`
- `RewriteClaudeCodeBody(body, path, profile)`
- `RewriteClaudeCodeUpstreamHeaders(req, profile)`


## 8. Rewrite 规则

### 8.1 本次要对齐 `cc-gateway` 的规则

#### `/v1/messages` / `/v1/messages/count_tokens`

- 重写 system prompt 中的：
  - `Platform: ...`
  - `Shell: ...`
  - `OS Version: ...`
  - `Working directory: ...`
  - `/Users/<name>/` / `/home/<name>/` 前缀
- 仅改 `<system-reminder>` 内的 message 文本，不改普通用户输入
- 清理 system 中纯 `x-anthropic-billing-header:` block
- 清理单字符串 system 中内联 `x-anthropic-billing-header: ...`
- 清理 body 顶层的：
  - `baseUrl`
  - `base_url`
  - `gateway`
- 规范化 `metadata.user_id` 中 JSON 结构的 `device_id`

#### headers

- 删除 `x-anthropic-billing-header`
- 强制上游 `User-Agent` 使用 canonical Claude CLI 形式

### 8.2 本次不照搬的规则

#### event logging 重写

不迁移。

原因：

- `sub2api` 已在 `/api/event_logging/batch` 直接返回 `200`

#### OAuth 注入逻辑

不迁移。

原因：

- `sub2api` 已有 `ClaudeTokenProvider` 与 `GatewayService` 处理 token refresh / auth

#### 多客户端 token 鉴权逻辑

不迁移。

原因：

- `sub2api` 已有 API key / group / subscription / user context


## 9. 推荐接入点

### 9.1 `Forward`

在 `GatewayService.Forward` 中：

- 保留现有：
  - `shouldMimicClaudeCode`
  - `injectClaudeCodePrompt`
  - `normalizeClaudeOAuthRequestBody`
  - model mapping
  - token 获取

- 新增：
  - 当请求已识别为真实 Claude Code 客户端时，调用新的 rewrite 模块重写 `body`

### 9.2 `buildUpstreamRequest`

在 `buildUpstreamRequest` 中：

- 保留：
  - 白名单 headers 透传
  - token 注入
  - fingerprint 应用
  - beta header 逻辑

- 新增：
  - 对真实 Claude Code 请求执行 upstream header rewrite

### 9.3 `ForwardCountTokens` / `buildCountTokensRequest`

同步接入相同 rewrite 逻辑。


## 10. 实现边界

### 10.1 保留不动的现有行为

- 多账号选择
- sticky session
- failover
- OAuth token refresh
- 账号级 TLS fingerprint
- 账号级 `metadata.user_id` 生成 / masking

### 10.2 允许调整的现有行为

- Claude Code 请求的 system prompt 清洗
- Claude Code 请求的 billing header system block 处理
- Claude Code 请求的 upstream `User-Agent`


## 11. 测试策略

### 11.1 新增单元测试

新增 Go 单元测试覆盖：

- system string 中 billing header 被清理
- system array 中 billing header block 被清理
- `<system-reminder>` 内路径/env 被改写
- `metadata.user_id` JSON 中 `device_id` 被规范化
- header 中 `x-anthropic-billing-header` 被清理
- upstream `User-Agent` 被规范到 canonical 值

### 11.2 回归测试

必须确保：

- 非 Claude Code 请求行为不变
- 多账号选择不变
- 现有 `IdentityService` tests 仍通过
- `count_tokens` 仍可正常工作


## 12. 验收标准

满足以下条件即视为本阶段完成：

1. Claude Code CLI 连接 `sub2api` 时，Anthropic 上游请求在 body/header 层吃到新的 rewrite 模块
2. `sub2api` 原有多账号池与路由逻辑完全保留
3. `event_logging/batch` 仍然本地吞掉，不发上游
4. 单元测试覆盖新的 rewrite 规则
5. 现有 Anthropic gateway 测试不因无关路径被破坏


## 13. 后续扩展

后续若需要进一步增强，可考虑：

- 允许账号级配置 canonical prompt env：
  - `cc_rewrite_platform`
  - `cc_rewrite_shell`
  - `cc_rewrite_os_version`
  - `cc_rewrite_working_dir`
- 增加 admin UI 开关：
  - 是否启用 Claude Code rewrite
  - 是否 strip billing header
- 增加调试 endpoint / ops 字段，直接显示 rewrite 后的上游请求摘要
