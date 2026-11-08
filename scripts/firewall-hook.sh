#!/bin/sh
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
mkdir -p "$RUN"
chmod 700 "$RUN"
sh "$DATA/routerctl.sh" acl || exit 1
# Firewall reload may flush interception. Clear stale runtime state before async reapply.
if [ -f "$RUN/enabled" ]; then sh "$DATA/routerctl.sh" disable-runtime; fi
sh /etc/rc.common "$DATA/watchdog.init" start
sh /etc/rc.common "$DATA/bootstrap.init" start
