# Releasing

A maintainer releases by pushing a tag. `release.yml` builds, signs and
publishes from it, and the result is a published release, never a draft.

## Before the release commit

Run `make live` and read the result. It drives the shipped binary against
a real account, inside one scratch space it creates and deletes. It is the
only check that can catch a wrong belief about the API, because every fake
in the unit suite is written from that belief.

- Set `LIVE_GROUP_EMAIL` to a Google Group the account can see.
  `find_group` needs a real one, and none may be named in this
  repository. The run only looks it up, and stops before it makes the
  scratch space when the variable is unset.
- `LIVE_KEEP=1` keeps the scratch space. Either way, the run makes it
  private first and fails if it cannot.

Run `make api-diff` as well. It refetches Google's discovery documents
into `testdata/api-methods.json` and reports what changed. Each method it
adds fails `api-coverage` until a row in `testdata/api-coverage.tsv`
says whether this server uses it.

`make release-rehearse` builds the archives locally and publishes
nothing. It needs only the Go toolchain.

## Steps

1. Land a `release: cut vX.Y.Z` commit on `main`. It moves
   `[Unreleased]` under `## [X.Y.Z] - YYYY-MM-DD`, which becomes the
   release notes verbatim. It also runs `make schema-baseline
   VERSION=vX.Y.Z`, without which the schema diff fails. The staleness
   gate accepts the changes under the heading being cut, since CI runs before
   the tag exists.
2. Wait for `ci.yml` to pass on that exact commit. A tag pushed earlier
   fails `verify-ci`; once CI is green, run `release.yml` by hand
   against the tag.
3. Tag it and push the tag on its own:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z: …" && git push origin vX.Y.Z
   ```

   Push one tag at a time. GitHub creates no tag events when more than
   three tags arrive in one push, and the release never starts. If that
   happens, run `release.yml` by hand against the tag.

## What `release.yml` does

1. `verify-ci` refuses the tag unless `ci.yml` passed on its commit.
2. The `goreleaser` job runs the tests again and takes the notes from
   the CHANGELOG. goreleaser builds the archives for Linux, macOS and
   Windows on amd64 and arm64, and packs the Claude Desktop `.mcpb`
   bundle. It signs `checksums.txt`, which covers the bundle, with a
   keyless Sigstore certificate, and attaches an SBOM per archive. The job
   then attests build provenance for the archives, the bundle and the
   checksums.
3. The `registry` job runs `publish-mcp.yml`, which writes `server.json`
   from the published `checksums.txt` and publishes it with
   `mcp-publisher`. It runs last, because the registry fetches the
   bundle's URL before it accepts the entry. A prerelease skips it. When
   it fails, run `publish-mcp.yml` by hand with the tag; the tag stays
   as it is.

## Check the release from outside

No rehearsal reaches signing, attestation or the registry entry, which
need a workflow's OIDC token. Check them against the published assets:

```bash
cd "$(mktemp -d)" && gh release download vX.Y.Z --repo mmedum/google-chat-mcp
```

Run the README's [verification commands](../README.md#install) there.
Then verify the bundle, and a corrupted copy of it, which must fail:

```bash
gh attestation verify google-chat-mcp_X.Y.Z.mcpb --repo mmedum/google-chat-mcp
echo "exit $?"   # 0
cp google-chat-mcp_X.Y.Z.mcpb corrupt.mcpb && printf x >> corrupt.mcpb
gh attestation verify corrupt.mcpb --repo mmedum/google-chat-mcp
echo "exit $?"   # not 0
```

A check that passes on anything is not a check. Watch for these traps:

- `gh attestation verify` prints nothing when it succeeds. An empty log
  is a pass.
- `$?` after a pipe is the pipe's last command, not `gh`. Read the exit
  code without piping.
- Do not truncate `checksums.txt` while looking for the bundle. Its row
  sorts first, so `tail` hides it.

Last, run the README's install command outside the repository, as a
user would, and check that `google-chat-mcp --version` names the release.

## A bad release

A bad release is fixed forward.

- Never move or delete a pushed tag. The Go module proxy caches it
  within minutes, and the checksum database records its hash.
- Ship a patch release with the fix.
- In that patch, add a `retract` directive to `go.mod` for the bad
  version: `retract vX.Y.Z // why`. The go command then skips it for
  `@latest`.
- Mark the bad version's CHANGELOG heading `[YANKED]`:
  `## [X.Y.Z] - YYYY-MM-DD [YANKED]`.
