# App 项目功能与实现梳理

## 1. 项目定位

`app` 不是一个“把请求原样转发到 OpenAI API”的简单代理，而是一个带管理后台的本地网关服务，核心职责包括：

- 通过 OpenAI OAuth / Device Code / CLI token 导入获取账号凭证
- 维护多账号池、并发控制、轮转、自动刷新、状态机
- 将 OpenAI 兼容请求翻译为 `chatgpt.com/backend-api/codex/responses`
- 为转发链路补充自己的业务逻辑：会话亲和、隐式续链、限流缓存、空响应重试、结构化输出修正
- 提供后台页面用于账号管理、代理池、日志、统计、设置

从代码结构上看，它是“认证中心 + 账号池 + Codex 协议翻译层 + 管理后台”的组合体，而不是纯 API relay。

## 2. 整体架构

入口在 `app/src/index.ts`。服务启动时会初始化并挂载：

- 认证相关：`AccountPool`、`RefreshScheduler`
- 网络相关：`CookieJar`、`ProxyPool`、TLS transport
- 模型相关：`ModelStore`、后台模型刷新
- 观测相关：`UsageStatsStore`、日志捕获
- 路由：
  - `/auth/*`
  - `/auth/accounts/*`
  - `/admin/*`
  - `/v1/chat/completions`
  - `/v1/responses`
  - `/v1/models`
  - 以及 Anthropic / Gemini / API key / Ollama 相关入口

实现方式是单进程内聚式服务：配置文件、账号持久化、会话、刷新任务、代理检查都在同一进程内完成。

## 3. 配置体系

配置定义在 `app/src/config-schema.ts`，运行时由 `app/src/config-loader.ts` 加载：

- 基础配置来自 `app/config/default.yaml`
- 本地覆盖来自 `data/local.yaml`
- 部分字段可被环境变量覆盖

关键配置域：

- `api`: 上游 base URL
- `client`: 桌面客户端指纹参数
- `model`: 默认模型、默认 reasoning、是否注入 desktop context
- `auth`: OAuth 端点、client_id、刷新策略、并发、轮转策略
- `server`: host、port、`proxy_api_key`
- `tls`: 全局代理、是否强制 HTTP/1.1
- `logs`: 日志采集配置
- `quota`: 配额阈值与跳过 exhausted 策略
- `update`: 自更新
- `providers`: OpenAI / Anthropic / Gemini / custom provider 配置

结论：

- `app` 是配置驱动型产品
- 但 OpenAI 主链路不是“读取一个 API key 后直连官方 API”，而是“读取 OAuth 配置后使用账号 token 调 Codex backend”

## 4. 认证能力

### 4.1 OpenAI OAuth PKCE 登录

主流程在 `app/src/routes/auth.ts` + `app/src/auth/oauth-pkce.ts`：

1. 前端调用 `POST /auth/login-start`
2. 服务端生成 PKCE `code_verifier`、`code_challenge`、`state`
3. 返回 OpenAI 登录页 URL
4. 前端弹出 OpenAI 页面
5. OpenAI 回调到本地 `localhost:1455`
6. 前端或本地回调服务把 `code/state` relay 给服务端
7. 服务端调用 `https://auth.openai.com/oauth/token` 换取 `access_token` / `refresh_token`
8. 账号入池，安排刷新任务

关键细节：

- OAuth query string 不能直接用 `URLSearchParams`
  - 因为空格会被编码成 `+`
  - OpenAI auth 端点要求 `%20`
- 回调端口固定为 `1455`
  - 这是和当前 OAuth client 白名单绑定的兼容要求
- 存在 `pendingSessions` / `completedSessions`
  - 用于防止重复兑换 code 或并发抢占同一 session

### 4.2 Device Code 登录

`/auth/device-login` 与 `/auth/device-poll/:deviceCode` 支持设备码登录。适合没有浏览器 popup 的场景。

### 4.3 CLI 凭证导入

`/auth/import-cli` 支持导入 Codex CLI 的 `auth.json`。本质是把已有 token / refresh token 纳入账号池。

### 4.4 手工 token 导入

`/auth/token` 和 `/auth/accounts` 支持手动提交 token / refresh token。

注意事项：

- JWT 导入不仅检查是否能解析
- 还会验证是否过期、是否包含 `chatgpt_account_id`

## 5. 账号池与状态机

账号池实现由以下模块组成：

