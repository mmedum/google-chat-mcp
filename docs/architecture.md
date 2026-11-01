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
   │                   register() applies the annotations, the read-only and
   │                   destructive gating, confirm and the dry-run context
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
instead of posting. A preview may read what it has to show or check:
a delete's witness, an invite's directory lookup. 36 tools carry the
flag. The staleness gate holds
that number to the shipped surface, because it had already drifted: the
docs said thirteen when the server registered twenty-five.

### One `Kind` decides three things

`register` takes a tool's `Kind` — read (or a read that writes a local
file), adding or write (each idempotent or not), destructive — and
derives from it the MCP
annotations, whether
`GCM_READ_ONLY` leaves the tool registered, and whether it needs
`GCM_ENABLE_DESTRUCTIVE` to be registered at all. Three rules enforced by
author discipline is three ways to be quietly wrong. Two more are held
at start, by a panic: a destructive tool takes `confirm`, and only a
destructive tool carries the mark.

Annotations decide what a client prompts for, so they are exact.
`readOnlyHint` goes only on a tool that changes nothing in Chat, since
VS Code ignores `destructiveHint` and prompts on every tool without
`readOnlyHint`. `destructiveHint` is false only on a write that adds
and replaces nothing, since the spec reads false as "only additive
updates". `TestOnlyToolsThatAddSayTheyAreNotDestructive` names each such
tool: the `Adding` kinds and `download_attachment`. Every other write
says true, a message edit or a removed reaction as much as a delete, so
a new tool errs toward a prompt. What sets a delete apart is the flag
that registers it and its `confirm`, not the hint. Codex prompts by
it, and claude.ai and ChatGPT document that it decides theirs. But an annotation is a request, not a control.
The spec says clients must treat annotations as untrusted, and every
widely used client has a mode or a setting that runs a listed tool with
nobody approving the call: Claude Code's auto and bypassPermissions
modes and its allow rules among them. What holds is server-side: a tool
that is not registered cannot be called, a dry run cannot write, and a
witness that does not match changes nothing.

So the destructive tools are not registered unless
`GCM_ENABLE_DESTRUCTIVE=true`. Until 6.0.0 they were registered and
refused at call time, a recorded deviation from the standard: a tool
that vanished was held to be a broken client rather than a safer one.
That left Claude Code prompting for a marked tool the server then
refused, and the standard now says to assume every registered tool can
run unattended, so the deviation is gone. `--dump-schemas` still writes
them: the schema diff holds every released tool to the build, and a
delete the flag leaves out is no less part of the contract.

The flag is off by default. It was on until the first outside person
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

### What a write may reach

The server asks the person nothing. Approving a call is the client's:
Claude Code's permission rules, and the prompt other clients draw from
the annotations — Codex by `destructiveHint`, claude.ai and ChatGPT by
what they document of it, VS Code on every tool without `readOnlyHint`.
A remembered approval covers a whole tool, never an argument value;
argument-level control exists only where an operator writes it, in a
hook or a Gemini CLI policy rule. So a risky variant carried as an
argument rides along unseen once its tool is always allowed. Each one
is refused in the ordinary tool, and where it is wanted at all it is a
tool of its own, registered only with the destructive ones.

- **Mentioning everyone is refused.** A `send_message` whose text
  mentions everyone in the space, `<users/all>`, is `[blocked]` before
  anything is sent, a dry run included: it notifies every member. An
  `update_message` that edits such a mention in is refused too, since an
  edit notifies whoever it newly mentions; an edit of a message that
  already mentioned everyone may keep it. The mention is matched
  regardless of case. Google's own Chat MCP server tells the model, in
  `send_message`'s description, that mentioning all users "is strictly
  prohibited".
- **A space is created private and stays private.** `create_space` and
  `update_space` refuse a target audience, `[blocked]`, and
  `update_space` still makes a space private. Opening a space is
  `set_space_audience`, a destructive tool. Whoever is in the audience
  can find the space, read it and join, and making it private again does
  not take back what they read.
- **`add_member` invites only people inside the organization into a
  space that admits outsiders.** Someone added can read what is already
  in the space. Whether a space admits people from outside the
  organization is Google's `externalUserAllowed`, set when the space is
  created and output only afterward (discovery revision 20261016:
  "Immutable"). Where a space says it admits them, an address the
  organization's directory does not list among its members is refused
  `[blocked]`, before anything is sent, a dry run included. A directory
  that declines to answer vouches for nobody: a personal account has
  none, so everyone counts as outside there. `add_external_member`
  invites someone from outside, and is destructive. A space that says
  nothing admits nobody from outside, since Google leaves a false
  boolean out of its JSON, and needs no lookup: Google refuses the
  invite there itself. Every Google Group is refused into a space that
  admits outsiders, whatever the directory says of it: Google invites
  the group's outside members and carries every later change to the
  group into the space (evidence log), so no check at add time holds.
  `add_external_member` adds it. In a space that admits nobody from
  outside, Google leaves the group's outsiders out, and a group add is
  ordinary.
  `create_space`, `create_group_chat` and `find_direct_message` stay
  ordinary writes: a new conversation shows nobody what was already
  there.
- **Only `promote_member` makes a manager.** A manager can change the
  space's settings and remove people, which is a new owner in the
  standard's words. `update_member_role` takes `MEMBER` alone and refuses
  the other roles `[blocked]`.
- **Deletes are registered only under `GCM_ENABLE_DESTRUCTIVE=true`, and
  each call says `confirm: true`.** That argument is a witness that the
  caller meant the call, never an approval: the model sets it. The same
  holds for every destructive tool. `delete_message` deletes one message.
  Its replies are other people's, and go only through `delete_thread`,
  which was `delete_message`'s `force` until 6.0.0. `unpin_message` and
  `remove_reaction` are ordinary writes, since one call puts a pin or a
  reaction back.
