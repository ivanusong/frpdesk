#!/usr/bin/env bash
# frpdesk 跨平台构建脚本：macOS 主机出 darwin 双架构 + windows 双架构，
# Linux 主机出本机架构，合计覆盖六个用户平台（darwin/windows/linux × amd64/arm64）。
#
# 用法：
#   bash build.sh                                  # 缺省目标：macOS 主机四目标；Linux 主机本机架构
#   bash build.sh darwin/arm64 windows/arm64 ...   # 显式选择目标（os/arch，可任意组合）
#   bash build.sh --print-version                  # 只解析并打印版本号，不构建
#
# 可选环境变量：
#   FRPDESK_VERSION        显式构建版本（可带 v 前缀）；CI 传入 tag 解析结果，
#                         本地可临时覆盖。未设置时取 git describe（见下）。
#   FRPDESK_BIN_DIR        产物目录（默认 bin/）。
#   UPDATE_MANIFEST_URL     自定义客户端更新主源（stable.json 完整 URL）；设置后注入，
#                           两项均不设置时禁用更新检查（默认不内置任何地址）。
#   UPDATE_MIRROR_BASE_URL  可选官方镜像公开根地址（http(s)、无尾斜杠、无空白/引号）；
#                           设置后应用优先从镜像检查与下载，网络失败回退主源。
#
# 流程：生成 Go bindings → 同步进 embed 目录（frontend/dist）→ 按目标编译打包。
# 产物（bin/，均带版本号）：
#   frpdesk-<版本>-darwin-<arch>.zip      .app 包（ad-hoc 签名）；
#   frpdesk-<版本>-windows-<arch>.exe     单文件 exe（-H windowsgui）；
#   frpdesk-<版本>-linux-<arch>.tar.gz    顶层单一可执行（updater 归档硬约束，见
#                                         scripts/package-linux.sh）；darwin 的 .app
#                                         bundle 也留在 bin/ 供本地运行。
# 与 Taskfile.yml 同一套构建约定（production tag / trimpath / 最低 macOS 12），
# Taskfile 继续负责本地开发（run / wails3 dev），本脚本只管分发产物。
#
# 说明：
#   - macOS 主机：darwin 走 CGO + WebKit（amd64 靠 clang 多架构交叉）；windows 走
#     纯 Go WebView2（CGO_ENABLED=0）交叉编译，每架构各自生成 rsrc_windows_<arch>.syso；
#   - linux 目标必须在同架构 Linux 主机构建：Wails Linux 走 CGO + GTK4/webkitgtk-6.0
#     （third_party/wails/pkg/application/linux_cgo.go），不做 Mac 伪交叉；
#     Ubuntu 依赖：libgtk-4-dev libwebkitgtk-6.0-dev（gcc/pkg-config Runner 预装）；
#   - 依赖 go 与 wails3，缺失时按提示安装（安装位置兼容 mise 的 GOBIN）：
#     go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.28
#
# 与 GitHub Actions 的对应关系：build.yml 只读构建六包上传附件；release.yml 矩阵构建后
# 聚合生成 stable.json 并发布；linux 目标由 ubuntu-24.04 / ubuntu-24.04-arm 原生构建。

set -euo pipefail

# 基于脚本所在目录定位项目根（无论从哪里调用都正确）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

APP_NAME="frpdesk"
BIN_DIR="${FRPDESK_BIN_DIR:-bin}"
# 版本号来源（tag-only，不再读取仓库内版本文件）：
#   FRPDESK_VERSION（可带 v 前缀）> git describe --tags --always --dirty > dev
VERSION="${FRPDESK_VERSION:-}"
if [ -z "$VERSION" ]; then
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
fi
VERSION="${VERSION#v}"
[ -n "$VERSION" ] || VERSION="dev"

# --print-version：只解析并打印版本号（供 CI/测试/本地查看），不执行任何构建与检查
if [ "${1:-}" = "--print-version" ]; then
    printf '%s\n' "$VERSION"
    exit 0
fi
# 与 go.mod 里 wails v3 版本保持一致
WAILS3_VERSION="v3.0.0-beta.28"

# 客户端更新源：默认空 = 不注入，应用内更新检查禁用（可选自定义）
UPDATE_MANIFEST_URL="${UPDATE_MANIFEST_URL:-}"
# 可选镜像根地址：默认空 = 不注入，行为与未配置镜像时完全一致
UPDATE_MIRROR_BASE_URL="${UPDATE_MIRROR_BASE_URL:-}"

