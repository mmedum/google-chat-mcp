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
   ├──► internal/directory   resolve the user ids Chat sent no address for, cached
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

### A dry run cannot write

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

### The person confirms what cannot be taken back

The interaction hint asks a client to put a person in the loop, and
whether it does is the client's decision. So when the client supports
MCP form elicitation, the server asks the person itself before eight
writes: `delete_message`, `delete_space`, `delete_custom_emoji`,
`add_member`, a `send_message` whose text mentions everyone in the
space, `<users/all>`, an `update_message` that edits such a mention
in, since an edit notifies whoever it newly mentions, and an
`update_space` or `create_space` that opens a space to a target
audience, since whoever is in it can read the space and making it
private again does not take that back. The mention is
matched regardless of case. `GCM_ASK_BEFORE_SEND=true` makes every post
and every edit ask.
The set is the rare, irreversible or wide-reaching writes, chosen by the
maintainer on 2026-09-29; an ordinary post asks nothing by default,
because a question asked dozens of times a day is answered without
reading, and the rare ones need the attention.

- **A second gate, not a replacement.** `GCM_ALLOW_DESTRUCTIVE`,
  `confirm_space_id` and every other check come first; a call they
  refuse asks nothing. The question comes after the reads, just before
  the write, so it shows what the write would do: the message's sender
  and the start of its text, the space by name, the person added, the
  post's text.
- **Accepting is the confirmation.** The form has no fields. Anything
  but an accept — decline, cancel, an error, an answer after its
  question expired — is `[blocked]`, refused before anything runs, and
  nothing is changed. The refusal never says the person declined: a
  client can answer without showing anyone anything. Two clients accept
  an empty form without a person choosing to, Codex under approval
  policy `never` with full access and VS Code when the question is
  skipped; a required choice would stop both, and was found slower and
  less clear than Accept in Claude Code, so the form stays empty.
- **No question possible.** A client that declares no form elicitation
  gets no question, as before. `GCM_REQUIRE_PROMPT=true` refuses those
  writes there instead. A dry run never asks.
- **What the question quotes** stands in a code span, on one line:
  format and control characters removed, backticks, grave and acute
  marks and quote marks folded to a plain single quote, links broken,
  cut at 120 characters and a post at 300. A client that draws the
  question as Markdown, as VS Code does, shows a code span literally,
  and a blank line between lines keeps them apart.
- **The answer is bound to its question.** A signed, single-use state
  carries the tool, a hash of the arguments and of the question, a nonce
  and, where it travels through the client, a 5-minute expiry. The retry
  reads again and is refused if what it would do changed.
- **A failure after the answer is never "nothing changed".** A call the
  person confirmed that then ends without a result is
  `[ambiguous_outcome]`: it may have posted or deleted, and is not to be
  made again.

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

Chat sends a sender's or a member's address itself, and that answer
wins. Its display name wins too: when Chat names an address, the name
beside it is used over the People profile name. Only a user Chat sent
no address for is looked up in People, and there the People name wins
over Chat's. A user Chat sent an address but no name for is looked up
for the name alone. What Chat names is written to the directory cache,
so a later payload with no address, such as a reaction's user, still
resolves someone this server has seen. That covers a message's
mentions and a search's results as well as listings. A search reads the
cache but never asks People: it can match hundreds of messages.

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

### The gap to the API is written down

Two files, and the split is which of them a person writes.
`testdata/api-methods.json` is every method of the Chat, People and
Cloud Identity APIs with its verb and path, as Google published them; `make api-diff` writes
it and nobody edits it. `testdata/api-coverage.tsv` is one verdict per
method, by hand: `used` names the `gchat.Client` method that implements
it, `out` gives the reason it is deliberately not called.

`gates api-coverage` holds the two and the client to each other, offline,
in `make check`. A method with no verdict fails the build, so a
capability Google adds cannot arrive unnoticed; a call with no row fails
it too, so a new one cannot be added without saying which API method it
is; and a verdict on a method that no longer exists fails, so the record
cannot outlive what it was about.

