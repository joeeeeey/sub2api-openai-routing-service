# PRD: Sub2API OpenAI Routing Service Deployment and LiteLLM Upstream Integration

核验时间: 2026-03-25  
核验范围: `sub2api` 代码库、`infra` 代码库、Datadog 只读 API  
文档状态: Draft  
负责人: Joey Jiang / Codex

## 1. 背景

当前本地已经验证了下面这条链路可工作:

- `InterviewAI -> local V2 backend (:5002) -> sub2api /openai-routing -> OpenAI OAuth account`

并且本地进一步验证了:

- `sub2api /openai-routing/v1/*` 已经兼容 OpenAI SDK 请求格式
- LiteLLM 可把 `sub2api /openai-routing/v1` 当成 OpenAI 上游来使用
- `azure/gpt-5.2` / `azure/gpt-5.4` 模型通过 LiteLLM proxy 可成功完成非流式与流式调用

下一步目标不是继续停留在本地手工调试，而是把 `sub2api openai-routing-service` 变成一个可部署到 FRAI dev cluster 的服务，支持:

- OpenAI OAuth 登录态与账号池路由
- OpenAI 兼容 REST API
- 通过 UI 管理 group / API key / usage
- 作为 LiteLLM 的一个上游 provider 接入

## 2. 目标

### 2.1 产品目标

1. 在 FRAI dev cluster 部署一个可用的 `sub2api-openai-routing-service`
2. 支持通过 UI 登录 / 管理 OpenAI OAuth 账号，并给 `/openai-routing/*` 直接使用
3. 支持通过本地 port-forward 访问 UI 和 OpenAI 兼容 API
4. 支持 LiteLLM 将其作为 OpenAI-compatible upstream 使用
5. 支持 `gpt-*` / `azure/gpt-*` 流式模型调用验证

### 2.2 工程目标

1. 产出一个适合部署到 EKS 的 amd64 Docker image
2. image push 到:
   `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service:<hash>`
3. infra repo 中增加 ArgoCD/Kustomize 管理方式
4. 尽量复用 dev cluster 现有 PG / Redis 基础设施，不新增不必要的 STS / IRSA
5. 保留 OpenAI OAuth 账号池、路由、refresh、usage 统计能力

## 3. 非目标

1. 本阶段不做公网 Ingress
2. 本阶段不做 Helm chart 交付
3. 本阶段不做生产环境 rollout
4. 本阶段不做多 region / canary / autoscaling 优化
5. 本阶段不要求把所有 InterviewAI 模型都切到该服务，仅先证明 LiteLLM + OpenAI 兼容路径成立

## 4. 事实矩阵

| 主张 | 状态 | 证据 | 备注 |
|---|---|---|---|
| `sub2api` 已有可用 Docker 构建资产 | 事实 | `Dockerfile`, `Dockerfile.goreleaser`, `.goreleaser.yaml` | 当前 Dockerfile 带前端构建与嵌入式 UI |
| `sub2api` 已有正式 OpenAI 兼容路由 | 事实 | 本地已验证 `/openai-routing/v1/chat/completions`, `/openai-routing/v1/models` | 当前本地 8080 已跑通 |
| `sub2api` 本地 UI 可管理 OpenAI group / API key | 事实 | 本地已验证登录 UI、创建绑定 OpenAI group 的 key、通过 `/openai-routing/*` 使用 | 当前工作流已打通 |
| `infra` dev 侧现有服务主要用 ArgoCD + Kustomize 管理 | 事实 | `applications/interviewai-services/.../base|dev/kustomization.yaml` | 不是 Helm chart 直接部署模式 |
| `infra` 中已有可复用的 component PostgreSQL / Redis | 事实 | `helmfile-infra/helmfiles/component/postgresql.yaml`, `redis.yaml`, `litellm-postgresql.yaml` | 可复用集群内 DB/Redis 体系，但实例/库名需隔离 |
| `interviewai-core` dev overlay 使用 ECR image + ServiceAccount patch | 事实 | `applications/interviewai-services/interviewai-core/dev/*.yaml` | 适合作为新服务的参考模板 |
| Datadog 只读凭据当前可用 | 事实 | `dd_ro.py validate -> {"valid": true}` | 可用于后续补 dashboard/monitor |
| 当前未发现现成 `sub2api-openai-routing-service` dashboard | 推断 | Datadog dashboard list 中未见相关 dashboard 名称 | 后续可单独补 |
| LiteLLM 可把 `sub2api /openai-routing/v1` 当 OpenAI 上游使用 | 事实 | 本地 LiteLLM proxy 在 `:4002` 已成功跑通 `models` / stream / non-stream | 本地验证完成 |

