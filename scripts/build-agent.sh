#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
export GOCACHE="$ROOT/work/go-cache" GOPATH="$ROOT/work/go-path"
if [ -z "${GO:-}" ]; then if [ -x "$ROOT/tools/go/bin/go" ]; then GO="$ROOT/tools/go/bin/go"; else GO=go; fi; fi
mkdir -p "$ROOT/build/agent"
cd "$ROOT/agent"
"$GO" fmt ./...
"$GO" test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 "$GO" build -trimpath -ldflags='-s -w -buildid=' -o "$ROOT/build/agent/router-agent" .
gzip -n -9 -c "$ROOT/build/agent/router-agent" > "$ROOT/build/agent/router-agent.gz"
shasum -a 256 "$ROOT/build/agent/router-agent" "$ROOT/build/agent/router-agent.gz"
wc -c "$ROOT/build/agent/router-agent" "$ROOT/build/agent/router-agent.gz"
