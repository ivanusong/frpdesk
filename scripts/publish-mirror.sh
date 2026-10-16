#!/usr/bin/env bash
# publish-mirror.sh —— 把已发布的 GitHub Release 资产原样同步到通用 S3 兼容镜像存储。
#
# 定位（保持小而美）：
#   - 只做"同步已发布产物"：对 workflow 从 GitHub Release 下载好的资产做
#     size/sha512 核验，原样上传，不重新构建、不改字节。
#   - 存储布局：<root>/releases/<version>/<原GitHub文件名>、每版 manifest.json、
#     根 stable.json（Cache-Control: no-cache，最后一步才切换）。
#   - 各阶段失败立即失败：不切根 stable、不伪装成功；版本对象禁止不同内容覆盖；
#     同版本重跑（内容一致）可恢复、幂等。
#   - 凭据只用环境变量，构造 env-only 的 rclone remote，不落盘、不写死任何
#     私有域名 / bucket / 区域；rclone 缺失由 workflow 先行安装。
#   - 镜像清单由主源 stable.json 仅改写 artifact.url 生成（scripts/mirror-manifest.mjs），
#     与主源同版本同摘要，客户端据此保证"回退不换版本"。
#
# 用法：
#   publish-mirror.sh --version X.Y.Z --manifest <github-stable.json> \
#     --artifacts-dir <dir> [--extra-file <file>]... [--dry-run]
#
# 环境变量：
#   UPDATE_MIRROR_BASE_URL       镜像公开下载根 URL（无尾斜杠；构建时以同值注入客户端）
#   MIRROR_S3_ENDPOINT           S3 兼容端点（可含 http(s)://）
#   MIRROR_S3_BUCKET             存储桶名
#   MIRROR_S3_PREFIX             可选，桶内前缀（默认空）
#   MIRROR_S3_REGION             可选，默认 us-east-1
#   MIRROR_S3_PROVIDER           可选，rclone provider（默认 Other，通用 S3 兼容）
#   MIRROR_S3_PATH_STYLE         可选，true/false 覆盖 path-style（默认 true，已实测通用端点）
#   MIRROR_S3_ACCESS_KEY_ID      访问密钥（Secret）
#   MIRROR_S3_SECRET_ACCESS_KEY  访问密钥（Secret）
#
# 退出码：0 成功或"未配置镜像跳过"；1 配置不完整/校验失败/同步失败。
set -euo pipefail
set -o errtrace

REMOTE_NAME="frpdesk_mirror"   # env-only rclone remote，名字固定便于排查
STAGE=""
WORK=""

TS() { date "+%Y-%m-%dT%H:%M:%S%z"; }
# 日志一律走 stderr：stdout 保留给命令替换捕获的数据，避免 trap 输出污染 $(...) 结果
log()  { local lvl="$1"; shift; echo "[$(TS)] [$lvl] $*" >&2; }
info() { log INFO "$@"; }
warn() { log WARN "$@"; }
die()  { log ERROR "$@"; exit 1; }

# ERR trap 里必须 exit：bash 中 ERR trap 触发后若仅返回，set -e 的退出会被取代，
# 失败会继续向后执行（可能带病走到 stable 切换）。exit 使失败沿 $(...) 子 shell
# 逐层向上传播，确保任一阶段失败立即终止。
on_err() {
  local rc=$?
  [ "$rc" -ne 0 ] || return 0
  log ERROR "镜像同步失败于阶段 [${STAGE:-unknown}]（exit=${rc}），根 stable.json 未切换"
  exit "$rc"
}
trap on_err ERR

usage() {
  sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
}

# ---------- 参数解析 ----------
DRY_RUN=0
VERSION=""
MANIFEST=""
ARTIFACTS_DIR=""
EXTRA_FILES=""