## 5. 现状核验摘要

### 5.1 `sub2api` 现状

- 当前仓库已有两套 Docker 构建资产:
  - [Dockerfile](/Users/joey/repos/my-project/sub2api/Dockerfile)
  - [Dockerfile.goreleaser](/Users/joey/repos/my-project/sub2api/Dockerfile.goreleaser)
- 当前默认 Dockerfile 是:
  - frontend build
  - backend embed build
  - runtime image
- 当前 runtime image 还附带 `pg_dump` / `psql`，适合完整 `sub2api` 部署，但对 routing-only service 不是必需
- 当前本地已存在正式 compat 路由与 UI 组合启动方式:
  - `make openai-routing-ui-local`

### 5.2 `infra` 现状

- `interviewai-services` 当前不是 Helm chart 直装，而是 ArgoCD 管的 Kustomize 目录
- dev overlay 结构已存在成熟样例:
  - `applications/interviewai-services/interviewai-core/base`
  - `applications/interviewai-services/interviewai-core/dev`
- image 注入方式是 `kustomization.yaml -> images:` 覆盖 ECR tag
- ServiceAccount / imagePullSecret / deployment patch 都在 dev overlay 独立 patch

### 5.3 dev cluster 数据依赖现状

- 端口转发管理脚本表明 dev cluster 现有可复用依赖包括:
  - Apollo config service
  - PostgreSQL
  - Redis
  - Redis Stack
- 本地实测:
  - `pf-start` 后 `4000 / 5100 / 6379 / 6381` 可正常监听
- 说明 dev cluster 中已有共享基础组件，新的 routing service 不需要为了“先跑起来”再引入一套新的 PG/Redis 控制面

## 6. 需求

### 6.1 镜像构建与发布

需要一个面向 `sub2api-openai-routing-service` 的生产可部署镜像:

1. 目标平台: `linux/amd64`
2. 镜像仓库:
   `507254053937.dkr.ecr.us-west-2.amazonaws.com/finalroundai/sub2api-openai-routing-service:<git-hash>`
3. 需要配置 30 天自动删除策略
4. Dockerfile 需要符合最佳实践:
   - 多阶段构建
   - 仅保留运行所需文件
   - 优先使用 Go 静态二进制
   - 可接受 root 用户运行
   - 不要求把 pg client 一并带入

### 6.2 部署方式

不做 Helm chart。最终以 infra repo 中的 ArgoCD + Kustomize 目录管理为准。

目标部署形态:

1. 在 `infra` repo 新增一套 `applications/...` 目录
2. 包含:
   - base deployment/service/serviceaccount/config
   - dev overlay
3. dev 阶段先不做 Ingress
4. 通过 `kubectl port-forward` 访问:
   - UI
   - `/openai-routing/v1/*`

### 6.3 数据依赖

第一阶段希望复用 dev cluster 现有 PG / Redis 基础设施，但数据库逻辑隔离:

1. Redis: 可直接复用现有实例，使用独立 key namespace
2. PostgreSQL: 复用现有 PG server，但使用独立 database/schema/credentials
3. 不为第一阶段单独新增新的 STS / IRSA，只在确有 AWS API 访问需求时再评估

### 6.4 OpenAI OAuth 账号同步

第一阶段需要把“当前本地已登录的 OpenAI OAuth 账号”同步到 dev 部署实例中，以便:

1. UI 中能直接看到已有 OpenAI account
2. 通过 UI 创建的 API key 能直接跑 `/openai-routing/*`
3. LiteLLM 上游测试能用真实路由账号池

