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

See [`CLAUDE.md`](./CLAUDE.md) §Commands for the canonical gate commands
(`ruff check`, `ruff format --check`, `ty check`, `pytest` with the 80 %
coverage floor). CI runs the same set on every PR; the PR template
includes a checklist.

For workflow edits, sanity-check with
`rhysd/actionlint:1.7.12 .github/workflows/<file>.yml`.

## Branch and commit conventions

- Branch names follow `type/short-description`: `feat/`, `fix/`,
  `refactor/`, `chore/`, `docs/`, `test/`. Release branches are
  `release/vX.Y.Z`.
- Commit messages use conventional-commit style with a scope —
  `fix(stdio): …`, `feat(tools): …`, `docs(readme): …`. Look at
  recent `git log` for the tone.
- Keep the subject under 72 chars; use the body for context and `Why`.
- Never commit directly to `main`. All work lands via PR.
- Force-push freely before your first review. After a maintainer has
  commented, add commits instead — we squash-merge, so intermediate
  history is discarded at merge anyway, and force-pushing orphans review
  comments and re-arms the CI approval gate on fork PRs. If you do
  force-push, use `--force-with-lease`; maintainers sometimes push
  directly to PR branches.

## PR process

1. Open against `main`. Include a summary + test plan (the PR template
   has the skeleton).
2. CI must be fully green: ruff, ty, pytest (with coverage), hadolint,
   trivy, pip-audit, gitleaks, Docker build.
3. A maintainer reviews and merges. Squash-merge is the default.
4. After merge, the branch is deleted.

## Changelog

Every user-visible change gets an entry under `## [Unreleased]` in
`CHANGELOG.md`, in the same PR that makes the change. Internal refactors
with no user-visible effect do not.

- **Sections** are the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
  set and nothing else — Added, Changed, Deprecated, Removed, Fixed,
  Security — in that order. Not `Internal`, not `Documented`, not a
  severity split like `Security — High`.
- **Length: 3-6 lines per entry.** What changed, who it affects, what it
  does now. Do not narrate the investigation, quote logs, or defend the
  fix against alternatives you rejected — that is what the PR is for, and
  the reader can click through.
- **Breaking changes** are marked `**Breaking:**` and say what the deployer
  has to do, in the imperative. Anything needing an OAuth re-consent, a
  re-login, or a config change is breaking.
- **Write for someone deciding whether to upgrade.** Lead with the impact,
  not the mechanism.

`release.yml` lifts the matching section verbatim into the GitHub release
notes, so the entry is the release note. Versioning follows
[SemVer](https://semver.org/spec/v2.0.0.html) — see "Versioning and support"
in the README for what counts as breaking.

## Release process

Release cutting is maintainer-only:

1. Land a `release: cut vX.Y.Z …` commit on `main` that updates
   `CHANGELOG.md`. On a minor bump, that commit must also move the
   `ghcr.io/mmedum/google-chat-mcp:X.Y` tag in `compose.yml` and
   `README.md` — that pin is what the quickstart deploys, and CI fails
   the commit if it disagrees with the newest CHANGELOG heading.
2. Tag: `git tag -a vX.Y.Z -m "vX.Y.Z: …" && git push origin vX.Y.Z`.
3. `release.yml` picks it up, builds the multi-arch image, pushes to
   GHCR with SBOM + provenance, and creates the GitHub release from the
   matching CHANGELOG section.

## Code style

- Match the surrounding code — the project has a consistent style
  enforced by `ruff` + `ty`. Don't reformat untouched code.
- Comments explain WHY (non-obvious constraints, invariants,
  workarounds). Don't narrate WHAT the code does.
- Pydantic models at tool I/O use `extra="forbid"` — that is our own
  contract, and an unrecognised key from a calling model must be
  rejected. Chat-API response models use `extra="allow"` and report
  unknown keys via `schema_drift` + `mcp_schema_drift_total`: drift must
  be observable, not fatal. See `docs/architecture.md`.
- Secret fields in `Settings` are `pydantic.SecretStr`; read them via
  `.get_secret_value()`.

`CLAUDE.md` at repo root has deeper architectural context — start there
if you're wondering *why* the code is the way it is.

## License

By contributing, you agree that your contributions will be licensed
under the project's [Apache 2.0 License](./LICENSE).
