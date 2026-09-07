# Contributing to google-chat-mcp

Thanks for considering a contribution. This is a small Go MCP server for
Google Chat with a strict test and security posture. Read this once
before your first pull request; the rest is enforced by CI.

## Reporting bugs and requesting features

Open a [GitHub issue](https://github.com/mmedum/google-chat-mcp/issues/new/choose)
using the relevant template. Include `google-chat-mcp doctor`'s output
and the version — and note that the logs carry no message content,
addresses or search terms by design, so a debug log is safe to paste.

For anything security-sensitive — a vulnerability, a suspected token
leak — do not open a public issue. Follow `SECURITY.md`.

## Development setup

```bash
git clone https://github.com/mmedum/google-chat-mcp
cd google-chat-mcp
make build
```

Go is the only prerequisite, at the version in `go.mod`; the toolchain
line makes the `go` command fetch it if yours is older. Nothing else in
the repository needs an interpreter, which is deliberate: a gate that
shells out to a language you have not declared is a gate contributors
cannot run.

## Gates that must pass

```bash
make check
```

That is gofmt, `go vet` including every tagged suite, golangci-lint, race
tests with a coverage floor per package — 80% for everything except
`cmd/`, which has a lower one of its own printed on every run —
`govulncheck`, a licence allow-list, a stdio smoke test, a schema diff
against the released tool surface, an API-coverage gate, the live
driver's surface gate, and a staleness gate that fails when the README,
`docs/` or the CHANGELOG drift from the code. CI runs the same set on
Linux, macOS and Windows.

Four of those are worth knowing about before they fail on you:

- **The schema diff** compares the built binary with
  `testdata/schemas-baseline.json`. A renamed tool or a dropped output
  field breaks every caller and fails the build. A reshaped input is
  reported and allowed.
- **`internal/leakcheck`** fails on anything that could identify a real
  person, account or space — an email under a domain someone could own,
  a 21-digit account id, a space id that does not look invented, and the
  same ids base64-encoded. Write fixtures as `janedoe@example.com` and
  `spaces/AAAAspace1`: a made-up id has to say so. It also fails when it
  reads nothing, because a scan with no input and a clean repository
  report the same thing.

  **What it cannot catch is the payload.** An identifier has a shape;
  a message, a space's display name or a colleague's sentence does not,
  and no pattern separates one from invented prose. On a chat server
  that is most of what there is to leak. Two habits cover the gap, and
  only habits can: fixtures are **generated, never recorded** — a
  fixture pasted out of a live response is itself the leak — and a live
  run reads back only what it wrote. When a smoke record is written up,
  every id, address and name in it is a placeholder.
- **The API-coverage gate** works over two files. `testdata/api-methods.json`
  is every method of the Chat and People APIs as Google published them,
  written by `make api-diff` and never edited by hand.
  `testdata/api-coverage.tsv` is one verdict per method, written by hand:
  a `used` row names the `gchat.Client` method that implements it, an
  `out` row gives the reason it is deliberately not used. The gate holds
  the two and the client to each other, offline, so a method with no
  verdict and a call with no row both fail the build. It cannot see a
  method Google added yesterday — only `make api-diff` refetches, which
  is why the release checklist runs it.
- **The live driver's surface gate** fails when a tool is neither
  exercised by `internal/livecheck` nor excused there with a reason. Add
  a tool and this is what stops it going quietly unexercised. It needs no
  credentials — it reads what the built binary registers — which is why
  it runs in `make check` while the driver itself only runs under
  `make live`. If a tool genuinely must not be called against a real
  account, say so in `excused` and say why; an exemption with no reason
  is a gap nobody has decided about.

For workflow edits, run
`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`.

## Branch and commit conventions

- Branch names follow `type/short-description`: `feat/`, `fix/`,
  `refactor/`, `chore/`, `docs/`, `test/`. Release branches are
  `release/vX.Y.Z`.
- Commit messages use conventional-commit style with a scope —
  `fix(gchat): …`, `feat(tools): …`, `docs(readme): …`. Look at a recent
  `git log` for the tone.
- Keep the subject under 72 characters; use the body for why.
- Never commit directly to `main`. All work lands through a pull
  request.
- Force-push freely before your first review. After a maintainer has
  commented, add commits instead — we squash-merge, so intermediate
  history is discarded anyway, and force-pushing orphans review comments
  and re-arms the CI approval gate on fork pull requests. If you do
  force-push, use `--force-with-lease`.

## Pull requests

1. Open against `main`, with a summary and a test plan. The template has
   the skeleton.
2. CI must be fully green.
3. A maintainer reviews and merges, squashed by default.
4. The branch is deleted after merge.
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
[SemVer](https://semver.org/spec/v2.0.0.html) — see "Versioning" in the
README for what counts as breaking.

## Release process

Release cutting is maintainer-only:

0. Run `make live` and read the result. It drives the shipped binary
   against a real account inside one scratch space it creates and
   deletes, and it is the only thing here that can catch a wrong belief
   about the API — every fake in the unit suite is written from what we
   think Google does, and seven rows of the evidence log were settled by
   a live call contradicting the reference. `make check` cannot replace
   it and a green `make check` does not imply it. `make check` does run
   `live-surface`, which fails if a tool is neither exercised by the
   driver nor excused with a reason, so the gap is visible on every
   build rather than at a release.

   Run `make api-diff` at the same time. It is the only thing here that
   reaches Google's own documentation, and a manual target nobody runs is
   a gate that never fires. It refetches the method list into
   `testdata/api-methods.json` and reports what changed; every method it
   adds then fails `api-coverage` until a row in
   `testdata/api-coverage.tsv` says whether this server should use it.

1. Land a `release: cut vX.Y.Z …` commit on `main` that moves the
   `[Unreleased]` section under a `## [X.Y.Z] - YYYY-MM-DD` heading.
   `release.yml` lifts that section verbatim into the GitHub release
   notes, so read it once as the release note it becomes.
2. Wait for `ci.yml` to pass on that exact commit. `release.yml`'s
   `verify-ci` gate requires a green run for the tagged commit, so
   tagging first fails the release.
3. Tag: `git tag -a vX.Y.Z -m "vX.Y.Z: …" && git push origin vX.Y.Z`.
   Push tags one at a time — GitHub drops tag events past the third in a
   single push, and the release silently never runs.
4. `release.yml` builds six platform archives with goreleaser, packs the
   Claude Desktop `.mcpb` bundle, signs the checksums — which cover the
   bundle too — with a keyless Sigstore certificate, attaches an SBOM per
   archive and build provenance, and creates the GitHub release from the
   CHANGELOG section.
5. It then writes `server.json` from that release's own `checksums.txt`
   and publishes it to the MCP registry with `mcp-publisher`. That step
   runs last because the registry fetches the bundle's URL before it
   accepts the entry, so it needs the release to exist.

To rehearse the build without publishing anything:
`goreleaser release --snapshot --clean --skip=sign,sbom,publish`. It
needs nothing but the Go toolchain: `scripts/gates` packs the bundle.

The staleness gate accepts a release commit: it wants the changes since
the last tag written down either under `[Unreleased]` or under the
heading for the version being cut. Requiring `[Unreleased]` would fail
every release pull request, since CI runs before the tag exists.

## Code style

- Match the surrounding code. Do not reformat what you did not touch.
- Comments explain why — a constraint, an invariant, something Google
  does that the code has to work around. Do not narrate what the code
  does.
- Write plainly, in code comments as much as in the CHANGELOG. Lead with
  the outcome, one idea per sentence.
- Output types are hand-written shadows of the wire types, never embedded
  ones. The duplication is the point: a field Google adds upstream cannot
  reach the model without someone deciding it should.
- Unknown fields in a Google response are counted and logged by name,
  never rejected. Drift must be observable, not fatal — rejecting it cost
  the previous implementation two total outages.
- Every tool is registered through `internal/tools.register`, which
  derives the annotations, the read-only gating, the interaction hint and
  the rendered reply from one `Kind`.
- Logs record the call, never the payload. `docs/architecture.md` says
  what that means and where the non-obvious leak was.

`CLAUDE.md` has the short version of all of this, and
`docs/architecture.md` the reasoning — start there if you are wondering
why the code is the way it is.

## License

By contributing, you agree that your contributions will be licensed
under the project's [Apache 2.0 License](./LICENSE).
