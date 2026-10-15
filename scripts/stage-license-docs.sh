#!/usr/bin/env bash
# 把许可文档铺设到目标目录（供打包分发复用，唯一职责就是复制，无其他逻辑）：
#   DEST/LICENSE、DEST/THIRD_PARTY_NOTICES.md、DEST/licenses/
# 用法: scripts/stage-license-docs.sh <目标目录>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEST="${1:?用法: $0 <目标目录>}"

for f in LICENSE THIRD_PARTY_NOTICES.md; do
    [ -f "$ROOT/$f" ] || { echo "[licenses] 缺少 $ROOT/$f" >&2; exit 1; }
done
[ -d "$ROOT/licenses" ] || { echo "[licenses] 缺少 $ROOT/licenses/" >&2; exit 1; }

mkdir -p "$DEST"
cp -f "$ROOT/LICENSE" "$DEST/LICENSE"
cp -f "$ROOT/THIRD_PARTY_NOTICES.md" "$DEST/THIRD_PARTY_NOTICES.md"
rm -rf "$DEST/licenses"
cp -R "$ROOT/licenses" "$DEST/licenses"
# 源模块缓存的原文可能是只读文件；bundle 的 xattr 清理与签名阶段需要可写副本。
chmod -R u+rw "$DEST"
echo "[licenses] 已铺设许可文档 → $DEST"