Two of the rules exist because writing a limit down is not keeping it.
A `used` row is **bound** to the method it names: the HTTP verb and the
custom-method suffix in the client's request literal have to be the ones
Google publishes, so swapping the reasons on `markAsActive` and
`markAsAway` fails instead of passing. And a method no scope this server
asks for authorizes is **`unreachable`**, derived from the scopes in the
snapshot rather than argued in prose — seventeen rows used to make that
argument by hand, and adding `contacts` to `scopes.All` would have
falsified thirteen of them with nothing objecting. It now fails those
thirteen, which is the moment to decide what to do with the methods that
just came into reach.

It exists because nothing else here looks outward. The schema diff
compares this server with its own last release, and the live driver's
surface gate compares the driver with this server. `make api-diff`
refetches the discovery documents into the snapshot and reports what
changed, verb and path included — a method that keeps its name and moves
is a break the name alone would not show. It reaches the network, which
is why it is a target somebody runs rather than a gate CI depends on: a
gate that fails when Google is slow is one people learn to rerun until it
passes. What CI gets instead is the file it wrote, and the snapshot's
fetch date is printed on every run.

## Distribution

`go install`, or a signed archive from a release: six platforms, a
`checksums.txt` signed with a keyless Sigstore bundle, an SBOM per
archive, and build provenance. The README says how to verify each.