while [ $# -gt 0 ]; do
  case "$1" in
    --version)      [ -n "${2:-}" ] || die "--version 需要取值"; VERSION="$2"; shift 2 ;;
    --manifest)     [ -n "${2:-}" ] || die "--manifest 需要取值"; MANIFEST="$2"; shift 2 ;;
    --artifacts-dir) [ -n "${2:-}" ] || die "--artifacts-dir 需要取值"; ARTIFACTS_DIR="$2"; shift 2 ;;
    --extra-file)   [ -n "${2:-}" ] || die "--extra-file 需要文件路径"; EXTRA_FILES="${EXTRA_FILES}${2}"$'\n'; shift 2 ;;
    --dry-run)      DRY_RUN=1; shift ;;
    --help|-h)      usage; exit 0 ;;
    *)              die "未知参数：$1（--help 查看用法）" ;;
  esac
done

[ -n "$VERSION" ] || die "缺少 --version X.Y.Z"
printf '%s' "$VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' || die "版本号格式非法：${VERSION}（期望三段数字）"
[ -n "$MANIFEST" ] || die "缺少 --manifest <github-stable.json>"
[ -n "$ARTIFACTS_DIR" ] || die "缺少 --artifacts-dir <dir>"
[ -f "$MANIFEST" ] || die "清单不存在：${MANIFEST}"
[ -d "$ARTIFACTS_DIR" ] || die "产物目录不存在：${ARTIFACTS_DIR}"
printf '%s' "$EXTRA_FILES" | while IFS= read -r extra; do
  [ -n "$extra" ] || continue
  [ -f "$extra" ] || die "附加文件不存在：${extra}"
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------- 镜像配置解析与门禁 ----------
BASE_URL="${UPDATE_MIRROR_BASE_URL:-}"
ENDPOINT="${MIRROR_S3_ENDPOINT:-}"
BUCKET="${MIRROR_S3_BUCKET:-}"
PREFIX="${MIRROR_S3_PREFIX:-}"
REGION="${MIRROR_S3_REGION:-us-east-1}"
AK="${MIRROR_S3_ACCESS_KEY_ID:-}"
SK="${MIRROR_S3_SECRET_ACCESS_KEY:-}"
PROVIDER="${MIRROR_S3_PROVIDER:-Other}"
PATH_STYLE="${MIRROR_S3_PATH_STYLE:-true}"

if [ -z "$BASE_URL" ] && [ -z "$ENDPOINT" ] && [ -z "$BUCKET" ] && [ -z "$AK" ] && [ -z "$SK" ]; then
  info "未配置镜像相关变量（UPDATE_MIRROR_BASE_URL / MIRROR_S3_* 均为空），跳过镜像同步，发布仅走 GitHub"
  exit 0
fi

if [ "$DRY_RUN" -eq 1 ]; then
  # 干跑只需要公开根地址用于生成镜像清单；存储侧参数不要求
  [ -n "$BASE_URL" ] || die "--dry-run 需要环境变量 UPDATE_MIRROR_BASE_URL（生成镜像清单 URL 用）"
else
  MISSING=""
  check_required() {
    [ -n "$2" ] || MISSING="${MISSING} $1"
  }
  check_required UPDATE_MIRROR_BASE_URL "$BASE_URL"
  check_required MIRROR_S3_ENDPOINT "$ENDPOINT"
  check_required MIRROR_S3_BUCKET "$BUCKET"
  check_required MIRROR_S3_ACCESS_KEY_ID "$AK"
  check_required MIRROR_S3_SECRET_ACCESS_KEY "$SK"
  if [ -n "$MISSING" ]; then
    die "镜像同步已启用但以下必填变量缺失：${MISSING}（如不需要镜像请把这些变量全部留空）"
  fi
fi

# 与客户端 normalizeMirrorBaseURL 规则一致：http(s)、有主机、无空白/引号/反斜杠、无尾斜杠
validate_base_url() {
  case "$1" in
    *[\ \	]*|*\"*|*"'"*|*\\*) die "UPDATE_MIRROR_BASE_URL 不能包含空白/引号/反斜杠：$1" ;;
  esac
  case "$1" in
    https://*|http://*) : ;;
    *) die "UPDATE_MIRROR_BASE_URL 必须是 http(s) 地址：$1" ;;
  esac
  case "$1" in
    */) die "UPDATE_MIRROR_BASE_URL 不能以 / 结尾：$1" ;;
    *\?*|*\#*) die "UPDATE_MIRROR_BASE_URL 不能带查询参数或片段：$1" ;;
  esac
  local rest host
  rest="${1#*://}"
  host="${rest%%/*}"
  [ -n "$host" ] || die "UPDATE_MIRROR_BASE_URL 缺少主机名：$1"
}
validate_base_url "$BASE_URL"

