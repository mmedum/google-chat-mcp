# Security model

The threat model and the invariants the code holds: the reference for "is
that safe?", in review or before you run this.
[`SECURITY.md`](../SECURITY.md) says how to report a vulnerability.

It applies to one binary, running as a subprocess of your MCP client, on
your machine, with your own OAuth credentials. There is no network
listener except while `login` waits for the browser, no shared
deployment, and nobody else's tokens.

## Trust boundaries

| Boundary | Crosses | Assumption |
|---|---|---|
| MCP client ↔ this server | An OS pipe | Both run as the same OS user, with no authentication between them. None would mean anything: a process that can write to the server's stdin can already read its memory. |
| This server ↔ Google | TLS | Go's `crypto/tls` verifies every call against the system roots, the OAuth token endpoint included. Nothing in the tree skips verification. |
| This server ↔ disk | The file system | A `0700` directory of `0600` files under your user config directory. It holds the refresh token only when the OS keyring was unavailable, and the cache of names and addresses until `logout` removes it. **On Windows the mode is not the mechanism**: the ACL the directory inherits from your profile is. Nothing catches a `GCM_CONFIG_DIR` redirected somewhere world-readable. |
| This server ↔ your files | The file system | Only the one directory `GCM_LOCAL_DIR` names, for the file-transfer tools. Unset, the default, they refuse. |
| Browser ↔ the login listener | `127.0.0.1` on a random port | Alive only while `login` waits. PKCE and state are enforced on the callback. The kernel binds the socket to this process, so a co-resident process cannot take it. |

## What is worth stealing

- **The refresh token.** Long-lived, and it carries every scope you
  granted. It lives in the OS keyring where there is one, and in a
  `0600` file where there is not.
- **The access token in flight.** Short-lived, with the same scopes.
- **Message content and member addresses.** They pass through tool
  results on their way to the client, which is the point of the server.
- **Your OAuth client secret.** Google treats a Desktop client's secret
  as public, which is why the loopback flow uses PKCE. It is still yours.
  The file is read where you keep it, never copied.

## Adversaries

**A malicious MCP client, or a model driving an honest one.** It can call
any registered tool under your credentials. Whatever the client approves,
the server refuses a post or an edit that mentions everyone. Opening a
space to an audience, making a manager and admitting someone from outside
the organization each have a tool of their own. Those tools and every
delete are registered only with `GCM_ENABLE_DESTRUCTIVE=true`, and each
call says `confirm: true`.

The space, message and emoji deletes and `set_space_audience` also repeat
what they act on, and are refused when it differs. The `admin` toolset
reaches every named space in the organization. Its deletes and the tools
that widen access need `GCM_ENABLE_DESTRUCTIVE=true` too, and
`admin_add_member` never adds a Google Group or your own account. Past
that, Google's per-scope authorization is the bound, and `GCM_READ_ONLY`
is yours.

**A co-resident process on your machine.** It cannot take the login
socket. `GCM_CONFIG_DIR` refuses a path outside your home directory unless
`GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME` says you meant it. This server creates
that directory `0700`, so a mistyped value would re-permission something
else.

**A malicious or merely surprising Google response.** Every value read is
bounded at the decode boundary. Unknown fields are counted and logged by
name, never rejected, because rejecting them turns every upstream
addition into an outage (D-08).

**Prompt injection from chat content.** Message bodies reach the client
verbatim, and this server cannot tell an instruction from a sentence.
Know that before pointing an unattended agent at a space strangers can
post to.

## Invariants the code holds

A test or a gate holds each of these. A way to break one is a
vulnerability.

- **Stdout carries only JSON-RPC.** Logs go to stderr, and the smoke test
  fails on any other line (D-02).
- **Logs never carry a payload.** Message text, addresses, display names,
  tokens and search terms stay out at every level, a transport error's
  URL included. `TestLogsNeverCarryThePayload` fails the build on one
  (D-03).
- **A dry run cannot write.** The flag puts the request on a context the
  HTTP client refuses to write under. A preview may read what it shows
  (D-09).
- **Resource names are checked before they are sent.** Several Chat calls
  put a name inside a filter expression, where an unchecked quote would
  end the clause early. An emoji argument refuses quotes, backslashes and
  whitespace for the same reason.
- **The config directory override refuses a path outside your home**
  unless the opt-in is set.
- **The token file is written only when the keyring was unavailable.**
  `login` says so when it happens, and `status` reports which source
  answered.
- **`logout` revokes at Google before deleting locally.** A deleted local
  copy of a token that still works is not a logout.
- **An umbrella scope satisfies the scopes split out of it**, as Google
  accepts it, so the server does not refuse a call Google allows (D-37).
- **Only 2xx is success.** A 3xx is an error, not an empty result.
- **An access token goes only to the hosts this server was configured
  with.** The check runs before the Authorization header goes on, and
  compares the scheme, the normalized hostname and the port. No URL from a
  response body is followed: an attachment's `downloadUri` is on a host
  the allowlist refuses, as Google's reference advises.
- **Local files are reached through one object.** `localFiles` resolves
  the symlinks and refuses anything outside `GCM_LOCAL_DIR`. A download
  never overwrites: a taken name gains a counter.

## Not a vulnerability

Do not report these as vulnerabilities. Open an ordinary issue if one
surprises you.

- **What runs as you can act as you.** A compromised host or user
  account, or any process running as you, can read the token and drive
  the server. The design assumes the process owner is you.
- **Your own OAuth client, Cloud project and token file.** Protecting
  them is the job of the person who runs this server.
  [`docs/troubleshooting.md`](troubleshooting.md#tasks) says how to rotate the
  secret and revoke access.
- **A tool doing what its registration allows.** A client or a model
  calling a registered tool under your credentials is the design. The
  controls are `GCM_READ_ONLY`, `GCM_ENABLE_DESTRUCTIVE` and
  `GCM_TOOLSETS`.
- **A model following instructions in a message.** That is the client's
  concern. A message that makes the server itself break an invariant is a
  vulnerability.
- **Denial of service.** The rate limiters keep the server inside
  Google's quotas. They do not resist an attacker who already runs code
  as you.
- **Your Workspace administrator reading your messages.** They can, with
  or without this server.
- **Someone else's code.** A flaw in Google's OAuth service or APIs goes
  to Google, and one in another dependency, such as the MCP Go SDK, to
  that project. We will help route it.

## Dependency vulnerabilities

`govulncheck` runs in `make check` and in CI, on every pull request and
every push to `main`. It fails the build when this server's code can
reach a vulnerable symbol.

A CVE in a dependency is a vulnerability here only when `govulncheck`
reports it reachable. It is then fixed in a release whose CHANGELOG entry
names the CVE or GHSA. Otherwise the dependency is updated in the normal
course, and a report about it is not a vulnerability.

## What you are responsible for

- **Your own Google app.** Each person creates their own OAuth client, so
  nobody else's rollout can revoke your access. Publishing it outside
  your Workspace brings Google's verification, as
  [`docs/troubleshooting.md`](troubleshooting.md#do-the-restricted-scopes-mean-a-security-assessment)
  explains.
- **The scopes you grant.** A tool whose scope you declined returns a
  `[scope]` error naming it. That is a supported state, not a broken one.
- **Where you point an unattended agent.** `GCM_READ_ONLY=true`, and
  leaving `GCM_ENABLE_DESTRUCTIVE` and the `admin` toolset off, are the
  controls a client cannot override. A `login` run under `GCM_READ_ONLY`
  gets a token that cannot write either.
- **Keeping the client secret file out of version control.** This server
  reads it where you put it and never copies it.
