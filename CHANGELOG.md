# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Sections are the Keep a Changelog set — Added, Changed, Deprecated, Removed,
Fixed, Security — in that order. Changes that require deployer action before
upgrading are marked **Breaking:** and say what to do.

## [Unreleased]

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
