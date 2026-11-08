#!/bin/sh
# Offline build only: this script has no router/device/container operations.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
SOURCE=${MIHOMO_SOURCE:-"$ROOT/work/mihomo-source"}
if [ -z "${GO:-}" ]; then if [ -x "$ROOT/tools/go1268/bin/go" ]; then GO="$ROOT/tools/go1268/bin/go"; else GO=go; fi; fi
"$GO" version | grep -q "go1.26.8 " || { echo "Pinned core build requires Go 1.26.8" >&2; exit 1; }
ARCH=${UDP_TEST_ARCH:-arm64}
case "$ARCH" in arm64|amd64) ;; *) echo 'UDP_TEST_ARCH must be arm64 or amd64' >&2; exit 2 ;; esac
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=1
export GOCACHE="$ROOT/work/go-cache" GOPATH="$ROOT/work/go-path"
OUT="$ROOT/build/udp-rootfix"
mkdir -p "$OUT" "$ROOT/logs/udp-rootfix"
[ "$(git -C "$SOURCE" rev-parse HEAD)" = 88dcbf7f1614a67c3b36b848ee3592dfa92ada36 ] || { echo 'Wrong upstream commit' >&2; exit 1; }
cd "$SOURCE"
"$GO" test -race -count=1 -timeout 30s ./common/udpreply ./listener/tproxy
"$GO" vet ./common/udpreply ./listener/tproxy
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" "$GO" test -c -tags udp_integration -ldflags='-s -w' -o "$OUT/udpreply-linux-$ARCH.test" ./common/udpreply
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" "$GO" test -c -tags udp_integration -ldflags='-s -w' -o "$OUT/tproxy-linux-$ARCH.test" ./listener/tproxy
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 "$GO" test -c -tags udp_integration -ldflags='-s -w' -o "$OUT/udpreply-linux-armv7.test" ./common/udpreply
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 "$GO" test -c -tags udp_integration -ldflags='-s -w' -o "$OUT/tproxy-linux-armv7.test" ./listener/tproxy
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 "$GO" build -tags with_gvisor -trimpath -ldflags='-s -w -buildid= -X github.com/metacubex/mihomo/constant.Version=v1.19.32-udpfix-candidate' -o "$OUT/mihomo-linux-armv7-udpfix-candidate" .
cd "$ROOT/tests/udp-peer"
"$GO" test -race -count=1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" "$GO" build -trimpath -ldflags='-s -w -buildid=' -o "$OUT/udp-peer-linux-$ARCH" .
cd "$ROOT"
file "$OUT/mihomo-linux-armv7-udpfix-candidate" "$OUT/udpreply-linux-$ARCH.test" "$OUT/tproxy-linux-$ARCH.test"
shasum -a 256 "$OUT/mihomo-linux-armv7-udpfix-candidate" "$OUT/udpreply-linux-$ARCH.test" "$OUT/tproxy-linux-$ARCH.test" "$OUT/udp-peer-linux-$ARCH"
echo 'OFFLINE BUILD COMPLETE. Linux socket/peer delivery tests have NOT been executed.'
