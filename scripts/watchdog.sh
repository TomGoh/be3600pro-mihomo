#!/bin/sh
set -u
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
failures=0
integrity_failures=0
round=0
while :; do
    if [ -f "$RUN/enabled" ]; then
        for family in iptables ip6tables; do
            file="/proc/sys/net/bridge/bridge-nf-call-$family"
            [ ! -f "$file" ] || printf '0\n' > "$file"
        done
        healthy=1
        curl -f -K "$DATA/core-curl.conf" --max-time 2 http://127.0.0.1:9090/version >/dev/null 2>&1 || healthy=0
        if [ -f "$DATA/agent-required" ]; then
            curl -f -K "$DATA/agent-curl.conf" --max-time 2 https://192.168.31.1:9443/api/status >/dev/null 2>&1 || healthy=0
        fi
        if [ "$healthy" = 1 ]; then failures=0; else failures=$((failures+1)); fi
        if [ "$failures" -ge 3 ]; then
            printf '%s SERVICE_UNHEALTHY_FALLBACK\n' "$(date +%s)" >> "$RUN/watchdog.log"
            sh "$DATA/routerctl.sh" disable
            failures=0
        fi
    else failures=0
    fi
    round=$((round+1))
    if [ "$round" -ge 5 ]; then
        round=0
        result=0
        sh "$DATA/health.sh" > "$RUN/interception-health.next" || result=$?
        mv "$RUN/interception-health.next" "$RUN/interception-health.json"
        if [ "$result" = 1 ]; then integrity_failures=$((integrity_failures+1));
        elif [ "$result" = 0 ]; then integrity_failures=0; fi
        if [ "$integrity_failures" -ge 2 ]; then
            printf '%s INTERCEPTION_INCOMPLETE_FALLBACK\n' "$(date +%s)" >> "$RUN/watchdog.log"
            sh "$DATA/routerctl.sh" disable
            integrity_failures=0
        fi
    fi
    for name in core agent; do
        file="$RUN/$name.log"
        if [ -f "$file" ] && [ "$(wc -c < "$file")" -gt 1048576 ]; then cp "$file" "$file.1"; : > "$file"; fi
    done
    sleep 3
done
