#!/usr/bin/env bash
# tag-release-notes.sh —— 从 annotated tag 的描述生成 release-notes.md。
#
# 发布日志的唯一来源是 annotated tag 的 message（tag 描述即发布说明，无需另建文件）。提取规则：
#   1. tag 必须是 annotated（轻量 tag 直接失败，提示用 git tag -a 创建）；
#   2. message 含 subject 与 body 时优先取 body（以首个空行分隔），body 为空时回退
#      subject；纯多行列表（无空行）整段保留；
#   3. 去掉首尾空行；描述为空视为发布前置校验失败。
#
# 用法: scripts/tag-release-notes.sh [tag] [输出文件]
#   tag       默认取 GITHUB_REF_NAME；本地验证可显式传入
#   输出文件  默认 release-notes.md；传 - 输出到 stdout
# 退出码: 0 成功；1 tag 类型/描述校验失败。

set -euo pipefail

TAG="${1:-${GITHUB_REF_NAME:-}}"
OUTPUT="${2:-release-notes.md}"

die() { echo "[tag-notes] $*" >&2; exit 1; }

[ -n "$TAG" ] || die "缺少 tag：请设置 GITHUB_REF_NAME 或传入 tag，例如 scripts/tag-release-notes.sh v0.2.12"

if ! git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null 2>&1; then
    die "当前仓库不存在 tag ${TAG}；发布需先创建 annotated tag：git tag -a ${TAG} -m \"<发布说明>\""
fi

object_type="$(git cat-file -t "refs/tags/${TAG}")"
if [ "$object_type" != "tag" ]; then
    die "tag ${TAG} 不是 annotated tag（objecttype=${object_type}）；请用 git tag -a ${TAG} -m \"<发布说明>\" 创建带描述的 annotated tag"
fi

# %(contents) 为完整 tag message（不含 tag 头与签名），多行原样保留
message="$(git for-each-ref "refs/tags/${TAG}" --format='%(contents)')"

notes="$(
    printf '%s\n' "$message" | awk '
        { lines[++n] = $0 }
        END {
            start = 1; while (start <= n && lines[start] ~ /^[[:space:]]*$/) start++
            end = n;   while (end >= 1   && lines[end]   ~ /^[[:space:]]*$/) end--
            if (start > end) exit

            blank = 0
            for (i = start; i <= end; i++) {
                if (lines[i] ~ /^[[:space:]]*$/) { blank = i; break }
            }
            if (blank == 0) {                 # 无空行：整段作为描述，多行列表完整保留
                for (i = start; i <= end; i++) print lines[i]
            } else {
                body = blank + 1
                while (body <= end && lines[body] ~ /^[[:space:]]*$/) body++
                if (body <= end) {            # 有 body：优先 body
                    for (i = body; i <= end; i++) print lines[i]
                } else {                      # body 为空：回退 subject 段
                    for (i = start; i < blank; i++) print lines[i]
                }
            }
        }
    '
)"

[ -n "$notes" ] || die "tag ${TAG} 的描述为空；请用 git tag -a ${TAG} -m \"<发布说明>\"（或 -F <notes-file>）写入发布日志"

if [ "$OUTPUT" = "-" ]; then
    printf '%s\n' "$notes"
else
    printf '%s\n' "$notes" > "$OUTPUT"
    echo "[tag-notes] 已从 annotated tag ${TAG} 生成 ${OUTPUT}"
fi
