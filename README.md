<img src="assets/icon.svg" width="64" height="64" alt="">

# frpdesk

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

## License

frpdesk's own code is licensed under [Apache License 2.0](LICENSE). Third-party code retains its original licenses; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [licenses/](licenses/).
