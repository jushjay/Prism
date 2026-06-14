# DeepSeek 兼容适配记录

## 背景

本次适配目标是让 `custom` 账号下的 DeepSeek 类 OpenAI 兼容模型，能够在 Cursor 中稳定完成多轮 agent/tool 调用，而不是只做单轮文本回复。

最终稳定方案是：

- `custom` 账号优先走标准 `/v1/chat/completions`
- 请求中透传标准 `tools` / `tool_choice`
- 将标准 chat streaming `delta.tool_calls` 转为内部 `responses` 事件
- 将内部工具调用历史重建成标准 OpenAI chat 历史
- 保留 `previous_response_id` 和会话亲和，保证多轮连续性

本文件记录排查过程中的异常现象、根因和最终保留的解决方法，供后续适配其他模型复用。

## 日志与排查入口

本次主要依赖以下日志：

- `data/logs/cursor-audit.jsonl`
- `data/logs/custom-egress.jsonl`
- `data/logs/openai-egress.jsonl`

本地开发时需要开启：

```env
CURSOR_AUDIT_LOG_ENABLED=true
OPENAI_EGRESS_AUDIT_LOG_ENABLED=true
CUSTOM_EGRESS_AUDIT_LOG_ENABLED=true
```

排查顺序建议固定为：

1. 先看 `cursor-audit.jsonl`，确认 Cursor 发来的原始请求形态。
2. 再看 `custom-egress.jsonl`，确认发给上游的目标 URL、请求体、返回状态。
3. 用成功的 OpenAI 请求对比失败的 custom 请求，找字段差异，不先猜模型能力。

## 异常 1：Cursor 对话只能处理一次请求

### 现象

- DeepSeek 在 Cursor 中首轮可以回复
- 第二轮续聊时，无法延续上下文，看起来像“只能处理一次请求”

### 日志特征

- `custom-egress.jsonl` 中 custom 请求可以正常发出
- 但发往上游的 `/v1/responses` 请求里没有 `previous_response_id`

### 根因

`custom` 路径在创建请求时把 `request.PreviousResponseID` 清空了，导致续聊时上游只能把第二轮当作新会话处理。

同时，`/v1/chat/completions` 路径缺少和 `/v1/responses` 一致的会话亲和逻辑，无法保证 continuation 继续命中同一账号。

### 最终修复

- 保留 `custom` 请求的 `previous_response_id`
- 在 `marshalHTTPResponsesRequest` 中显式序列化 `previous_response_id`
- 在 `handleChatCompletions` 中补齐基于 `previous_response_id` 的账号亲和和 `turnState` 复用逻辑

### 保留原因

这是多轮连续会话的基础能力，后续所有 OpenAI 兼容模型都需要。

## 异常 2：custom 目标地址被重复拼接

### 现象

- DeepSeek 请求失败
- 日志里目标地址变成了类似：

```text
https://host/v1/responses/v1/responses
```

### 日志特征

- `openai-egress.jsonl` 成功请求目标地址正常
- `custom-egress.jsonl` 失败请求中 `target_path` 被重复拼接

### 根因

历史 custom 账号配置里，`custom_base_url` 已经包含了 `/v1/responses` 或 `/v1/chat/completions`，系统又把 `custom_endpoint_type` 拼了一次。

新增/编辑账号时会归一化，但旧账号加载时没有做同样的归一化。

### 最终修复

- 增加 `normalizeCustomEndpointConfig`
- 在新增账号、编辑账号、加载历史账号元数据时统一执行
- 若 `custom_base_url` 已经包含 endpoint，则自动拆回：
  - `custom_base_url = https://host`
  - `custom_endpoint_type = /v1/...`

### 保留原因

这是兼容历史配置必须保留的修复，否则切换 provider 或迁移老数据时会反复出现。

## 异常 3：Cursor 显示 `<tool_calls>`，agent 一轮后结束

### 现象

- 对话中直接显示 `<tool_calls>` 或类似 XML 标记
- agent 没有继续进入下一轮工具执行

### 早期观察

部分上游返回的不是标准 OpenAI tool call chunk，而是把工具调用写成文本，例如：

```text
<tool_calls>
  <tool_call name="Read">...</tool_call>
</tool_calls>
```

### 处理结论

这类文本格式兼容曾做过临时兜底解析，但最终没有保留。

### 不保留的原因

