# Third-Party Notices

frpdesk is licensed under the [Apache License 2.0](LICENSE). The third-party
components listed below keep their own licenses; full license texts are in
[`licenses/`](licenses/).

Local fork modifications are documented in
[`third_party/wails/README.md`](third_party/wails/README.md) and in the
`frpdesk fork` comments inside `third_party/frp/client/service.go`.

| Component | Version | License | Bundled at |
| --- | --- | --- | --- |
| [fatedier/frp](https://github.com/fatedier/frp) — local fork | v0.71.0 | Apache-2.0 | `third_party/frp/` |
| [fatedier/golib](https://github.com/fatedier/golib) | v0.8.2 | Apache-2.0 | Go module |
| [skip2/go-qrcode](https://github.com/skip2/go-qrcode) | da1b6568686e | MIT | Go module |
| [wailsapp/wails/v3](https://github.com/wailsapp/wails) — local fork | v3.0.0-beta.28 | MIT | `third_party/wails/` |
| go-common-file-dialog (inside the Wails fork) | — | MIT | `third_party/wails/internal/go-common-file-dialog/` |
| webviewloader, a Go port of OpenWebView2Loader (inside the Wails fork) | — | ISC | `third_party/wails/internal/webview2/webviewloader/` |
| [Lucide](https://lucide.dev/) icons, some derived from [Feather](https://feathericons.com/) | inline | ISC / MIT | `frontend/dist/main.js`, `frontend/dist/style.css` |
| [hashicorp/yamux](https://github.com/hashicorp/yamux), indirect dependency of frp | v0.1.1 | MPL-2.0 | Go module |

For the two local forks, the copy under `third_party/` is the exact source built
into this application. Every license text in `licenses/` is an unmodified copy of
the upstream license.
