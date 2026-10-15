#!/usr/bin/env bash
# frpdesk 发布脚本：写版本 → 三目标构建（darwin arm64/amd64 + windows amd64）→
# wails3 updater manifest/verify → rclone 上传 MinIO 并最后切换 stable 指针。
#
# 用法：
#   ./release.sh                       # 读取 CHANGELOG.md 最新版本节，完整发布
#   ./release.sh 0.2.0                 # 完整发布（写 VERSION、构建、上传、切 stable）
#   ./release.sh 0.2.0 --prepare-only  # 只产出本地 bin/releases/<v>/，不上传（显式指定版本时不读远端）
#   ./release.sh 0.2.0 --dry-run       # 只打印计划，零副作用
#   ./release.sh 0.2.0 --skip-build     # 跳过构建：核验该版本既有产物后上传
#   ./release.sh 0.2.0 --notes-file NOTES.md
#
# 版本：稳定 x.y.z（可 v 前缀），禁止前导零/空段/尾点；自动路径的唯一源头是
# CHANGELOG.md 最新版本节（先写 changelog 再发布，没写新节发不了版）。
# 更新日志：未传 --notes-file 时自动从 CHANGELOG.md 摘取当前版本节（见 CHANGELOG.md 头部约定）。
# release stable 指针固定指向最新版本 manifest。
# 远端版本目录恰好 5 个文件：3 个安装包 + licenses.zip + manifest.json；manifest 仍仅列 3 个安装包。
# 只用 rclone copy/copyto/check/cat（绝不 sync/purge、不改 bucket 权限），认证全靠本机 rclone 配置。
# 签名可选：配 UPDATER_SIGNING_KEY 则 manifest 附 Ed25519 签名（公钥 <key>.pub），否则仅 sha512 摘要。
# 配置：环境变量优先于 .release.env（后者仅本机可信、已 gitignore）。仓库不内置任何发布地址：
#   RCLONE_DEST         发布必填（远端存储目标），缺配置直接失败，绝不误传到默认位置
#   DOWNLOAD_BASE_URL   发布/准备必填（安装包下载根，用于 manifest URL）
#   UPDATE_MANIFEST_URL 真实构建必填（客户端 stable.json 完整地址，经 build.sh 链接参数注入）
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

APP_NAME="frpdesk"
BIN_DIR="bin"

C_G='\033[0;32m'; C_Y='\033[0;33m'; C_R='\033[0;31m'; C_0='\033[0m'
info() { printf "${C_G}[release]${C_0} %s\n" "$*"; }
warn() { printf "${C_Y}[release]${C_0} %s\n" "$*" >&2; }
err()  { printf "${C_R}[release]${C_0} %s\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

usage() {
    cat <<'EOF'
用法: ./release.sh [x.y.z] [--dry-run|--prepare-only] [--notes-file FILE]
      ./release.sh <x.y.z> --skip-build [--prepare-only]  # 核验并上传该版本既有产物
  不传版本号      读取 CHANGELOG.md 最新版本节发布（先写 changelog 再发布；首次发布同样适用）
  --dry-run        只打印计划，零副作用
  --prepare-only   只构建本地产物（bin/releases/<v>/），不上传（显式指定版本时不读远端）
  --skip-build     跳过构建，核验既有产物后继续（可重跑已发布版本做核验补传）
  --notes-file F   发布说明 Markdown（缺值报错；文件必须存在）；未传时自动取 CHANGELOG.md 中当前版本节
EOF
}

# ---- 参数解析 ==

VERSION_ARG=""
DRY_RUN=0; PREPARE_ONLY=0; SKIP_BUILD=0; NOTES_FILE=""

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run)      DRY_RUN=1 ;;
        --prepare-only) PREPARE_ONLY=1 ;;
        --skip-build)   SKIP_BUILD=1 ;;
        --notes-file)   [ $# -ge 2 ] || { err "--notes-file 缺少值"; usage >&2; exit 1; }
                        NOTES_FILE="$2"; shift ;;
        -h|--help)      usage; exit 0 ;;
        --*)            err "未知参数: $1"; usage >&2; exit 1 ;;
        *)              [ -z "$VERSION_ARG" ] || { err "多余的位置参数: $1（版本只能给一次）"; exit 1; }
                        VERSION_ARG="$1" ;;
    esac
    shift