- 这是 provider 的非标准输出
- 解析文本标记只适用于少数格式，维护成本高
- 后续日志证明它不是本次最终稳定问题的根因
- 对 OpenAI 兼容模型，更稳的方向是强制走标准 `/v1/chat/completions` 协议，而不是长期兼容文本伪协议

### 最终策略

- DeepSeek 类 provider 不再依赖文本 `<tool_calls>` 兼容
- 统一走标准 `chat/completions` 工具协议

## 异常 4：切到 `/v1/chat/completions` 后，模型仍然“口头执行工具”

### 现象

- 模型回复里写了：
  - `cat > xxx.sh`
  - `bash xxx.sh`
  - “脚本已成功执行”
- 但本地没有文件
- Cursor UI 里也没有显示任何真实命令执行过程

### 日志特征

- `custom-egress.jsonl` 中上游返回 `200`
- 但请求体里只有 `messages`
- 没有 `tools`
- 没有 `tool_choice`

### 根因

`custom /v1/chat/completions` 请求组装时，没有把工具定义传给上游。

结果是：

- DeepSeek 只能把自己当普通聊天模型
- 它会“描述应该怎么做”
- 但不会产生真实的标准 tool call
- Cursor 自然也不会执行本地工具

### 最终修复

在 `marshalAuditChatCompletionsRequest` 中：

- 透传标准 `tools`
- 透传 `tool_choice`
- 工具定义规范化为 OpenAI 标准格式：

```json
{
  "type": "function",
  "function": {
    "name": "Read",
    "description": "...",
    "parameters": {}
  }
}
```

### 保留原因

这是本次导致“模型假装执行成功”的直接根因，必须保留。

## 异常 5：多轮后历史被污染，后续请求带着伪工具文本继续发给上游

### 现象

- 即使切到 `chat/completions`
- 后续请求历史中仍然混进了：
  - `<tool_call ...>`
  - `<parameter ...>`
  - “脚本已成功执行”

### 日志特征

- `custom-egress.jsonl` 的 `request_body.messages` 中，assistant 历史不是标准工具消息
- 而是普通文本形式的“伪执行记录”

### 根因

内部 `responses` 历史在转回 `chat/completions` 时，没有把：

- `function_call`
- `function_call_output`
- `custom_tool_call`
- `custom_tool_call_output`

重建为标准 OpenAI chat 历史，而是把污染后的文本继续当 assistant content 发了出去。

### 最终修复

在 `marshalAuditChatCompletionsRequest` 中重建标准工具历史：

- assistant `tool_calls`
- tool `tool_call_id`

对应关系：

- `function_call` / `custom_tool_call` -> assistant `tool_calls`
- `function_call_output` / `custom_tool_call_output` -> tool message

### 保留原因

这是所有 `responses -> chat/completions` 桥接都需要的标准化能力，不是 DeepSeek 独有问题。

## 异常 6：DeepSeek 返回了标准 chat tool_calls，但内部没有继续执行

### 现象

- 上游已经走标准 `/v1/chat/completions`
- 但如果返回的是标准 `delta.tool_calls`
- 内部仍然没有转成后续工具执行事件

### 根因

`custom` 的 chat streaming 转换层只处理了文本增量，没有把 OpenAI chat 工具调用流转换成内部 `responses` 事件。

### 最终修复

在 `convertChatCompletionsStream` 中增加标准 tool call 流转换：

- `delta.tool_calls` -> `response.output_item.added`
- arguments 增量 -> `response.function_call_arguments.delta`
- 完成时 -> `response.function_call_arguments.done`

### 保留原因

这是让标准 OpenAI 兼容模型真正进入内部 agent/tool 执行链路的核心桥接逻辑，必须保留。

## 最终保留的代码改动

### 1. custom 账号 URL 归一化

文件：

- `server/internal/auth/accounts.go`
- `server/internal/auth/accounts_test.go`

作用：

- 兼容历史 custom 账号配置
- 防止 endpoint 重复拼接

### 2. previous_response_id 与会话亲和

文件：

- `server/internal/codex/client.go`
- `server/internal/app/server.go`
- 对应测试文件

作用：

- 保障多轮会话不断链
- continuation 请求继续命中原账号

### 3. custom chat/completions 的标准工具历史重建

文件：

- `server/internal/codex/client.go`
- `server/internal/codex/client_test.go`

