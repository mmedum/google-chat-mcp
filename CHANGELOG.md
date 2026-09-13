# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Sections are the Keep a Changelog set — Added, Changed, Deprecated, Removed,
Fixed, Security — in that order. Changes that require deployer action before
upgrading are marked **Breaking:** and say what to do.

## [Unreleased]

### Changed
- Release notes are published one heading level up. In `CHANGELOG.md` a
  version is an `##` and its change kinds are `###` underneath it; on the
  release page the version heading is gone, because GitHub renders the
  tag name as the page's `h1`. Published unaltered, the notes therefore
  started at `h3` directly under an `h1` — a skipped rank, which the
  W3C's heading guidance says to avoid. Confirmed by reading the rendered
  page rather than guessing: `h1 v2.0.4`, then `h3 Added`.

  So the section's headings are lifted one level on the way out, and the
  page reads `h1` then `h2` with nothing missing between. No wrapper
  heading was added: "Changelog" restates what the page obviously is, and
  repeating the version duplicates what GitHub already prints above it.
  Fenced code is left alone, since a `#` comment in a shell block is not
  a heading, and only `h3` and deeper are lifted, so a second `h1` can
  never be emitted.

## [2.0.4] - 2026-09-13

### Added
- A `transcript` gate, the last of the four to get one. The live driver
  and the eval harness may put a value into their transcript only through
  a redactor, and the gate reads their syntax trees to say so — 101
  writes, each a literal, a count, or through one, with an allowlist
  carrying a reason per entry.

  It found two things. The eval harness logged the whole tool response on
  a failure, unredacted, while every neighbouring line went through
  `clip` — the same line, in the same function, as a sibling server. And
  `clip` did not redact at all: it truncated, which reads as safe and is
  not, because the first 300 characters of a tool response are where an
  address is. It masks first and truncates second now, and a test holds
  that, because the gate can only say a value went through `clip` and not
  that `clip` still does anything.

  Also: the live driver's own address pattern could not match an address
  that had already been masked upstream, so the domain would have
  survived into the transcript — the third instance of that same fault,
  after two siblings.

### Changed
- The README follows the skeleton now shared by the sibling servers,
  checked against GitHub's own README guidance, the community profile
  checklist and the standard-readme spec. This repository supplied the
  skeleton and the tail, and was missing three things the sources name: a
  description under 120 characters, *why the project is useful*, and
  *where users can get help*. So: a shorter opening line, a `Why
  google-chat-mcp` section, `Getting help` carrying the `doctor` advice
  that was buried in the client setup, a `Documentation` section listing
  the five files under `docs/`, and a `Contributing` section, which the
  spec requires and which had been a sentence inside `Development`.
  `Connect your client` is `Connect a client` and `What keeps you safe`
  is `Safety`, so the four servers read the same.
- The release footer is the shared wording, and now carries the
  verification commands rather than telling you to verify.
- The address masking moved into `internal/redact`, the same package with
  the same two functions that the three sibling servers have. It was
  defined in the wire package here and in a different place with a
  different name in each of the others — four answers to one question,
  which is how a session working on one server ends up inventing a fifth
  rather than finding the fourth.

### Fixed
- The release page shows the release notes. `release.yml` has always
  written the `CHANGELOG.md` section for the tag and passed it with
  `--release-notes`, and `.goreleaser.yaml` has always thrown it away:
  `changelog: disable: true` is evaluated in the changelog pipe's `Skip`,
  which runs before `Run`, so `ctx.ReleaseNotes` was never assigned and
  the file the workflow had just written was never opened. The body
  collapsed to the footer. Every release page this project has published
  carries a footer and nothing above it, and the step that built the
  notes passed green each time — the failure was only visible by reading
  the page afterwards, which nobody did.

  The block is deleted rather than set to false. `release.footer` is
  untouched and still applies: `internal/pipe/release/body.go` renders
  `Header`, `ReleaseNotes`, `Footer` on every path, `--release-notes`
  included. Verified against goreleaser v2.18.1; the documentation does
  not describe the interaction, and its summary of it is misleading in
  the other direction.

  The README said the changelog "is also what the release notes are made
  from", which was the intent and not the behaviour. It is true now.


