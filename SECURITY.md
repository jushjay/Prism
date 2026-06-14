# Security Policy

## Supported use

This project is intended for self-hosted deployments operated by trusted administrators.

## Sensitive data

This application can process and store sensitive runtime data, including:

- OAuth access tokens and refresh tokens
- request and response payloads
- account metadata
- client IP information
- usage statistics

Treat the `data/` directory and `.env` file as secrets.

## Safe deployment baseline

- Set a strong `PROXY_API_KEY` before exposing the service.
- Keep `.env` and `data/` out of version control and backups shared with others.
- Keep audit logging disabled unless you explicitly need it.
- If audit logging is enabled, secure the generated log files and define retention rules.
- Run behind a trusted reverse proxy if exposed to the internet.
- Restrict dashboard access to trusted operators.
- Rotate credentials if secrets were ever committed or shared.

## Reporting a vulnerability

Do not open a public issue for a suspected security vulnerability.

Report it privately to the project maintainer through a private channel before public disclosure.

## Logging warning

The optional audit logs can contain full request bodies, response bodies, headers, account labels, and client IP metadata. Enable them only with clear operator intent.
