# Security Policy

## Supported use

This project is intended for self-hosted deployments operated by trusted administrators.

## Authentication

The sidecar performs no authentication of its own. The client's `Authorization` header (a sub2api API key) is forwarded to sub2api, which is responsible for validating it. Do not expose the sidecar port directly; keep it bound to `127.0.0.1` behind nginx.

## Sensitive data

When audit logging is enabled the sidecar writes, under `DATA_DIR`:

- full Cursor request bodies (prompts, tool definitions)
- the rewritten requests sent to sub2api and upstream error bodies
- client IP and User-Agent metadata

`Authorization`, `x-api-key`, `api-key`, `cookie` and `set-cookie` headers are redacted.

## Safe deployment baseline

- Keep audit logging disabled unless you explicitly need it.
- If audit logging is enabled, secure the generated log files and define retention rules.
- Keep `.env`, `sidecar.env` and `data/` out of version control.

## Reporting a vulnerability

Do not open a public issue for a suspected security vulnerability.

Report it privately to the project maintainer through a private channel before public disclosure.
