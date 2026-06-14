# Go + Ant Design 版产品规划

## 1. 规划目标

目标不是把现有 TypeScript 项目逐文件翻译成 Go，而是做一个新的、OpenAI-only 的产品，行为对齐 `app` 在 OpenAI/Codex 主链路上的能力：

- OpenAI OAuth 登录拿 token，不再手填 OpenAI API key
- 多账号池、自动刷新、状态机、并发控制
- OpenAI 兼容 API 对外暴露
- 内部调用 Codex backend
- 保留 `app` 已验证有效的业务逻辑
- 管理后台改为 React + Ant Design

同时明确收口：

- 不做 Anthropic
- 不做 Gemini
- 不做第三方 API key provider 路由
- 不做 Ollama bridge

这是一个“全新项目”，不是最小化改动式迁移。

## 2. 产品范围定义

### 2.1 Phase 1 必须对齐的能力

- OpenAI OAuth PKCE 登录
- Device Code 登录
- CLI `auth.json` 导入
- 手动 refresh token 导入
- 账号池管理
- 自动 token 刷新
- OpenAI 兼容 `/v1/chat/completions`
- OpenAI 兼容 `/v1/responses`
- `/v1/models`
- 会话亲和与 `previous_response_id`
- 隐式续链
- prompt cache key 管理
- `turnState` 继承
- rate limit / quota 被动采集
- dashboard 登录保护
- 代理池管理
- 日志与基础诊断
- 设置页与连接测试

### 2.2 明确不做

- Anthropic 协议入口
- Gemini 协议入口
- 多 provider API key catalog
- OpenRouter 之类上游适配器
- Ollama bridge
- 任何“手动配置 OpenAI API key 作为主上游凭证”的路径

## 3. 推荐产品结构

建议拆成两个独立工程，但共享一个产品目录：

- `server/`
  - Go 后端
- `web/`
  - React + Ant Design 前端

推荐原因：

- Go 后端需要长连接、调度器、状态机、代理转发
- Ant Design 天然是 React 生态，直接重建前端更合适
- 前后端分离后更容易替换 UI，不影响协议层

## 4. Go 后端架构规划

## 4.1 模块划分

建议按职责拆包：

- `internal/config`
  - 配置模型、加载、热重载、环境变量覆盖
- `internal/auth/oauth`
  - PKCE、device code、token exchange、refresh
- `internal/auth/session`
  - dashboard session、OAuth pending session
- `internal/accounts`
  - registry、状态机、持久化、选择器、生命周期
- `internal/affinity`
  - responseId / conversationId / turnState / functionCallIds 映射
- `internal/proxy/codex`
  - Codex backend client、HTTP SSE、WebSocket
- `internal/proxy/fingerprint`
  - 桌面指纹头构造
- `internal/proxy/cookies`
  - cookie jar
- `internal/proxy/transport`
  - HTTP client、代理、fallback、TLS/HTTP1.1 策略
- `internal/translation/openai`
  - chat/responses 到 codex request 的转换
- `internal/translation/schema`
  - structured output schema 预处理与 reconvert
- `internal/router/api`
  - `/v1/*` 路由
- `internal/router/auth`
  - `/auth/*`
- `internal/router/admin`
  - `/admin/*`
- `internal/logging`
  - 请求日志、检索
- `internal/stats`
  - usage snapshot
- `internal/proxies`
  - 代理池与健康检查
- `internal/tasks`
  - token refresh、session cleanup、model refresh、proxy check

## 4.2 技术选型

推荐：

- HTTP framework: `gin`
  - 若更重视中间件生态和 JSON 处理，选 `gin`
  - 若更重视简洁和标准库亲和，选 `chi`
- HTTP client:
  - 标准库 `net/http` + 自定义 `Transport`
  - WebSocket 用 `nhooyr.io/websocket` 或 `gorilla/websocket`
- 配置:
  - `yaml.v3`
- 持久化:
  - Phase 1 仍可使用 JSON/YAML 文件
  - 账号、代理、设置、日志索引可落盘到 `data/`
- 并发控制:
  - `sync.Mutex` / `RWMutex`
  - `context.Context`
  - channel + worker semaphore

推荐先不引入数据库。原因：

- 现有 `app` 就是文件持久化模型
- Phase 1 的复杂度不在 SQL，而在协议与状态机

## 5. 后端关键设计

### 5.1 认证中心

要完整实现 4 条入站认证路径：

1. OAuth PKCE popup
2. Device Code
3. CLI auth 导入
4. refresh token / token 手工导入

数据模型建议：

