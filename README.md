# sub2api-cursor-sidecar

[English](README.en.md)

让 Cursor 通过「自定义模型」稳定使用 [sub2api](https://github.com/Wei-Shaw/sub2api) 的轻量 sidecar，部署在 nginx 与 sub2api 之间。

## 解决什么问题

- **思考强度丢失**：Cursor 调用自定义模型时不带 `reasoning_effort`，上游只能用默认强度。sidecar 按 `sidecar.yaml` 为每个模型指定强度，例如 `gpt-5.4-high` 会被改写为 `gpt-5.4` + `high`。
- **协议不兼容**：sub2api 使用 Codex OAuth 账号时，Cursor 发出的 Chat Completions / Responses 请求无法直接使用。sidecar 会把请求转换为 sub2api 的 `/v1/responses` 流式请求，再把结果转换回 Cursor 需要的格式。

只有 Cursor 的请求（`User-Agent` 包含 `Cursor/`）会被改写，其他请求原样转发给 sub2api。sidecar 本身不做鉴权，`Authorization`（sub2api API Key）原样交给 sub2api 校验。

```
Cursor / 其他客户端 --> nginx --> sidecar (127.0.0.1:8090) --> sub2api (127.0.0.1:8080)
```

## 配合 sub2api 使用

1. **安装 sidecar**（需要 Go 1.25+）

   ```bash
   sudo ./deploy/sidecar/install.sh
   ```

   安装为 systemd 服务 `sub2api-cursor-sidecar`，配置文件位于 `/etc/sub2api-cursor-sidecar/`。

2. **指向 sub2api**：编辑 `/etc/sub2api-cursor-sidecar/sidecar.env`

   ```bash
   HOST=127.0.0.1
   PORT=8090
   SUB2API_BASE_URL=http://127.0.0.1:8080   # sub2api 根地址，不带 /v1
   ```

3. **配置模型与思考强度**：编辑 `/etc/sub2api-cursor-sidecar/sidecar.yaml`

   ```yaml
   models:
     gpt-5.4:      { reasoning_effort: medium }
     gpt-5.4-high: { target: gpt-5.4, reasoning_effort: high }
   ```

   key 是 Cursor 中添加的模型名；`target` 是发给 sub2api 的真实模型（省略时等于 key）；`reasoning_effort` 可选 `none` / `minimal` / `low` / `medium` / `high` / `xhigh`。

   修改后执行 `systemctl restart sub2api-cursor-sidecar`。

4. **修改 nginx**：把原来 `proxy_pass` 到 sub2api 的站点改为指向 `127.0.0.1:8090`，并关闭 `proxy_buffering` 以支持 SSE。完整示例见 [`deploy/sidecar/nginx.conf`](deploy/sidecar/nginx.conf)，也可以用 `install.sh --with-nginx` 安装。

5. **配置 Cursor**：在 Models 设置中填入 sub2api 的 API Key，把 *Override OpenAI Base URL* 设为 `https://<你的域名>/v1`，再添加 `sidecar.yaml` 里的模型名（如 `gpt-5.4-high`）。

健康检查：`curl http://127.0.0.1:8090/_sidecar/health`

## 本地开发

```bash
cp .env.example .env   # 按需修改 SUB2API_BASE_URL
make run
make test
```

## 致谢

部分思路参考了 [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy)。