# 公共链接参数：版本号必注入；更新源只在显式配置时注入（注入前先在下方校验）
BUILD_LDFLAGS="-w -s -X main.currentVersion=${VERSION}"
if [ -n "$UPDATE_MANIFEST_URL" ]; then
    BUILD_LDFLAGS="$BUILD_LDFLAGS -X main.updateManifestURL=${UPDATE_MANIFEST_URL}"
fi
if [ -n "$UPDATE_MIRROR_BASE_URL" ]; then
    BUILD_LDFLAGS="$BUILD_LDFLAGS -X main.updateMirrorBaseURL=${UPDATE_MIRROR_BASE_URL}"
fi

C_GREEN='\033[0;32m'; C_RED='\033[0;31m'; C_RESET='\033[0m'
info() { printf "${C_GREEN}[构建]${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}[错误]${C_RESET} %s\n" "$*" >&2; }

# 校验注入地址：防止空白/引号/反斜杠破坏 ldflags 参数
validate_url() {
    local name="$1" value="$2" bad=""
    [ -z "$value" ] && return 0
    case "$value" in
        http://*|https://*) ;;
        *) err "$name 必须是 http(s) 地址：$value"; exit 1 ;;
    esac
    case "$value" in
        *" "*|*$'\t'*|*$'\r'*|*$'\n'*) bad="空白字符" ;;
        *"\""*|*"'"*) bad="引号字符" ;;
        *"\\"*) bad="反斜杠字符" ;;
    esac
    if [ -n "$bad" ]; then
        err "$name 不能包含${bad}"
        exit 1
    fi
}
validate_url UPDATE_MANIFEST_URL "$UPDATE_MANIFEST_URL"
validate_url UPDATE_MIRROR_BASE_URL "$UPDATE_MIRROR_BASE_URL"
# 镜像根按约定不带尾斜杠（客户端按 <base>/releases/<version>/<filename> 拼接）
if [ -n "$UPDATE_MIRROR_BASE_URL" ] && [ "${UPDATE_MIRROR_BASE_URL%/}" != "$UPDATE_MIRROR_BASE_URL" ]; then
    err "UPDATE_MIRROR_BASE_URL 不能以 / 结尾"
    exit 1
fi

# ---- 构建目标 ================================================================

# 支持的目标（os/arch）；linux 必须在同架构 Linux 主机构建（CGO + GTK4/webkitgtk-6.0，
# 见 third_party/wails/pkg/application/linux_cgo.go），不做 Mac 伪交叉。
TARGETS=()
if [ $# -gt 0 ]; then
    for arg in "$@"; do
        case "$arg" in
            darwin/arm64|darwin/amd64|windows/amd64|windows/arm64|linux/amd64|linux/arm64)
                TARGETS+=("$arg") ;;
            *) err "未知构建目标：${arg}（可用：darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 linux/amd64 linux/arm64）"; exit 1 ;;
        esac
    done
fi
if [ "${#TARGETS[@]}" -eq 0 ]; then
    case "$(uname -s)" in
        Darwin) TARGETS=(darwin/arm64 darwin/amd64 windows/amd64 windows/arm64) ;;
        Linux)
            host_arch="$(uname -m)"
            case "$host_arch" in
                x86_64) host_arch=amd64 ;;
                aarch64) host_arch=arm64 ;;
            esac
            TARGETS=("linux/${host_arch}") ;;
        *) err "仅支持 macOS/Linux 主机（当前：$(uname -s)）；或显式传入目标"; exit 1 ;;
    esac
