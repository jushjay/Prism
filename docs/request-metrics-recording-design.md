# 请求耗时与首 Token 记录实施设计稿

## 1. 目标

本设计稿用于在 `prism` 中新增“请求级明细记录”能力，覆盖：

- 上游请求总耗时 `duration_ms`
- 首 token 耗时 `first_token_ms`
- 每次上游尝试的请求级上下文

当前阶段只做“数据记录”，明确不做：

- 聚合表
- 统计接口
- 前端展示
- 后台筛选页

本稿的目的不是讨论可行性，而是为后续开发提供可直接实施的方案。后续新会话应以本稿为准完成实现。

## 2. 设计原则

### 2.1 记录对象

记录对象不是“最终 usage 成功事件”，而是“每次上游请求尝试”。

原因：

- 当前 `usage_events` 仅在最终成功并拿到 usage 后写入，见 [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1951)
- 请求链路存在重试、换号、失败返回
- 如果把 latency 只绑定到 usage，会丢失失败请求与中间尝试

因此本次设计新增独立请求明细表，不复用 `usage_events` 作为 latency 主表。

### 2.2 与现有 usage 的关系

- `usage_events` 继续承担 token 账本职责
- 新表承担请求观测职责
- 两者允许冗余部分账号、模型信息，但不强制要求立即建立外键

如后续需要，可再为 `usage_events` 增加 `request_event_id` 关联字段，但本阶段不强制实现。

### 2.3 与现有 audit 的关系

当前 upstream audit 中的 `DurationMs` 不作为正式请求耗时口径来源。

原因：

- `createAccountResponse()` 在发起上游请求前开始计时，见 [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1131)
- `auditUpstreamResult()` 在拿到 `*http.Response` 后立即写 audit，见 [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1192)
- 对流式请求，这更接近“首包/响应头建立耗时”，不是完整请求结束耗时

因此：

- audit 继续保留，不改语义
- 请求明细表单独记录完整耗时和 TTFT

## 3. 指标口径

### 3.1 `duration_ms`

定义：

- 从“发起上游请求前”开始计时
- 到“本次上游响应处理完成”结束计时

不同路径下的结束时机：

- `stream=true`：
  - 读到流结束
  - 或收到终止事件并完成向下游写出
  - 或因错误中断处理
- 非流式但上游实际返回 SSE：
  - `codex.ParseSSE()` / `codex.ReadResponseText()` / 等价解析完成
- 上游错误：
  - `createAccountResponse()` 返回 error 时结束

备注：

- 该口径表示“该次上游尝试的完整处理耗时”
- 不等于当前 audit 中的 `DurationMs`

### 3.2 `first_token_ms`

定义：

- 从与 `duration_ms` 相同的起点开始
- 到“首个真正用户可感知输出事件”到达为止

要求：

- 不将 `codex.rate_limits` 视为 token
- 不将纯控制事件视为 token
- 如果请求失败且从未产生用户可感知输出，则记为 `NULL`

建议判定规则：

- Responses 流：
  - 仅当事件能够产生下游输出时才记录首 token
  - 如果当前分支会经过 `streamTupleAwareResponsesEvent()` 或 `writeRawSSEEvent()`，则应在写出前先判断该事件是否为有效输出事件
- Chat Completions 流：
  - 仅当 `streamState.consume(event)` 产出非空 chunk 时记录
- 非流式 SSE：
  - 解析事件序列时，首次发现可生成正文文本、工具调用、或有效 output item 的事件时记录

口径目标：

- 尽可能贴近真实用户首次看到内容的时间
- 不使用“首个任意 SSE data 行”这种宽松定义

## 4. 数据模型

### 4.1 新表名称

建议新增表：

- `request_events`

名称要求：

- 表意明确
- 避免和 `usage_events` 混淆
- 后续可以自然扩展到错误分类、重试链路、响应大小等字段

### 4.2 字段清单

建议字段如下。

#### 主键与时间

- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `started_at TEXT NOT NULL`
- `completed_at TEXT NOT NULL`

#### 核心指标

- `duration_ms INTEGER NOT NULL`
- `first_token_ms INTEGER`

#### 结果状态

- `success INTEGER NOT NULL`
- `status_code INTEGER`
- `error_message TEXT`

说明：