- **A destructive call repeats what it acts on.** The client's prompt
  shows the arguments, and a resource name tells the person approving
  nothing. So `delete_message` and `delete_thread` take how the message's
  text begins, at least its first 20 characters; `delete_space` and
  `set_space_audience` take the space's display name; and
  `delete_custom_emoji` takes the emoji's shortcode. The server reads the
  target first, a dry run included, and refuses `[stale]` when it
  differs. A message this account cannot read cannot be deleted here,
  because what cannot be read cannot be compared. A thread whose first
  message is already deleted is the exception: there is no text left to
  compare, and the replies under it still go. `remove_member`,
  `delete_section`, `promote_member` and `add_external_member` take
  `confirm` alone: what they act on is a membership or a section named as
  `list_members` and `list_sections` show it, or an address the call
  already carries. A group is the exception: `add_external_member` takes
  its `groups/{id}` and its address, `group_email`, reads the group from
  Cloud Identity and refuses `[stale]` when the address differs. A group
  this account cannot read cannot be added there.
- **`update_space` keeps the guidelines.** Google's mask covers all of
  `spaceDetails`, so a description sent alone clears the guidelines. The
  server reads them first and sends them back unchanged.
- **The mark goes on four tools.** `delete_space`, `admin_delete_space`,
  `set_space_audience` and `delete_custom_emoji` carry
  `anthropic/requiresUserInteraction`. Claude Code 2.1.199 or later
  prompts for them on every call in every interactive mode, with no
  "don't ask again" from 2.1.246, and denies them where no prompt can be
  drawn: dontAsk, `-p` without a permission host,
  `--permission-prompt-tool`. An Agent SDK host's `canUseTool` can
  approve them. They are the four whose prompt is itself the point: a
  space goes with every message in it, whether the caller manages it or
  deletes it as an administrator, an emoji goes from every message and
  reaction across the organization, and an audience reads what it
  reads. The mark is per tool, so on `set_space_audience` it covers an
  administrator's `audiences/{id}` as well as the organization's
  default, though such an audience may be narrower than the domain. No
  other tool carries it: a mark on `delete_message` would ask once per
  message of a bulk delete, and a question answered dozens of times in a
  row is answered without reading. No client but Claude Code reads it, so
  the flag that registers these tools is the control.
- **No tool widens the server's own reach.** No tool turns a toolset on,
  lifts a refusal or edits a setting; only the operator does, outside
  the conversation. `TestNoToolCanWriteTheServersSettings` holds that no
  package a tool reaches imports `internal/userconfig` or
  `internal/credentials` or sets an environment variable, and
  `TestNoToolChangesWhatIsRegistered` that calling every tool leaves the
  registered surface as it was.

The rule is the family's, decided by the maintainer on 2026-10-22 and
ported here on 2026-10-23; the evidence log says why the question went.
A lint rule holds it: forbidigo refuses the SDK's `Elicit` and the input
requests a result can carry.

### Administrator access

A Workspace administrator can act on any space in the organization
through Google's `useAdminAccess`. Nine Chat methods take it: spaces
get, search, patch and delete, and members list, get, create, patch and
delete. Nothing reaches messages that way, and nothing creates a space.

**Every administrator call is its own `admin_` tool, reads included.**
A remembered approval covers a whole tool, never an argument, so a flag
on `get_space` or `update_space` would carry an administrator's reach
under an approval given for the ordinary tool. Every other gate in the
family also works by leaving a tool unregistered; a flag refused at call
time would be the one exception. `search_spaces` shipped
`use_admin_access` before this rule, and 9.0.0 removed it.
`admin_search_spaces` is the same search with the administrator's reach
and filters, and the two descriptions name each other.

**`admin` is one toolset.** Toolsets group a feature area, and
`GCM_READ_ONLY` and `GCM_ENABLE_DESTRUCTIVE` cut across all of them, the
admin set included. The kinds follow the ordinary tools, with one
exception:

- `admin_search_spaces`, `admin_get_space`, `admin_list_members` and
  `admin_get_member` read, and stay under `GCM_READ_ONLY`.
- `admin_update_space` and `admin_update_member_role` are ordinary
  writes. An edit of a name, description or guidelines says what it
  replaced, so it can be put back.
- `admin_promote_member` and `admin_remove_member` are destructive, as
  their ordinary forms are. `admin_update_space_permissions` is
  destructive because widening a permission hands members what a
  manager holds, which is a new owner in the standard's words.
- `admin_add_member` is destructive though `add_member` is not. Without
  admin access a caller adds people to a space they are in, so the new
  member reads what the caller already reads. With it, the caller adds
  people to a space whose history the caller cannot see. That is
  widening access, and what was read cannot be taken back.
- `admin_delete_space` deletes a whole space and carries the mark, as
  `delete_space` does.

One combination cannot be set: an administrator's reads and the
caller's own writes without the administrator's edits. Read-only mode
removes both, and nothing else separates them. Nobody has asked for it,
and it would take a toolset split by access, which no other toolset in
the family has.

**Admin access rides on the request context.** `register` puts every
call of a tool whose name starts `admin_` on `service.AsAdministrator`,
which is
`gchat.WithAdminAccess`. The client adds `useAdminAccess=true` and the
administrator's scope to every Chat call made on it, and the two
conditions Google sets on the admin form: an administrator's search
names `customer = "customers/my_customer"`, and a membership listing
filters on `member.type != "BOT"`. A Chat call with no administrator
form is refused with `ErrNoAdminForm` before it leaves the process. People, Cloud Identity and OpenID Connect calls on
the same path go as the caller, since none of them takes the parameter.

