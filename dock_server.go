//go:build server

// 服务端构建没有 Dock/原生窗口：tray 等桌面代码仍参与编译，需要空实现满足符号引用。
package main

import "github.com/wailsapp/wails/v3/pkg/application"

// macSetDockVisible 服务端无 Dock，空实现。
func macSetDockVisible(bool) {}

// macActivateApp 服务端无应用前台激活，空实现。
func macActivateApp() {}

// macTakeOverMiniaturize 服务端无原生窗口，空实现。
func macTakeOverMiniaturize(*application.WebviewWindow) {}