## [2.0.3] - 2026-09-13

### Changed
- `status` reports the account the same way in all four: the local part
  removed, the domain kept. The domain is the half a diagnosis uses —
  shared drives are a Workspace feature and a personal account cannot
  create one, so `@gmail.com` and a Workspace domain are two different
  sets of behaviour to explain — while the local part answers nothing.
  It is never an input to any command here, and this output is what the
  issue form asks people to paste. One server showed it in full, one
  masked the domain as well (which hid the useful half), and two sat in
  between.

### Fixed
- `--version` reports one spelling whichever way the binary was built.
  goreleaser stamps its own `{{.Version}}`, which has the leading `v`
  stripped, so a release archive said `1.1.0`; `go install` stamps
  nothing and the fallback reads `v1.1.0` out of the build info. The same
  release therefore reported two different strings depending on how
  somebody installed it, and anything parsing `--version` got a different
  answer per install method. Reported from outside by a reader comparing
  five servers side by side.
  The release stamp now carries the tag itself rather than goreleaser's
  v-stripped form, so the two sources agree at the source; the
  normalisation stays for a version passed by hand to `make`.
- `status` prints the same lines, in the same order, with the same
  labels as the three sibling servers, once a profile is configured (the
  not-yet-signed-in message still differs between them). They had drifted into four shapes
  — a version banner in three of them, `client secret` against
  `client json`, `read-only` against `read only`, four label widths — and
  the same reader found that too.
- A permission failure no longer repeats the account it refused. Google
  names the account in the message of a 403, and that message was
  repeated verbatim into the error string — as was the whole response
  body when it was not an error envelope at all. That string reaches
  stderr, and the MCP stdio transport says a server may write logging
  there and clients "MAY capture, forward, or ignore" it, while the
  protocol's logging section says log messages MUST NOT carry personal
  identifying information. The local part is now masked where Google's
  text is parsed — one place, rather than at each print, so a print added
  later is safe without its author knowing the rule, and a writer wrapper
  could split an address across two Write calls and miss it. The domain
  is kept, because it is what says which account was refused.
## [2.0.2] - 2026-09-13

### Added
- The API-fields gate judges every struct, not only the ones whose name a
  schema happens to share. It compared the name matches and skipped the
  rest in silence, with a floor of 20 under the number matched standing in
  for a check — which against a real 49 left 29 renames of headroom. A repo-wide
  rename of a modelled struct took its properties out of the comparison
  and the gate still printed ok. It now runs a third direction over the
  wire package: every struct carrying a JSON tag must match a published
  schema, be named by an `alias` row, or carry a new `local` row saying it
  models none. The floor is gone rather than raised, because the rename
  now fails on the renamed type itself. Forty structs were invisible; they are accounted for now, and 36 more schemas are compared (49 to 85).
  A rejected row no longer counts as a decision either: an invalid `out`
  row used to excuse the very field it named.
- **An API-fields gate.** `make api-fields` is the coverage gate one
  level down: `testdata/api-fields.json` is every schema and property
  the Chat and People discovery documents publish, `testdata/api-fields.tsv`
  is one hand-written row per exception, and the modelled side is read
  out of `internal/gchat` with `go/ast`, promoting embedded structs'
  tags. Both directions fail — a field Google adds to a type this server
  models, and a field this server carries that nothing publishes — and
  the number of schemas matched is part of the rule, because a gate that
  matches a struct to a schema by name goes blind the moment somebody
  renames a struct.

  It is keyed by API, not by schema name, because Chat and People both
  publish a `Membership`: merging them invents missing fields and
  unpublished ones in equal measure. Two other verdicts exist for the
  same reason. `owner` says which API a struct of a shared name models,
  and `unrelated` says a struct that merely shares a schema's name is
  about something else — this server's `Section` is a space's place in
  the sidebar where Chat publishes a card layout, and its `Media` is a
  download's bytes where Chat publishes a resource name.