for candidate in "$BUCKET" "$PREFIX"; do
  case "$candidate" in
    *[\ \	]*) die "MIRROR_S3_BUCKET / MIRROR_S3_PREFIX 不能包含空白：${candidate}" ;;
    ..*) die "MIRROR_S3_BUCKET / MIRROR_S3_PREFIX 不能以 .. 开头：${candidate}" ;;
  esac
done
PREFIX="${PREFIX#/}"
PREFIX="${PREFIX%/}"

# ---------- 依赖工具 ----------
for tool in node shasum awk; do
  command -v "$tool" >/dev/null 2>&1 || die "缺少依赖命令：${tool}"
done
if [ "$DRY_RUN" -eq 0 ]; then
  command -v rclone >/dev/null 2>&1 || die "缺少 rclone（workflow 会先行确保安装；本地可 brew install rclone）"
  if ! command -v curl >/dev/null 2>&1; then
    warn "缺少 curl，将跳过镜像公开地址可达性检查"
  fi
fi

info "镜像同步开始：version=${VERSION} dry_run=${DRY_RUN}"
info "配置摘要：base=${BASE_URL} provider=${PROVIDER} region=${REGION} path_style=${PATH_STYLE} bucket=${BUCKET} prefix=${PREFIX:-<无>} endpoint=${ENDPOINT}"

# ---------- 阶段 1/5：校验本地产物与主源清单（size/sha512）并生成镜像清单 ----------
STAGE="校验与清单生成"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/frpdesk-mirror.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

FILES="$(node "$SCRIPT_DIR/mirror-manifest.mjs" \
  --manifest "$MANIFEST" \
  --artifacts-dir "$ARTIFACTS_DIR" \
  --base-url "$BASE_URL" \
  --expect-version "$VERSION" \
  --output "$WORK/manifest.json" \
  --list-files)" || die "本地产物校验/清单生成失败"
cp "$WORK/manifest.json" "$WORK/stable.json"
[ -n "$FILES" ] || die "清单中未解析出任何制品文件名"
info "镜像版本清单内容（将同时写入每版 manifest.json 与根 stable.json）："
sed 's/^/    /' "$WORK/manifest.json"

printf '%s' "$EXTRA_FILES" | while IFS= read -r extra; do
  [ -n "$extra" ] || continue
  base="$(basename "$extra")"
  if printf '%s\n' "$FILES" | grep -Fxq "$base"; then
    die "附加文件与清单制品重名：${base}"
  fi
  info "附加文件：${base} size=$(wc -c < "$extra" | tr -d ' ') sha512=$(shasum -a 512 "$extra" | awk '{print $1}' | cut -c1-16)…"
done

# ---------- 干跑：只做本地校验与计划展示，不触碰任何存储 ----------
if [ "$DRY_RUN" -eq 1 ]; then
  info "[DRY-RUN] 本地校验与清单生成已通过，以下为同步计划（未触碰存储）："
  printf '%s\n' "$FILES" | while IFS= read -r name; do
    [ -n "$name" ] || continue
    info "[DRY-RUN] 将上传（已存在时要求内容一致）：releases/${VERSION}/${name} → ${BASE_URL}/releases/${VERSION}/${name}"
  done
  printf '%s' "$EXTRA_FILES" | while IFS= read -r extra; do
    [ -n "$extra" ] || continue
    name="$(basename "$extra")"
    info "[DRY-RUN] 将上传（已存在时要求内容一致）：releases/${VERSION}/${name} → ${BASE_URL}/releases/${VERSION}/${name}"
  done
  info "[DRY-RUN] 将上传每版清单：releases/${VERSION}/manifest.json"
  info "[DRY-RUN] 全部对象核验通过后，最后切换根 stable.json（Cache-Control: no-cache）→ ${BASE_URL}/stable.json"
  info "[DRY-RUN] 结束（真实同步需配置 MIRROR_S3_* 并去掉 --dry-run）"
  exit 0