Two more artifacts come out of the same run. A `.mcpb` bundle for Claude
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
| A link in a message is an annotation, not text | `text` carries the words that were linked; the target is in `annotations[].richLinkMetadata`. `richLinkType` is DRIVE_FILE, CHAT_SPACE, GMAIL_MESSAGE, MEET_SPACE or CALENDAR_EVENT — there is no CHAT_MESSAGE, and a link to one message is CHAT_SPACE with `chatSpaceLinkData.message` set. Discovery document, 2026-09-17. Dropping the annotation left a message whose whole body was a link reading as a bare word |
| A quote can be created through the API, unlike a link chip | `quotedMessageMetadata` takes the quoted message's name and its `lastUpdateTime` on create, and Google rejects a stale timestamp. No tool here sets it, so `send_message` cannot make one and the live driver cannot manufacture the case: the quotes behind this were made in the Chat client and read back by hand on 2026-09-17 |
| Chat makes no rich link for a link posted through the API | A message whose body is a message URL comes back with no annotations at all — the chip is made by the Chat client, not by the API. Live 2026-09-17, twice: once with a URL of the driver's own shape and once with the permalink byte for byte. So the live suite cannot manufacture the case it checks, and a link pasted by a person is the only thing that produces one |
| A pasted link decodes as CHAT_SPACE naming the message | Read back through the shipped binary on 2026-09-17 from a link pasted in the Chat client: `richLinkType` CHAT_SPACE, with the space, the thread and the message all named, and the annotation spanning the whole body. The message's own `text` was the anchor words and nothing else, which is the bug in one line |
| A message permalink has three path segments and a query | `room/{space}/{thread}/{message}?cls=10`, read off a real one on 2026-09-17. A message created through the API is named `{id}.{id}` and the URL carries the half before the dot, which is also its thread's id |
| A message search returns the matched message, annotations included | `view` gates the metadata around a hit — `read` and `spaceMuteSetting` — not the fields of the message itself, which is why the basic view is described as "only the matched messages … but no additional metadata". Discovery document, 2026-09-17. Not settled live, and the live run of 2026-09-17 could not settle it: the hit it searched for carried no annotations for either route to drop, because the API had made none |
| A quoted message's `sender` is a DISPLAY NAME, not a resource name | The reference calls it "the quoted message's author name", which this repository read as `users/{id}` and wrote into a comment. A real reply-quote and a real forward, both read through the shipped binary on 2026-09-17, carried a person's name. It is passed through verbatim and never looked up: `people/Jane Doe` is a malformed id, and sending one costs a junk entry in the batch that resolves the real senders. Google answers that batch per person, so the other addresses still resolved — which is how the mistake survived its first live read |
| A forward names the space it came from, and a DM by the other person | `forwardedMetadata` carried the source space and its display name on a real forward, 2026-09-17; for a direct message that name is the other participant, as the reference says. Whether a forward also carries the links and files the reference promises is still unverified: the one forwarded here had neither |
| A quote's snapshot is filled differently for a reply and a forward | A REPLY carries `sender` and `text` only; `formattedText`, `annotations` and `attachments` are documented as populated for FORWARD alone, and `forwardedMetadata` only for a forward. Reference, 2026-09-17. So an empty link list on a reply-quote is Google saying nothing, not the quoted message having no links, and the tool schemas say which is which. For a forward the snapshot is the only copy a caller can reach: the source space is usually one they are not in |
| A deleted message answers 200 with a tombstone, not 404 | Which is why a repeat delete must read the answer rather than the status. Live 2026-09-05 |
| A deleted space answers 403, not 404 | So "deleted" cannot be told from "not yours", and the refusal message does not claim to know which. Live 2026-09-05 |
| `User.email` is filled for senders and members, external people included | Discovery (revision 20260922): filled under user auth for a message's `sender`, a mention and a `Membership`, "provided the user is a member of the space or has prior affinity". Live 2026-09-27 over 39 spaces, with the scopes this server requests: every same-domain human member and nearly every sender carried it, and so did an external member and external senders in a space that admits guests. Apps never did. Twelve humans came back with no address, most of them in direct messages and likely accounts that are gone; People resolved none of them, nor the external member Chat did name. So Chat's address is used first and People only fills a gap |
| `role` is silently ignored when adding a member | Google answers 200 and records ROLE_MEMBER. The tool dropped the argument and says to follow with `update_member_role`; the result reads the role off the answer, which is what made this visible. Live 2026-09-05 |
| An umbrella scope satisfies every narrower scope split out of it | Verified against the discovery document. Without the table the server refuses calls Google allows, and refuses them for the people who paid the most for consent |
| A person's token can quote, and cannot forward | The create-messages guide quotes with `chat.messages.create` under user auth. `quotedMessageMetadata.lastUpdateTime` must be the quoted message's latest, or the post fails, so the server reads it first. Live 2026-10-08: a REPLY quote posted and read back. FORWARD, GA on 2026-06-12, was refused with "Can't forward a message" three ways: a main-chat message into its own space, a thread reply into another thread of its space, and a message into another space. So `send_message` quotes a message in the same space only |
| A search hit's read state may be left out when false | Google's JSON leaves a false boolean out, so an unread hit may carry no `read` field, the same as every hit without the read-state scope. The server reads one hit marked read, or `unread_only`, as proof the silent hits are unread, and says null otherwise. A scratch space cannot show which Google does: every message in it is the caller's own, and live 2026-10-08 a hit in a space just marked unread still came back read |
| A person's token can post Markdown | The REST reference called `markupSyntax` output only on 2026-09-05. Discovery revision 20261005 calls it "Optional. Specifies how the server interprets the message `text`", and the release note of 2026-08-19 makes Markdown GA for messages created through the API. Live 2026-10-08: a Markdown post came back with its bold word in Chat's markup and its plain text without the asterisks |
| The media upload protocol is only in the discovery document | `media.upload` is a POST to a different base with a JSON metadata part; the guide does not say so. `downloadUri` is documented as not for downloading, and is never fetched |
| The Chat API has 54 methods and this server calls 50 | Discovery document, 2026-09-07. Two of the four left out are not choices: `spaces.completeImport` takes only `chat.import` and `spaces.messages.attachments.get` only `chat.bot`. The other two are — `spaces.create`, because `spaces.setup` does the same thing and adds the first members, and the PUT form of a message update, which would clear cards and attachments |
| `chat.bot` cannot be granted to this server at all | Google: "This scope only supports app authentication with service accounts. You can't authenticate with user credentials or with domain-wide delegation using this scope." Checked 2026-09-08. It is the only scope `spaces.messages.attachments.get` accepts, so that method is out of reach for a per-user client and nothing is lost by it: a message already carries its `attachment` with the `attachmentDataRef`, and the bytes come from `media.download`, which accepts `chat.messages.readonly` |
| A space's audience goes in a patch of its own | Discovery (revision 20260928), `spaces.patch`: `access_settings.audience` needs every other mask left out, a named space, a space manager, user authentication and no import mode. Masking it with no value removes the audience, which makes the space private. So `update_space` refuses an audience beside a name or description rather than sending two patches that could half land. Live 2026-10-03: the camelCase mask `accessSettings.audience` opened a space to `audiences/default` and the empty one made it private, each read back through `get_space` |
| Who may set a space's audience is documented two ways | The guide "Make a space discoverable to a target audience" (updated 2026-09-23) asks for a super administrator; discovery asks only for a space manager. Not settled: the maintainer runs it as an administrator, and the tool description names only the space manager rule Google enforces in the reference |
| `spaces.setup` takes `accessSettings.audience` | Its own reference never names it; only the target-audience guide says setup takes it. Live 2026-10-03: a space created with `audiences/default` read back `DISCOVERABLE`. So `create_space` keeps `audience` |
| No API lists target audiences | Their ids come from the Admin console URL, `admin.google.com/ac/targetaudiences/{id}`, per the same guide. `audiences/default` is the organization's default. So `audience` takes an id and cannot offer choices |
| `spaces.search` without admin access returns only joined spaces | The reference says results are "limited to spaces where the calling user is a joined member". The guide says only "spaces they have access to". Live 2026-10-08: a search with no name filter returned exactly the named spaces `list_spaces` returned. The tool had said it reached spaces the caller is not in |
| Quotas: 15 reads and 1 write per second per user | Chat API limits page, 2026-09-05. The limiter defaults follow it |
| A write with no idempotency key is retried on 429 only, not on 503 | `google/rpc/code.proto` defines UNAVAILABLE, which maps to HTTP 503, as transient but adds "it is not always safe to retry non-idempotent operations". A 503 on such a write is now as ambiguous as a 500. Read 2026-10-01; it reverses the earlier rule that a 503 meant Google turned the request away |

