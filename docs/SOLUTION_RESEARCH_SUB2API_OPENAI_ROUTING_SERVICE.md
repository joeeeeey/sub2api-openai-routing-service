# SOLUTION_RESEARCH_SUB2API_OPENAI_ROUTING_SERVICE

## 1. 研究问题

你当前真正需要的不是“再造一个本地 OpenAI 兼容 server”，而是：

> 一个可以尽快上线、对后端测试开放、同时复用 `sub2api` 现有 OpenAI Codex OAuth 账号池和多账号路由能力的 RESTful OpenAI 兼容服务。

核心决策问题：

1. 在 `sub2api` 里继续加功能并暴露新服务能力
2. 借鉴代码独立写一个新项目


## 2. 已知事实

### 2.1 `sub2api` 已经有的能力

`sub2api` 已经原生具备：

- OpenAI OAuth/Codex 登录状态管理
- token refresh
- 账号池持久化
- 多账号调度
- failover
- session stickiness
- OpenAI `chat.completions` / `responses` / Codex internal 兼容转换

这些能力并不是简单的 HTTP 转发，而是带有大量已有行为和边界条件。

### 2.2 你真正 care 的东西

你明确说了：

- 你 **不 care** credit 计算
- 你 **care**：
  - 账号登录
  - 登录状态记录
  - 多个 Codex 账号之间的路由

这意味着：

- 价值核心在 `sub2api` 现有账号与路由体系
- 不是“OpenAI 兼容 HTTP 接口”本身


## 3. 方案候选

### 方案 A：继续在 `sub2api` 内新增 OpenAI 兼容服务能力

做法：

- 在 `sub2api` 现有服务里新增一组 OpenAI 兼容 endpoint
- 直接复用现有账号池、token refresh、账号调度、failover、粘连
- 通过配置决定：
  - 是否开放
  - 是否鉴权
  - 是否计费
  - 路由到哪个 group

### 方案 B：独立新项目，借鉴 `sub2api` 代码重写

做法：

- 新建一个小项目
- 拷贝 `sub2api` 中 OpenAI OAuth、转换、路由逻辑
- 自己维护账号池与 refresh

### 方案 C：独立薄包装服务，但底层依赖 `sub2api` 的库/内部接口

做法：

- 新项目只负责 REST 接口壳子
- 底层尽量 import `sub2api` 内部包或通过内部 RPC 调用 `sub2api`


## 4. 对比分析

### 4.1 上线速度

#### 方案 A

最快。

原因：

- 账号管理和路由逻辑不需要重建
- 只需要补一层接口和配置
- 现在我们已经有了 `oauth-local-server` 的原型验证

#### 方案 B

最慢。

原因：

- 虽然看起来“独立干净”，但实际上要重新解决：
  - 账号存储
  - refresh
  - 路由
  - failover
  - 上游兼容

#### 方案 C

理论上次快，但实现复杂度不一定比 A 低。

原因：

- 要先把 `sub2api` 内部能力模块化
- 否则会落到“新项目 import 一堆 internal 逻辑”的尴尬形态


### 4.2 风险

#### 方案 A

风险：

- 要谨慎隔离现有 `sub2api` 正式路径与新测试路径
- 必须用 config/route prefix/auth mode 控制 blast radius

但这些风险是可控工程风险。

#### 方案 B

风险更高。

原因：

- 一旦拷贝逻辑，后续 `sub2api` 修 bug / 调整路由策略时，两边会快速漂移
- 你很快会维护两套“几乎一样但不完全一样”的 OpenAI OAuth 系统

#### 方案 C

最大风险在边界模糊。

原因：

- 如果只是“薄壳”，最后大概率还是要不断反向依赖 `sub2api`
- 结果是多出一个部署单元，但没有真正换来清晰边界


### 4.3 复用价值

#### 方案 A

复用价值最高。

能直接复用：

- DB 中账号记录
- refresh token 生命周期
- 调度策略
- 模型映射
- Codex internal 兼容逻辑

