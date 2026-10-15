//go:build darwin

// macOS AppKit 桥：仅用公开 API，补齐 wails v3 未暴露的运行时窗口/Dock 行为。
// AppKit 要求主线程调用，非主线程时 dispatch_async 兜底（无需等待完成）。
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

static void frpdeskApplyDockVisible(bool visible) {
    [NSApp setActivationPolicy:visible ? NSApplicationActivationPolicyRegular
                                       : NSApplicationActivationPolicyAccessory];
}

static void frpdeskSetDockVisible(bool visible) {
    if (![NSThread isMainThread]) {
        dispatch_async(dispatch_get_main_queue(), ^{ frpdeskApplyDockVisible(visible); });
        return;
    }
    frpdeskApplyDockVisible(visible);
}

static void frpdeskActivateApp(void) {
    if (![NSThread isMainThread]) {
        dispatch_async(dispatch_get_main_queue(), ^{ [NSApp activateIgnoringOtherApps:YES]; });
        return;
    }
    [NSApp activateIgnoringOtherApps:YES];
}

// 黄色最小化按钮重定向到 performClose:（target = 窗口自身）：
// 走既有 windowShouldClose delegate → WindowClosing → Go 侧 Cancel + hideToTray，
// 与红色关闭按钮/Cmd+W 完全同路，无需任何新回调。
static void frpdeskTakeOverMiniaturizeImpl(void *nsWindow) {
    NSWindow *win = (NSWindow *)nsWindow;
    NSButton *btn = [win standardWindowButton:NSWindowMiniaturizeButton];
    if (btn == nil) {
        return;
    }
    btn.target = win;
    btn.action = @selector(performClose:);
}

static void frpdeskTakeOverMiniaturize(void *nsWindow) {
    if (![NSThread isMainThread]) {
        dispatch_async(dispatch_get_main_queue(), ^{ frpdeskTakeOverMiniaturizeImpl(nsWindow); });
        return;
    }
    frpdeskTakeOverMiniaturizeImpl(nsWindow);
}
*/
import "C"

// macSetDockVisible 切换 Dock 图标：true=Regular（常规应用，Dock 显示）；
// false=Accessory（纯托盘应用，Dock 隐藏，状态栏图标不受影响）。
func macSetDockVisible(visible bool) {
	C.frpdeskSetDockVisible(C.bool(visible))
}

// macActivateApp 把应用带到前台（恢复窗口时配合 Focus 使用）。
func macActivateApp() {
	C.frpdeskActivateApp()
}

// macTakeOverMiniaturize 接管窗口黄色最小化按钮：点击转为隐藏到托盘。
// 窗口 impl 未就绪时 NativeWindow 返回 nil，直接忽略（由调用方选好时机）。
func macTakeOverMiniaturize(window *application.WebviewWindow) {
	nw := window.NativeWindow()
	if nw == nil {
		return
	}
	C.frpdeskTakeOverMiniaturize(nw)
}
