# Contributing to google-chat-mcp

Thanks for considering a contribution. This project is an MCP server for
Google Chat — a relatively small Python codebase with a strict test and
security posture. Read this once before your first PR; the rest is
enforced by CI.

## Reporting bugs & requesting features

Open a [GitHub issue](https://github.com/mmedum/google-chat-mcp/issues/new/choose)
using the relevant template.

For anything security-sensitive (vulnerability, suspected token leak,
etc.) do **not** open a public issue — follow `SECURITY.md`.

## Development setup

```bash
git clone https://github.com/mmedum/google-chat-mcp
cd google-chat-mcp
uv sync --extra dev
uv run pre-commit install   # optional: local gitleaks + ruff hooks
```

Python 3.14 is required (pinned in `.python-version` and `pyproject.toml`).

## Gates that must pass

CI enforces these on every PR; run them locally before pushing:

```bash
uv run ruff check .
uv run ruff format --check .
uv run ty check
uv run pytest               # 80% coverage floor
```

Plus on the release branch only, but worth sanity-checking any workflow
change:

```bash
docker run --rm -v "$PWD":/repo -w /repo rhysd/actionlint:1.7.12 .github/workflows/<file>.yml
```

## Branch and commit conventions

- Branch names follow `type/short-description`: `feat/`, `fix/`,
  `refactor/`, `chore/`, `docs/`, `test/`. Release branches are
  `release/vX.Y.Z`.
- Commit messages use conventional-commit style with a scope —
  `fix(stdio): …`, `feat(tools): …`, `docs(readme): …`. Look at
  recent `git log` for the tone.
- Keep the subject under 72 chars; use the body for context and `Why`.
- Never commit directly to `main`. All work lands via PR.

## PR process

1. Open against `main`. Include a summary + test plan (the PR template
   has the skeleton).
2. CI must be fully green: ruff, ty, pytest (with coverage), hadolint,
   trivy, pip-audit, gitleaks, Docker build.
3. A maintainer reviews and merges. Squash-merge is the default.
4. After merge, the branch is deleted.

## Release process

Release cutting is maintainer-only:

1. Land a `release: cut vX.Y.Z …` commit on `main` that updates
   `CHANGELOG.md`.
2. Tag: `git tag -a vX.Y.Z -m "vX.Y.Z: …" && git push origin vX.Y.Z`.
3. `release.yml` picks it up, builds the multi-arch image, pushes to
   GHCR with SBOM + provenance, and creates the GitHub release from the
   matching CHANGELOG section.

## Code style

- Match the surrounding code — the project has a consistent style
  enforced by `ruff` + `ty`. Don't reformat untouched code.
- Comments explain WHY (non-obvious constraints, invariants,
  workarounds). Don't narrate WHAT the code does.
- Pydantic models at tool I/O use `extra="forbid"`; Chat-API response
  models also use `extra="forbid"` so schema drift surfaces as
  validation errors rather than silent drops.
- Secret fields in `Settings` are `pydantic.SecretStr`; read them via
  `.get_secret_value()`.

`CLAUDE.md` at repo root has deeper architectural context — start there
if you're wondering *why* the code is the way it is.

## License

By contributing, you agree that your contributions will be licensed
under the project's [Apache 2.0 License](./LICENSE).
