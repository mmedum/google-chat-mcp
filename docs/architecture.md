# Architecture

How `google-chat-mcp` is put together, and the decisions a contributor
should not undo without a better reason than the one that put them
here. Part 1 is the overview. Part 2, from [Decisions](#decisions), is
one entry per decision, cited elsewhere by its D-ID. What was verified
about Google's APIs and the clients lives in the comment at the code
that depends on it, with the source linked.

## Context

`google-chat-mcp` is a Model Context Protocol server for Google Chat.
One Go binary runs as a subprocess of an MCP client and speaks MCP over
stdio. It holds one person's OAuth credentials and calls Google's REST
APIs directly. Nothing is hosted: there is no shared deployment, no
service account and no domain-wide delegation (D-01).

The parties around the server:

- **The person** signs in once with `google-chat-mcp login`. Every call
  acts as them, with their permissions in Chat.
- **The operator** decides what the server may do, through `GCM_*`
  environment variables the client passes at start: the toolsets,
  read-only mode, the destructive switch and the local directory. The
  operator is usually the person, setting up their own client.
- **The client** draws the approval prompt from the tool annotations and
  its own rules. The server asks the person nothing itself (D-15).
- **The model** chooses the calls and their arguments. It can be wrong
  or steered by what it reads, so nothing it sends widens what the
  operator allowed (D-26).

The server talks to Google through:

- the Chat API, for spaces, messages, members, reactions, sections,
  events, read state, custom emoji and availability;
- the People API, for the names and addresses Chat did not send (D-35);
- the Cloud Identity Groups API, for Google Groups in a space (D-21);
- Google's OAuth and OpenID Connect endpoints, for login, refresh and
  `whoami`.

It is not a Chat app. It acts as a person and never as a bot, so a
method only an app can call is out of reach: Google grants `chat.bot`
to service accounts alone. It is not a hosted connector either, so
Claude on the web and on mobile cannot reach it (D-01).

## Quality goals

In priority order:

1. **Reach stays where the operator set it.** A tool the settings leave
   out is not registered, so no client mode and no model can call it.
   A risky variant is a tool of its own, and no tool edits the settings
   (D-12, D-16, D-26).
2. **The stream and the logs stay clean.** Stdout carries only protocol
   frames, the logs carry no conversation, and the repository carries no
   real account (D-02, D-03, D-04).
3. **An upstream change degrades rather than breaks.** An unknown field
   is logged, a failed name lookup costs one field, and a repeat delete
   reads what is there (D-08, D-35, D-34).
4. **The tool surface is a contract.** Names, output fields and resource
   URIs a release shipped keep working for the people who built on them
   (D-07, D-13).
5. **Beliefs about the API are checked against the API.** Every fake in
   the unit suite is written from what we believe Google does. The live
   run is where the belief meets Google, and the comment at the code
   that depends on a belief links its source and names the live step
   that checks it (D-39, D-49).

## Codemap

- `cmd/google-chat-mcp` is the program. With no subcommand it serves;
  `login`, `logout`, `status`, `doctor` and `--dump-schemas` are the
  rest. It names stdout and stderr once and passes them down (D-02).
- `internal/auth` runs Google's desktop OAuth flow, a loopback redirect
  with PKCE, and the token source that refreshes access tokens.
- `internal/config` reads the `GCM_*` environment and the bound flags,
  validates them once at start, and keeps `config.Vars`, the registry
  of every variable the server reads (D-44).
- `internal/credentials` stores and finds the refresh token: the
  environment first, then the OS keyring, then a `0600` file.
- `internal/directory` holds the email cache and the People resolver
  that fills it (D-35).
- `internal/doctor` is the live check behind `google-chat-mcp doctor`,
  which reports the drift it finds in the caller's own spaces (D-08).
- `internal/evals` scores a model driving the tools, behind the `evals`
  build tag, with a permission-prompt helper for the headless client
  (D-48).
- `internal/gchat` is the REST client and wire types for Chat, People,
  Cloud Identity and OpenID Connect, with the rate limiters, the retry
  rules and the drift-reporting decoder (D-05, D-38).
- `internal/leakcheck` fails the build on anything that identifies a
  real person, account or space (D-04).
- `internal/livecheck` is the live driver behind the `live` build tag,
  whose surface gate runs in `make check` (D-49).
- `internal/redact` masks addresses in text this server did not write,
  before it leaves the process.
- `internal/scopes` holds the scope set, the umbrella table and the
  scope each call needs (D-37).
- `internal/server` wires the MCP SDK, holds the server instructions and
  dumps the schemas.
- `internal/service` holds the rules worth testing, and is the only
  place a Google failure becomes a `[class]` tool error.
- `internal/tools` defines the MCP tools, their schemas and their
  renderings, each added through `register` (D-10).
- `internal/userconfig` stores non-secret per-profile state: the client
  secret's path, the account and the granted scopes.
- `internal/version` holds the version the build stamps in.

Beside the packages, `scripts/gates` is every check the repository runs
on itself, written in Go. `testdata` holds the released surface and the
API snapshots, and `packaging/mcpb` the Claude Desktop bundle's manifest
and Linux launcher.

## Request flow

A tool call crosses the layers in one direction:

```
MCP client
   │  JSON-RPC over stdio, newline-delimited; stdout carries nothing else
   ▼
internal/server        SDK session, tool and resource registration
   │
   ▼
internal/tools         decode the input, call the service, shape both halves of the reply
   │                   register() applies the annotations, the gating, confirm,
   │                   the dry-run context and the administrator context
   ▼
internal/service       the rules worth testing, and the only place that
   │                   turns a Google failure into a [class] tool error
   ├──► internal/directory   resolve the user ids Chat sent no address for, cached
   ▼
internal/gchat         one request: rate limiter, retry, drift-reporting decode
   │
   ▼
internal/auth          refresh token → access token, cached until it expires
   │
   ▼
Google Chat, People, Cloud Identity and OIDC
```

### Start

`main` loads the configuration and refuses to start on a bad setting.
`internal/server` then registers every tool through `register`, which
leaves out what the settings exclude: the write tools under
`GCM_READ_ONLY`, the destructive ones without `GCM_ENABLE_DESTRUCTIVE`,
and any toolset not named (D-10, D-27). A tool spec that breaks a rule,
such as a destructive tool without `confirm`, panics here, so a mistake
fails at start in every configuration. The registered surface does not
change for the life of the process (D-26).

### A read

A client calls `get_messages`. `internal/tools` decodes the input
against a schema that refuses unknown properties, and calls the
service. The service asks `internal/gchat` for a page, which waits on
the read limiter, sends one request, and decodes the answer without
rejecting unknown fields (D-08). Senders Chat named arrive with their
addresses; the rest are resolved through the cache, then People, and a
failed lookup leaves the address null (D-35). The tool answers with the
structured result and a readable rendering (D-13).

### A write

A client calls `delete_message` with `space_display_name`, `text_start`
and `confirm: true`. `register` refuses the call without `confirm`. The
service reads the space and checks its name (D-17), then reads the
message and compares its text (D-18). A mismatch is `[stale]`, and
nothing is sent. With `dry_run`, the call runs on a context that
`internal/gchat` refuses to write under, so the reads happen and the
delete does not (D-09). Otherwise the delete goes out on the write
limiter. A refused delete is read back before it is reported, since
Google answers "gone" and "not yours" alike (D-34). A write that may
have landed before a server failure says so rather than inviting a
retry (D-38).

### Login and refresh

`google-chat-mcp login` sends the person to Google's consent page and
listens on a loopback port for the redirect. It stores the refresh token in the OS
keyring, or in a `0600` file when no keyring answers. The profile in
`internal/userconfig` records the scopes Google granted, which can be
fewer than were asked for (D-37). At serve time the token source trades
the refresh token for an access token and caches it until it expires.
When Google refuses the refresh token it answers `invalid_grant`, and
only signing in again fixes that.

## Invariants

- Stdout carries only MCP JSON-RPC frames (D-02).
- No log line carries message text, an address, a display name, a token
  or a search term (D-03).
- Nothing in the repository identifies a real person, account or space
  (D-04).
- A released tool name, output field or resource URI changes only in an
  acknowledged break (D-07).
- An unknown field in a Google response is logged, never refused (D-08).
- A dry run sends nothing that changes Chat (D-09).
- Every tool is added through `register`, and its kind decides its
  annotations and whether it is registered (D-10).
- A destructive tool is registered only under
  `GCM_ENABLE_DESTRUCTIVE=true` (D-12).
- Every reply carries `structuredContent` and a text rendering (D-13).
- `send_message` adds nothing to the text it posts (D-14).
- The server never asks the person a question (D-15).
- A write to a space is refused unless `space_display_name` names that
  space (D-17).
- A destructive call is refused unless it says `confirm: true` (D-18).
- No tool changes the server's settings or what it registers (D-26).
- Every administrator call goes through an `admin_` tool, and only those
  tools act as an administrator (D-28).
- A People failure never empties a listing or drops a row (D-35).
- A write with no idempotency key is retried only after a 429 (D-38).
- Every API method has a verdict in `testdata/api-coverage.tsv` (D-39).

## Cross-cutting concerns

**Configuration.** Every setting is a `GCM_*` variable with a flag bound
to the same name. A flag overrides the environment, and the environment
overrides the default. Validation runs once at start, so a bad setting
fails before any tool is offered. A removed setting is refused with the
name of its replacement. Every reader goes through `config.Lookup`,
which drops the spaces around a value (D-44). The full list is in
[configuration.md](configuration.md).

**Errors.** Every tool error starts with a class in brackets:
`[auth]`, `[scope]`, `[invalid]`, `[quota]`, `[forbidden]`, `[server]`,
`[upstream]`, `[unsupported]`, `[blocked]`, `[stale]` or `[unexpected]`.
The class tells the model what to do next, which Google's own message
often does not. `[scope]` names the missing scope and the command that
grants it (D-37). `[blocked]` is a refusal by rule, and `[stale]` a
witness that did not match (D-18). `make classes` holds the vocabulary
closed from both sides.

**Logging.** `slog` to stderr, text or JSON. A line names the method,
the tool, the resource id, the outcome and the duration, and nothing a
person wrote (D-03). Addresses in text the server did not write, such as
Google's error messages, are masked by `internal/redact` before they
reach a log or a reply.

**Limits and retries.** Reads and writes have their own limiters, set
inside Google's per-user quota. A read retries any transient failure
with backoff. A write retries only where repeating it is safe (D-38).
The caller's context bounds the whole call, and a long transfer that
stalls is cut.

**Schema drift.** Google adds fields without notice. The decoder counts
and logs them, `doctor` reports them on demand, and `make api-fields`
holds every published field to a verdict (D-08, D-39).

**Scopes.** Login asks for the scopes the settings need and no more:
read-only scopes under read-only mode, and the administrator scopes only
with the admin toolset (D-36, D-33). Each call names its scope, so a
missing one becomes a `[scope]` error with the fix (D-37).

**Testing.** The unit suite runs against fakes, which encode what we
believe Google does. `make live` drives the shipped binary against a
real account, and is the only check that can show a belief wrong
(D-49). `make live-surface` runs in `make check` and fails on a tool no
live step exercises. A change to the write path or a response shape
gets a live run before it counts.

**Documentation.** A decision lives once, in part 2 here. What was
verified about Google lives in the comment at the line that depends on
it: the behavior, the source link, where the source is wrong or silent,
and the live step that checks it. A refuted belief goes at that line or
in a decision's `Rejected:` line. The docs gate holds every D-ID
citation to an entry that exists, and every "Held by" to a gate or test
that exists. The staleness gate holds the README,
[configuration.md](configuration.md) and the setup guide to the code.

## Decisions

| ID | Decision | Status | Held by |
| --- | --- | --- | --- |
| D-01 | One stdio binary per person, with nothing to host | accepted | smoke, review |
| D-02 | Stdout carries only MCP JSON-RPC frames | accepted | forbidigo, smoke |
| D-03 | Logs record the call, never the payload | accepted | TestLogsNeverCarryThePayload |
| D-04 | Nothing in the repository identifies a real account | accepted | leaks, TestTheRepositoryIsClean |
| D-05 | Own wire types over raw REST | accepted | api-fields, review |
| D-06 | Output types shadow the wire and never embed it | accepted | schema-diff, TestEveryToolGetsAnAnnotatedOutputSchema |
| D-07 | The released tool surface is a contract | accepted | schema-diff, schema-ack, TestTheDumpIgnoresServerSettings |
| D-08 | Drift is observable, never fatal | accepted | TestDriftDoesNotFailTheCall, TestDriftIsCountedEveryTimeAndLoggedOnce, TestDecodeReportsUnknownFields |
| D-09 | A dry run cannot write | accepted | TestADryRunCannotWriteWhateverTheMethod, TestEveryDryRunWritesNothing |
| D-10 | Every tool goes through `register`, which reads one `Kind` | accepted | TestAnnotationsAreConsistent, TestRegistrationRefusesAnUnguardedDestructiveTool, TestReadOnlyLeavesOutEveryWrite |
| D-11 | Annotations say exactly what a tool changes | accepted | TestOnlyToolsThatAddSayTheyAreNotDestructive, TestReadOnlyToolsAreAnnotated |
| D-12 | Destructive tools exist only when the operator turns them on | accepted | TestADestructiveToolIsRegisteredOnlyWhenAllowed, TestTheDestructiveSwitch |
| D-13 | A reply carries both halves, in different forms | accepted | TestEveryToolReturnsBothHalves, TestRenderings |
| D-14 | `send_message` posts the body verbatim, once | accepted | TestSendMessagePostsTheTextVerbatim, TestAMentionIsPostedVerbatim, TestSendMessageUsesTheCallersIDWhenGiven |
| D-15 | The server asks the person nothing | accepted | forbidigo |
| D-16 | A risky variant is a tool of its own | accepted | TestOnlyPromoteMemberMakesAManager, TestAGroupWhereOutsidersAreAdmittedIsAddedOnlyAsExternal, TestCreateSpaceRefusesAnAudience |
| D-17 | A write to a space names the space it writes to | accepted | TestCheckSpaceHoldsTheNameToTheSpace, TestCheckSpaceRefusesWhatItCannotRead, TestEveryToolThatNamesASpaceIsFilledIn |
| D-18 | A destructive call takes `confirm` and repeats what it acts on | accepted | TestADeleteRunsOnlyWithConfirm, TestDeleteMessageHoldsItsTextToTheWitness, TestDeleteSpaceHoldsItsNameToTheWitness, TestDeleteCustomEmojiHoldsItsShortcodeToTheWitness, TestAddExternalMemberHoldsAGroupToItsAddress |
| D-19 | Mentioning everyone is refused | accepted | TestAMentionOfEveryoneIsRefused, TestMentionsEveryone |
| D-20 | A space is created private and opened only by `set_space_audience` | accepted | TestCreateSpaceRefusesAnAudience, TestUpdateSpaceMakesASpacePrivateAndNeverOpensOne, TestUpdateSpaceRefusesAnAudienceBesideOtherFields, TestSetSpaceAudienceOpensASpace |
| D-21 | `add_member` invites only the organization into a space that admits outsiders | accepted | TestAddMemberRefusesSomeoneFromOutside, TestAddMemberRefusesAGroupWhereOutsidersAreAdmitted, TestAGroupWhereOutsidersAreAdmittedIsAddedOnlyAsExternal, TestAddMemberDryRunInvitesNobody |
| D-22 | Only `promote_member` makes a manager | accepted | TestOnlyPromoteMemberMakesAManager, TestUpdateMemberRoleChecksTheRole |
| D-23 | `delete_message` deletes one message, and `delete_thread` its replies | accepted | TestDeleteThreadTakesTheReplies, TestDeleteThreadReportsARefusal |
| D-24 | `update_space` keeps the guidelines | accepted | TestUpdateSpaceKeepsTheGuidelines |
| D-25 | The `requiresUserInteraction` mark goes only where the prompt is the point | accepted | TestOnlyTheMarkedSetAsksForAPerson, TestRegistrationRefusesAnUnguardedDestructiveTool |
| D-26 | No tool widens the server's own reach | accepted | TestNoToolCanWriteTheServersSettings, TestNoToolChangesWhatIsRegistered |
| D-27 | Toolsets are feature areas, never access levels | accepted | TestToolsetsNarrowTheSurface, TestAdminToolsNeedTheAdminToolset |
| D-28 | Every administrator call is its own `admin_` tool | accepted | TestAdminToolsNeedTheAdminToolset, TestOrdinaryCallsNeverAskForAdminAccess, TestAdminRefusalsNameTheAdminTool |
| D-29 | Administrator tools take their ordinary forms' kinds, except an add | accepted | schema-diff, TestADestructiveToolIsRegisteredOnlyWhenAllowed |
| D-30 | Administrator access rides on the request context | accepted | TestAsAdministratorMarksTheContext, TestAnAdminToolsCallsRunAsTheAdministrator, TestAdminRemoveReadsBackAsTheAdministrator, TestAdminAccessRunsTheNineAdminMethodsAndRefusesTheRest |
| D-31 | Administrator writes act on named spaces only | accepted | TestAdminWritesActOnNamedSpacesOnly, TestAdminWritesRefuseANamedGroupChat |
| D-32 | `admin_add_member` adds people, never a group and never the caller | accepted | TestAdminAddMemberNeverAddsTheCaller |
| D-33 | The administrator scopes follow the switches | accepted | TestLoginAsksForTheAdminDeleteOnlyWithDeletesOn, TestLoginAsksForWhatTheSettingsAllow, TestAdminUmbrellasCoverTheirReads |
| D-34 | Idempotency is checked, not assumed | accepted | TestDeleteMessageIsIdempotent, TestRemoveMemberIsIdempotent, TestIsAlreadyGoneNeverSwallowsAMissingScope, live |
| D-35 | A People failure costs one field, never a row | accepted | TestPeopleFailuresNeverEmptyAListing, TestAPeopleFailureShowsAsANullEmail, TestAnAddressChatNamedIsRememberedForAReaction, TestASearchNamesASenderTheServerHasSeen |
| D-36 | Read-only mode holds a token that cannot write | accepted | TestReadOnlyAsksForNothingThatWrites, TestReadOnlyLeavesOutAReadThroughAWriteScope, TestReadOnlyRegistersExactlyTheReadTools |
| D-37 | Every call names its scope, and an umbrella satisfies its parts | accepted | TestEveryCallNamesItsScope, TestAMissingScopeErrorNamesTheScope, TestImpliesOnlyNamesRequestedScopes, TestGrantedScopesReadsWhatGoogleReturned |
| D-38 | A write is retried only where repeating it is safe | accepted | TestANonIdempotentWriteIsNotRetriedOnAnAmbiguous5xx, TestAnIdempotentWriteStillRetriesOnA500, TestRetriesOn429, TestTheReadLimiterIsApplied |
| D-39 | Every API method and field has a written verdict | accepted | api-coverage, api-fields, TestCheckCallsHoldsTheRecordAndTheClientToEachOther |
| D-40 | Releases are `go install` and signed archives | accepted | pins, release-rehearse |
| D-41 | The Claude Desktop bundle ships inside the signature | accepted | mcpb-pack, TestPackWritesWhatTheManifestNames, TestABundleThatDoesNotCloseIsNotWritten |
| D-42 | The registry entry is published after the release exists | accepted | server-json, TestServerJSONNamesTheModuleGoModDeclares |
| D-43 | The bundle runs on every platform Claude Desktop does | accepted | mcpb-manifest, TestTheLauncherRunsWhatThePackerStages, TestThePlatformsAreTheStagedOnes |
| D-44 | The bundle asks for every setting the server reads | accepted | mcpb-manifest, TestBundleVarsAreDerivedFromTheSettings, TestEveryVariableTheCodeNamesIsInEnvVars, TestASettingOfSpacesIsUnset |
| D-45 | The bundle's defaults configure nothing | accepted | mcpb-manifest, TestTheRefusalsTurnASwitchOnWhateverItsDefault, TestADefaultHoldingAReferenceIsRefused |
| D-46 | The bundle passes settings by `env` alone | accepted | mcpb-manifest, TestManifestProblemsRunsEveryCheck, TestAManifestKeyGivenTwiceIsRefused |
| D-47 | Windows programs carry a version resource that git ignores | accepted | winres, TestWinresWritesWhereGoreleaserBuilds, TestReleaseHooksDoNotWriteIntoTheCheckout |
| D-48 | Agent evals judge the space, never the model's account of it | accepted | evals |
| D-49 | A live run writes down only what it created | accepted | transcript, live |
| D-50 | Forward a message through `send_message` | rejected | — |

### D-01 One stdio binary per person, with nothing to host

The server is one Go binary that runs as a subprocess of the MCP
client. It holds one person's credentials and calls Google's REST APIs
directly. There is no shared deployment, no service account and no
domain-wide delegation.

The reason is the authorization server an HTTPS transport would need.
Google supports neither dynamic client registration nor client ID
metadata documents, so a remote MCP server cannot hand a client
Google's own login. The Go SDK's `auth` package is a resource server
only: it verifies bearer tokens and serves protected-resource metadata,
and issues nothing. Bridging that gap means running and securing an
OAuth server of our own, which is a different project.

Per-user OAuth keeps every call inside the signed-in person's own
permissions in Chat. Nothing here can act for anyone else.

Rejected: an HTTPS transport with a hosted authorization server, for
the reason above.
Cost: Claude on the web and on mobile cannot reach a stdio server.

### D-02 Stdout carries only MCP JSON-RPC frames

MCP's stdio transport says the server "MUST NOT write anything to its
`stdout` that is not a valid MCP message", and "MAY write UTF-8 strings
to its standard error (`stderr`) for logging purposes"
([transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)).
A stray print corrupts the JSON-RPC stream before the client's first
request completes, and the client silently stops working.

So logs go to stderr through `slog`. `main` names the process's streams
once and passes them down as `io.Writer`. forbidigo refuses
`fmt.Print*` and `os.Stdout` everywhere else, and `scripts/` is
excluded as maintainer tooling. Its settings carry their own messages,
so a settings block that fails to load shows up as forbidigo's default
message. The smoke test drives the binary over stdio and fails on any
line the server writes that is not a frame.

Rejected: a log level that quiets output in production, since one stray
print at any level still breaks the stream; a logging library with a
sink of its own, which writes where it likes.
Cost: a dependency that prints is unusable, and a debugging print goes
to stderr.

### D-03 Logs record the call, never the payload

A log line may say which method ran, against which resource, how it
ended and how long it took. It never carries message text, an email
address, a display name, a token or a search term.

Resource ids are the deliberate exception. A space id is how an
operator follows something up, the logs go to their own stderr, and an
id on its own says nothing about what was said.

The subtle leak is the request URL. `net/http` wraps every transport
failure in a `*url.Error` whose message renders the whole URL, and a
People search puts the search term in the query string. An ordinary
timeout would then log whom the person looked up. `withoutURL` strips
the URL from transport errors.

`TestLogsNeverCarryThePayload` drives the tools with a payload full of
canaries and fails if one appears at any level. Reverting `withoutURL`
makes it fail, which shows the test can.

Rejected: payloads at debug level, since a debug log is still a file
someone shares when asking for help.
Cost: a failure is diagnosed from the method, the resource and the
outcome alone.

### D-04 Nothing in the repository identifies a real account

The repository is public, and the server is built by running it
against a live Workspace account. Every id it handles reaches a
terminal during ordinary work: a doctor run, a smoke test, a pasted
error. From there it is one careless copy away from a fixture or a
commit message.

So nothing internal lands here: no organization names, email
addresses, space or message ids, account ids, Cloud project ids or
OAuth client ids. Fixtures are synthetic, and a made-up id says so, as
`spaces/AAAAspace1` does. `internal/leakcheck` scans the tree, uncommitted
files included, and finds an id hidden in base64 too.

Every rule in it is an allow-list. A deny-list naming the organization
or account to watch for would itself be the leak it means to prevent.
gitleaks covers credentials; this covers identifiers, which are not
secrets and pass gitleaks untouched.

Rejected: a deny-list of the maintainer's domains and spaces, for the
reason above.
Cost: a pattern cannot tell a sentence out of a real conversation from
invented prose, so keeping conversations out is a habit, not a check.

### D-05 Own wire types over raw REST

`internal/gchat` is a raw REST client with wire types of its own, and
declares only the fields this server reads. It does not import
`google.golang.org/api`.

That package drags in gRPC and telemetry for a handful of JSON calls.
Its generated types would also decide our output shapes, and they
accept or reject fields on their own terms, which is the opposite of
what D-08 needs.

The client stays small enough to read in full. Each request names its
verb, path, scope and limiter in one place, which is what the coverage
gate binds to Google's published methods (D-39).

Rejected: the generated client, for the reasons above.
Cost: every method and field is written by hand, and a field Google
adds stays invisible until someone models it; `make api-fields` makes
that a decision rather than an accident.

### D-06 Output types shadow the wire and never embed it

Every `*Output` struct in `internal/tools` is written out by hand
rather than embedding a wire type. The duplication buys an allow-list:
a field Google adds cannot reach the model until someone decides it
should.

The output schemas are generated from those Go types by
[`github.com/google/jsonschema-go`](https://github.com/google/jsonschema-go).
It emits no `$defs` or `$ref` and inlines a nested type wherever it
appears, so a type used twice on one tool is serialized twice. Those
bytes are protocol traffic and client-side validation, not the model's
context: an Anthropic Messages API tool carries an input schema and no
output schema. So the duplication is accepted.

Every output field carries a schema description, so a client and a
model reading the schema learn what the field means and when it is
null.

Rejected: embedding the wire types, which passes every upstream
addition straight to the model; hand-written JSON schemas, which would
save bytes at the price of drifting from the Go types they describe.
Cost: each field is written twice, once on the wire and once on the
output, with a mapping between them.

### D-07 The released tool surface is a contract

Tools keep their names and output fields, and resource templates keep
their URIs. A client, a saved permission rule and a model all address a
tool by name and read its fields by name, so a rename breaks them all.

`testdata/schemas-baseline.json` is the surface of the last release.
`scripts/gates schema-diff` compares the built server's
`--dump-schemas` with it and fails on a lost tool, field or template.
Adding is fine. `--dump-schemas` writes the whole surface whatever the
switches say, destructive tools included, so a tool the configuration
leaves out is still held.

On a pull request, `schema-ack` wants a commit footer naming each item
of the surface that changed, annotations included, since a flipped hint
changes whether a client asks. A break against the released surface
needs a `BREAKING CHANGE:` footer, and ships as a major release with a
new baseline. Footers are read as
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
defines them: a token in any case but `BREAKING CHANGE`, and a value
that may run over several lines.

Rejected: letting a rename through as an ordinary change, since a
caller finds out only when its call fails.
Cost: a bad name, once released, costs a major release to change.

### D-08 Drift is observable, never fatal

Google adds fields to the Chat API without notice. Decoding rejects
nothing: an unknown field increments a counter and logs its path once,
by name only. `google-chat-mcp doctor` walks live responses from the
caller's own spaces and reports what it saw.

Strict decoding turns an upstream addition into a total outage. Worse,
a write that lands and then fails to decode reports an error for
something that happened, which invites a duplicate.

A read that models only part of a resource asks for that part through
Google's [`fields` system parameter](https://cloud.google.com/apis/docs/system-parameters),
which every Google API takes. A field it never reads then raises no
false alarm, and the drift log stays worth reading.

Rejected: strict decoding, for the reasons above.
Cost: a renamed field reads as an absent value until the drift log or
`doctor` shows the new name.

### D-09 A dry run cannot write

`dry_run` is not a promise each handler keeps. The flag puts the call
on a context that `internal/gchat` refuses to write under. A tool that
declares `dry_run` and forgets its own preview branch then fails loudly
instead of posting.

A preview may read what it has to show or check: a delete's witness,
an invite's directory lookup, where a space sits before a section move,
or the quoted message's timestamp. It never sends a request that
changes Chat, whatever the method.

The guard sits under the handlers, the way the leak check sits under
the fixtures: a rule kept by remembering to look is not a rule.

Rejected: a preview branch in each handler alone, since a forgotten
branch posts for real and nothing notices.
Cost: a preview costs the reads the real call would make.

### D-10 Every tool goes through `register`, which reads one `Kind`

`register` takes a tool's `Kind` and derives from it what a client and
the operator see. A kind is a read, a read that writes a local file, a
write that only adds, a write that replaces or removes, either write
idempotent or not, or a destructive write.

From the kind come the MCP annotations (D-11), whether `GCM_READ_ONLY`
leaves the tool registered (D-36), and whether it needs
`GCM_ENABLE_DESTRUCTIVE` (D-12). A panic at start holds that a
destructive tool takes `confirm` and no other tool does
(D-18), and that only a destructive tool carries the mark (D-25). A tool
named `admin_` goes in the admin toolset and on the administrator's
context (D-30). Every output type must render the readable half of its
reply (D-13), and every call runs through the dry-run context (D-09).

Rules kept by hand at every call site are ways to be quietly wrong.
One place that decides them is one place to review.

Rejected: annotations and gating written per tool, for the reason
above.
Cost: a tool that fits no kind needs a new kind, and a new kind needs a
case in each derivation; until it has one, the server panics at start.

### D-11 Annotations say exactly what a tool changes

Annotations decide what a client prompts for, so they are exact.

`readOnlyHint` goes only on a tool that changes nothing in Chat. VS
Code's source ignores `destructiveHint` and prompts on every tool
without `readOnlyHint`. So `download_attachment`, which writes a local
file, does not carry it.

`destructiveHint` is false only on a write that adds and replaces
nothing, since the
[specification](https://modelcontextprotocol.io/specification/2025-11-25/schema)
reads false as "only additive updates" and defaults to true. That is
the adding kinds, and `download_attachment`, which never overwrites a
file. Every other write says true, a message
edit or a removed reaction as much as a delete, so a new tool errs
toward a prompt. Codex prompts by `destructiveHint`, and claude.ai and
ChatGPT document that it decides theirs.

An annotation is a request, not a control. What holds is server-side:
a tool that is not registered cannot be called (D-12), a dry run cannot
write (D-09), and a witness that does not match changes nothing (D-18).

Rejected: false as a write's default, which lets a new tool that
replaces something skip the prompt until someone notices.
Cost: a client that prompts by `destructiveHint` asks for an edit or a
removed reaction too.

### D-12 Destructive tools exist only when the operator turns them on

The destructive tools are registered only under
`GCM_ENABLE_DESTRUCTIVE=true`, and the flag is off by default. A tool
that is not registered cannot be called, by any client mode or any
model.

Every widely used client has a mode or a setting that runs a listed
tool with nobody approving the call, and the
[MCP specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
has clients treat annotations as untrusted. So a destructive tool's
prompt cannot be the control; registration is.

The default is off because a Chat delete cannot be undone. A Drive file
goes to a trash it can be restored from, and a deleted Chat message is
gone. google-drive-mcp and google-docs-mcp gate their deletes behind a
variable that starts off too.

Destructive covers the deletes, opening a space to an audience, making
someone a manager, inviting someone from outside the organization, and
the administrator writes D-29 names.

Rejected: registering the destructive tools and refusing them at call
time, which left Claude Code prompting for a tool the server then
refused; on by default, for the reason above.
Cost: an operator who wants a delete edits the configuration and
restarts the server.

### D-13 A reply carries both halves, in different forms

Every reply carries `structuredContent` under the output schema and a
readable rendering in `content`. The MCP specification asks for both:
"for backwards compatibility, a tool that returns structured content
SHOULD also return the serialized JSON in a TextContent block".
SEP-1624 asks that the two be "semantically equivalent (same
information, different presentation)", and names stringified JSON in
`content` as redundant context.

Neither half can be dropped, because clients disagree about which one
they show the model. Claude Code forwards only `structuredContent`
([anthropics/claude-code#55677](https://github.com/anthropics/claude-code/issues/55677)),
while claude.ai and ChatGPT show the text.

`internal/tools/render.go` holds the renderings: one record per line,
facts separated by " · ", and anything Google did not send left out.
`register` constrains a tool's output type to one that renders, so a
tool cannot be added without its text half.

Rejected: the serialized JSON in both halves, which spends the model's
context twice; one half alone, which goes blank on the clients that
read the other.
Cost: every output type carries a hand-written rendering, and no gate
can check that it says every fact the JSON says; the rendering tests
hold it.

### D-14 `send_message` posts the body verbatim, once

`send_message` posts the text exactly as given. The server adds no
prefix, no suffix and no "sent by an assistant" footer. The message goes
out under the person's name, so its words are theirs to choose, and a
caller that wants attribution writes it.

Each message carries a client-chosen message id, generated when the
caller gives none. A retry after a timeout therefore cannot post twice:
Google answers `ALREADY_EXISTS`, and the server fetches what landed and
returns that. The id is what earns the send a retry after a transport
failure (D-38).

Rejected: an attribution footer added by the server, for the reason
above.
Cost: a reader cannot tell from the message alone that a model wrote
it.

### D-15 The server asks the person nothing

Approving a call is the client's job. Claude Code has its permission
rules, and other clients draw a prompt from the annotations (D-11). The
server never asks a question of its own. It refuses the call, or makes
the risky variant a tool of its own (D-16).

A question through form elicitation is no consent control. The
specification lets the client answer it "from the user or other
sources". A Claude Code hook can answer it with no dialog, and Codex in
full access accepts an empty form by itself. Several clients decline it
undrawn or let it time out, and a question asked on every call expired
partway through a bulk delete.

forbidigo refuses the SDK's `Elicit` and the input requests a result
can carry, so the rule cannot be broken by accident.

Rejected: confirming writes through form elicitation, for the reasons
above; the mark and an elicitation on one tool, which asked the person
twice for one delete.
Cost: a client that prompts for nothing runs every registered write
unasked, which registration answers (D-12).

### D-16 A risky variant is a tool of its own

A remembered approval covers a whole tool, never an argument value:
claude.ai, Codex, VS Code and Copilot remember "always allow" per tool
or per server. Argument-level control exists only where an operator
writes it, in a hook or a Gemini CLI policy rule. So a risky variant
carried as an argument rides along unseen once its tool is always
allowed.

Each such variant is refused in the ordinary tool. Where it is wanted at
all, it is a tool of its own, registered only with the destructive ones
or in the admin toolset:

- a thread's replies go through `delete_thread`, never a `force` on
  `delete_message` (D-23);
- someone from outside the organization comes in through
  `add_external_member` (D-21);
- a manager is made by `promote_member` (D-22);
- a space opens to an audience through `set_space_audience` (D-20);
- an administrator acts through an `admin_` tool, reads included
  (D-28).

A broadcast to everyone in a space has no tool at all (D-19).

Rejected: a flag on the ordinary tool, refused unless set, since the
approval given for the tool covers the flag too.
Cost: the surface grows by a tool for each variant, and a model has to
pick the right one, which the descriptions name.

### D-17 A write to a space names the space it writes to

Every write that names a space takes `space_display_name` beside
`space_id`. A client's approval prompt shows the call's arguments, and a
space id tells the person reading it nothing.

The server reads the space and checks the name before anything is
written, dry runs included:

- a named space by its display name;
- a direct message by the other person's email, since Google's own
  lookup of the direct message with that address has to answer with
  this space;
- a group chat by the other members' emails, comma-separated in any
  order.

A mismatch is `[stale]` and changes nothing. The prompt therefore cannot
name one space while the call writes to another. A space this account
cannot read cannot be checked, so it cannot be written to here.

Rejected: the space id alone, which an approver cannot read; a name
taken on trust, which a model can get wrong unnoticed.
Cost: each write reads the space first, and a group chat's check lists
its members.

### D-18 A destructive call takes `confirm` and repeats what it acts on

Every destructive tool takes `confirm: true`. The argument is a witness
that the caller meant the call, never an approval: the model sets it.

A client's prompt shows the arguments, and a resource name tells the
person approving nothing. So a call that removes something repeats what
it acts on:

- `delete_message` and `delete_thread` take how the message's text
  begins, at least its first 20 characters;
- `delete_space` and `set_space_audience` take the space's display name;
- `delete_custom_emoji` takes the emoji's shortcode;
- `add_external_member`, for a group, takes its `groups/{id}` and its
  address, `group_email`.

The server reads the target first, a dry run included, and refuses
`[stale]` when it differs. What cannot be read cannot be compared, so a
target this account cannot read cannot be acted on here. A thread whose
first message is already deleted is the exception: no text is left to
compare, and the replies under it still go (D-23).

`remove_member`, `delete_section`, `promote_member`, and
`add_external_member` for a person take `confirm` alone. What they act
on is a membership or a section named as the listings show it, or an
address the call already carries.

Rejected: `confirm` alone everywhere, since a person approving
`spaces/AAAAspace1/messages/AAAAmsg1` cannot tell which message goes.
Cost: each such call reads its target before it writes.

### D-19 Mentioning everyone is refused

A `send_message` whose text mentions everyone in the space,
`<users/all>`, is refused `[blocked]` before anything is sent, a dry
run included. It notifies every member.

An `update_message` that edits such a mention in is refused too, since
an edit notifies whoever it newly mentions. An edit of a message that
already mentioned everyone may keep the mention. The mention is matched
regardless of case.

The refusal is the server's because a client approves a tool, never one
of its arguments (D-16). Google's own Chat MCP server tells the model,
in its `send_message` description, that mentioning all users "is
strictly prohibited".

Rejected: leaving it to the client, which approves `send_message` once
and every broadcast with it.
Cost: a person who means to notify everyone posts that message
themselves.

### D-20 A space is created private and opened only by `set_space_audience`

`create_space` and `update_space` refuse a target audience, `[blocked]`.
`update_space` can still make a space private. Opening a space is
`set_space_audience`, a destructive tool that carries the mark (D-25).
Whoever is in the audience can find the space, read it and join, and
making it private again does not take back what they read.

An audience goes in a patch of its own. Google refuses
`accessSettings.audience` beside any other field, and masking it with
no value makes the space private. So `update_space` refuses an
audience beside a name or description, rather than sending two patches
that could half land.

No API lists target audiences, so `audience` takes an id from the Admin
console, or `audiences/default` for the organization's default. The
guide and discovery disagree about who may set one; the tool
description names the space-manager rule the reference states.

Rejected: an `audience` argument on `create_space` and `update_space`,
which a remembered approval would carry unseen (D-16).
Cost: a discoverable space takes two calls, the second behind the
destructive flag.

### D-21 `add_member` invites only the organization into a space that admits outsiders

Someone added to a space can read what is already in it. Whether a
space admits people from outside the organization is Google's
`externalUserAllowed`, set when the space is created and immutable
afterward.

Where a space admits outsiders, `add_member` refuses `[blocked]` an
address the organization's directory does not list among its members,
before anything is sent, a dry run included. A directory that declines
to answer vouches for nobody: a personal account has none, so everyone
counts as outside there. A space that says nothing admits nobody from
outside, since Google leaves a false boolean out of its JSON, and needs
no lookup, since Google refuses the invite there itself.

Every Google Group is refused into a space that admits outsiders,
whatever the directory says of it. Google invites the group's outside
members and carries every later change to the group into the space
([Chat Help](https://support.google.com/chat/answer/13535086)), so no
check at add time holds. In a space that admits nobody from outside,
Google leaves the group's outsiders out, and a group add is ordinary. A
group is named by its Cloud Identity name, which `find_group` looks up
from an address.

`add_external_member` invites someone from outside, or a group, and is
destructive. `create_space`, `create_group_chat` and
`find_direct_message` stay ordinary writes: a new conversation shows
nobody what was already there.

Rejected: checking a group's members once at add time, which misses
everyone added to the group later.
Cost: a colleague the directory cannot see is refused as an outsider.

### D-22 Only `promote_member` makes a manager

A manager can change a space's settings and remove people, which makes
a new manager a new owner of the space. `promote_member` makes one, and
is destructive (D-12).

`update_member_role` takes `MEMBER` alone, to make a manager an ordinary
member again, and refuses the other roles `[blocked]`.

`add_member` takes no role. Google ignores a role given on create and
records `ROLE_MEMBER` without a word, so the result reports the role
Google recorded rather than the one requested.

Rejected: a `role` on `update_member_role` that reaches `MANAGER`, which
a remembered approval would carry unseen (D-16).
Cost: changing a role takes the tool that matches its direction.

### D-23 `delete_message` deletes one message, and `delete_thread` its replies

`delete_message` deletes one message and always sends `force=false`. A
thread's replies are other people's messages, and they go only through
`delete_thread`, a destructive tool of its own that sends `force=true`.

[Discovery](https://chat.googleapis.com/$discovery/rest?version=v1)
says `force` deletes a message's threaded replies, and that without it
a delete of a message with replies fails. It says
nothing of a root already deleted, and that state cannot be made through
the API to probe it. So `delete_thread` takes a first message that reads
as deleted without comparing its witness, sends `force=true`, and
reports Google's answer, a refusal included. The replies left under it
are what the call is for.

`unpin_message` and `remove_reaction` are ordinary writes, since one
call puts a pin or a reaction back.

Rejected: `force` as an argument on `delete_message` (D-16).
Cost: a model that means to clear a thread has to find the second tool,
which `delete_message`'s description names.

### D-24 `update_space` keeps the guidelines

Google's update mask covers all of `spaceDetails`. A description sent
alone therefore clears the space's guidelines.

`update_space` reads the guidelines first and sends them back unchanged
beside the new description. `admin_update_space` does the same, and an
administrator's patch keeps them as the caller's does. The read is the
one the space check makes anyway (D-17).

The guidelines are often other people's text, and a person changing a
description does not expect the rules of the space to vanish with it.

Rejected: sending the description alone, which clears the guidelines
as a side effect nobody asked for.
Cost: an edit can race another person's edit of the guidelines between
the read and the patch.

### D-25 The `requiresUserInteraction` mark goes only where the prompt is the point

`delete_space`, `admin_delete_space`, `set_space_audience` and
`delete_custom_emoji` carry `anthropic/requiresUserInteraction`, and no
other tool does. Claude Code prompts for a marked tool on every call in
every interactive mode, and denies it where no prompt can be drawn. The
MCP specification never names the key, and no other client reads it.

These are the tools whose prompt is itself the point. A space goes with
every message in it, whether its manager deletes it or an administrator
does. An emoji goes from every message and reaction across the
organization. An audience reads what it reads. The mark is per tool, so
on `set_space_audience` it covers an administrator's `audiences/{id}`
as well as the organization's default.

A mark on `delete_message` would ask once per message of a bulk delete,
and a question answered dozens of times in a row is answered without
reading. No client but Claude Code reads the mark, so the flag that
registers these tools is the control (D-12).

Rejected: the mark on every destructive tool, for the reason above.
Cost: a headless Claude Code run cannot call these tools; an Agent SDK
host can approve them in its own `canUseTool`.

### D-26 No tool widens the server's own reach

No tool turns a toolset on, lifts a refusal or edits a setting. The
server's settings are the environment, the profile `internal/userconfig`
writes and the token `internal/credentials` stores, all set by the
operator outside the conversation.

A model that could widen its own reach would make every other gate here
advisory. The registration switch (D-12), read-only mode (D-36) and the
refusals all assume the operator's settings hold for the whole session.

No package a tool reaches imports `internal/userconfig` or
`internal/credentials`, or sets an environment variable. Calling every
tool leaves the registered surface as it was. `admin_add_member` never
adds the caller, for the same reason (D-32).

Rejected: a tool that turns a toolset on when asked, which a remembered
approval would leave on for good.
Cost: changing what is registered takes the operator and a restart.

### D-27 Toolsets are feature areas, never access levels

`GCM_TOOLSETS` names the feature areas to register beside `core`, which
is always on: `sections`, `pins`, `emoji`, `events`, `readstate`,
`settings`, `availability` and `admin`. A smaller surface costs a model
less to read and gives it fewer wrong tools to pick.

`GCM_READ_ONLY` and `GCM_ENABLE_DESTRUCTIVE` cut across every toolset,
the admin set included. A toolset is never split by access. Read-only
mode already strips the write tools and the write scopes (D-36), and a
split would make the same switch twice.

One combination cannot be set: an administrator's reads with the
caller's own writes but without the administrator's edits. Read-only
mode removes both, and nothing else separates them. It would take a
toolset split by access.

Rejected: a read-only admin toolset beside a writing one, for the
reasons above.
Cost: the combination above is not available.

### D-28 Every administrator call is its own `admin_` tool

A Workspace administrator can act on any space in the organization
through Google's `useAdminAccess`, which the spaces and members methods
take. Nothing reaches messages that way, and nothing creates a space.
Every administrator call is its own `admin_` tool, reads included.

A remembered approval covers a whole tool, never an argument (D-16). A
flag on `get_space` or `update_space` would carry an administrator's
reach under an approval given for the ordinary tool. Every other gate
here works by leaving a tool unregistered, and a flag refused at call
time would be the one exception.

`admin_search_spaces` is `search_spaces` with the administrator's reach
and filters, and the two descriptions name each other. Without admin
access a search finds only joined spaces, and Google pages and counts
it only under admin access. Members see that "the organization's
administrator performed" a change, and not who
([admin overview](https://developers.google.com/workspace/chat/admin-overview)).

Rejected: a `use_admin_access` flag on the ordinary tools, for the
reason above.
Cost: the admin set repeats part of the ordinary surface.

### D-29 Administrator tools take their ordinary forms' kinds, except an add

The `admin_` tools follow the kinds of their ordinary forms (D-10):

- `admin_search_spaces`, `admin_get_space`, `admin_list_members` and
  `admin_get_member` read, and stay under `GCM_READ_ONLY`.
- `admin_update_space` and `admin_update_member_role` are ordinary
  writes. An edit of a name, description or guidelines says what it
  replaced, so it can be put back. What an administrator's patch may
  change is narrower than the caller's.
- `admin_promote_member` and `admin_remove_member` are destructive, as
  their ordinary forms are.
- `admin_update_space_permissions` is destructive, because widening a
  permission hands members what a manager holds.
- `admin_delete_space` deletes a whole space and carries the mark, as
  `delete_space` does (D-25).

`admin_add_member` is the exception: it is destructive though
`add_member` is not. Without admin access a caller adds people to a
space they are in, so the new member reads what the caller already
reads. With it, the caller adds people to a space whose history the
caller cannot see. That widens access, and what was read cannot be
taken back.

Rejected: `admin_add_member` as an ordinary write like `add_member`,
for the reason above.
Cost: an administrator adding a colleague to a space needs the
destructive flag.

### D-30 Administrator access rides on the request context

`register` puts every call of a tool whose name starts `admin_` on
`service.AsAdministrator`, which is `gchat.WithAdminAccess`. The client
adds `useAdminAccess=true` and the administrator's scope to every Chat
call made on that context. It adds the conditions Google sets on the
admin form too: a search names `customer = "customers/my_customer"`,
and a member listing filters on `member.type != "BOT"`. A Chat call
with no administrator form is refused with `ErrNoAdminForm` before it
leaves the process. People, Cloud
Identity and OpenID Connect calls on the same path go as the caller,
since none of them takes the parameter.

It is a context rather than an option on each call because the guards
read too. `remove_member` checks a refused delete by reading the
membership back. Read as the caller, a membership in a space the caller
is not in answers not found, and the refusal would be reported as a
member already gone. On the context, the read-back is the
administrator's, like the delete.

The name is the rule, because the name is what a client's prompt and
its permission rules see. `register` puts every `admin_` tool in the
admin toolset, and refuses at start one that names another.

Rejected: an administrator option on each client call, which a guard's
own read could miss.
Cost: every `internal/gchat` method has to say whether it has an
administrator form.

### D-31 Administrator writes act on named spaces only

Every administrator write that names a space refuses anything but a
named space, `[invalid]`, before it writes.

The space check (D-17) of a direct message or a group chat names the
people in it beside the caller. That means nothing for a conversation
the caller is not in. Google's administrator search finds named spaces
alone too, so an administrator has no way to find another kind.

The administrator writes that take a membership rather than a space
read nothing first, and rely on Google for that.

Rejected: checking an unnamed space by its members under admin access,
which asks the caller for addresses they have no way to know.
Cost: an administrator cannot act on a direct message or a group chat
through this server.

### D-32 `admin_add_member` adds people, never a group and never the caller

Adding yourself would let every tool here read the space as you, which
is the model widening its own reach (D-26). A group you belong to would
add you with it, and no check at add time sees a later change to the
group. So `admin_add_member` adds people only.

The caller's own address is compared first. Then the directory is asked
once whose the address is. An address it does not know is refused,
since Google adds nobody from outside on an administrator's say. One
that is the caller's under another name is refused too. A directory
that cannot answer vouches for nobody, and the add is refused.

Rejected: adding a group after checking its members once, which misses
everyone the group gains later.
Cost: an administrator adds a group's people one at a time.

### D-33 The administrator scopes follow the switches

With `admin` named, login asks for `chat.admin.spaces.readonly` and
`chat.admin.memberships.readonly`. Unless `GCM_READ_ONLY` is set, it
also asks for `chat.admin.spaces` and `chat.admin.memberships`. With
`GCM_ENABLE_DESTRUCTIVE=true` as well, it asks for `chat.admin.delete`,
the one restricted administrator scope, which deletes any space in the
organization.

One Admin console privilege covers an administrator's reads and writes
([Admin Help](https://support.google.com/a/answer/13369245)), and the
[privilege list](https://support.google.com/a/answer/1219251) has no
read-only form of it. So reads and writes part at the OAuth scope, and
the token holds no more than the switches allow.

None of the administrator scopes is in `scopes.All`, so nobody else's
consent screen carries them. `chat.delete` stays requested whatever the
destructive flag says: it reaches only the caller's own spaces, and
moving it would cost every installed token a second login.

Rejected: requesting the administrator scopes from everyone, which puts
them on every consent screen.
Cost: turning on the admin writes or deletes costs a second login.

### D-34 Idempotency is checked, not assumed

`delete_message` and `remove_member` treat a 404 as success. A 403 is
different. Google spells "already deleted, and this space keeps no
history" and "you may not delete this" the same way. So the refusal
path reads the target back, and only a not-found answer counts as gone.
A missing-scope 403 is never taken for gone.

A deleted message reads back as a 200 tombstone, not a 404, which the
[reference](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.messages/delete)
does not describe. `delete_message` reads the message before it
deletes, to compare its witness (D-18), so a repeat that finds a
tombstone sends nothing at all. A deleted space answers 403, so
"deleted" cannot be told from "not yours", and the refusal does not
claim to know which.

`delete_section` succeeds on 404 only. Its 403 is a system section
refusing deletion, and that is worth reporting.

Rejected: treating every 403 on a delete as already gone, which reports
a refused delete as one that happened.
Cost: a refused delete costs a second read.

### D-35 A People failure costs one field, never a row

Chat sends a sender's or a member's address itself, external people
included, and that answer wins. Its display name wins too: when Chat
names an address,
the name beside it is used over the People profile name. Only a user
Chat sent no address for is looked up in People, and there the People
name wins over Chat's. A user Chat sent an address but no name for is
looked up for the name alone.

What Chat names is written to the directory cache. A later payload with
no address, such as a reaction's user, still resolves someone this
server has seen. That covers a message's mentions and a search's
results as well as listings. A search reads the cache but never asks
People, since it can match many messages.

Enrichment is best effort. When a lookup fails, the rows come back
without addresses. A list is never emptied and a row is never dropped
because a name could not be resolved: an empty member list reads as an
empty space.

Rejected: failing the call when People fails, which turns one missing
scope into an empty listing.
Cost: an address can be null, and a reader has to treat null as
unknown.

### D-36 Read-only mode holds a token that cannot write

Under `GCM_READ_ONLY`, the write tools are not registered, and login
asks for the read-only scopes alone: the OpenID Connect ones and every
`.readonly` scope in the set, derived from the names. A read-only server
logged in that way holds a token that cannot write, whatever tools it
registers.

Google reads a space's notification setting only through
`chat.users.spacesettings`, which writes it too. So read-only mode
leaves `get_space_notification_setting` out, and its spec says so with
`WriteScope`.

A token from an earlier login keeps its write scopes until the next
login, since nothing narrows a token already granted, and the docs say
to log in again. Turning writes back on costs a second login, which the
`[scope]` error names.

Rejected: read-only as registration alone, which leaves a token that
can write in the keyring.
Cost: switching modes means logging in again.

### D-37 Every call names its scope, and an umbrella satisfies its parts

Every Chat, People and Cloud Identity call names the scope it needs, and
that scope is one login asks for. Reactions and reaction events name
`chat.messages.readonly`, which discovery lists for both. A group read
names `cloud-identity.groups.readonly`, the narrowest that works.

A missing-scope 403 becomes a `[scope]` error naming the scope URL and
the command that fixes it, because Google's own message does not.

`internal/scopes` records which umbrella scope satisfies which narrower
one, checked against the
[discovery document](https://chat.googleapis.com/$discovery/rest?version=v1).
Without that table
the server refuses calls Google would allow, and refuses them for the
people who paid the most for consent.

A desktop client's login can grant some scopes and not others, per
Google's [native-app guide](https://developers.google.com/identity/protocols/oauth2/native-app).
So the profile records the scopes Google returned, not those requested,
and `status` says which tools will fail.

Rejected: passing Google's 403 through as it comes, which names no scope
and no fix.
Cost: every new call needs its scope written down, and a call without
one fails a test.

### D-38 A write is retried only where repeating it is safe

A read retries on any transient failure. A write retries only when
Google answered, and only with an answer that means the request was
turned away before it was applied. That is a 429 alone. The whole 5xx
range is ambiguous: a 500 can follow a commit, a 502 or 504 comes from a
gateway that never learned what the backend did, and a 503 is "not
always safe to retry"
([code.proto](https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto)).

A write with an idempotency key is the exception, and it is declared
rather than assumed. `send_message` carries a client-chosen message id,
so Google refuses a second copy (D-14). That earns it a retry after a
transport failure or a 5xx. A server failure of a write that is not safe
to repeat is marked as possibly applied, so the reply does not invite a
blind retry.

The limiters stay inside Google's
[per-user quota](https://developers.google.com/workspace/chat/limits).
They are per
process and the quota is per user, so two clients on one account each
take the whole allowance, and a 429 is still handled with backoff.

Rejected: retrying a write on 503, which can make two of whatever it
created.
Cost: an ambiguous write failure goes back to the caller to check.

### D-39 Every API method and field has a written verdict

`testdata/api-methods.json` is every method of the Chat, People and
Cloud Identity APIs and the OpenID Connect endpoint, with its verb and
path as Google publishes them. `make api-diff` writes it, and nobody
edits it. `testdata/api-coverage.tsv` holds one verdict per method, by
hand: `used` names the `gchat.Client` method that calls it, and `out`
gives the reason it is left out. `testdata/api-fields.tsv` does the same
for published fields.

`scripts/gates api-coverage` holds the record and the client to each
other, offline. A method with no verdict fails, so a capability Google
adds cannot arrive unnoticed. A call with no row fails, and so does a
verdict on a method Google does not publish.

A `used` row is bound to its method: the verb and custom-method suffix
in the client's request have to be the ones Google publishes. A method
no requested scope authorizes is `unreachable`, derived from the scopes
in the snapshot rather than argued in prose. `chat.bot` is a scope a
person cannot hold, and some Cloud Identity methods publish none.

`make api-diff` refetches the discovery documents and reports what
changed, verb and path included. It reaches the network, so it is a
target somebody runs: a gate that fails when Google is slow is one
people learn to rerun until it passes.

Rejected: a hand-written list of what is left out, which nothing holds
to the API.
Cost: every method Google adds fails the build until someone judges it.

### D-40 Releases are `go install` and signed archives

A person installs with `go install`, or downloads a release archive
for Linux, macOS or Windows, on amd64 or arm64. The release signs
`checksums.txt` with a keyless Sigstore bundle tied to the release
workflow's identity, and carries an SBOM per archive and build
provenance. The README says how to verify each.

Keyless signing leaves no private key to hold or leak. cosign 3 needs
`--bundle`, and its older signature flags produce nothing
([v3.0.1 release notes](https://github.com/sigstore/cosign/releases/tag/v3.0.1)).
Pinning an action by SHA does not pin the tool it installs, so
`cosign-release` and `syft-version` are pinned beside the SHAs.
`make pins` holds every third-party tool to an exact version.

There is nothing to host, so nothing else is published but the bundle
(D-41) and the registry entry (D-42).

Rejected: a long-lived signing key, which is one more secret to hold,
rotate and lose.
Cost: verifying a download takes cosign and the identity the README
names.

### D-41 The Claude Desktop bundle ships inside the signature

The release also packs a `.mcpb` bundle for Claude Desktop, from
goreleaser's universal-binary post hook. That is the one point in the
pipeline where every binary exists and the checksum file is still
unwritten, so the bundle ships inside the signature rather than beside
it.

The packer, the bundle gate and `scripts/gates mcpb-manifest` run one
function, `manifestProblems`, so the release packs nothing the tests
refuse. They read the committed manifest once, with its schema, the
launcher and go.mod from the same root. The packer ships the bytes it
checked with only the version stamped in. The version is held to
Semantic Versioning, and a refusal says what is wrong with it, since the
schema takes any string. A bundle whose zip or file fails to close is
not written.

Rejected: packing the bundle after the release, which would leave it
outside `checksums.txt` and unsigned.
Cost: the bundle depends on goreleaser's hook order.

### D-42 The registry entry is published after the release exists

The MCP registry entry is written by `scripts/gates server-json` from
the release's checksum file, and published with `mcp-publisher`.

goreleaser's `mcp` block cannot publish an MCPB package, since it has
no field for the hash the registry requires. The registry checks an
MCPB entry in code, not in its published schema: a hash, a
release-asset URL on github.com or gitlab.com carrying "mcp", no
`registryBaseUrl`, and a HEAD on the URL before it accepts the entry.
So the entry can be published only
once the release exists, and from the same checksums the release signed.

go.mod's module path is read by one `modulePath`, which the registry
entry and the bundle share.

Rejected: goreleaser's `mcp` block, for the reason above.
Cost: publishing to the registry is a second workflow that runs after
the release.

### D-43 The bundle runs on every platform Claude Desktop does

The bundle carries macOS, Windows and Linux. A manifest picks a binary
by platform and has no key for the architecture, so every platform it
claims has to work on both amd64 and arm64. macOS does through a
universal binary, and Windows through amd64 under emulation. Linux has
neither, so the bundle carries both Linux binaries and
`server/launch-linux.sh`, which reads `uname -m` and execs the right one.

Claude Desktop for Linux ships for x64 and arm64 both, and the client's
own manifest schema lists `darwin`, `win32` and `linux`. Leaving Linux
out serves nobody, and a single Linux binary fails for real people.

The platforms and their entry points come from one staged table.
`compatibility.platforms` must be exactly the platforms the packer
stages an entry point for, and `entry_point` is the file the default
command runs.

Rejected: leaving Linux out, and one Linux binary, for the reasons
above.
Cost: the bundle carries a shell launcher, held to the staged files by
the packer's tests.

### D-44 The bundle asks for every setting the server reads

The bundle asks for what a person sets in another client's
configuration, each under the variable's name without `GCM_`,
lowercased: `client_secret`, `profile`, `read_only`,
`enable_destructive`, `toolsets` and `local_dir`.

The bundle gate derives that list from `config.Vars`. It records every
variable `Define` reads, and names those read outside it: the config
directory, its opt-in and the refresh token. The gate asks for every
one, less the removed ones and less each it gives a reason to leave out.
A setting added later is asked for until the manifest asks or a reason
is written, and a reason for a variable the server does not read is
refused.

A config test holds that every `GCM_` name the code spells, in every
file and every test, is in that registry. Every reader goes through
`config.Lookup`, which drops the spaces around a value, so spaces alone
are unset. The profile field states `config.ProfileRule`, as the
configuration page must, and the toolsets field lists every toolset in
order.

Rejected: a hand-kept list of bundle settings, which misses a setting
added later.
Cost: every new variable needs a bundle field or a written reason.

### D-45 The bundle's defaults configure nothing

A bundle install adds no approval behind the server's own: the
[manifest's](https://github.com/anthropics/mcpb/blob/v2.1.2/MANIFEST.md)
`tools` list is for display, Claude Desktop approves per tool by the
annotations, and "Always allow" skips the prompt from then on. So
every switch the bundle asks for defaults to the server's own, off. The
manifest lists no tools, since the settings change what registers, and
carries no icon.

The gate builds the configuration a person who accepts every default
would get, and holds it to an unconfigured server's. A required field's
default counts, since a host may fill it in. Every optional field has a
default, since a host passes an empty one through unsubstituted. A
default may not hold `${...}`, which a host may expand and the check
does not, nor be a list or an object.

The gate also starts every set of the switches and holds the sets the
server refuses to a declared list, both ways. The list is empty:
read-only wins over `enable_destructive` rather than refusing the pair,
and both fields say so.

`privacy_policies` names Google's policy, since the server sends data to
Google. The specification requires it of such a server, and the gate
refuses an empty list, which neither the schema nor `mcpb validate`
does.

Rejected: defaults that turn on writes or deletes for a smoother
install, since nothing behind the bundle asks again.
Cost: a person who wants deletes turns them on in the install form.

### D-46 The bundle passes settings by `env` alone

`mcp_config.env` carries every setting, and `mcp_config.args` is
present and empty. An argument would configure the server past the
defaults check, since a host substitutes `${user_config.x}` into `args`
as into `env`. A platform override's `env` or `args`, an empty one
included, replaces the base rather than adding to it, in the
[reference host](https://github.com/anthropics/mcpb/blob/v2.1.2/src/shared/config.ts).
So the gate refuses an argument, and any `env` or `args` on an override.

Every command starts with `${__dirname}/` and names a staged program,
the one form whose meaning the specification fixes. `LICENSE`
and `README.md` are packed to be read, not run.

The defaults check (D-45) reads `mcp_config.env` alone. That is sound
only while these refusals hold, so relaxing either needs the defaults
check to read the same.

The gate refuses a key given twice in one object of the committed
manifest, which its decode would merge while the schema and the host
keep the last, and two keys that differ only in case.

Rejected: platform overrides with settings of their own, which would
leave the base settings off that platform.
Cost: a platform cannot get a setting of its own.

### D-47 Windows programs carry a version resource that git ignores

Go writes no version resource, and code signing needs one. SignPath
Foundation signs only a program whose resource names the project and
carries the version it was asked to sign
([terms](https://signpath.org/terms)). Smart App Control blocks
"unknown, unsigned code", and every release is a new file with no
reputation
([Microsoft Learn](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/overview)).
The policy SignPath asks for is in the README and linked from every
release; it also says what is signed.

goreleaser's before hook runs `scripts/gates winres`, which writes one
`.syso` per Windows architecture into `cmd/google-chat-mcp`, where the
linker takes it. The resource holds the product name, from
`server.Name`, and the release's version, in the fixed part and in the
strings.

The `.syso` files are gitignored. Committed, they would carry one
release's version into every later build. Untracked and not ignored,
they would stamp `vcs.modified=true` into the binary, since Go reads
that from `git status`, which leaves ignored files out. goreleaser
checks for a dirty tree before the before hooks run, so a hook that
rewrote a tracked file would build the tag from files the tag does not
hold. `make release-rehearse` removes the files after a local run.

Rejected: committing the `.syso` files, for the reason above.
Cost: a pre-release and its release share one fixed version, which only
an installer compares; a `.syso` left by an interrupted goreleaser run
stays until `make clean`.

### D-48 Agent evals judge the space, never the model's account of it

`internal/evals`, behind the `evals` build tag and never in CI, scores
whether a model can do the job through these tools. Each task runs
against a scratch space the harness creates, and is judged on the space
read back afterward rather than on what the model said it did.

A task can declare itself unreachable when the world would not permit
the end state. It is then skipped rather than scored against the model.
A check that could never pass measures the harness, not the model.

The suite also records which half of a reply the client put in front of
the model, which is how the rendering's reach is checked (D-13).

Rejected: judging the model's final answer, which scores a claim rather
than the result.
Cost: a run needs a login, the `claude` CLI and real API spend, so the
suite runs only by hand.

### D-49 A live run writes down only what it created

`make live` and `make evals` drive the shipped binary against a real
account. Everything happens inside scratch spaces the run creates and
deletes, so nothing it touches belongs to anybody. The live driver
never leaves a space, since Google deletes a space its last member
leaves within seconds, which no Google page says.

A step prints, and an eval saves, only what the run itself created. A
tool result is written down only when the call that produced it named
the scratch space, and `redact` holds that. A model that calls
`list_spaces` or `search_people` gets rows about real conversations and
real colleagues, which would otherwise land in a saved trace.

No pattern can tell an invented space id from a real one, so the rule
goes by where a value came from, not by its shape.

Rejected: scrubbing transcripts by pattern, for the reason above.
Cost: a failure seen in a real space cannot be replayed from the
transcript.

### D-50 Forward a message through `send_message`

`send_message` quotes a message and never forwards one. Google's
[create-messages guide](https://developers.google.com/workspace/chat/create-messages)
quotes through `chat.messages.create` under user authentication, and
its release notes make the `FORWARD` quote type generally available. A
person's token is refused all the same: Google answers "Can't forward a
message" from main chat into the same space, between threads of one
space, and into another space. A `REPLY` quote that carries the quoted
message's latest `lastUpdateTime` posts and reads back.

So `quote_message` takes a message from the space being posted to. The
server reads its timestamp first, since Google refuses a stale one, and
refuses a message from another space with the reason before anything
is sent. A forward a person makes in the Chat client still reads back,
with its source space and that space's display name.

Rejected: a `FORWARD` quote type on `send_message`, or a
`forward_message` tool, which Google refuses to every token this
server can hold; falling back to a reply-quote, which posts something
other than what was asked.
Cost: sharing a message into another space is the person's to do in
the Chat client.

Next ID: D-51