fi

# ---------- env-only rclone remote（凭据不落盘）----------
export RCLONE_CONFIG_FRPDESK_MIRROR_TYPE=s3
export RCLONE_CONFIG_FRPDESK_MIRROR_PROVIDER="$PROVIDER"
export RCLONE_CONFIG_FRPDESK_MIRROR_ENDPOINT="$ENDPOINT"
export RCLONE_CONFIG_FRPDESK_MIRROR_REGION="$REGION"
export RCLONE_CONFIG_FRPDESK_MIRROR_ACCESS_KEY_ID="$AK"
export RCLONE_CONFIG_FRPDESK_MIRROR_SECRET_ACCESS_KEY="$SK"
export RCLONE_CONFIG_FRPDESK_MIRROR_FORCE_PATH_STYLE="$PATH_STYLE"

if [ -n "$PREFIX" ]; then
  OBJ_BASE="${BUCKET}/${PREFIX}"
else
  OBJ_BASE="$BUCKET"
fi
rpath() { printf '%s:%s/releases/%s/%s' "$REMOTE_NAME" "$OBJ_BASE" "$VERSION" "$1"; }
public_url() { printf '%s/releases/%s/%s' "$BASE_URL" "$VERSION" "$1"; }
ROOT_RPATH="${REMOTE_NAME}:${OBJ_BASE}/stable.json"
ROOT_PUBLIC_URL="${BASE_URL}/stable.json"
ACTIONS_LOG="$WORK/actions.log"
: > "$ACTIONS_LOG"

# ---------- 远端查询原语 ----------
# remote_stat：stdout 输出对象大小；对象不存在输出空；其余任何失败硬失败。
# 仅当错误信息明确是 not found 才视为不存在，避免把认证/网络错误误判为"可覆盖"。
# 两个原语都运行在 $(...) 子 shell 内且自行处理错误：先关闭 ERR trap，
# 防止 errtrace 继承的 on_err 把日志写进捕获结果。
remote_stat() {
  local path="$1" out rc errf="$WORK/stat.err"
  set +e
  trap - ERR
  out="$(rclone lsjson "$path" --stat 2>"$errf")"
  rc=$?
  set -e
  trap on_err ERR
  if [ "$rc" -eq 0 ]; then
    printf '%s' "$out" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{process.stdout.write(String(JSON.parse(s).Size))})'
  elif grep -qi "not found" "$errf"; then
    : # 对象不存在：输出空
  else
    cat "$errf" >&2
    die "查询远端对象失败：${path}"
  fi
}

# remote_sha：回读远端对象计算 sha512（hex）；失败返回非零。
remote_sha() {
  local path="$1" hex rc errf="$WORK/cat.err"
  set +e
  trap - ERR
  hex="$(rclone cat "$path" 2>"$errf" | shasum -a 512 | awk '{print $1}')"
  rc=$?
  set -e
  trap on_err ERR
  if [ "$rc" -ne 0 ]; then
    cat "$errf" >&2
    return 1
  fi
  printf '%s' "$hex"
}

# public_check：镜像公开地址 HEAD 可达性（warn-only，仅提示，不阻断发布）。
public_check() {
  local url="$1" label="$2" code rc
  command -v curl >/dev/null 2>&1 || return 0
  set +e
  trap - ERR
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 -I "$url")"
  rc=$?
  set -e
  trap on_err ERR
  if [ "$rc" -ne 0 ] || [ "$code" != "200" ]; then
    warn "镜像公开地址暂未可达（HTTP ${code:-未知}，客户端会自动回退主源，请人工确认）：${url}"
    return 0
  fi
  info "公开地址可达（HTTP 200）：${label}"
}

