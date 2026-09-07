# Architecture

How `google-chat-mcp` is put together, and the decisions a contributor
should not undo without a reason better than the one that put them here.

## Shape

One Go binary. It speaks MCP over stdio as a subprocess of the client,
holds one person's credentials, and calls Google's REST APIs directly.
There is no server to host, no shared deployment, no service account and
no domain-wide delegation.

That is a deliberate narrowing. An HTTPS transport would need an
authorization server of its own, because Google supports neither dynamic
client registration nor client ID metadata documents, and the Go SDK's
`auth` package is a resource server only: it verifies bearer tokens and
serves protected-resource metadata, and issues nothing. Bridging that gap
means running and securing an OAuth server, which is a different project.
Claude on the web and on mobile cannot reach a stdio server, and that is
the cost.

## Packages

```
cmd/google-chat-mcp/     subcommands, flag parsing, server wiring
internal/config/         GCM_* environment plus bound flags, validated once at start
internal/userconfig/     non-secret per-profile state: client secret path, account, scopes
internal/credentials/    refresh token: environment, then OS keyring, then a 0600 file
internal/auth/           loopback OAuth login, and the token source that refreshes
internal/scopes/         the scope set, the umbrella table, and what each tool needs
internal/gchat/          REST client and wire types for Chat, People and OIDC
internal/directory/      email cache and the resolver that fills it
internal/service/        the rules: idempotency, degrading, search, section moves
internal/tools/          MCP tools, their schemas, and their renderings
internal/server/         SDK wiring and the schema dump
internal/doctor/         the live check behind `google-chat-mcp doctor`
internal/leakcheck/      fails the build on anything identifying a real account
scripts/gates/           every check the repository runs on itself
```

## Request flow

```
MCP client
   │  JSON-RPC over stdio, newline-delimited; stdout carries nothing else
   ▼
internal/server        SDK session, tool and resource registration
   │
   ▼
internal/tools         decode the input, call the service, shape both halves of the reply
   │                   register() applies the annotations, the read-only gating,
   │                   the interaction hint and the dry-run context
   ▼
internal/service       the rules worth testing, and the only place that
   │                   turns a Google failure into a [class] tool error
   ├──► internal/directory   resolve user ids to names and addresses, cached
   ▼
internal/gchat         one request: rate limiter, retry with backoff, drift-reporting decode
   │
   ▼
internal/auth          refresh token → access token, cached until it expires
   │
   ▼
Google Chat, People and OIDC
```

## Decisions

### Stdout is the protocol

Newline-delimited JSON-RPC frames and nothing else. Logs go to stderr
through `slog`. One stray `fmt.Println` corrupts the session before the
client's first request completes, which is why the smoke test asserts on
every line the server writes.

### Logs record the call, never the payload

A log line may say which method ran, against which resource, how it
ended and how long it took. It may not carry message text, an email
address, a display name, a token or a search term.

Resource ids are the deliberate exception: a space id is how an operator
follows something up, the logs go to their own stderr, and an id on its
own says nothing about what was said.

The subtle leak is the request URL. `net/http` wraps every transport
failure in a `*url.Error` whose message renders the whole URL, and a
People search puts the caller's search term in the query string — so an
ordinary timeout would write "who did this person look up" into the
logs. `withoutURL` strips it. `TestLogsNeverCarryThePayload` drives the
tools with a payload full of canaries and fails if one appears at any
level; reverting `withoutURL` makes it fail, which is the check that the
test is worth having.

### Own wire types, raw REST

No `google.golang.org/api`. It drags in gRPC and telemetry for a subset
we hand-write, and the generated types would decide our output shapes.
`internal/gchat` declares only the fields this server reads.

### Output types shadow the wire, they do not embed it

Every `*Output` struct in `internal/tools` is written out by hand rather
than embedding a wire type. The duplication buys an allow-list: a field
Google adds upstream cannot reach the model without someone deciding it
should. Their field names come from `testdata/schemas-baseline.json` and
are a contract — a rename breaks every caller, and the schema diff fails
on one.

### Drift is observable, never fatal

Google adds fields to the Chat API without notice. Decoding rejects
nothing: an unknown field increments a counter and logs its path once,
by name only. `doctor` walks live responses and reports what it saw. The
alternative — strict decoding — turns an upstream addition into a total
outage, which is exactly what happened here, twice.

### A dry run cannot reach the network

`dry_run` is not a promise each handler keeps. The flag puts the call on
a context `internal/gchat` refuses to write under, so a tool that
declares `dry_run` and forgets its own preview branch fails loudly
instead of posting. 25 tools carry the flag. The staleness gate holds
that number to the shipped surface, because it had already drifted: the
docs said thirteen when the server registered twenty-five.

### One `Kind` decides four things

`register` takes a tool's `Kind` — read, write, idempotent write,
destructive — and derives from it the MCP annotations, whether
`GCM_READ_ONLY` leaves the tool registered, whether the client is asked
to put a person in the loop, and that the reply is rendered. Four rules
enforced by author discipline is four ways to be quietly wrong.

