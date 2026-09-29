# sub2api-cursor-sidecar

[中文](README.md)

A small sidecar that sits between nginx and [sub2api](https://github.com/Wei-Shaw/sub2api) and fixes two problems when Cursor talks to sub2api through a custom model:

1. **Lost reasoning effort**: Cursor does not send `reasoning_effort` for custom models, so upstream always uses its default.
2. **Protocol mismatch**: when sub2api serves Codex OAuth accounts, the Chat Completions / Responses-variant payloads Cursor sends are not compatible with the upstream.

It is a trimmed-down Prism: only the Cursor request handling is kept. Account pools, OAuth, the dashboard and the database are gone.

## Request flow

```
Cursor / other clients --> nginx --> sidecar --> sub2api
```

- **Non-Cursor requests** are reverse-proxied to sub2api untouched (body, headers, status, SSE and WebSocket).
- **Cursor requests** (`User-Agent` contains `Cursor/` and the request is `POST /v1/chat/completions` or `POST /v1/responses`):
  1. The request is normalized (tool definitions, `function_call_output`, `call_id`, structured-output schemas).
  2. Reasoning effort: an explicit `reasoning_effort` / `reasoning.effort` is kept. Otherwise the effort configured for that
     model in `sidecar.yaml` is used (exactly one effort per model). Unlisted models are forwarded unchanged without an effort.
     Requests without a `model` are rejected with 400.
  3. The request is converted to a Responses request and sent to sub2api `/v1/responses` (always `stream=true`, `store=false`).
  4. Upstream SSE is converted back into what Cursor expects: Chat Completions chunks (with `reasoning_content`, `tool_calls`, usage) or Responses events. Non-stream requests get an aggregated JSON body.
  5. sub2api errors (status code and body) are passed straight through.

The sidecar does no authentication: the client's `Authorization` header (the sub2api API key) is forwarded as-is.

## Configuration

### Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `HOST` / `PORT` | `127.0.0.1` / `8090` | Listen address |
| `SUB2API_BASE_URL` | `http://127.0.0.1:8080` | sub2api root URL (without `/v1`) |
| `UPSTREAM_PRESERVE_HOST` | `true` | Keep the client `Host` header when proxying |
| `SIDECAR_CONFIG` | `server/configs/sidecar.yaml` | Model / reasoning effort file; must exist when set explicitly |
| `DATA_DIR` | `./data` | Audit log directory |
| `CURSOR_AUDIT_LOG_ENABLED` / `UPSTREAM_AUDIT_LOG_ENABLED` | `false` | Inbound Cursor / outbound sidecar audit logs (jsonl, contain prompts, `Authorization` is redacted) |

### sidecar.yaml

Each key is the model name Cursor sends (the custom model added in Cursor), and each model has exactly one effort:

```yaml
models:
  gpt-5.4:       { reasoning_effort: medium }                   # effort for the base model
  gpt-5.4-high:  { target: gpt-5.4, reasoning_effort: high }    # virtual model: sent as gpt-5.4 + high
  gpt-5.4-xhigh: { target: gpt-5.4, reasoning_effort: xhigh }
```

- `target`: upstream model sent to sub2api; defaults to the key.
- `reasoning_effort`: required, one of `none`, `minimal`, `low`, `medium`, `high`, `xhigh`. A missing or invalid effort, or a duplicate model, fails startup.

Restart the service after changes.

## Running locally

```bash
cp .env.example .env   # adjust SUB2API_BASE_URL
make run
make test              # go vet + go test
curl http://127.0.0.1:8090/_sidecar/health
```

Requires Go 1.25+.

## Deployment

```bash
sudo ./deploy/sidecar/install.sh               # build and install the sub2api-cursor-sidecar systemd service
sudo ./deploy/sidecar/install.sh --with-nginx  # also install the example nginx site
```

- Config lives in `/etc/sub2api-cursor-sidecar/sidecar.env` and `sidecar.yaml`; existing files are never overwritten.
- See [`deploy/sidecar/nginx.conf`](deploy/sidecar/nginx.conf): point the site that used to `proxy_pass` to sub2api at the sidecar, with `proxy_buffering off` for SSE.
- Health check: `GET /_sidecar/health` (never forwarded to sub2api).

## Cursor settings

In Cursor's Models settings:

1. Enable *OpenAI API Key* and enter your sub2api API key.
2. Enable *Override OpenAI Base URL* and set it to `https://<your-domain>/v1`.
3. Add custom models such as `gpt-5.4` or `gpt-5.4-high` (matching the `models` keys in `sidecar.yaml`).

## Layout

- `server/cmd/prism`: entrypoint
- `server/internal/app`: routing, reverse proxy, Cursor handlers and stream conversion
- `server/internal/codex`: Responses request model, sanitizing/serialization, sub2api client
- `server/internal/openai`: Chat Completions to Responses translation
- `server/internal/schema`: tuple schema compatibility for structured outputs
- `server/internal/reasoning`: `sidecar.yaml` per-model reasoning efforts
- `deploy/sidecar`: systemd unit, nginx example, install script

## Acknowledgements

Some ideas come from [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy).
