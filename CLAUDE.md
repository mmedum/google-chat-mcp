# CLAUDE.md — google-chat-mcp

A Go MCP server that exposes Google Chat over stdio: one binary per
person, per-user OAuth, nothing hosted. Why the code is the way it is
lives in `docs/architecture.md`, one decision per `D-NN`. What we
verified about Google lives in `docs/evidence.md`, one entry per
`E-NNN`. Read the decision before changing the tool surface, the write
path or the auth wiring.

## Commands

- `make check` runs every gate CI runs; its `check` target is the list.
- `make live` drives the shipped binary against a real account. It
  needs `LIVE_GROUP_EMAIL`.
- `make api-diff` refetches Google's discovery documents.
- `make release-rehearse` builds the release archives and publishes
  nothing.
- `go run ./scripts/gates` lists the gates; add a gate's name to run
  that one.

## Hard rules

1. **Never commit anything internal.** No organization names, space or
   message ids, account emails, Cloud project ids, OAuth client ids or
   secrets, and no content from real conversations: not in fixtures,
   commit messages or logs. A made-up id says so: `spaces/AAAAspace1`,
   never `spaces/AAAQ`. `internal/leakcheck` fails the build (D-04).
2. **Never write to stdout outside `main`.** Stdout carries only MCP
   JSON-RPC frames, and logs go to stderr through `slog` (D-02).
   forbidigo holds it, with `scripts/` excluded.
3. **Never log the payload**: message text, addresses, display names,
   tokens or search terms. Method, tool, resource id, outcome and
   duration are fine (D-03).
4. **Never alter what `send_message` posts.** No prefix, no suffix
   (D-14).
5. **Never add a dry run that works by convention.** The flag puts the
   call on a context `internal/gchat` refuses to write under (D-09).
6. **Never register a tool outside `register`** (D-10). Never make a
   risky variant an argument: it is a tool of its own (D-16), and so is
   every administrator call (D-28). The server asks the person nothing
   (D-15), and the `requiresUserInteraction` mark stays on the tools
   D-25 names.
7. **Never send the same JSON twice in a reply.** `structuredContent`
   follows the output schema, and `content` is a readable rendering
   (D-13).
8. **Never import `google.golang.org/api`.** Extend the wire types in
   `internal/gchat` (D-05).
9. **Never rename a released tool or drop an output field**, nor change
   a resource template's URI. `scripts/gates schema-diff` fails on one;
   adding is fine (D-07).
10. **Never commit to `main` or push.** Work on a branch; the owner
    pushes.
11. **Never adopt a belief about Google without a primary source.**
    Record what you verified, or found wrong, as an `E-NNN` entry.

## Definition of done

- `make check` passes. On a pull request CI also runs `schema-ack`,
  which needs a commit footer naming each changed item of the tool
  surface (`CONTRIBUTING.md`).
- New behavior has tests.
- `/simplify` and `/code-review high` have run. A fixed finding is one
  line in the commit body. A declined one goes in the PR body, or
  becomes a present-tense constraint comment at the line it protects.
  Never write findings into `docs/`.
- The schema diff has been read for anything breaking.
- Anything touching the write path or an API response shape has had a
  `make live` run. A release also needs `make api-diff`
  (`docs/release.md`).

## Traps

- **Green gates are not done.** Every fake in the unit suite is written
  from what we believe Google does, and only `make live` tests that
  belief. A repeat delete answering with a tombstone is the example
  (E-017).
- **An empty page is not the end of a listing.** Google applies the
  page size before it filters (E-005).
- **A forbidigo settings block that fails to load leaves forbidigo on
  its defaults**, still firing and looking right. Check the message
  text when you verify it.
- **Do not write a count into a doc** unless a gate checks it.
- **Never delete a decision or reuse an ID.** Supersede or deprecate
  it, and take the next ID from the file's `Next ID:` line.

## Writing

Plain and short, everywhere: lead with the outcome, one idea per
sentence, no narration of the investigation. History goes in commit
messages. The CHANGELOG rules in `CONTRIBUTING.md` are not optional,
because `release.yml` lifts the section verbatim into the release notes.
