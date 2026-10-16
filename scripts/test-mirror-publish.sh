#!/usr/bin/env bash
# test-mirror-publish.sh —— scripts/publish-mirror.sh + scripts/mirror-manifest.mjs 的离线验证。
#
# 不触碰任何真实对象存储、不启动任何服务：用 stub rclone（FAKE_STATE 目录模拟对象，
# 可注入损坏/失败）覆盖主 Agent 要求的关键路径：
#   1. 本地摘要不对 → 不上传；
#   2. 上传/远端校验失败 → 不切根 stable；
#   3. 全部成功 → 最后一步才切根 stable（带 Cache-Control: no-cache）；
#   以及幂等重跑、不同内容禁覆盖、版本回退保护、配置门禁与 dry-run。
#
# 用法：bash scripts/test-mirror-publish.sh
# 退出码：0 全部通过；1 存在失败用例。
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PUBLISH="$HERE/publish-mirror.sh"
MANIFEST_TOOL="$HERE/mirror-manifest.mjs"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-mirror-test.XXXXXX")"
LOGF="$WORKDIR/last-run.log"
PASS=0
FAIL=0

cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

ok()   { PASS=$((PASS + 1)); echo "[通过] $*"; }
fail() { FAIL=$((FAIL + 1)); echo "[失败] $*"; }

run_case() { # $1=描述 $2=期望rc 其余=命令
  local desc="$1" want="$2" rc=0
  shift 2
  "$@" >"$LOGF" 2>&1 || rc=$?
  if [ "$rc" -eq "$want" ]; then
    ok "${desc}（rc=${rc}）"
  else
    fail "${desc}：期望 rc=${want}，实际 rc=${rc}，输出如下"
    sed 's/^/    | /' "$LOGF" | tail -30
  fi
}

assert_grep() { # $1=描述 $2=模式 $3=文件
  if [ -f "$3" ] && grep -q "$2" "$3"; then
    ok "$1"
  else
    fail "$1（未在 $3 找到：$2）"
    [ -f "$3" ] && sed 's/^/    | /' "$3" | tail -20
  fi
}

assert_nogrep() { # $1=描述 $2=模式 $3=文件
  if [ -f "$3" ] && grep -q "$2" "$3"; then
    fail "$1（不应出现却找到：$2）"
    sed 's/^/    | /' "$3" | grep "$2" | head -5 | sed 's/^/    | /'
  else
    ok "$1"
  fi
}

assert_file() { if [ -f "$2" ]; then ok "$1"; else fail "$1（缺文件：$2）"; fi; }
assert_no_file() { if [ -e "$2" ]; then fail "$1（不应存在：$2）"; else ok "$1"; fi; }

# ---------- 造fixture：3 安装包 + licenses.zip + github-stable.json ----------
FIXTURE="$WORKDIR/fixture"
node - "$FIXTURE" <<'NODE'
const { writeFileSync, mkdirSync } = require('node:fs');
const { createHash } = require('node:crypto');
const path = require('node:path');
const crypto = require('node:crypto');
const dir = process.argv[2];
const files = [
  ['frpdesk-0.2.10-darwin-arm64.zip', 'darwin', 'arm64', 96 * 1024],
  ['frpdesk-0.2.10-darwin-amd64.zip', 'darwin', 'amd64', 128 * 1024],
  ['frpdesk-0.2.10-windows-amd64.exe', 'windows', 'amd64', 160 * 1024],
];
mkdirSync(dir, { recursive: true });
const artifacts = [];
for (const [name, platform, arch, size] of files) {
  const buf = crypto.randomBytes(size);
  writeFileSync(path.join(dir, name), buf);
  artifacts.push({
    url: `https://github.com/ivanusong/frpdesk/releases/download/v0.2.10/${name}`,
    platform,
    arch,
    size,
    digestAlgo: 'sha512',
    digest: createHash('sha512').update(buf).digest('base64'),
  });
}
writeFileSync(path.join(dir, 'frpdesk-0.2.10-licenses.zip'), crypto.randomBytes(8 * 1024));
writeFileSync(
  path.join(dir, 'github-stable.json'),
  JSON.stringify({
    schemaVersion: 1,
    version: '0.2.10',
    channel: 'stable',
    name: 'frpdesk v0.2.10',
    notes: 'test fixture\n第二行\n',
    publishedAt: '2026-10-16T00:00:00Z',
    artifacts,
  }, null, 2) + '\n',
);
NODE
ok "fixture 已生成：$FIXTURE"