### Cloud Identity, for Google Groups

| What | Verdict |
|---|---|
| Chat names a group by its Cloud Identity id | Chat discovery (revision 20260928): a group member's name is "a group in Cloud Identity Groups API. Format: groups/{group}", and `spaces.setup` says to find it with `groups.lookup`; a group's email is not accepted. So `find_group` asks Cloud Identity and hands the name to `add_member` unchanged |
| `groups.lookup` turns an address into that name | Cloud Identity discovery (revision 20260930): `GET v1/groups:lookup?groupKey.id=<email>` answers `{"name": "groups/{id}"}`, and `groups.get` gives the group's `displayName` and its address in `groupKey.id` |
| `cloud-identity.groups.readonly` is the narrowest scope that works | Both methods accept it, `cloud-identity.groups` and `cloud-platform`; the other two are wider. Google's scopes page gives it no tier. The setup guide says an ordinary user may call the API with their own OAuth sign-in |
| Which groups a person can see is not documented per method | Google's help on group settings says a group's "Who can see group" decides who can find it, and lists "Group members" first. Live 2026-10-03: `groups.lookup` answers 403, not 404, for an address no group has, so a missing group and a hidden one look alike. `find_group` says it cannot tell which, rather than claiming either |
| A group's direct members come from `groups.memberships.list` | Cloud Identity discovery (revision 20260930): it accepts `cloud-identity.groups.readonly`, and returns each member's address in `preferredMemberKey.id`, its roles and its type. `searchTransitiveMemberships` would reach nested members, but only Enterprise and Cloud Identity Premium accounts may call it and the rest get a 403. Google shows a group's members only to someone the group's own settings allow, so a refusal costs that group's row its members and nothing else |
| Five Cloud Identity methods publish no scope | `customers.userinvitations.*` lists none in discovery revision 20260930. The coverage gate marks such a method `unreachable` with the reason `none published`, since there is no scope to name |
| A group read asks only for the fields it reads | `fields=name,groupKey,displayName`, the `fields` system parameter every Google API takes (cloud.google.com/apis/docs/system-parameters, the page `prettyPrint` cites). A group also carries its parent, labels and timestamps, and every field decoded but not modeled is logged as schema drift, so without it each read raised four false alarms |
| A disabled API answers 403 `SERVICE_DISABLED` | The AIP-193 `ErrorInfo` reason; the older frontend says `accessNotConfigured`. It was reported as a plain permission refusal, which points the person at the wrong fix, so it has its own message naming the setup page |

### MCP, and the clients that read it

