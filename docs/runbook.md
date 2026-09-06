# Runbook

What to do when something does not work. Start with `doctor`; most
first-run reports are a missing API or a scope that was never granted,
and it names both.

```bash
google-chat-mcp doctor
```

It says which account is signed in, where the refresh token came from,
and what Google actually answered — it lists spaces, reads a sample of
messages, members and sections, and reports anything it could not
parse. Nothing it prints is committed anywhere; it is for you.

## A tool answers `[scope] …`

The message names the exact scope URL and the command that fixes it.
Google's own 403 does not, which is why this server rewrites it.

Two things have to line up. The scope must be on your OAuth consent
screen — see [`gcp-setup.md`](gcp-setup.md) — and you must have granted
it. If a scope is missing from the consent screen, adding it to the
request changes nothing: the grant comes back without it and the 403
repeats.

```bash
google-chat-mcp login --client-secret ./client_secret.json
```

Google's granular consent lets you tick scopes individually at grant
time, so a partial grant is a normal state, not a broken one. The tools
you did not grant for return this error; the rest keep working.

Sections are their own grant. `chat.users.sections` is not implied by
`chat.spaces` — a sidebar section is per-user client state, not space
data — so the section tools can fail while everything else works.

## `[auth] no refresh token found`

Nothing is stored for this profile. Run `login`. If you expected a token
to be there, check you are on the profile you think:
`google-chat-mcp status` prints the profile, the account and where the
token lives.

## The token is in a file, not the keyring

`login` says so when it happens, and `status` and `doctor` report the
source. It means the OS keyring could not be reached — no Secret Service
on a headless Linux box is the usual reason. The file is `0600` in a
`0700` directory and works fine; it is just less protected than a
keyring entry that only unlocks with your login session.

To move it into a keyring once one is available, log in again.

## Rotating the OAuth client secret

Create a new client secret in Google Cloud, then:

```bash
google-chat-mcp logout
google-chat-mcp login --client-secret ./new_client_secret.json
```

Delete the old secret in Google Cloud afterwards, not before —
`logout` needs the old one to revoke.

## Revoking access

```bash
google-chat-mcp logout
```

Revokes the refresh token at Google, then deletes the local copy. The
order matters: deleting a local copy of a token that still works is not
a logout.

