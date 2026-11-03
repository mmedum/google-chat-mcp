# Troubleshooting

What to do when something does not work, and the maintenance tasks after
that. Start with these two commands:

```bash
google-chat-mcp status
google-chat-mcp doctor
```

`status` asks Google nothing. It prints the profile, the account, where
the token is stored, and every scope the token lacks. `doctor` calls
Google: it lists your spaces and reads a sample of messages, members and
sections. It names each check that could not run and why, counts the rows
it could not read, and lists the response fields it does not model.

## `[scope] Missing required OAuth scope: …`

**Cause:** the token lacks the scope this tool needs. The message names
the exact scope URL. Google lets you tick scopes one by one at consent, so
a partial grant is a normal state, and the tools you did grant keep
working. The sidebar section tools need `chat.users.sections`, which
`chat.spaces` does not imply, so they can fail while everything else
works.

**Fix (resolves it):** on an External app, put the scope on the consent
screen's list first, as [`gcp-setup.md`](gcp-setup.md#4-add-the-scopes)
shows. An Internal app needs no list. Then log in again and grant it:

```bash
google-chat-mcp login --client-secret ./client_secret.json
```

An app your Workspace administrator marks Trusted grants every scope or
none at login.

## `[auth] Not signed in, or the stored credentials no longer work`

The same cause shows as `[auth] Google rejected the stored credentials`,
and `status` shows `token store: none` or `not signed in`.

