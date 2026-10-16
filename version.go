package main

// currentVersion 是应用版本号（稳定 x.y.z）。
// 源码默认 "dev" 仅为未注入时的占位（go run / go test 场景）；
// 正式构建由 build.sh 通过 -ldflags "-X main.currentVersion=<VERSION>" 注入：
// CI 传 tag 解析结果（FRPDESK_VERSION），本地默认 git describe。
// 运行 `frpdesk --version` 可打印版本（main.go 在启动 GUI 前处理并退出）。
var currentVersion = "dev"
