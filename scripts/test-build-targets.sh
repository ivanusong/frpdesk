#!/usr/bin/env bash
# test-build-targets.sh —— build.sh 六平台桌面目标 + Linux CLI 双架构的契约测试
# （mock 工具链，不真实编译）。
#
# 全部在临时 fixture 仓库中运行（只复制 build.sh、打包脚本、build 资源、许可文档与
# workflow 副本；预置 frontend/dist/bindings marker），**绝不写入真实仓库**——真实仓库的
# syso/info.json/bindings 即使测试失败也原样保留，退出时仅清理临时 fixture。
#
# 用桩替换 go / wails3 / plutil / codesign / xattr / ditto / uname，记录调用并伪造产物，
# 验证（不依赖网络与真实工具链，macOS 上即可跑）：
#   1. darwin 主机缺省四目标：darwin/arm64 darwin/amd64 windows/amd64 windows/arm64；
#   2. 显式目标子集只构建所选项；
#   3. windows 各架构各自 generate syso -arch，不串架构；windows 走 CGO_ENABLED=0；
#   4. linux 目标在 darwin 主机被拒绝（CGO + GTK4/webkitgtk 不做伪交叉）；
#   5. 未知目标被拒绝；--print-version 兼容且不触发任何构建；
#   6. 伪 Linux 主机缺省出本机架构 linux 包，CGO_ENABLED=1，且 tar.gz 顶层恰好单一
#      可执行 frpdesk（updater extract.go 硬约束）并保留执行位；
#   7. cli/linux 双架构在 darwin 主机交叉构建：CGO_ENABLED=0、server+production tag、
#      产物名 frpdesk-cli-<版本>-linux-<arch>.tar.gz，包内顶层单一 frpdesk；
#   8. release.yml 契约：引用六平台桌面产物与 CLI 双包、10 个 draft 资产、CLI 不进入
#      updater stable.json、镜像同步包含 CLI 附件；
#   9. 隔离性：bindings rm/copy 只发生在 fixture（marker 被替换），真实仓库状态零变化。
# 用法：bash scripts/test-build-targets.sh
# 退出码：0 全部通过；1 存在失败用例。

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REAL_REPO="$HERE/.."
WORK="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-build-targets.XXXXXX")"
FIXTURE="$WORK/fixture"
MOCKBIN="$WORK/mockbin"
MOCK_LOG="$WORK/mock.log"
PASS=0
FAIL=0

cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

ok() { PASS=$((PASS + 1)); echo "[通过] $*"; }
fail() { FAIL=$((FAIL + 1)); echo "[失败] $*"; }

# ---- fixture 搭建（只写临时目录）--------------------------------=============

mkdir -p "$FIXTURE/scripts" "$FIXTURE/build" "$FIXTURE/assets" \
    "$FIXTURE/.github/workflows" "$FIXTURE/frontend/dist/bindings"
cp "$REAL_REPO/build.sh" "$FIXTURE/"
cp "$REAL_REPO/scripts/package-linux.sh" "$REAL_REPO/scripts/stage-license-docs.sh" "$FIXTURE/scripts/"
cp -R "$REAL_REPO/build/darwin" "$FIXTURE/build/darwin"
cp -R "$REAL_REPO/build/windows" "$FIXTURE/build/windows"
cp "$REAL_REPO/assets/icon.svg" "$FIXTURE/assets/"
cp "$REAL_REPO/LICENSE" "$REAL_REPO/THIRD_PARTY_NOTICES.md" "$FIXTURE/"
cp -R "$REAL_REPO/licenses" "$FIXTURE/licenses"
cp "$REAL_REPO/.github/workflows/release.yml" "$REAL_REPO/.github/workflows/build.yml" \
    "$FIXTURE/.github/workflows/"
# bindings marker：证明 rm -rf + 复制只发生在 fixture 内部
echo "must-be-replaced" > "$FIXTURE/frontend/dist/bindings/marker.txt"