You can also revoke from
[your Google account's third-party access page](https://myaccount.google.com/permissions),
which is what to use if you have lost the machine. Do both if the
machine is out of your hands: the account page revokes the grant, and
the local file stays until the disk is wiped.

## Two accounts on one machine

Profiles keep separate client secrets, accounts and tokens.

```bash
google-chat-mcp --profile work login --client-secret ./work_client_secret.json
google-chat-mcp --profile personal login --client-secret ./personal_client_secret.json
```

Then give each client entry its own `GCM_PROFILE`. See
[`configuration.md`](configuration.md).

## Google changed a Chat API response

Symptom: `doctor` reports drift paths, or `schema_drift` warnings appear
on stderr.

This is not an outage. Unknown fields are counted and logged by name,
never rejected, exactly so that an upstream addition cannot take the
server down. Drift is worth acting on when a field this server *does*
read changes shape — then a listing starts dropping rows, and `doctor`'s
"dropped" count is the number that says so.

If rows are being dropped, open an issue with the drift paths and the
dropped count. Do not paste message content.

## Names and email addresses come back null

`get_messages`, `get_thread`, `get_message` and `list_members` resolve
people through the People API, and that lookup can come back empty
without failing.

- **Someone outside your directory.** An external or guest user has no
  entry the People API will return to you. This is permanent, not a
  misconfiguration.
- **`directory.readonly` not granted.** Same-domain colleagues resolve
  once it is; without it, close to nothing does.
- **The lookup failed.** A 403, 429 or 5xx from the People API costs the
  email field and nothing else — the row still arrives, with the user id
  and whatever name Chat itself supplied, and a `person_lookup_degraded`
  warning goes to stderr with the upstream status so a scope problem can
  be told from a quota one. Nothing is suppressed: the next call tries
  again.

The fix, when the value matters, is `search_people`. It uses the
directory and contacts search endpoints, which return populated names
and addresses where the per-person lookup does not, and it writes what
it learns into the cache, so the next read resolves the same person
without another round trip.

Treat a null email as "not resolved", never as "this person has no
email".

## `search_people` finds nobody in your own Workspace

Symptom: an empty result, or only `CONTACTS` hits, for someone plainly
in your organization. The stderr line usually carries Google's reason:
*the G Suite domain admin has disabled external directory sharing*.

Two separate admin settings gate this, and they are easy to confuse.
Contact sharing inside the directory controls whether people can see
each other. External directory sharing controls whether a third-party
OAuth app — which is what your own Cloud project's client is, from
Google's point of view — may read directory data on someone's behalf.
The default, "authenticated user basic profile fields", lets the app
read only the caller's own profile, which shows up as zero hits.

Fixes, cleanest first:

1. **Allow-list your client.** Admin console → Security → API controls →
   App access control → Contacts API → "Restricted but trust this
   specific app", and paste the OAuth client id. Only your server gets
   directory access.
2. **Widen external directory sharing.** Admin console → Apps → Google
   Workspace → Directory → Directory settings → External directory
   sharing. Broader: it opens directory reads to any OAuth app that has
   been granted `directory.readonly`.
3. **Live with it.** `CONTACTS` still covers people you have
   corresponded with, including contacts Google fills in from Chat.

Changes take a few minutes to propagate. Check `sources_succeeded` on a
fresh `search_people` result to confirm.

If you are not an administrator, pass `sources: ["CONTACTS"]` and accept
narrower coverage.

## `search_people` on a consumer Gmail account

There is no Workspace directory, so the directory sources are refused
and only `CONTACTS` answers. `sources_succeeded` says so. There is no
admin setting to change; an address for someone you have never
corresponded with has to be pasted in by hand.

## Do the restricted scopes mean a security assessment?

Only if you publish externally. Two of the scopes are in Google's
restricted tier: `chat.messages`, which `update_message` and
`delete_message` need, and `chat.spaces`, which `update_space` needs.
Google lists only the umbrella for `spaces.patch` under user
authentication, so the granular scopes do not cover it.

- **Internal app**, visible only inside your own Workspace: nothing.
  No verification, no assessment. Declare the scopes and run it.
- **External app in testing**, up to 100 test users: nothing, beyond an
  unverified-app warning your testers click through.
- **External app in production**: an annual third-party Cloud
  Application Security Assessment on top of the usual sensitive-tier
  verification. It costs real money each year and takes weeks. One
  assessment covers every restricted scope on that project, so a second
  one does not double it.

The section scopes are sensitive tier, not restricted: a few days of
self-service verification for an external app, and nothing for an
internal one.

Since almost everyone running this creates an internal app for
themselves, this usually costs nothing at all.

## `add_member` succeeds on someone already in the space

Google's `spaces.members.create` returns 200 with the existing
membership rather than a conflict, most of the time. The conflict is
documented and does happen, so the server handles both, but a
successful `add_member` means "they are in the space now", not "they
were not before".

## `remove_reaction` reports nothing removed, but the reaction is there

The by-triple form — message, emoji, person — has to resolve each
reactor's address to match yours against it, and an address that does
not resolve is skipped. See the null-address section above.

Use the direct form instead: take `reaction_name` from `list_reactions`
and pass that.

## A repeat `delete_message` looks like a permission problem

It should not, any more. Google answers a read of a deleted message with
a 200 tombstone rather than a 404, so a naive "is it gone?" check reads
an ordinary repeat delete as a refusal. The server checks for the
tombstone.

If you do see a delete refused: a 403 that is not about scopes means you
may not delete that message. You can only delete your own.

## Reporting a problem

Include `google-chat-mcp doctor`'s output, the version
(`google-chat-mcp --version`), how you installed it, and which client
you are using. Turn the log level up first if the problem is a failing
call:

```bash
GCM_LOG_LEVEL=debug GCM_LOG_FORMAT=text google-chat-mcp doctor
```

The logs carry no message content, addresses or search terms by design,
so a debug log is safe to paste. Space and message ids are in there, so
redact those if they identify something you would rather not share.
