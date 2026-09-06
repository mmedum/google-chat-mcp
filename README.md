# google-chat-mcp

[![CI](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/mmedum/google-chat-mcp?sort=semver)](https://github.com/mmedum/google-chat-mcp/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/mmedum/google-chat-mcp.svg)](https://pkg.go.dev/github.com/mmedum/google-chat-mcp)
[![License: Apache 2.0](https://img.shields.io/github/license/mmedum/google-chat-mcp)](./LICENSE)

Google Chat as MCP tools. Read, search and write to your spaces, direct
messages and sidebar from Claude Code, Claude Desktop, or any other MCP
client.

A single Go binary that speaks MCP over stdio. It runs as a subprocess of
your client, on your own machine, against your own Google account. There
is no server to host, no shared deployment and no service account: you
create a Google OAuth client, log in once, and the refresh token stays in
your OS keyring.

## Install

```bash
go install github.com/mmedum/google-chat-mcp/cmd/google-chat-mcp@latest
```

Or take a signed archive from the
[latest release](https://github.com/mmedum/google-chat-mcp/releases/latest)
— Linux, macOS and Windows, on amd64 and arm64 — and verify it before
you run it:

```bash
sha256sum -c checksums.txt --ignore-missing

# The checksum file is signed with a keyless Sigstore certificate tied to
# the release workflow's identity. The bundle carries both.
cosign verify-blob checksums.txt \
  --bundle checksums.txt.bundle \
  --certificate-identity-regexp 'https://github\.com/mmedum/google-chat-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# And the archive itself carries build provenance.
gh attestation verify google-chat-mcp_*.tar.gz --repo mmedum/google-chat-mcp
```

Every archive also ships an SBOM, so you can see what is inside a binary
you did not build.

### Claude Desktop

Every release also carries a `.mcpb` bundle. Open it and Claude Desktop
installs the server and asks for your OAuth client JSON — no config file
to edit. It holds a macOS binary that runs on both architectures and a
Windows one; its SHA-256 is in the same signed `checksums.txt`. The
release is listed in the MCP registry as
`io.github.mmedum/google-chat-mcp`.

The bundle does not log you in. Install the binary as well, run
`google-chat-mcp login` once, and the bundle picks up the same
credentials. Claude Code does not install `.mcpb` files, so it uses the
command below.

## Set up Google

You need your own OAuth client. It takes about fifteen minutes once, and
[`docs/gcp-setup.md`](docs/gcp-setup.md) walks through it. In short:
create a project, enable the Google Chat API and the People API,
configure the consent screen with the scopes this server asks for, and
create an **OAuth 2.0 Client ID** of type **Desktop app**. Download the
JSON.

Then log in:

```bash
google-chat-mcp login --client-secret ./client_secret.json
```

Login opens your browser, or prints the URL when there is no browser to
open (`--no-browser` forces that). The callback lands on `127.0.0.1` on a
random port, with PKCE and state throughout. The refresh token goes into
your OS keyring; if there is no keyring, it goes into a `0600` file and
the command tells you so.

`google-chat-mcp status` says which account is signed in and where the
token lives. `google-chat-mcp logout` revokes the token at Google and
deletes the local copy.

### Logging in over SSH

The callback goes to the *remote* host's loopback address, and your
browser is local, so forward the port. It is chosen at random and printed
only once login is already waiting, so read it out of the printed URL —
it appears percent-encoded, as `127.0.0.1%3A<port>` — and in a second
local terminal:

```bash
ssh -N -L <port>:127.0.0.1:<port> user@remote-host
```

Then open the URL locally. If `ssh` reports `bind: Address already in
use`, cancel the login with Ctrl-C and run it again to draw another port.

## Connect your client

Claude Code:

```bash
claude mcp add google-chat -- google-chat-mcp
```

Or, in a client config file:

```json
{
  "mcpServers": {
    "google-chat": {
      "command": "google-chat-mcp"
    }
  }
}
```

Claude Desktop reads the same shape from its own config file. Use the
binary's absolute path there if it is not on the app's `PATH`, or install
the `.mcpb` bundle above and edit nothing.

Every setting is an environment variable, listed in
[`docs/configuration.md`](docs/configuration.md). The three worth
knowing now: `GCM_READ_ONLY=true` leaves out every tool that changes anything in Chat,
`GCM_PROFILE` lets one machine hold a work account and a personal one,
and `GCM_LOCAL_DIR` names the one directory files may be written to and
read from. Without that last one the server touches no files at all,
and the three tools that move them say so.

If something does not work, run `google-chat-mcp doctor`. It checks the
credentials, the granted scopes and what Google actually answers, and
names what is missing.

## Tools

| Tool | What it does | Scope |
|---|---|---|
| `whoami` | Which account the stored credentials belong to | `openid email profile` |
| `list_spaces` | Spaces, group chats and direct messages you are in | `chat.spaces.readonly` |
| `get_space` | One space by resource name | `chat.spaces.readonly` |
| `search_spaces` | Named spaces by display name, including ones you are not in | `chat.spaces.readonly`; `chat.admin.spaces.readonly` for `use_admin_access` |
| `find_group_chats` | The group chats holding exactly you and the people you name | `chat.memberships.readonly`, `chat.spaces.readonly` |
| `find_direct_message` | The direct message with one person, created if there is none yet | `chat.spaces.readonly`, `chat.spaces.create` |
| `get_messages` | Recent messages in a space, newest first, senders resolved to names | `chat.messages.readonly` |
| `get_message` | One message, with its reaction counts | `chat.messages.readonly` |
| `get_thread` | Every message in one thread, oldest first | `chat.messages.readonly` |
| `download_attachment` | Save a message's attachment into the server's local directory | `chat.messages.readonly` |
| `search_messages` | Google's search across every space you can see, or a regular-expression scan of one | `chat.messages.readonly`; `chat.users.readstate.readonly` for `unread_only` |
| `search_people` | Turn a name into an email address, from the directory and your contacts | `directory.readonly`, `contacts.readonly` |
| `list_members` | Who is in a space, resolved to names and addresses | `chat.memberships.readonly`, `directory.readonly` |
| `get_member` | One membership: who or what it is, their role, whether they have joined | `chat.memberships.readonly`, `directory.readonly` |
| `update_member_role` | Make someone a member, manager or assistant manager | `chat.memberships` |
| `list_reactions` | Reactions on a message | `chat.messages.reactions` |
| `list_pinned_messages` | What a space has pinned | `chat.spaces.pins.readonly` |
| `pin_message` | Pin a message, which everyone in the space sees | `chat.spaces.pins` |
| `unpin_message` | Remove a pin | `chat.spaces.pins` |
| `get_space_read_state` | How far you have read in a space | `chat.users.readstate.readonly` |
| `get_thread_read_state` | How far you have read in a thread | `chat.users.readstate.readonly` |
| `mark_space_read` | Clear a space's unread badge, for you only | `chat.users.readstate` |
| `mark_space_unread` | Rewind your read mark, for you only | `chat.users.readstate` |
| `list_space_events` | What changed in a space over the last 28 days | the read scope of each event type asked for |
| `get_space_event` | One of those changes by resource name | `chat.spaces.readonly` |
| `get_space_notification_setting` | What Chat tells you about a space | `chat.users.spacesettings` |
| `update_space_notification_setting` | Change it, or mute the space | `chat.users.spacesettings` |
| `get_availability` | Your own presence and custom status | `chat.users.availability.readonly` |
| `set_availability` | Active, away, or do not disturb until a time | `chat.users.availability` |
| `set_custom_status` | Set or clear the text and emoji beside your name | `chat.users.availability` |
| `list_custom_emojis` | Your organisation's own emoji | `chat.customemojis.readonly` |
| `get_custom_emoji` | One of them by resource name | `chat.customemojis.readonly` |
| `create_custom_emoji` | Add one from a local image | `chat.customemojis` |
| `delete_custom_emoji` | Remove one, for everyone | `chat.customemojis` |
| `delete_space` | Delete a space and everything in it | `chat.delete` |
| `list_sections` | Your own sidebar sections | `chat.users.sections.readonly` |
| `list_section_items` | What a section holds, or which section a space sits in | `chat.users.sections.readonly` |
| `send_message` | Post text, exactly as given. Optionally into a thread, or carrying an uploaded file | `chat.messages.create` |
| `upload_attachment` | Send a local file to a space and get the token that attaches it | `chat.messages.create` |
| `update_message` | Edit the text of a message you sent | `chat.messages` |
| `delete_message` | Delete a message. Already gone counts as success | `chat.messages` |
| `add_reaction` | React to a message with a Unicode emoji | `chat.messages.reactions` |
| `remove_reaction` | Remove a reaction, by resource name or by message, emoji and person | `chat.messages.reactions` |
| `create_group_chat` | Start an unnamed group chat with 2 to 20 people | `chat.spaces.create` |
| `create_space` | Create a named space with up to 20 people | `chat.spaces.create` |
| `update_space` | Rename a space or change its description | `chat.spaces` |
| `add_member` | Invite someone to a space by email | `chat.memberships` |
| `remove_member` | Remove a membership. Already gone counts as success | `chat.memberships` |
| `create_section` | Add a sidebar section | `chat.users.sections` |
| `rename_section` | Rename a sidebar section | `chat.users.sections` |
| `delete_section` | Delete a sidebar section; its spaces fall back to the defaults | `chat.users.sections` |
| `position_section` | Reorder a section, by absolute rank or by moving it to the start or end | `chat.users.sections` |
| `move_space_to_section` | File a space under a section. A space already there is left alone | `chat.users.sections` |

Sections are your own sidebar. Nothing there changes a space, and nobody
else sees it.

Three resources carry the same content as the matching tools, for a
client that includes resources in its context:

- `gchat://spaces/{space_id}`
- `gchat://spaces/{space_id}/messages/{message_id}`
- `gchat://spaces/{space_id}/threads/{thread_id}`

## What keeps you safe

- **`dry_run` on thirteen write tools.** It returns the request body that
  would have been sent, and the call cannot reach the network: the flag
  puts the request on a context the HTTP client refuses to write under,
  so a tool that forgot its own preview branch fails loudly instead of
  posting.
- **`GCM_READ_ONLY=true` leaves the write tools unregistered.** A tool
  that is not registered cannot be called, whatever permission mode the
  client is in or whatever a model asks for. It is set where you start
  the server, so it binds the whole session rather than one call.
- **Annotations and an interaction hint.** Write and delete tools ask the
  client to put a person in the loop. What that is worth is the client's
  decision, and the two answers are further apart than they look. In
  Claude Code interactively, auto mode has run a write with no prompt at
  all. Headless, the same mark refuses every write outright — an allow
  rule does not suppress it, and there is nobody to ask. So if you are
  automating with nobody at the keyboard, `GCM_INTERACTION_HINT=false`
  is the way to use the write tools at all, and it is a decision to make
  on purpose.
- **`send_message` posts the body verbatim.** No prefix, no suffix, no
  "sent by an assistant" footer.
- **A retried post cannot land twice.** Each message carries a
  client-chosen id, so a retry after a timeout returns what already
  landed instead of posting again.
- **Logs never carry the payload.** They record which call was made, to
  which resource, and how it ended. Message text, addresses, names and
  search terms stay out, and a test fails the build if one appears.

## How it works

```
MCP client ──stdio──► google-chat-mcp
                       ├── tools      one handler per tool; shapes the reply
                       ├── service    the rules: idempotency, degrading, search
                       ├── gchat      REST client for Chat, People and OIDC
                       ├── directory  email cache, so a page of messages costs one lookup
                       └── auth       refresh token → access token
```

[`docs/architecture.md`](docs/architecture.md) has the request flow, the
package layout, the evidence log behind the conventions, and the design
decisions a contributor should not undo. The threat model is in
[`docs/security.md`](docs/security.md), and the procedures for rotating
or recovering credentials are in [`docs/runbook.md`](docs/runbook.md).

## Development

```bash
make build     # the binary
make test      # race detector, coverage floor
make check     # everything CI runs
```

`make check` is the definition of done: formatting, `go vet`,
golangci-lint, race tests with a per-package coverage floor,
`govulncheck`, a licence check, a stdio smoke test, a schema diff against
the released tool surface, and a staleness gate that fails when this
README, the docs or the changelog drift from the code. Contributing
conventions are in [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Versioning

Tool names and their output fields are stable within a major version. A
change needing you to act — a new scope, another login, a different
command in your client config — is marked **Breaking:** in
[`CHANGELOG.md`](CHANGELOG.md), which is also what the release notes are
made from.

## Security

[`SECURITY.md`](SECURITY.md) says how to report a vulnerability.

## Code of conduct

[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) — Contributor Covenant 3.0.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