done

# ---- 配置：环境变量优先于 .release.env ==

_env_dest="${RCLONE_DEST+x}";        _save_dest="${RCLONE_DEST:-}"
_env_url="${DOWNLOAD_BASE_URL+x}";   _save_url="${DOWNLOAD_BASE_URL:-}"
_env_manifest="${UPDATE_MANIFEST_URL+x}"; _save_manifest="${UPDATE_MANIFEST_URL:-}"
_env_key="${UPDATER_SIGNING_KEY+x}"; _save_key="${UPDATER_SIGNING_KEY:-}"
_env_rbin="${RCLONE_BIN+x}";         _save_rbin="${RCLONE_BIN:-}"
if [ -f "${SCRIPT_DIR}/.release.env" ]; then
    # 本机可信配置（已 gitignore），直接 source
    # shellcheck source=/dev/null
    source "${SCRIPT_DIR}/.release.env"
fi
[ -n "$_env_dest" ] && RCLONE_DEST="$_save_dest"
[ -n "$_env_url" ]  && DOWNLOAD_BASE_URL="$_save_url"
[ -n "$_env_manifest" ] && UPDATE_MANIFEST_URL="$_save_manifest"
[ -n "$_env_key" ]  && UPDATER_SIGNING_KEY="$_save_key"
[ -n "$_env_rbin" ] && RCLONE_BIN="$_save_rbin"

RCLONE_BIN="${RCLONE_BIN:-rclone}"
# 存储目标与更新源不内置默认值（仓库公开，避免泄漏维护者基础设施）；缺配置在下方 fail early。
RCLONE_DEST="${RCLONE_DEST:-}"
DOWNLOAD_BASE_URL="${DOWNLOAD_BASE_URL:-}"
UPDATE_MANIFEST_URL="${UPDATE_MANIFEST_URL:-}"
DOWNLOAD_BASE_URL="${DOWNLOAD_BASE_URL%/}"
UPDATE_MANIFEST_URL="${UPDATE_MANIFEST_URL%/}"
UPDATER_SIGNING_KEY="${UPDATER_SIGNING_KEY:-}"

REMOTE_OUT=""
remote_cat() {  # S3 的 cat 对不存在的对象也可能返回 0；先确认路径是文件
    local out rc is_dir
    out="$("$RCLONE_BIN" lsjson "$1" --stat 2>/dev/null)" && rc=0 || rc=$?
    case "$rc" in
        0)   ;;
        3|4) return 1 ;;
        *)   die "检查远端 ${1} 失败（rclone exit ${rc}）：网络/权限异常不能当作首次发布" ;;
    esac
    is_dir="$(printf '%s' "$out" | plutil -extract IsDir raw -o - - 2>/dev/null)" \
        || die "远端 ${1} 的对象信息无法解析，停止发布"
    # S3 将不存在的对象路径作为空目录返回；真实文件的 IsDir 为 false
    [ "$is_dir" != true ] || return 1
    out="$("$RCLONE_BIN" cat "$1" 2>/dev/null)" && rc=0 || rc=$?
    case "$rc" in
        0)   REMOTE_OUT="$out" ;;
        3|4) return 1 ;;
        *)   die "读取远端 ${1} 失败（rclone exit ${rc}）：网络/权限异常不能当作首次发布" ;;
    esac
}

# ---- 版本确定与校验 ==

VERSION=""
version_ok() {  # $1=输入；合法时置全局 VERSION（剥 v 前缀）
    local re='^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
    [[ "$1" =~ $re ]] || return 1
    VERSION="${1#v}"
}

# semver_cmp a b → 输出 -1/0/1（a<b / a=b / a>b）
semver_cmp() {
    local a1 a2 a3 b1 b2 b3
    IFS='.' read -r a1 a2 a3 <<< "$1"
    IFS='.' read -r b1 b2 b3 <<< "$2"
    if [ "$a1" -ne "$b1" ]; then [ "$a1" -lt "$b1" ] && echo -1 || echo 1
    elif [ "$a2" -ne "$b2" ]; then [ "$a2" -lt "$b2" ] && echo -1 || echo 1
    elif [ "$a3" -ne "$b3" ]; then [ "$a3" -lt "$b3" ] && echo -1 || echo 1
    else echo 0; fi
}

