# GCP setup for google-chat-mcp

One-time, ~15 minutes. Every deployer (stdio user or HTTPS operator) runs
this against their own Google Cloud project. No centralized app exists —
you own the consent screen, the client ID, and what your users see.

Prerequisites:

- A Google account. For Workspace accounts, your admin must allow
  user-managed OAuth clients; for consumer accounts, any Google
  account works but you may need to add yourself as a test user (step 6).

## 1. Create a Google Cloud project

[console.cloud.google.com](https://console.cloud.google.com/) → project
picker → "New project". Name it anything (`google-chat-mcp`, `my-gchat-bot`).
Copy the project ID — you'll select it in later steps.

## 2. Enable the APIs

APIs & Services → Library. Enable:

- **Google Chat API**
- **People API** (for sender email resolution in `get_messages` / `list_members`)

The OIDC `/userinfo` endpoint the `whoami` tool hits is always available; no
explicit API enable is needed.

## 3. Configure the OAuth consent screen

APIs & Services → OAuth consent screen.

- **User type:**
  - *Workspace domain:* Internal (auth restricted to your domain; no
    Google app-verification needed).
  - *Consumer account:* External (you'll add yourself as a test user
    in step 6; no verification needed while in Testing state).
- **App information:**
  - **App name:** Whatever your users should see on the consent screen
    ("Alice's Google Chat MCP", "Engineering Chat bot", etc.).
  - **User support email.**
  - **App logo (recommended):** Upload your org's logo or a personal
    avatar. Required before applying for sensitive-tier verification
    later; showing up with an unbranded consent screen looks unpolished
    day one. PNG, square, at least 120×120.
  - **Developer contact information:** your email.
- **App domain (optional):** Your homepage / privacy-policy / terms URLs.
  Required only for sensitive-tier verification.

## 4. Add scopes

Same screen → "Scopes" step. Click "Add or remove scopes" and paste:

```
openid
email
profile
https://www.googleapis.com/auth/chat.messages.readonly
https://www.googleapis.com/auth/chat.messages.create
https://www.googleapis.com/auth/chat.messages.reactions
https://www.googleapis.com/auth/chat.spaces.readonly
https://www.googleapis.com/auth/chat.spaces.create
https://www.googleapis.com/auth/chat.memberships.readonly
https://www.googleapis.com/auth/directory.readonly
```

All sit in Google's *sensitive* tier (3–5 day self-service verification
if you ever go External + Published). There's no *restricted*-tier scope
in this set — no annual CASA review.

## 5. Create an OAuth 2.0 Client ID

APIs & Services → Credentials → "Create credentials" → "OAuth client ID".

### Stdio mode

- **Application type:** Desktop app.
- **Name:** whatever (`google-chat-mcp stdio`).
- Click Create → the dialog offers "DOWNLOAD JSON". Save it as
  `client_secret.json` somewhere you can find it — you'll pass the path
  to `google-chat-mcp login --client-secret`.

### HTTPS mode

- **Application type:** Web application.
- **Authorized redirect URIs:** Add `https://<your-mcp-host>/oauth/callback`
  (whatever hostname you're running the server behind).
- Save the Client ID + Client Secret into `./secrets/google_client_id`
  and `./secrets/google_client_secret` per the HTTPS-mode README section.

## 6. Test users (External + Testing only)

OAuth consent screen → "Test users" → add each email that should be able
to run the OAuth flow while the app is in Testing state. Up to 100 entries.

Workspace Internal apps skip this step — the consent screen is already
scoped to your domain.

## 7. First login

### Stdio

```bash
uv tool install google-chat-mcp
google-chat-mcp login --client-secret /path/to/client_secret.json
```

The login prints the consent URL to stdout, opens your browser (or asks
you to paste the URL if it can't), and saves Fernet-encrypted tokens
to `~/.config/google-chat-mcp/tokens.json` (0600). Now wire the binary
into your MCP client — see the [README](../README.md#3-wire-into-your-mcp-client).

### HTTPS

Deploy the container per the README, then connect your MCP client to
`https://<your-host>/mcp`. The client walks OAuth on first use.

---

## Submitting for verification (optional, later)

You only need this if you're exposing your External OAuth client to more
than 100 test users. Google's self-service verification for the sensitive
scopes listed above runs 3–5 business days. Requirements: privacy policy
URL, terms URL, domain-verified homepage, app logo (already uploaded in
step 3).

See Google's [OAuth verification docs](https://support.google.com/cloud/answer/13464325)
when you're ready.