# ---------- 单对象：存在性核验 → 上传 → 回读核验；禁止不同内容覆盖 ----------
ensure_object() {
  local local_file="$1" rpath_="$2" label="$3"
  local lsize lhex csize chex rsize rhex2
  lsize="$(wc -c < "$local_file" | tr -d ' ')"
  lhex="$(shasum -a 512 "$local_file" | awk '{print $1}')"

  csize="$(remote_stat "$rpath_")"
  if [ -n "$csize" ]; then
    if [ "$csize" != "$lsize" ]; then
      die "镜像对象已存在且大小不同，禁止不同内容覆盖（如同版本重建请先人工清理镜像对象）：${rpath_}（远端 ${csize}B ≠ 本地 ${lsize}B）"
    fi
    chex="$(remote_sha "$rpath_")" || die "读取远端已有对象失败：${rpath_}"
    if [ "$chex" != "$lhex" ]; then
      die "镜像对象已存在且 sha512 不同，禁止不同内容覆盖（如同版本重建请先人工清理镜像对象）：${rpath_}"
    fi
    info "[跳过] ${label} 远端已存在且内容一致（size=${lsize}）"
    printf 'skipped\n'
    return 0
  fi

  info "[上传] ${label} → ${rpath_}（size=${lsize}）"
  rclone copyto "$local_file" "$rpath_"

  rsize="$(remote_stat "$rpath_")"
  if [ "$rsize" != "$lsize" ]; then
    die "上传后回读大小不一致：${rpath_}（远端 ${rsize:-无} ≠ 本地 ${lsize}）"
  fi
  rhex2="$(remote_sha "$rpath_")" || die "上传后回读失败：${rpath_}"
  if [ "$rhex2" != "$lhex" ]; then
    die "上传后回读 sha512 不一致（疑似传输损坏，禁止继续）：${rpath_}"
  fi
  info "[校验通过] ${label} 远端内容与本地一致（sha512=${lhex:0:16}…）"
  printf 'uploaded\n'
}

upload_one() {
  local name="$1" local_file="$2" action
  action="$(ensure_object "$local_file" "$(rpath "$name")" "$name")"
  printf '%s\n' "$action" >> "$ACTIONS_LOG"
  public_check "$(public_url "$name")" "$name"
}

# ---------- 阶段 2/5：上传制品（3 安装包 + 附加文件）----------
STAGE="上传制品"
while IFS= read -r name; do
  [ -n "$name" ] || continue
  upload_one "$name" "${ARTIFACTS_DIR:?}/${name}"
done <<EOF
$FILES
EOF

printf '%s' "$EXTRA_FILES" | while IFS= read -r extra; do
  [ -n "$extra" ] || continue
  upload_one "$(basename "$extra")" "$extra"
done

# ---------- 阶段 3/5：上传每版 manifest.json ----------
STAGE="上传版本清单"
upload_one "manifest.json" "$WORK/manifest.json"

# ---------- 阶段 4/5：切换根 stable.json（最后一步，失败前绝不触碰）----------
STAGE="切换根 stable.json"
local_hex="$(shasum -a 512 "$WORK/stable.json" | awk '{print $1}')"
local_size="$(wc -c < "$WORK/stable.json" | tr -d ' ')"
switch_needed=1

root_size="$(remote_stat "$ROOT_RPATH")"
if [ -n "$root_size" ]; then
  set +e
  trap - ERR
  cur_json="$(rclone cat "$ROOT_RPATH" 2>"$WORK/root.err")"
  rc=$?
  set -e
  trap on_err ERR
  [ "$rc" -eq 0 ] || { cat "$WORK/root.err" >&2; die "读取远端根 stable.json 失败：${ROOT_RPATH}"; }
  cur_ver="$(printf '%s' "$cur_json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(String(JSON.parse(s).version??""))}catch(e){process.exit(3)}})')" \
    || die "远端根 stable.json 不是合法的更新清单 JSON：${ROOT_RPATH}"
  cmp="$(node -e 'const v=(s)=>String(s).trim().replace(/^v/,"").split(".").map(n=>parseInt(n,10)||0);const a=v(process.argv[1]),b=v(process.argv[2]);for(let i=0;i<3;i++){if(a[i]!==b[i]){process.stdout.write(a[i]<b[i]?"lt":"gt");process.exit(0)}}process.stdout.write("eq")' "$cur_ver" "$VERSION")"
  case "$cmp" in
    gt)
      die "远端根 stable.json 版本 ${cur_ver} 高于本次 ${VERSION}，拒绝把更新指针回退到旧版本"
      ;;
    eq)
      cur_hex="$(remote_sha "$ROOT_RPATH")" || die "读取远端根 stable.json 失败：${ROOT_RPATH}"
      if [ "$cur_hex" = "$local_hex" ]; then
        info "根 stable.json 已是本版本相同内容，无需切换（幂等重跑）"
        switch_needed=0
      else
        die "远端根 stable.json 同为版本 ${VERSION} 但内容不同（如同版本重建，请先人工核对 GitHub Release 与镜像对象）"
      fi
      ;;
    lt)
      info "远端根 stable.json 当前版本 ${cur_ver}，将切换到 ${VERSION}"
      ;;
  esac