# ---------- stub rclone ----------
STUB_DIR="$WORKDIR/stub-bin"
mkdir -p "$STUB_DIR"
cat > "$STUB_DIR/rclone" <<'STUB'
#!/usr/bin/env bash
# 测试桩：用 FAKE_STATE 目录模拟对象存储；FAKE_FAIL_*/FAKE_CORRUPT_KEY 注入故障。
set -u
STATE="${FAKE_STATE:?FAKE_STATE 未设置}"
op="$1"; shift
mkdir -p "$STATE"
echo "$op $*" >> "$STATE/ops.log"
key_of() { printf '%s' "$1" | sed 's/^[^:]*://'; }
case "$op" in
  version)
    echo "rclone stub v1.0 (test)" ;;
  lsjson)
    path=""; meta=0
    while [ $# -gt 0 ]; do
      case "$1" in
        --stat) ;;
        --metadata) meta=1 ;;
        *) path="$1" ;;
      esac
      shift
    done
    if [ -n "${FAKE_FAIL_LSJSON:-}" ]; then echo "2026/01/01 ERROR: access denied (injected)" >&2; exit 1; fi
    key="$(key_of "$path")"; f="$STATE/objects/$key"
    if [ ! -f "$f" ]; then echo "2026/01/01 NOTICE: Failed to lsjson: directory not found" >&2; exit 3; fi
    size="$(wc -c < "$f" | tr -d ' ')"
    cc=""
    [ -f "$STATE/meta/$key" ] && cc="$(cat "$STATE/meta/$key")"
    if [ "$meta" = 1 ]; then
      printf '{\n\t"Path": "%s",\n\t"Name": "%s",\n\t"Size": %s,\n\t"Metadata": {"cache-control": "%s"}\n}\n' "$key" "$(basename "$key")" "$size" "$cc"
    else
      printf '{\n\t"Path": "%s",\n\t"Name": "%s",\n\t"Size": %s\n}\n' "$key" "$(basename "$key")" "$size"
    fi ;;
  cat)
    path="${1:?}"; key="$(key_of "$path")"; f="$STATE/objects/$key"
    [ -f "$f" ] || { echo "object not found" >&2; exit 3; }
    cat "$f" ;;
  copyto)
    local_file="${1:?}"; remote="${2:?}"; key="$(key_of "$remote")"; f="$STATE/objects/$key"
    if [ "$key" = "${FAKE_FAIL_COPYTO:-}" ]; then echo "injected copyto failure" >&2; exit 1; fi
    mkdir -p "$(dirname "$f")"
    cp "$local_file" "$f"
    if [ "$key" = "${FAKE_CORRUPT_KEY:-}" ]; then
      # 同尺寸损坏首字节：确保回读校验走到 sha512 不一致（而非大小不一致）
      printf 'X' | dd of="$f" bs=1 seek=0 conv=notrunc 2>/dev/null
    fi
    for a in "$@"; do
      case "$a" in
        cache-control=no-cache) mkdir -p "$(dirname "$STATE/meta/$key")"; printf 'no-cache' > "$STATE/meta/$key" ;;
      esac
    done ;;
  *)
    echo "stub: unsupported op: $op" >&2; exit 2 ;;
esac
STUB
chmod +x "$STUB_DIR/rclone"
export PATH="$STUB_DIR:$PATH"

# .invalid 是 RFC 6761 保留 TLD，保证离线时公开地址检查快速失败（warn-only）
env_on() {
  export UPDATE_MIRROR_BASE_URL="https://mirror.invalid/downloads"
  export MIRROR_S3_ENDPOINT="https://s3.invalid"
  export MIRROR_S3_BUCKET="example-bucket"
  export MIRROR_S3_PREFIX="prod/frpdesk"
  export MIRROR_S3_REGION="us-east-1"
  export MIRROR_S3_PROVIDER="Other"
  export MIRROR_S3_PATH_STYLE="true"
  export MIRROR_S3_ACCESS_KEY_ID="stub-key"
  export MIRROR_S3_SECRET_ACCESS_KEY="stub-secret"
  export FAKE_STATE="$WORKDIR/stub-state"
}
env_off() {
  unset UPDATE_MIRROR_BASE_URL MIRROR_S3_ENDPOINT MIRROR_S3_BUCKET MIRROR_S3_PREFIX \
        MIRROR_S3_REGION MIRROR_S3_PROVIDER MIRROR_S3_PATH_STYLE \
        MIRROR_S3_ACCESS_KEY_ID MIRROR_S3_SECRET_ACCESS_KEY \
        FAKE_STATE FAKE_FAIL_LSJSON FAKE_FAIL_COPYTO FAKE_CORRUPT_KEY 2>/dev/null || true
}
OBJ_BASE="example-bucket/prod/frpdesk"
STABLE_OBJ="$WORKDIR/stub-state/objects/$OBJ_BASE/stable.json"
file_sha() { shasum -a 512 "$1" | awk '{print $1}'; }
sync_cmd() {
  bash "$PUBLISH" --version 0.2.10 --manifest "$FIXTURE/github-stable.json" \
    --artifacts-dir "$FIXTURE" --extra-file "$FIXTURE/frpdesk-0.2.10-licenses.zip"
}