### Changed
- **Compact JSON on every request.** Google indents its JSON unless told
  otherwise, and `prettyPrint` is a system parameter of every Google API
  rather than a Chat feature, so this client now asks for it once in
  `newRequest` — which both the JSON path and the transfer path come
  through — instead of at each place that builds a query. A call added
  later and given no thought gets it too. A space's message history is
  what pays for the indentation here; on a sibling server the same change
  took a large response from 7.44 MB to 2.96 MB. Set after the host
  allowlist check, which is what makes rewriting the URL safe: every
  request reaching that point is one this client has already decided the
  access token may go to. Skipped for a request that is not asking for
  JSON — an attachment download asks for bytes, and a media URL carrying
  a JSON formatting parameter reads as a mistake. A query that names
  `prettyPrint` itself is left alone.

  Three patch tests compared the whole query string against
  `updateMask=…`, which made them assertions about every parameter rather
  than about the mask. They read the mask out of the query now, through
  one helper, which is what they were always about: an unmasked patch is
  what clears the cards and attachments this server cannot rebuild.

### Fixed
- A forwarded message's space is read again. `ForwardedMeta` carried a
  single `spaceName`, a name Chat publishes nowhere — `ForwardedMetadata`
  publishes `space` and `spaceDisplayName` — so the field never decoded
  and a forwarded message came back with nothing in it. Found by the new
  third direction of the api-fields gate.
- A space permission granted to assistant managers is reported. The
  permission-setting type modelled `managersAllowed` and `membersAllowed`
  and not `assistantManagersAllowed`, so a ROLE_ASSISTANT_MANAGER grant
  decoded as granted to nobody.
- **A quoted message's attachments never decoded.** Chat spells the
  field `attachments` on `QuotedMessageSnapshot` and `attachment` on a
  `Message`; the singular was copied to the plural's type, so the field
  was silently always empty. Found by the api-fields gate on its first
  run, which is the kind of bug it exists for: a wrong tag reads exactly
  like an API that sent nothing.
- **Three fields that could never be populated are gone.**
  `MessagePin.createTime` and `.creator`, and `Membership.membershipId`,
  are not published by the API and were read by nothing. The
  `Affiliation` field beside the last of them carries a comment saying a
  live doctor run reported it as drift twice before anything modelled
  it; that is the same class of problem, now caught by a gate rather
  than by someone noticing.

## [2.0.1] - 2026-09-08

### Fixed
- **`go install` works again.** The module path is now
  `github.com/mmedum/google-chat-mcp/v2`, which Go requires from v2
  onwards. Without it `go install ...@v2.0.0` failed outright and
  `@latest` silently installed v1.0.0 — deletes ungated, none of the 2.0.0
  fixes, and no warning. Install with:
  `go install github.com/mmedum/google-chat-mcp/v2/cmd/google-chat-mcp@latest`.
  The archives, the Claude Desktop bundle and the registry entry were
  never affected. **Breaking for importers only:** if you import this
  module, add `/v2` to the path. The tool surface is unchanged.

## [2.0.0] - 2026-09-07

### Changed
- **Breaking: deletes are refused unless you turn them on.** Set
  `GCM_ALLOW_DESTRUCTIVE=true` to allow `delete_message`, `delete_space`,
  `delete_section` and `delete_custom_emoji` to do anything; without it
  they answer `[unsupported]` and change nothing. The tools stay
  registered either way, so the tool surface is unchanged and a model is
  told why rather than left to infer it from a missing tool. Off by
  default because a deleted Chat message has no trash behind it, and the
  sibling Drive and Docs servers gate their recoverable deletes the same
  way. If you rely on deletes, set the variable.
- **Four read tools now page.** `list_spaces`, `get_messages`,
  `get_thread` and `list_members` take `page_token` and return
  `next_page_token`, like the nine listings that already did. They
  previously returned the first page and said nothing about the rest, so
  a caller could report 50 of 300 members as the whole membership.
