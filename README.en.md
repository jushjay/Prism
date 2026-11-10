# sub2api-cursor-sidecar

[中文](README.md)

A small sidecar that lets Cursor use [sub2api](https://github.com/Wei-Shaw/sub2api) reliably through custom models. It sits between nginx and sub2api.

## What it solves

- **Lost reasoning effort**: Cursor does not send `reasoning_effort` for custom models, so upstream always uses its default. The sidecar assigns an effort per model via `sidecar.yaml`, e.g. `gpt-5.4-high` is sent as `gpt-5.4` + `high`.
- **Protocol mismatch**: when sub2api serves Codex OAuth accounts, the Chat Completions / Responses payloads Cursor sends do not work as-is. The sidecar converts them into streaming `/v1/responses` requests to sub2api and converts the results back into what Cursor expects.

Only Cursor requests (`User-Agent` contains `Cursor/`) are rewritten; everything else is proxied to sub2api untouched. The sidecar does no authentication: the `Authorization` header (your sub2api API key) is forwarded to sub2api.

```
Cursor / other clients --> nginx --> sidecar (127.0.0.1:8090) --> sub2api (127.0.0.1:8080)
```

## Using it with sub2api

1. **Install the sidecar** (requires Go 1.25+)

   ```bash
   sudo ./deploy/sidecar/install.sh
   ```

   This installs the `sub2api-cursor-sidecar` systemd service with config under `/etc/sub2api-cursor-sidecar/`.

2. **Point it at sub2api**: edit `/etc/sub2api-cursor-sidecar/sidecar.env`

   ```bash
   HOST=127.0.0.1
   PORT=8090
   SUB2API_BASE_URL=http://127.0.0.1:8080   # sub2api root URL, without /v1
   ```

3. **Configure models and reasoning effort**: edit `/etc/sub2api-cursor-sidecar/sidecar.yaml`

   ```yaml
   models:
     gpt-5.4:      { reasoning_effort: medium }
     gpt-5.4-high: { target: gpt-5.4, reasoning_effort: high }
   ```

   Keys are the model names you add in Cursor; `target` is the real model sent to sub2api (defaults to the key); `reasoning_effort` is one of `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max`.

   Run `systemctl restart sub2api-cursor-sidecar` after changes.

4. **Update nginx**: change the site that used to `proxy_pass` to sub2api so it points at `127.0.0.1:8090`, with `proxy_buffering off` for SSE. See [`deploy/sidecar/nginx.conf`](deploy/sidecar/nginx.conf), or install it with `install.sh --with-nginx`.

5. **Configure Cursor**: in Models settings, enter your sub2api API key, set *Override OpenAI Base URL* to `https://<your-domain>/v1`, and add the model names from `sidecar.yaml` (e.g. `gpt-5.4-high`).

Health check: `curl http://127.0.0.1:8090/_sidecar/health`

## Development

```bash
cp .env.example .env   # adjust SUB2API_BASE_URL
make run
make test
```

## Acknowledgements

Some ideas come from [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy).
