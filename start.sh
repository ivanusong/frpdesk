#!/usr/bin/env bash
# frpdesk 开发模式一键启动：封装 `wails3 dev`，改代码无需手动构建即可看到新界面。
#
# dev 模式机制（装配在 Taskfile.yml / build/Taskfile.yml / build/config.yml）：
#   - 前端零构建：frontend/dist 由 python http.server 直接伺服（9245 端口），
#     改 HTML/CSS/JS 保存后刷新窗口即生效（右键 → 检查元素，console 执行 location.reload()）；
#   - Go 改动：watcher 监听 *.go，自动重编译并重启应用，bindings 随之重新生成同步；
#   - 纯调样式也可直接浏览器打开 http://localhost:9245（bindings 不通、数据为空属正常）。
#
# 启动前检查（非破坏性，只清理本项目形态的残留）：
#   - 定位 wails3（兼容 mise 的 GOBIN，同 build.sh）；
#   - 9245 被上次 dev 残留的 http.server 占用则清掉，被其他进程占用则报错退出；
#   - frpdesk 正在运行（托盘正式版或上次 dev 残留）则报错退出：
#     单实例 ID 相同，dev 实例会把自己当第二实例直接退掉。
#
# 前台运行，Ctrl+C 退出；应用与 python 伺服都由 wails3 dev 统一管理回收，
# 因此直接 exec 交接，让 Ctrl+C 信号直达，无需 trap。

set -euo pipefail

# 基于脚本所在目录定位项目根（无论从哪里调用都正确）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# 前端 dev server 端口（与 build/Taskfile.yml 的 VITE_PORT 默认值一致）
DEV_PORT="9245"
# 与 go.mod 里 wails v3 版本保持一致
WAILS3_VERSION="v3.0.0-beta.28"

C_GREEN='\033[0;32m'; C_RED='\033[0;31m'; C_RESET='\033[0m'
info() { printf "${C_GREEN}[dev]${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}[错误]${C_RESET} %s\n" "$*" >&2; }

# ---- 前置检查 ================================================================

if ! command -v go >/dev/null; then
    err "缺少 go"
    exit 1
fi

# 依次在 PATH、GOBIN、GOPATH/bin、~/go/bin 里找 wails3（同 build.sh）
WAILS3="$(command -v wails3 || true)"
if [ -z "$WAILS3" ]; then
    for d in "$(go env GOBIN 2>/dev/null)" "$(go env GOPATH 2>/dev/null)/bin" "$HOME/go/bin"; do
        if [ -n "$d" ] && [ -x "$d/wails3" ]; then
            WAILS3="$d/wails3"
            break
        fi
    done
fi
if [ -z "$WAILS3" ]; then
    err "缺少 wails3：go install github.com/wailsapp/wails/v3/cmd/wails3@${WAILS3_VERSION}"
    exit 1
fi

# frpdesk 正在运行 → 单实例 ID 冲突，dev 实例会秒退
if pgrep -x frpdesk >/dev/null; then
    err "检测到正在运行的 frpdesk，请先退出（托盘菜单 → 退出）"
    err "若是上次 dev 的残留进程，可执行 pkill -x frpdesk 后重试"
    exit 1
fi

# 9245 端口：本项目形态（python http.server）的残留则清理，其他占用报错让用户处理
PIDS="$(lsof -ti tcp:"$DEV_PORT" 2>/dev/null || true)"
if [ -n "$PIDS" ]; then
    # lsof 多 pid 换行分隔，ps -p 需要逗号分隔
    CMDS="$(ps -p "$(echo "$PIDS" | tr '\n' ',' | sed 's/,$//')" -o command=)"
    if echo "$CMDS" | grep -q "http\.server.*${DEV_PORT}"; then
        info "清理上次 dev 残留的前端伺服进程：$(echo "$PIDS" | tr '\n' ' ')"
        kill $PIDS  # 多 pid 需要分词
        sleep 0.5
    else
        err "端口 ${DEV_PORT} 被其他进程占用：$(echo "$CMDS" | head -1)"
        err "请释放该端口后重试"
        exit 1
    fi
fi

# ---- 启动 ====================================================================

info "wails3: ${WAILS3}"
info "前端改动即存即生效（保存后刷新窗口）；Go 改动自动重编重启"
info "纯调样式可直接浏览器开 http://localhost:${DEV_PORT}"
info "Ctrl+C 退出（应用与前端伺服由 wails3 dev 一并停止）"

# build/config.yml 的 executes 以裸命令名调 wails3（经 /bin/sh 按 PATH 解析），
# GOBIN 不在 PATH 时子任务会 command not found：把 wails3 所在目录挂到 PATH 末尾
export PATH="$PATH:$(dirname "$WAILS3")"

# 前台交接给 wails3 dev（子进程生命周期由其管理，信号直通）
exec "$WAILS3" dev
