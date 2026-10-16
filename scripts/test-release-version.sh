#!/usr/bin/env bash
# test-release-version.sh —— tag-only 版本解析的离线验证。
#
# 覆盖：
#   1. scripts/tag-version.sh 从参数或 GITHUB_REF_NAME 解析 vX.Y.Z，输出 x.y.z；
#   2. 非「v + 三段数字」的 tag（缺 v、两段、带后缀、多余段、空）必须失败并提示格式；
#   3. build.sh --print-version 显式 FRPDESK_VERSION 覆盖（v 前缀归一化）；
#   4. 未显式设置时回退 git describe --tags --always --dirty，与仓库实际一致；
#   5. 仓库内不再存在 VERSION 文件，且 build.sh 不再读取它。
# 不依赖网络与第三方工具。用法：bash scripts/test-release-version.sh
# 退出码：0 全部通过；1 存在失败用例。

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
TAG_VERSION="$HERE/tag-version.sh"
BUILD_SH="$REPO/build.sh"
PASS=0
FAIL=0

ok() { PASS=$((PASS + 1)); echo "[通过] $*"; }
fail() { FAIL=$((FAIL + 1)); echo "[失败] $*"; }

# 成功用例：$1=描述 $2=期望 $3...=命令
expect_out() {
    local desc="$1" want="$2" got rc=0
    shift 2
    got="$("$@" 2>/dev/null)" || rc=$?
    if [ "$rc" -eq 0 ] && [ "$got" = "$want" ]; then
        ok "$desc"
    else
        fail "${desc}（rc=${rc}，got=$(printf '%q' "$got")）"
    fi
}

# 失败用例：$1=描述 $2=stderr 应包含的片段 其余=命令；要求非零退出
# bash 3.2 变量名遇中文需用 ${}（$1 等带花括号不适用，故先取局部变量）
expect_fail() {
    local desc="$1" want="$2" err rc=0
    shift 2
    err="$("$@" 2>&1 >/dev/null)" || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$err" == *"$want"* ]]; then
        ok "$desc"
    else
        fail "${desc}（rc=${rc}，stderr=$(printf '%q' "$err")）"
    fi
}

echo "== tag-version.sh =="
expect_out "显式参数解析 v0.3.0" "0.3.0" bash "$TAG_VERSION" v0.3.0
expect_out "GITHUB_REF_NAME 解析 v0.3.1" "0.3.1" env GITHUB_REF_NAME=v0.3.1 bash "$TAG_VERSION"
expect_out "显式参数优先于 GITHUB_REF_NAME" "0.3.2" env GITHUB_REF_NAME=v9.9.9 bash "$TAG_VERSION" v0.3.2

expect_fail "缺少 tag 失败" "缺少 tag" env -u GITHUB_REF_NAME bash "$TAG_VERSION"
expect_fail "缺少 v 前缀失败" "v 前缀加三段数字" bash "$TAG_VERSION" 0.3.0
expect_fail "两段版本失败" "v 前缀加三段数字" bash "$TAG_VERSION" v0.3
expect_fail "带后缀失败" "v 前缀加三段数字" bash "$TAG_VERSION" v0.3.0-rc1
expect_fail "四段版本失败" "v 前缀加三段数字" bash "$TAG_VERSION" v0.3.0.1

echo "== build.sh 版本解析 =="
expect_out "FRPDESK_VERSION 显式覆盖" "1.2.3" env FRPDESK_VERSION=1.2.3 bash "$BUILD_SH" --print-version
expect_out "FRPDESK_VERSION 支持 v 前缀" "1.2.4" env FRPDESK_VERSION=v1.2.4 bash "$BUILD_SH" --print-version

want="$(git -C "$REPO" describe --tags --always --dirty 2>/dev/null || echo dev)"
want="${want#v}"
expect_out "未显式设置回退 git describe/dev" "$want" env -u FRPDESK_VERSION bash "$BUILD_SH" --print-version

if [ -e "$REPO/VERSION" ]; then
    fail "仓库内 VERSION 文件应已删除"
else
    ok "仓库内 VERSION 文件已删除"
fi
if grep -qE 'cat +VERSION|< *VERSION|tr +-d.*VERSION' "$BUILD_SH"; then
    fail "build.sh 仍在读取 VERSION 文件"
else
    ok "build.sh 不再读取 VERSION 文件"
fi

echo
echo "[汇总] 通过 ${PASS}，失败 ${FAIL}"
[ "$FAIL" -eq 0 ]