```go
type Account struct {
    EntryID              string
    UserID               string
    AccountID            string
    Email                string
    AccessToken          string
    RefreshToken         string
    ExpiresAt            time.Time
    PlanType             string
    Status               AccountStatus
    ProxyAPIKey          string
    Label                string
    LastRefreshAt        *time.Time
    RateLimitedUntil     *time.Time
    CachedQuota          *QuotaSnapshot
    Disabled             bool
    CreatedAt            time.Time
    UpdatedAt            time.Time
}
```

状态机至少保留：

- `active`
- `expired`
- `quota_exhausted`
- `rate_limited`
- `refreshing`
- `disabled`
- `banned`

### 5.2 Refresh 设计

这一块必须照着 `app` 的风险控制思路做：

- 每个账号独立刷新计划
- 刷新时从磁盘重新读取 refresh token
- 严格区分 pre-flight error 和 mid-flight error
- 只在安全场景下重试 refresh
- 失败采用指数退避

不要做：

- 多 goroutine 并发刷新同一账号
- 网络失败后立即重复提交 refresh
- 导入后马上高频调用上游 usage 接口做验证

### 5.3 账号池与调度器

需要 3 个核心组件：

- `Registry`
  - 管账号元数据和持久化
- `Allocator`
  - 按模型/状态/亲和性挑账号
- `Lifecycle`
  - 管 acquire/release 和并发槽位

选择策略要保留：

- preferred account
- tier priority
- rotation strategy
- `max_concurrent_per_account`
- `request_interval_ms`

### 5.4 Codex Client

Go 版 `CodexClient` 需要同时实现：

- HTTP SSE 请求
- WebSocket 请求
- `codex/usage`
- model fetch

关键行为：

- 构造桌面指纹头
- 维护 cookie jar
- 捕获并写回 `Set-Cookie`
- 支持 per-account proxy
- 支持 direct fallback

其中最重要的兼容点：

- `previous_response_id` 场景必须走 WS
- `service_tier` 发给 Codex backend 前必须剥离
- reasoning 请求自动补 `include: ["reasoning.encrypted_content"]`

### 5.5 转发编排层

这一层建议作为独立服务 `ProxyOrchestrator`，负责：

- 鉴权
- 请求翻译
- 账号 acquire
- 隐式续链判断
- prompt cache key 生成
- turnState 注入
- 上游调用
- rate limit header 解析
- streaming / non-streaming 结果转换
- 空响应重试
- 错误分类和换账号
- usage 记账
- session affinity 写回

如果不把这些逻辑聚合到一个编排层，Go 版很容易退化成“多个 handler 里 scattered if/else”，后期难维护。

### 5.6 Schema 兼容层

必须单独抽一层，不建议散落在 handler 内。

保留能力：

- `json_object` / `json_schema` 翻译
- `additionalProperties: false` 注入
- tuple schema 的预处理
- 响应文本回写时的 reconvert

### 5.7 Dashboard 会话与接口鉴权

建议保留两套鉴权：

- Dashboard:
  - cookie session
  - 登录密码 = `proxy_api_key`
- API:
  - `Authorization: Bearer <proxy_api_key 或 account proxyApiKey>`

这和 `app` 的产品行为一致，也便于内外网使用。

## 6. Ant Design 前端规划

## 6.1 技术栈

推荐：

- React
- Ant Design
- React Router
- TanStack Query
- Zustand 或 Redux Toolkit

## 6.2 页面信息架构

建议保留但收口为 6 个主页面：

1. 概览
2. 账号管理
3. 代理池
4. 使用统计
5. 日志
6. 设置

明确移除：

- API Keys 页面
- Anthropic / Gemini setup
- Ollama bridge 配置

## 6.3 页面详细规划

### 概览

- 账号总数 / active / expired / banned / rate-limited
- 最近请求数
- 当前默认模型
- 最近告警
- 快速发起 OAuth 登录

### 账号管理

- OAuth 登录按钮
- Device Code 登录
- 导入 CLI auth
- 手动导入 refresh token
- 列表页展示：
  - 账号邮箱
  - plan
  - 状态
  - 最近刷新时间
  - quota 摘要
  - 代理分配
  - 标签
- 行级操作：
  - 刷新
  - 禁用/启用
  - 删除
  - 编辑标签
  - 查看 cookies

### 代理池

- 代理列表
- 健康状态
- 账号到代理绑定
- 全局代理设置

### 使用统计

- 请求量
- input/output token
- cached token
- reasoning token
- 账号维度聚合

### 日志

- 请求列表
- 筛选条件
  - 时间
  - 模型
  - 状态码
  - streaming / non-streaming
- 详情面板

### 设置

- 服务设置
  - host / port
- 鉴权设置
  - `proxy_api_key`
- 模型设置
  - 默认模型
  - 默认 reasoning
- 转发设置
  - `request_interval_ms`
  - `max_concurrent_per_account`
