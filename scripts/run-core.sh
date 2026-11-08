#!/bin/sh
set -eu
umask 077
mkdir -p /tmp/codex-mihomo
export GOMEMLIMIT=160MiB
export GOGC=50
export SAFE_PATHS=/data/codex-router
exec /tmp/mihomo -d /tmp/codex-mihomo -f /data/codex-router/config.json >> /tmp/codex-mihomo/core.log 2>&1
