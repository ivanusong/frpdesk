# wails 本地 fork（热修）

fork 自 [wails v3.0.0-beta.28](https://github.com/wailsapp/wails)（2026-10-10 拉取），
通过 `frpdesk/go.mod` 末尾的 `replace github.com/wailsapp/wails/v3 => ./third_party/wails` 生效。

## 唯一改动

`pkg/application/webview_window_darwin.m` 的 `- (void) startDrag:(NSWindow*)window`：

原实现直接复用缓存的 `self.leftMouseEvent` 调 `performWindowDragWithEvent:`。
由于 `wails:drag` 由 JS 异步 invoke 触发，事件到达时缓存可能已过期
（mouseup 已清空、被上一轮拖拽会话吞掉、双击 zoom 已改变窗口位置等），
而 `performWindowDragWithEvent:` 会把过期事件的 `locationInWindow` 钉到当前光标位置，
导致 macOS 标题栏拖拽时窗口偶发瞬移（跳向右上、紧贴菜单栏）。

补丁改为始终以当前光标位置（`[NSEvent mouseLocation]`）合成一枚 mousedown 事件，
抓取点即光标当前位置，任何时序下 `startDrag` 都不改变窗口位置，
随后交由 Window Server 接管拖拽。

## 说明

- 上游 master 分支（截至 2026-10-10）同样未修，此补丁可直接拿去提 PR。
- 本仓库仅保留 Wails 运行时所需目录，其他上游示例、测试、开发工具及说明文档已删减。
- 升级 wails 版本时需重新评估本 fork：先确认上游是否已修 startDrag，
  再决定是重新套用补丁还是弃用 fork。
- Wails 原 MIT 许可全文见 `LICENSE` 与主仓库 `licenses/MIT-wails.txt`；其内置
  go-common-file-dialog、webviewloader 的许可与本地修改说明见主仓库 `THIRD_PARTY_NOTICES.md`。
