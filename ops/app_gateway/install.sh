#!/bin/sh
# Install or update the OmniAPI app account gateway.
#
#   sudo sh ops/app_gateway/install.sh           # 预演，只打印将要做什么
#   sudo sh ops/app_gateway/install.sh --apply   # 真的安装并启动
#
# 首次安装需要 .secrets/app-gateway.json（密钥可用 scripts/gen_app_gateway_secrets.py
# 生成，或由经营者手工放置）。脚本不生成、不打印任何秘密。
set -eu

APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

ROOT=/opt/ai-gateway
UNIT=omniapi-app-gateway.service
USER_NAME=omniapi-watch
DATA="$ROOT/runtime/app-gateway"
SECRETS="$ROOT/.secrets/app-gateway.json"
CONFIG="$ROOT/config/app-gateway.json"
PORT=8791

say() { printf '%s\n' "$*"; }
run() {
    if [ "$APPLY" = "1" ]; then
        say "+ $*"
        "$@"
    else
        say "（预演）$*"
    fi
}

say "== OmniAPI 客户端账号网关安装 =="
say "模式: $([ "$APPLY" = "1" ] && echo 应用 || echo 预演)"
say ""

# -- preconditions ---------------------------------------------------------
BLOCKED=0
for path in "$SECRETS" "$CONFIG" "$ROOT/ops/app_gateway/app_gateway.py" \
            "$ROOT/ops/systemd/$UNIT"; do
    if [ ! -e "$path" ]; then
        say "BLOCKED: 缺少 $path"
        BLOCKED=1
    fi
done
if ! id "$USER_NAME" >/dev/null 2>&1; then
    say "BLOCKED: 服务账号 $USER_NAME 不存在（先装充值服务）"
    BLOCKED=1
fi
if command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ":$PORT "; then
    say "注意: $PORT 已有监听，安装会重启占用它的服务"
fi
[ "$BLOCKED" = "1" ] && exit 1

say "-- 数据目录 --"
run install -d -m 0750 -o "$USER_NAME" -g omniapi "$DATA"

say ""
say "-- 防火墙 --"
# 服务监听在宿主机，nginx 在容器内经 host.docker.internal 回连。
# 与 8787/8790 同款：只放通 docker 网段，公网仍然进不来。
if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -q "$PORT"; then
        say "$PORT 规则已存在，跳过"
    else
        run ufw allow from 172.16.0.0/12 to any port "$PORT" proto tcp comment 'docker bridges only'
    fi
else
    say "注意: 未找到 ufw，请自行确认容器能访问宿主机 $PORT"
fi

say ""
say "-- systemd 单元 --"
if [ "$APPLY" = "1" ]; then
    install -m 0644 -o root -g root "$ROOT/ops/systemd/$UNIT" "/etc/systemd/system/$UNIT"
else
    say "（预演）install -m 0644 $ROOT/ops/systemd/$UNIT /etc/systemd/system/$UNIT"
fi
run systemctl daemon-reload
run systemctl enable "$UNIT"
run systemctl restart "$UNIT"

say ""
say "-- 自检 --"
if [ "$APPLY" = "1" ]; then
    sleep 2
    systemctl --no-pager --lines=0 status "$UNIT" || true
    say ""
    say "健康检查:"
    curl -s --max-time 10 "http://127.0.0.1:$PORT/healthz" || say "（健康检查失败，看 journalctl -u $UNIT）"
    say ""
else
    say "（预演）重启服务并检查 /healthz"
fi

say ""
if [ "$APPLY" = "1" ]; then
    say "完成。还需两步："
    say "1) nginx 加 /api/app/ 与 /api/sync/ 转发、/api/user/self 分流（见 docs/CLIENT_REMEDIATION_PLAN.md 第 3 节），reload nginx。"
    say "2) 若尚未部署：安装 omni-sync 服务（ops/sync），同步服务也在主站跑。"
else
    say "这是预演，什么都没有改。加 --apply 才会真的安装。"
fi