echo "===== 成功路径 ====="

# --- S1 全量成功：最后一步才切 stable，且带 no-cache ---
env_on; rm -rf "$FAKE_STATE"
run_case "S1 首次全量同步成功" 0 sync_cmd
assert_grep "S1 日志：镜像同步完成（上传5）" "镜像同步完成：version=0.2.10 上传=5 跳过(内容一致)=0" "$LOGF"
for f in frpdesk-0.2.10-darwin-arm64.zip frpdesk-0.2.10-darwin-amd64.zip frpdesk-0.2.10-windows-amd64.exe frpdesk-0.2.10-licenses.zip manifest.json; do
  assert_file "S1 对象已入库：releases/0.2.10/$f" "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/$f"
  if cmp -s "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/$f" "$FIXTURE/$f" 2>/dev/null \
    || cmp -s "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/$f" "$WORKDIR/stub-manifest.json" 2>/dev/null; then
    : # 内容一致性对安装包成立；manifest.json 由独立断言覆盖
  fi
done
cmp -s "$FIXTURE/frpdesk-0.2.10-darwin-arm64.zip" "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/frpdesk-0.2.10-darwin-arm64.zip" \
  && ok "S1 安装包对象与本地字节一致" || fail "S1 安装包对象与本地字节不一致"
assert_file "S1 根 stable.json 已入库" "$STABLE_OBJ"
cmp -s "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/manifest.json" "$STABLE_OBJ" \
  && ok "S1 每版 manifest.json 与根 stable.json 内容一致" || fail "S1 每版 manifest.json 与根 stable.json 不一致"
LAST_COPYTO="$(grep '^copyto' "$WORKDIR/stub-state/ops.log" | tail -1)"
case "$LAST_COPYTO" in
  *"/stable.json"*) ok "S1 stable.json 是最后一个上传动作" ;;
  *) fail "S1 stable.json 不是最后一个上传动作：$LAST_COPYTO" ;;
esac
grep '^copyto' "$WORKDIR/stub-state/ops.log" | sed -n '$p' | grep -q "cache-control=no-cache" \
  && ok "S1 stable 切换带 Cache-Control: no-cache" || fail "S1 stable 切换缺少 cache-control=no-cache"
assert_grep "S1 回读确认 no-cache 元数据" "Cache-Control=no-cache 元数据已确认" "$LOGF"
node - "$FIXTURE/github-stable.json" "$STABLE_OBJ" "https://mirror.invalid/downloads" <<'NODE' && ok "S1 镜像清单仅改写 URL，摘要/大小与主源逐项一致" || fail "S1 镜像清单校验"
const { readFileSync } = require('node:fs');
const [srcPath, dstPath, base] = process.argv.slice(2);
const src = JSON.parse(readFileSync(srcPath, 'utf8'));
const dst = JSON.parse(readFileSync(dstPath, 'utf8'));
if (src.version !== dst.version || src.channel !== dst.channel) throw new Error('version/channel 被改动');
if (src.artifacts.length !== dst.artifacts.length) throw new Error('制品数变化');
for (let i = 0; i < src.artifacts.length; i++) {
  const a = src.artifacts[i], b = dst.artifacts[i];
  const name = decodeURIComponent(new URL(a.url).pathname.split('/').pop());
  const expect = `${base}/releases/${src.version}/${name}`;
  if (b.url !== expect) throw new Error(`artifacts[${i}].url=${b.url}，期望 ${expect}`);
  for (const k of ['platform', 'arch', 'size', 'digestAlgo', 'digest']) {
    if (a[k] !== b[k]) throw new Error(`artifacts[${i}].${k} 不一致`);
  }
}
NODE
STABLE_SHA_BEFORE="$(file_sha "$STABLE_OBJ")"

