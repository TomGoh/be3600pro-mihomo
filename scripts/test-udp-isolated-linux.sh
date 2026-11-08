#!/bin/sh
# Future Linux acceptance only. Do not run while the user is gaming.
# Creates two OWN containers on an internal dual-stack network; no downloads.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
OUT="$ROOT/build/udp-rootfix"
CONTEXT=${UDP_TEST_DOCKER_CONTEXT:-default}
NAME="cdx-udp-proof-$(date +%s)-$$"
NET_ID= PEER_ID= SENDER_ID=
d() { docker --context "$CONTEXT" "$@"; }
cleanup() {
    [ -z "$SENDER_ID" ] || d rm -f "$SENDER_ID" >/dev/null 2>&1 || true
    [ -z "$PEER_ID" ] || d rm -f "$PEER_ID" >/dev/null 2>&1 || true
    [ -z "$NET_ID" ] || d network rm "$NET_ID" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
host_arch=$(d info --format '{{.Architecture}}')
case "$host_arch" in aarch64|arm64) ARCH=arm64 ;; x86_64|amd64) ARCH=amd64 ;; *) echo 'Unsupported Linux test architecture' >&2; exit 2 ;; esac
for file in "udpreply-linux-$ARCH.test" "tproxy-linux-$ARCH.test" "udp-peer-linux-$ARCH"; do
    [ -f "$OUT/$file" ] || { echo "Missing offline artifact: $file" >&2; exit 1; }
done
LOG="$ROOT/logs/udp-rootfix/$NAME"
mkdir -p "$LOG"
# A scratch image contains no network/download tools and no third-party runtime.
IMAGE=cdx-be3600-udp-test:local
if ! d image inspect "$IMAGE" >/dev/null 2>&1; then
    python3 -c 'import sys,tarfile; tarfile.open(fileobj=sys.stdout.buffer,mode="w|").close()' | d import - "$IMAGE" > "$LOG/image-id.txt"
fi
NET_ID=$(d network create --internal --ipv6 --subnet 192.0.2.0/29 --subnet 2001:db8:7::/64 --label codex.be3600.udp-test=true "$NAME")
# The simulated source (.6 / ::9) is UNASSIGNED, hence nonlocal to the sender.
# Keeping it on-link avoids confusing Docker internal-network source filtering
# or reverse-path filtering with the behavior under test. Off-link/MTU behavior
# remains a separate acceptance gate; this is not a public-network simulation.
for suite in udpreply tproxy; do
    PEER_ID=$(d create --pull=never --name "$NAME-peer-$suite" --network "$NET_ID" --ip 192.0.2.2 --ip6 2001:db8:7::2 --read-only --cap-drop ALL --pids-limit 128 --memory 128m --cpus 1 --mount "type=bind,source=$OUT,target=/tests,readonly" --entrypoint "/tests/udp-peer-linux-$ARCH" "$IMAGE")
    d start "$PEER_ID" >/dev/null
    count=0
    until d logs "$PEER_ID" 2>&1 | grep -q UDP_PEER_READY; do
        count=$((count+1))
        if [ "$count" -ge 50 ]; then d logs "$PEER_ID" > "$LOG/$suite-peer.log" 2>&1; echo 'Peer failed to become ready' >&2; exit 1; fi
        sleep 0.2
    done
    SENDER_ID=$(d create --pull=never --name "$NAME-sender-$suite" --network "$NET_ID" --ip 192.0.2.3 --ip6 2001:db8:7::3 --read-only --cap-drop ALL --cap-add NET_RAW --pids-limit 128 --memory 256m --cpus 1 --mount "type=bind,source=$OUT,target=/tests,readonly" --env UDP_REPLY_PEER4=192.0.2.2:40000 --env 'UDP_REPLY_PEER6=[2001:db8:7::2]:40000' --entrypoint "/tests/$suite-linux-$ARCH.test" "$IMAGE" -test.v -test.timeout=25s)
    d start "$SENDER_ID" >/dev/null
    sender_code=$(d wait "$SENDER_ID")
    peer_code=$(d wait "$PEER_ID")
    d logs "$SENDER_ID" > "$LOG/$suite-sender.log" 2>&1
    d logs "$PEER_ID" > "$LOG/$suite-peer.log" 2>&1
    printf '%s sender=%s peer=%s\n' "$suite" "$sender_code" "$peer_code" | tee -a "$LOG/exit-codes.txt"
    [ "$sender_code" = 0 ] && [ "$peer_code" = 0 ] || { echo 'Linux UDP acceptance FAILED; retain logs' >&2; exit 1; }
    d rm "$SENDER_ID" "$PEER_ID" >/dev/null
    SENDER_ID= PEER_ID=
done
echo "PASS: both Linux suites and independent IPv4/IPv6 peers; evidence $LOG"