| What | Verdict |
|---|---|
| A tool's `outputSchema` has no slot in the Messages API | A custom tool there takes `name`, `description`, `input_schema` and `input_examples`; there is no output-schema field, and the pricing note counts "tool names, descriptions, and schemas" in the `tools` parameter. Docs, 2026-09-17. MCP's own spec gives `outputSchema` to the client — "Clients SHOULD validate structured results against this schema" — so output-schema bytes are protocol traffic and client-side validation, not a measured cost in the model's context. What the model reads, and what the guidance says to write in full, is the tool description |
| The schema generator inlines a nested type everywhere it appears | `github.com/google/jsonschema-go` models `$defs` and `$ref` but `For[T]()` emits neither: it errors on a cycle and never de-duplicates. Read at v0.4.3, 2026-09-17. So a type used twice on one tool is serialized twice, and `quote` carrying links and attachments cost 16.5 KB across the four message tools. Hand-writing the schemas would buy that back at the price of schemas that can drift from the Go types they describe |
| A reply carries both halves | The spec asks a tool returning structured content to also return the serialized JSON in a text block. They are never the same bytes here: `content` is a compact rendering, `structuredContent` the machine one |
| Claude Code forwards only `structuredContent` | Measured 2026-09-06 by asking the client through its own transcript, after google-docs-mcp found the same. So the rendering is invisible in that client — and claude.ai and ChatGPT show the text half, which is why both are still sent |
| `anthropic/requiresUserInteraction` is a vendor key, and an absolute one | It appears nowhere in the MCP spec, which puts human-in-the-loop on the application: "the protocol itself does not mandate any specific user interaction model". In Claude Code the mark refuses every headless route, including the documented `--permission-prompt-tool`. Interactive auto mode, by contrast, prompts for nothing. `GCM_INTERACTION_HINT` is the way out for a deployment with nobody at the keyboard |
| Strict inputs: a struct's schema refuses unknown properties | Which is right for a tool and wrong for a permission hook — a hook that refuses a payload it does not model fails in a place that implicates a different tool entirely |
| A server can ask the person to confirm a write through form elicitation, on every protocol | The `ElicitRequestFormParams` type in the specification's `schema.ts` for 2025-06-18, 2025-11-25 and 2026-07-28, the 2026-07-28 multi-round-trip pattern, and the MCP Go SDK v1.8.0's `mcp/server.go`, read 2026-09-28. A result may carry `inputRequests` with a signed `requestState`; before 2026-07-28 the SDK sends `elicitation/create` itself and calls the handler again. `requestedSchema` may have no properties. The client answers "from the user or other sources", so an accept is never proof a person read anything. Held by tests on all three protocols |
| A client draws an elicitation question as plain text | Refuted. VS Code builds it as `new MarkdownString(elicitation.message)`, untrusted, so emphasis, link text and HTML-like text draw and single line breaks join (`mcpElicitationService.ts`, `main` at 251bcf5f, 2026-09-29). Quoted text is a code span, and lines stand apart |
| A required choice confirms better than an empty form | Declined for now. Codex accepts a form with no properties by itself under approval policy `never` with full access, and VS Code resolves a skipped question as `accept` (`codex-rs/codex-mcp/src/elicitation.rs` at c248f6d4; `mcpElicitationService.ts`). But in Claude Code 2.1.284 a choice list took the maintainer 60 seconds against 8 for the empty form, and read as confusing |
| A tool the server confirms itself should carry the mark too | Refuted 2026-10-09, after the owner was asked twice for one delete in google-docs-mcp: the client prompted for the mark, then the server asked. No source recommends two hard gates for one call; GitHub's `delete_repository` and Supabase confirm with `destructiveHint` plus a form elicitation, and Claude Code's documentation scopes the mark to "tools whose permission prompt is itself the point". `tools/list` drops the mark, for a client that can ask, from the four tools that ask before every write (`AsksEveryCall`). A send that asks only before `@all`, and a write that never asks, keep it, since there it is the only per-call prompt |
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
afterward rather than on what the model said.

Two rules make the results mean anything. A task can declare itself
**unreachable** — the world would not have permitted the end state —
so it is skipped rather than scored against the model; both of this
suite's first failures were checks that could never have passed, and one
was found before it ever ran. And a tool result is written down only
when the call that produced it named the scratch space, so a report can
carry nothing from a real conversation.
