#!/bin/sh
# Install the OmniAPI recharge request service.
#
# Dry run by default: prints exactly what it would change and touches nothing.
# Pass --apply to perform the changes. Idempotent - safe to re-run.
#
# What it does:
#   1. creates the unprivileged account the service runs as;
#   2. grants that account read access to the admin session and write access to
#      runtime/, where the append-only queue and the credit ledger live;
#   3. generates the admin-page password once, if it does not exist yet;
#   4. installs and starts the systemd unit, plus the FX preheat timer;
#   5. keeps the port closed to the public internet.
set -eu

ROOT=/opt/ai-gateway
GROUP=omniapi
ACCOUNT=omniapi-watch
SERVICE=omniapi-topup-watch
UNIT_SRC="$ROOT/ops/systemd/$SERVICE.service"
UNIT_DST="/etc/systemd/system/$SERVICE.service"
FX_SERVICE=omniapi-fx-refresh
FX_UNIT_SRC="$ROOT/ops/systemd/$FX_SERVICE.service"
FX_UNIT_DST="/etc/systemd/system/$FX_SERVICE.service"
FX_TIMER_SRC="$ROOT/ops/systemd/$FX_SERVICE.timer"
FX_TIMER_DST="/etc/systemd/system/$FX_SERVICE.timer"
METHODS_CONFIG="$ROOT/config/payment-methods.json"
SECRETS="$ROOT/.secrets/topup-admin.json"
PORT=8787

APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
run() {
    if [ "$APPLY" = "1" ]; then
        "$@"
    else
        printf '   would run: %s\n' "$*"
    fi
}

[ "$(id -u)" = "0" ] || { say "BLOCKED: 需要 root 运行"; exit 2; }
[ -d "$ROOT" ] || { say "BLOCKED: 找不到 $ROOT"; exit 2; }
[ -f "$UNIT_SRC" ] || { say "BLOCKED: 找不到 $UNIT_SRC"; exit 2; }
[ -f "$ROOT/.secrets/admin.json" ] || { say "BLOCKED: 缺少 .secrets/admin.json（服务用它发放兑换码）"; exit 2; }

if [ "$APPLY" = "0" ]; then
    say "DRY RUN：以下改动尚未执行。确认后加 --apply。"
fi

step "1/6 服务账号"
if getent group "$GROUP" >/dev/null 2>&1; then
    say "   组 $GROUP 已存在"
else
    run groupadd --system "$GROUP"
fi
if id -u "$ACCOUNT" >/dev/null 2>&1; then
    say "   账号 $ACCOUNT 已存在"
else
    run useradd --system --gid "$GROUP" --home-dir /nonexistent --shell /usr/sbin/nologin "$ACCOUNT"
fi

step "2/6 权限：只放开服务真正需要的读写"
# .secrets 目录需要组可进入；目录里其余文件保持 root 私有。
run chgrp "$GROUP" "$ROOT/.secrets"
run chmod 750 "$ROOT/.secrets"
run chgrp "$GROUP" "$ROOT/.secrets/admin.json"
run chmod 640 "$ROOT/.secrets/admin.json"
# setgid 让 runtime/ 下新文件的属组自动跟随，经营者脚本与服务都能读写。
run chgrp "$GROUP" "$ROOT/runtime"
run chmod 2770 "$ROOT/runtime"
for name in topup-ledger.json topup-requests.jsonl topup-quotes.jsonl fx-rates.json payment-settings.json payment-settings-audit.json admin-session-state.json; do
    if [ -f "$ROOT/runtime/$name" ]; then
        run chown "$ACCOUNT:$GROUP" "$ROOT/runtime/$name"
        run chmod 640 "$ROOT/runtime/$name"
    fi
done
# 收款方式由服务在每次请求时读取，必须对服务账号可读（经营者用 root 编辑）。
if [ -f "$METHODS_CONFIG" ]; then
    run chown "root:$GROUP" "$METHODS_CONFIG"
    run chmod 640 "$METHODS_CONFIG"
