#!/bin/sh
set -eu
umask 077
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
mkdir -p "$RUN"
mkdir "$RUN/bootstrap.lock" 2>/dev/null || exit 0
trap 'rmdir "$RUN/bootstrap.lock"' EXIT
fail() { printf '%s\n' "$1" > "$RUN/bootstrap-error"; exit 1; }
if [ ! -x "$RUN/router-agent" ]; then
    expected=$(cat "$DATA/agent.sha256")
    actual=$(sha256sum "$DATA/router-agent.gz" | cut -d ' ' -f 1)
    [ "$actual" = "$expected" ] || fail '管理程序校验失败，请从 Mac 恢复'
    gzip -dc "$DATA/router-agent.gz" > "$RUN/router-agent.next"
    chmod 700 "$RUN/router-agent.next"
    mv "$RUN/router-agent.next" "$RUN/router-agent"
fi
sh /etc/rc.common "$DATA/agent.init" start
sh /etc/rc.common "$DATA/watchdog.init" start
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
        dropbear -s -j -k -r "$DATA/ssh-host-rsa" -p 192.168.31.1:2222 -P /tmp/codex-dropbear.pid
    fi
fi
. "$DATA/core-release.sh"
if [ ! -x /tmp/mihomo ] || [ "$(sha256sum /tmp/mihomo | cut -d ' ' -f 1)" != "$CORE_BINARY_SHA" ]; then
    archive="$RUN/mihomo-download.gz"
    # Official release API; direct HTTPS bootstrap needs no running proxy.
    if ! curl -4 -fLsS --connect-timeout 10 --max-time 90 --retry 1 --retry-delay 3 -H 'Accept: application/octet-stream' -o "$archive" "$BASE_URL"; then
        rm -f "$archive"; fail '内核下载失败，请在 App 设置中使用离线恢复'
    fi
    actual=$(sha256sum "$archive" | cut -d ' ' -f 1)
    if [ "$actual" != "$BASE_ARCHIVE_SHA" ]; then
        rm -f "$archive"; fail '内核下载校验失败，请使用离线恢复'
    fi
    gzip -dc "$archive" > /tmp/mihomo.base
    rm -f "$archive"
    actual=$(sha256sum /tmp/mihomo.base | cut -d ' ' -f 1)
    if [ "$actual" != "$BASE_BINARY_SHA" ]; then
        rm -f /tmp/mihomo.base; fail '内核解压校验失败，请使用离线恢复'
    fi
    if ! "$RUN/router-agent" -apply-core-patch; then rm -f /tmp/mihomo.base; fail 'UDP修复补丁校验或恢复失败，请使用离线恢复'; fi
    rm -f /tmp/mihomo.base
fi
count=0
until curl -f -K "$DATA/agent-curl.conf" --max-time 2 https://192.168.31.1:9443/api/status >/dev/null 2>&1; do
    count=$((count+1)); [ "$count" -lt 30 ] || fail '管理服务尚未就绪，代理保持关闭'; sleep 2
done
sh "$DATA/routerctl.sh" core-start || fail '核心未能启动，请在 App 中检查状态'
rm -f "$RUN/bootstrap-error"
if [ "$(cat "$DATA/desired-enabled" 2>/dev/null || echo 0)" = 1 ]; then
    sh "$DATA/routerctl.sh" enable || fail '节点测试失败，代理保持关闭'
fi
