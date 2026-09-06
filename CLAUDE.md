# CLAUDE.md — google-chat-mcp project instructions

Project-specific rules for Claude Code in this repository. The user's
global instructions still apply; this file adds to them.

## Mission

A Go MCP server that exposes Google Chat over stdio, distributed to
other people. One binary, per-user OAuth, no hosted deployment. The
design, the evidence log and the constraints that must not be undone
live in `docs/architecture.md`. Read it before changing the tool
surface, the write path or the auth wiring. Its evidence log says what
was checked, against which source, and which live call contradicted it.

## Hard rules

1. **Nothing internal, ever.** No organisation names, space or message
   ids, account emails, Cloud project ids, OAuth client ids or secrets,
   and no content from real conversations — not in fixtures, not in
   commit messages, not in logs. Fixtures are synthetic, and a made-up
   id says so: `spaces/AAAAspace1`, never `spaces/AAAQ`.
   `internal/leakcheck` fails the build on one, base64-encoded ids
   included.
2. **Stdout carries only MCP JSON-RPC frames.** Logs use `slog` to
   stderr. Never `fmt.Println` on the server path.
3. **Logs never carry the payload.** Method, tool, resource id, outcome
   and duration are fine. Message text, addresses, display names, tokens
   and search terms are not — a search term reaches a log through the
   request URL, which is why transport errors are stripped of it.
   `TestLogsNeverCarryThePayload` holds this.
4. **`send_message` posts the body verbatim.** No prefix, no suffix,
   nothing appended server-side, ever.
5. **A dry run may not reach the network.** The flag puts the call on a
   context `internal/gchat` refuses to write under. Do not add a preview
   that works by convention instead.
6. **Every tool goes through `register`.** It decides the annotations,
   the read-only gating, the interaction hint and the rendered reply from
   one `Kind`. Four rules kept by hand at fifty call sites is four ways
   to be quietly wrong.
7. **A reply carries both halves.** `structuredContent` under the output
   schema, and a readable rendering in `content` — never the same JSON
   twice. `internal/tools/render.go` says why, with the sources.
8. **Own wire types, raw REST.** Do not import `google.golang.org/api`;
   it drags in gRPC and telemetry for a subset we hand-write. Extend
   `internal/gchat`.
9. **The released tool surface is a contract.** Tools keep their names
   and output fields; `scripts/schema-diff.sh` fails on a rename or a
   lost field. Adding is fine.
10. **No auto-commit, no auto-push.** Work on a branch; the owner pushes.
11. **Verify a convention against a primary source** before adopting it,
    and record the verdict in the evidence log.

## Where things go

- `cmd/google-chat-mcp/` — subcommands and server wiring.
- `internal/config/` env plus bound flags; `internal/credentials/`
  env → keyring → file; `internal/userconfig/` non-secret profile state;
  `internal/auth/` loopback OAuth and the token source;
  `internal/scopes/` the scope set and what each tool needs.
- `internal/gchat/` REST client and wire types, with retry, limiters and
  drift reporting; `internal/directory/` the email cache and resolver.
- `internal/service/` the rules worth testing — idempotency, degrading,
  search, section moves; `internal/tools/` the MCP tools, their schemas
  and their renderings; `internal/server/` SDK wiring and the schema dump.
- `internal/doctor/` the live check; `internal/leakcheck/` and
  `internal/version/` the build stamp.
- `scripts/gates/` is every check this repository runs on itself, as Go:
  the schema diff, the coverage floor, the smoke test, the staleness
  gate. It is under `scripts/` and not `internal/` because it never
  ships, and it is Go and not shell so that the code holding the gates
  shut is held to them too.
- `internal/livecheck/` drives the shipped binary against a real account,
  behind a `live` build tag; `internal/evals/` scores a model driving the
  tools, behind an `evals` one. Neither runs in CI. `livecheck`'s surface
  gate does, because it needs no credentials.
- `testdata/schemas-baseline.json` — the released tool surface, which a
  change may add to but never drop from.

## Definition of done

`make check`, which is what CI runs: gofmt, `go vet` including every
tagged suite, golangci-lint, race tests with an 80% floor per package —
`cmd/` has its own lower floor, printed on every run, because the OAuth
flow and the serve loop have no seam a unit test can reach — plus
`govulncheck`, the licence allow-list, a stdio smoke test, the schema
diff, the live driver's surface gate, and the staleness gate over README,
`docs/` and CHANGELOG. Plus tests for new behaviour, `/simplify` and
`/code-review high` with findings resolved or written down, and a look at
the schema diff for anything breaking.

Before a release, `make live` as well. It drives the shipped binary
against a real account and is the only thing here that can catch a wrong
belief about the API, because every fake in the unit suite is written
from that belief. `make check` cannot replace it and green does not imply
it.

Green gates are not done. A repeat-delete bug once went through every
one of them, because a unit test asserts the behaviour the code was
written to have — Google answers a deleted message with a tombstone, and
nothing local knew that. Anything touching the write path or an API
response shape gets a live run before it counts.

## Writing

Plain and short, everywhere it lands — code comments, commit messages,
CHANGELOG, docs, tool descriptions. Lead with the outcome. One idea per
sentence. No narration of the investigation.

CHANGELOG rules are in `CONTRIBUTING.md` and are not optional: Keep a
Changelog sections only, `**Breaking:**` on anything needing the reader
to act. `release.yml` lifts the section verbatim into the release notes,
so the entry is the release note.
