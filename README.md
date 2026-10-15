<img src="build/appicon.png" width="72" height="72" alt="">

# Frpdesk

frpdesk is a native desktop client for [frp](https://github.com/fatedier/frp), built with [Wails v3](https://v3.wails.io). The frp client runs directly inside the app, with no sidecar process.

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

## Release

Push a `vX.Y.Z` tag. Its version must match both `VERSION` and the newest `## X.Y.Z`
section of [CHANGELOG.md](CHANGELOG.md); the
[Release workflow](.github/workflows/release.yml) then runs the tests, builds the
macOS and Windows artifacts, generates the Wails updater `stable.json` from the
CHANGELOG notes, and publishes everything as a GitHub Release.

Shipped builds check for updates at
`https://github.com/ivanusong/frpdesk/releases/latest/download/stable.json`.


## License

[Apache-2.0](LICENSE). Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