fi
for target in "${TARGETS[@]}"; do
    case "$target" in
        linux/*)
            if [ "$(uname -s)" != "Linux" ]; then
                err "目标 $target 只能在 Linux 主机构建（CGO + GTK4/webkitgtk-6.0，不做 Mac 伪交叉）"
                exit 1
            fi
            ;;
    esac
done

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
    # 版本化 zip 即分发产物（updater 清单直接引用该文件名）
    ditto -c -k --keepParent --norsrc "${BIN_DIR}/${APP_NAME}-darwin-${arch}.app" \
        "${BIN_DIR}/${APP_NAME}-${VERSION}-darwin-${arch}.zip"
    info "darwin/${arch} 完成 → ${BIN_DIR}/${APP_NAME}-${VERSION}-darwin-${arch}.zip"
}

# Windows（纯 Go WebView2，可交叉编译；-H windowsgui 隐藏控制台黑窗）
build_windows() {
    local arch="$1"
    info "构建 windows/${arch}..."
    # Windows VERSIONINFO 的 fixed 字段要求数字四段：本地 git describe 版本可能带后缀
    # （如 0.2.12-3-gabc-dirty），取数字三段；取不到时回退 0.0.0；ProductVersion 保留原串
    local win_version
    win_version="$(printf '%s' "$VERSION" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p')"
    [ -n "$win_version" ] || win_version="0.0.0"
    # 资源嵌入：exe 图标 + DPI manifest + 版本信息（资源管理器属性面板可见）。
    # 版本号从解析结果注入 info.json，每架构各自生成 rsrc_windows_<arch>.syso；
    # go build 只自动链接匹配当前 GOOS/GOARCH 的 syso，不会串架构。
    cat > build/windows/info.json <<EOF
{
  "fixed": {
    "file_version": "${win_version}.0",
    "product_version": "${win_version}.0"
  },
  "info": {
    "0409": {
      "CompanyName": "Frpdesk",
      "FileDescription": "frp 穿透客户端",
      "ProductName": "Frpdesk",
      "ProductVersion": "${VERSION}"
    }
  }
}
EOF
    "$WAILS3" generate syso -arch "$arch" \
        -icon build/windows/icon.ico \
        -manifest build/windows/wails.exe.manifest \
        -info build/windows/info.json \
        -out "rsrc_windows_${arch}.syso"
    CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
        go build -tags production -trimpath -ldflags="${BUILD_LDFLAGS} -H=windowsgui" \
        -o "${BIN_DIR}/${APP_NAME}-${VERSION}-windows-${arch}.exe" .
    info "windows/${arch} 完成 → ${BIN_DIR}/${APP_NAME}-${VERSION}-windows-${arch}.exe"
}

# Linux（CGO + GTK4/webkitgtk-6.0，须在同架构 Linux 主机构建）
build_linux() {
    local arch="$1"
    info "构建 linux/${arch}..."
    CGO_ENABLED=1 GOOS=linux GOARCH="$arch" \
        go build -tags production -trimpath -ldflags="${BUILD_LDFLAGS}" \
        -o "${BIN_DIR}/${APP_NAME}-linux-${arch}" .
    # updater 归档硬约束：恰好一个顶层条目 → tar.gz 只含单一可执行，许可走共享 licenses.zip
    "$SCRIPT_DIR/scripts/package-linux.sh" "${BIN_DIR}/${APP_NAME}-linux-${arch}" \
        "${BIN_DIR}/${APP_NAME}-${VERSION}-linux-${arch}.tar.gz"
    rm -f "${BIN_DIR}/${APP_NAME}-linux-${arch}"
    info "linux/${arch} 完成 → ${BIN_DIR}/${APP_NAME}-${VERSION}-linux-${arch}.tar.gz"
}

for target in "${TARGETS[@]}"; do
    case "$target" in
        darwin/*)  build_darwin "${target##*/}" ;;
        windows/*) build_windows "${target##*/}" ;;
        linux/*)   build_linux "${target##*/}" ;;
    esac
done

if [ -n "$UPDATE_MANIFEST_URL" ]; then
    if [ -n "$UPDATE_MIRROR_BASE_URL" ]; then
        info "已注入客户端更新主源（stable.json）与镜像源：${UPDATE_MIRROR_BASE_URL}"
    else
        info "已注入客户端更新主源（stable.json）"
    fi
elif [ -n "$UPDATE_MIRROR_BASE_URL" ]; then
    info "已注入镜像更新源：${UPDATE_MIRROR_BASE_URL}"
else
    info "未配置更新源：应用内更新检查将禁用"
fi

# Windows EXE 的许可文档作为邻接文件分发；CI 同时打为 licenses.zip。
"$SCRIPT_DIR/scripts/stage-license-docs.sh" "$BIN_DIR"

info "全部完成（版本 ${VERSION}；目标：${TARGETS[*]}），产物在 ${BIN_DIR}/"