- `success` 使用 0/1 存储
- `status_code` 对网络错误可为空或为 0，推荐允许为空
- `error_message` 允许为空，建议在写入前截断，避免异常体过大

#### 请求上下文

- `source_path TEXT`
- `endpoint_style TEXT NOT NULL`
- `request_stream INTEGER NOT NULL`
- `retry_attempt INTEGER NOT NULL DEFAULT 0`
- `upstream_type TEXT NOT NULL`

字段语义：

- `source_path`：当前服务入口，例如 `/v1/responses`、`/v1/chat/completions`
- `endpoint_style`：上游 endpoint 风格，例如 `responses`、`chat_completions`
- `request_stream`：用户请求是否为 stream
- `retry_attempt`：第几次上游尝试，从 0 开始
- `upstream_type`：`openai` 或 `custom`

#### 账号上下文

- `account_id TEXT NOT NULL`
- `account_provider TEXT`
- `account_identity TEXT`
- `account_display_name TEXT`
- `account_label TEXT`
- `account_email TEXT`
- `upstream_account_id TEXT`
- `account_snapshot TEXT`

说明：

- 与 `usage_events` 保持相同的账号快照风格，方便后续关联与筛选
- `account_snapshot` 可直接复用 `auth.BuildUsageSnapshot(account)` 结果序列化

#### 模型上下文

- `requested_model TEXT NOT NULL`
- `routed_model TEXT NOT NULL`

说明：

- `requested_model`：客户端请求模型
- `routed_model`：实际路由到上游账号的模型

#### 上游上下文

- `upstream_request_id TEXT`
- `response_id TEXT`

说明：

- `upstream_request_id` 可来自响应头，例如 `x-request-id`
- `response_id` 来自 SSE 事件解析出的最终 response id

#### usage 快照

- `input_tokens INTEGER`
- `output_tokens INTEGER`
- `cached_tokens INTEGER`
- `reasoning_tokens INTEGER`

说明：

- 本阶段虽然不做聚合，但建议一并记录
- 这样后续做请求级问题排查时不必再 join usage 表
- 对失败请求这些字段可为空或为 0，推荐允许为空以区分“未拿到 usage”

### 4.3 索引建议

仅建立基础索引：

- `idx_request_events_started_at(started_at)`
- `idx_request_events_account_id(account_id)`
- `idx_request_events_account_identity(account_identity)`
- `idx_request_events_requested_model(requested_model)`
- `idx_request_events_routed_model(routed_model)`
- `idx_request_events_success(success)`

本阶段不建立复杂复合索引。

## 5. 代码结构设计

### 5.1 不扩展 `usage.Service` 的原因

虽然也可以把请求级明细写入逻辑塞进 `usage.Service`，但不推荐。

原因：

- `usage.Service` 当前语义很明确，主要负责 token 使用量账本
- 请求观测数据和 usage 数据生命周期不同
- 后续该领域很可能继续扩展错误分类、重试链、响应状态等字段

推荐做法：

- 新建 `server/internal/metrics` 或 `server/internal/requestlog` 包
- 单独提供 `Service`

推荐命名：

- 包：`requestlog`
- 服务：`requestlog.Service`

### 5.2 推荐目录结构

建议新增：

- `server/internal/requestlog/service.go`
- `server/internal/requestlog/service_test.go`

如后续复杂度增加，再拆：

- `models.go`
- `sqlite.go`
- `query.go`

当前阶段不需要过度拆分。

## 6. 服务接口设计

### 6.1 Record 结构

建议定义：

```go
type Record struct {
    StartedAt         time.Time
    CompletedAt       time.Time
    DurationMs        int
    FirstTokenMs      *int

    Success           bool
    StatusCode        *int
    ErrorMessage      string

    SourcePath        string
    EndpointStyle     string
    RequestStream     bool
    RetryAttempt      int
    UpstreamType      string

    AccountID         string
    AccountProvider   auth.AccountProvider
    AccountIdentity   string
    AccountDisplayName string
    AccountLabel      string
    AccountEmail      string
    UpstreamAccountID string
    AccountSnapshot   *auth.UsageAccountSnapshot

    RequestedModel    string
    RoutedModel       string

    UpstreamRequestID string
    ResponseID        string

    InputTokens       *int
    OutputTokens      *int
    CachedTokens      *int
    ReasoningTokens   *int
}
```

要求：