The interaction hint follows "irreversible, and other people see it"
rather than "destroys something". Those rank differently: a deleted
sidebar section is private and reversible, while a posted message cannot
be recalled from anyone's notifications and an invited member can read
the whole space.

It is a request, not a control. Claude Code in its auto permission mode
was observed running `send_message` with no prompt, and the spec says
clients must treat tool annotations as untrusted. What actually holds is
server-side: `GCM_READ_ONLY` leaves the tool unregistered, and a dry run
cannot write.

**Recorded deviation.** The shared standard leaves destructive tools
unregistered unless a flag enables them. This server keeps them
registered and refuses the *call* instead, because the released surface
is the contract this server has to keep and a tool that vanishes is a
broken client rather than a safer one: the model cannot tell "not
permitted here" from "this server cannot do that". `GCM_READ_ONLY` is
the same opt-in running the other way. (The 28 tools that number once
referred to were the Python server's, before the port. This server
registers 53.)

The guard is on by default. It was off until the first outside person
installed all three of these servers in one sitting and asked why Chat
was the loose one — Drive and Docs gate deletes behind a variable that
starts off, and Chat is the one of the three where the delete cannot be
undone. A Drive file goes to a trash you can restore from; a deleted
Chat message is gone.

### A reply carries both halves, and they are not the same bytes

`structuredContent` under the output schema, and a readable rendering in
`content`. The MCP specification asks for both — "for backwards
compatibility, a tool that returns structured content SHOULD also return
the serialized JSON in a TextContent block", unchanged from 2025-06-18
through 2025-11-25 — and SEP-1624 asks that the two be semantically
equivalent rather than one being a stringified copy of the other.

