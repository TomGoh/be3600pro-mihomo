#!/bin/sh
# Standalone diagnostic guard; not installed or started by normal service setup.
set -u
live=0
run=
token=
cleanup=
clock=/proc/uptime
lease_seconds=120
max_seconds=2700
poll_seconds=10
usage() { echo 'Usage: stability-guard.sh --run-live --run-dir DIR --token OWNER --cleanup-script FILE [--lease-seconds N --max-seconds N --poll-seconds N --clock-file FILE]' >&2; exit 2; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --run-live) live=1; shift ;;
        --run-dir|--token|--cleanup-script|--clock-file|--lease-seconds|--max-seconds|--poll-seconds)
            [ "$#" -ge 2 ] || usage
            case "$1" in
                --run-dir) run=$2 ;; --token) token=$2 ;; --cleanup-script) cleanup=$2 ;;
                --clock-file) clock=$2 ;; --lease-seconds) lease_seconds=$2 ;;
                --max-seconds) max_seconds=$2 ;; --poll-seconds) poll_seconds=$2 ;;
            esac
            shift 2 ;;
        *) usage ;;
    esac
done
canonical() {
    case "$1" in ''|*[!0-9]*|0[0-9]*) return 1 ;; esac
    [ "${#1}" -le 9 ]
}
[ "$live" = 1 ] && [ -n "$token" ] && [ -d "$run" ] && [ -f "$cleanup" ] && [ -r "$clock" ] || usage
for value in "$lease_seconds" "$max_seconds" "$poll_seconds"; do canonical "$value" && [ "$value" -gt 0 ] || usage; done
[ "$lease_seconds" -le 600 ] && [ "$max_seconds" -le 3600 ] && [ "$poll_seconds" -le 30 ] || usage
now=$(cut -d . -f 1 "$clock")
canonical "$now" || usage
started=$now
last=$now
first=1
while [ "$(cat "$run/stability-token" 2>/dev/null)" = "$token" ]; do
    now=$(cut -d . -f 1 "$clock")
    canonical "$now" && [ "$now" -ge "$started" ] || usage
    lease=$(cat "$run/stability-lease" 2>/dev/null || true)
    if canonical "$lease" && [ "$lease" -le "$now" ] && [ "$lease" -ge "$last" ]; then last=$lease; fi
    # A stale initial heartbeat is meaningful, including zero. Never accept a
    # future timestamp; empty/malformed reads retain the last known timestamp.
    if [ "$first" = 1 ] && canonical "$lease" && [ "$lease" -lt "$started" ]; then last=$lease; fi
    first=0
    if [ "$((now-last))" -ge "$lease_seconds" ] || [ "$((now-started))" -ge "$max_seconds" ]; then
        [ "$(cat "$run/stability-token" 2>/dev/null)" = "$token" ] || exit 0
        printf '%s LEASE_OR_DEADLINE_EXPIRED\n' "$now" >> "$run/stability-guard.log"
        sh "$cleanup" disable || exit 1
        if [ "$(cat "$run/stability-token" 2>/dev/null)" = "$token" ]; then
            rm -f "$run/stability-token" "$run/stability-lease"
        fi
        exit 0
    fi
    sleep "$poll_seconds"
done
