#!/bin/sh
# Remove interception/autostart; preserve owner configuration, backups and SSH.
set -eu
DATA=/data/codex-router
if [ "${1:-}" != --remove-service ]; then
    echo 'Usage: sh uninstall.sh --remove-service'
    echo 'Stops proxy services and removes only their firewall boot include.'
    echo 'Keeps /data/codex-router configuration/backups and current LAN SSH.'
    exit 0
fi
sh "$DATA/routerctl.sh" disable
for name in rollback bootstrap watchdog core agent; do
    sh /etc/rc.common "$DATA/$name.init" stop || true
done
uci -q delete firewall.codex_mihomo || true
uci commit firewall
echo 'Proxy removed from runtime and boot. Private configuration/backups retained.'
echo 'Current key-only LAN SSH is retained; it will not restart through this hook.'