VER_SOURCE="显式指定"
if [ -n "$VERSION_ARG" ]; then
    version_ok "$VERSION_ARG" || die "非法版本: ${VERSION_ARG}（需稳定 x.y.z，可 v 前缀；禁止前导零/空段/尾点）"
else
    # 版本号唯一源头：CHANGELOG.md 最新版本节（先写 changelog 再发布，没写新节 fail early）
    [ -f CHANGELOG.md ] || die "CHANGELOG.md 不存在：请先在 CHANGELOG.md 写新版本节（## x.y.z）"
    changelog_ver="$(awk '/^## / { sub(/^## /, ""); print; exit }' CHANGELOG.md)"
    # 节标题按约定为纯三段版本号（不带 v 前缀）；version_ok 校验合法性（禁前导零/空段/尾点）
    if ! version_ok "$changelog_ver" || [ "$changelog_ver" != "$VERSION" ]; then
        die "CHANGELOG.md 最新节标题「${changelog_ver:-<无>}」不是合法版本号（需纯三段 x.y.z，不带 v）：请先在 CHANGELOG.md 写新版本节（## x.y.z）"
    fi
    VER_SOURCE="CHANGELOG.md 最新节"
    # 远端 stable 三态沿用 remote_cat（对象不存在 / 网络权限异常 die / 正常读取）
    command -v "$RCLONE_BIN" >/dev/null 2>&1 || [ -x "$RCLONE_BIN" ] || die "缺少 rclone（RCLONE_BIN=${RCLONE_BIN}）"
    command -v plutil >/dev/null 2>&1 || die "缺少 plutil（macOS 自带）"
    if remote_cat "${RCLONE_DEST}/stable.json"; then
        remote_ver="$(printf '%s' "$REMOTE_OUT" | plutil -extract version raw -o - - 2>/dev/null)" \
            || die "远端 stable.json 的版本无法解析，停止发布"
        # version_ok 有副作用（覆盖 VERSION），先存住 CHANGELOG 版本，校验后恢复
        want_ver="$VERSION"
        version_ok "$remote_ver" || die "远端 stable.json 的版本缺失或非法，停止发布"
        VERSION="$want_ver"
        case "$(semver_cmp "$want_ver" "$remote_ver")" in
            0)  die "CHANGELOG.md 最新版本 ${want_ver} 与线上一致，请先在 CHANGELOG.md 写新版本节" ;;
            -1) die "版本降级被拒绝: ${want_ver} < 远端 stable ${remote_ver}（CHANGELOG.md 最新节不可低于线上）" ;;
        esac
        info "自动版本：CHANGELOG.md 最新节 ${want_ver} → 发布（线上 stable ${remote_ver}）"
    else
        info "远端无 stable.json（首次发布）：自动版本取 CHANGELOG.md 最新节 ${VERSION}"
    fi
fi

REL_DIR="${BIN_DIR}/releases/${VERSION}"
REMOTE_REL="${RCLONE_DEST}/releases/${VERSION}"
F_ARM64_ZIP="${APP_NAME}-${VERSION}-darwin-arm64.zip"
F_AMD64_ZIP="${APP_NAME}-${VERSION}-darwin-amd64.zip"
F_WIN_EXE="${APP_NAME}-${VERSION}-windows-amd64.exe"
F_LICENSES_ZIP="${APP_NAME}-${VERSION}-licenses.zip"
F_MANIFEST="manifest.json"

# ---- dry-run：只打印计划，零写入（自动版本读 CHANGELOG.md 与远端 stable；显式版本不读远端）==

if [ "$DRY_RUN" = 1 ]; then
    SKIP_NOTE=""; [ "$SKIP_BUILD" = 1 ] && SKIP_NOTE="（skip-build: 跳过 1-2）"
    cat <<PLAN
