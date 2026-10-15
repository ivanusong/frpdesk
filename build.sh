#!/usr/bin/env bash
# frpdesk 一键跨平台构建：Windows amd64 + macOS arm64 + macOS amd64 一次全出。
#
# 可选环境变量：
#   UPDATE_MANIFEST_URL  客户端更新源（stable.json 完整 URL）；不设置则不注入，
#                        应用内更新检查明确禁用。真实发布由 release.sh 从 .release.env
#                        读取后传入（仓库不内置任何具体发布地址）。
#
# 流程：生成 Go bindings → 同步进 embed 目录（frontend/dist）→ 按目标矩阵编译。
# macOS 产物打包成 .app（ad-hoc 签名）；Windows 产物为单文件 exe。
# 与 Taskfile.yml 同一套构建约定（production tag / trimpath / 最低 macOS 12），
# Taskfile 继续负责本地开发（run / wails3 dev），本脚本只管分发产物。
#
# 说明：
#   - 仅可在 macOS 上运行（darwin 编译与 codesign 依赖本机工具链）；
#   - Wails v3 在 macOS 走 CGO + WebKit，amd64 靠 clang 多架构交叉；
#     在 Windows 走纯 Go 的 WebView2 绑定，CGO_ENABLED=0 即可从 Mac 交叉编译；
#   - 依赖 go 与 wails3，缺失时按提示安装（安装位置兼容 mise 的 GOBIN）：
#     go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.28
#
# 产物（bin/）：
#   frpdesk-darwin-arm64.app  frpdesk-darwin-amd64.app  frpdesk-windows-amd64.exe
#   （.app 内 Info.plist 版本号由 VERSION 文件注入；zip 只由 release.sh 在
#    bin/releases/<版本>/ 内按版本名生成，这里不再遍历旧产物打 zip）

set -euo pipefail

# 基于脚本所在目录定位项目根（无论从哪里调用都正确）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

APP_NAME="frpdesk"
BIN_DIR="bin"
# 版本号唯一权威：VERSION 文件（release.sh 发布时写入新值）
VERSION="$(tr -d '[:space:]' < VERSION 2>/dev/null || true)"
# 与 go.mod 里 wails v3 版本保持一致
WAILS3_VERSION="v3.0.0-beta.28"

# 客户端更新源：默认空 = 不注入，应用内更新检查禁用（具体地址只来自本地配置/release.sh）
UPDATE_MANIFEST_URL="${UPDATE_MANIFEST_URL:-}"

# 公共链接参数：版本号必注入；更新源只在显式配置时注入
BUILD_LDFLAGS="-w -s -X main.currentVersion=${VERSION}"
if [ -n "$UPDATE_MANIFEST_URL" ]; then
    BUILD_LDFLAGS="$BUILD_LDFLAGS -X main.updateManifestURL=${UPDATE_MANIFEST_URL}"
fi

C_GREEN='\033[0;32m'; C_RED='\033[0;31m'; C_RESET='\033[0m'
info() { printf "${C_GREEN}[构建]${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}[错误]${C_RESET} %s\n" "$*" >&2; }

[ -n "$VERSION" ] || { err "VERSION 文件缺失或为空"; exit 1; }

# ---- 前置检查 ================================================================

if ! command -v go >/dev/null; then
    err "缺少 go"
    exit 1
fi

# 依次在 PATH、GOBIN、GOPATH/bin、~/go/bin 里找 wails3
WAILS3="$(command -v wails3 || true)"
if [ -z "$WAILS3" ]; then
    for d in "$(go env GOBIN 2>/dev/null)" "$(go env GOPATH 2>/dev/null)/bin" "$HOME/go/bin"; do
        if [ -n "$d" ] && [ -x "$d/wails3" ]; then
            WAILS3="$d/wails3"
            break
        fi
    done
fi
if [ -z "$WAILS3" ]; then
    err "缺少 wails3：go install github.com/wailsapp/wails/v3/cmd/wails3@${WAILS3_VERSION}"
    exit 1
fi

# ---- 前端 bindings（与 Taskfile 同一套流程，只做一次）=======================

info "生成前端 bindings..."
"$WAILS3" generate bindings -b -names -clean -silent
rm -rf frontend/dist/bindings
cp -R frontend/bindings frontend/dist/bindings
cp -f assets/icon.svg frontend/dist/icon.svg

