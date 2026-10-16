package main

import (
	"embed"
	"fmt"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"frpdesk/internal/i18n"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// --version：打印版本后立即退出，必须发生在 NewAppService 之前，
	// 绝不初始化 GUI / 托盘 / 单实例监听（发布脚本用它做版本验证）。
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println(currentVersion)
			return
		}
	}

	appService := NewAppService()

	app := application.New(application.Options{
		Name:        "frpdesk",
		Description: i18n.T(i18n.KeyAppDescription),
		Services: []application.Service{
			application.NewService(appService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// 关窗只隐藏到托盘（见 WindowClosing 钩子），退出统一走托盘菜单/Cmd+Q；
			// 必须禁用“关窗即退出”，否则隐藏流程可能连带终止应用
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		// 单实例：第二次启动只通知首实例恢复主窗口，自身立即退出；
		// 不会触发额外业务启动（回调只恢复窗口）
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "cc.frp.frpdesk",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				appService.showMainWindow()
			},
		},
		Logger: slog.Default(),
	})

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "frpdesk",
		Width:     780,
		Height:    600, // 顶栏合并为单条后回收一行高度
		MinWidth:  680,
		MinHeight: 440,
		Mac: application.MacWindow{
			// 隐藏标题文字但保留红绿灯（内容延伸进标题栏区域），
			// 由前端合并顶栏充当拖拽区（--wails-draggable），交互元素显式 no-drag
			TitleBar: application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	// 装配托盘 + 关闭拦截 + Dock reopen（Run 之前，框架就绪期安全初始化）
	appService.attachUI(app, window)

	if err := app.Run(); err != nil {
		slog.Error("app exited with an error", "err", err)
	}
}
