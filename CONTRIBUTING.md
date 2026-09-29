# Contributing

## Development setup

1. Copy `.env.example` to `.env` and point `SUB2API_BASE_URL` at a sub2api instance.
2. Run `make run`.

## Before submitting changes

- Keep changes focused and reviewable.
- Add or update tests when behavior changes.
- Run `make test`.
- Confirm no secrets or audit logs are staged.
- Update documentation when behavior, configuration, or deployment steps change.

## Pull request notes

- Describe the user-visible effect.
- Note any config impact (`sidecar.env`, `sidecar.yaml`).

## Security

- Never commit `.env`, `data/`, tokens or logs.
- Do not include real credentials in examples, tests, or documentation.
- Use private reporting for vulnerabilities as described in `SECURITY.md`.
