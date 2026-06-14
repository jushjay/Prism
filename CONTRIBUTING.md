# Contributing

## Development setup

1. Copy `.env.example` to `.env`.
2. Set `PROXY_API_KEY` and any other required values.
3. Run `make api-dev`.
4. Run `make web-install && make web-dev` for frontend work.

## Before submitting changes

- Keep changes focused and reviewable.
- Add or update tests when behavior changes.
- Run:

```bash
cd server
go test ./...
```

```bash
cd web
pnpm build
```

- Confirm no secrets, runtime databases, or audit logs are staged.
- Update documentation when behavior, configuration, or deployment steps change.

## Pull request notes

- Describe the user-visible effect.
- Note any config or migration impact.
- Include screenshots for dashboard UI changes when relevant.

## Security

- Never commit `.env`, `data/`, database files, tokens, backups, or logs.
- Do not include real credentials in examples, tests, or documentation.
- Use private reporting for vulnerabilities as described in `SECURITY.md`.