else
  info "远端尚无根 stable.json，将首次写入 ${VERSION}"
fi

if [ "$switch_needed" -eq 1 ]; then
  info "[切换] ${ROOT_RPATH}（Cache-Control: no-cache）"
  # 参数组合为主 Agent 在公开 HTTPS 端点实测有效的配方
  rclone copyto "$WORK/stable.json" "$ROOT_RPATH" \
    --ignore-times --metadata --metadata-set "cache-control=no-cache"

  root_size2="$(remote_stat "$ROOT_RPATH")"
  [ "$root_size2" = "$local_size" ] || die "切换后回读大小不一致：远端 ${root_size2:-无} ≠ 本地 ${local_size}"
  root_hex="$(remote_sha "$ROOT_RPATH")" || die "切换后回读失败：${ROOT_RPATH}"
  [ "$root_hex" = "$local_hex" ] || die "切换后回读 sha512 不一致，stable 指针可能异常：${ROOT_RPATH}"
  info "根 stable.json 切换完成并回读校验通过（version=${VERSION} sha512=${local_hex:0:16}…）"

  # 元数据回读：后端不返回该元数据时仅告警（不阻断）
  meta="$(rclone lsjson "$ROOT_RPATH" --stat --metadata 2>/dev/null \
    | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const m=JSON.parse(s).Metadata||{};process.stdout.write(String(m["cache-control"]??""))}catch(e){process.exit(0)}})')" || meta=""
  if [ -n "$meta" ]; then
    if [ "$meta" != "no-cache" ]; then
      die "根 stable.json 的 Cache-Control 元数据为 ${meta}，预期 no-cache（请检查存储桶/网关配置）"
    fi
    info "远端 Cache-Control=no-cache 元数据已确认"
  else
    warn "存储后端未返回 Cache-Control 元数据，无法自动确认（请人工确认不会长缓存 stable.json）"
  fi

  # 公开地址核验（warn-only）：可达性 + Cache-Control 头
  if command -v curl >/dev/null 2>&1; then
    hdr="$(curl -sI --max-time 15 "$ROOT_PUBLIC_URL" | tr -d '\r')" || hdr=""
    if printf '%s' "$hdr" | head -1 | grep -q " 200"; then
      info "公开地址可达：${ROOT_PUBLIC_URL}"
      cc="$(printf '%s\n' "$hdr" | awk 'tolower($1)=="cache-control:"{print $2}')"
      if [ -n "$cc" ]; then
        case "$cc" in
          *no-cache*) info "公开地址返回 Cache-Control: ${cc}" ;;
          *) warn "公开地址 Cache-Control=${cc}，未包含 no-cache，客户端可能读到过期指针" ;;
        esac
      else
        warn "公开地址未返回 Cache-Control 头，请人工确认缓存策略"
      fi
    else
      warn "镜像公开地址暂未可达（客户端会自动回退主源，请人工确认）：${ROOT_PUBLIC_URL}"
    fi
  fi
fi

# ---------- 阶段 5/5：汇总 ----------
STAGE="汇总"
UPLOADED="$(grep -c '^uploaded$' "$ACTIONS_LOG" || true)"
SKIPPED="$(grep -c '^skipped$' "$ACTIONS_LOG" || true)"
info "镜像同步完成：version=${VERSION} 上传=${UPLOADED} 跳过(内容一致)=${SKIPPED}；根 stable.json 已指向 ${VERSION}"
