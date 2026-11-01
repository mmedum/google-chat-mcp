# google-chat-mcp

[![CI](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/mmedum/google-chat-mcp?sort=semver)](https://github.com/mmedum/google-chat-mcp/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/mmedum/google-chat-mcp/v9.svg)](https://pkg.go.dev/github.com/mmedum/google-chat-mcp/v9)
[![License: Apache 2.0](https://img.shields.io/github/license/mmedum/google-chat-mcp)](./LICENSE)

Google Chat as MCP tools. Read, search and write to your spaces, DMs and sidebar.

A single Go binary that speaks MCP over stdio. It runs as a subprocess of
your client, on your own machine, against your own Google account. There
is no server to host, no shared deployment and no service account: you
create a Google OAuth client, log in once, and the refresh token stays in
your OS keyring. Claude Code, Claude Desktop and any other MCP client can
drive it.

## Why google-chat-mcp

Chat integrations are normally apps: they post as a bot, see only the
spaces somebody added them to, and need a service account an
administrator provisions. This one runs as **you**. It reaches what your
account already reaches, and messages you send are from you, not from an
app with your name on it.

That also puts the user-scoped half of Chat in reach, which an app
identity cannot get to at all: your sidebar sections, how far you have
read in a space or a thread, your availability and custom status, and
your organization's custom emoji. Sixty-nine tools cover it, most writes
take `dry_run`, and nothing is requested at login beyond the scopes the
tools you are actually using need.

## Install

```bash
go install github.com/mmedum/google-chat-mcp/v9/cmd/google-chat-mcp@latest
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
installs the server and asks for the settings a config file would carry,
with no file to edit: your OAuth client JSON, which may be left empty
to use the one `login` recorded, the profile, read-only,
the toolsets and the attachment folder. The tools that cannot be taken
back (`enable_destructive`) stay off unless you turn them on, and so
does attachment transfer until you pick a folder. This server asks
nobody before a call. Claude Desktop approves each tool, and a tool you
always allow then runs unasked, so leave `enable_destructive` off unless
you need its tools.

It covers macOS, Windows and Linux on both architectures each:
macOS through a universal binary, Windows through amd64, and Linux
through a small launcher that picks the right binary at start, because a
bundle manifest has no key for the architecture. Its SHA-256 is in the
same signed `checksums.txt`. The release is listed in the MCP registry as
`io.github.mmedum/google-chat-mcp`.

The bundle does not log you in. Install the binary as well, run
`google-chat-mcp login` once, and the bundle picks up the same
credentials. Claude Code does not install `.mcpb` files, so it uses the
command below.

## Set up Google

You need your own OAuth client. It takes about fifteen minutes once, and
[`docs/gcp-setup.md`](docs/gcp-setup.md) walks through it. In short:
create a project, enable the Google Chat API, the People API and the
Cloud Identity API,
configure the consent screen, list the scopes this server asks for if
the app is External, and create an **OAuth 2.0 Client ID** of type **Desktop app**. Download the
JSON.

Then log in:

```bash
google-chat-mcp login --client-secret ./client_secret.json
```

Login opens your browser, or prints the URL when there is no browser to
open (`--no-browser` forces that). The callback lands on `127.0.0.1` on a
random port, with PKCE and state throughout. The refresh token goes into
your OS keyring; if there is no keyring, it goes into a `0600` file and
the command tells you so. On Windows a Go file mode only sets the
read-only attribute, so what protects that file there is the ACL its
directory inherits, not the mode.

`google-chat-mcp status` says which account is signed in and where the
token lives. `google-chat-mcp logout` revokes the token at Google and
deletes the local copy.

`google-chat-mcp status --json` prints the same state as one JSON object
on stdout, for a script that needs to know whether this server is
authorized before starting it. `credentials.resolved` is the field to
branch on, `credentials.signed_in` distinguishes "no profile yet" from
"profile but no token", `schema_version` changes only when a field is
removed or its meaning changes, and the account is masked to its domain
exactly as the text output masks it. A label in the human output is free
to be reworded in any release; the object is not.

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

## Connect a client

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
[`docs/configuration.md`](docs/configuration.md). The four worth
knowing now: `GCM_READ_ONLY=true` leaves out every tool that changes anything in Chat,
`GCM_ENABLE_DESTRUCTIVE=true` adds the tools that cannot be taken back,
`GCM_PROFILE` lets one machine hold a work account and a personal one,
and `GCM_LOCAL_DIR` names the one directory files may be written to and
read from. Without that last one the server touches no files at all,
and the three tools that move them say so.

## Tools

| Tool | What it does | Scope |
|---|---|---|
| `whoami` | Which account the stored credentials belong to | `openid email profile` |
| `list_spaces` | Spaces, group chats and direct messages you are in | `chat.spaces.readonly` |
| `get_space` | One space by resource name | `chat.spaces.readonly` |
| `search_spaces` | Named spaces you are in, by display name | `chat.spaces.readonly` |
| `find_group_chats` | The group chats holding exactly you and the people you name | `chat.memberships.readonly`, `chat.spaces.readonly` |
| `find_direct_message` | The direct message with one person, created if there is none yet | `chat.spaces.readonly`, `chat.spaces.create` |
| `get_messages` | Recent messages in a space, newest first, senders resolved to names | `chat.messages.readonly` |
| `get_message` | One message, with its reaction counts, what its text links to and what it quotes | `chat.messages.readonly` |
| `get_thread` | Every message in one thread, oldest first | `chat.messages.readonly` |
| `download_attachment` | Save a message's attachment into the server's local directory | `chat.messages.readonly` |
| `search_messages` | Google's search across every space you can see, or a regular-expression scan of one | `chat.messages.readonly`; `chat.users.readstate.readonly` for `unread_only`; `chat.spaces.readonly` for `space_type` and `space_name` |
| `search_people` | Turn a name into an email address, from the directory and your contacts | `directory.readonly`, `contacts.readonly` |
| `find_group` | Turn a Google Group's email address into the id `add_member` takes | `cloud-identity.groups.readonly` |
| `list_members` | Who is in a space, people and groups resolved to names and addresses, and on request who is in each group | `chat.memberships.readonly`, `directory.readonly`, `cloud-identity.groups.readonly` |
| `get_member` | One membership: who or what it is, their role, whether they have joined | `chat.memberships.readonly`, `directory.readonly`, `cloud-identity.groups.readonly` |
| `update_member_role` | Make a manager an ordinary member again | `chat.memberships` |
| `promote_member` | Make someone a manager or an assistant manager, with `GCM_ENABLE_DESTRUCTIVE` | `chat.memberships` |
| `list_reactions` | Reactions on a message | `chat.messages.readonly` |
| `list_pinned_messages` | What a space has pinned | `chat.spaces.pins.readonly` |
| `pin_message` | Pin a message, which everyone in the space sees | `chat.spaces.pins` |
| `unpin_message` | Remove a pin | `chat.spaces.pins` |
| `get_space_read_state` | How far you have read in a space | `chat.users.readstate.readonly` |
| `get_thread_read_state` | How far you have read in a thread | `chat.users.readstate.readonly` |
| `mark_space_read` | Clear a space's unread badge, for you only | `chat.users.readstate` |
| `mark_space_unread` | Rewind your read mark, for you only | `chat.users.readstate` |
| `list_space_events` | What changed in a space over the last 28 days | the read scope of each event type asked for |
| `get_space_event` | One of those changes by resource name | `chat.spaces.readonly` |
| `get_space_notification_setting` | What Chat tells you about a space; not under `GCM_READ_ONLY` | `chat.users.spacesettings` |
| `update_space_notification_setting` | Change it, or mute the space | `chat.users.spacesettings` |
| `get_availability` | Your own presence and custom status | `chat.users.availability.readonly` |
| `set_availability` | Active, away, or do not disturb until a time | `chat.users.availability` |
| `set_custom_status` | Set or clear the text and emoji beside your name | `chat.users.availability` |
| `list_custom_emojis` | Your organization's own emoji | `chat.customemojis.readonly` |
| `get_custom_emoji` | One of them by resource name | `chat.customemojis.readonly` |
| `create_custom_emoji` | Add one from a local image | `chat.customemojis` |
| `delete_custom_emoji` | Remove one, for everyone, with `GCM_ENABLE_DESTRUCTIVE` | `chat.customemojis` |
| `delete_space` | Delete a space and everything in it, with `GCM_ENABLE_DESTRUCTIVE` | `chat.delete` |
| `list_sections` | Your own sidebar sections | `chat.users.sections.readonly` |
| `list_section_items` | What a section holds, or which section a space sits in | `chat.users.sections.readonly` |
| `send_message` | Post text, exactly as given, in Chat's syntax or Markdown. Optionally into a thread, quoting a message, or carrying an uploaded file | `chat.messages.create`; `chat.messages.readonly` to quote |
| `upload_attachment` | Send a local file to a space and get the token that attaches it | `chat.messages.create` |
| `update_message` | Edit the text of a message you sent | `chat.messages` |
| `delete_message` | Delete one message, with `GCM_ENABLE_DESTRUCTIVE`. Already gone counts as success | `chat.messages` |
| `delete_thread` | Delete a thread's first message and every reply, with `GCM_ENABLE_DESTRUCTIVE` | `chat.messages` |
| `add_reaction` | React to a message with a Unicode emoji | `chat.messages.reactions` |
| `remove_reaction` | Remove a reaction, by resource name or by message, emoji and person | `chat.messages.reactions` |
| `create_group_chat` | Start an unnamed group chat with 2 to 20 people | `chat.spaces.create` |
| `create_space` | Create a private named space with up to 20 people | `chat.spaces.create` |
| `update_space` | Rename a space, change its description, or make it private | `chat.spaces` |
| `set_space_audience` | Open a space to a target audience, with `GCM_ENABLE_DESTRUCTIVE` | `chat.spaces` |
| `add_member` | Invite someone in your organization to a space by email, or add a Google Group to a space that admits nobody from outside | `chat.memberships`; `chat.spaces.readonly`, `directory.readonly` to check the address |
| `add_external_member` | Invite someone from outside your organization, or add a Google Group where outsiders are admitted, with `GCM_ENABLE_DESTRUCTIVE` | `chat.memberships`; `cloud-identity.groups.readonly` to check a group's address |
| `remove_member` | Remove a membership, with `GCM_ENABLE_DESTRUCTIVE`. Already gone counts as success | `chat.memberships` |
| `create_section` | Add a sidebar section | `chat.users.sections` |
| `rename_section` | Rename a sidebar section | `chat.users.sections` |
| `delete_section` | Delete a sidebar section, with `GCM_ENABLE_DESTRUCTIVE`; its spaces fall back to the defaults | `chat.users.sections` |
| `position_section` | Reorder a section, by absolute rank or by moving it to the start or end | `chat.users.sections` |
| `move_space_to_section` | File a space under a section. A space already there is left alone | `chat.users.sections` |

Sections are your own sidebar. Nothing there changes a space, and nobody
else sees it.

The `admin` toolset is for a Workspace administrator, and is off unless
`GCM_TOOLSETS` names it, as in `all,admin`. Its tools act on any named
space in the organization, spaces you are not in included, with your
administrator privileges. None of them reads messages: Google offers no
administrator access to those.
[`docs/gcp-setup.md`](docs/gcp-setup.md) lists the scopes it adds.

| Tool | What it does | Scope |
|---|---|---|
| `admin_search_spaces` | Every named space in the organization, by name, age, activity or history, each with its description | `chat.admin.spaces.readonly` |
| `admin_get_space` | Any space by resource name, its permission settings included | `chat.admin.spaces.readonly` |
| `admin_list_members` | Who is in any space, resolved as `list_members` resolves them | `chat.admin.memberships.readonly`, `directory.readonly`, `cloud-identity.groups.readonly` |
| `admin_get_member` | One membership of any space | `chat.admin.memberships.readonly`, `directory.readonly`, `cloud-identity.groups.readonly` |
| `admin_update_space` | Rename any named space, or change its description or guidelines, and say what each was before | `chat.admin.spaces` |
| `admin_update_space_permissions` | Change who besides the managers may manage members, edit details, turn history on or off, use @all, manage apps and webhooks, and reply, with `GCM_ENABLE_DESTRUCTIVE` | `chat.admin.spaces` |
| `admin_update_member_role` | Make a manager of any space an ordinary member again | `chat.admin.memberships` |
| `admin_promote_member` | Make someone a manager of any space, with `GCM_ENABLE_DESTRUCTIVE` | `chat.admin.memberships` |
| `admin_add_member` | Add someone in your organization to any named space, never a Google Group and never yourself, with `GCM_ENABLE_DESTRUCTIVE` | `chat.admin.memberships`; `chat.admin.spaces.readonly`, `directory.readonly` to check the space and the address |
| `admin_remove_member` | Remove a membership of any space, with `GCM_ENABLE_DESTRUCTIVE`. Already gone counts as success | `chat.admin.memberships` |
| `admin_delete_space` | Delete any named space and everything in it, with `GCM_ENABLE_DESTRUCTIVE` | `chat.admin.delete`; `chat.admin.spaces.readonly` to check the name |

Three resources carry the same content as the matching tools, for a
client that includes resources in its context:

- `gchat://spaces/{space_id}`
- `gchat://spaces/{space_id}/messages/{message_id}`
- `gchat://spaces/{space_id}/threads/{thread_id}`

## Safety

- **`dry_run` on 36 write tools.** It returns the request body that
  would have been sent, and the call cannot write: the flag puts the
  request on a context the HTTP client refuses to write under, so a
  tool that forgot its own preview branch fails loudly instead of
  posting. A preview may still read what it shows or checks, such as the
  quoted message a quote names or the text a delete repeats.
- **`GCM_READ_ONLY=true` leaves the write tools unregistered.** A tool
  that is not registered cannot be called, whatever permission mode the
  client is in or whatever a model asks for. It is set where you start
  the server, so it binds the whole session rather than one call. A
  `google-chat-mcp login` run with it set asks for read-only scopes, so
  that token cannot write either. A token from an earlier login keeps
  its write scopes until you log in again, and turning writes back on
  takes another login.
- **What cannot be taken back is not registered unless you say so.**
  The deletes, `delete_thread`, `set_space_audience`, `promote_member`
  and `add_external_member` exist only with `GCM_ENABLE_DESTRUCTIVE=true`,
  and so do the admin tools that delete, promote, add someone or change
  a space's permissions. Each call then says `confirm: true`.
  `delete_message`, `delete_thread`, `delete_space`, `admin_delete_space`,
  `delete_custom_emoji` and `set_space_audience` also repeat what they act
  on: how the message begins, or the space's or the emoji's name. The
  server reads the target first and refuses `[stale]` when it differs, so
  the call your client shows you says which message or space goes.
- **Your client approves each call; the server asks you nothing.**
  Claude Code asks for the tools you list under `permissions.ask`.
  claude.ai and Codex prompt for a tool marked `destructiveHint`, which
  every tool in the list above that needs `GCM_ENABLE_DESTRUCTIVE`
  carries, and so does every write that replaces or removes something,
  such as `update_message`, `update_space` or `remove_reaction`. VS
  Code prompts for every tool that changes anything,
  `download_attachment` included. In Claude Code, put the writes that
  post, invite, share or delete under `permissions.ask` in your settings,
  with the name you gave the server in place of `google-chat`:

  ```json
  {
    "permissions": {
      "ask": [
        "mcp__google-chat__send_message", "mcp__google-chat__update_message",
        "mcp__google-chat__upload_attachment",
        "mcp__google-chat__create_space", "mcp__google-chat__create_group_chat",
        "mcp__google-chat__find_direct_message",
        "mcp__google-chat__add_member", "mcp__google-chat__add_external_member",
        "mcp__google-chat__update_member_role", "mcp__google-chat__promote_member",
        "mcp__google-chat__remove_member", "mcp__google-chat__update_space",
        "mcp__google-chat__pin_message", "mcp__google-chat__create_custom_emoji",
        "mcp__google-chat__delete_message", "mcp__google-chat__delete_thread",
        "mcp__google-chat__delete_section",
        "mcp__google-chat__admin_update_space",
        "mcp__google-chat__admin_update_space_permissions",
        "mcp__google-chat__admin_update_member_role",
        "mcp__google-chat__admin_promote_member",
        "mcp__google-chat__admin_add_member",
        "mcp__google-chat__admin_remove_member"
      ]
    }
  }
  ```

  `delete_space`, `admin_delete_space`, `set_space_audience` and
  `delete_custom_emoji` need no rule: they carry Claude Code's
  `requiresUserInteraction` mark, so Claude Code 2.1.199 or later asks
  on every call in every interactive mode, and refuses them where nobody
  can be asked.
- **Nothing reaches the whole space or past it by default.** A post or
  an edit that mentions everyone, `<users/all>`, is refused. A space is
  created private, and `update_space` does not open one. In a space that
  admits people from outside your organization, `add_member` refuses an
  address your organization's directory does not know and every Google
  Group, and `update_member_role` makes nobody a manager: those are
  `add_external_member` and `promote_member`.
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

Every space, message and thread is addressed by its resource name, never
by position, and a listing keeps paging while Google returns a token —
including past an empty page, which Chat returns when it applies the page
size before the filter.

## Getting help

If something does not work, run `google-chat-mcp doctor`. It checks the
credentials, the granted scopes and what Google actually answers, and
names what is missing — most first-run trouble is an API that was never
enabled or a consent screen without you on it.

If that does not explain it,
[open an issue](https://github.com/mmedum/google-chat-mcp/issues). Never
paste a space or message resource name, message text, a
`client_secret.json` or a token into one; describe the shape instead.
Security problems go through [`SECURITY.md`](SECURITY.md), privately.

## Versioning

Tool names and their output fields, and the resource templates, are
stable within a major version. A change needing you to act — a new
scope, another login, a different command in your client config — is
marked **Breaking:** in
[`CHANGELOG.md`](CHANGELOG.md), which is also what each release's notes
are made from.

## Development

```bash
make build     # the binary
make test      # race detector, coverage floor
make check     # everything CI runs
```

`make check` is the definition of done: formatting, `go vet`,
golangci-lint, race tests with a per-package coverage floor,
`govulncheck`, a license check, a stdio smoke test, a schema diff against
the released tool surface, an API-coverage gate that fails when a Google
API method has no verdict on it or a call this server makes has no row,
and a staleness gate that fails when this README, the docs or the
changelog drift from the code.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — the request flow, the
  package layout, the evidence log behind the conventions, and the
  design decisions a contributor should not undo.
- [`docs/configuration.md`](docs/configuration.md) — every environment
  variable.
- [`docs/gcp-setup.md`](docs/gcp-setup.md) — the Google Cloud project and
  OAuth client, step by step.
- [`docs/security.md`](docs/security.md) — the threat model.
- [`docs/runbook.md`](docs/runbook.md) — rotating or recovering
  credentials.

## Contributing

Questions and bugs go in
[issues](https://github.com/mmedum/google-chat-mcp/issues); pull requests
are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) covers the branch and
review flow, and `make check` is what has to pass.

## Security

[`SECURITY.md`](SECURITY.md) says how to report a vulnerability.

## Code of conduct

[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) — Contributor Covenant 3.0.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
