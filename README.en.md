# Prism

Prism is a self-hosted proxy and dashboard for managing account pools, OpenAI-compatible API routes, usage analytics, basic access control, and model routing.

The backend is written in Go. The dashboard is built with React, Umi Max, and Ant Design.

## Features

- OpenAI OAuth account import and token refresh.
- Custom upstream account management.
- OpenAI-compatible `/v1/chat/completions`, `/v1/responses`, and `/v1/models` routes.
- Usage analytics grouped by account, model, and time range.
- Security controls including dashboard authentication, bearer-token API access, IP filtering, and source statistics.
- Optional request and egress audit logs for debugging.
- Static dashboard hosting from the Go service when `web/dist` exists.

## Intended Use

Prism is primarily a self-hosted tool for trusted operators. The currently validated workflow is focused on Cursor custom API integration and proxy use cases.

Other IDEs, editors, or API clients may work, but they have not been systematically tested. If you need a broad multi-client proxy platform, evaluate the compatibility requirements before depending on this project.

## Acknowledgements

This project was partly inspired by ideas from [`icebear0828/codex-proxy`](https://github.com/icebear0828/codex-proxy). If you need a more general-purpose proxy, that project is worth evaluating as well.

## Repository Layout

- `server/`: Go API server.
- `web/`: React + Ant Design dashboard.
- `docs/`: Design notes and implementation references.
- `.env.example`: Local configuration template.

Runtime state is stored in `data/` by default. The `data/` directory is intentionally ignored because it can contain SQLite databases, OAuth tokens, audit logs, prompts, responses, headers, account metadata, and client IP information.

## Requirements

- Go 1.25+
- Node.js 20+
- pnpm, or Corepack with pnpm enabled

## Quick Start

1. Create a local environment file:

```bash
cp .env.example .env
```

2. Edit `.env` and set at least:

```bash
PROXY_API_KEY=<strong-random-secret>
OPENAI_OAUTH_CLIENT_ID=<client-id-if-using-oauth>
```

`PROXY_API_KEY` is used as both the dashboard login password and the default bearer token for `/v1/*` routes.

3. Start the backend:

```bash
make api-dev
```

4. In another terminal, install and start the dashboard dev server:

```bash
make web-install
make web-dev
```

5. Build the dashboard for backend-hosted static serving:

```bash
cd web
pnpm build
```

When `web/dist` exists, the Go server can serve the dashboard from the same origin.

## Configuration

The main configuration surface is `.env`. Important values include:

- `HOST` and `PORT`: backend listen address.
- `PROXY_API_KEY`: dashboard password and default API bearer token.
- `STORAGE_DB_DIR` or `STORAGE_DB_FILE`: optional SQLite storage override.
- `OPENAI_BASE_URL`: upstream Codex backend API base URL.
- `OPENAI_OAUTH_CLIENT_ID`: OAuth client ID for account import.
- `DEFAULT_MODEL`: default routed model.
- `DEFAULT_REASONING_EFFORT`: default reasoning effort.
- `CURSOR_AUDIT_LOG_ENABLED`, `OPENAI_EGRESS_AUDIT_LOG_ENABLED`, `CUSTOM_EGRESS_AUDIT_LOG_ENABLED`: optional audit logging flags.

Audit logging is disabled by default. If enabled, logs can contain sensitive request bodies, response bodies, headers, account labels, and client IP metadata.

## Authentication

- Dashboard login uses `PROXY_API_KEY`.
- OpenAI-compatible `/v1/*` routes require `Authorization: Bearer <token>`.
- The bearer token can be the global `PROXY_API_KEY` or an account-specific proxy key if configured.

Example:

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer ${PROXY_API_KEY}"
```

## Development Commands

```bash
make api-dev
make api-env-check
make web-install
make web-dev
make dev-info
```

Backend tests:

```bash
cd server
go test ./...
```

Frontend build:

```bash
cd web
pnpm build
```

## Security Notes

Before exposing Prism to any network you do not fully trust:

- Set a strong `PROXY_API_KEY`.
- Keep `.env`, `data/`, databases, WAL/SHM files, logs, backups, and auth files out of version control.
- Keep audit logging disabled unless explicitly needed.
- Put the service behind a trusted reverse proxy for internet-facing deployments.
- Restrict dashboard access to trusted operators only.
- Rotate credentials immediately if they were ever committed, shared, or exposed.

Read [SECURITY.md](SECURITY.md) before running a public or semi-public deployment.

## Release Hygiene

This repository is prepared so that local runtime files are ignored by Git. Before publishing a fork or release, verify:

- `git status --ignored --short` does not show unexpected tracked secrets.
- `.env` contains only local secrets and is not tracked.
- `data/` is not tracked.
- Audit logs are not tracked.
- You are not publishing old Git history that previously contained secrets or runtime data.

## License

This project is licensed under the [PolyForm Noncommercial License 1.0.0](LICENSE).

This is a noncommercial source-available license. It is not an OSI-approved open-source license.

In practical terms, personal use, learning, and modification are allowed under the license terms, but commercial use is not permitted.
