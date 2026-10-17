#!/usr/bin/env bash
# package-linux.sh —— 把 Linux 可执行文件打包为 updater 兼容的 tar.gz。
#
# 硬约束（third_party/wails/pkg/updater/extract.go）：更新归档必须恰好包含一个
# 顶层条目，非 darwin 平台的更新是替换单个文件。因此本脚本只把单个可执行
# `frpdesk` 放进归档顶层——不带目录层、不带许可文档（许可走共享 licenses.zip）。
# 可执行位写入 tar 条目；更新替换后 helper 还会按旧二进制恢复权限，双保险。
#
# 用法: scripts/package-linux.sh <可执行文件> <输出.tar.gz>
# 退出码: 0 成功；1 输入/自检失败。

set -euo pipefail

APP_NAME="frpdesk"

BIN="${1:?用法: $0 <可执行文件> <输出.tar.gz>}"
OUT="${2:?用法: $0 <可执行文件> <输出.tar.gz>}"

[ -f "$BIN" ] || { echo "[package-linux] 可执行文件不存在：$BIN" >&2; exit 1; }

stage="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-linux-pkg.XXXXXX")"
cleanup() { rm -rf "$stage"; }
trap cleanup EXIT

cp "$BIN" "$stage/${APP_NAME}"
chmod 0755 "$stage/${APP_NAME}"

# -C 限定打包根，保证 tar.gz 顶层只有 frpdesk 一个条目
tar -czf "$OUT" -C "$stage" "${APP_NAME}"

# 自检：恰好一个顶层条目且名为 frpdesk，解包后可执行位在位
entries="$(tar -tzf "$OUT")"
if [ "$(printf '%s\n' "$entries" | grep -c .)" -ne 1 ] || [ "$entries" != "${APP_NAME}" ]; then
    echo "[package-linux] 自检失败：归档顶层条目必须只有 ${APP_NAME}，实际：$entries" >&2
    exit 1
fi
verify_dir="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-linux-verify.XXXXXX")"
tar -xzf "$OUT" -C "$verify_dir"
if [ ! -x "$verify_dir/${APP_NAME}" ]; then
    echo "[package-linux] 自检失败：解包后的 ${APP_NAME} 无可执行位" >&2
    rm -rf "$verify_dir"
    exit 1
fi
rm -rf "$verify_dir"

echo "[package-linux] 已打包 → ${OUT}（顶层单一可执行 ${APP_NAME}）"