#### 方案 B

复用价值最低。

本质是在重复造轮子。

#### 方案 C

复用价值中等，但实现前提是 `sub2api` 先抽象稳定接口。


### 4.4 维护成本

#### 方案 A

最低。

因为：

- 所有 OpenAI OAuth 相关行为只维护一份

#### 方案 B

最高。

因为：

- 每次协议变化、模型变化、refresh 行为变化都要同步两份系统

#### 方案 C

中等偏高。

因为：

- 多一个仓库 / 服务 / 部署 / 观测面


## 5. 结论

### 推荐结论

**推荐方案 A：继续在 `sub2api` 内新增 OpenAI 兼容服务能力。**

这是最符合你当前目标的方案：

- 你最 care 的是账号池和路由
- 这两者都已经在 `sub2api`
- 你要尽快上线做后端测试
- 所以不应该在这个阶段分叉出第二套系统

### 为什么不是独立项目

因为你现在要的核心价值不在“HTTP 接口层”，而在：

- 账号登录态
- 多账号调度
- refresh
- failover

这些都在 `sub2api` 里。

独立项目只会让你为“表面隔离”付出很大的重复实现成本。


## 6. 推荐落地形态

### 推荐形态：同仓库、同核心逻辑、可单独部署

不是简单说“全塞进主路径”。

推荐是：

- **代码仍然放在 `sub2api` 仓库**
- **复用现有 service / repository / account routing**
- **但可以通过独立配置或独立启动模式单独部署**

也就是：

- 代码上统一
- 部署上可以独立

这样你同时得到：

- 快速上线
- 逻辑复用
- 部署隔离


## 7. 建议实现方式

### Phase 1：最快上线方案

在 `sub2api` 里新增一组路由：

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/models`
- `/healthz`

并加配置：

- `openai_compat_service.enabled`
- `openai_compat_service.auth_mode`
- `openai_compat_service.route_group_id`
- `openai_compat_service.skip_billing`
- `openai_compat_service.default_reasoning_effort`

内部处理：

- 复用已有 OpenAI Gateway / OAuth token provider / account selector
- 不重新建第二套 refresh 和 routing
- 默认 `reasoning_effort=low`

### Phase 2：减少对现有路径侵入

如果你担心主 server blast radius，可以做成：

- 同 repo 内的第二个启动模式
- 例如 `cmd/server --mode openai-compat`

或者：

- 第二个 binary
- 但 binary 仍复用同一批 internal packages

### Phase 3：再考虑真正拆服务

只有在以下前提同时成立时，才建议再拆独立项目：

1. 流量规模已经明显增长
2. 测试服务和主服务生命周期强烈不同
3. 鉴权 / quota / SLA 要求和主服务显著不同
4. `sub2api` 内部已经完成模块化，拆出去不会复制核心逻辑


## 8. 推荐架构判断

### 短期判断

**不要独立重写。**

### 中期判断

**可以独立部署，但不要独立实现。**

### 长期判断

如果未来这个“OpenAI 兼容服务”成长为正式产品，再考虑：

- 从 `sub2api` 中抽出共享包
- 新服务依赖共享包

但这不是当前最优先的路径。


## 9. 你现在最合适的下一步

建议按这个顺序推进：

1. 在 `sub2api` 里把现有 `oauth-local-server` 原型升级成“复用账号池路由”的正式 server 能力
2. 先允许 dev / internal test 使用
3. 默认关闭 billing 或弱化 billing
4. 验证 InterviewAI / LiteLLM / OpenAI SDK 的接入稳定性
5. 只有当这条路径跑通后，再讨论是否拆独立部署或独立服务


## 10. 最终建议（一句话）

**如果你要尽快上线给后端测试，最佳选择不是独立新项目，而是继续在 `sub2api` 上加一个 OpenAI 兼容服务模式，复用它现有的账号管理和多 Codex 账号路由能力。**

