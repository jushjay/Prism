# Prism

[English](README.en.md)

Prism 是一个自托管代理与管理后台，用于管理账号池、兼容 OpenAI 的接口、用量分析、基础访问控制和模型路由。

后端使用 Go 编写，管理后台使用 React、Umi Max 和 Ant Design 构建。

## 功能特性

- 支持 OpenAI OAuth 账号导入与 token 刷新。
- 支持自定义上游账号管理。
- 提供兼容 OpenAI 的 `/v1/chat/completions`、`/v1/responses` 和 `/v1/models` 接口。
- 支持按账号、模型和时间范围查看用量分析。
- 提供后台登录认证、Bearer Token API 访问、IP 过滤和访问来源统计等安全控制。
- 支持可选的请求与出站审计日志，便于排查问题。
- 当 `web/dist` 存在时，可由 Go 服务直接托管前端页面。

## 适用场景

Prism 主要面向可信任运维者的自托管场景。当前重点验证过的工作流集中在 Cursor 自定义 API 接入与代理使用场景。

其他 IDE、编辑器或 API 客户端理论上也可能可用，但目前没有做过系统性的兼容性测试。如果你需要的是一个面向多客户端的通用代理平台，请在依赖本项目之前自行评估兼容性需求。

## 致谢

本项目部分思路参考了 [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy)。如果你需要更通用的代理方案，这是一个非常不错的选择。

## 仓库结构

- `server/`: Go API 服务端。
- `web/`: React + Ant Design 管理后台。
- `docs/`: 设计说明与实现参考文档。
- `.env.example`: 本地配置模板。

运行时状态默认保存在 `data/` 目录中。`data/` 已被 Git 忽略，因为其中可能包含 SQLite 数据库、OAuth token、审计日志、提示词、响应内容、请求头、账号元数据和客户端 IP 信息等敏感数据。

## 环境要求

- Go 1.25+
- Node.js 20+
- pnpm，或启用了 pnpm 的 Corepack

## 快速开始

1. 创建本地环境变量文件：

```bash
cp .env.example .env
```

2. 编辑 `.env`，至少设置以下内容：

```bash
PROXY_API_KEY=<strong-random-secret>
OPENAI_OAUTH_CLIENT_ID=<client-id-if-using-oauth>
```

`PROXY_API_KEY` 同时作为后台登录密码，以及 `/v1/*` 路由默认的 Bearer Token。

3. 启动后端服务：

```bash
make api-dev
```

4. 在另一个终端安装依赖并启动前端开发服务：

```bash
make web-install
make web-dev
```

5. 构建前端产物，供后端统一托管：

```bash
cd web
pnpm build
```

当 `web/dist` 存在时，Go 服务可以在同一域名下直接提供前端页面。

## 配置说明

主要配置入口是 `.env`。常用配置包括：

- `HOST` 与 `PORT`：后端监听地址。
- `PROXY_API_KEY`：后台密码与默认 API Bearer Token。
- `STORAGE_DB_DIR` 或 `STORAGE_DB_FILE`：可选的 SQLite 存储路径覆盖。
- `OPENAI_BASE_URL`：上游 Codex 后端 API 基础地址。
- `OPENAI_OAUTH_CLIENT_ID`：用于导入账号的 OAuth Client ID。
- `DEFAULT_MODEL`：默认路由模型。
- `DEFAULT_REASONING_EFFORT`：默认 reasoning effort。
- `CURSOR_AUDIT_LOG_ENABLED`、`OPENAI_EGRESS_AUDIT_LOG_ENABLED`、`CUSTOM_EGRESS_AUDIT_LOG_ENABLED`：可选审计日志开关。

审计日志默认关闭。开启后，日志中可能包含敏感的请求体、响应体、请求头、账号标签以及客户端 IP 元数据。

## 认证说明

- 后台登录使用 `PROXY_API_KEY`。
- 兼容 OpenAI 的 `/v1/*` 路由要求 `Authorization: Bearer <token>`。
- Bearer Token 可以使用全局 `PROXY_API_KEY`，也可以使用单账号配置的代理密钥。

示例：

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer ${PROXY_API_KEY}"
```

## 开发命令

```bash
make api-dev
make api-env-check
make web-install
make web-dev
make dev-info
```

后端测试：

```bash
cd server
go test ./...
```

前端构建：

```bash
cd web
pnpm build
```

## 安全说明

在将 Prism 暴露到任何你无法完全信任的网络之前，请至少确认：

- 设置强度足够高的 `PROXY_API_KEY`。
- 不要把 `.env`、`data/`、数据库、WAL/SHM 文件、日志、备份文件或认证文件提交到版本库。
- 除非确有需要，否则保持审计日志关闭。
- 面向公网部署时，应放在可信反向代理之后。
- 后台访问权限仅开放给可信运维人员。
- 如果密钥曾经被提交、分享或泄露，应立即轮换。

公开或半公开部署前，请先阅读 [SECURITY.md](SECURITY.md)。

## 发布前检查

仓库已经尽量将本地运行文件加入忽略规则。发布前建议再次确认：

- `git status --ignored --short` 中没有意外被跟踪的敏感文件。
- `.env` 仅包含本地密钥，且未被 Git 跟踪。
- `data/` 未被 Git 跟踪。
- 审计日志未被 Git 跟踪。
- 旧 Git 历史中不存在你不希望公开的敏感数据或运行时文件。

## 许可证

本项目使用 [PolyForm Noncommercial License 1.0.0](LICENSE)。

这是一种禁止商用的 source-available 许可证，不属于 OSI 定义下的开源许可证。

通俗来说，个人使用、学习和修改在许可证条款允许范围内，但不允许商业用途。
