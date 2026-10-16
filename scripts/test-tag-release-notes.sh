#!/usr/bin/env bash
# test-tag-release-notes.sh —— scripts/tag-release-notes.sh 的离线验证。
#
# 在临时 git 仓库中构造 annotated/轻量 tag，覆盖描述提取与失败路径：
#   1. subject + body → 取 body（多行列表完整保留）；
#   2. 仅 subject → 取 subject；
#   3. 无空行的纯多行列表 → 整段保留；
#   4. 首尾空行去除；
#   5. 轻量 tag → 失败且提示 git tag -a；
#   6. annotated tag 描述为空 → 失败；
#   7. GITHUB_REF_NAME 默认取值与默认输出文件名 release-notes.md。
# 不依赖网络与第三方工具。用法：bash scripts/test-tag-release-notes.sh
# 退出码：0 全部通过；1 存在失败用例。

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/tag-release-notes.sh"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-tag-notes-test.XXXXXX")"
PASS=0
FAIL=0

cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

ok() { PASS=$((PASS + 1)); echo "[通过] $*"; }
fail() { FAIL=$((FAIL + 1)); echo "[失败] $*"; }

repo="$WORKDIR/repo"
git init -q "$repo"
git -C "$repo" config user.email test@example.com
git -C "$repo" config user.name tester
git -C "$repo" config tag.gpgsign false
git -C "$repo" commit -q --allow-empty -m init
cd "$repo"

# 提取成功用例：$1=描述 $2=tag $3=期望正文
expect_notes() {
    local desc="$1" tag="$2" want="$3" got rc=0
    got="$(bash "$SCRIPT" "$tag" - 2>/dev/null)" || rc=$?
    if [ "$rc" -eq 0 ] && [ "$got" = "$want" ]; then
        ok "$desc"
    else
        fail "${desc}（rc=${rc}，got=$(printf '%q' "$got")）"
    fi
}

# 失败用例：$1=描述 $2=tag；要求非零退出且 stderr 含 git tag -a 提示
expect_fail() {
    local desc="$1" tag="$2" err rc=0
    err="$(bash "$SCRIPT" "$tag" - 2>&1 >/dev/null)" || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$err" == *"git tag -a"* ]]; then
        ok "$desc"
    else
        fail "${desc}（rc=${rc}，stderr=$(printf '%q' "$err")）"
    fi
}

git tag -a v-body -m $'Frpdesk v0.2.12\n\n- 新增：多行描述\n- 修复：列表保留'
expect_notes "subject + body 时取 body 且保留多行列表" v-body $'- 新增：多行描述\n- 修复：列表保留'

git tag -a v-subject -m '仅 subject 的描述'
expect_notes "body 为空时回退 subject" v-subject '仅 subject 的描述'

git tag -a v-list -m $'- 纯列表第一行\n- 纯列表第二行'
expect_notes "无空行的多行列表整段保留" v-list $'- 纯列表第一行\n- 纯列表第二行'

git tag -a v-trim -m $'subject\n\n- 首尾空行外的正文\n\n\n'
expect_notes "描述去除首尾空行" v-trim '- 首尾空行外的正文'

git tag v-light
expect_fail "轻量 tag 发布失败" v-light

git tag -a v-empty -m '   '
expect_fail "描述为空的 annotated tag 发布失败" v-empty

rm -f release-notes.md
if GITHUB_REF_NAME=v-body bash "$SCRIPT" >/dev/null 2>&1 &&
    [ "$(cat release-notes.md)" = $'- 新增：多行描述\n- 修复：列表保留' ]; then
    ok "未显式传 tag 时读取 GITHUB_REF_NAME 并默认写入 release-notes.md"
else
    fail "未显式传 tag 时读取 GITHUB_REF_NAME 并默认写入 release-notes.md"
fi

echo
echo "[汇总] 通过 ${PASS}，失败 ${FAIL}"
[ "$FAIL" -eq 0 ]
