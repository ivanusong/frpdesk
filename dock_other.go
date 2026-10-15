//go:build !darwin

// 非 macOS 平台无 Dock 激活策略与标准窗口按钮概念：窗口 Show/Focus 即可，
// 保持 Windows CGO_ENABLED=0 纯 Go 交叉构建可用。
package main

import "github.com/wailsapp/wails/v3/pkg/application"

// macSetDockVisible 非 macOS 平台为空实现。
func macSetDockVisible(bool) {}

// macActivateApp 非 macOS 平台为空实现。
func macActivateApp() {}

// macTakeOverMiniaturize 非 macOS 平台为空实现（Windows 最小化行为不在此范围）。
func macTakeOverMiniaturize(*application.WebviewWindow) {}