计划（零副作用，未做任何构建/写入/上传）:
  版本: ${VERSION}（来源：${VER_SOURCE}；VERSION 文件当前 $(tr -d '[:space:]' < VERSION 2>/dev/null || echo 无)）
  1. 写 VERSION ← ${VERSION}；build.sh 三目标构建${SKIP_NOTE}（客户端更新源注入：${UPDATE_MANIFEST_URL:-<未配置，将禁用更新检查>}）
  2. 收集 → ${REL_DIR}/: ${F_ARM64_ZIP} ${F_AMD64_ZIP} ${F_WIN_EXE} ${F_LICENSES_ZIP} ${F_MANIFEST}
     （5 个版本文件：3 安装包 + 许可文档包 + manifest；manifest 仍仅含 3 安装包；+2 个 .app 仅本地留存）
  3. wails3 updater manifest -channel stable -url-prefix ${DOWNLOAD_BASE_URL:-<未配置>}/releases/${VERSION}${UPDATER_SIGNING_KEY:+（签名）}（仅 3 安装包）
  4. wails3 updater verify（无签名也校验摘要）
  5. rclone copy 4 个分发文件（3 安装包 + licenses.zip）→ ${RCLONE_DEST:-<未配置>}/releases/${VERSION}/（--immutable --checksum）
  6. manifest 缺失时 copyto 上传；check --download --one-way 核验 5 个版本文件
  7. 最后 copyto manifest → ${RCLONE_DEST:-<未配置>}/stable.json（Cache-Control: no-cache）
  发布后入口: ${DOWNLOAD_BASE_URL:-<未配置>}/stable.json
PLAN
    [ -z "$RCLONE_DEST" ] && warn "RCLONE_DEST 未配置（真实发布必需，参考 release.env.example）"
    [ -z "$DOWNLOAD_BASE_URL" ] && warn "DOWNLOAD_BASE_URL 未配置（发布必需，参考 release.env.example）"
    [ "$SKIP_BUILD" != 1 ] && [ -z "$UPDATE_MANIFEST_URL" ] && warn "UPDATE_MANIFEST_URL 未配置（发行构建必需，否则客户端更新检查禁用）"
    [ "$SKIP_BUILD" = 1 ] && [ ! -d "$REL_DIR" ] && warn "产物目录不存在: ${REL_DIR}"
    exit 0
fi

# ---- 临时文件统一清理（构建段可能产出 changelog notes，发布段有上传清单）==

CHANGELOG_NOTES_TMP=""; LIST_TMP=""; LICENSE_STAGE_TMP=""
trap 'rm -f "$CHANGELOG_NOTES_TMP" "$LIST_TMP"; [ -z "$LICENSE_STAGE_TMP" ] || rm -rf "$LICENSE_STAGE_TMP"' EXIT

# ---- 工具与配置校验（写 VERSION / 构建之前）==

find_wails3() {
    command -v wails3 2>/dev/null && return 0
    local d
    for d in "$(go env GOBIN 2>/dev/null)" "$(go env GOPATH 2>/dev/null)/bin" "$HOME/go/bin"; do
        [ -n "$d" ] && [ -x "$d/wails3" ] && { echo "$d/wails3"; return 0; }
    done
    return 1
}

WAILS3="$(find_wails3)" || die "缺少 wails3（go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.28）"
command -v plutil >/dev/null 2>&1 || die "缺少 plutil（macOS 自带）"
if [ "$SKIP_BUILD" != 1 ]; then
    command -v go >/dev/null 2>&1 || die "缺少 go"
    for t in codesign ditto xattr; do
        command -v "$t" >/dev/null 2>&1 || die "缺少 $t"
    done
fi
if [ "$PREPARE_ONLY" != 1 ]; then
    command -v "$RCLONE_BIN" >/dev/null 2>&1 || [ -x "$RCLONE_BIN" ] || die "缺少 rclone（RCLONE_BIN=${RCLONE_BIN}）"
fi
[ -n "$DOWNLOAD_BASE_URL" ] || die "DOWNLOAD_BASE_URL 未配置（准备产物和发布均需要，参考 release.env.example）"
case "$DOWNLOAD_BASE_URL" in http://?*|https://?*) ;; *) die "DOWNLOAD_BASE_URL 必须是 HTTP/HTTPS 下载地址" ;; esac
# 真实发布（非 prepare-only）必须有远端目标：拒绝任何默认位置，防止误上传。
if [ "$PREPARE_ONLY" != 1 ]; then
    [ -n "$RCLONE_DEST" ] || die "RCLONE_DEST 未配置（真实发布必需，参考 release.env.example；不提供默认目标防误传）"
