#!/usr/bin/env bash
# 从 CHANGELOG.md 摘取指定版本的更新日志正文（到下一个版本节为止，去首尾空行），
# 供发布工作流生成 GitHub Release notes 与 updater manifest 的 notes 字段复用。
# 用法: scripts/release-notes.sh <x.y.z> [CHANGELOG.md]
# 版本节缺失或正文为空时以非零退出，调用方应视为发布前置校验失败。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

VERSION="${1:?用法: $0 <x.y.z> [CHANGELOG.md]}"
CHANGELOG="${2:-$ROOT/CHANGELOG.md}"

[ -f "$CHANGELOG" ] || { echo "[notes] 缺少 $CHANGELOG" >&2; exit 1; }

notes="$(
    awk -v ver="$VERSION" '
        $0 == "## " ver { found = 1; next }
        found && /^## / { exit }
        found { lines[++n] = $0 }
        END {
            start = 1; while (start <= n && lines[start] == "") start++
            end = n;   while (end >= 1   && lines[end]   == "") end--
            for (i = start; i <= end; i++) print lines[i]
        }
    ' "$CHANGELOG"
)"

[ -n "$notes" ] || { echo "[notes] $CHANGELOG 中没有 $VERSION 的版本节（或正文为空）" >&2; exit 1; }

printf '%s\n' "$notes"