# ---- 编译与打包 ==============================================================

# 打包 .app：$1=已编译二进制 $2=产物 .app 路径（内部可执行名须与 Info.plist 的
# CFBundleExecutable 一致，固定为 frpdesk）
package_app() {
    local bin="$1" app="$2"
    rm -rf "$app"
    mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
    cp build/darwin/icons.icns "$app/Contents/Resources/"
    cp build/darwin/Info.plist "$app/Contents/"
    # 版本号由 VERSION 注入（覆盖模板值）
    plutil -replace CFBundleIdentifier -string "com.frpdesk.app" "$app/Contents/Info.plist"
    plutil -replace CFBundleVersion -string "$VERSION" "$app/Contents/Info.plist"
    plutil -replace CFBundleShortVersionString -string "$VERSION" "$app/Contents/Info.plist"
    cp "$bin" "$app/Contents/MacOS/${APP_NAME}"
    # 许可证文档必须在 codesign 前复制进 bundle，签名后不可再改 bundle 内容。
    "$SCRIPT_DIR/scripts/stage-license-docs.sh" "$app/Contents/Resources"
    xattr -cr "$app"
    codesign --force --deep --sign - "$app"
    codesign --verify --deep --strict "$app"
}

# macOS（CGO + WebKit，本机 arm64 / 交叉 amd64）
build_darwin() {
    local arch="$1"
    info "构建 darwin/${arch}..."
    CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" \
    MACOSX_DEPLOYMENT_TARGET=12.0 \
    CGO_CFLAGS='-mmacosx-version-min=12.0' CGO_LDFLAGS='-mmacosx-version-min=12.0' \
        go build -tags production -trimpath -ldflags="${BUILD_LDFLAGS}" \
        -o "${BIN_DIR}/${APP_NAME}-darwin-${arch}" .
    package_app "${BIN_DIR}/${APP_NAME}-darwin-${arch}" "${BIN_DIR}/${APP_NAME}-darwin-${arch}.app"
    rm -f "${BIN_DIR}/${APP_NAME}-darwin-${arch}"
    info "darwin/${arch} 完成 → ${BIN_DIR}/${APP_NAME}-darwin-${arch}.app"
}

# Windows（纯 Go WebView2，可交叉编译；-H windowsgui 隐藏控制台黑窗）
build_windows() {
    info "构建 windows/amd64..."
    # 资源嵌入：exe 图标 + DPI manifest + 版本信息（资源管理器属性面板可见）。
    # 版本号从 VERSION 注入 info.json，wails3 生成 rsrc_windows_amd64.syso 后
    # go build 自动链接（仅 GOOS=windows 生效，darwin 构建不受影响）。
    cat > build/windows/info.json <<EOF
{
  "fixed": {
    "file_version": "${VERSION}.0",
    "product_version": "${VERSION}.0"
  },
  "info": {
    "0409": {
      "CompanyName": "frpdesk",
      "FileDescription": "frp 穿透客户端",
      "ProductName": "frpdesk",
      "ProductVersion": "${VERSION}"
    }
  }
}
EOF
    "$WAILS3" generate syso -arch amd64 \
        -icon build/windows/icon.ico \
        -manifest build/windows/wails.exe.manifest \
        -info build/windows/info.json \
        -out rsrc_windows_amd64.syso
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
        go build -tags production -trimpath -ldflags="${BUILD_LDFLAGS} -H=windowsgui" \
        -o "${BIN_DIR}/${APP_NAME}-windows-amd64.exe" .
    info "windows/amd64 完成 → ${BIN_DIR}/${APP_NAME}-windows-amd64.exe"
}

build_darwin arm64
build_darwin amd64
build_windows

if [ -n "$UPDATE_MANIFEST_URL" ]; then
    info "已注入客户端更新源（stable.json）"
else
    info "未配置 UPDATE_MANIFEST_URL：应用内更新检查将禁用"
fi

# Windows EXE 的许可文档作为邻接文件分发；CI 同时打为 licenses.zip。
"$SCRIPT_DIR/scripts/stage-license-docs.sh" "$BIN_DIR"

info "全部完成（版本 ${VERSION}），产物在 ${BIN_DIR}/"