## 7. 建议方案

### 7.1 镜像方案

建议新增一套 routing-service 专用 Dockerfile，例如:

- `Dockerfile.routing-service`

设计原则:

1. 仍复用 frontend embed 构建
   - 因为需要 UI
2. backend 仅构建 `cmd/server`
3. runtime image 尽量精简:
   - 可选 `alpine`
   - 或更极致的 distroless 方案
4. 首阶段建议用 `alpine`，原因:
   - 更容易排障
   - 保留 `wget/curl/ca-certs/tzdata`
   - 不必为了 rootless 专门处理 volume 权限

建议同时新增一个简单 build/push 脚本，例如:

- `deploy/build_routing_service_image.sh`

行为:

1. 读取当前 git hash
2. build `linux/amd64`
3. tag 到目标 ECR
4. push 到 ECR

### 7.2 ECR 生命周期策略

建议在 ECR 仓库上设置 lifecycle policy:

1. 匹配 `sub2api-openai-routing-service:*`
2. 保留最近 30 天内的镜像
3. 自动删除更老的 image tag

### 7.3 infra repo 部署结构

建议新增一套新的 ArgoCD/Kustomize 应用目录，例如:

```text
applications/sub2api-openai-routing-service/
  base/
    deployment.yaml
    service.yaml
    serviceaccount.yaml
    configmap.yaml
    kustomization.yaml
  dev/
    kustomization.yaml
    deployment-patch.yaml
    serviceaccount-patch.yaml
    imagepullsecret-patch.yaml
```

设计上直接复用 `applications/interviewai-services/interviewai-core/dev` 的模式:

1. `base` 定义通用 Deployment/Service
2. `dev` overlay 覆盖:
   - namespace
   - ECR image tag
   - configMapRef
   - serviceAccount annotations
   - imagePullSecrets

### 7.4 配置方案

建议 dev deploy 至少暴露这些配置项:

#### 必需

- `OPENAI_COMPAT_SERVICE_ENABLED=true`
- `OPENAI_COMPAT_SERVICE_AUTH_MODE=api_key`
- `OPENAI_COMPAT_SERVICE_PATH_PREFIX=/openai-routing`
- `OPENAI_COMPAT_SERVICE_DEFAULT_REASONING_EFFORT=low`
- `DATA_DIR`
- `TOTP_ENCRYPTION_KEY`

#### 连接现有基础设施

- `database.host / port / db / user / password`
- `redis.host / port / db`

#### dev 环境建议

- `server.mode=release` 或显式 dev-friendly logging
- `ops` 可先关闭或降配
- `pricing` 远端同步失败不应阻塞主链路

### 7.5 PG / Redis 复用策略

建议:

1. PostgreSQL
   - 复用 dev cluster 现有 PG server
   - 新建独立 database，例如 `sub2api_openai_routing`
   - 独立用户名密码
2. Redis
   - 复用现有 Redis
   - 独立 DB index 或明确前缀空间
3. 不单独部署新的 PG/Redis StatefulSet
4. 这样第一阶段无需引入新的数据库运维面

### 7.6 OpenAI OAuth 同步方案

建议不要手工在生产 UI 上重新登录一次当前账号。  
第一阶段采用“导入已存在本地 OAuth state”的方式:

1. 从当前本地 `.openai-routing-service/state` 或 OAuth state 文件读取 token
2. 通过一次性 job / admin import 脚本把账号写入 dev 环境的 `accounts` 表
3. 把它绑定到目标 OpenAI group

候选实现方式:

1. 一次性 Kubernetes Job
   - 跑 `openai-oauth-client provision-sub2api-local` 的服务化变体
2. 或单独 admin import 命令
   - 更适合长期维护

### 7.7 LiteLLM 上游接入方案

结论: 可以直接把 `sub2api /openai-routing/v1` 作为 LiteLLM 的 OpenAI-compatible upstream。

本地已验证可行配置:

```yaml
model_list:
  - model_name: gpt-5.2
    litellm_params:
      model: openai/azure/gpt-5.2
      api_base: http://127.0.0.1:8080/openai-routing/v1
      api_key: <SUB2API_UI_CREATED_KEY>
      stream_timeout: 60

  - model_name: gpt-5.4
    litellm_params:
      model: openai/azure/gpt-5.4
      api_base: http://127.0.0.1:8080/openai-routing/v1
      api_key: <SUB2API_UI_CREATED_KEY>
      stream_timeout: 60
```

本地已核验:

1. `GET /v1/models`
2. `POST /v1/chat/completions` 非流式
3. `POST /v1/chat/completions` 流式

均成功。

因此后续可以支持两种接法:

1. 业务服务直接打 `sub2api /openai-routing/v1`
2. 业务服务先打 LiteLLM，再由 LiteLLM 转发到 `sub2api`

## 8. 验收标准

### 8.1 本地镜像与部署验收

1. 能在本地构建 amd64 image
2. 能 push 到目标 ECR 仓库
3. ECR 仓库存在 30 天 lifecycle policy

### 8.2 dev cluster 部署验收

1. ArgoCD 应用能成功同步
2. Pod 正常 Running / Ready
3. Service 可通过 port-forward 访问
4. `GET /openai-routing/healthz` 返回 `{"ok":true}`
5. UI 可打开并登录

### 8.3 OpenAI 兼容 API 验收

1. `/openai-routing/v1/models` 返回 `gpt-*`
2. `/openai-routing/v1/chat/completions` 非流式成功
3. `/openai-routing/v1/chat/completions` 流式成功
4. 默认 `reasoning_effort=low` 行为可观察
5. usage / first_token_ms / request logs 可在系统内看到

### 8.4 LiteLLM 上游验收

1. LiteLLM proxy 可读取该上游配置
2. `GET /v1/models` 可列出映射模型
3. 通过 LiteLLM 调用 `gpt-*` 模型的 stream / non-stream 成功
4. InterviewAI 指向 LiteLLM 后可最小闭环成功

## 9. 风险与待核实项

### 9.1 风险

1. `sub2api` pricing / remote sync 任务可能带来启动噪音
2. OpenAI OAuth token 导入到 dev 环境需要安全处理
3. dev 共用 PG/Redis 时，资源隔离和命名规范需要控制好
4. LiteLLM 对长流式请求的 timeout 配置需要单独调优

### 9.2 待核实

1. dev PG 是否更适合独立 database 还是独立 schema
2. 是否已有统一的 ECR lifecycle policy Terraform 模块可直接复用
3. ArgoCD app 放在 `applications/sub2api-openai-routing-service` 还是 `applications/interviewai-services` 下更符合现有分类
4. dev 环境是否需要单独 Datadog dashboard / monitor

## 10. 分阶段执行建议

### P0: 交付部署骨架

1. 新增 routing-service 专用 Docker build/push 方案
2. 新建 ECR repo + lifecycle policy
3. 在 infra repo 新增 base/dev Kustomize
4. 通过 ArgoCD 部署到 dev
5. port-forward 到本地能打开 UI 与 healthz

### P1: 导入账号并跑通 API

1. 导入当前 OpenAI OAuth 账号到 dev 实例
2. UI 创建 API key
3. 本地 `curl` 打 `/openai-routing/*` 成功

### P2: 接 LiteLLM

1. 准备 LiteLLM config
2. 让 LiteLLM 指向 `sub2api /openai-routing/v1`
3. 验证 `gpt-*` 的 stream / non-stream

### P3: 接业务方

1. InterviewAI 切到 LiteLLM or 直连 `sub2api`
2. 跑最小 E2E
3. 根据 TTFT / error rate 补日志与监控

## 11. 结论

基于当前核验，推荐路线是:

1. `sub2api` 继续作为正式 OpenAI-compatible routing service
2. 部署方式用 `Docker + ECR + infra repo Kustomize/ArgoCD`
3. 第一阶段复用 dev cluster 现有 PG / Redis，不再新增一套 DB/STS/Helm 管理面
4. LiteLLM 作为上游适配层是可行且已本地验证成功的

这条路线能最快把本地 POC 推进到 dev cluster 可验证服务。
