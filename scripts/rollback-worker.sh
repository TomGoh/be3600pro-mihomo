#!/bin/sh
set -eu
RUN=/tmp/codex-mihomo
token=$(cat "$RUN/pending-token")
sleep 120
[ "$(cat "$RUN/pending-token" 2>/dev/null || true)" = "$token" ] || exit 0
[ "$(cat "$RUN/confirmed-token" 2>/dev/null || true)" != "$token" ] || exit 0
printf '%s NETWORK_ROLLBACK\n' "$(date +%s)" >> "$RUN/watchdog.log"
sh /data/codex-router/routerctl.sh disable
