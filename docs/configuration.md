# Configuration

Every setting has an environment variable and a command-line flag. The
flag wins over the environment, and the environment wins over the
default. Settings are validated once at start, and every problem is
reported at once rather than one per run.

Environment variables are the practical route: an MCP client passes a
stdio server a command, its arguments and its environment, and nothing
else.

## Settings

| Variable | Flag | Default | What it does |
|---|---|---|---|
| `GCM_PROFILE` | `--profile` | `default` | Which stored login to use. A profile keeps its own client secret path, account and token, so one machine can hold a work account and a personal one. Lowercase letters, digits, `_` and `-`, up to 64 characters. |
| `GCM_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn` or `error`. Logs go to stderr; stdout carries only MCP frames. |
| `GCM_LOG_FORMAT` | `--log-format` | `json` | `json` or `text`. |
| `GCM_READ_ONLY` | `--read-only` | `false` | Registers only the tools that do not change anything in Chat. A write tool that is not registered cannot be called, whatever the client's permission mode. One registered tool does write, and only to your disk: `download_attachment` saves a file into `GCM_LOCAL_DIR`, and it is not marked read-only. Leave `GCM_LOCAL_DIR` unset and this server touches nothing anywhere. |
| `GCM_ALLOW_DESTRUCTIVE` | `--allow-destructive` | `false` | Deletes are refused unless you set this to `true`. The four delete tools stay registered either way, so a model that reaches for one is told it is not permitted here and that nothing was changed, rather than being left to guess from a tool that is missing. Off by default because a deleted Chat message has no trash behind it — the sibling Drive and Docs servers gate their *recoverable* deletes the same way. |
| `GCM_INTERACTION_HINT` | `--interaction-hint` | `true` | Marks every write tool as needing a person, which clients that read the mark honour by asking before the call. Turn it off only for a deployment with nobody at the keyboard: in Claude Code the mark is absolute — an allow rule does not suppress it, and headless there is nobody to ask, so every write is refused. Reads are unaffected either way. |
| `GCM_TOOLSETS` | `--toolsets` | `all` | Comma-separated groups to register, or `all`. `core` is always included. The groups are `core`, `sections`, `pins`, `emoji`, `events`, `readstate`, `settings`, `availability` and `admin`. `all` means every group except `admin`, which has to be named: it searches spaces you are not a member of, only a Workspace administrator can use it, and turning it on adds an administrator's scope to what `login` asks for. |
| `GCM_HTTP_TIMEOUT_SECONDS` | `--http-timeout` | `10s` | How long one call to Google may take. Accepts a duration (`10s`, `1m30s`) or a bare number of seconds. Between 1 second and 10 minutes. |
| `GCM_HTTP_MAX_RETRIES` | `--http-max-retries` | `3` | Extra attempts after a 429 or 5xx, 0 to 10. A write is only retried when repeating it cannot post twice. |
| `GCM_SEARCH_MAX_PAGES` | `--search-max-pages` | `10` | How many pages `search_messages` may scan before it stops and says the result is partial, 1 to 50. |
| `GCM_DIRECTORY_CACHE_TTL_SECONDS` | `--directory-cache-ttl` | `24h` | How long a resolved email address stays cached. Same formats as the timeout. Between 1 minute and 365 days. |
| `GCM_CHAT_API_BASE` | `--chat-api-base` | `https://chat.googleapis.com/v1` | Chat API root. Exists so a test can point at a local server. |
| `GCM_PEOPLE_API_BASE` | `--people-api-base` | `https://people.googleapis.com/v1` | People API root, same reason. |
| `GCM_CLIENT_SECRET` | `--client-secret` | *(from the profile)* | Path to the OAuth Desktop client JSON. `login` records this in the profile, so it is normally only passed once. |
| `GCM_LOCAL_DIR` | `--local-dir` | *(unset)* | The one directory attachments are downloaded to and uploaded from. Unset means no file transfer at all: `download_attachment` and `upload_attachment` refuse and say so. Must be an absolute path — a relative one would mean whatever directory the MCP client happened to launch the server from. A download never overwrites, and an upload outside this directory is refused with the symlinks resolved first. |

## Credentials

The refresh token is looked up in this order, the same one `gh` uses.

1. `GCM_REFRESH_TOKEN` in the environment. For CI and automation.
2. The OS keyring — Secret Service on Linux, Keychain on macOS,
   Credential Manager on Windows — under the service
   `google-chat-mcp` and the profile name as the account.
3. A `0600` file in the profile directory, written at login only when
   the keyring was unavailable. `login` says so when it happens, and
   `doctor` reports which of the three answered. On Windows that mode
   sets the read-only attribute and nothing else — the file is protected
   by the ACL it inherits, so the keyring is the meaningful answer there.

A missing keyring entry falls through to the next source. So does a
keyring that cannot be reached at all, which is what makes a headless
machine work; if nothing is found, the keyring's own error is reported
alongside.

## Where things are stored

Non-secret state — the client secret path, which account logged in,
where the token went, which scopes were granted — lives in
`os.UserConfigDir()/google-chat-mcp`. That is
`~/.config/google-chat-mcp` on Linux,
`~/Library/Application Support/google-chat-mcp` on macOS and
`%AppData%\google-chat-mcp` on Windows. A profile other than `default`
lives under `profiles/<name>/`.

| Variable | Default | What it does |
|---|---|---|
| `GCM_CONFIG_DIR` | *(the path above)* | Moves the whole directory. Tests use it; so does an unusual setup. |
| `GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME` | *(unset)* | Allows `GCM_CONFIG_DIR` to point outside your home directory. |

The home-directory check is there because this server creates that
directory `0700` and writes `0600` files into it. A mistyped override —
`~/.ssh`, say — would re-permission something that matters, so a path
outside the home directory is refused unless you say you meant it.

## Examples

Read-only, in a client config:

```json
{
  "mcpServers": {
    "google-chat": {
      "command": "google-chat-mcp",
      "env": { "GCM_READ_ONLY": "true" }
    }
  }
}
```

A second account beside the first:

```bash
google-chat-mcp --profile personal login --client-secret ./client_secret.json
```

```json
{
  "mcpServers": {
    "google-chat-personal": {
      "command": "google-chat-mcp",
      "env": { "GCM_PROFILE": "personal" }
    }
  }
}
```

Chasing a problem, with the payload still kept out of the logs:

```bash
GCM_LOG_LEVEL=debug GCM_LOG_FORMAT=text google-chat-mcp doctor
```
