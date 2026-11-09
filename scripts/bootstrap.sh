#!/bin/sh
set -eu
umask 077
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
mkdir -p "$RUN"
# flock is released by the kernel after crash/PID reuse. Migrate the legacy
# empty lock directory only; never delete another process's lock file.
rmdir "$RUN/bootstrap.lock" 2>/dev/null || true
exec 8> "$RUN/bootstrap.lock"
flock -n -x 8 || exit 0
printf '%s\n' "$$" > "$RUN/bootstrap-owner"
trap 'rm -f "$RUN/bootstrap-owner"' EXIT
CDX_EXPECT_EPOCH=$(cat "$DATA/intent-generation" 2>/dev/null || true)
export CDX_EXPECT_EPOCH
printf 'prepare\n' > "$RUN/bootstrap-stage"
fail() { printf '%s\n' "$1" > "$RUN/bootstrap-error"; exit 1; }
if [ ! -x "$RUN/router-agent" ]; then
    expected=$(cat "$DATA/agent.sha256")
    actual=$(sha256sum "$DATA/router-agent.gz" | cut -d ' ' -f 1)
    [ "$actual" = "$expected" ] || fail '管理程序校验失败，请从 Mac 恢复'
    gzip -dc "$DATA/router-agent.gz" > "$RUN/router-agent.next"
    chmod 700 "$RUN/router-agent.next"
    mv "$RUN/router-agent.next" "$RUN/router-agent"
fi
sh /etc/rc.common "$DATA/agent.init" start 8>&-
sh /etc/rc.common "$DATA/watchdog.init" start 8>&-
count=0
until ip -4 addr show br-lan 2>/dev/null | grep -q '192.168.31.1/'; do
    count=$((count+1)); [ "$count" -lt 60 ] || fail '家庭网络接口尚未就绪'; sleep 1
done
# Key-only maintenance is LAN-bound, and the original authorized_keys are retained.
if [ -s "$DATA/maintenance.pub" ] && [ -s "$DATA/ssh-host-rsa" ]; then
    mkdir -p /etc/dropbear
    key=$(cat "$DATA/maintenance.pub")
    grep -Fqx "$key" /etc/dropbear/authorized_keys 2>/dev/null || printf '%s\n' "$key" >> /etc/dropbear/authorized_keys
    chmod 600 /etc/dropbear/authorized_keys
    if [ ! -s /tmp/codex-dropbear.pid ] || ! kill -0 "$(cat /tmp/codex-dropbear.pid)" 2>/dev/null; then
        dropbear -s -j -k -r "$DATA/ssh-host-rsa" -p 192.168.31.1:2222 -P /tmp/codex-dropbear.pid 8>&-
    fi
fi
. "$DATA/core-release.sh"
if [ ! -x /tmp/mihomo ] || [ "$(sha256sum /tmp/mihomo | cut -d ' ' -f 1)" != "$CORE_BINARY_SHA" ]; then
    archive="$RUN/mihomo-download.gz"
    . "$DATA/bootstrap-download.sh"
    result=0
    bootstrap_fetch "$archive" || result=$?
    [ "$result" != 1 ] || fail '内核下载重试已用尽，请在 App 中重试或离线恢复'
    # Coordinate the final replacement with an offline App upload, not its download.
    exec 7> "$RUN/core-install.lock"
    flock -x -w 60 7 || fail '核心正在恢复，请稍后重试'
    if [ ! -x /tmp/mihomo ] || [ "$(sha256sum /tmp/mihomo | cut -d ' ' -f 1)" != "$CORE_BINARY_SHA" ]; then
        printf 'verify and restore\n' > "$RUN/bootstrap-stage"
        gzip -dc "$archive" > /tmp/mihomo.base
        rm -f "$archive"
        actual=$(sha256sum /tmp/mihomo.base | cut -d ' ' -f 1)
        if [ "$actual" != "$BASE_BINARY_SHA" ]; then
            rm -f /tmp/mihomo.base; fail '内核解压校验失败，请使用离线恢复'
        fi
        if ! "$RUN/router-agent" -apply-core-patch; then rm -f /tmp/mihomo.base; fail 'UDP补丁恢复失败，请使用离线恢复'; fi
        rm -f /tmp/mihomo.base
    fi
    rm -f "$archive"
    flock -u 7
    exec 7>&-

fi
count=0
until curl -f -K "$DATA/agent-curl.conf" --max-time 2 https://192.168.31.1:9443/api/status >/dev/null 2>&1; do
    count=$((count+1)); [ "$count" -lt 30 ] || fail '管理服务尚未就绪，代理保持关闭'; sleep 2
done
printf 'start core\n' > "$RUN/bootstrap-stage"
if ! curl -f -K "$DATA/core-curl.conf" --max-time 2 http://127.0.0.1:9090/version >/dev/null 2>&1; then
 sh "$DATA/routerctl.sh" core-start 8>&- || fail '核心未能启动，请在 App 中检查状态'
fi
rm -f "$RUN/bootstrap-error"
if [ ! -f "$RUN/enabled" ] && [ "$(cat "$DATA/desired-enabled" 2>/dev/null || echo 0)" = 1 ]; then
    sh "$DATA/routerctl.sh" enable 8>&- || fail '节点测试失败，代理保持关闭'
fi

printf 'ready\n' > "$RUN/bootstrap-stage"