# --- S2 幂等重跑 ---
run_case "S2 同版本重跑幂等成功" 0 sync_cmd
assert_grep "S2 日志：内容一致跳过上传" "远端已存在且内容一致" "$LOGF"
assert_grep "S2 日志：stable 无需切换" "已是本版本相同内容，无需切换" "$LOGF"
[ "$(file_sha "$STABLE_OBJ")" = "$STABLE_SHA_BEFORE" ] \
  && ok "S2 stable.json 字节未变化" || fail "S2 stable.json 字节变化"

echo "===== 保护路径 ====="

# --- S3 远端对象被改动 → 拒绝覆盖，不切 stable ---
printf 'tampered' >> "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/frpdesk-0.2.10-darwin-amd64.zip"
run_case "S3 远端对象不同内容时拒绝覆盖" 1 sync_cmd
assert_grep "S3 日志：禁止不同内容覆盖" "禁止不同内容覆盖" "$LOGF"
assert_grep "S3 日志：失败声明未切 stable" "根 stable.json 未切换" "$LOGF"
[ "$(file_sha "$STABLE_OBJ")" = "$STABLE_SHA_BEFORE" ] \
  && ok "S3 stable.json 未被切换" || fail "S3 stable.json 被改动"
# 恢复对象
cp "$FIXTURE/frpdesk-0.2.10-darwin-amd64.zip" "$WORKDIR/stub-state/objects/$OBJ_BASE/releases/0.2.10/frpdesk-0.2.10-darwin-amd64.zip"

# --- S4 远端 stable 版本更高 → 拒绝回退 ---
node -e 'const{readFileSync,writeFileSync}=require("node:fs");const m=JSON.parse(readFileSync(process.argv[1],"utf8"));m.version="9.9.9";writeFileSync(process.argv[2],JSON.stringify(m,null,2)+"\n")' \
  "$FIXTURE/github-stable.json" "$STABLE_OBJ"
run_case "S4 远端 stable 版本更新时拒绝回退指针" 1 sync_cmd
assert_grep "S4 日志：拒绝回退" "拒绝把更新指针回退到旧版本" "$LOGF"

echo "===== 故障注入 ====="

# --- S5 本地制品与清单不一致 → 上传前失败 ---
FIXTURE_BAD="$WORKDIR/fixture-bad"
cp -R "$FIXTURE" "$FIXTURE_BAD"
# 同尺寸篡改首字节：确保触发的是 sha512 不一致路径（而非大小不一致）
printf 'X' | dd of="$FIXTURE_BAD/frpdesk-0.2.10-windows-amd64.exe" bs=1 seek=0 conv=notrunc 2>/dev/null
env_on; rm -rf "$FAKE_STATE"
run_case "S5 本地 sha512 与清单不一致时拒绝上传" 1 bash "$PUBLISH" \
  --version 0.2.10 --manifest "$FIXTURE_BAD/github-stable.json" \
  --artifacts-dir "$FIXTURE_BAD" --extra-file "$FIXTURE_BAD/frpdesk-0.2.10-licenses.zip"
assert_grep "S5 日志：sha512 与清单不一致" "sha512 与清单不一致" "$LOGF"
assert_no_file "S5 未上传任何对象" "$WORKDIR/stub-state/objects"

# --- S6 上传回读损坏 → 失败且绝不切 stable ---
env_on; rm -rf "$FAKE_STATE"
export FAKE_CORRUPT_KEY="$OBJ_BASE/releases/0.2.10/frpdesk-0.2.10-windows-amd64.exe"
run_case "S6 上传回读 sha512 不一致时失败" 1 sync_cmd
assert_grep "S6 日志：回读 sha512 不一致" "上传后回读 sha512 不一致" "$LOGF"
assert_no_file "S6 stable.json 未被上传" "$STABLE_OBJ"
env_off

# --- S7 上传命令失败 → 显式失败 ---
env_on; rm -rf "$FAKE_STATE"
export FAKE_FAIL_COPYTO="$OBJ_BASE/releases/0.2.10/frpdesk-0.2.10-darwin-arm64.zip"
run_case "S7 上传命令失败时显式失败" 1 sync_cmd
assert_grep "S7 日志：失败阶段可见" "镜像同步失败于阶段" "$LOGF"
assert_no_file "S7 stable.json 未被上传" "$STABLE_OBJ"
env_off