# 真实仓库不变性的只读基线：gitignored 的 bindings 目录也纳入内容摘要，
# （git status 对忽略路径是盲的），全部只读、绝不写入。
path_digest() {
    local p="$1" files
    if [ ! -e "$REAL_REPO/$p" ]; then
        echo absent
        return
    fi
    files="$(cd "$REAL_REPO" && find "$p" -type f 2>/dev/null | LC_ALL=C sort)"
    if [ -z "$files" ]; then
        echo empty
        return
    fi
    if [ -d "$REAL_REPO/$p" ]; then
        (cd "$REAL_REPO" && printf '%s\n' "$files" | xargs shasum -a 256 | shasum -a 256 | awk '{print $1}')
    else
        (cd "$REAL_REPO" && shasum -a 256 "$p" | awk '{print $1}')
    fi
}
real_state() {
    local p
    for p in frontend/bindings frontend/dist/bindings frontend/dist/icon.svg \
        build/windows/info.json rsrc_windows_amd64.syso rsrc_windows_arm64.syso; do
        echo "${p}=$(path_digest "$p")"
    done
    git -C "$REAL_REPO" status --porcelain -- \
        build/windows/info.json rsrc_windows_amd64.syso rsrc_windows_arm64.syso
}
real_state > "$WORK/real-before.txt"

# ---- 工具桩 ------------------------------------------------------------------

mkdir -p "$MOCKBIN"

cat > "$MOCKBIN/go" <<'EOF'
#!/usr/bin/env bash
out=""; prev=""
for a in "$@"; do
    [ "$prev" = "-o" ] && out="$a"
    prev="$a"
done
echo "go GOOS=${GOOS:-} GOARCH=${GOARCH:-} CGO_ENABLED=${CGO_ENABLED:-} out=${out}" >> "$MOCK_LOG"
echo "go-args $*" >> "$MOCK_LOG"
if [ -n "$out" ]; then
    mkdir -p "$(dirname "$out")"
    : > "$out"
fi
EOF

cat > "$MOCKBIN/wails3" <<'EOF'
#!/usr/bin/env bash
case "${1:-}/${2:-}" in
    generate/bindings)
        echo "wails3 bindings" >> "$MOCK_LOG"
        mkdir -p frontend/bindings
        ;;
    generate/syso)
        arch=""; out=""; prev=""
        for a in "$@"; do
            [ "$prev" = "-arch" ] && arch="$a"
            [ "$prev" = "-out" ] && out="$a"
            prev="$a"
        done
        echo "wails3 syso arch=${arch} out=${out}" >> "$MOCK_LOG"
        [ -n "$out" ] && : > "$out"
        ;;
    *)
        echo "wails3 unexpected: $*" >> "$MOCK_LOG"
        exit 1
        ;;
esac
EOF

cat > "$MOCKBIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
    -s) echo "${MOCK_UNAME_S:-Darwin}" ;;
    -m) echo "${MOCK_UNAME_M:-arm64}" ;;
    *) echo "${MOCK_UNAME_S:-Darwin}" ;;
esac
EOF

for tool in plutil codesign xattr; do
    cat > "$MOCKBIN/$tool" <<EOF
#!/usr/bin/env bash
echo "$tool \$*" >> "\$MOCK_LOG"
exit 0
EOF
done

cat > "$MOCKBIN/ditto" <<'EOF'
#!/usr/bin/env bash
# 末参为输出 zip，伪造产物
last=""; for a in "$@"; do last="$a"; done
echo "ditto $*" >> "$MOCK_LOG"
[ -n "$last" ] && { : > "$last"; }
exit 0
EOF