fi
# 需要构建产物的路径必须配置客户端更新源：不提供默认地址，缺失则拒绝构建发行包。
if [ "$SKIP_BUILD" != 1 ]; then
    [ -n "$UPDATE_MANIFEST_URL" ] || die "UPDATE_MANIFEST_URL 未配置（发行构建的客户端更新源，参考 release.env.example）"
    case "$UPDATE_MANIFEST_URL" in http://?*|https://?*) ;; *) die "UPDATE_MANIFEST_URL 必须是 HTTP/HTTPS 地址" ;; esac
fi
[ "$SKIP_BUILD" != 1 ] || [ -z "$NOTES_FILE" ] || die "--skip-build 复用已有清单，不能同时指定 --notes-file"
[ -z "$NOTES_FILE" ]        || [ -f "$NOTES_FILE" ]        || die "--notes-file 文件不存在: ${NOTES_FILE}"
[ -z "$UPDATER_SIGNING_KEY" ] || [ -f "$UPDATER_SIGNING_KEY" ] || die "签名私钥不存在: ${UPDATER_SIGNING_KEY}"
if [ -n "$UPDATER_SIGNING_KEY" ] && [ ! -f "${UPDATER_SIGNING_KEY}.pub" ]; then
    die "找不到公钥 ${UPDATER_SIGNING_KEY}.pub（wails3 updater genkey 会把它生成在私钥旁）"
fi

# ---- 构建（或核验既有产物）===

check_plist() {  # $1=.app：CFBundleShortVersionString 必须等于 VERSION
    local v
    v="$(plutil -extract CFBundleShortVersionString raw "$1/Contents/Info.plist" 2>/dev/null || true)"
    [ "$v" = "$VERSION" ] || die "$1 的 Info.plist 版本=${v:-<无>} ≠ ${VERSION}"
}

check_manifest() {  # 校验 REL_DIR/manifest.json：版本、channel、恰好 3 产物、URL 匹配
    local m="${REL_DIR}/${F_MANIFEST}" man_ver want
    [ -f "$m" ] || die "缺少 ${m}"
    man_ver="$(sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' "$m" | head -1)"
    [ "$man_ver" = "$VERSION" ] || die "manifest 版本=${man_ver:-<无>} ≠ ${VERSION}"
    grep -Fq '"channel": "stable"' "$m" || die "manifest channel 应为 stable"
    [ "$(grep -c '"digest"' "$m")" = 3 ] || die "manifest 产物应恰好 3 个"
    for want in "$F_ARM64_ZIP" "$F_AMD64_ZIP" "$F_WIN_EXE"; do
        grep -Fq "\"url\": \"${DOWNLOAD_BASE_URL}/releases/${VERSION}/${want}\"" "$m" \
            || die "manifest 缺少/URL 不匹配: ${want}"
    done
}

if [ "$SKIP_BUILD" = 1 ]; then
    info "跳过构建，核验既有产物 → ${REL_DIR}"
    [ -d "$REL_DIR" ] || die "产物目录不存在: ${REL_DIR}（先运行一次构建或去掉 --skip-build）"
    for f in "$F_ARM64_ZIP" "$F_AMD64_ZIP" "$F_WIN_EXE" "$F_LICENSES_ZIP" "$F_MANIFEST" \
             "${APP_NAME}-${VERSION}-darwin-arm64.app" "${APP_NAME}-${VERSION}-darwin-amd64.app"; do
        [ -e "${REL_DIR}/${f}" ] || die "产物缺失: ${REL_DIR}/${f}"
    done
    # app 内许可文档与 Windows 许可包都必须存在，不能静默复用不完整旧产物。
    for app in "${APP_NAME}-${VERSION}-darwin-arm64.app" "${APP_NAME}-${VERSION}-darwin-amd64.app"; do
        for f in LICENSE THIRD_PARTY_NOTICES.md; do
            [ -f "${REL_DIR}/${app}/Contents/Resources/${f}" ] || die "app 许可文档缺失: ${REL_DIR}/${app}/Contents/Resources/${f}"
        done
        [ -d "${REL_DIR}/${app}/Contents/Resources/licenses" ] || die "app licenses/ 缺失: ${REL_DIR}/${app}"
    done
    unzip -Z -1 "${REL_DIR}/${F_LICENSES_ZIP}" | grep -qx LICENSE || die "许可文档包缺少 LICENSE: ${REL_DIR}/${F_LICENSES_ZIP}"
    unzip -Z -1 "${REL_DIR}/${F_LICENSES_ZIP}" | grep -qx THIRD_PARTY_NOTICES.md || die "许可文档包缺少 THIRD_PARTY_NOTICES.md: ${REL_DIR}/${F_LICENSES_ZIP}"
    unzip -Z -1 "${REL_DIR}/${F_LICENSES_ZIP}" | grep -q '^licenses/.*\.txt$' || die "许可文档包缺少 licenses/*.txt: ${REL_DIR}/${F_LICENSES_ZIP}"
    check_manifest
    check_plist "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-arm64.app"
    check_plist "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-amd64.app"
    info "既有产物核验通过（复用 manifest，不重新生成）"