It is a context rather than an option on each call because the guards
read too. `remove_member` checks a refused delete by reading the
membership back. Read as the caller, a membership in a space the caller
is not in answers not found, and the refusal would be reported as a
member already gone. On the context, the read-back is the
administrator's like the delete. The name is the rule, because the name
is what a client's prompt and its permission rules see; `register` puts
every such tool in the admin toolset, and refuses at start one that
names another.

**Named spaces only.** The check of a direct message or a group chat
names the people in it beside the caller, which means nothing for a
conversation the caller is not in, and Google's administrator search
finds named spaces alone. Every admin write that names a space refuses
anything else `[invalid]` before it writes. The three that take a
membership rather than a space read nothing first, and rely on Google
for that.

**`admin_add_member` adds people, never a group and never the caller.**
Adding yourself would let every tool here read the space as you, which
is the model widening its own reach (the standard's §3). A group you
belong to would add you with it, and no check at add time sees a later
change to the group. The caller's own address is compared first. Then
the directory is asked once whose the address is: an address it does
not know is refused, since Google adds nobody from outside on an
administrator's say, and one that is the caller's under another name
is refused too. A directory that cannot answer vouches for nobody, and
the add is refused.

**The scopes follow the switches.** With `admin` named, login asks for
`chat.admin.spaces.readonly` and `chat.admin.memberships.readonly`.
Unless `GCM_READ_ONLY` is set, it also asks for `chat.admin.spaces` and
`chat.admin.memberships`. With `GCM_ENABLE_DESTRUCTIVE=true` as well, it
asks for `chat.admin.delete`, the one restricted administrator scope,
which deletes any space in the organization. None of them is in
`scopes.All`, so nobody else's consent screen carries them.
`chat.delete` stays requested whatever the destructive flag says: it
reaches only the caller's own spaces, and moving it would cost every
installed token a second login.

### Idempotency is checked, not assumed

`delete_message` and `remove_member` treat a 404 as success. A 403 is
different: Google spells "already deleted, and this space keeps no
history" and "you may not delete this" the same way, so the refusal path
reads the target back and only a not-found answer counts as gone. An
earlier version assumed otherwise, and so reported a refused delete as
one that had already happened. `delete_message` also reads the message
before it deletes, to compare its witness, so a repeat that finds a
tombstone sends nothing at all.

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

Under `GCM_READ_ONLY`, login asks for the read-only scopes alone: the
OpenID Connect ones and every `.readonly` scope in the set, derived from
the names. A read-only server logged in that way holds a token that
cannot write either, whatever tools it registers. A token from an
earlier login keeps its write scopes until the next login: nothing
narrows a token already granted, so the docs say to log in again.
Turning writes back on costs a second login, which the `[scope]` error
names. Google reads a space's
notification setting only through `chat.users.spacesettings`, which
writes it too, so read-only mode leaves `get_space_notification_setting`
out. Every other read names a scope that login asks for: reactions and
reaction events name `chat.messages.readonly`, which discovery lists for
both, and `TestEveryCallNamesItsScope` holds it.

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

The bundle has the sibling servers' shape. It stages
`server/google-chat-mcp-darwin`, `server/google-chat-mcp.exe`,
`server/google-chat-mcp-linux-amd64`, `server/google-chat-mcp-linux-arm64`
and `server/launch-linux.sh`, with the LICENSE and the README at the
root, and claims Claude Desktop 0.10.0 or later. It asks for what a
person sets in another client's configuration, each under the
variable's name without `GCM_`, lowercased: `client_secret`, `profile`,
`read_only`, `enable_destructive`, `toolsets` and `local_dir`. The
bundle gate derives that list from `config.Vars`, which records every
variable Define reads and names those read outside it: the config
directory, its opt-in and the refresh token. It asks for every one, less
the removed ones and less each one it gives a reason to leave out, so a
setting added later is asked for until the manifest asks or a reason is
written. A config test holds that every `GCM_` name the code spells is
in that registry, every test file included, since tests set the
variables of the server they start. A test may also spell one of the
names the test lists for a variable the server does not read, each held
to being spelled by a test and read by nothing. A `+` chain that another
operand continues builds a name rather than spelling one, so its last
word there is a prefix. Every reader of a variable goes through
`config.Lookup`, which drops the spaces around a value, so spaces alone
are unset; a config test walks every variable in the registry, those
read outside Define through their own readers, states what each reads
from a value with spaces around it and from spaces alone, and fails on
one it has no row for. The gate
refuses a reason for a variable the server no longer reads, a variable
in `env` that the bundle leaves out or a release removed, a switch asked
as anything but a boolean, defaults that configure the server at all, naming the fields they change, a required
field's default included since Claude Desktop may fill it in, a default
that is a list or an object, any `env` on a platform override, which a
host uses in place of `mcp_config.env`, a missing `mcp_config.args` or
an argument in it, since the bundle passes settings by `env` and an
argument would configure the server past these checks, any `args` on a
platform override, an empty list included, an `entry_point` or a command
that names no staged program (`LICENSE` and `README.md` are packed to be
read, not run), an `entry_point` that is not the file the default
command runs, a command that does not start with `${__dirname}/`, a
claimed platform that spawns any file but the one staged for it, a
platform the staged table gives two entry points,
`compatibility.platforms` other than the platforms the packer stages an
entry point for, an override for a platform the bundle does not claim,
a key given twice in one object of the committed manifest, which the
gate's decode would merge while the schema and the host keep the last,
two keys in one object that differ only in case, which the decode would
match to one field, a manifest whose keys the walk cannot read, a
`user_config` default holding a `${...}` reference, which a host may
expand and the defaults check does not, a profile field that does not state `config.ProfileRule`, which the
configuration page must state too (the staleness gate holds it), a
toolsets field that does not list every toolset in order, a bundle that stops asking
for either of those two, and an empty `privacy_policies`. Written down from a
`/code-review high` rather than changed: the defaults check reads
`mcp_config.env` alone. The refusals of an argument and of an override's
`env` are what keep it sound, so relaxing either needs the defaults
check to read the same. Kept from the 7.0.1 `/simplify` and
`/code-review medium`: `configDiff`, a one-line wrapper over
`fieldDiff` that keeps the gate's call typed; and the optional OAuth
client field, which the reference host fills with its `""` default as
it does every optional field, Claude Desktop not probed.

The gate also starts every set of the switches over the defaults, each
switch in a set on whatever its default, and holds the sets the server
refuses to a declared list, both ways. Each key in a declared set is a
switch with a title, and every other field in its set names that title.
The list is empty: Read-only wins over `enable_destructive` rather than
refusing the pair, and both fields say so. The gate is the
`scripts/gates` tests, and they, the packer and `mcpb-manifest` run one
function, `manifestProblems`, so the release packs nothing the tests
refuse: the placeholder version, the schema reference, the vendored
schema, the name and platforms, and the launcher and go.mod beside the
manifest are in it. The gate, the packer and `mcpb-manifest VERSION
[ROOT]` read the committed manifest through `committedManifest`, once,
with the schema, the launcher and go.mod from the same root, and the
packer ships the bytes it checked with the version stamped in. go.mod's
module is read by one `modulePath`, which the registry entry reads too.
The version is held to Semantic Versioning, and a refusal says what is
wrong with it, since the schema takes any string. The stamped document
is not validated again: the edit puts that version where the
placeholder was, and the schema would take it. `mcpb-manifest` writes
its problems to stderr alone. A bundle whose zip or file fails to close
is not written. The manifest lists no tools, since the settings change
what registers, and carries no icon.

The family sweep of 2026-10-24 changed what this gate and the
configuration hold: every reader of a variable trims through
`config.Lookup`, the configuration directory's opt-in included; the
platforms and their entry points come from one staged table, and
`entry_point` is the default command's file; the manifest's keys are
checked before anything reads its decode; the version is held to
Semantic Versioning; a bundle that fails to close is not written; and
the name scan reads every test file. `/simplify` (one agent, four
angles), `/code-review medium` and `/security-review` ran over
`main...family-rules` the same day. Fixed from them: the name scan
skipped the tests under `scripts/`, and read a prefix a `+` chain was
building as a name; the spaces walk compared each value with the code's
own reading of another, and now states what each variable reads; the
stamped manifest's schema check could not fail, and is gone; the version
refusal said "with no spaces" whatever was wrong; the staged platforms
could list one twice; the schema check named its file in some messages
only; a key holding a dot was ambiguous in a refusal, keys differing only
in case passed, and so did a walk that failed; `mcpb-manifest` read a
path of its own, past `committedManifest`, and now takes a root;
`fieldDiff` reported a differing unnamed field only when no named one
differed, and compared a pointer by its address; a default holding
`${...}` passed; go.mod's module was read by a pattern that failed on a
trailing comment, and the registry entry read it from the build instead;
config typed the default profile userconfig named; and
`config.Lookup`'s comment said it returns the name. `/security-review`
found nothing at confidence 8 or above. Kept: the names a test spells
for a variable the server does not read are a typed list, since a
negative test's name cannot be derived, and the list is held both ways.
The registry entry reads no manifest, so the family's rule that it read
one through `committedManifest` has nothing to hold here. The
local-files refusals in `internal/service`, two tool descriptions and an
input's schema tag still spell `GCM_LOCAL_DIR` and `GCM_TOOLSETS` by
hand, which the family's spelled-once rule would replace:
`internal/service` imports no config, a struct tag cannot take a
constant, and the name scan holds every spelling to the registry, so a
misspelling fails.

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
| `spaces.findDirectMessage` with a person's address answers with the one direct message between the caller and them | The check a write to a direct message makes: the space Google finds for the address given as `space_display_name` has to be the space written to. Settled by the live run of 2026-10-27, where a write naming the wrong space was refused `[stale]` |
| A quote can be created through the API, unlike a link chip | `quotedMessageMetadata` takes the quoted message's name and its `lastUpdateTime` on create, and Google rejects a stale timestamp. No tool here sets it, so `send_message` cannot make one and the live driver cannot manufacture the case: the quotes behind this were made in the Chat client and read back by hand on 2026-09-17 |
| Chat makes no rich link for a link posted through the API | A message whose body is a message URL comes back with no annotations at all — the chip is made by the Chat client, not by the API. Live 2026-09-17, twice: once with a URL of the driver's own shape and once with the permalink byte for byte. So the live suite cannot manufacture the case it checks, and a link pasted by a person is the only thing that produces one |
| A pasted link decodes as CHAT_SPACE naming the message | Read back through the shipped binary on 2026-09-17 from a link pasted in the Chat client: `richLinkType` CHAT_SPACE, with the space, the thread and the message all named, and the annotation spanning the whole body. The message's own `text` was the anchor words and nothing else, which is the bug in one line |
| A message permalink has three path segments and a query | `room/{space}/{thread}/{message}?cls=10`, read off a real one on 2026-09-17. A message created through the API is named `{id}.{id}` and the URL carries the half before the dot, which is also its thread's id |
| A message search returns the matched message, annotations included | `view` gates the metadata around a hit — `read` and `spaceMuteSetting` — not the fields of the message itself, which is why the basic view is described as "only the matched messages … but no additional metadata". Discovery document, 2026-09-17. Not settled live, and the live run of 2026-09-17 could not settle it: the hit it searched for carried no annotations for either route to drop, because the API had made none |
| A quoted message's `sender` is a DISPLAY NAME, not a resource name | The reference calls it "the quoted message's author name", which this repository read as `users/{id}` and wrote into a comment. A real reply-quote and a real forward, both read through the shipped binary on 2026-09-17, carried a person's name. It is passed through verbatim and never looked up: `people/Jane Doe` is a malformed id, and sending one costs a junk entry in the batch that resolves the real senders. Google answers that batch per person, so the other addresses still resolved — which is how the mistake survived its first live read |
| A forward names the space it came from, and a DM by the other person | `forwardedMetadata` carried the source space and its display name on a real forward, 2026-09-17; for a direct message that name is the other participant, as the reference says. Whether a forward also carries the links and files the reference promises is still unverified: the one forwarded here had neither |
| A quote's snapshot is filled differently for a reply and a forward | A REPLY carries `sender` and `text` only; `formattedText`, `annotations` and `attachments` are documented as populated for FORWARD alone, and `forwardedMetadata` only for a forward. Reference, 2026-09-17. So an empty link list on a reply-quote is Google saying nothing, not the quoted message having no links, and the tool schemas say which is which. For a forward the snapshot is the only copy a caller can reach: the source space is usually one they are not in |
| A deleted message answers 200 with a tombstone, not 404 | Which is why a repeat delete must read the answer rather than the status. Live 2026-09-05 |
| A forced delete of a deleted thread root may still take its replies | **Not established.** Discovery (revision 20261008), `messages.delete`: "When `true`, deleting a message also deletes its threaded replies. When `false`, if a message has threaded replies, deletion fails." It says nothing of a root already deleted. Through the API that state cannot be made to probe it, since a root with replies goes only with `force`, which takes them too. So `delete_thread` takes a first message that reads as deleted, without comparing its witness, sends `force=true` and reports Google's answer, a refusal included: the replies left under it are what the call is for, and the deleted first message says nothing of them. `delete_message` always sends `force=false`. 2026-10-23 |
| A group in a space that admits outsiders brings them in, now and later | Google's help page "Add a Google Group to a space" (support.google.com/chat/answer/13535086, read 2026-10-23): "If Allow people outside your organization to join is enabled, the group and its members are invited to the space"; external members are "excluded in spaces with guest access turned off"; and "Any changes to the group's membership ... are also automatically reflected in the space". So a group the organization owns can bring in an outsider after it is added, and `add_member` refuses every group into such a space rather than checking the group once. Help-page prose, not settled live: the live run adds no group, since that would put real people in the scratch space |
| A deleted space answers 403, not 404 | So "deleted" cannot be told from "not yours", and the refusal message does not claim to know which. Live 2026-09-05, and the same under admin access on 2026-10-31: a repeated `admin_delete_space` and an administrator's read of the deleted space both got the 403 |
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
| Google pages and counts a space search only under admin access | Discovery revision 20261026, `SearchSpacesResponse`: `nextPageToken` and `totalSize` are each "Only populated when `useAdminAccess` is set to `true`". Read 2026-10-31. So `search_spaces`, which has no admin access since 9.0.0, takes no `page_token`, and its `next_page_token` and `total_size` are never set |
| Nine Chat methods take `useAdminAccess`, and no others | Discovery revision 20261026 gives the parameter to spaces get, search, patch and delete and to members list, get, create, patch and delete; the admin-overview guide (updated 2026-10-09) names the same nine. Reads take `chat.admin.spaces.readonly` or `chat.admin.memberships.readonly`, or the umbrella beside each; writes take `chat.admin.spaces` or `chat.admin.memberships`. Read 2026-10-31. `TestAdminAccessRunsTheNineAdminMethodsAndRefusesTheRest` holds the list |
| An administrator's space delete needs `chat.admin.delete` | Not `chat.admin.spaces`, which cannot delete. The spaces.delete reference (updated 2025-07-25) and discovery revision 20261026 agree. Read 2026-10-31. It is the one restricted administrator scope, so only the destructive flag asks for it |
| An administrator's patch may change a name, the details and the permission settings | The spaces.patch reference (updated 2026-09-21) refuses `space_type`, `space_history_state`, `access_settings.audience` and `access_settings.access_permission_settings` under admin access. No page says the other three work, but a live run on 2026-10-31 changed a name, a description with the guidelines kept, and six permission settings in one patch, as `permission_settings.{key}` and `permissionSettings.{key}` alike. `permissionSettings.viewSpaceMembership` must go with `access_settings.access_permission_settings`, so it is left out |
| An administrator reaches a space the caller is not in | Live, 2026-10-31. A space the run made and then left empty took an administrator's read, edit and member listing, and was gone 5 to 10 seconds after the leave: an administrator's read then got the 403, and an administrator's search no longer found it. A space with a second person in it was still there two minutes after an administrator removed the caller, and `admin_delete_space` deleted it. No page says Google deletes an empty space. So the live driver never leaves a space, and `admin_remove_member` is excused from it |
| Who may reply changes only where members cannot post | Neither the Space reference nor the Chat Help Center says when `replyMessages` can change. Live, 2026-10-31: in a space where members post, Google refused it to the space's own manager as "the permission settings are immutable", even when the patch changed nothing, and refused it under admin access as "permission denied … or the resource doesn't exist". In a space created as `ANNOUNCEMENT_SPACE` it changed both ways, as the manager and as an administrator. A patch without `managersAllowed` is refused as "Invalid combination of permissions". So the server refuses `reply_messages` where `postMessages` gives members the right, before anything is sent |
| An administrator's search must say whose spaces | The spaces.search reference: "`customer` is required when `useAdminAccess` is set to `true`", and `customers/my_customer` is its only value. This server sent `use_admin_access` without it until 2026-10-31, and the live driver had never run it. A live run on 2026-10-31 searched with the clause, every administrator filter and each order |
| An administrator's member listing needs a member type filter | The members.list reference (updated 2025-07-16) requires `member.type = "HUMAN"` or `member.type != "BOT"` under admin access, and lists no apps. The server sends `member.type != "BOT"`, the one that can keep Google Group memberships, since a filter drops a group that does not match it. Whether `showInvited` works under admin access is not documented. Read 2026-10-31. A live run that day listed members with the filter; `showInvited` under admin access is still untested |
| Google counts a space's name, description and guidelines in UTF-16 units | The Space reference (updated 2026-09-30) allows a description of 4,096 characters and guidelines of 5,000; Google's own refusal of a long name says 128. The 150 this server enforced on descriptions matched an old Ruby client library's docs. Live, 2026-10-31: 4,096 two-byte letters (8,192 bytes) were kept and 2,049 emoji (4,098 UTF-16 units) were refused, "Maximum length of the field is 4096"; guidelines and names went the same way at 5,000 and 128. So Google counts UTF-16 units, not bytes and not characters, and an emoji counts as two. The live driver writes and reads back a 4,096-unit description with emoji in it |
| Members see an administrator's change, but not who made it | The admin-overview guide (updated 2026-10-09), "What users see in Chat": "The organization's administrator performed" the action, and it "does not identify the administrator". The Reports API's Chat events record `actor_type` ADMIN for a space's details, name, deletion and memberships; no event covers a permission change. Read 2026-10-31 |
| One Admin console privilege covers an administrator's reads and writes | Admin Help a/answer/13369245: "Super admins have the manage chat and spaces privilege by default". The privilege list, a/answer/1219251, has no read-only form of it. So reads and writes part at the OAuth scope, not at the privilege. Read 2026-10-31 |
| A desktop client's login can grant some scopes and not others | Google's native-app guide (updated 2026-10-30), step 6: "users may not grant your app access to all of them". The exceptions are "apps marked as Trusted" and Workspace Enterprise apps with domain-wide delegation, which "either receive all requested scopes or none". The granular-permissions page (updated 2026-10-30) ties `enable_granular_consent` to client ids "created before 2019", so login does not send it. Read 2026-10-31. This reverses a reading of the granular-permissions page earlier that day, which took it to say a desktop client gets the choice only with that parameter |
| An Internal app needs no scope list on its consent screen | Google's configure-oauth-consent guide (updated 2026-09-03): "you only need to list scopes for apps used by people outside your Google Workspace organization", and for internal apps "scopes aren't listed on the consent screen". The setup guide said a scope off the screen is never granted; it now asks for the list on External apps only. Read 2026-10-31. Live the same day: an Internal app's token held 18 scopes its Data Access page does not list. Several have no broader scope listed there that could account for them, among them `chat.spaces`, `chat.delete`, `contacts.readonly` and the `chat.users` scopes |
| Google writes a time it does not have as the Unix epoch | Live, 2026-10-31: an administrator's spaces.get answered `lastActiveTime` `1970-01-01T00:00:00Z` for a space whose search row gave a time in October 2026. The reference does not mention it. Every timestamp the server reads now takes the epoch as absent. `TestGetSpaceReadsTheEpochAsNoTime` holds it |
| `destructiveHint` false means the tool only adds | The MCP schema (2025-11-25), `ToolAnnotations`: "If false, the tool performs only additive updates", and the default is true. So a tool that replaces or removes anything says true, a message edit and a removed reaction included, though it needs no flag here. Read 2026-10-31. The `Overwrite` kinds carried it then, on fifteen tools. Since 2026-11-01 they are the `Write` kinds and true is the default for a write; `TestOnlyToolsThatAddSayTheyAreNotDestructive` names each tool that says false |
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
| `anthropic/requiresUserInteraction` is a vendor key, and Claude Code holds it in every interactive mode | It appears nowhere in the MCP spec, which puts human-in-the-loop on the application: "the protocol itself does not mandate any specific user interaction model". Claude Code 2.1.199 or later prompts for a marked tool on every call in every interactive mode (default, acceptEdits, plan, auto, bypassPermissions), allow rules and hooks do not skip it, and from 2.1.246 it offers no "don't ask again". Where no prompt can be drawn it denies the call: dontAsk, `-p` without a permission host, `--permission-prompt-tool`. An Agent SDK host's `canUseTool` can approve it. Anthropic documents it for "tools whose permission prompt is itself the point". No other client reads it. Corrected 2026-10-23 from the family's approval evidence; this row said auto mode prompted for nothing, which was true before 2.1.199 |
| VS Code prompts by `readOnlyHint`, not by `destructiveHint` | VS Code never reads `destructiveHint`: it prompts on every tool without `readOnlyHint: true`. Codex prompts by `destructiveHint` in its default tool mode, at source; claude.ai and ChatGPT document that it decides theirs. Family approval evidence, 2026-10-23. So `readOnlyHint` goes only on a tool that changes nothing, and `download_attachment`, which writes a local file, does not carry it |
| A remembered approval covers a tool, never an argument | claude.ai, Codex, VS Code and Copilot remember "always allow" per tool or per server, and Claude Code's permission rules match an MCP argument only in a `--disallowedTools` deny rule. Hooks in Claude Code, Codex, VS Code and Copilot CLI see arguments and can deny or force a prompt, and Gemini CLI policy rules match them, but only where an operator writes one. Family approval evidence, 2026-10-23. So `force`, an outside invite and a manager role are tools of their own here |
| A client can run a registered tool with nobody approving it | In every widely used client examined but ChatGPT a mode or a setting does: Claude Code's bypassPermissions, allow rules and auto mode, claude.ai's Always allow, Codex `--yolo`, VS Code's Allow all, Copilot CLI `--yolo`, Gemini CLI YOLO, Cursor's Run Everything. Only a tool carrying the mark is held in Claude Code. Family approval evidence, 2026-10-23. So the destructive tools are not registered unless the operator turns them on |
| Strict inputs: a struct's schema refuses unknown properties | Which is right for a tool and wrong for a permission hook — a hook that refuses a payload it does not model fails in a place that implicates a different tool entirely |
| A server can ask the person to confirm a write through form elicitation, on every protocol | The `ElicitRequestFormParams` type in the specification's `schema.ts` for 2025-06-18, 2025-11-25 and 2026-07-28, the 2026-07-28 multi-round-trip pattern, and the MCP Go SDK v1.8.0's `mcp/server.go`, read 2026-09-28. A result may carry `inputRequests` with a signed `requestState`; before 2026-07-28 the SDK sends `elicitation/create` itself and calls the handler again. `requestedSchema` may have no properties. The client answers "from the user or other sources", so an accept is never proof a person read anything. Held by tests on all three protocols |
| A client draws an elicitation question as plain text | Refuted. VS Code builds it as `new MarkdownString(elicitation.message)`, untrusted, so emphasis, link text and HTML-like text draw and single line breaks join (`mcpElicitationService.ts`, `main` at 251bcf5f, 2026-09-29). Quoted text is a code span, and lines stand apart |
| A required choice confirms better than an empty form | Declined for now. Codex accepts a form with no properties by itself under approval policy `never` with full access, and VS Code resolves a skipped question as `accept` (`codex-rs/codex-mcp/src/elicitation.rs` at c248f6d4; `mcpElicitationService.ts`). But in Claude Code 2.1.284 a choice list took the maintainer 60 seconds against 8 for the empty form, and read as confusing |
| A tool the server confirms itself should carry the mark too | Refuted 2026-10-09, after the owner was asked twice for one delete in google-docs-mcp: the client prompted for the mark, then the server asked. No source recommends two hard gates for one call; GitHub's `delete_repository` and Supabase confirm with `destructiveHint` plus a form elicitation, and Claude Code's documentation scopes the mark to "tools whose permission prompt is itself the point". `tools/list` drops the mark, for a client that can ask, from the four tools that ask before every write (`AsksEveryCall`), and from `send_message` and `update_message` when `GCM_ASK_BEFORE_SEND` makes them ask every time. A send that asks only before `@all`, and a write that never asks, keep it, since there it is the only per-call prompt. The cost: outside auto mode, the mark stopped these four calls before they reached the server, with a prompt or, headless, a refusal. Now a Claude Code `Elicitation` hook that accepts confirms them alone, since such a hook can "respond programmatically, skipping the dialog entirely" (code.claude.com/docs/en/hooks, read 2026-10-09). Superseded by the row below; its "outside auto mode" is refuted for Claude Code 2.1.199 or later, which prompts for a marked tool in auto mode too |
| The server should confirm a write with the person itself, through form elicitation | **Refuted 2026-10-22**, on documents and source, and the wording corrected 2026-10-23 against the family's approval evidence. The specification gives per-call approval to the client and never makes elicitation a consent control: the client may gather the answer "from the user or other sources". The servers that do confirm each pick their own class, from GitHub's one tool to Azure's 145 or so commands to every write in AWS Redshift's write mode, and GitHub, AWS's SAP server and the archived Dynatrace server have no off switch. The question is unreliable: a Claude Code `Elicitation` hook, once someone installs one, answers it with no dialog, and Codex in full access accepts a form with no fields by itself. By open user reports Anthropic has not answered, Claude Desktop's Code tab and the VS Code extension decline it without drawing it, Cowork leaves it undrawn until the call times out, and the Claude Code terminal shows the first three lines of its message (one report, against 2.1.301). No client's permission rules decide a question one by one: Claude Code reaches it only through hooks and `--permission-prompts none`, Codex governs elicitations as a class through its approval policy, and VS Code not at all. In the maintainers' own run of 2026-10-15, a question asked on every call expired while a chat server deleted 68 messages. So the server asks nobody now; the four rows above describe the question this replaces |
| `tools/list` and `tools/call` see the same client capabilities | Not on 2026-07-28; found by review 2026-10-09 and kept as a known limit. From that protocol a client sends its capabilities with each request, and the Go SDK v1.8.0 reads the request's `_meta` before the session's. So a client can declare form elicitation to `tools/list` and none to `tools/call`, and get neither the mark nor a question. That gives a misbehaving client nothing it lacked: one that declares form elicitation can answer the question itself and accept without a person. Moot since 2026-10-22: the server asks nobody |
| Stdout carries JSON-RPC frames only | Spec, transports/stdio. One stray `fmt.Println` corrupts the protocol, which is what the smoke test guards |

### Releasing

| What | Verdict |
|---|---|
| cosign 3 made `--bundle` required | The old `--output-signature` and `--output-certificate` silently produce nothing. Release notes, v3.0.1, after a sibling repository's release died on it |
| Pinning an action does not pin what the action installs | So `cosign-release` and `syft-version` are pinned beside the SHAs. A half-pinned step reads exactly like a pinned one |
| goreleaser's `mcp` block cannot publish an MCPB package | The registry requires `fileSha256` for that type and the block has exactly `registry_type`, `identifier` and `transport`. Read out of its config at v2.18.0 and again at v2.18.1 |
| The registry enforces its MCPB rules in code, not in the published schema | A hash, a github.com or gitlab.com release-asset URL carrying "mcp", no `registryBaseUrl`, and a HEAD on the URL before it accepts the entry — so the entry can only be published after the release exists |
| A bundle install has a second approval behind the server, as Claude Code's ask rules are | **Refuted**, read 2026-10-23; Claude Desktop itself not probed. anthropics/mcpb `70fe3b34` and v2.1.2 declare the manifest's `tools` list for display, and nothing reads it at run time. Claude's connector docs: Desktop approves per tool, driven by the annotations, and "Always allow" skips the prompt "for that tool from now on". So every switch the bundle asks for defaults to the server's own, off, the bundle gate refuses one that does not, and the manifest lists no tools |
| `privacy_policies` is optional for a bundle | **Refuted**. The manifest specification (anthropics/mcpb `70fe3b34`, MANIFEST.md) makes it "Required when the extension connects to external services (first- or third-party) that process user data". Neither the schema nor `mcpb validate` enforces it, so the bundle gate does. The manifest names Google's, https://policies.google.com/privacy, which answered 200 on 2026-10-24 |
| Any `command` the specification shows runs from the bundle | **Refined**, read 2026-10-24; Claude Desktop not probed. anthropics/mcpb v2.1.2: MANIFEST.md L497 and L510-515 give a binary the command `server/my-server`, and L532 says `${__dirname}` "is replaced with the absolute path to the extension's directory"; `src/shared/config.ts` L118, L133 and L173-176 substitute variables in `command` and resolve no path. The specification shows a relative command once and never says what it is resolved against, and the reference host passes it to the spawn as written. `${__dirname}` is the one form whose meaning the specification fixes. The bundle gate requires every command to start with `${__dirname}/` and to name a staged program, as the committed manifest's do, and refuses a relative or absolute one; a probe of Claude Desktop's working directory would settle the relative form |
| A platform override's `env` is merged into `mcp_config.env` | **Refuted**, read 2026-10-24: the reference host, anthropics/mcpb `src/shared/config.ts` at v2.1.2, does `result.env = platformConfig.env \|\| result.env`. An override's `env`, an empty `{}` included, replaces the base, so the settings would not reach that platform. The bundle gate refuses any `env` on an override |
| A platform override's `args` are added to `mcp_config.args` | **Refuted**, read 2026-10-24: the reference host, anthropics/mcpb `src/shared/config.ts` at v2.1.2, does `result.args = platformConfig.args \|\| result.args` and substitutes `${user_config.x}` into `args` as into `env`. An argument such as `--enable-destructive=true` reaches the server, and an override's `args`, an empty `[]` included, replaces the base. The bundle gate refuses an argument in `mcp_config.args` and any `args` on an override. The base keeps its `"args": []`, and the gate refuses it missing: only the reference host was read, and Claude Desktop without the key is not probed |
| A required setting's default reaches the server when the person gives none | **Refuted** for the reference host, read 2026-10-24: anthropics/mcpb `src/shared/config.ts` at v2.1.2. `getMcpConfigForManifest` sets each key's `default` first ("First, add defaults from manifest") and then `Object.assign(mergedConfig, userConfig)`, so the person's value wins. `hasRequiredConfigMissing` reads a required key from `const config = userConfig \|\| {}`, the person's values alone, and one missing skips the launch. So a required key's default reaches the server only as a value the person gave, which Claude Desktop's install form may fill in from the default; Desktop is not probed. The bundle gate substitutes a required key's default too, holding the bundle to a person who accepts it unread |
| A bundle setting the person leaves empty reaches the server empty | **Refuted** for an optional key with no default: the reference host, anthropics/mcpb `src/shared/config.ts` at v2.1.2, passes `${user_config.x}` through as written. A `""` default reaches the server as `""`, and a boolean as `"true"` or `"false"`. So every optional key has a default, and the bundle gate builds the configuration from the bundle's defaults and holds it to an unconfigured server's |
| A `user_config` default reaches the server as written | **Refuted** for the specification, read 2026-10-24: anthropics/mcpb v2.1.2 MANIFEST.md L572 says a default "supports variable substitution", and L586-590 list `${HOME}`, `${DESKTOP}` and `${DOCUMENTS}` for one. The reference host, `src/shared/config.ts` at v2.1.2, builds its variables with the system directories first (L132-137) and each key's value, its default included, after them (L142-171), and replaces them in that order (L26), so a reference inside a substituted default is expanded only when its variable comes later. Claude Desktop is not probed. The defaults check expands none, so the bundle gate refuses a default holding `${` |
| `defaults.run` covers every `run` step in a workflow | Most specific wins, and an explicit `shell: bash` also turns on pipefail. Without it a step on the Windows runner gets PowerShell, and the mangling shows up on the line that runs |
| A commit footer's token is matched exactly, so `SCHEMA-CHANGE:` must be written in capitals | **Refuted**, read 2026-11-01: Conventional Commits 1.0.0, item 15, "The units of information that make up Conventional Commits MUST NOT be treated as case-sensitive by implementors, with the exception of BREAKING CHANGE which MUST be uppercase"; item 16, "BREAKING-CHANGE MUST be synonymous with BREAKING CHANGE". So `schema-ack` reads `SCHEMA-CHANGE:` in any case, and `BREAKING CHANGE:` and `BREAKING-CHANGE:` only in capitals |

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
