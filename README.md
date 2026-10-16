<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

Frpdesk is a native desktop client for [frp](https://github.com/fatedier/frp), built with [Wails v3](https://v3.wails.io). The frp client runs directly inside the app, with no sidecar process.

## Features

- HTTP, TCP, and UDP tunnels
- Multiple FRPS server configurations
- Service status, logs, and HTTP request statistics
- Menu-bar background running on macOS
- Built-in HTTP proxy with CORS and Host rewriting

## Development

Requirements: Go 1.25+, Wails v3 CLI, and Node.js for the frontend checks.

```bash
wails3 dev          # run locally
./build.sh          # build macOS arm64/amd64 and Windows amd64
go test ./...       # run tests
```

Frontend checks (zero npm dependencies, also run in CI):

```bash
node scripts/verify-multiserver-ui.mjs   # multi-FRPS UI behavior
```

## Release

Push an **annotated** `vX.Y.Z` tag — the tag is the only version source, there is no
version file or changelog to maintain. The
[Release workflow](.github/workflows/release.yml) parses the version from the tag,
uses the tag message (the body when it has a subject and a body) as the release notes
for both the GitHub Release description and the Wails updater `stable.json` notes
shown in-app, then runs the tests, builds the macOS and Windows artifacts, and
publishes everything as a GitHub Release. A lightweight tag or an empty message fails
the release before anything is published.

Local builds use `git describe --tags --always --dirty` when no explicit version is
given; set `FRPDESK_VERSION` to override.

```bash
git tag -a v0.3.0 -m "Frpdesk v0.3.0

- What changed in this release
- A few lines are enough"
git push origin v0.3.0
```

Shipped builds check for updates against the GitHub Release `stable.json` of the
repository they were built from (derived at build time, so forks publish their own
update source). The concrete URL is injected by the build, never hardcoded in source.

### Optional update mirror (S3)

The official release can additionally sync the published assets, byte-for-byte, to
any S3-compatible storage for faster downloads. It is entirely optional: without the
configuration below, releases go to GitHub only and everything keeps working.

Repository **Actions variables**:

| Variable | Meaning |
| --- | --- |
| `UPDATE_MIRROR_BASE_URL` | Public download root of the mirror (no trailing slash); compiled into binaries at build time |
| `MIRROR_S3_ENDPOINT` | S3-compatible endpoint |
| `MIRROR_S3_BUCKET` | Bucket name |
| `MIRROR_S3_PREFIX` | Optional key prefix inside the bucket |
| `MIRROR_S3_REGION` | Optional, defaults to `us-east-1` |

Repository **Actions secrets**: `MIRROR_S3_ACCESS_KEY_ID`, `MIRROR_S3_SECRET_ACCESS_KEY`.

Layout: `<root>/releases/<version>/<original-release-filename>` plus a per-version
`manifest.json`; the root `stable.json` (published with `Cache-Control: no-cache`)
points clients at the current version. The mirror manifest is derived from the
published GitHub `stable.json` with only the artifact URLs rewritten — versions,
sizes and digests stay identical, so the app can fall back from the mirror to GitHub
without ever accepting a different version.

Behavior: a partially filled configuration fails the sync job loudly; the sync runs
as a separate job after the GitHub Release is published, re-uploads nothing that is
already identical, refuses to overwrite version-scoped objects with different
content, and only then switches the root `stable.json`. If the mirror is down,
clients automatically fall back to GitHub.


## License

[Apache-2.0](LICENSE). Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
