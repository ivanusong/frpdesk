#!/usr/bin/env bash
# tag-version.sh —— 从 annotated tag 解析发布版本（唯一版本来源，不读取仓库内版本文件）。
#
# 输入优先级：显式参数 > GITHUB_REF_NAME（CI tag 事件）。
# 只接受 v 前缀 + 三段数字（如 v0.3.0），输出规范化的 x.y.z；
# 格式非法或缺少 tag 时非零退出，调用方应视为发布前置校验失败。
#
# 用法: scripts/tag-version.sh [vX.Y.Z]
#   release 与 sync-mirror 作业共用本脚本，避免各自重复解析。

set -euo pipefail

TAG="${1:-${GITHUB_REF_NAME:-}}"

die() { echo "[tag-version] $*" >&2; exit 1; }

[ -n "$TAG" ] || die "缺少 tag：请设置 GITHUB_REF_NAME 或传入 tag，例如 scripts/tag-version.sh v0.3.0"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "tag 版本格式非法：${TAG}（期望 v 前缀加三段数字，如 v0.3.0）"
fi

printf '%s\n' "${TAG#v}"