作用：

- 将内部工具调用历史还原为标准 OpenAI chat 消息

### 4. custom chat/completions 的 tools / tool_choice 透传

文件：

- `server/internal/codex/client.go`
- `server/internal/codex/client_test.go`

作用：

- 避免模型只能“口头描述执行”
- 让上游真正进入工具调用模式

### 5. chat streaming 标准 tool_calls 转内部事件

文件：

- `server/internal/codex/client.go`
- `server/internal/codex/client_test.go`

作用：

- 将标准 OpenAI chat tool call 流桥接成内部 `responses` 事件

## 明确不保留的尝试

以下尝试仅用于调试，最终没有保留：

- 解析普通文本里的 `<tool_calls>...</tool_calls>` 标记
- 解析 `<tool_call name="...">...</tool_call>` 文本格式并转工具调用

不保留原因：

- 不是最终稳定根因
- 依赖 provider 的非标准输出
- 易碎且难维护
- 对后续兼容其他模型参考价值不高

## 后续适配其他模型的建议

### 优先判断模型是否真的“OpenAI 兼容”

不要只看厂商文档，要看真实返回：

1. 是否支持标准 `/v1/chat/completions`
2. 是否支持标准 `tools`
3. 是否返回标准 streaming `delta.tool_calls`
4. 是否支持多轮工具历史重放

### 排查原则

1. 若 UI 显示“工具执行完成”，但本地没痕迹，优先怀疑模型只生成了文本，没有真实 tool call。
2. 若多轮会话断链，先查 `previous_response_id` 是否透传，再查账号亲和。
3. 若目标 URL 异常，先查历史 custom 配置是否带 endpoint。
4. 若后续请求历史中出现伪工具文本，说明桥接层没有把历史重建成标准 chat 消息。
5. 若上游已返回标准 `tool_calls`，但内部不执行，说明 streaming 转换层缺桥。

### 推荐适配路径

对“声称 OpenAI 兼容”的 provider，优先顺序建议：

1. 优先尝试 `/v1/chat/completions`
2. 确认 `tools` / `tool_choice` 被真实透传
3. 确认返回的是标准 `delta.tool_calls`
4. 再做流式转换和历史重建

不要优先依赖 provider 自定义的 `/v1/responses` 或文本伪工具协议，除非没有其他选项。

## 回归测试覆盖点

本次保留的关键测试包括：

- 历史 custom 账号 URL 归一化
- `previous_response_id` 在 custom 路径保留
- `chat/completions` 工具定义透传
- `chat/completions` 标准 `tool_calls` 流转换
- `responses` 工具历史重建为标准 chat 消息

后续新增 provider 适配时，至少应复用这几类测试。

## 当前 DeepSeek GLM 与 OpenAI 链路差异

本节描述当前代码里，`custom` 账号下的 DeepSeek/GLM 模型，与 `openai` 账号模型在“请求处理”和“返回处理”上的实际区别。

### 1. Cursor 入口层

两者入口基本一致：

- Cursor 发来的 `/v1/chat/completions`
- 会先被转成统一的内部结构 `codex.ResponsesRequest`

也就是说，在进入上游转发前，内部统一抽象都是：

- `instructions`
- `input`
- `tools`
- `tool_choice`
- `previous_response_id`
- `prompt_cache_key`

对应代码：

- `server/internal/openai/translate.go`
- `server/internal/app/server.go`

### 2. 发给上游的请求格式不同

#### OpenAI 账号

OpenAI 账号固定走原生 `/codex/responses`。

发给上游的是 `responses` 风格请求，核心字段包括：

- `instructions`
- `input`
- `tools`
- `tool_choice`
- `text`
- `prompt_cache_key`
- `previous_response_id`

特点：

- 更接近内部结构，基本不用额外改写协议
- continuation 时优先走上游原生 `previous_response_id`

对应代码：

- `server/internal/codex/client.go` 中 `CreateResponse`
- `server/internal/codex/client.go` 中 `createResponseViaHTTP`
- `server/internal/codex/client.go` 中 `createResponseViaWebSocket`

#### DeepSeek GLM custom 账号

DeepSeek/GLM 当前属于 `custom` 账号，稳定路径优先走 `/v1/chat/completions`。

发给上游的是标准 OpenAI chat 风格请求，核心字段包括：