else
    printf '%s\n' "$VERSION" > VERSION
    info "VERSION → ${VERSION}"
    UPDATE_MANIFEST_URL="$UPDATE_MANIFEST_URL" bash build.sh
    info "收集产物 → ${REL_DIR}"
    rm -rf "$REL_DIR"
    mkdir -p "$REL_DIR"
    cp -R "${BIN_DIR}/${APP_NAME}-darwin-arm64.app" "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-arm64.app"
    cp -R "${BIN_DIR}/${APP_NAME}-darwin-amd64.app" "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-amd64.app"
    cp    "${BIN_DIR}/${APP_NAME}-windows-amd64.exe" "${REL_DIR}/${F_WIN_EXE}"
    # ditto 避免 __MACOSX / 多顶层目录；--norsrc 丢弃资源分支
    ditto -c -k --keepParent --norsrc "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-arm64.app" "${REL_DIR}/${F_ARM64_ZIP}"
    ditto -c -k --keepParent --norsrc "${REL_DIR}/${APP_NAME}-${VERSION}-darwin-amd64.app" "${REL_DIR}/${F_AMD64_ZIP}"

    # Windows 许可文档包与三个安装包独立分发；manifest 保持原有 3 个安装项。
    LICENSE_STAGE_TMP="$(mktemp -d)"
    "$SCRIPT_DIR/scripts/stage-license-docs.sh" "$LICENSE_STAGE_TMP"
    ditto -c -k --norsrc "$LICENSE_STAGE_TMP" "${REL_DIR}/${F_LICENSES_ZIP}"
    rm -rf "$LICENSE_STAGE_TMP"
    LICENSE_STAGE_TMP=""

    info "生成 updater manifest（channel=stable, url-prefix=${DOWNLOAD_BASE_URL}/releases/${VERSION}；只含 3 个安装包）"
    # 更新日志：显式 --notes-file 优先；否则从 CHANGELOG.md 摘取当前版本节
    notes_src="$NOTES_FILE"
    if [ -z "$notes_src" ] && [ -f CHANGELOG.md ]; then
        CHANGELOG_NOTES_TMP="$(mktemp)"
        # 摘取 ^## ${VERSION}$ 节（到下一个 ^## 为止），去首尾空行
        awk -v ver="$VERSION" '
            BEGIN { found = 0 }
            found && /^## / { exit }
            $0 == "## " ver { found = 1; next }
            found { lines[++n] = $0; next }
            END {
                start = 1; while (start <= n && lines[start] == "") start++
                end = n;   while (end >= 1   && lines[end]   == "") end--
                for (i = start; i <= end; i++) print lines[i]
            }
        ' CHANGELOG.md > "$CHANGELOG_NOTES_TMP"
        if [ -s "$CHANGELOG_NOTES_TMP" ]; then
            notes_src="$CHANGELOG_NOTES_TMP"
            info "更新日志：取自 CHANGELOG.md 的 ${VERSION} 节"
        else
            warn "CHANGELOG.md 无 ${VERSION} 节，本次发布不带更新日志"
        fi
    fi
    m_args=(-version "$VERSION" -channel stable
            -url-prefix "${DOWNLOAD_BASE_URL}/releases/${VERSION}"
            -output "${REL_DIR}/${F_MANIFEST}")
    [ -n "$UPDATER_SIGNING_KEY" ] && m_args+=(-key "$UPDATER_SIGNING_KEY")
    [ -n "$notes_src" ]           && m_args+=(-notes-file "$notes_src")
    "$WAILS3" updater manifest "${m_args[@]}" \
        "${REL_DIR}/${F_ARM64_ZIP}" "${REL_DIR}/${F_AMD64_ZIP}" "${REL_DIR}/${F_WIN_EXE}"
    check_manifest