# --- S8 查询远端非 not-found 错误 → 不误判为可覆盖 ---
env_on; rm -rf "$FAKE_STATE"
export FAKE_FAIL_LSJSON=1
run_case "S8 查询远端失败（认证类错误）时硬失败" 1 sync_cmd
assert_grep "S8 日志：查询远端对象失败" "查询远端对象失败" "$LOGF"
assert_nogrep "S8 无任何上传动作" "\[上传\]" "$LOGF"
env_off

echo "===== 配置门禁 ====="

# --- S9 启用但配置不完整 → 明确失败 ---
env_off
export UPDATE_MIRROR_BASE_URL="https://mirror.invalid/downloads"
export MIRROR_S3_ENDPOINT="https://s3.invalid"
run_case "S9 启用但缺少必填变量时明确失败" 1 sync_cmd
assert_grep "S9 日志：缺失变量逐一列出" "必填变量缺失" "$LOGF"
assert_grep "S9 日志：列出 MIRROR_S3_BUCKET" "MIRROR_S3_BUCKET" "$LOGF"

# --- S10 未配置镜像 → 跳过 ---
env_off
export FAKE_STATE="$WORKDIR/stub-state-s10"
run_case "S10 未配置镜像时跳过且成功" 0 sync_cmd
assert_grep "S10 日志：跳过镜像同步" "跳过镜像同步" "$LOGF"
assert_no_file "S10 未产生任何 rclone 调用" "$FAKE_STATE"

# --- S11 dry-run：本地校验+计划，零 rclone 调用 ---
env_off
export UPDATE_MIRROR_BASE_URL="https://mirror.invalid/downloads"
export FAKE_STATE="$WORKDIR/stub-state-s11"
run_case "S11 dry-run 仅本地校验与计划" 0 bash "$PUBLISH" --dry-run \
  --version 0.2.10 --manifest "$FIXTURE/github-stable.json" \
  --artifacts-dir "$FIXTURE" --extra-file "$FIXTURE/frpdesk-0.2.10-licenses.zip"
assert_grep "S11 日志：展示同步计划" "\[DRY-RUN\] 将上传" "$LOGF"
assert_grep "S11 日志：计划切换 stable 并带 no-cache" "Cache-Control: no-cache" "$LOGF"
assert_nogrep "S11 dry-run 无任何 rclone 调用" "rclone" "$LOGF"
assert_no_file "S11 未产生任何 rclone 调用" "$FAKE_STATE"

# --- S12 清单版本与 --version 不一致 → 失败 ---
env_off
run_case "S12 清单版本与期望版本不一致时失败" 1 node "$MANIFEST_TOOL" \
  --manifest "$FIXTURE/github-stable.json" --artifacts-dir "$FIXTURE" \
  --expect-version 0.2.11 --check-only

echo "===== 与 /tmp/frpdesk-bridge.hWjFnZ 真实发布产物交叉验证（存在才跑）====="
BRIDGE="/tmp/frpdesk-bridge.hWjFnZ"
if [ -f "$BRIDGE/github-stable.json" ] && [ -f "$BRIDGE/mirror-stable.json" ]; then
  BASE_URL_EVIDENCE="$(node -e 'const m=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));const u=new URL(m.artifacts[0].url);process.stdout.write(u.origin+u.pathname.replace(/\/releases\/.*$/,""))' "$BRIDGE/mirror-stable.json")"
  OUT="$WORKDIR/bridge-manifest.json"
  run_case "C1 官方 fixture 三安装包+清单本地校验通过" 0 node "$MANIFEST_TOOL" \
    --manifest "$BRIDGE/github-stable.json" --artifacts-dir "$BRIDGE" \
    --base-url "$BASE_URL_EVIDENCE" --output "$OUT" --list-files
  if cmp -s "$OUT" "$BRIDGE/mirror-stable.json"; then
    ok "C2 生成的镜像清单与官方预期 mirror-stable.json 逐字节一致"
  else
    fail "C2 生成的镜像清单与官方预期 mirror-stable.json 不一致"
    diff "$OUT" "$BRIDGE/mirror-stable.json" | head -20 | sed 's/^/    | /'
  fi
else
  echo "[跳过] C 组：未找到 $BRIDGE（离线环境可忽略）"
fi

echo "===== 结果：通过=$PASS 失败=$FAIL ====="
[ "$FAIL" -eq 0 ]