- 时长字段统一存毫秒
- usage 快照字段建议用指针，避免“0”和“未知”混淆

### 6.2 Service 接口

建议提供最小接口：

```go
type Service struct {
    mu sync.RWMutex
    db *sql.DB
}

func NewService(db *sql.DB) (*Service, error)
func (s *Service) Record(record Record) error
```

本阶段不做查询接口。

## 7. 数据库迁移设计

### 7.1 SQLiteStore 初始化

在 [server/internal/store/sqlite.go](/data/prism/server/internal/store/sqlite.go:167) 现有建表逻辑中新增：

- `CREATE TABLE IF NOT EXISTS request_events (...)`
- 基础索引

### 7.2 幂等迁移

如项目已有“增量 ALTER”风格迁移，则应同时补充：

- `ALTER TABLE` 路径只在字段需要演进时使用
- 本次因是新表，优先直接 `CREATE TABLE IF NOT EXISTS`

### 7.3 字段类型约定

- 时间：`TEXT`，统一使用 `time.RFC3339Nano`
- 布尔：`INTEGER`
- 可空整数：允许 `NULL`

## 8. 采集链路设计

### 8.1 统一的尝试级上下文

在 `handleResponses()` 与 `handleChatCompletions()` 中，每次进入账号尝试循环时都创建一份“尝试级观测上下文”。

建议字段：

- `attemptStart time.Time`
- `firstTokenMs *int`
- `responseID string`
- `upstreamRequestID string`
- `statusCode *int`
- `usage codex.Usage`
- `recorded bool`

建议不要把这些散落成多个局部变量，而是形成一个小 struct，避免遗漏路径。

### 8.2 `handleResponses()` 流式路径

位置：

- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:887)

实施方式：

1. 在调用 `createAccountResponse()` 前记录 `attemptStart`
2. 成功拿到 `resp` 后，从 header 提取 `upstreamRequestID`
3. 在 SSE 循环中：
   - 跳过 `codex.rate_limits`
   - 若事件包含 usage，则更新 usage 快照
   - 若事件首次被判定为“有效输出事件”，记录 `firstTokenMs`
   - 若事件解析出 response id，更新 `responseID`
4. 循环结束后：
   - 记录 `completedAt`
   - 计算 `duration_ms`
   - 写入 `request_events`

### 8.3 `handleChatCompletions()` 流式路径

位置：

- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1044)

实施方式：

1. 同样在尝试开始前记录 `attemptStart`
2. 成功拿到 `resp` 后提取 `upstreamRequestID`
3. 在 `streamState.consume(event)` 之后：
   - 若返回 chunk 非空，且尚未记录首 token，则记录 `firstTokenMs`
4. 结束后写入 `request_events`

注意：

- 初始化 role chunk 不算首 token
- 必须由真实上游输出触发 TTFT

### 8.4 `handleResponses()` 非流式路径

位置：

- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:906)

实施方式：

1. `attemptStart` 在本次账号尝试开始前记录
2. `codex.ParseSSE(resp)` 返回后，扫描事件：
   - 提取 response id
   - 提取 usage
   - 首次遇到有效输出事件时记录 `firstTokenMs`
3. 在 `buildResponsesPayload()` 前或后都可以写入请求记录
4. 若因为空响应触发重试，本次尝试也应记一条 `request_events`

### 8.5 `handleChatCompletions()` 非流式路径

位置：

- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1059)

实施方式：

1. `attemptStart` 在本次账号尝试开始前记录
2. `codex.ReadResponseText(events)` 前后，通过扫描事件提取：
   - response id
   - usage
   - 首次有效输出事件时间
3. 无论最终是否因空响应重试，都应记录本次尝试

### 8.6 上游错误路径

当 `createAccountResponse()` 返回 error 时：

- 立即以失败请求写一条 `request_events`
- `first_token_ms = NULL`
- `duration_ms = time.Since(attemptStart).Milliseconds()`
- `status_code`：
  - 如果是 `*codex.UpstreamError`，取其 `StatusCode`
  - 否则为空
- `error_message`：
  - 使用 `err.Error()`
  - 建议限制长度，例如 2KB 或 4KB

注意：

- 即使之后会重试到别的账号，本次失败尝试也必须保留

### 8.7 空响应重试路径

当前项目对空响应会重试，见：

- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:919)
- [server/internal/app/server.go](/data/prism/server/internal/app/server.go:1078)

要求：

- 空响应本次尝试仍然写入 `request_events`
- `success` 建议记为 `true`
- 但可通过 `response_id` 为空或 usage 很低反映其质量问题

原因：

- 从请求观测角度看，这次上游尝试确实完成了
- 是否满足业务语义由更高层决定，不应在观测层抹掉

## 9. 有效输出事件判定

### 9.1 需要新增 helper

建议新增若干 helper，避免 TTFT 逻辑散落：

- `extractResponseIDFromEvent(event codex.SSEEvent) string`
- `isMeaningfulResponsesOutputEvent(event codex.SSEEvent) bool`
- `chatChunksCarryVisibleOutput(chunks []any) bool`
- `usagePointersFromCodexUsage(usage codex.Usage) (...)`

### 9.2 Responses 流判定建议

对于 `/v1/responses`：

- `response.output_text.delta`
- `response.output_item.added`
- `response.content_part.added`
- 或其他明确带正文 / 工具调用内容的事件

可视为有效输出事件。

不应计入：

- `codex.rate_limits`
- 纯 metadata
- 空数据帧

### 9.3 Chat Completions 流判定建议

对于 `/v1/chat/completions`：

- 只有 `streamState.consume(event)` 生成了可写 chunk，才认为出现首 token

这是该路径最稳妥的口径。

## 10. 服务接入点

### 10.1 Server 结构体

在 `Server` 中新增：

- `requestLog *requestlog.Service`

初始化位置：

- `NewServer()` 中创建并注入

### 10.2 初始化失败处理

与现有 `usage.NewService()` 风格一致：

- 初始化失败直接返回错误

### 10.3 记录失败不影响主流程

`requestLog.Record(...)` 失败时：

- 只记录日志
- 不能影响正常代理返回

这是观测链路的基本原则。

## 11. 推荐实现步骤

后续开发建议严格按以下顺序实施：

1. 新增 `requestlog` 包与 `Record` / `Service`
2. 增加 SQLite `request_events` 表与索引
3. 在 `Server` 中初始化 `requestLog`
4. 先接入上游错误路径记录
5. 接入两条非流式路径记录
6. 接入两条流式路径记录
7. 增加 TTFT helper，统一口径
8. 增加测试，覆盖成功、失败、重试、空响应

不要先改 `usage.Service` 试图兼容本需求。

## 12. 测试要求

至少覆盖以下场景。

### 12.1 成功非流式 Responses

- 记录 1 条 `request_events`
- `duration_ms > 0`
- `first_token_ms != NULL`
- usage 字段存在

### 12.2 成功流式 Responses

- 记录 1 条 `request_events`
- 首 token 在首个有效输出事件时写入
- 不能把 `codex.rate_limits` 算成首 token

### 12.3 成功流式 Chat Completions

- 初始 role chunk 不算首 token
- 首个真实输出 chunk 才算

### 12.4 上游 4xx/5xx 失败

- 记录 1 条失败事件
- `success = false`
- `first_token_ms = NULL`
- `status_code` 正确

### 12.5 网络错误

- 记录 1 条失败事件
- `status_code = NULL` 或约定值
- `error_message` 有内容

### 12.6 空响应后重试成功

- 至少记录 2 条 `request_events`
- 第 1 条是空响应尝试
- 第 2 条是成功尝试
- `retry_attempt` 正确递增

## 13. 验收标准

完成开发后，至少满足：

- 新表 `request_events` 自动创建
- 请求成功、失败、重试都会留下明细
- `duration_ms` 口径为“完整请求处理耗时”
- `first_token_ms` 不把 `codex.rate_limits` 或初始 role chunk 误算进去
- 记录失败不影响主业务链路

## 14. 明确不做的事情

本稿对应的开发范围明确不包括：

- `usage_events` 聚合逻辑改造
- 管理后台页面
- `/admin/request-events` 查询接口
- P50/P95/P99 统计
- 报警阈值
- Prometheus 指标导出

## 15. 后续扩展预留

本设计完成后，后续可以直接扩展：

- 请求明细查询接口
- 后台分页列表
- duration / TTFT 分位统计
- 按模型 / 账号 / 状态码聚合
- 异常请求排查页

不需要再修改数据记录口径。

