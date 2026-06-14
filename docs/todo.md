# Prism TODO

## 状态说明

当前 `prism` 已完成：

- Go 后端基础工程
- React + Ant Design 前端基础工程
- OpenAI OAuth / Device Code / CLI auth 导入入口
- 账号池基础持久化与 refresh scheduler
- Dashboard 登录
- `/v1/models`
- `/v1/responses`
- `/v1/chat/completions`
- Codex backend HTTP / WebSocket 基础调用
- 会话亲和 / 隐式续链 / `turnState` / 稳定 `prompt_cache_key`
- tier priority / `max_concurrent_per_account` / `request_interval_ms`
- structured output / tuple schema / tool call streaming / reasoning streaming
- upstream 错误分类 / rate limit 被动采集 / 空响应重试 / refresh 互斥保护
- 基础后台页面：概览、账号、模型、设置、API 示例

当前仍未完成或未完全对齐 `app` 的部分如下。

## 已完成的 P0

- `previous_response_id -> account` affinity 映射
- 显式续链优先命中原账号
- 基于 conversation / instructions / tool outputs 的 implicit resume
- `x-codex-turn-state` 记录与自动回带
- 稳定 `prompt_cache_key`
- `tier_priority` / `max_concurrent_per_account` / `request_interval_ms`
- `json_object` / `json_schema` / tuple schema 兼容
- chat completions `tool_calls` 流式兼容
- chat completions `reasoning_content` 流式兼容
- `401/402/403/429` 驱动账号状态更新
- HTTP headers + WebSocket `codex.rate_limits` 被动采集
- 非流式空响应重试
- refresh 互斥，避免重复消费 refresh token

## 仍待补齐的高优先级项

- `/v1/responses` 完成态 payload 仍是简化版，未完全复刻 `app` 的所有 output item 细节
- Dashboard 设置仍是只读，尚未支持写回 `default model / reasoning / refresh / proxy_api_key`
- quota 明细、日志、统计、代理池、CookieJar 持久化仍未实现
- 集成测试、Docker、CI 仍未补齐

## P1：后台管理能力补齐

- 账号管理增强
  - 标签编辑
  - 启用 / 禁用
  - quota 查看
  - cookie 查看与编辑
  - 导入 / 导出

- 设置页增强
  - 支持写回配置
  - default model / reasoning 修改
  - refresh 参数修改
  - proxy_api_key 修改

- 模型管理增强
  - `/v1/models/catalog`
  - `/v1/models/:id/info`
  - 动态拉取 backend model 并合并

- 日志能力
  - ingress / egress 请求日志
  - 筛选与详情

- 统计能力
  - usage snapshots
  - input / output / cached / reasoning token 聚合

- 连接测试页
  - OAuth / account / transport / upstream 基础探测

## P1：代理与网络层补齐

- 代理池
  - 全局代理
  - per-account proxy
  - direct / auto / specific assignment

- CookieJar 持久化
  - 按账号存取 cookies
  - `Set-Cookie` 捕获与回放

- Cloudflare / direct fallback
  - 代理失败自动回退 direct
  - Cloudflare challenge 检测

- 桌面指纹头进一步对齐
  - `sec-ch-ua`
  - `originator`
  - 更接近 Codex Desktop 的 header 集

## P1：前端页面补齐

- 代理池页面
- 日志页面
- 使用统计页面
- OAuth 回调状态与错误提示增强
- 账号详情抽屉

## P2：工程化补齐

- 单元测试
  - auth
  - account pool
  - translator
  - codex client

- 集成测试
  - `/auth/*`
  - `/v1/models`
  - `/v1/responses`
  - `/v1/chat/completions`

- Docker / 启动脚本
- CI
- release 构建说明

## 下一步建议顺序

1. 先补 `session affinity + implicit resume + turnState + prompt_cache_key`
2. 再补 `structured outputs + tool call streaming + reasoning streaming`
3. 再补 `rate limit / error classification / empty response retry`
4. 然后补 `日志 / 统计 / 代理池 / 设置写回`
5. 最后补 `测试 / Docker / CI`