Neither half can be dropped, because clients disagree about which they
show the model: Claude Code forwards only `structuredContent`
(anthropics/claude-code#55677), while claude.ai and ChatGPT show the
text. `internal/tools/render.go` holds the renderings, and the rule that
they must carry every fact the JSON carries is held by tests, because no
gate can check it.

### `send_message` posts the body verbatim

No prefix, no suffix, no "sent by an assistant" footer. If a caller wants
attribution, the caller writes it.

Each message carries a client-chosen id, so a retry after a timeout
cannot post twice: Google answers `ALREADY_EXISTS`, and the server
fetches what landed and returns that.

### Idempotency is checked, not assumed

`delete_message` and `remove_member` treat a 404 as success. A 403 is
different: Google spells "already deleted, and this space keeps no
history" and "you may not delete this" the same way, so the refusal path
reads the target back and only a not-found answer counts as gone. An
earlier version assumed otherwise, and so reported a refused delete as
one that had already happened.

`delete_section` succeeds on 404 only. Its 403 is a system section
refusing deletion, and that is worth reporting.

The trap underneath: a deleted message reads back as a 200 tombstone, not
a 404. Found by a live smoke test after every unit test passed.

### A People failure costs one field, never a row

Enrichment is best effort. When a directory lookup fails, the rows come
back without email addresses; a list is never emptied and a row is never
dropped because a name could not be resolved.

### Scopes are named exactly

A missing-scope 403 becomes a `[scope]` error naming the scope URL and
the command that fixes it, because Google's own message does not.
`internal/scopes` also records which umbrella satisfies which narrower
scope, verified against the discovery document — without that table the
server refuses calls Google would have allowed, and refuses them for the
people who paid the most for consent.

## Distribution

`go install`, or a signed archive from a release: six platforms, a
`checksums.txt` signed with a keyless Sigstore bundle, an SBOM per
archive, and build provenance. The README says how to verify each.

Two more artefacts come out of the same run. A `.mcpb` bundle for Claude
Desktop, packed from goreleaser's universal-binary post hook — the one
point in the pipeline where every binary exists and the checksum file
has not been written, so the bundle ships inside the signature rather
than beside it. And an entry in the MCP registry, written by
`scripts/gates server-json` from that same checksum file and
published with `mcp-publisher`.

The bundle carries all three platforms Claude Desktop runs on. A
manifest picks a binary by platform and has no key for the architecture,
so every platform it claims has to work on both. macOS does through a
universal binary and Windows through amd64 under emulation. Linux has
neither, so the bundle carries both Linux binaries and a launcher that
reads `uname -m` and execs the right one.

Linux was left out at first, on the belief that it would mean two
bundles or a broken one. That was wrong in one direction and right in
another: Claude Desktop for Linux exists and ships x64 and arm64 both —
so excluding Linux served nobody, and a single Linux binary really would
have been wrong for real people rather than hypothetical ones. The
launcher is the third option neither of those considered. Checked
against the client's own manifest schema, whose platform enum is
`darwin`, `win32`, `linux`.

## Evidence log

Every convention here was checked against a primary source or a live
probe before it was adopted. The rows below are the ones that still
explain the code; each says what was checked, and when a live call
contradicted a document, which won.

### Google's Chat API

| What | Verdict |
|---|---|
| The message-search filter field is `create_time`, not `createTime` | The REST reference's own examples say `createTime` and Google rejects it. Settled live 2026-09-05 by sending the clause alone |
| The space-event filter field is `event_types`, plural | The mirror image: the reference's prose says `event_type` and its examples say `event_types`, and the examples are right. Settled live 2026-09-06. The same page has now been wrong in both directions, so it cannot be read once |
| A space-scoped message search must still pass `spaces/-` as the parent | Google refuses a real parent outright and names the fix: put the space in the filter. Live 2026-09-05 |
| Every media download is served as `application/octet-stream` | Whatever the attachment resource says its type is. So the attachment's own declared type is the one to report. Live 2026-09-06 |
| A page can come back EMPTY with a next page token still on it | Google applies `pageSize` before it filters, so a space holding two messages answers `pageSize=1` with no messages and a token. An empty page therefore does not mean the end, and a caller that stops on one reports a space with messages in it as quiet. The tool descriptions and the server instructions say so. Live 2026-09-07, found by a step written to assert the opposite |
| A deleted message answers 200 with a tombstone, not 404 | Which is why a repeat delete must read the answer rather than the status. Live 2026-09-05 |
| A deleted space answers 403, not 404 | So "deleted" cannot be told from "not yours", and the refusal message does not claim to know which. Live 2026-09-05 |
| `role` is silently ignored when adding a member | Google answers 200 and records ROLE_MEMBER. The tool dropped the argument and says to follow with `update_member_role`; the result reads the role off the answer, which is what made this visible. Live 2026-09-05 |
| An umbrella scope satisfies every narrower scope split out of it | Verified against the discovery document. Without the table the server refuses calls Google allows, and refuses them for the people who paid the most for consent |
| `markupSyntax` is output only | So a user-authenticated caller cannot ask for Markdown, whatever the release note implies. REST reference, 2026-09-05 |
| The media upload protocol is only in the discovery document | `media.upload` is a POST to a different base with a JSON metadata part; the guide does not say so. `downloadUri` is documented as not for downloading, and is never fetched |
| Quotas: 15 reads and 1 write per second per user | Chat API limits page, 2026-09-05. The limiter defaults follow it |

### MCP, and the clients that read it

| What | Verdict |
|---|---|
| A reply carries both halves | The spec asks a tool returning structured content to also return the serialized JSON in a text block. They are never the same bytes here: `content` is a compact rendering, `structuredContent` the machine one |
| Claude Code forwards only `structuredContent` | Measured 2026-09-06 by asking the client through its own transcript, after google-docs-mcp found the same. So the rendering is invisible in that client — and claude.ai and ChatGPT show the text half, which is why both are still sent |
| `anthropic/requiresUserInteraction` is a vendor key, and an absolute one | It appears nowhere in the MCP spec, which puts human-in-the-loop on the application: "the protocol itself does not mandate any specific user interaction model". In Claude Code the mark refuses every headless route, including the documented `--permission-prompt-tool`. Interactive auto mode, by contrast, prompts for nothing. `GCM_INTERACTION_HINT` is the way out for a deployment with nobody at the keyboard |
| Strict inputs: a struct's schema refuses unknown properties | Which is right for a tool and wrong for a permission hook — a hook that refuses a payload it does not model fails in a place that implicates a different tool entirely |
| Stdout carries JSON-RPC frames only | Spec, transports/stdio. One stray `fmt.Println` corrupts the protocol, which is what the smoke test guards |

### Releasing

| What | Verdict |
|---|---|
| cosign 3 made `--bundle` required | The old `--output-signature` and `--output-certificate` silently produce nothing. Release notes, v3.0.1, after a sibling repository's release died on it |
| Pinning an action does not pin what the action installs | So `cosign-release` and `syft-version` are pinned beside the SHAs. A half-pinned step reads exactly like a pinned one |
| goreleaser's `mcp` block cannot publish an MCPB package | The registry requires `fileSha256` for that type and the block has exactly `registry_type`, `identifier` and `transport`. Read out of its config at v2.18.0 and again at v2.18.1 |
| The registry enforces its MCPB rules in code, not in the published schema | A hash, a github.com or gitlab.com release-asset URL carrying "mcp", no `registryBaseUrl`, and a HEAD on the URL before it accepts the entry — so the entry can only be published after the release exists |
| `defaults.run` covers every `run` step in a workflow | Most specific wins, and an explicit `shell: bash` also turns on pipefail. Without it a step on the Windows runner gets PowerShell, and the mangling shows up on the line that runs |

## Agent evals

`internal/evals`, behind a build tag and never in CI, scores whether a
model can do the job through these tools: ten tasks, each against a
scratch space the harness creates, each judged on the space read back
afterwards rather than on what the model said.

Two rules make the results mean anything. A task can declare itself
**unreachable** — the world would not have permitted the end state —
so it is skipped rather than scored against the model; both of this
suite's first failures were checks that could never have passed, and one
was found before it ever ran. And a tool result is written down only
when the call that produced it named the scratch space, so a report can
carry nothing from a real conversation.