chmod +x "$MOCKBIN"/*

# 桩只验证调用契约；版本注入的真实产物由本机实际构建覆盖（见任务验收）。

run_build() { # $1=BIN目录 其余=build.sh 参数；stdout 丢弃，日志在 MOCK_LOG
    local bin_dir="$1"; shift
    (cd "$FIXTURE" && PATH="$MOCKBIN:$PATH" MOCK_LOG="$MOCK_LOG" \
        FRPDESK_VERSION=9.9.9 FRPDESK_BIN_DIR="$bin_dir" \
        bash "$FIXTURE/build.sh" "$@" >/dev/null 2>"$WORK/stderr.log")
}

expect_fail() { # $1=描述 $2=stderr 应包含片段 其余=build.sh 参数
    local desc="$1" want="$2" bin_dir="$WORK/bin-$RANDOM" rc=0
    shift 2
    (cd "$FIXTURE" && PATH="$MOCKBIN:$PATH" MOCK_LOG="$MOCK_LOG" \
        FRPDESK_VERSION=9.9.9 FRPDESK_BIN_DIR="$bin_dir" \
        bash "$FIXTURE/build.sh" "$@" >/dev/null 2>"$WORK/stderr.log") || rc=$?
    if [ "$rc" -ne 0 ] && grep -q "$want" "$WORK/stderr.log"; then
        ok "$desc"
    else
        fail "$desc（rc=${rc}，stderr=$(head -c 200 "$WORK/stderr.log")）"
    fi
}

# ---- 用例 --------------------------------------------------------------------

echo "== darwin 主机缺省四目标 =="
: > "$MOCK_LOG"
bin1="$WORK/bin-default"
if run_build "$bin1"; then
    ok "缺省构建成功"
else
    fail "缺省构建成功（stderr=$(head -c 200 "$WORK/stderr.log")）"
fi
for want in \
    "go GOOS=darwin GOARCH=arm64 CGO_ENABLED=1" \
    "go GOOS=darwin GOARCH=amd64 CGO_ENABLED=1" \
    "go GOOS=windows GOARCH=amd64 CGO_ENABLED=0" \
    "go GOOS=windows GOARCH=arm64 CGO_ENABLED=0" \
    "wails3 syso arch=amd64 out=rsrc_windows_amd64.syso" \
    "wails3 syso arch=arm64 out=rsrc_windows_arm64.syso"; do
    if grep -qF "$want" "$MOCK_LOG"; then
        ok "调用契约：$want"
    else
        fail "调用契约：$want"
    fi
done
if grep -q "GOOS=linux" "$MOCK_LOG"; then
    fail "darwin 主机缺省不得构建 linux"
else
    ok "darwin 主机缺省不构建 linux"
fi
for f in \
    "$bin1/frpdesk-9.9.9-darwin-arm64.zip" \
    "$bin1/frpdesk-9.9.9-darwin-amd64.zip" \
    "$bin1/frpdesk-9.9.9-windows-amd64.exe" \
    "$bin1/frpdesk-9.9.9-windows-arm64.exe"; do
    [ -f "$f" ] && ok "产物存在：$(basename "$f")" || fail "产物存在：$(basename "$f")"
done
# fixture 内 bindings marker 必须已被 rm -rf + 复制流程替换
if [ ! -e "$FIXTURE/frontend/dist/bindings/marker.txt" ] && [ -d "$FIXTURE/frontend/dist/bindings" ]; then
    ok "bindings rm/copy 只发生在 fixture（marker 已被替换）"
else
    fail "bindings rm/copy 只发生在 fixture（marker 已被替换）"
fi

echo "== 显式目标子集 =="
: > "$MOCK_LOG"
bin2="$WORK/bin-subset"
run_build "$bin2" windows/arm64
if grep -q "GOARCH=arm64 CGO_ENABLED=0" "$MOCK_LOG" && [ "$(grep -c '^go ' "$MOCK_LOG")" -eq 1 ]; then
    ok "显式 windows/arm64 只构建一个目标"
else
    fail "显式 windows/arm64 只构建一个目标（log=$(cat "$MOCK_LOG")）"
fi
if grep -q "wails3 syso arch=arm64" "$MOCK_LOG" && ! grep -q "syso arch=amd64" "$MOCK_LOG"; then
    ok "syso 只生成 arm64，不串架构"
else
    fail "syso 只生成 arm64，不串架构（log=$(cat "$MOCK_LOG")）"
fi
[ -f "$bin2/frpdesk-9.9.9-windows-arm64.exe" ] && ok "显式目标产物存在" || fail "显式目标产物存在"

echo "== 目标/host 约束 =="
expect_fail "linux 目标在 darwin 主机被拒绝" "只能在 Linux 主机构建" linux/amd64
expect_fail "未知目标被拒绝" "未知构建目标" bogus/target

echo "== 服务端 CLI 交叉构建（darwin 主机 → linux 双架构）=="
: > "$MOCK_LOG"
bin_cli="$WORK/bin-cli"
if run_build "$bin_cli" cli/linux/amd64 cli/linux/arm64; then
    ok "CLI 双架构构建成功"
else
    fail "CLI 双架构构建失败（stderr=$(head -c 200 "$WORK/stderr.log")）"
fi
for arch in amd64 arm64; do
    if grep -qF "go GOOS=linux GOARCH=${arch} CGO_ENABLED=0" "$MOCK_LOG"; then
        ok "CLI ${arch}：GOOS=linux CGO_ENABLED=0"
    else
        fail "CLI ${arch}：GOOS=linux CGO_ENABLED=0"
    fi
    targz="$bin_cli/frpdesk-cli-9.9.9-linux-${arch}.tar.gz"
    [ -f "$targz" ] && ok "CLI ${arch} 产物存在：$(basename "$targz")" || fail "CLI ${arch} 产物存在"
    if [ -f "$targz" ] && [ "$(tar -tzf "$targz" 2>/dev/null)" = "frpdesk" ]; then
        ok "CLI ${arch} tar.gz 顶层恰好单一 frpdesk"
    else
        fail "CLI ${arch} tar.gz 顶层恰好单一 frpdesk（实际：$(tar -tzf "$targz" 2>/dev/null | tr '\n' ' ')）"
    fi
done
if [ "$(grep -c -- "-tags server production" "$MOCK_LOG")" -eq 2 ] &&
    ! grep -q "wails3 syso" "$MOCK_LOG"; then
    ok "CLI 使用 server+production tag 且不生成 windows syso"
else
    fail "CLI 使用 server+production tag 且不生成 windows syso（log=$(cat "$MOCK_LOG")）"
fi

echo "== --print-version 兼容 =="
: > "$MOCK_LOG"
v="$(cd "$FIXTURE" && PATH="$MOCKBIN:$PATH" FRPDESK_VERSION=9.9.9 bash "$FIXTURE/build.sh" --print-version)"
if [ "$v" = "9.9.9" ] && [ ! -s "$MOCK_LOG" ]; then
    ok "--print-version 只解析版本，不触发构建"
else
    fail "--print-version 只解析版本，不触发构建（v=$v，log=$(cat "$MOCK_LOG")）"
fi

echo "== 伪 Linux 主机：缺省本机架构 + tar.gz 单一顶层 =="
: > "$MOCK_LOG"
bin3="$WORK/bin-linux"
(cd "$FIXTURE" && PATH="$MOCKBIN:$PATH" MOCK_LOG="$MOCK_LOG" \
    MOCK_UNAME_S=Linux MOCK_UNAME_M=x86_64 \
    FRPDESK_VERSION=9.9.9 FRPDESK_BIN_DIR="$bin3" \
    bash "$FIXTURE/build.sh" >/dev/null 2>"$WORK/stderr-linux.log") || fail "伪 Linux 主机构建失败"
if grep -qF "go GOOS=linux GOARCH=amd64 CGO_ENABLED=1" "$MOCK_LOG"; then
    ok "linux 目标 CGO_ENABLED=1"
else
    fail "linux 目标 CGO_ENABLED=1（log=$(cat "$MOCK_LOG")）"
fi
if grep -q "wails3 syso" "$MOCK_LOG"; then
    fail "linux 构建不应生成 windows syso"
else
    ok "linux 构建不生成 windows syso"
fi
targz="$bin3/frpdesk-9.9.9-linux-amd64.tar.gz"
if [ -f "$targz" ]; then
    ok "linux tar.gz 产物存在"
else
    fail "linux tar.gz 产物存在"
fi
if [ "$(tar -tzf "$targz" 2>/dev/null | grep -c .)" -eq 1 ] &&
    [ "$(tar -tzf "$targz" 2>/dev/null)" = "frpdesk" ]; then
    ok "tar.gz 顶层恰好单一 frpdesk（updater extract.go 约束）"
else
    fail "tar.gz 顶层恰好单一 frpdesk（实际：$(tar -tzf "$targz" 2>/dev/null | tr '\n' ' ')）"
fi
vdir="$WORK/verify-linux"
mkdir -p "$vdir"
tar -xzf "$targz" -C "$vdir"
if [ -x "$vdir/frpdesk" ]; then
    ok "tar.gz 内 frpdesk 保留执行位"
else
    fail "tar.gz 内 frpdesk 保留执行位"
fi

echo "== release.yml 资产契约 =="
contract=0
for token in \
    "frpdesk-\${VERSION}-darwin-arm64.zip" \
    "frpdesk-\${VERSION}-darwin-amd64.zip" \
    "frpdesk-\${VERSION}-windows-amd64.exe" \
    "frpdesk-\${VERSION}-windows-arm64.exe" \
    "frpdesk-\${VERSION}-linux-amd64.tar.gz" \
    "frpdesk-\${VERSION}-linux-arm64.tar.gz" \
    "frpdesk-cli-\${VERSION}-linux-amd64.tar.gz" \
    "frpdesk-cli-\${VERSION}-linux-arm64.tar.gz" \
    "frpdesk-\${VERSION}-licenses.zip" \
    "stable.json"; do
    if grep -qF "$token" "$FIXTURE/.github/workflows/release.yml"; then
        contract=$((contract + 1))
    else
        fail "release.yml 缺少资产契约：$token"
    fi
done
if [ "$contract" -eq 10 ]; then
    ok "release.yml 资产契约齐全（6 桌面包 + CLI 双包 + licenses.zip + stable.json）"
fi
# CLI 制品只随 Release 分发，绝不进入桌面 updater 清单
if sed -n '/wails3 updater manifest/,/wails3 updater verify/p' "$FIXTURE/.github/workflows/release.yml" | grep -q "frpdesk-cli"; then
    fail "CLI 制品混入了桌面 updater stable.json 清单"
else
    ok "CLI 制品未进入桌面 updater stable.json"
fi
if grep -qF "核验 Linux CLI 归档" "$FIXTURE/.github/workflows/release.yml" &&
    grep -qF "ARM aarch64" "$FIXTURE/.github/workflows/release.yml"; then
    ok "release.yml 核验 CLI 归档（单一顶层/可执行位/目标架构）"
else
    fail "release.yml 缺少 CLI 归档核验步骤"
fi
# 镜像同步必须显式携带 CLI 附件（不在 stable.json 中，不会被镜像脚本自动发现）
for arch in amd64 arm64; do
    token="--extra-file \"mirror-staging/frpdesk-cli-\${VERSION}-linux-${arch}.tar.gz\""
    if grep -qF -- "$token" "$FIXTURE/.github/workflows/release.yml"; then
        ok "镜像同步包含 CLI 附件：linux-${arch}"
    else
        fail "镜像同步缺少 CLI 附件：linux-${arch}"
    fi
done
for slug in linux/amd64 linux/arm64 cli/linux/amd64 cli/linux/arm64; do
    if grep -qF "$slug" "$FIXTURE/.github/workflows/build.yml" && grep -qF "$slug" "$FIXTURE/.github/workflows/release.yml"; then
        ok "workflow 覆盖目标 $slug"
    else
        fail "workflow 覆盖目标 $slug"
    fi
done

echo "== 真实仓库隔离性 =="
real_state > "$WORK/real-after.txt"
if diff -u "$WORK/real-before.txt" "$WORK/real-after.txt" >/dev/null; then
    ok "真实仓库 syso/info.json/bindings 状态零变化"
else
    fail "真实仓库状态发生变化：$(diff -u "$WORK/real-before.txt" "$WORK/real-after.txt" | head -5)"
fi
if [ -e "$REAL_REPO/frontend/dist/bindings" ] && grep -rq "must-be-replaced" "$REAL_REPO/frontend/dist/bindings" 2>/dev/null; then
    fail "marker 泄漏到真实仓库"
else
    ok "marker 未泄漏到真实仓库"
fi

echo
echo "[汇总] 通过 ${PASS}，失败 ${FAIL}"
[ "$FAIL" -eq 0 ]