- `account-pool.ts`
- `account-registry.ts`
- `account-lifecycle.ts`
- `account-persistence.ts`
- `refresh-scheduler.ts`
- `session-affinity.ts`

### 5.1 账号状态

账号状态不是二元的登录/未登录，而是完整状态机：

- `active`
- `expired`
- `quota_exhausted`
- `rate_limited`
- `refreshing`
- `disabled`
- `banned`

### 5.2 账号获取逻辑

请求进来后不会随机选账号，而是按以下规则筛选：

- 必须是 active
- 不能在排除集里
- 不能超过 `max_concurrent_per_account`
- 如模型依赖 plan，则按 plan 过滤
- 如开启 `tier_priority`，优先高 tier
- 如存在 `preferredEntryId`，优先会话亲和
- 最后再按 `rotation_strategy` 选择

### 5.3 并发控制

`account-lifecycle.ts` 维护每个账号的 active slots：

- 支持每账号最大并发数限制
- 请求结束后显式 release
- 超过 5 分钟的 stale slot 自动回收

### 5.4 持久化

账号保存到 `data/accounts.json`。

细节：

- 可迁移旧 `auth.json`
- 不会因一次更新而清空已有 refresh token
- 会回填 JWT 中的账号字段

### 5.5 会话亲和

`session-affinity.ts` 维护：

- `responseId -> entryId`
- `responseId -> conversationId`
- `responseId -> turnState`
- `responseId -> instructions`
- `responseId -> inputTokens`
- `responseId -> functionCallIds`

这不是附属信息，而是核心业务数据，用于：

- `previous_response_id` 继续走同一账号
- 隐式续链
- prompt cache 复用
- `turnState` 回传

## 6. token 刷新与风控规避

### 6.1 自动刷新

`refresh-scheduler.ts` 的行为不是简单定时任务，而是考虑了 refresh token 单次消费的风险：

- 在 `exp - margin` 时刷新
- 有独立刷新并发上限
- 对同一账号维护 in-flight 状态
- 如果账号卡在 `refreshing`，有崩溃恢复逻辑
- 对 transient error 采用退避重试
- 连续永久错误超过阈值才判定过期

### 6.2 单次 refresh token 的特殊处理

该项目明确处理了“refresh token 可能是 one-time use”这一问题：

- 刷新前会同步磁盘上的最新 refresh token
- 只在“可以证明还没发出去”的前置失败上重试到别的链路
- 如果请求已经发出、中途断网，不会轻易重试

核心原因：

- 否则可能触发 `refresh_token_reused`

### 6.3 健康检查与 warmup 的禁忌

这是 `app` 最值得保留的工程经验之一：

- 账号健康检查只使用 OAuth refresh
- 不主动调用 Codex API 做健康探测
- 导入账号后不再默认调用 `/codex/usage` warmup

原因在代码里写得很明确：

- 过于积极地访问 `codex/usage` 会触发 OpenAI risk detection
- 可能导致账号 deactivation / ban

结论：

- 认证与健康检查链路必须谨慎
- 不能把“验证账号能不能调用”设计成高频上游探测

## 7. 真正的上游不是 OpenAI 公共 API

`app/src/proxy/codex-api.ts` 说明了最核心的一点：

- 主要上游是 `chatgpt.com/backend-api/codex/responses`
- 不是 `api.openai.com/v1/chat/completions`

这意味着：

- `app` 对外暴露的是 OpenAI 兼容协议
- 对内调用的是 Codex Responses backend
- 中间存在一层较重的协议翻译和补丁逻辑

## 8. OpenAI 调用注意事项

### 8.1 请求头和客户端指纹

`fingerprint/manager.ts` 会构造近似 Codex Desktop 的请求头，包括：

- `Authorization`
- `ChatGPT-Account-Id`
- `originator`
- `User-Agent`
- `sec-ch-ua`
- `Accept-Language`
- `Accept-Encoding`
- `sec-fetch-*`

含义：

- 该项目不是“裸 fetch 上游”
- 它刻意模拟桌面客户端指纹

### 8.2 Cookie 管理

存在 `CookieJar`，会收集和回放 `Set-Cookie`，目的是维持 Cloudflare / session 的连续性。

### 8.3 Cloudflare 和 direct fallback

`tls/direct-fallback.ts` 实现了重要兜底：

