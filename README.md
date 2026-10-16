<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

English | [简体中文](README_zh-CN.md)

frpdesk is a native desktop client for [frp](https://github.com/fatedier/frp), built with [Wails v3](https://v3.wails.io). The frp client runs directly inside the app, with no sidecar process.

## Features

- HTTP, TCP, and UDP tunnels
- Multiple FRPS server configurations
- Service status, logs, and HTTP request statistics
- Menu-bar background running on macOS
- Built-in HTTP proxy with CORS and Host rewriting
- Simplified Chinese / English UI that can follow the system language

## UI language

- The title-bar language button cycles through **System → 简体中文 → English**; the default is System, which uses Simplified Chinese on Chinese systems and falls back to English for other languages. In System mode the **native side resolves the language** (environment variables first, then the platform UI language) and the webview follows that result, so the UI and the tray menu always agree.
- The choice is persisted: the frontend stores it locally and also saves it in the config file (`language` field of `config.json`), so the tray menu and backend messages keep the same language after a restart. If saving fails, the UI rolls back to the last confirmed choice and shows an error instead of silently drifting from the tray.
- Switching applies instantly: page, cards, forms, statistics, confirmations, toasts, tooltips and ARIA labels are re-rendered; unsaved form input is kept and tunnels are not restarted.
- User data (service names, server addresses, domains), technical logs and raw FRP errors stay in their original form for troubleshooting.

## Translation contributions

All user-visible strings live in two flat dictionaries (semantic key → zh/en text):

| Location | Scope |
| --- | --- |
| `DICT` in `frontend/dist/i18n.js` | All frontend strings (static `data-i18n*` attributes and dynamic rendering); `{0}..{n}` are placeholders |
| `dict` in `internal/i18n/keys.go` | Backend strings shown directly (tray menu/status, validation errors, connectivity probes, update prompts) |

To add or change a translation: keep key names unchanged, keep both `zh` and `en` non-empty with matching `{n}` placeholders, then run `node scripts/verify-i18n.mjs` and `go test ./internal/i18n/`. Run `node scripts/verify-multiserver-ui.mjs` and `go test ./...` for behavior regressions. Raw FRP errors (`rawErr` / logs) must stay untranslated.

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
node scripts/verify-i18n.mjs             # dictionary completeness and key alignment
```

## Release

Push a `vX.Y.Z` tag. Its version must match both `VERSION` and the newest `## X.Y.Z`
section of [CHANGELOG.md](CHANGELOG.md); the
[Release workflow](.github/workflows/release.yml) then runs the tests, builds the
macOS and Windows artifacts, generates the Wails updater `stable.json` from the
CHANGELOG notes, and publishes everything as a GitHub Release.

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