fi

# ---- updater verify（是否签名都执行）==

v_args=(-manifest "${REL_DIR}/${F_MANIFEST}" -dir "$REL_DIR")
[ -n "$UPDATER_SIGNING_KEY" ] && v_args+=(-publickey "${UPDATER_SIGNING_KEY}.pub")
info "updater verify..."
"$WAILS3" updater verify "${v_args[@]}"

# ---- prepare-only 到此为止（仅自动确定版本时读远端，不上传）==

if [ "$PREPARE_ONLY" = 1 ]; then
    info "prepare-only 完成，产物在 ${REL_DIR}/（未上传）"
    exit 0
fi

# ---- 发布：读远端 → 上传 → 核验 → 切 stable ==

info "远端: ${RCLONE_DEST} | 下载根: ${DOWNLOAD_BASE_URL}"
remote_manifest=""
if remote_cat "${REMOTE_REL}/${F_MANIFEST}"; then
    remote_manifest="$REMOTE_OUT"
    if [ "$remote_manifest" != "$(cat "${REL_DIR}/${F_MANIFEST}")" ]; then
        die "远端已存在 ${VERSION} 但 manifest 内容不同：已发布版本不可改写（同版重跑请用 --skip-build，或换新版本号）"
    fi
    info "远端已存在同版本 manifest（内容一致）：将核验并补传缺失文件"
else
    info "远端无 ${VERSION} manifest（该版本首次发布）"
fi

remote_stable=""
if remote_cat "${RCLONE_DEST}/stable.json"; then
    remote_stable="$REMOTE_OUT"
    remote_ver="$(printf '%s' "$remote_stable" | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -1)"
    target_version="$VERSION"
    version_ok "$remote_ver" || die "远端 stable.json 的版本缺失或非法，停止发布"
    remote_ver="$VERSION"
    VERSION="$target_version"
    case "$(semver_cmp "$VERSION" "$remote_ver")" in
        -1) die "版本降级被拒绝: ${VERSION} < 远端 stable ${remote_ver}" ;;
        0)  warn "与远端 stable 同版本（${remote_ver}）：仅核验/补传，不改写已存在文件" ;;
    esac
else
    info "远端无 stable.json（首次发布）"
fi

LIST_TMP="$(mktemp)"

info "上传 4 个分发包（3 安装包 + licenses.zip） → ${REMOTE_REL}/（--immutable 保证已存在且不同的文件报错不改写）"
printf '%s\n' "$F_ARM64_ZIP" "$F_AMD64_ZIP" "$F_WIN_EXE" "$F_LICENSES_ZIP" > "$LIST_TMP"
"$RCLONE_BIN" copy "$REL_DIR" "${REMOTE_REL}/" --immutable --checksum --files-from "$LIST_TMP"

if [ -z "$remote_manifest" ]; then
    info "上传 manifest → ${REMOTE_REL}/${F_MANIFEST}"
    "$RCLONE_BIN" copyto "${REL_DIR}/${F_MANIFEST}" "${REMOTE_REL}/${F_MANIFEST}" --checksum
fi

info "rclone check --download --one-way 核验恰好 5 个版本文件（4 个分发包 + manifest，下载双侧内容比对）"
printf '%s\n' "$F_ARM64_ZIP" "$F_AMD64_ZIP" "$F_WIN_EXE" "$F_LICENSES_ZIP" "$F_MANIFEST" > "$LIST_TMP"
"$RCLONE_BIN" check --download --one-way "$REL_DIR" "${REMOTE_REL}/" --files-from "$LIST_TMP"

info "切换 stable → ${RCLONE_DEST}/stable.json"
"$RCLONE_BIN" copyto "${REL_DIR}/${F_MANIFEST}" "${RCLONE_DEST}/stable.json" \
    --ignore-times --metadata --metadata-set cache-control=no-cache

# ---- 完成 ==

info "发布完成: ${VERSION}"
info "  客户端更新源: ${UPDATE_MANIFEST_URL:-<未注入（更新检查禁用）>}"
info "  下载根: ${DOWNLOAD_BASE_URL}"
info "  版本目录: ${DOWNLOAD_BASE_URL}/releases/${VERSION}/"
