# Contributing to google-chat-mcp

Thanks for considering a contribution. This is a small Go MCP server for
Google Chat with a strict test and security posture. Read this once
before your first pull request; CI enforces the rest.

## Reporting bugs and requesting features

Open a [GitHub issue](https://github.com/mmedum/google-chat-mcp/issues/new/choose)
using the relevant template. Include what
[`docs/troubleshooting.md`](docs/troubleshooting.md#reporting-a-problem)
asks for. The logs carry no message content, addresses or search terms,
so a debug log is safe to paste.

For anything security-sensitive, such as a vulnerability or a suspected
token leak, do not open a public issue. Follow [`SECURITY.md`](SECURITY.md).

## Development setup

```bash
git clone https://github.com/mmedum/google-chat-mcp
cd google-chat-mcp
make build
```

Go is the only prerequisite, at the version in `go.mod`; the toolchain
line makes the `go` command fetch it if yours is older. Nothing else in
the repository needs an interpreter, on purpose: a gate that shells out
to a language you have not declared is a gate contributors cannot run.

## Gates that must pass

```bash
make check
```

`make check` runs the gates CI runs, and the `check` target in the
`Makefile` is the list. Among them: gofmt, `go vet` including every
tagged suite, golangci-lint, and race tests with a coverage floor per
package. The floor is 80%, except for `cmd/`, which has a lower one of its
own printed on every run. The others include `govulncheck`, a license
allow-list, gitleaks, actionlint, a stdio smoke test, the schema diff,
the API-coverage gate and the live driver's surface gate. A staleness
gate fails when the README, `docs/` or the CHANGELOG drift from the code,
and the docs gate when a citation does not resolve. CI runs the unit
tests on Linux, macOS and Windows, and on a pull request `schema-ack`.

These are worth knowing before they fail on you:

- **The schema diff** compares the built binary's whole dump with
  `testdata/schemas-baseline.json`: the tools, the resource templates,
  the prompts and the instructions in each mode. A renamed tool or
  resource template, or a dropped output field, breaks every caller and
  fails the build. Anything else that moved is reported and allowed: a
  reshaped input, a description, an annotation, a `_meta` key, the
  instructions. A client decides from those whether to ask, so a
  reviewer has to see them. The baseline holds the surface of the
  CHANGELOG's newest release, and the diff fails when it holds any
  other. With no entry under `[Unreleased]`, the build is that release,
  so its surface must match the baseline exactly. The release commit
  records the baseline with `make schema-baseline VERSION=vX.Y.Z`. That
  refuses a build that drops a tool or an output field, and a build
  stamped with another version.

  On a pull request, CI also runs `schema-ack`. A commit footer has to
  name each item that changed since the merge base:
  `SCHEMA-CHANGE: send_message, update_message: destructiveHint true`.
  The names are the list before the first colon followed by white space;
  the text after it describes the change and names nothing. Name a tool
  by its name, a resource template by its URI, and the instructions as
  `instructions`. Put a name with a comma in it in backticks, and keep
  the list on one line: a wrapped line that starts `word: ` begins another
  footer, as Conventional Commits reads it. A `Token #value` footer such
  as `Closes #12` acknowledges nothing. If the change breaks something a
  release shipped, name that item in a `BREAKING CHANGE:` footer
  instead. When the gate fails, it prints the footer to add, in an empty
  commit, never an amend.
- **`internal/leakcheck`** fails on anything that could identify a real
  person, account or space: an email under a domain someone could own,
  a 21-digit account id, a space id that does not look invented, and the
  same ids base64-encoded. Write fixtures as `janedoe@example.com` and
  `spaces/AAAAspace1`: a made-up id has to say so. It also fails when it
  reads nothing, because a scan with no input and a clean repository
  report the same thing.

  **What it cannot catch is the payload.** An identifier has a shape; a
  message, a space's display name or a colleague's sentence does not,
  and no pattern separates one from invented prose. Two habits cover the
  gap, and only habits can. Fixtures are **generated, never recorded**,
  because a fixture pasted out of a live response is itself the leak.
  And a live run reads back only what it wrote. When a smoke record is
  written up, every id, address and name in it is a placeholder.
- **The API-coverage gate** works over two files.
  `testdata/api-methods.json` is every method of the Chat, People and
  Cloud Identity APIs as Google published them, written by
  `make api-diff` and never edited by hand. `testdata/api-coverage.tsv`
  is one verdict per method, written by hand: a `used` row names the
  `gchat.Client` method that implements it, and an `out` row gives the
  reason it is deliberately not used. The gate holds the two and the
  client to each other, offline, so a method with no verdict and a call
  with no row both fail the build. It cannot see a method Google added
  after the snapshot. Only `make api-diff` refetches, and
  [`docs/release.md`](docs/release.md) runs it before every release.
- **The live driver's surface gate** fails when a tool is neither
  exercised by `internal/livecheck` nor excused there with a reason. It
  is what stops an added tool going quietly unexercised. It needs no
  credentials, since it reads what the built binary registers, so it
  runs in `make check` while the driver itself runs only under
  `make live`. If a tool must not be called against a real account, say
  so in `excused` and say why; an exemption with no reason is a gap
  nobody has decided about.

- **The docs gate** holds the design docs to their IDs. A decision in
  `docs/architecture.md` is `D-NN`; an ID is never reused, and the file
  ends with its `Next ID:` line. Cite a decision by ID, or a doc by
  `docs/<file>.md#<heading-slug>`, never by section number. What you
  verified about Google goes in the comment at the line that depends on
  it, with the source link and the `make live` step that checks it,
  never in a file of its own. The gate fails on a citation that does
  not resolve, a decision whose "Held by" names a test or gate that does
  not exist, a package missing from the codemap, and a path, `make`
  target or gate a doc names that does not exist.

For workflow edits, run `make actionlint`.

## Branch and commit conventions

- Branch names follow `type/short-description`: `feat/`, `fix/`,
  `refactor/`, `chore/`, `docs/`, `test/`. Release branches are
  `release/vX.Y.Z`.
- Commit messages use conventional-commit style with a scope:
  `fix(gchat): …`, `feat(tools): …`, `docs(readme): …`. Look at a recent
  `git log` for the tone.
- Keep the subject under 72 characters; use the body for why.
- Never commit directly to `main`. All work lands through a pull
  request.
- Force-push freely before your first review. After a maintainer has
  commented, add commits instead. We squash-merge, so intermediate
  history is discarded anyway, and a force-push orphans review comments
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
`CHANGELOG.md`, in the same pull request that makes the change. Leave out
changes only contributors see, unless they must act on them.

- **An entry is a bullet at the margin.** The staleness gate counts
  nothing else, and nothing in fenced code. It wants one when a commit
  since the newest release tag changes what the binary is built from,
  the bundle manifest, or go.mod's lines other than `require` and `tool`.
- **Sections** are the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
  set and nothing else: Added, Changed, Deprecated, Removed, Fixed and
  Security, in that order. Not `Internal`, not `Documented`, not a
  severity split like `Security — High`.
- **One line per change, 25 words or fewer, impact first.** Write for
  someone deciding whether to upgrade: what changes for them, not the
  mechanism. The investigation, logs and rejected alternatives belong in
  the pull request.
- **Breaking changes** are marked `**Breaking:**`. Such an entry may add
  one sentence saying what to do, in the imperative. Anything needing an
  OAuth re-consent, a re-login or a config change is breaking.
- **Security fixes** name the CVE or GHSA.

`release.yml` lifts the matching section verbatim into the GitHub release
notes, so the entry is the release note. Versioning follows
[SemVer](https://semver.org/spec/v2.0.0.html); the README's
[Versioning](README.md#versioning) section says what counts as breaking.

## Releasing

Maintainers release as [`docs/release.md`](docs/release.md) says.

## Code style

- Match the surrounding code. Do not reformat what you did not touch.
- Comments explain why: a constraint, an invariant, something Google
  does that the code has to work around. Do not narrate what the code
  does.
- Write plainly, in code comments as much as in the CHANGELOG. Lead with
  the outcome, one idea per sentence.
- Output types are hand-written shadows of the wire types, never embedded
  ones. The duplication is the point: a field Google adds upstream cannot
  reach the model without someone deciding it should.
- Unknown fields in a Google response are counted and logged by name,
  never rejected. Drift must be observable, not fatal, because rejecting
  an unknown field turns every upstream addition into an outage.
- Every tool is registered through `internal/tools.register`, which
  derives the annotations, the read-only and destructive gating,
  `confirm` and the rendered reply from one `Kind`.
- Logs record the call, never the payload. `docs/architecture.md` says
  what that covers.

`CLAUDE.md` has the short version of all of this, and
`docs/architecture.md` the reasoning. Start there if you are wondering
why the code is the way it is.

## License

By contributing, you agree that your contributions will be licensed
under the project's [Apache 2.0 License](./LICENSE).