**Cause:** this profile has no refresh token, or Google refuses the one
it has. Google ends a refresh token when you revoke access, when it goes
unused for six months, or when an administrator restricts a scope it
carries. On an External app in testing, every refresh token expires after
seven days, as
[Google's OAuth documentation](https://developers.google.com/identity/protocols/oauth2)
says under "Refresh token expiration".

**Fix (resolves it):** check that `status` shows the profile you expect,
then run `login` again. For the seven-day expiry, logging in again is a
workaround. Moving the app out of testing resolves it, and
[`gcp-setup.md`](gcp-setup.md#verification-later-and-probably-never)
says when that needs Google's verification.

Every tool fails until you log in, and nothing else is affected.

## `keyring unavailable (…); refresh token saved in plaintext`

`login` prints this, and `status` shows `token store: file`.

**Cause:** the OS keyring could not be reached. The usual reason is a
headless Linux machine with no Secret Service running.

**Fix (resolves it):** once a keyring is available, run `login` again. The
token goes into the keyring and the file is deleted.

**Impact:** the file is `0600` in a `0700` directory under your user
config directory. That is less protection than a keyring entry, which
unlocks only with your login session. On Windows the mode is not a
permission at all: the file is protected by the access control list it
inherits from your profile. Anyone who can read the file holds every
scope you granted, until you revoke it.

## `[forbidden] Google refused this because the API it uses is not enabled`

**Cause:** an API this server calls is off on the Google Cloud project
your OAuth client belongs to. Google's own message follows the text, names
the API, and links to the page that enables it. The People API serves
`search_people` and the address lookups. The Cloud Identity API serves
`find_group` and the group names in `list_members`.

**Fix (resolves it):** enable the API, as
[`gcp-setup.md`](gcp-setup.md#2-enable-the-apis) lists, wait a minute, and
call the tool again.

Only the tools that call that API fail; the rest keep working.

## `find_group` cannot find a group

`find_group` asks Cloud Identity, which answers only for the groups the
signed-in account can see.

- **`[scope]` naming `cloud-identity.groups.readonly`.** The token
  predates the scope. Add it to the consent screen if the app is External,
  then run `login` again.
- **`[forbidden]` saying no group is visible.** No group has that address,
  or its owner keeps it visible to its own members only. Google answers
  both the same way, so check the spelling first. The group's "Who can see
  group" setting decides the second, and its owner can change it or add
  you.

`list_members` names groups through the same lookup. Without it, a group
row still arrives with only `groups/{id}`, and a `group_lookup_degraded`
warning goes to stderr.

## `doctor` lists unknown response fields, or `schema_drift` appears on stderr

**Cause:** Google added response fields that this server does not model.
Unknown fields are counted and logged by name, never rejected, so an
upstream addition cannot take the server down.

**Fix:** none is needed while `doctor` reports no skipped rows. When it
says `row(s) could not be read and were skipped`, a field this server
reads has changed shape. Open an issue with the field paths and that
count, and paste no message content.

**Impact:** a listing with skipped rows is incomplete, and its result
reports how many rows it could not parse. Nothing is written or lost.

## Names and email addresses come back null

`get_messages`, `get_thread`, `get_message` and `list_members` take the
address Chat sends with a sender or member. When Chat sends none, the
server asks the People API, and that lookup can come back empty without
failing. Treat a null email as "not resolved", never as "has no email".

- **A Chat app, or an account that is gone.** Chat sends no address for
  these, and the People API cannot fill one in. Nothing fixes it.
- **`directory.readonly` not granted.** Same-domain colleagues resolve
  once it is; without it, close to nothing does.
- **The lookup failed.** A 403, 429 or 5xx from the People API costs only
  the email field, and a `person_lookup_degraded` warning on stderr
  carries the upstream status. The next call tries again.

**Workaround:** `search_people` searches the directory and your contacts,
which return addresses where the per-person lookup does not. What it
learns goes into the cache, so the next read resolves that person.

## `search_people` finds nobody in your own Workspace

The result is empty, or holds only `CONTACTS` hits, for someone plainly in
your organization. The stderr line usually carries Google's reason: *the
G Suite domain admin has disabled external directory sharing*.

**Cause:** external directory sharing decides whether a third-party OAuth
app, your own client included, may read directory data for you. Its
default lets the app read only your own profile, which shows as zero
hits. Contact sharing is a separate setting.

**Fix (resolves it), for an administrator, cleanest first:**

1. Admin console → Security → API controls → App access control →
   Contacts API → "Restricted but trust this specific app", with your
   OAuth client id. Only your client gains directory access.
2. Admin console → Apps → Google Workspace → Directory → Directory
   settings → External directory sharing. This opens directory reads to
   every OAuth app granted `directory.readonly`.

Changes take a few minutes; check `sources_succeeded` on a fresh result.

**Workaround:** pass `sources: ["CONTACTS"]`, which covers the people you
have corresponded with.

## `search_people` answers only from contacts on a personal Google account

**Cause:** a consumer Gmail account has no Workspace directory. The
directory sources are refused, only `CONTACTS` answers, and
`sources_succeeded` says so.

**Fix:** there is no setting to change. Paste in by hand the address of
someone you have never corresponded with.

## `add_member` succeeds for someone already in the space

**Cause:** Google's `spaces.members.create` usually answers 200 with the
existing membership rather than a conflict. The conflict is documented
and does happen, so the server handles both.

**Fix:** none is needed. A successful `add_member` means "they are in the
space", not "they were not before". Read `list_members` first when the
difference matters.

## `add_member` refuses a colleague with `[blocked]`

**Cause:** in a space that admits people from outside your organization,
`add_member` looks the address up in your organization's directory. It
refuses when no member there has it, or when the directory declines to
answer. A colleague misses when an administrator turned directory sharing
off, and everyone misses on a personal Google account, which has no
directory. A Google Group is always refused there, because its outside
members join with it, and so does anyone added to the group later.

**Fix:** `search_people` with `sources: ["DIRECTORY"]` shows what the
directory answers. Where the person is really outside, or the lookup
cannot see them, use `add_external_member`, which needs
`GCM_ENABLE_DESTRUCTIVE=true`. It adds a group too, given `group_email`
beside `group_name`.

**Impact:** `add_external_member` gives someone outside your organization
a place in the space, which is why it is registered only on request.

## A write refuses with `[stale]`

The message ends `so it is not the one this call meant; nothing was
changed`, or names `text_start` for a message.

**Cause:** the call repeats what it acts on, and the target says
otherwise. The deletes and `set_space_audience` repeat how the message
begins, or the space's or the emoji's name. A write to a space repeats
`space_display_name`, and `add_external_member` a group's address. The
server reads the target first and refuses when the two differ. A name
ignores case and spacing; a message's text ignores only spacing.

**Fix (resolves it):** read the target again, check it is still the one
you mean, and pass what it says.

Nothing was changed. The refusal keeps a call from acting on another
message or space than the one your client showed you.

## `remove_reaction` reports nothing removed, but the reaction is there

**Cause:** the form that takes a message, an emoji and a person has to
resolve each reactor's address to match yours. A reactor whose address
does not resolve is skipped, for the reasons in
[Names and email addresses come back null](#names-and-email-addresses-come-back-null).

**Fix (resolves it):** take `reaction_name` from `list_reactions` and pass
that on its own.

## `delete_message` refuses with `[forbidden]`

**Cause:** you may delete only your own messages, and Google refuses the
rest with a 403 that names no scope. Google answers a space that does not
exist with the same 403.

A repeat delete is not this. Google answers a read of a deleted message
with a tombstone, the server reads the message before it deletes, and a
message already gone counts as success.

**Fix:** none for someone else's message. Check the resource name if the
message should be yours.

## Tasks

### Rotating the OAuth client secret

Add a second secret to the same OAuth client in Google Cloud and download
its JSON. Then:

```bash
google-chat-mcp logout
google-chat-mcp login --client-secret ./new_client_secret.json
```

`logout` revokes the old refresh token, so no copy of it keeps working.
Delete the old secret in Google Cloud last. Until you log in with the
replacement, the server signs its token refreshes with the old one.

### Revoking access

```bash
google-chat-mcp logout
```

It asks first, unless you pass `--yes`. It revokes the refresh token at
Google, then deletes the local copy and the profile's stored state. The
order matters: deleting the local copy of a token that still works is not
a logout. When the revoke fails, `logout` warns and names
[your Google account's third-party access page](https://myaccount.google.com/permissions).
A token in `GCM_REFRESH_TOKEN` is out of its reach, and it says so.

If you have lost the machine, revoke from that page. The grant ends there,
and the local file stays until the disk is wiped.

### Two accounts on one machine

Profiles keep separate client secrets, accounts and tokens.

```bash
google-chat-mcp --profile work login --client-secret ./work_client_secret.json
google-chat-mcp --profile personal login --client-secret ./personal_client_secret.json
```

Then give each client entry its own `GCM_PROFILE`, as
[`configuration.md`](configuration.md) shows.

### Reporting a problem

Include the `status` and `doctor` output, the version
(`google-chat-mcp --version`), how you installed it, and which client you
use. Turn the log level up first if a call is failing:

```bash
GCM_LOG_LEVEL=debug GCM_LOG_FORMAT=text google-chat-mcp doctor
```

The logs carry no message content, addresses or search terms, so a debug
log is safe to paste. They do carry space and message ids, so redact any
that identify something you would rather not share.

### Do the restricted scopes mean a security assessment?

Only if you publish an External app. These scopes are in Google's
restricted tier:

- `chat.messages`, which `update_message` and `delete_message` need;
- `chat.spaces`, which `update_space` needs, because Google lists only the
  umbrella scope for `spaces.patch` under user authentication;
- `chat.delete`, which `delete_space` needs;
- `chat.admin.delete`, when the admin toolset and deletes are on.

What each kind of app costs:

- **Internal app**, visible only inside your own Workspace: nothing. No
  verification and no assessment.
- **External app in testing**, up to 100 test users: nothing beyond an
  unverified-app warning your testers click through.
- **External app in production**: an annual third-party Cloud Application
  Security Assessment on top of the sensitive-tier verification. It costs
  money every year and takes weeks. One assessment covers every
  restricted scope on the project.

The section scopes are sensitive, not restricted: a few days of
self-service verification for an External app, and nothing for an
Internal one.
