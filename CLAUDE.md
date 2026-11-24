# CLAUDE.md — google-chat-mcp

A Go MCP server that exposes Google Chat over stdio: one binary per
person, per-user OAuth, nothing hosted. Why the code is the way it is
lives in `docs/architecture.md`, one decision per `D-NN`. What we
verified about Google lives in the comment at the line that depends on
it. Read the decision before changing the tool surface, the write path
or the auth wiring.

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

1. **Keep everything internal out of commits**, because the repository
   is public. That means organization names, space or message ids,
   account emails, Cloud project ids, OAuth client ids, secrets and
   content from real conversations, in fixtures, commit messages and
   committed logs alike. A made-up id says so: `spaces/AAAAspace1`, not
   `spaces/AAAQ`. `internal/leakcheck` fails the build (D-04).
2. **Write to stdout only in `main`.** Stdout carries only MCP JSON-RPC
   frames, because a stray print corrupts the stream. Logs go to stderr
   through `slog` (D-02). forbidigo holds it, with `scripts/` excluded.
3. **Log the call, not the payload.** Method, tool, resource id, outcome
   and duration are fine. Leave out message text, addresses, display
   names, tokens and search terms, because a debug log is still a file
   someone shares when asking for help (D-03).
4. **Post exactly the text `send_message` is given**, with no prefix and
   no suffix, because the message goes out under the person's name
   (D-14).
5. **Enforce a dry run in `internal/gchat`, not by convention.** The
   flag puts the call on a context `internal/gchat` refuses to write
   under, because a forgotten preview branch posts for real (D-09).
6. **Register every tool through `register`** (D-10). Give a risky
   variant its own tool rather than an argument (D-16), and every
   administrator call its own tool too (D-28), because a remembered
   approval covers a whole tool. The server asks the person nothing
   (D-15), and the `requiresUserInteraction` mark stays on the tools
   D-25 names.
7. **Send the JSON once in a reply, beside a readable rendering.**
   `structuredContent` follows the output schema, and `content` renders
   the same facts as text. Neither half can be dropped. Keep the JSON
   out of `content`, because the JSON in both halves spends the model's
   context twice (D-13).
8. **Extend the wire types in `internal/gchat` rather than importing
   `google.golang.org/api`**, because that package drags in gRPC and
   telemetry for a handful of JSON calls (D-05).
9. **Keep a released tool's name and output fields, and a resource
   template's URI**, because clients, saved permission rules and models
   address and read them by name or URI. `scripts/gates schema-diff`
   fails on a rename, a dropped field or a changed URI; adding is fine
   (D-07).
10. **Commit on a branch, not `main`, and leave pushing to the owner.**
11. **Back every belief about Google with a primary source.** Record
    what you verified, or found wrong, in the comment at the line that
    depends on it, with the source link and the `make live` step that
    checks it.

## Definition of done

- `make check` passes. On a pull request CI also runs `schema-ack`,
  which needs a commit footer naming each changed item of the tool
  surface, and `changelog`, which needs an entry under `[Unreleased]`
  when what users get changes, or a `Changelog: none` footer
  (`CONTRIBUTING.md`).
- New behavior has tests.
- `/simplify` and `/code-review medium` have run. A fixed finding is one
  line in the commit body. A declined one goes in the PR body, or
  becomes a present-tense constraint comment at the line it protects.
  Keep findings out of `docs/`.
- The schema diff has been read for anything breaking.
- Anything touching the write path or an API response shape has had a
  `make live` run. A release also needs `make api-diff`
  (`docs/release.md`).

## Traps

- **Green gates are not done.** Every fake in the unit suite is written
  from what we believe Google does, and only `make live` tests that
  belief. The example: Google answers a deleted message with a 200
  tombstone, not a 404, so a repeat delete has to read the answer.
- **An empty page is not the end of a listing.** Google applies the
  page size before it filters, so a page can be empty and still carry a
  token.
- **A forbidigo settings block that fails to load leaves forbidigo on
  its defaults**, still firing and looking right. Check the message
  text when you verify it.
- **Write a count into a doc only when a gate checks it.**
- **Supersede or deprecate a decision rather than deleting it.** Take
  the next ID from the file's `Next ID:` line rather than reusing one.

## Writing

Plain and short, everywhere: lead with the outcome, one idea per
sentence, no narration of the investigation. History goes in commit
messages. Follow the CHANGELOG rules in `CONTRIBUTING.md` exactly,
because `release.yml` lifts the section verbatim into the release notes.