else
    say "   警告：缺少 $METHODS_CONFIG，收款方式将退回内置骨架（Payoneer 会因未配置链接而不可用）"
fi

# 上传二维码会完整解码并安全重编码；缺少 Pillow 时拒绝启动上传功能。
if python3 -c 'from PIL import Image' >/dev/null 2>&1; then
    say "   Pillow 图片解码器已安装"
else
    run apt-get update
    run apt-get install -y python3-pil
fi
# 汇率快照由预热定时器写出、由服务读取。若曾被 root 手动抓取过，文件会是
# root:root 0600，服务读不到就只能退回备用汇率。这里统一到共享属组。
if [ -f "$ROOT/runtime/fx-rates.json" ]; then
    run chgrp "$GROUP" "$ROOT/runtime/fx-rates.json"
    run chmod 640 "$ROOT/runtime/fx-rates.json"
fi

step "3/6 管理员网页口令"
if [ -f "$SECRETS" ]; then
    say "   已存在，保持不动：$SECRETS"
else
    if [ "$APPLY" = "1" ]; then
        OUT=$(python3 "$ROOT/ops/topup_watch/topup_watch.py" --init-secrets --secrets "$SECRETS")
        printf '%s\n' "$OUT"
        # 口令只生成一次，哈希无法反推。留一份 root 私有副本，否则经营者一旦
        # 没记下就只能重设。存进密码管理器后应删除该文件。
        printf '%s' "$OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["password"])' \
            > "$ROOT/.secrets/topup-admin-password.txt"
        chmod 600 "$ROOT/.secrets/topup-admin-password.txt"
        chgrp "$GROUP" "$SECRETS"
        chmod 640 "$SECRETS"
        say ""
        say "   口令副本：$ROOT/.secrets/topup-admin-password.txt（仅 root 可读）"
        say "   请存进密码管理器，然后删除该文件：rm $ROOT/.secrets/topup-admin-password.txt"
    else
        say "   would generate: $SECRETS（并打印一次口令）"
    fi
fi

step "4/6 systemd 单元"
run install -m 644 "$UNIT_SRC" "$UNIT_DST"
run systemctl daemon-reload
run systemctl enable --now "$SERVICE"

step "5/6 汇率快照预热定时器"
# 服务只在有人访问时才去拉行情，首个访客要等上游响应，并发还会把第三方端点打爆。
# 定时器每 5 分钟预热一次，报价才真正拿到近期报价。
run install -m 644 "$FX_UNIT_SRC" "$FX_UNIT_DST"
run install -m 644 "$FX_TIMER_SRC" "$FX_TIMER_DST"
run systemctl daemon-reload
run systemctl enable --now "$FX_SERVICE.timer"

step "6/6 端口只对容器网桥开放"
if command -v ufw >/dev/null 2>&1; then
    # 顺序很重要：容器回连宿主机走的是 INPUT 链，一条笼统的 deny 会把容器一起
    # 挡住（服务会 502）。必须先放行 docker 网段，再拒绝其余来源。
    if ufw status 2>/dev/null | grep -q "8787/tcp.*172.16.0.0/12"; then
        say "   已存在 docker 网段放行规则"
    else
        run ufw allow from 172.16.0.0/12 to any port "$PORT" proto tcp comment "docker bridges only"
    fi
    if ufw status 2>/dev/null | grep -qE "^$PORT/tcp +DENY"; then
        say "   已存在其它来源的拒绝规则"
    else
        run ufw deny "$PORT/tcp" comment "topup-watch stays private"
    fi
else
    say "   未安装 ufw，跳过（请确认云安全组不含 $PORT）"
fi

step "完成"
if [ "$APPLY" = "1" ]; then
    sleep 2
    systemctl is-active "$SERVICE" || true
    say ""
    say "自检："
    python3 "$ROOT/ops/topup_watch/topup_watch.py" --check
else
    say "以上为预演结果。执行：sh $0 --apply"
fi