- **`send_message` takes `client_message_id`.** Set it and repeating the
  exact call lands on the same message instead of posting a second one.
- Read-only mode no longer advertises `send_message` and
  `find_direct_message`, which it does not register.
- `get_messages`, `get_thread` and `list_members` report `unparsed`, so a
  listing that dropped rows says so instead of only logging it.
- Quoted messages carry their content: `quotedMessageSnapshot` is
  modelled, so a reply can be read together with what it replied to.
- **The Claude Desktop bundle now covers Linux.** It shipped macOS and
  Windows only, on the belief that Linux would need two bundles or a
  broken one. Claude Desktop for Linux exists and supports both x64 and
  arm64, so the bundle carries both Linux binaries and picks between them
  at start. macOS and Windows are unchanged.

## [1.0.0] - 2026-09-06

First release. A single Go binary that speaks MCP over stdio, runs as a
subprocess of your client, and talks to Google Chat as you.

### Added
- **The binary and its subcommands.** `login`, `logout`, `status`, `doctor`,
  `--version` and `--dump-schemas`. Login is a loopback OAuth flow with PKCE;
  the refresh token goes to the OS keyring, falling back to a `0600` file when
  there is no keyring, and the command says which happened.
- **53 tools over the Chat API**, in groups you can leave out with
  `GCM_TOOLSETS`: messages and threads, spaces and members, search, sidebar
  sections, pins, read state, notification settings, custom emoji, space
  events, availability and custom status, and attachments. Every space,
  message and thread is addressed by its resource name, never by position.
- **Three resources.** `gchat://spaces/{id}` and its messages and threads,
  carrying the same content as the matching `get_` tools.
- **A dry run on every write that can take one.** `dry_run: true` returns the
  exact request body and sends nothing — enforced underneath the handler, on a
  context the API client refuses to write under, so a tool that forgot its own
  preview branch fails rather than posting.
- **`send_message` posts the body exactly as given.** No prefix, no suffix,
  nothing appended.
- **Three settings that narrow what the server can do.** `GCM_READ_ONLY=true`
  registers only the tools that change nothing; `GCM_ALLOW_DESTRUCTIVE=false`
  keeps the writes but drops the deletes; `GCM_LOCAL_DIR` is the one directory
  attachments may be read from or written to, and unset — the default — means
  no file transfer at all.
- **`GCM_INTERACTION_HINT`** marks every write as needing a person, which
  clients that read the mark honour by asking first. Default on. Turn it off
  for a deployment with nobody at the keyboard: in Claude Code the mark is
  absolute, and headless every write is refused before it reaches Google.
- **Unknown response fields are counted and named, never fatal.** Google adds
  fields without notice; `doctor` lists the ones this server does not model, so
  a field worth reading can be found before anyone needs it.
- **Errors say what to do.** Each carries a class — `invalid`, `not_found`,
  `auth`, `scope`, `forbidden`, `conflict`, `quota`, `server` — so a refusal
  that more access would fix reads differently from one that waiting fixes.
- **Signed releases.** Six platform archives, `checksums.txt` signed with a
  keyless Sigstore bundle, an SBOM per archive, and build provenance. The
  README shows how to verify each.
- **A Claude Desktop bundle.** The `.mcpb` on each release carries a macOS
  binary that runs on both architectures and a Windows one; its SHA-256 is in
  the signed checksum file. Claude Code does not install `.mcpb` files and
  keeps the command in the README.
- **An MCP registry entry**, `io.github.mmedum/google-chat-mcp`, pointing at
  that bundle and carrying the hash clients check before installing.

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v2.0.4...HEAD
[2.0.4]: https://github.com/mmedum/google-chat-mcp/compare/v2.0.3...v2.0.4
[2.0.3]: https://github.com/mmedum/google-chat-mcp/compare/v2.0.2...v2.0.3
[2.0.2]: https://github.com/mmedum/google-chat-mcp/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/mmedum/google-chat-mcp/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/mmedum/google-chat-mcp/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v1.0.0