- `messages`
- `tools`
- `tool_choice`
- `reasoning_effort`

特点：

- 内部 `input` 需要先被重建成标准 `messages`
- 工具历史也要重建成 assistant `tool_calls` 和 tool message

对应代码：

- `server/internal/codex/client.go` 中 `CreateCustomResponse`
- `server/internal/codex/client.go` 中 `marshalAuditChatCompletionsRequest`

### 3. 多轮会话连续性的实现方式不同

#### OpenAI 账号

主要依赖上游原生 continuation 能力：

- `previous_response_id`
- websocket continuation

也就是说，会话连续性更多交给 OpenAI `/codex/responses` 本身处理。

#### DeepSeek GLM custom 账号

如果走 `/v1/chat/completions`，上游通常不是按 `previous_response_id` 原生续聊。

当前做法是：

- 本地先依据 `previous_response_id` 做账号亲和
- 复用 turn state
- 再把完整历史重放成 `messages` 发给上游

也就是说：

- OpenAI 更偏“上游原生续聊”
- DeepSeek/GLM 更偏“本地维持连续性，再重放完整历史”

对应代码：

- `server/internal/app/server.go`

### 4. 上游返回格式不同

#### OpenAI 账号

OpenAI 上游直接返回内部兼容的 `responses` 事件流。

特点：

- 本地基本直接消费
- 不需要额外做 chat streaming 协议翻译

#### DeepSeek GLM custom 账号

如果走 `/v1/chat/completions`，上游返回的是标准 chat streaming chunk，例如：

- `choices[].delta.content`
- `choices[].delta.tool_calls`
- `finish_reason`

这些返回不能直接给内部逻辑使用，必须先桥接回内部 `responses` 事件。

对应代码：

- `server/internal/codex/client.go` 中 `convertChatCompletionsStream`

### 5. 工具调用的处理方式不同

#### OpenAI 账号

OpenAI `/codex/responses` 上游天然返回内部兼容的工具调用事件。

所以：

- 工具调用是原生链路
- 本地更多是透传和最终转换给 Cursor

#### DeepSeek GLM custom 账号

DeepSeek/GLM 若返回的是标准 OpenAI chat `tool_calls`，本地还需要主动桥接：

- `delta.tool_calls` -> `response.output_item.added`
- 参数增量 -> `response.function_call_arguments.delta`
- 参数完成 -> `response.function_call_arguments.done`

如果没有这层桥接，Cursor 不会继续执行本地工具。

对应代码：

- `server/internal/codex/client.go`

### 6. 历史消息的重建要求不同

#### OpenAI 账号

发给 OpenAI `/codex/responses` 时，内部 `input` 本身就已经接近上游协议。

#### DeepSeek GLM custom 账号

发给 `/v1/chat/completions` 时，必须把内部历史重建成标准 OpenAI chat 历史：

- `function_call` / `custom_tool_call` -> assistant `tool_calls`
- `function_call_output` / `custom_tool_call_output` -> tool message

如果不重建：

- 历史会退化成普通文本
- 上游会把“之前的工具调用”当作聊天内容
- 后续容易出现模型口头描述执行，但没有真实工具调用

对应代码：

- `server/internal/codex/client.go` 中 `marshalAuditChatCompletionsRequest`

### 7. 为什么之前 DeepSeek 会出现“说已执行，但本地没执行”

根因不是 Cursor 展示问题，而是 custom chat 请求缺少标准工具定义。

之前 `custom /v1/chat/completions` 没把：

- `tools`
- `tool_choice`

传给上游。

结果是：

- DeepSeek/GLM 只能当普通聊天模型回复
- 它会输出“应该如何执行”的文本
- 但不会产生真实工具调用
- Cursor 因此不会执行本地命令，也不会创建文件

而 OpenAI 走的是原生 `/codex/responses` 工具链，所以不会走到这个问题。

### 8. 总结

一句话概括当前差异：

- OpenAI：内部请求几乎原样发到原生 `/codex/responses`，返回也基本原生消费
- DeepSeek/GLM：内部请求要先改写成标准 `/v1/chat/completions` 的 `messages/tools`，返回后再桥接回内部 `responses` 事件

因此在处理数据时：

- OpenAI 主要是“透传 + 少量兼容”
- DeepSeek/GLM 主要是“协议改写 + 历史重建 + 返回桥接”
