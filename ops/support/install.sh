#!/bin/sh
# Install or update the OmniAPI live chat service.
#
#   sudo sh ops/support/install.sh           # 预演，只打印将要做什么
#   sudo sh ops/support/install.sh --apply   # 真的安装并启动
#
# It deliberately does not create any secret: the service shares the operator
# session secret with the recharge service, and the push credentials are the
# operator's to place at .secrets/support-notify.json when they pick a channel.
set -eu

APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

ROOT=/opt/ai-gateway
UNIT=omniapi-support.service
USER_NAME=omniapi-watch
EVENTS="$ROOT/runtime/support"
CONFIG="$ROOT/config/support.json"
SECRETS="$ROOT/.secrets/topup-admin.json"

say() { printf '%s\n' "$*"; }
run() {
    if [ "$APPLY" = "1" ]; then
        say "+ $*"
        "$@"
    else
        say "（预演）$*"
    fi
}

say "== OmniAPI 客服服务安装 =="
say "模式: $([ "$APPLY" = "1" ] && echo 应用 || echo 预演)"
say ""

# -- preconditions ---------------------------------------------------------
BLOCKED=0
for path in "$SECRETS" "$CONFIG" "$ROOT/ops/support/support_service.py" \
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
[ "$BLOCKED" = "1" ] && exit 1

# Nothing may hold the port before we restart.
if command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':8790 '; then
    say "注意: 8790 已有监听，安装会重启该服务"
fi

# -- files and directories -------------------------------------------------
run install -d -m 0750 -o "$USER_NAME" -g omniapi "$EVENTS"

say ""
say "-- 防火墙 --"
# 8787 已经有一条同样的规则；服务监听在宿主机上，nginx 在容器里经
# host.docker.internal 回连，没有这条规则容器侧会直接超时（表现像服务挂了）。
# 只放通 docker 网段，公网仍然进不来。
if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -q '8790'; then
        say "8790 规则已存在，跳过"
    else
        run ufw allow from 172.16.0.0/12 to any port 8790 proto tcp comment 'docker bridges only'
    fi
else
    say "注意: 未找到 ufw，请自行确认容器能访问宿主机 8790"
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
    sudo -u "$USER_NAME" /usr/bin/python3 "$ROOT/ops/support/support_service.py" --check || true
    say ""
    say "健康检查:"
    curl -s --max-time 10 "http://127.0.0.1:8790/healthz" || say "（健康检查失败，看 journalctl -u $UNIT）"
    say ""
else
    say "（预演）重启服务并跑 --check 与 /healthz"
fi

say ""
if [ "$APPLY" = "1" ]; then
    say "完成。记得：nginx 需要有 /brand/api/support/ 与 /brand/admin/api/support/ 两个 location，"
    say "并把 /opt/ai-gateway/nginx/https.conf 的改动同步到容器（见 docs/SUPPORT.md）。"
else
    say "这是预演，什么都没有改。加 --apply 才会真的安装。"
fi
