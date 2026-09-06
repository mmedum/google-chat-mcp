# Setting up Google

One time, about fifteen minutes. You do this against your own Google
Cloud project: there is no shared app, so you own the consent screen,
the client id, and what you see when you grant access.

You need a Google account. On a Workspace account your administrator has
to allow user-managed OAuth clients. A consumer account works too, with
one extra step in section 6.

## 1. Create a project

[console.cloud.google.com](https://console.cloud.google.com/) → the
project picker → **New project**. Call it whatever you like.

## 2. Enable the APIs

APIs & Services → Library. Enable both:

- **Google Chat API**
- **People API** — this is what turns a user id into a name and an email
  address in `get_messages` and `list_members`.

The OpenID userinfo endpoint behind `whoami` needs no enabling.

## 3. Configure the consent screen

APIs & Services → OAuth consent screen.

**User type.** On a Workspace account choose **Internal**: access is
limited to your own domain and Google requires no verification at all,
whatever tier the scopes are in. On a consumer account choose
**External** and add yourself as a test user in section 6; no
verification is needed while the app stays in testing.

**App information.** An app name — this is what you see on the consent
screen, so name it for yourself — a support email, and a developer
contact address. A logo is optional and only matters if you later apply
for verification.

## 4. Add the scopes

Same screen, the **Scopes** step. Click *Add or remove scopes* and paste
all of these. A scope that is not on this screen is not granted, and the
tool that needs it fails with an error naming it.

```
openid
email
profile
https://www.googleapis.com/auth/chat.messages.readonly
https://www.googleapis.com/auth/chat.messages.create
https://www.googleapis.com/auth/chat.messages.reactions
https://www.googleapis.com/auth/chat.messages
https://www.googleapis.com/auth/chat.spaces.readonly
https://www.googleapis.com/auth/chat.spaces.create
https://www.googleapis.com/auth/chat.spaces
https://www.googleapis.com/auth/chat.memberships.readonly
https://www.googleapis.com/auth/chat.memberships
https://www.googleapis.com/auth/chat.users.sections.readonly
https://www.googleapis.com/auth/chat.users.sections
https://www.googleapis.com/auth/chat.delete
https://www.googleapis.com/auth/chat.spaces.pins.readonly
https://www.googleapis.com/auth/chat.spaces.pins
https://www.googleapis.com/auth/chat.customemojis.readonly
https://www.googleapis.com/auth/chat.customemojis
https://www.googleapis.com/auth/chat.users.readstate.readonly
https://www.googleapis.com/auth/chat.users.readstate
https://www.googleapis.com/auth/chat.users.spacesettings
https://www.googleapis.com/auth/chat.users.availability.readonly
https://www.googleapis.com/auth/chat.users.availability
https://www.googleapis.com/auth/directory.readonly
https://www.googleapis.com/auth/contacts.readonly
```

You do not have to grant them all when you log in. Google's granular
consent lets you tick them individually, and a tool whose scope you
declined returns an error naming what is missing rather than failing in
some other way. What you cannot do is grant a scope that is not on this
screen.

One scope is deliberately not in that list:

```
https://www.googleapis.com/auth/chat.admin.spaces.readonly
```

It searches every space in the Workspace rather than the ones you are
in, and only an administrator can use it. `login` asks for it only when
the admin toolset is on, so add it to this screen alongside
`GCM_TOOLSETS=all,admin`, or leave both alone.

Most sit in Google's *sensitive* tier. Three are *restricted*:
`chat.messages`, `chat.spaces` and `chat.delete`. For an **Internal**
app none of that matters — no verification, no assessment. If you ever
publish externally it matters a great deal;
[`runbook.md`](runbook.md#do-the-restricted-scopes-mean-a-security-assessment)
has the detail.

## 5. Create the OAuth client

APIs & Services → Credentials → **Create credentials** → **OAuth client
ID**.

- **Application type: Desktop app.** Not "Web application" — the login
  flow listens on `127.0.0.1`, which is what a desktop client is for.
- Name it whatever you like.
- Create, then **Download JSON**. Save it as `client_secret.json`
  somewhere you can find it again.

Google treats a desktop client's secret as public, which is why the
login uses PKCE. It is still worth keeping out of a shared directory and
out of version control.

## 6. Test users

Only for an **External** app in testing. OAuth consent screen → **Test
users** → add your own address. Up to 100 people.

An Internal app skips this: the consent screen is already limited to
your domain.

## 7. Log in

```bash
google-chat-mcp login --client-secret /path/to/client_secret.json
```

Your browser opens; if there is none, the URL is printed and the command
waits. The refresh token goes into your OS keyring, or into a `0600`
file if there is no keyring — the command says which.

Check it worked:

```bash
google-chat-mcp doctor
```

Then wire the binary into your client, per the
[README](../README.md#connect-your-client).

## Verification, later and probably never

You only need Google's verification if you publish an **External** app
to more than 100 people. Self-service verification for the sensitive
scopes takes a few business days and wants a privacy policy, terms, a
verified domain and a logo. The restricted scopes additionally need an
annual third-party security assessment.

Almost nobody running this needs any of it: one person, one internal
app, no verification. Google's
[verification documentation](https://support.google.com/cloud/answer/13464325)
is there if you do.
