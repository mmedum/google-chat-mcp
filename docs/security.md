# Security model

The threat model and the invariants the code holds. This is the
reference for "is that safe?", whether it comes up in review or from
someone deciding whether to run this.

The shape it applies to: one binary, running as a subprocess of your MCP
client, on your machine, holding your own OAuth credentials. There is no
network listener except for a few seconds during `login`, no shared
deployment, and nobody else's tokens.

## Trust boundaries

| Boundary | Crosses | Assumption |
|---|---|---|
| MCP client ↔ this server | An OS pipe | The client and the server run as the same OS user. There is no authentication between them, and none would mean anything: a process that can write to the server's stdin can already read its memory. |
| This server ↔ Google | TLS | One trust store. Go's `crypto/tls` verifies against the system roots for every call — Chat, People, OIDC, and the OAuth token endpoint alike. There is no way to skip verification anywhere in the tree. |
| This server ↔ disk | The file system | A `0700` directory holding `0600` files under your user config directory. The refresh token is there only when the OS keyring was unavailable. |
| This server ↔ your files | The file system | Nothing, unless you set `GCM_LOCAL_DIR`. That names one directory, and it is the only place the file-transfer tools read from and write to. Unset, which is the default, they refuse. |
| Browser ↔ the login listener | `127.0.0.1` on a random port | Alive only while `login` waits. PKCE and state are enforced on the callback. A co-resident process cannot take the socket, because the kernel binds it to this process. |

## What is worth stealing

- **The refresh token.** Long-lived, and it carries every scope you
  granted. It lives in the OS keyring where there is one, and in a
  `0600` file where there is not.
- **The access token in flight.** Short-lived, same scope union.
- **Message content and member addresses.** They pass through tool
  results on their way to the client, which is the point of the server.
- **Your OAuth client secret.** For a Desktop client Google treats it as
  public, which is why the loopback flow uses PKCE; it is still yours and
  the file is read, never copied into the config directory.

## Adversaries

**A malicious MCP client, or a model driving an honest one.** It can
call any tool, under your valid credentials. The server does not police
what the client asks for — Google's per-scope authorization is the
bound, and `GCM_READ_ONLY` is yours. This is the trust model, not a gap
in it: a client that can start the server can also read its token.

**A co-resident process on your machine.** It cannot take the login
socket. It can set environment variables, which is why the two that
would matter are gated: `GCM_CONFIG_DIR` refuses a path outside your
home directory unless `GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME` says you meant
it, because this server creates that directory `0700` and a mistyped
value would re-permission something else. A process that can already set
your environment has better options than either.

**A malicious or merely surprising Google response.** Every value read
is bounded at the decode boundary, and unknown fields are counted and
logged by name rather than rejected — see "drift is observable, never
fatal" in [`architecture.md`](architecture.md). Rejecting them has cost
this server two total outages.

**Prompt injection from chat content.** Message bodies reach the client
verbatim, because a chat server that edited them would be useless. A
model that follows instructions it read in a message is a client-side
concern; this server cannot tell an instruction from a sentence. Worth
knowing before pointing an unattended agent at a space that strangers
can post to.

## Out of scope

- Compromise of the host or of your user account. The whole design
  assumes the process owner is you.
- Compromise of Google's OAuth service.
- Denial of service. The rate limiters exist to stay inside Google's
  quotas, not to resist an attacker who already runs code as you.
- Whether your own Workspace administrator can read your messages. They
  can, with or without this.

## Invariants the code holds

Each of these is enforced, not documented — the tests named in
`internal/` pin them.

1. **Stdout carries only JSON-RPC.** Logs go to stderr. The smoke test
   fails on any other line.
2. **Logs never carry a payload.** Message text, addresses, display
   names, tokens and search terms stay out at every level, including
   inside a transport error's URL.
   `TestLogsNeverCarryThePayload` fails the build on one.
3. **A dry run cannot reach the network.** The flag puts the request on
   a context the HTTP client refuses to write under, so the guarantee
   does not depend on each handler remembering.
4. **Resource names are checked before they are sent.** Several Chat
   calls put a name inside a filter expression, where an unchecked quote
   would end the clause early. Nothing reachable that way crosses a
   space or a user, because the parent is fixed in the request path, but
   a filter the caller wrote is not one this server should send.
5. **An emoji argument refuses quotes, backslashes and whitespace.**
   Same reason: `remove_reaction` looks one up with
   `emoji.unicode = "…"`.
6. **The config directory override refuses a path outside your home**
   unless the opt-in is set.
7. **The token file is `0600` in a `0700` directory**, and is written
   only when the keyring was unavailable. `login` says so when that
   happens, and `doctor` reports which source answered. On Windows those
   bits are not a permission: the file inherits the access control list
   of your user profile instead, which keeps other standard users out but
   is not the same guarantee. Windows Credential Manager is the keyring
   there, so the file is the fallback on that platform too.
8. **`logout` revokes at Google before deleting locally.** A deleted
   local copy of a token that still works is not a logout.
9. **An umbrella scope satisfies the scopes split out of it**, matching
   what Google accepts. Without that table the server would refuse calls
   Google allows, for exactly the people who paid the most for consent.
10. **Only 2xx is success.** A 3xx is an error, not an empty result.
11. **An access token goes only to the hosts this server was configured
    with.** The check runs where the request is built, before the
    Authorization header goes on, and it compares the scheme, the
    hostname lowercased with a trailing dot trimmed, and the port —
    kept, not stripped. Google puts URLs in response bodies, and none of
    them is ever followed: an attachment's `downloadUri` is on a host
    this allowlist refuses, which is what Google's own reference says to
    do with it.
12. **Local files are reached through one object.** Every read and write
    the tools make to your disk goes through `localFiles`, which
    resolves the symlinks and then refuses anything outside
    `GCM_LOCAL_DIR`. A download never overwrites: a name already taken
    gains a counter. Unset means no file transfer at all, and
    `download_attachment`, `upload_attachment` and `create_custom_emoji`
    say so rather than falling back to anywhere else.

The gate has one limit worth stating plainly: it matches shapes, and
the payload has none. A credential or an id can be recognised; a
sentence out of a real conversation cannot be told from invented prose
by any pattern. So the rule that keeps conversations out of this
repository is a habit rather than a check — fixtures are generated and
never recorded, and a live run reads back only what it wrote.

## What you are responsible for

- **Your own Google app.** Each person running this creates their own
  OAuth client. Nothing is centralized, so nothing about your project
  reaches anyone else — and nobody else's rollout can revoke your
  access.
- **The scopes you grant.** The consent screen lists them individually.
  Granting fewer means some tools return a `[scope]` error naming what
  is missing; that is a supported state, not a broken one.
- **Where you point an unattended agent.** `GCM_READ_ONLY=true` is the
  control that a client cannot override.
- **Keeping the client secret file out of version control.** It is
  yours, it is read from wherever you put it, and this repository never
  copies it anywhere.

## Reporting

[`SECURITY.md`](../SECURITY.md) says how to report a vulnerability.