- 若当前走代理时遇到 Cloudflare challenge
- 或代理/TLS 网络错误
- 会尝试 direct fallback

但 refresh token 场景更严格：

- 只有前置安全失败才允许换链路重试
- 避免单次 refresh token 被重复消费

### 8.4 WebSocket 不是可选增强，而是某些场景的必要条件

`createResponse()` 的策略：

- 若 `useWebSocket = true`，先走 WS
- 若 WS 失败且请求没有 `previous_response_id`，可降级为 HTTP SSE
- 若存在 `previous_response_id`，WS 失败不能安全降级

原因：

- HTTP SSE 路径无法安全承接 `previous_response_id`

这意味着：

- Go 实现必须支持 WS 路径
- 不能只做 SSE relay 就宣称对齐

### 8.5 `service_tier` 不能直接透传

客户端侧可以传 `service_tier`，但发往 Codex backend 前会被移除。

原因：

- 上游会报 `Unsupported service_tier`

### 8.6 reasoning 请求需要额外 include

如果请求里开启 reasoning，而 `include` 为空，代理会自动补：

- `reasoning.encrypted_content`

这属于转发层自己的协议增强。

## 9. 转发不是透传，存在大量自有业务逻辑

核心逻辑在 `routes/shared/proxy-handler.ts`。

### 9.1 会话亲和

当请求携带 `previous_response_id` 时，代理会优先选取生成该 response 的同一账号。

### 9.2 prompt cache key

代理会根据请求内容推导稳定的 conversation key，并写入：

- `prompt_cache_key`

这用于提高同链路会话的 prompt cache 命中。

### 9.3 隐式续链

即使客户端没有显式发送 `previous_response_id`，只要满足条件，代理仍可能自动把本轮请求改写为“续上一次 response”：

触发条件包括：

- 同一个 conversation
- 指令一致
- 工具调用输出可以和上一轮对上
- 仍命中同一账号

启用后代理会：

- 自动补 `previous_response_id`
- 只发送 assistant 之后的增量输入
- 回带上轮 `turnState`

如果隐式续链走 WS 失败，会回退到完整历史重放。

这是非常关键的产品行为：

- `app` 不只是请求翻译器
- 它还在主动优化会话延续与输入裁剪

### 9.4 节流与错峰

通过 `request_interval_ms` 和 `prevSlotMs`，代理会在同账号连续请求之间做错峰等待，避免上游流量过于密集。

### 9.5 限流与配额状态缓存

项目会从两类地方解析 rate limit：

- HTTP headers
- WebSocket `codex.rate_limits` 事件

然后把结果同步到账号池中，更新：

- cached quota
- rate limit reset time
- 账号状态

因此它的 quota 数据是“被动收集”的，而不是强依赖轮询。

### 9.6 空响应重试

非流式路径下，若上游返回 empty response，会在其他账号上重试，最多 2 次。

### 9.7 错误分类驱动重试

`CodexApiError` 会被分类处理：

- `429`: 标记 rate-limited，换账号
- `402`: 配额耗尽
- `403`: 视为 ban
- `401`: 根据 message 区分 expired / banned
- 模型不支持：允许切换其他账号重试一次

如果所有账号都不可用，会合成总结性错误返回。

结论：

- `app` 的业务复杂度主要就在转发编排层
- 若新项目只做“转发 HTTP”，实际上会丢失大部分行为一致性

## 10. OpenAI 兼容接口如何实现

### 10.1 `/v1/chat/completions`

在 `routes/chat.ts` 中：

- 先做 OpenAI chat schema 校验
- 如果 body 更像 Responses 协议，也做兼容翻译
- 然后把请求转换为 Codex Responses request
- 再进入共享 proxy handler

翻译逻辑在 `translation/openai-to-codex.ts`：

- `system/developer` 消息转换为 `instructions`
- 普通消息转换为 `input`
- 工具调用与工具输出保留
- image part 转为 `input_image`
- `reasoning_effort` 和 `service_tier` 支持从字段或模型后缀派生
- `response_format` 转为 `text.format`

### 10.2 结构化输出修正

`translation/shared-utils.ts` 对 JSON schema 做了额外处理：

- 自动补 `additionalProperties: false`
- 把 tuple schema 的 `prefixItems` 转成上游更可接受的对象结构

而在响应回写时，`codex-to-openai.ts` 又会把这类结果 reconvert 回客户端期望形式。

这说明：

