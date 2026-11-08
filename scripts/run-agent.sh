#!/bin/sh
set -eu
umask 077
export GOMEMLIMIT=32MiB GOGC=50
exec /tmp/codex-mihomo/router-agent >> /tmp/codex-mihomo/agent.log 2>&1