- 刷新设置
  - `refresh_enabled`
  - `refresh_margin_seconds`
  - `refresh_concurrency`
- 日志设置
- quota 告警设置
- 连接测试
- API 接入示例

## 7. API 规划

### 7.1 认证与账号

- `GET /auth/status`
- `POST /auth/login-start`
- `POST /auth/code-relay`
- `GET /auth/callback`
- `POST /auth/device-login`
- `GET /auth/device-poll/:deviceCode`
- `POST /auth/import-cli`
- `POST /auth/token`
- `POST /auth/logout`

- `GET /auth/accounts`
- `POST /auth/accounts`
- `POST /auth/accounts/:id/refresh`
- `PATCH /auth/accounts/:id`
- `DELETE /auth/accounts/:id`
- `POST /auth/accounts/import`
- `GET /auth/accounts/export`
- `GET /auth/accounts/:id/quota`
- `GET /auth/accounts/:id/cookies`
- `PUT /auth/accounts/:id/cookies`

### 7.2 Dashboard

- `POST /auth/dashboard-login`
- `POST /auth/dashboard-logout`
- `GET /auth/dashboard-status`

### 7.3 API 兼容

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/responses/compact`
- `GET /v1/models`
- `GET /v1/models/catalog`

### 7.4 Admin

- `GET/POST /admin/settings`
- `GET/POST /admin/general-settings`
- `GET/POST /admin/rotation-settings`
- `GET/POST /admin/quota-settings`
- `GET /admin/logs`
- `GET /admin/usage-stats`
- `POST /admin/test-connection`
- `GET /health`
- `GET /debug/diagnostics`

## 8. 数据持久化规划

Phase 1 仍采用文件持久化：

- `data/accounts.json`
- `data/proxies.json`
- `data/local.yaml`
- `data/logs/*.jsonl` 或 ring buffer snapshot
- `data/models-cache.yaml`

建议：

- 账号、代理、设置独立文件
- 日志使用 append-only + 最近 N 条索引
- affinity/session 可先用内存，进程重启不强求恢复

## 9. 关键技术风险与验收点

### 9.1 OAuth 兼容

验收点：

- popup 登录可完成
- 本地回调端口行为正确
- `code/state` 不会被重复消费

风险：

- URL 编码不一致导致 OpenAI auth 失败
- 本地回调端口被占用

### 9.2 refresh token 安全

验收点：

- refresh 不中断
- 网络失败不会导致 token 被复用作废

风险：

- 错误重试设计不当导致账号集体失效

### 9.3 WS 与 previous_response_id

验收点：

- 带 `previous_response_id` 的请求可稳定继续同一链路
- WS 失败时不会错误降级为 SSE

### 9.4 Cloudflare / cookies / fallback

验收点：

- 账号设置 cookies 后可稳定请求
- 代理异常时可 direct fallback

### 9.5 协议翻译正确性

验收点：

- Chat Completions 与 Responses 两套接口都可用
- tools、tool outputs、image parts 正确翻译
- structured outputs 返回格式与客户端预期一致

### 9.6 转发业务逻辑一致性

验收点：

- 隐式续链可命中
- prompt cache key 可复用
- turnState 可继承
- 限流头能更新本地账号状态
- 空响应会跨账号重试

## 10. 实施顺序建议

### Milestone 1: 核心认证与账号池

- 配置系统
- 账号模型与持久化
- OAuth PKCE
- refresh scheduler
- dashboard login

### Milestone 2: Codex Client 与 `/v1/responses`

- HTTP SSE
- WebSocket
- cookie jar
- direct fallback
- `/v1/responses`

### Milestone 3: `/v1/chat/completions` 与 schema 兼容

- chat -> codex 翻译
- codex -> openai 流式转换
- structured output 预处理与 reconvert

### Milestone 4: 转发编排增强

- affinity
- implicit resume
- rate limit 同步
- 空响应重试
- 模型 plan 过滤

### Milestone 5: Ant Design 后台

- 概览
- 账号管理
- 代理池
- 日志
- 统计
- 设置

### Milestone 6: 联调与验收

- 浏览器 OAuth 实测
- OpenAI SDK 接入实测
- 多账号轮转实测
- 代理场景实测
- 高并发和断网恢复测试

## 11. 最终建议

对于这个新项目，最合理的产品定义不是“TS app 的所有功能原样搬运到 Go”，而是：

- 保留 `app` 中真正有价值且已被验证的 OpenAI/Codex 主链路能力
- 去掉多 provider 和历史兼容包袱
- 用 Go 重做状态机、转发编排和调度器
- 用 Ant Design 重做管理后台

这样做的收益是：

- 产品边界更清晰
- 后端逻辑更适合长期维护
- 不会把 Anthropic / Gemini / API key provider 的历史设计继续带进新项目