- 结构化输出不是直通
- 代理层承担了 schema 兼容层职责

### 10.3 `/v1/responses`

`routes/responses.ts` 的行为也不是简单 passthrough：

- 默认启用 `useWebSocket = true`
- 翻译 `previous_response_id`
- 处理 reasoning、tools、tool_choice、`text.format`
- 对 `json_schema` 做 prepare 和 reconvert
- 对 completed response 若 `output` 为空，会从流式片段重建

## 11. 模型体系

`models/model-store.ts` + `model-fetcher.ts` 共同提供：

- 静态模型目录
- 别名
- 默认模型
- reasoning / speed 后缀解析
- 按 plan 的模型可用性映射
- 后端动态模型合并

细节：

- 模型名后缀可携带语义，例如 `-fast`、`-high`
- 账号选择时会结合 plan 过滤模型

## 12. 代理池

`proxy-pool.ts` 提供账号到代理的绑定能力：

- 可走 global
- 可 direct
- 可 auto
- 可指定某个代理

还会做代理健康检查，并持久化到 `data/proxies.json`。

这一层与 OAuth 刷新、上游请求、Cloudflare 规避是耦合的。

## 13. 日志、观测与诊断

后台能力包括：

- `/admin/logs`
- `/admin/usage-stats`
- `/admin/connection`
- `/health`
- `/debug/fingerprint`
- `/debug/diagnostics`

日志不仅记录入口，也记录 egress 到 `/codex/responses` 的请求状态和时延。

## 14. Dashboard 登录保护

`dashboard-auth.ts` + `dashboard-login.ts` 实现后台登录保护：

- 若配置了 `server.proxy_api_key`
- 且请求不是 localhost
- 管理后台必须先登录

实现细节：

- 登录密码就是 `proxy_api_key`
- 登录后发 `_codex_session` cookie
- 有滑动过期
- 有 IP 维度的登录尝试限流

注意：

- 这套保护只用于 dashboard/admin 页面
- `/v1/*` 接口仍用 Bearer key 校验

## 15. 前端功能清单

前端在 `app/web`，当前使用 Preact，不是 Ant Design。

主要页面和能力：

- Overview
  - 账号总览
  - 快速加号
  - 代理池总览
- Accounts
  - OAuth 登录
  - refresh token 导入
  - 导入/导出
  - 手动刷新
  - 状态切换
  - 标签编辑
  - quota 查看
  - cookie 管理
- API Keys
  - 第三方 provider API key 管理
- Proxies
  - 代理池和账号代理绑定
- Usage Stats
  - 历史统计
- Logs
  - 请求日志浏览
- Settings
  - 一般设置
  - 日志设置
  - quota 设置
  - 轮转设置
  - API 接入示例
  - 连接测试

## 16. 当前 `app` 的功能边界

当前 `app` 的完整产品边界大于“OpenAI 登录 + OpenAI 兼容代理”：

- 已实现 OpenAI OAuth / Codex backend 主链路
- 还实现了 Anthropic / Gemini 兼容入口
- 还支持第三方 API key provider 路由
- 还支持 Ollama bridge

因此在做新项目规划时，需要先明确：

- 是完整复刻 `app` 的全部产品边界
- 还是以 `app` 的 OpenAI/Codex 主链路为核心，做 OpenAI-only 新产品

结合当前需求，更合理的产品边界应是：

- 保留 `app` 的 OpenAI/Codex 认证、账号池、转发编排、后台管理能力
- 不再复制 Anthropic / Gemini / API key provider / Ollama 相关产品面

## 17. 对新项目最重要的技术结论

如果目标是“功能对齐”，以下点不能丢：

1. 上游必须按 Codex backend 思维实现，而不是按 OpenAI 公共 API 思维实现。
2. OAuth PKCE、Device Code、CLI 导入、refresh token 生命周期要完整保留。
3. 必须支持账号池、会话亲和、并发槽位、自动刷新。
4. 必须支持 WebSocket 路径，否则 `previous_response_id` 不对齐。
5. 转发层必须保留隐式续链、prompt cache、turnState、rate limit 被动收集。
6. 必须保留 Cloudflare / cookie / direct fallback 的兼容处理。
7. 结构化输出 schema 修正与响应 reconvert 必须保留，否则行为会和 `app` 不一致。
8. 健康检查和导入后的验证要规避风控，不能高频触达 `/codex/usage`。

