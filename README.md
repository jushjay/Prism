# sub2api-cursor-sidecar

[English](README.en.md)

一个部署在 nginx 与 [sub2api](https://github.com/Wei-Shaw/sub2api) 之间的轻量 sidecar，专门解决 Cursor 通过「自定义模型」访问 sub2api 时的两个问题：

1. **丢失思考强度**：Cursor 调用自定义模型时不会带上 `reasoning_effort`，导致上游始终使用默认强度。
2. **协议不兼容**：sub2api 使用 Codex OAuth 账号时，Cursor 发来的 Chat Completions / 变体 Responses 请求与上游不兼容。

本项目由 Prism 裁剪而来，只保留了 Cursor 请求的处理逻辑，不再包含账号池、OAuth、管理后台、数据库等功能。

## 链路

```
Cursor / 其他客户端 --> nginx --> sidecar --> sub2api
```

- **非 Cursor 请求**：原样反向代理到 sub2api（请求体、请求头、状态码、响应体、SSE、WebSocket 都不做修改）。
- **Cursor 请求**（`User-Agent` 包含 `Cursor/`，且为 `POST /v1/chat/completions` 或 `POST /v1/responses`）：
  1. 规范化请求（工具定义、`function_call_output`、`call_id`、结构化输出 schema 等）。
  2. 判定思考强度：请求里显式带了 `reasoning_effort` / `reasoning.effort` 就保留；否则使用 `sidecar.yaml` 中该模型配置的强度（每个模型一一对应）。未配置的模型不注入强度，模型名原样转发。
     请求未带 `model` 时直接返回 400。
  3. 统一转换为 Responses 请求，发往 sub2api 的 `/v1/responses`（上游固定 `stream=true`、`store=false`）。
  4. 把上游 SSE 转回 Cursor 需要的格式：Chat Completions chunk（含 `reasoning_content`、`tool_calls`、usage）或 Responses 事件；非流式请求会聚合为完整 JSON。
  5. sub2api 返回的错误（状态码 + 响应体）直接透传给 Cursor。

sidecar 不做任何鉴权：客户端的 `Authorization`（即 sub2api 的 API Key）原样转发给 sub2api 校验。

## 配置

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HOST` / `PORT` | `127.0.0.1` / `8090` | 监听地址 |
| `SUB2API_BASE_URL` | `http://127.0.0.1:8080` | sub2api 根地址（不带 `/v1`） |
| `UPSTREAM_PRESERVE_HOST` | `true` | 反代时保留客户端的 `Host` 头 |
| `SIDECAR_CONFIG` | `server/configs/sidecar.yaml` | 模型与思考强度配置；显式指定时文件必须存在 |
| `DATA_DIR` | `./data` | 审计日志目录 |
| `CURSOR_AUDIT_LOG_ENABLED` / `UPSTREAM_AUDIT_LOG_ENABLED` | `false` | Cursor 入站 / sidecar 出站审计日志（jsonl，含提示词，`Authorization` 会脱敏） |

### sidecar.yaml

每个 key 是 Cursor 发送的模型名（即 Cursor 中添加的自定义模型），每个模型对应唯一的思考强度：

```yaml
models:
  gpt-5.4:       { reasoning_effort: medium }                   # 基础模型的默认强度
  gpt-5.4-high:  { target: gpt-5.4, reasoning_effort: high }    # 虚拟模型：改写为 gpt-5.4 + high
  gpt-5.4-xhigh: { target: gpt-5.4, reasoning_effort: xhigh }
```

- `target`：发往 sub2api 的真实模型，省略时等于 key。
- `reasoning_effort`：必填，可选 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`；缺失、非法或模型重复都会导致启动失败。

修改后需重启服务。

## 本地运行

```bash
cp .env.example .env   # 按需修改 SUB2API_BASE_URL
make run               # 启动
make test              # go vet + go test
curl http://127.0.0.1:8090/_sidecar/health
```

需要 Go 1.25+。

## 部署

```bash
sudo ./deploy/sidecar/install.sh               # 编译并安装 systemd 服务 sub2api-cursor-sidecar
sudo ./deploy/sidecar/install.sh --with-nginx  # 同时安装 nginx 站点示例
```

- 配置位于 `/etc/sub2api-cursor-sidecar/sidecar.env` 与 `sidecar.yaml`，已存在时不会被覆盖。
- nginx 示例见 [`deploy/sidecar/nginx.conf`](deploy/sidecar/nginx.conf)：把原来 `proxy_pass` 到 sub2api 的站点改为指向 sidecar，并关闭 `proxy_buffering` 以支持 SSE。
- 健康检查：`GET /_sidecar/health`（不会转发给 sub2api）。

## Cursor 设置

在 Cursor 的 Models 设置中：

1. 打开 *OpenAI API Key*，填入 sub2api 的 API Key。
2. 打开 *Override OpenAI Base URL*，填 `https://<你的域名>/v1`。
3. 添加自定义模型，例如 `gpt-5.4`、`gpt-5.4-high`（与 `sidecar.yaml` 中 `models` 的 key 一致）。

## 仓库结构

- `server/cmd/prism`：程序入口
- `server/internal/app`：路由分流、反向代理、Cursor 请求处理与流式转换
- `server/internal/codex`：Responses 请求模型、清洗序列化、调用 sub2api
- `server/internal/openai`：Chat Completions -> Responses 转换
- `server/internal/schema`：结构化输出 tuple schema 兼容
- `server/internal/reasoning`：`sidecar.yaml` 模型与思考强度配置
- `deploy/sidecar`：systemd、nginx、安装脚本

## 致谢

部分思路参考了 [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy)。
